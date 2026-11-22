package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"
)

type QuestMaterial struct{ Template, Count uint32 }

type QuestGrant struct {
	Asset
	Kind        byte
	First, Last uint16
	StackLimit  uint32
	QuickSlots  bool
}

// QuestLoot applies material debits before each corresponding claim's grants.
// Joint claims call this in order and share a single committed inventory.
func QuestLoot(in Inventory, materials []QuestMaterial, grants []QuestGrant, gold int64, now time.Time) (Inventory, error) {
	in.Items = slices.Clone(in.Items)
	if gold < 0 || gold > math.MaxUint32 {
		return in, ErrFunds
	}
	in.Gold = gold
	slices.SortStableFunc(in.Items, func(a, b Asset) int { return int(a.Slot) - int(b.Slot) })
	for _, m := range materials {
		left := m.Count
		for i := range in.Items {
			a := &in.Items[i]
			if a.Location != 0 || a.TemplateID != m.Template || a.Count == 0 {
				continue
			}
			attrs, err := movementAttributes(a)
			if err != nil || attrs.Locked || (attrs.ExpiresAt != nil && *attrs.ExpiresAt <= now.Unix()) {
				return in, ErrInvalid
			}
			take := min(left, a.Count)
			left -= take
			a.Count -= take
			if left == 0 {
				break
			}
		}
		if left != 0 {
			return in, ErrInvalid
		}
	}
	in.Items = slices.DeleteFunc(in.Items, func(a Asset) bool { return a.Count == 0 })
	for _, g := range grants {
		if g.Count == 0 || g.StackLimit == 0 || g.First > g.Last {
			return in, ErrInvalid
		}
		free := make([]uint16, 0, int(g.Last-g.First)+7)
		if g.QuickSlots {
			free = append(free, 3, 4, 5, 6, 7, 8)
		}
		for slot := g.First; ; slot++ {
			free = append(free, slot)
			if slot == g.Last {
				break
			}
		}
		left := g.Count
		slices.SortStableFunc(in.Items, func(a, b Asset) int { return int(a.Slot) - int(b.Slot) })
		occupied := map[uint16]bool{}
		for i := range in.Items {
			a := &in.Items[i]
			if a.Location != g.Location {
				continue
			}
			occupied[a.Slot] = true
			if left == 0 || g.Kind != 2 || a.TemplateID != g.TemplateID || a.Count >= g.StackLimit || a.Slot > g.Last || a.Source != g.Source {
				continue
			}
			same := stacks(a, &g.Asset)
			if g.Source == "shared" {
				var err error
				same, err = compatibleMaterial(*a, g.Asset)
				if err != nil {
					return in, err
				}
			}
			if same {
				take := min(left, g.StackLimit-a.Count)
				left -= take
				a.Count += take
			}
		}
		for _, slot := range free {
			if left == 0 {
				break
			}
			if occupied[slot] {
				continue
			}
			a := g.Asset
			var key [16]byte
			if _, err := rand.Read(key[:]); err != nil {
				return in, err
			}
			key[6] = key[6]&15 | 64
			key[8] = key[8]&63 | 128
			a.UID, a.Slot, a.Count = hex.EncodeToString(key[:]), slot, min(left, g.StackLimit)
			left -= a.Count
			in.Items = append(in.Items, a)
		}
		if left != 0 {
			return in, ErrCapacity
		}
	}
	slices.SortStableFunc(in.Items, func(a, b Asset) int {
		if a.Location != b.Location {
			return int(a.Location) - int(b.Location)
		}
		return int(a.Slot) - int(b.Slot)
	})
	return in, nil
}

type questGrantResult struct {
	InstanceId string
	TemplateId uint32
	Location   byte
	Slot       uint16
	Granted    uint32
}

func QuestGranted(before, after Inventory) json.RawMessage {
	prev := make(map[string]uint32, len(before.Items))
	for _, a := range before.Items {
		prev[a.UID] = a.Count
	}
	wire := []questGrantResult{}
	for _, a := range after.Items {
		if a.Count <= prev[a.UID] {
			continue
		}
		id := a.UID
		if len(id) == 32 {
			id = id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
		}
		wire = append(wire, questGrantResult{id, a.TemplateID, a.Location, a.Slot, a.Count - prev[a.UID]})
	}
	raw, _ := json.Marshal(wire)
	return raw
}

func updateQuestInventory(ctx context.Context, tx *sql.Tx, l Lease, c Character, before, after Inventory, weights map[uint32]int64) (int64, error) {
	delta := map[uint32]int64{}
	for _, a := range before.Items {
		if a.CharacterID == c.ID && a.Location == 0 {
			delta[a.TemplateID] -= int64(a.Count)
		}
	}
	for _, a := range after.Items {
		if a.CharacterID == c.ID && a.Location == 0 {
			delta[a.TemplateID] += int64(a.Count)
		}
	}
	if err := checkBagWeight(c, before, delta, weights); err != nil {
		if errors.Is(err, ErrCapacity) {
			return 0, ErrInvalid
		}
		return 0, err
	}
	return writeQuestInventory(ctx, tx, l, c, before, after)
}

func writeQuestInventory(ctx context.Context, tx *sql.Tx, l Lease, c Character, before, after Inventory) (int64, error) {
	old := make(map[string]Asset, len(before.Items))
	remaining := make(map[string]Asset, len(after.Items))
	for _, a := range before.Items {
		old[a.UID] = a
	}
	for _, a := range after.Items {
		remaining[a.UID] = a
	}
	sharedChanged := false
	for _, a := range before.Items {
		if _, exists := remaining[a.UID]; !exists {
			if _, err := tx.ExecContext(ctx, "DELETE FROM s9a1.assets WHERE account=$1 AND instance_id=$2", l.Account, a.UID); err != nil {
				return 0, err
			}
			sharedChanged = sharedChanged || a.CharacterID == 0
		}
	}
	for _, a := range after.Items {
		if prior, exists := old[a.UID]; exists {
			if prior.Count == a.Count {
				continue
			}
			if _, err := tx.ExecContext(ctx, "UPDATE s9a1.assets SET count=$3 WHERE account=$1 AND instance_id=$2", l.Account, a.UID, a.Count); err != nil {
				return 0, err
			}
		} else {
			weapon, err := json.Marshal(a.Weapon)
			if err != nil {
				return 0, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO s9a1.assets(instance_id,account,character_id,source,template_id,template_json,weapon_facts,count,location,slot,attributes_json,core,detail)
				VALUES($1,$2,nullif($3::integer,0),$4,$5,$6::json,$7::json,$8,$9,$10,$11::json,$12,$13)`, a.UID, l.Account, a.CharacterID, a.Source, a.TemplateID, string(a.Template), string(weapon), a.Count, a.Location, a.Slot, string(a.Attributes), a.Core, a.Detail); err != nil {
				return 0, err
			}
		}
		sharedChanged = sharedChanged || a.CharacterID == 0
	}
	if sharedChanged {
		var revision int64
		err := tx.QueryRowContext(ctx, "UPDATE s9a1.accounts SET asset_revision=asset_revision+1 WHERE account=$1 RETURNING asset_revision", l.Account).Scan(&revision)
		return revision, err
	}
	return before.AssetRevision, nil
}
