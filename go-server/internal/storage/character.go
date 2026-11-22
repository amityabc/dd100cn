package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

const characterColumns = `wire_id,storage_slot,name,job,level,created,revision,stats_json::text,gameplay_json::text,resource_hash,
	town,area,x,y,direction,state,wire_tail,return_town,return_area,return_x,return_y,return_direction,gold,coin`

func scanCharacter(row interface{ Scan(...any) error }) (Character, error) {
	var c Character
	var stats, gameplay string
	var rt, ra, rx, ry, rd sql.NullInt64
	p := &c.Position
	err := row.Scan(&c.ID, &c.StorageSlot, &c.Name, &c.Job, &c.Level, &c.Created, &c.Revision, &stats, &gameplay, &c.ResourceHash,
		&p.Town, &p.Area, &p.X, &p.Y, &p.Direction, &p.State, &p.WireTail, &rt, &ra, &rx, &ry, &rd, &c.Gold, &c.Coins)
	if err != nil {
		return Character{}, err
	}
	c.Stats, c.Gameplay = json.RawMessage(stats), json.RawMessage(gameplay)
	if rt.Valid {
		c.RoomReturn = &Position{Town: uint16(rt.Int64), Area: uint16(ra.Int64), X: uint16(rx.Int64), Y: uint16(ry.Int64), Direction: uint8(rd.Int64)}
	}
	return c, nil
}

