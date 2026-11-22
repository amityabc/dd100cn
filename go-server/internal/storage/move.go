package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
)

type movePlan struct {
	source, target *Asset
	destination    uint16
	merge, split   bool
}

func planMove(c Character, in Inventory, r MoveRequest) (movePlan, error) {
	var p movePlan
	if r.ActorIndex != math.MaxUint32 || r.State0 != 0 || r.State1 != 0 ||
		r.SourceList != 0 && r.SourceList != 3 || r.DestinationList != 0 && r.DestinationList != 3 ||
		r.SourceList == r.DestinationList && r.SourceSlot == r.DestinationSlot {
		return p, ErrInvalid
	}
	rows := map[[2]uint16]*Asset{}
	for i := range in.Items {
		a := &in.Items[i]
		rows[[2]uint16{uint16(a.Location), a.Slot}] = a
	}
	get := func(list uint8, slot uint16) *Asset { return rows[[2]uint16{uint16(list), slot}] }
	p.source = get(r.SourceList, r.SourceSlot)
	p.destination = r.DestinationSlot
	auto := p.destination == math.MaxUint16
	if auto {
		if p.source == nil || r.DestinationList != 0 || r.DestinationTemplate != 0 {
			return p, ErrInvalid
		}
		t, err := moveTemplate(p.source)
		if err != nil {
			return p, err
		}
		free, merge := -1, -1
		for i := t.FirstBagSlot; i <= t.bagEnd(in.BagExpansion); i++ {
			dst := get(r.DestinationList, i)
			if dst == nil {
				if free < 0 {
					free = int(i)
				}
				continue
			}
			if r.Count > 0 && t.Kind == 2 && dst.UID != p.source.UID && dst.Count <= t.StackLimit && r.Count <= t.StackLimit-dst.Count && stacks(p.source, dst) {
				merge = int(i)
				break
			}
		}
		if merge >= 0 {
			free = merge
		}
		if free < 0 {
			return p, ErrCapacity
		}
		p.destination = uint16(free)
	}
	p.target = get(r.DestinationList, p.destination)
	id := func(a *Asset) uint32 {
		if a == nil {
			return 0
		}
		return a.TemplateID
	}
	if id(p.source) != r.SourceTemplate || !auto && id(p.target) != r.DestinationTemplate || p.source == nil && p.target == nil ||
		r.DestinationStack != 0 && (p.target == nil || r.DestinationStack != p.target.Count) {
		return p, ErrInvalid
	}
	if p.source != nil {
		if p.source.CharacterID != c.ID || r.Count > p.source.Count {
			return p, ErrInvalid
		}
		if err := placeOrdinary(c, p.source, r.DestinationList, p.destination, in.BagExpansion); err != nil {
			return p, err
		}
	}
	if p.target != nil {
		if p.target.CharacterID != c.ID {
			return p, ErrInvalid
		}
		attrs, err := movementAttributes(p.target)
		if err != nil || attrs.Locked {
			return p, ErrInvalid
		}
	}
	if r.Count == 0 {
		if p.target != nil {
			if err := placeOrdinary(c, p.target, r.SourceList, r.SourceSlot, in.BagExpansion); err != nil {
				return p, err
			}
		}
		return p, nil
	}
	if p.source == nil {
		if r.Count != 1 || r.SourceList != 0 || r.DestinationList != 3 {
			return p, ErrInvalid
		}
		if _, err := p.target.WornEquipment(c); err != nil {
			return p, err
		}
		return p, placeOrdinary(c, p.target, r.SourceList, r.SourceSlot, in.BagExpansion)
	}
	t, err := moveTemplate(p.source)
	if err != nil {
		return p, err
	}
	if t.Kind == 2 && (p.target == nil || stacks(p.source, p.target)) {
		var n uint32
		if p.target != nil {
			n = p.target.Count
		}
		if n > t.StackLimit || r.Count > t.StackLimit-n {
			return p, ErrCapacity
		}
		p.merge, p.split = p.target != nil, p.target == nil && r.Count < p.source.Count
		return p, nil
	}
	if r.Count != p.source.Count {
		return p, ErrInvalid
	}
	if p.target != nil {
		err = placeOrdinary(c, p.target, r.SourceList, r.SourceSlot, in.BagExpansion)
	}
	return p, err
}

