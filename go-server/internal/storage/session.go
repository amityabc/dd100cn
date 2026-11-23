package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Acquire is called only after the entry layer resolves an authenticated
// account. A successful replacement fences old writes and old disconnects.
func (s *Store) Acquire(ctx context.Context, account string) (Lease, error) {
	if strings.TrimSpace(account) == "" {
		return Lease{}, ErrInvalid
	}
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Lease, error) {
		l := Lease{Account: account}
		err := tx.QueryRowContext(ctx, `INSERT INTO s9a1.accounts AS a (account,owner,epoch,phase)
			VALUES($1,gen_random_uuid(),1,0)
			ON CONFLICT(account) DO UPDATE SET owner=excluded.owner,epoch=a.epoch+1,phase=0,selected_id=NULL
			RETURNING owner::text,epoch`, account).Scan(&l.Owner, &l.Epoch)
		return l, err
	})
}

func (s *Store) Release(ctx context.Context, l Lease) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if errors.Is(err, ErrLease) {
			return struct{}{}, nil
		}
		if err != nil {
			return struct{}{}, err
		}
		if r.SelectedID != 0 {
			c, err := mailCharacter(ctx, tx, l.Account, r.SelectedID)
			if err != nil {
				return struct{}{}, err
			}
			changed, err := freezePetClocks(&c, l, time.Now())
			if err != nil {
				return struct{}{}, err
			}
			if changed {
				if _, err = saveMailCharacter(ctx, tx, l.Account, c); err != nil {
					return struct{}{}, err
				}
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE s9a1.accounts SET owner=NULL,selected_id=NULL,phase=5
			WHERE account=$1 AND owner=$2::uuid AND epoch=$3`, l.Account, l.Owner, l.Epoch)
		return struct{}{}, err
	})
	return err
}

// ValidateLease performs a single indexed ownership check for read-only
// requests such as heartbeat. Mutations validate again inside their transaction.
func (s *Store) ValidateLease(ctx context.Context, l Lease) error {
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM s9a1.accounts
		WHERE account=$1 AND owner=$2::uuid AND epoch=$3 AND phase<>5)`, l.Account, l.Owner, l.Epoch).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return ErrLease
	}
	return nil
}

// lockAccount serializes commands affecting the same account across processes.
// A roster read holds a shared lock so its settings and characters agree.
func lockAccount(ctx context.Context, tx *sql.Tx, l Lease, write bool) (Roster, error) {
	q := `SELECT phase,coalesce(selected_id,0),capacity,cera,cargo_gold,asset_revision,game_options,hotkeys,
		conqueror_expires_at,skill_master_expires_at,crystal_expires_at,growth_expires_at,treasure_gauge,unique_avatar_gauge
		FROM s9a1.accounts WHERE account=$1 AND owner=$2::uuid AND epoch=$3 AND phase<>5 `
	if write {
		q += "FOR UPDATE"
	} else {
		q += "FOR SHARE"
	}
	var r Roster
	err := tx.QueryRowContext(ctx, q, l.Account, l.Owner, l.Epoch).Scan(&r.Phase, &r.SelectedID, &r.Capacity, &r.Cera, &r.CargoGold, &r.AssetRevision, &r.GameOptions, &r.Hotkeys,
		&r.ConquerorExpiresAt, &r.SkillMasterExpiresAt, &r.CrystalExpiresAt, &r.GrowthExpiresAt, &r.TreasureGauge, &r.UniqueAvatarGauge)
	if errors.Is(err, sql.ErrNoRows) {
		return Roster{}, ErrLease
	}
	return r, err
}

func (s *Store) Snapshot(ctx context.Context, l Lease) (Roster, error) {
	return s.roster(ctx, l, false)
}

// Current reads the account and only its selected character under the same lease.
// Movement and sync do not need to load every other slot's gameplay and equipment.
func (s *Store) Current(ctx context.Context, l Lease) (Roster, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Roster, error) {
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil || r.SelectedID == 0 {
			return r, err
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, r.SelectedID))
		if err != nil {
			return Roster{}, err
		}
		r.Characters = []Character{c}
		err = loadWorn(ctx, tx, l.Account, r.Characters)
		return r, err
	})
}
func (s *Store) List(ctx context.Context, l Lease) (Roster, error) { return s.roster(ctx, l, true) }

func (s *Store) roster(ctx context.Context, l Lease, list bool) (Roster, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Roster, error) {
		r, err := lockAccount(ctx, tx, l, list)
		if err != nil {
			return Roster{}, err
		}
		if list {
			if r.Phase != Authenticated && r.Phase != CharacterList {
				return Roster{}, ErrPhase
			}
			if r.Phase != CharacterList {
				if _, err = tx.ExecContext(ctx, "UPDATE s9a1.accounts SET phase=1 WHERE account=$1", l.Account); err != nil {
					return Roster{}, err
				}
				r.Phase = CharacterList
			}
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 ORDER BY storage_slot,wire_id", l.Account)
		if err != nil {
			return Roster{}, err
		}
		defer rows.Close()
		r.Characters = make([]Character, 0)
		for rows.Next() {
			c, err := scanCharacter(rows)
			if err != nil {
				return Roster{}, err
			}
			r.Characters = append(r.Characters, c)
		}
		if err = rows.Err(); err != nil {
			return Roster{}, err
		}
		if err = rows.Close(); err != nil {
			return Roster{}, err
		}
		err = loadWorn(ctx, tx, l.Account, r.Characters)
		return r, err
	})
}

func (s *Store) AdvanceTown(ctx context.Context, l Lease, bootstrapOnly bool) (Phase, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Phase, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return Exited, err
		}
		if r.SelectedID == 0 || r.Phase < Selected || r.Phase > TownReady {
			return r.Phase, ErrPhase
		}
		if r.Phase == TownReady || (bootstrapOnly && r.Phase == TownLoading) {
			return r.Phase, nil
		}
		next := r.Phase + 1
		_, err = tx.ExecContext(ctx, "UPDATE s9a1.accounts SET phase=$2 WHERE account=$1", l.Account, next)
		return next, err
	})
}

func (s *Store) ReturnToList(ctx context.Context, l Lease) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		if _, err := lockAccount(ctx, tx, l, true); err != nil {
			return struct{}{}, err
		}
		_, err := tx.ExecContext(ctx, "UPDATE s9a1.accounts SET selected_id=NULL,phase=1 WHERE account=$1", l.Account)
		return struct{}{}, err
	})
	return err
}

func (s *Store) SaveGameOptions(ctx context.Context, l Lease, data []byte) error {
	if len(data) != 372 {
		return ErrInvalid
	}
	return s.saveSettings(ctx, l, "game_options", data)
}

func (s *Store) SaveHotkeys(ctx context.Context, l Lease, data []byte) error {
	if len(data) == 0 || len(data) >= 1400 {
		return ErrInvalid
	}
	return s.saveSettings(ctx, l, "hotkeys", data)
}

func (s *Store) saveSettings(ctx context.Context, l Lease, column string, data []byte) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		if _, err := lockAccount(ctx, tx, l, true); err != nil {
			return struct{}{}, err
		}
		_, err := tx.ExecContext(ctx, "UPDATE s9a1.accounts SET "+column+"=$2 WHERE account=$1 AND "+column+" IS DISTINCT FROM $2", l.Account, data)
		return struct{}{}, err
	})
	return err
}
