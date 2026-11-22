package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
)

// TownTransition is an already resource-qualified C36 route or C1418 room
// return. From carries C36's declared source area. RecordOrigin stores the
// locked row's complete previous point when entering a room.
type TownTransition struct {
	From, To                                        Position
	RoomReturn, RecordOrigin, NativeNPC, ReplayOnly bool
	TransitionKind                                  uint8
	TargetNPCID                                     uint32
	Gameplay                                        json.RawMessage
}
type TownTransitionResult struct {
	Character Character
	Replayed  bool
	From      Position
}
type sceneTrace struct {
	From, To              Position
	RoomReturn, NativeNPC bool
	Kind                  uint8
	NPC                   uint32
}

func sameArea(a, b Position) bool { return a.Town == b.Town && a.Area == b.Area }
func seriaRoom(p Position) bool   { return p.Town == 38 && p.Area == 1 }

func (s *Store) TransitionTown(ctx context.Context, l Lease, id uint16, rev int64, t TownTransition) (TownTransitionResult, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (TownTransitionResult, error) {
		var out TownTransitionResult
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return out, err
		}
		if r.Phase != TownReady || r.SelectedID != id {
			return out, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, id))
		if err != nil {
			return out, err
		}
		var trace sceneTrace
		var previous string
		err = tx.QueryRowContext(ctx, `SELECT trace::text FROM s9a1.town_transition WHERE account=$1 AND owner=$2 AND epoch=$3 AND character_id=$4`, l.Account, l.Owner, l.Epoch, id).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if err == nil {
			if err = json.Unmarshal([]byte(previous), &trace); err != nil {
				return out, err
			}
			if sameArea(c.Position, trace.To) && t.RoomReturn == trace.RoomReturn {
				if t.RoomReturn {
					out.Replayed = !seriaRoom(c.Position)
				} else {
					out.Replayed = pointOnly(t.To) == trace.To && sameArea(t.From, trace.From) && t.TransitionKind == trace.Kind && t.TargetNPCID == trace.NPC && t.NativeNPC == trace.NativeNPC
				}
			}
		}
		if !out.Replayed {
			if t.ReplayOnly {
				return out, ErrInvalid
			}
			if c.Revision != rev {
				return out, ErrConflict
			}
			target := pointOnly(t.To)
			if target.Direction > 7 || target.X > math.MaxInt16 || target.Y > math.MaxInt16 || target.Town > 255 || target.Area > 255 {
				return out, ErrInvalid
			}
			if t.RoomReturn {
				if !seriaRoom(c.Position) {
					return out, ErrPhase
				}
				if seriaRoom(target) || t.RecordOrigin || t.TargetNPCID != 0 || t.NativeNPC || t.TransitionKind != 0 || c.RoomReturn != nil && !sameReturn(c.RoomReturn, &target) {
					return out, ErrInvalid
				}
			} else {
				if !sameArea(c.Position, t.From) || sameArea(c.Position, target) || t.TransitionKind != 0 && t.TransitionKind != 5 && t.TransitionKind != 6 ||
					(t.NativeNPC || t.TargetNPCID != 0) && t.TransitionKind != 0 || t.RecordOrigin && (!seriaRoom(target) || seriaRoom(c.Position)) {
					return out, ErrInvalid
				}
			}
			var gameplay any
			if t.Gameplay != nil {
				var a, b map[string]json.RawMessage
				if err = json.Unmarshal(c.Gameplay, &a); err != nil {
					return out, err
				}
				if err = json.Unmarshal(t.Gameplay, &b); err != nil || b == nil {
					return out, ErrInvalid
				}
				// The route's only permitted profile mutation is arrival quest progress.
				delete(a, "Quests")
				delete(b, "Quests")
				if len(a) != len(b) {
					return out, ErrInvalid
				}
				for k, v := range a {
					if other, ok := b[k]; !ok || !equalJSON(v, other) {
						return out, ErrInvalid
					}
				}
				gameplay = string(t.Gameplay)
			}
			from := pointOnly(c.Position)
			c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET town=$4,area=$5,x=$6,y=$7,direction=$8,state=0,
				return_town=CASE WHEN $9 THEN town ELSE return_town END,return_area=CASE WHEN $9 THEN area ELSE return_area END,
				return_x=CASE WHEN $9 THEN x ELSE return_x END,return_y=CASE WHEN $9 THEN y ELSE return_y END,
				return_direction=CASE WHEN $9 THEN direction ELSE return_direction END,
				gameplay_json=coalesce($10::json,gameplay_json),revision=revision+1 WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns,
				l.Account, id, rev, target.Town, target.Area, target.X, target.Y, target.Direction, t.RecordOrigin, gameplay))
			if err != nil {
				return out, err
			}
			trace = sceneTrace{from, target, t.RoomReturn, t.NativeNPC, t.TransitionKind, t.TargetNPCID}
			raw, err := json.Marshal(trace)
			if err != nil {
				return out, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO s9a1.town_transition(account,owner,epoch,character_id,trace) VALUES($1,$2,$3,$4,$5::json)
				ON CONFLICT(account) DO UPDATE SET owner=excluded.owner,epoch=excluded.epoch,character_id=excluded.character_id,trace=excluded.trace`, l.Account, l.Owner, l.Epoch, id, string(raw))
			if err != nil {
				return out, err
			}
		}
		out.From = trace.From
		chars := []Character{c}
		err = loadWorn(ctx, tx, l.Account, chars)
		out.Character = chars[0]
		return out, err
	})
}