// checkMoveWeight uses the same per-template comparison as the original.
// A reduction or a bag-only rearrangement can proceed without a PVF lookup.
func checkMoveWeight(c Character, in Inventory, p movePlan, r MoveRequest, weights map[uint32]int64) error {
	if r.SourceList == r.DestinationList {
		return nil
	}
	deltas := map[uint32]int64{}
	if p.source != nil {
		d := int64(p.source.Count)
		if r.SourceList == 0 {
			d = -d
		}
		deltas[p.source.TemplateID] += d
	}
	if p.target != nil {
		d := int64(p.target.Count)
		if r.DestinationList == 0 {
			d = -d
		}
		deltas[p.target.TemplateID] += d
	}
	return checkBagWeight(c, in, deltas, weights)
}

// checkBagWeight applies the original carrying rule whenever an operation adds
// a template to the character's ordinary bag. A reduction remains possible
// for a historical character that is already over its limit.
func checkBagWeight(c Character, in Inventory, deltas map[uint32]int64, weights map[uint32]int64) error {
	grows := false
	for _, delta := range deltas {
		if delta > 0 {
			grows = true
		}
	}
	if !grows {
		return nil
	}
	counts := map[uint32]int64{}
	for _, a := range in.Items {
		if a.Location == 0 && a.CharacterID == c.ID {
			counts[a.TemplateID] += int64(a.Count)
		}
	}
	var oldWeight, newWeight int64
	for id := range deltas {
		if _, ok := counts[id]; !ok {
			counts[id] = 0
		}
	}
	for id, n := range counts {
		unit, ok := weights[id]
		if !ok || unit < 0 || unit > math.MaxInt32 {
			return ErrInvalid
		}
		next := n + deltas[id]
		if next < 0 || unit != 0 && (n > (math.MaxInt64-oldWeight)/unit || next > (math.MaxInt64-newWeight)/unit) {
			return ErrInvalid
		}
		oldWeight += n * unit
		newWeight += next * unit
	}
	var stats struct{ Values map[string]int64 }
	if err := json.Unmarshal(c.Stats, &stats); err != nil {
		return err
	}
	base := stats.Values["inventory_limit"]
	extra := int64(in.BagExpansion/8) * 10000
	if base < 0 || base > math.MaxInt64-extra {
		return ErrInvalid
	}
	if newWeight > oldWeight && newWeight > base+extra {
		return ErrCapacity
	}
	return nil
}

