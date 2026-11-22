package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/protocol"
)

const (
	RenameBag       uint32 = 2660698
	RenameCash      uint32 = 2682290
	RenameCommodity uint32 = 100902
	renameCooldown  int64  = 90 * 86400
	nameReservation int64  = 180 * 86400
)

type RenameState struct {
	PendingSource                           uint32
	GrantedAt, NextAllowedAt, LastChangedAt int64
	Pending                                 bool
}

func readRenameState(raw json.RawMessage) (RenameState, error) {
	var p struct{ CharacterRename RenameState }
	err := json.Unmarshal(raw, &p)
	s := p.CharacterRename
	s.Pending = s.PendingSource != 0
	if err != nil {
		return s, err
	}
	if s.Pending && s.PendingSource != RenameBag && s.PendingSource != RenameCash || s.GrantedAt < 0 || s.NextAllowedAt < 0 ||
		s.LastChangedAt < 0 || s.Pending != (s.GrantedAt > 0) || s.NextAllowedAt > 253402300799 || s.LastChangedAt > 253402300799 {
		return s, ErrInvalid
	}
	return s, nil
}

func ValidateRenameProfile(raw json.RawMessage) error {
	_, err := readRenameState(raw)
	return err
}

type RenameCommand struct {
	Sequence, Opcode, Slot uint16
	Request                protocol.RenameRequest
	// The native cash cart permits two ordinary purchase model values, both part of its replay identity.
	PurchaseModel byte
}

type RenameReplay struct {
	Owner                     Lease
	Command                   RenameCommand
	Character                 uint16
	Revision, AccountRevision int64
}

func (r RenameReplay) matches(l Lease, cmd RenameCommand, c Character, accountRevision int64) bool {
	return r.Owner == l && r.Command == cmd && r.Character == c.ID && r.Revision == c.Revision && r.AccountRevision == accountRevision
}

// Creation and rename share a writer lock across accounts. A paid rename frees an active
// name and reserves that same name in another table in one transaction.
func lockNames(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(1395736881,2)")
	return err
}

func completeRename(c Character, req protocol.RenameRequest, now int64) (RenameState, bool, error) {
	state, err := readRenameState(c.Gameplay)
	if err != nil {
		return state, false, err
	}
	if !validName(req.NewName) || req.Reason != 1 && req.Reason != 2 || c.Name != req.OldName || !state.Pending ||
		now <= 0 || now > 253402300799-nameReservation || now < state.NextAllowedAt || now < state.GrantedAt || nameKey(c.Name) == nameKey(req.NewName) {
		return state, false, ErrInvalid
	}
	if _, err := protocol.EncodeText(req.NewName, 12); err != nil {
		return state, false, ErrInvalid
	}
	paid := state.PendingSource == RenameCash
	state.Pending, state.PendingSource, state.GrantedAt, state.LastChangedAt = false, 0, 0, now
	if paid {
		state.NextAllowedAt = now + renameCooldown
	}
	return state, paid, nil
}

