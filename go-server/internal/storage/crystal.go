package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrCrystalInactive = errors.New("crystal contract is inactive")
	ErrCrystalMissing  = errors.New("selected crystal is unavailable")
)

// The crystal is a prerequisite in a fixed material slot. Selecting it never consumes it.
func (s *Store) SelectCrystal(ctx context.Context, l Lease, selection byte, duelReady bool, now time.Time) error {
	if selection > 5 && selection != 255 {
		return ErrInvalid
	}
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		var done struct{}
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return done, err
		}
		if r.SelectedID == 0 || r.Phase != TownReady && r.Phase != TownLoading && !(duelReady && r.Phase == Selected) {
			return done, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, r.SelectedID))
		if err != nil {
			return done, err
		}
		var p struct {
			Initialized              bool
			CrystalContractSelection byte
		}
		p.CrystalContractSelection = 255
		if err = json.Unmarshal(c.Gameplay, &p); err != nil {
			return done, err
		}
		if !p.Initialized {
			return done, ErrPhase
		}
		if p.CrystalContractSelection > 5 && p.CrystalContractSelection != 255 {
			return done, ErrInvalid
		}
		if p.CrystalContractSelection == selection {
			return done, nil
		}
		if selection != 255 {
			if r.CrystalExpiresAt <= now.Unix() {
				return done, ErrCrystalInactive
			}
			id := sharedMaterialIDs[selection]
			slot, _ := SharedMaterialSlot(id)
			var found bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM s9a1.assets WHERE account=$1 AND (character_id=$2 OR character_id IS NULL)
				AND location=0 AND slot=$3 AND template_id=$4 AND template_json->>'Kind'='2' AND count>0)`, l.Account, c.ID, slot, id).Scan(&found)
			if err != nil {
				return done, err
			}
			if !found {
				return done, ErrCrystalMissing
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE s9a1.characters SET revision=revision+1,
			gameplay_json=jsonb_set(gameplay_json::jsonb,'{CrystalContractSelection}',to_jsonb($3::integer))::json WHERE account=$1 AND wire_id=$2`, l.Account, c.ID, selection)
		return done, err
	})
	return err
}