func available(ctx context.Context, tx *sql.Tx, key string) (bool, error) {
	var ok bool
	err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM s9a1.characters WHERE name_key=$1)
		AND NOT EXISTS(SELECT 1 FROM s9a1.name_reservations WHERE name_key=$1 AND expires_at>extract(epoch FROM current_timestamp))`, key).Scan(&ok)
	return ok, err
}

func (s *Store) NameAvailable(ctx context.Context, l Lease, name string) (bool, error) {
	if !validName(name) {
		return false, ErrInvalid
	}
	return writeTx(ctx, s.db, func(tx *sql.Tx) (bool, error) {
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil {
			return false, err
		}
		if r.Phase != Authenticated && r.Phase != CharacterList {
			return false, ErrPhase
		}
		return available(ctx, tx, nameKey(name))
	})
}

func (s *Store) Create(ctx context.Context, l Lease, in CreateCharacter) (Character, error) {
	hash, err := hex.DecodeString(in.ResourceHash)
	var newProfile struct{ CharacterRename json.RawMessage }
	if !validName(in.Name) || in.Job > 15 || err != nil || len(hash) != 32 || !json.Valid(in.Stats) || !bytes.HasPrefix(bytes.TrimSpace(in.Stats), []byte("{")) ||
		!bytes.HasPrefix(bytes.TrimSpace(in.Gameplay), []byte("{")) || json.Unmarshal(in.Gameplay, &newProfile) != nil ||
		(len(newProfile.CharacterRename) > 0 && string(newProfile.CharacterRename) != "null") {
		return Character{}, ErrInvalid
	}
	p := Position{Town: 38, Area: 1, X: 450, Y: 234, Direction: 5, State: 3, WireTail: 100}
	if in.Opening != nil {
		p = *in.Opening
		p.WireTail = 100
		p.State = 0
	}
	if p.Direction > 7 {
		return Character{}, ErrInvalid
	}
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Character, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return Character{}, err
		}
		if r.Phase != CharacterList {
			return Character{}, ErrPhase
		}
		key := nameKey(in.Name)
		ok, err := available(ctx, tx, key)
		if err != nil {
			return Character{}, err
		}
		if !ok {
			return Character{}, ErrNameTaken
		}
		var slot int
		err = tx.QueryRowContext(ctx, `SELECT s FROM generate_series(0,$2::integer-1) s WHERE NOT EXISTS
			(SELECT 1 FROM s9a1.characters WHERE account=$1 AND storage_slot=s) ORDER BY s LIMIT 1`, l.Account, r.Capacity).Scan(&slot)
		if errors.Is(err, sql.ErrNoRows) {
			return Character{}, ErrCapacity
		}
		if err != nil {
			return Character{}, err
		}
		var id int
		err = tx.QueryRowContext(ctx, `UPDATE s9a1.character_id_allocator SET next_id=next_id+1 WHERE singleton AND next_id<=65535 RETURNING next_id-1`).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return Character{}, ErrCapacity
		}
		if err != nil {
			return Character{}, err
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, `INSERT INTO s9a1.characters
			(wire_id,account,storage_slot,name,name_key,job,level,created,revision,stats_json,gameplay_json,resource_hash,town,area,x,y,direction,state,wire_tail)
			VALUES($1,$2,$3,$4,$5,$6,1,$7,0,$8::json,$9::json,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING `+characterColumns,
			id, l.Account, slot, in.Name, key, in.Job, in.Created, string(in.Stats), string(in.Gameplay), in.ResourceHash, p.Town, p.Area, p.X, p.Y, p.Direction, p.State, p.WireTail))
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "characters_name_key_key" {
			return Character{}, ErrNameTaken
		}
		return c, err
	})
}

// Select takes the zero-based visible roster index, not a slot or wire ID.
func (s *Store) Select(ctx context.Context, l Lease, displayIndex uint16) (Character, error) {
	return s.SelectAt(ctx, l, displayIndex, nil)
}

// SelectionPlacement is derived by the town catalog for the displayed
// character. ReturnPoint may be nil for a room without a saved exit.
type SelectionPlacement struct {
	CharacterID uint16
	Revision    int64
	Home        Position
	InRoom      bool
	ReturnPoint *Position
}

func (s *Store) SelectAt(ctx context.Context, l Lease, displayIndex uint16, plan *SelectionPlacement) (Character, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Character, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return Character{}, err
		}
		if r.Phase != Authenticated && r.Phase != CharacterList {
			return Character{}, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+
			" FROM s9a1.characters WHERE account=$1 ORDER BY storage_slot,wire_id OFFSET $2 LIMIT 1", l.Account, displayIndex))
		if errors.Is(err, sql.ErrNoRows) {
			return Character{}, ErrInvalid
		}
		if err != nil {
			return Character{}, err
		}
		var profile struct {
			CharacterRename *struct{ PendingSource uint32 }
			Tutorial        *struct{ Completed bool }
		}
		if err = json.Unmarshal(c.Gameplay, &profile); err != nil {
			return Character{}, err
		}
		if profile.CharacterRename != nil && profile.CharacterRename.PendingSource != 0 {
			return Character{}, ErrPhase
		}
		if plan != nil {
			if plan.CharacterID != c.ID || plan.Revision != c.Revision {
				return Character{}, ErrConflict
			}
			if profile.Tutorial != nil && !profile.Tutorial.Completed {
				return Character{}, ErrPhase
			}
			if plan.Home.Direction > 7 || (plan.ReturnPoint != nil && plan.ReturnPoint.Direction > 7) {
				return Character{}, ErrInvalid
			}
			if !plan.InRoom || !sameReturn(c.RoomReturn, plan.ReturnPoint) {
				p := c.Position
				if !plan.InRoom {
					p = pointOnly(plan.Home)
					p.WireTail = c.Position.WireTail
				}
				var rt, ra, rx, ry, rd any
				if r := plan.ReturnPoint; r != nil {
					rt, ra, rx, ry, rd = r.Town, r.Area, r.X, r.Y, r.Direction
				}
				c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET town=$4,area=$5,x=$6,y=$7,direction=$8,state=$9,
					return_town=$10,return_area=$11,return_x=$12,return_y=$13,return_direction=$14,revision=revision+1
					WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns,
					l.Account, c.ID, c.Revision, p.Town, p.Area, p.X, p.Y, p.Direction, p.State, rt, ra, rx, ry, rd))
				if errors.Is(err, sql.ErrNoRows) {
					return Character{}, ErrConflict
				}
				if err != nil {
					return Character{}, err
				}
			}
		}
		worn := []Character{c}
		if err = loadWorn(ctx, tx, l.Account, worn); err != nil {
			return Character{}, err
		}
		c = worn[0]
		if _, err = tx.ExecContext(ctx, "DELETE FROM s9a1.last_inventory_move WHERE account=$1", l.Account); err != nil {
			return Character{}, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM s9a1.opening_entry WHERE account=$1", l.Account); err != nil {
			return Character{}, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM s9a1.town_transition WHERE account=$1", l.Account); err != nil {
			return Character{}, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE s9a1.accounts SET selected_id=$2,phase=2 WHERE account=$1", l.Account, c.ID)
		return c, err
	})
}

func pointOnly(p Position) Position {
	return Position{Town: p.Town, Area: p.Area, X: p.X, Y: p.Y, Direction: p.Direction}
}

func sameReturn(a, b *Position) bool {
	if a == nil || b == nil {
		return a == b
	}
	return pointOnly(*a) == pointOnly(*b)
}

// SavePosition saves an already validated scene result. Callers deriving
// arrival quest progress must use SaveTown with the updated profile.
func (s *Store) SavePosition(ctx context.Context, l Lease, id uint16, rev int64, p Position) (Character, error) {
	return s.SaveTown(ctx, l, id, rev, p, nil)
}

func (s *Store) SaveTown(ctx context.Context, l Lease, id uint16, rev int64, p Position, gameplay json.RawMessage) (Character, error) {
	if p.Direction > 7 || (gameplay != nil && !json.Valid(gameplay)) {
		return Character{}, ErrInvalid
	}
	var profile any
	if gameplay != nil {
		profile = string(gameplay)
	}
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Character, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return Character{}, err
		}
		if r.Phase != TownReady || r.SelectedID != id {
			return Character{}, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET town=$4,area=$5,x=$6,y=$7,direction=$8,state=$9,wire_tail=$10,
			gameplay_json=coalesce($11::json,gameplay_json),revision=revision+1 WHERE account=$1 AND wire_id=$2 AND revision=$3
			AND ($11::json IS NULL OR ($11::jsonb->'Tutorial') IS NOT DISTINCT FROM (gameplay_json::jsonb->'Tutorial')) RETURNING `+characterColumns,
			l.Account, id, rev, p.Town, p.Area, p.X, p.Y, p.Direction, p.State, p.WireTail, profile))
		if errors.Is(err, sql.ErrNoRows) {
			return Character{}, ErrConflict
		}
		if err != nil {
			return Character{}, err
		}
		worn := []Character{c}
		err = loadWorn(ctx, tx, l.Account, worn)
		return worn[0], err
	})
}
