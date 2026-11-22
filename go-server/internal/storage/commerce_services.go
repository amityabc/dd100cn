package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"time"
)

type CharacterServices struct {
	CharacterID uint16
	Expires     [8]uint32
	Used        [8]byte
}

type serviceUsage struct {
	Day                                                    int64
	Slot0, Slot1, Slot2, Slot3, Slot4, Slot5, Slot6, Slot7 byte
}

func (u serviceUsage) counts() [8]byte {
	return [8]byte{u.Slot0, u.Slot1, u.Slot2, u.Slot3, u.Slot4, u.Slot5, u.Slot6, u.Slot7}
}

func serviceDay(now time.Time) int64 {
	y, m, d := now.UTC().Add(2 * time.Hour).Date()
	return int64(y)*10000 + int64(m)*100 + int64(d)
}

func loadServiceUsage(c Character, now time.Time) (serviceUsage, error) {
	var p struct{ CharacterServices serviceUsage }
	err := json.Unmarshal(c.Gameplay, &p)
	day := serviceDay(now)
	if p.CharacterServices.Day < 0 {
		return p.CharacterServices, ErrInvalid
	}
	if p.CharacterServices.Day < day {
		return serviceUsage{Day: day}, err
	}
	return p.CharacterServices, err
}

func readCharacterDeadlines(ctx context.Context, tx *sql.Tx, account string) ([8]uint32, error) {
	var deadlines [8]uint32
	rows, err := tx.QueryContext(ctx, "SELECT slot,expires_at FROM s9a1.character_service_deadlines WHERE account=$1", account)
	if err != nil {
		return deadlines, err
	}
	defer rows.Close()
	for rows.Next() {
		var slot byte
		var until uint32
		if err = rows.Scan(&slot, &until); err != nil {
			return deadlines, err
		}
		if slot >= 8 {
			return deadlines, ErrInvalid
		}
		deadlines[slot] = until
	}
	return deadlines, rows.Err()
}

func (s *Store) CharacterServices(ctx context.Context, l Lease, now time.Time, duelReady bool) (CharacterServices, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (CharacterServices, error) {
		var out CharacterServices
		r, err := lockAccount(ctx, tx, l, false)
		duel := duelReady && r.Phase == Selected
		if err != nil || r.Phase != TownReady && !duel || r.SelectedID == 0 {
			return out, err
		}
		out.CharacterID = r.SelectedID
		c, err := mailCharacter(ctx, tx, l.Account, r.SelectedID)
		if err != nil {
			return out, err
		}
		out.Expires, err = readCharacterDeadlines(ctx, tx, l.Account)
		if err != nil {
			return out, err
		}
		usage, err := loadServiceUsage(c, now)
		if err != nil {
			return out, err
		}
		out.Used = usage.counts()
		return out, nil
	})
}

func (x *CommerceTx) extendCharacterService(o CashOffer, now time.Time) error {
	rules := map[byte]struct {
		Slot                       uint8
		Commodity, Template, Price uint32
	}{
		101: {1, 100539, 2681931, 200}, 102: {2, 100540, 2681932, 160}, 103: {3, 100541, 2681933, 200},
		104: {4, 100537, 2681929, 160}, 105: {5, 100538, 2681930, 160}, 106: {6, 100817, 2682205, 200}, 107: {7, 100882, 2682206, 160},
	}
	r, ok := rules[o.PremiumType]
	if !ok || o.Commodity != r.Commodity || o.TemplateID != r.Template || o.Count != 1 || o.Gold != 0 || o.Cera != r.Price ||
		o.PremiumSeconds != 7*86400 || now.Unix() <= 0 ||
		now.Unix()+int64(o.PremiumSeconds) > math.MaxUint32 {
		return ErrInvalid
	}
	res, err := x.tx.ExecContext(x.ctx, `INSERT INTO s9a1.character_service_deadlines(account,slot,expires_at)
		VALUES($1,$2,$3+$4) ON CONFLICT(account,slot) DO UPDATE SET expires_at=greatest(s9a1.character_service_deadlines.expires_at,$3)+$4
		WHERE greatest(s9a1.character_service_deadlines.expires_at,$3)+$4<=4294967295`, x.account, r.Slot, now.Unix(), o.PremiumSeconds)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalid
	}
	x.Inventory.AssetRevision++
	return nil
}

