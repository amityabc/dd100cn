package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// TutorialDefinition contains the resource facts already checked by the host
// against the active town channel and the PVF outdoor return point.
type TutorialDefinition struct {
	Job          uint8
	Dungeon      uint32
	ResourceHash string
	ReturnPoint  Position
}

type openingProfile struct {
	Initialized bool
	Progression struct {
		Hp                          uint32
		Fatigue, FatigueLimit       uint16
		FatigueDay                  int64
		LuckPoints, LuckPlayMinutes int32
	}
	Tutorial *struct {
		Dungeon      uint32
		ResourceHash string
		ReturnPoint  *Position
		Completed    bool
	}
}

func readOpening(c Character, proof TutorialDefinition) (openingProfile, error) {
	var p openingProfile
	hash, err := hex.DecodeString(proof.ResourceHash)
	if err != nil || len(hash) != 32 || proof.Dungeon == 0 || proof.ReturnPoint.Direction > 7 {
		return p, ErrInvalid
	}
	if err = json.Unmarshal(c.Gameplay, &p); err != nil {
		return p, err
	}
	t := p.Tutorial
	if t == nil || t.Completed {
		return p, ErrPhase
	}
	if proof.Job != c.Job || t.Dungeon != proof.Dungeon || t.ResourceHash != proof.ResourceHash || !sameReturn(t.ReturnPoint, &proof.ReturnPoint) {
		return p, ErrInvalid
	}
	return p, nil
}

// CompleteTutorial applies the old pending-opening transition under the
// current account lease. Historical characters without a tutorial go to town
// directly and never receive a new opening.
func (s *Store) CompleteTutorial(ctx context.Context, l Lease, id uint16, rev int64, proof TutorialDefinition) (Character, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Character, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return Character{}, err
		}
		if r.SelectedID != id || r.Phase < Selected || r.Phase > TownReady {
			return Character{}, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, id))
		if err != nil {
			return Character{}, err
		}
		if c.Revision != rev {
			return Character{}, ErrConflict
		}
		if _, err = readOpening(c, proof); err != nil {
			return Character{}, err
		}
		p := proof.ReturnPoint
		c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET
			gameplay_json=jsonb_set(gameplay_json::jsonb,'{Tutorial,Completed}','true')::json,
			town=$4,area=$5,x=$6,y=$7,direction=$8,state=0,
			return_town=NULL,return_area=NULL,return_x=NULL,return_y=NULL,return_direction=NULL,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns,
			l.Account, id, rev, p.Town, p.Area, p.X, p.Y, p.Direction))
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

// EnterOpening commits automatic tutorial selection and the new run's fatigue
// debit. A retransmission of this selection never creates a second debit.
func (s *Store) EnterOpening(ctx context.Context, l Lease, id uint16, rev int64, proof TutorialDefinition, now time.Time, cost uint16) (Character, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Character, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return Character{}, err
		}
		if r.SelectedID != id || r.Phase != TownLoading && r.Phase != TownReady {
			return Character{}, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, id))
		if err != nil {
			return Character{}, err
		}
		p, err := readOpening(c, proof)
		if err != nil {
			return Character{}, err
		}
		if !p.Initialized || p.Progression.Hp == 0 {
			return Character{}, ErrPhase
		}
		var entryHash string
		var entryCost uint16
		err = tx.QueryRowContext(ctx, "SELECT resource_hash,cost FROM s9a1.opening_entry WHERE account=$1 AND owner=$2 AND epoch=$3 AND character_id=$4", l.Account, l.Owner, l.Epoch, id).Scan(&entryHash, &entryCost)
		if err == nil {
			if entryHash != proof.ResourceHash || entryCost != cost {
				return Character{}, ErrConflict
			}
			worn := []Character{c}
			err = loadWorn(ctx, tx, l.Account, worn)
			return worn[0], err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Character{}, err
		}
		if c.Revision != rev {
			return Character{}, ErrConflict
		}
		progress := p.Progression
		if progress.Fatigue > progress.FatigueLimit || progress.FatigueDay < 0 || progress.LuckPoints < 0 || progress.LuckPoints > 100000 || progress.LuckPlayMinutes < 0 {
			return Character{}, ErrInvalid
		}
		date := now.UTC().Add(2 * time.Hour)
		day := int64(date.Year()*10000 + int(date.Month())*100 + date.Day())
		fatigue := progress.Fatigue
		if progress.FatigueDay != 0 && day > progress.FatigueDay {
			fatigue = progress.FatigueLimit
		}
		if cost != 0 && fatigue == 0 {
			return Character{}, ErrFunds
		}
		fatigue -= min(fatigue, cost)
		c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET gameplay_json=
			jsonb_set(jsonb_set(gameplay_json::jsonb,'{Progression,FatigueDay}',to_jsonb($4::bigint)),'{Progression,Fatigue}',to_jsonb($5::integer))::json,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns, l.Account, id, rev, max(day, progress.FatigueDay), fatigue))
		if err != nil {
			return Character{}, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE s9a1.accounts SET phase=4 WHERE account=$1", l.Account); err != nil {
			return Character{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO s9a1.opening_entry(account,owner,epoch,character_id,resource_hash,cost) VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT(account) DO UPDATE SET owner=excluded.owner,epoch=excluded.epoch,character_id=excluded.character_id,resource_hash=excluded.resource_hash,cost=excluded.cost`,
			l.Account, l.Owner, l.Epoch, id, proof.ResourceHash, cost); err != nil {
			return Character{}, err
		}
		worn := []Character{c}
		err = loadWorn(ctx, tx, l.Account, worn)
		return worn[0], err
	})
}
