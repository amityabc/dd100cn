package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
)

type WearScene struct {
	Run       string
	Character uint16
	Map       uint32
	X, Y      byte
}
type WearReplay struct {
	Lease Lease
	WearScene
	Sequence                  uint16
	Slot                      byte
	Target                    string
	Revision, AccountRevision int64
}
type WearResult struct {
	Character Character
	Asset     Asset
	Replay    WearReplay
	Applied   bool
}

func (s *Store) Wear(ctx context.Context, l Lease, scene WearScene, slot byte, seq uint16, last WearReplay) (WearResult, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (WearResult, error) {
		var out WearResult
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return out, err
		}
		if r.SelectedID != scene.Character || scene.Character == 0 || scene.Run == "" || scene.Map == 0 {
			return out, nil
		}
		c, err := mailCharacter(ctx, tx, l.Account, scene.Character)
		if err != nil {
			return out, err
		}
		var p struct {
			Initialized bool
			Progression struct{ Hp uint32 }
		}
		if err = json.Unmarshal(c.Gameplay, &p); err != nil {
			return out, err
		}
		if !p.Initialized || p.Progression.Hp == 0 {
			return out, nil
		}
		a, err := scanAsset(tx.QueryRowContext(ctx, "SELECT "+assetColumns+" FROM s9a1.assets WHERE account=$1 AND character_id=$2 AND location=3 AND slot=$3", l.Account, scene.Character, slot))
		if errors.Is(err, sql.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		t, err := moveTemplate(&a)
		if err != nil || t.Kind == 2 || t.MaximumDurability == 0 || a.Count != 1 || !mailUID(a.UID) || len(a.Detail) != 0 || len(a.Core) != 119 {
			return out, nil
		}
		marker := WearReplay{l, scene, seq, slot, a.UID, c.Revision, r.AssetRevision}
		if marker == last {
			return out, nil
		}
		var attrs map[string]json.RawMessage
		var v materialAttributes
		if err = json.Unmarshal(a.Attributes, &attrs); err != nil {
			return out, err
		}
		if err = json.Unmarshal(a.Attributes, &v); err != nil {
			return out, err
		}
		if attrs == nil || v.Durability == 0 || v.Durability > t.MaximumDurability || a.Weapon != nil && a.Weapon.Durability != v.Durability {
			return out, nil
		}
		v.Durability--
		attrs["Durability"] = json.RawMessage(strconv.FormatUint(uint64(v.Durability), 10))
		a.Attributes, err = json.Marshal(attrs)
		if err != nil {
			return out, err
		}
		if a.Weapon != nil {
			a.Weapon.Durability = v.Durability
		}
		w, err := json.Marshal(a.Weapon)
		if err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE s9a1.assets SET attributes_json=$2::json,weapon_facts=$3::json WHERE instance_id=$1", a.UID, string(a.Attributes), string(w)); err != nil {
			return out, err
		}
		if err = tx.QueryRowContext(ctx, "UPDATE s9a1.characters SET revision=revision+1 WHERE account=$1 AND wire_id=$2 RETURNING revision", l.Account, c.ID).Scan(&c.Revision); err != nil {
			return out, err
		}
		marker.Revision = c.Revision
		return WearResult{c, a, marker, true}, nil
	})
}