func (s *Store) RenameCharacter(ctx context.Context, l Lease, cmd RenameCommand, last RenameReplay, at time.Time) (RenameReplay, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (RenameReplay, error) {
		var out RenameReplay
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return out, err
		}
		if last.Owner == l && last.Command == cmd && last.AccountRevision == r.AssetRevision {
			var same bool
			err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM s9a1.characters WHERE account=$1 AND wire_id=$2 AND revision=$3)", l.Account, last.Character, last.Revision).Scan(&same)
			if err != nil {
				return out, err
			}
			if same {
				return last, nil
			}
		}
		if r.SelectedID != 0 || r.Phase != CharacterList && r.Phase != Authenticated {
			return out, ErrPhase
		}
		if err := lockNames(ctx, tx); err != nil {
			return out, err
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 ORDER BY storage_slot,wire_id OFFSET $2 LIMIT 1", l.Account, cmd.Request.Index))
		if errors.Is(err, sql.ErrNoRows) {
			return out, ErrInvalid
		}
		if err != nil {
			return out, err
		}
		now := at.Unix()
		state, paid, err := completeRename(c, cmd.Request, now)
		if err != nil {
			return out, err
		}
		key := nameKey(cmd.Request.NewName)
		ok, err := available(ctx, tx, key)
		if err != nil {
			return out, err
		}
		if !ok {
			return out, ErrNameTaken
		}
		if paid {
			result, err := tx.ExecContext(ctx, `INSERT INTO s9a1.name_reservations AS n(name_key,character_id,expires_at) VALUES($1,$2,$3)
				ON CONFLICT(name_key) DO UPDATE SET character_id=excluded.character_id,expires_at=excluded.expires_at WHERE n.expires_at<=$4`, nameKey(c.Name), c.ID, now+nameReservation, now)
			if err != nil {
				return out, err
			}
			if count, err := result.RowsAffected(); err != nil || count != 1 {
				return out, ErrConflict
			}
		}
		body, err := json.Marshal(state)
		if err != nil {
			return out, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE s9a1.characters SET name=$3,name_key=$4,revision=revision+1,gameplay_json=jsonb_set(
			gameplay_json::jsonb,'{CharacterRename}',coalesce(nullif(gameplay_json::jsonb->'CharacterRename','null'::jsonb),'{}'::jsonb) || $5::jsonb)::json
			WHERE account=$1 AND wire_id=$2`, l.Account, c.ID, cmd.Request.NewName, key, string(body))
		return RenameReplay{l, cmd, c.ID, c.Revision + 1, r.AssetRevision}, err
	})
}

type RenameGrant struct {
	Replay    RenameReplay
	Character Character
	Inventory Inventory
}

func (s *Store) GrantRename(ctx context.Context, l Lease, cmd RenameCommand, last RenameReplay, source json.RawMessage,
	town func(Character) (bool, error), at time.Time) (RenameGrant, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (RenameGrant, error) {
		var out RenameGrant
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return out, err
		}
		if r.Phase != TownReady || r.SelectedID == 0 {
			return out, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, r.SelectedID))
		if err != nil {
			return out, err
		}
		ok, err := town(c)
		if err != nil {
			return out, err
		}
		if !ok {
			return out, ErrPhase
		}
		in, err := readInventory(ctx, tx, l.Account, r)
		if err != nil {
			return out, err
		}
		if last.matches(l, cmd, c, r.AssetRevision) {
			return RenameGrant{last, c, in}, nil
		}
		state, err := readRenameState(c.Gameplay)
		if err != nil {
			return out, err
		}
		id := RenameBag
		if cmd.Opcode == 64 {
			id = RenameCash
		}
		now := at.Unix()
		var template struct {
			ordinaryTemplate
			ResourceExpiration *int64
		}
		if json.Unmarshal(source, &template) != nil || template.TemplateID != id || c.Job > 15 || template.UsableJobs&(1<<c.Job) == 0 ||
			c.Level < template.MinimumLevel || template.ResourceExpiration != nil && *template.ResourceExpiration <= now || state.Pending ||
			now < state.NextAllowedAt || now <= 0 || now > 253402300799-renameCooldown {
			return out, ErrInvalid
		}
		if id == RenameCash {
			if r.Cera < 8800 {
				return out, ErrInvalid
			}
			err = tx.QueryRowContext(ctx, `UPDATE s9a1.accounts SET cera=cera-8800,asset_revision=asset_revision+1 WHERE account=$1 RETURNING cera,asset_revision`, l.Account).Scan(&r.Cera, &r.AssetRevision)
		} else {
			err = debitRenameCard(ctx, tx, l.Account, c.ID, cmd.Slot, in, source, now)
		}
		if err != nil {
			return out, err
		}
		state.PendingSource, state.GrantedAt, state.Pending = id, now, true
		body, err := json.Marshal(state)
		if err != nil {
			return out, err
		}
		c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET revision=revision+1,gameplay_json=jsonb_set(
			gameplay_json::jsonb,'{CharacterRename}',coalesce(nullif(gameplay_json::jsonb->'CharacterRename','null'::jsonb),'{}'::jsonb) || $3::jsonb)::json
			WHERE account=$1 AND wire_id=$2 RETURNING `+characterColumns, l.Account, c.ID, string(body)))
		if err != nil {
			return out, err
		}
		in, err = readInventory(ctx, tx, l.Account, r)
		return RenameGrant{RenameReplay{l, cmd, c.ID, c.Revision, r.AssetRevision}, c, in}, err
	})
}

func debitRenameCard(ctx context.Context, tx *sql.Tx, account string, id, slot uint16, in Inventory, source json.RawMessage, now int64) error {
	for _, a := range in.Items {
		if a.CharacterID != id || a.Location != 0 || a.Slot != slot || a.TemplateID != RenameBag || a.Source != "item" {
			continue
		}
		t, err := moveTemplate(&a)
		if err != nil {
			return err
		}
		if !(slot >= 3 && slot <= 8 || slot >= t.FirstBagSlot && slot <= t.bagEnd(in.BagExpansion)) {
			return ErrInvalid
		}
		var parsed materialDefinition
		if err = json.Unmarshal(a.Template, &parsed); err != nil {
			return err
		}
		var attributes materialAttributes
		if err = json.Unmarshal(a.Attributes, &attributes); err != nil {
			return err
		}
		plain, err := ordinaryNewCore(a, parsed, attributes)
		if err != nil {
			return err
		}
		if !plain || attributes.ExpiresAt != nil && *attributes.ExpiresAt <= now || !sameCommerceTemplate(source, a.Template) {
			return ErrInvalid
		}
		if a.Count == 1 {
			_, err = tx.ExecContext(ctx, "DELETE FROM s9a1.assets WHERE account=$1 AND instance_id=$2", account, a.UID)
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE s9a1.assets SET count=count-1 WHERE account=$1 AND instance_id=$2", account, a.UID)
		}
		return err
	}
	return ErrInvalid
}