func writeMove(ctx context.Context, tx *sql.Tx, account string, c Character, p movePlan, r MoveRequest) error {
	if _, err := tx.ExecContext(ctx, "SET CONSTRAINTS s9a1.asset_position DEFERRED"); err != nil {
		return err
	}
	if p.merge || p.split {
		if p.merge {
			if _, err := tx.ExecContext(ctx, "UPDATE s9a1.assets SET count=count+$2 WHERE instance_id=$1", p.target.UID, r.Count); err != nil {
				return err
			}
		} else {
			_, err := tx.ExecContext(ctx, `INSERT INTO s9a1.assets(instance_id,account,character_id,source,template_id,template_json,weapon_facts,count,location,slot,attributes_json,core,detail)
				SELECT replace(gen_random_uuid()::text,'-',''),account,character_id,source,template_id,template_json,weapon_facts,$2,$3,$4,attributes_json,core,detail FROM s9a1.assets WHERE instance_id=$1`,
				p.source.UID, r.Count, r.DestinationList, p.destination)
			if err != nil {
				return err
			}
		}
		if r.Count == p.source.Count {
			_, err := tx.ExecContext(ctx, "DELETE FROM s9a1.assets WHERE instance_id=$1", p.source.UID)
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE s9a1.assets SET count=count-$2 WHERE instance_id=$1", p.source.UID, r.Count)
		return err
	}
	move := func(a *Asset, list uint8, slot uint16) error {
		if a == nil {
			return nil
		}
		_, err := tx.ExecContext(ctx, "UPDATE s9a1.assets SET location=$4,slot=$5 WHERE account=$1 AND character_id=$2 AND instance_id=$3", account, c.ID, a.UID, list, slot)
		return err
	}
	if err := move(p.source, r.DestinationList, p.destination); err != nil {
		return err
	}
	return move(p.target, r.SourceList, r.SourceSlot)
}

// Move performs a native C19 ordinary bag/worn transition. The host gates
// gameplay scene access. Weights come from the matching resource catalog.
func (s *Store) Move(ctx context.Context, l Lease, id uint16, rev int64, req MoveRequest, weights ...map[uint32]int64) (MoveOutcome, error) {
	var weight map[uint32]int64
	if len(weights) > 1 {
		return MoveOutcome{}, ErrInvalid
	}
	if len(weights) == 1 {
		weight = weights[0]
	}
	return writeTx(ctx, s.db, func(tx *sql.Tx) (MoveOutcome, error) {
		var out MoveOutcome
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return out, err
		}
		if r.SelectedID != id || r.Phase != TownReady {
			return out, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, id))
		if err != nil {
			return out, err
		}
		out.SourceList, out.SourceSlot, out.DestinationList, out.Count = req.SourceList, req.SourceSlot, req.DestinationList, req.Count
		var saved []byte
		err = tx.QueryRowContext(ctx, `SELECT body,destination FROM s9a1.last_inventory_move
			WHERE account=$1 AND owner=$2 AND epoch=$3 AND character_id=$4 AND revision=$5 AND account_revision=$6 AND sequence=$7`,
			l.Account, l.Owner, l.Epoch, id, c.Revision, r.AssetRevision, req.Sequence).Scan(&saved, &out.DestinationSlot)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		body := req.NativeBody()
		out.Replayed = err == nil && bytes.Equal(body, saved)
		in, err := readInventory(ctx, tx, l.Account, r)
		if err != nil {
			return out, err
		}
		if !out.Replayed {
			if c.Revision != rev {
				return out, ErrConflict
			}
			p, err := planMove(c, in, req)
			if err != nil {
				return out, err
			}
			if err = checkMoveWeight(c, in, p, req, weight); err != nil {
				return out, err
			}
			if err = writeMove(ctx, tx, l.Account, c, p, req); err != nil {
				return out, err
			}
			out.DestinationSlot = p.destination
			c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET revision=revision+1 WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns, l.Account, id, rev))
			if errors.Is(err, sql.ErrNoRows) {
				return out, ErrConflict
			}
			if err != nil {
				return out, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO s9a1.last_inventory_move(account,owner,epoch,character_id,revision,account_revision,sequence,body,destination)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(account) DO UPDATE SET owner=excluded.owner,epoch=excluded.epoch,
				character_id=excluded.character_id,revision=excluded.revision,account_revision=excluded.account_revision,sequence=excluded.sequence,body=excluded.body,destination=excluded.destination`,
				l.Account, l.Owner, l.Epoch, id, c.Revision, r.AssetRevision, req.Sequence, body, p.destination)
			if err != nil {
				return out, err
			}
			in, err = readInventory(ctx, tx, l.Account, r)
			if err != nil {
				return out, err
			}
		}
		out.Inventory = in
		for _, a := range in.Items {
			if a.Location == 3 {
				e, err := a.WornEquipment(c)
				if err != nil {
					return out, err
				}
				c.Worn = append(c.Worn, e)
			}
		}
		out.Character = c
		return out, nil
	})
}