func (x *CommerceTx) serviceActive(slot byte, now time.Time) error {
	deadlines, err := readCharacterDeadlines(x.ctx, x.tx, x.account)
	if err != nil {
		return err
	}
	if slot >= 8 || int64(deadlines[slot]) <= now.Unix() {
		return ErrInvalid
	}
	var p useState
	if err = json.Unmarshal(x.Character.Gameplay, &p); err != nil {
		return err
	}
	if !p.Initialized || p.Progression.HP == 0 {
		return ErrPhase
	}
	return nil
}

func (x *CommerceTx) UseCharacterService(slot byte, now time.Time) error {
	if err := x.serviceActive(slot, now); err != nil {
		return err
	}
	switch slot {
	case 3:
		return x.consumeCharacterService(slot, 10, now)
	case 4:
		var stats struct{ Values map[string]int64 }
		if err := json.Unmarshal(x.Character.Stats, &stats); err != nil {
			return err
		}
		hp, mp := stats.Values["hp_max"], stats.Values["mp_max"]
		if hp <= 0 || hp > math.MaxUint32 || mp < 0 || mp > math.MaxInt32 {
			return ErrInvalid
		}
		var p useState
		if err := json.Unmarshal(x.Character.Gameplay, &p); err != nil {
			return err
		}
		return x.useSave(p, map[string]any{"Hp": hp, "Mp": mp}, false)
	default:
		return ErrInvalid
	}
}

func (x *CommerceTx) consumeCharacterService(slot, limit byte, now time.Time) error {
	u, err := loadServiceUsage(x.Character, now)
	if err != nil {
		return err
	}
	var n *byte
	switch slot {
	case 3:
		n = &u.Slot3
	case 6:
		n = &u.Slot6
	default:
		return ErrInvalid
	}
	if *n >= limit {
		return ErrInvalid
	}
	(*n)++
	return replaceMailFields(&x.Character, map[string]any{"CharacterServices": u})
}

func (x *CommerceTx) RepairWithService(rules map[uint32]RepairDefinition, now time.Time) error {
	if err := x.serviceActive(6, now); err != nil {
		return err
	}
	type target struct {
		item     Asset
		attrs    materialAttributes
		template ordinaryTemplate
	}
	needsFreeRepair := false
	var damaged []target
	for _, a := range x.Inventory.Items {
		if a.CharacterID != x.Character.ID || a.Location != 0 && a.Location != 3 || a.Source != "item" && a.Source != "weapon" {
			continue
		}
		var kind struct {
			Kind        int
			CharmEnergy bool
		}
		if err := json.Unmarshal(a.Template, &kind); err != nil {
			return err
		}
		if kind.Kind == 2 || kind.CharmEnergy {
			continue
		}
		t, err := moveTemplate(&a)
		if err != nil {
			return err
		}
		var v materialAttributes
		if err = json.Unmarshal(a.Attributes, &v); err != nil {
			return err
		}
		if t.MaximumDurability == 0 || v.Durability >= t.MaximumDurability {
			continue
		}
		needsFreeRepair = needsFreeRepair || v.Durability == 0 && (a.Location == 3 || a.Slot < 9)
		damaged = append(damaged, target{a, v, t})
	}
	if !needsFreeRepair {
		x.noChange = true
		return nil
	}
	for _, d := range damaged {
		a, v, t := d.item, d.attrs, d.template
		r, ok := rules[a.TemplateID]
		if !ok || r.TemplateID != a.TemplateID || r.ResourceHash != t.ResourceHash || r.MaximumDurability != t.MaximumDurability ||
			a.UID == "" || a.Count != 1 || len(a.Detail) != 0 || v.ExpiresAt != nil && *v.ExpiresAt <= now.Unix() ||
			a.Weapon != nil && (a.Weapon.Durability != v.Durability || a.Weapon.QualitySeed != v.QualitySeed) {
			return ErrInvalid
		}
	}
	if err := x.consumeCharacterService(6, 6, now); err != nil {
		return err
	}
	for _, d := range damaged {
		a, t := d.item, d.template
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(a.Attributes, &fields); err != nil || fields == nil {
			return ErrInvalid
		}
		var err error
		fields["Durability"], err = json.Marshal(t.MaximumDurability)
		if err != nil {
			return err
		}
		if a.Weapon != nil {
			w := *a.Weapon
			w.Durability = t.MaximumDurability
			a.Weapon = &w
		}
		if err = x.updateCraftAsset(a, fields, a.Core); err != nil {
			return err
		}
	}
	return nil
}
