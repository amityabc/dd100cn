package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// TutorialDeath contains the domain executor's complete frozen C39 plan.
// The trusted resource calculator supplies the next level, full stats and
// gameplay profile even when no level-up occurred.
type TutorialDeath struct {
	CharacterID                         uint16
	Revision                            int64
	Opening                             TutorialDefinition
	Level                               uint8
	Stats, Gameplay                     json.RawMessage
	ReceiptKey, Input, Result           string
	DungeonResourceHash, ExperienceHash string
}
type TutorialDeathResult struct {
	Character Character
	Replayed  bool
}

type openingDeathInput struct {
	Source, Version, Account, OwnerGuid, RunId string
	Epoch                                      int64
	Character, Actor                           uint16
	Dungeon, MapId, ExperienceAward            uint32
	MazeIndex, RoomX, RoomY, Class             uint8
	Opcode                                     uint16
	ResourceHash, ExperienceHash, Body         string
	Maps                                       []uint32
}
type openingDeathResult struct {
	Actor                                                                              uint16
	ExperienceAward                                                                    uint32
	Maps, Totals                                                                       []uint32
	Drops                                                                              []json.RawMessage
	QuestDrop, Luck, FirstHunt, MaterialPolicy, OculusBossPolicy, ApcIndependentPolicy json.RawMessage
}
type deathReceipt struct {
	SpecialGrants      json.RawMessage `json:",omitempty"`
	Key, Input, Result string
	Revision           int64
	Grants             []json.RawMessage
}

func matchesSHA(s string) bool { _, err := hex.DecodeString(s); return len(s) == 64 && err == nil }
func matchesUUID(s string) bool {
	if len(s) != 36 || s != strings.ToLower(s) || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return err == nil && len(b) == 16 && !bytes.Equal(b, make([]byte, 16))
}

func deathFacts(l Lease, p TutorialDeath) (openingDeathInput, openingDeathResult, error) {
	var i openingDeathInput
	var z openingDeathResult
	if json.Unmarshal([]byte(p.Input), &i) != nil || json.Unmarshal([]byte(p.Result), &z) != nil {
		return i, z, ErrInvalid
	}
	if !matchesUUID(i.RunId) || !matchesSHA(p.DungeonResourceHash) || i.ResourceHash != p.DungeonResourceHash || !matchesSHA(p.ExperienceHash) || i.ExperienceHash != p.ExperienceHash ||
		i.Account != l.Account || i.OwnerGuid != l.Owner || i.Epoch != l.Epoch || i.Character != p.CharacterID || i.Dungeon != p.Opening.Dungeon ||
		i.Source != "ClientDelegated" || i.Version != "TrustedLocalClientCombat/v1" || i.Class != 1 || i.Opcode != 39 || i.Actor < 402 || i.Actor == math.MaxUint16 ||
		z.Actor != i.Actor || len(z.Totals) != 4 || len(z.Maps) == 0 || len(z.Maps) != len(i.Maps) || z.Drops == nil || len(z.Drops) != 0 {
		return i, z, ErrInvalid
	}
	if len(z.QuestDrop) > 0 || len(z.Luck) > 0 || len(z.FirstHunt) > 0 || len(z.MaterialPolicy) > 0 || len(z.OculusBossPolicy) > 0 || len(z.ApcIndependentPolicy) > 0 {
		return i, z, ErrInvalid
	}
	for n, id := range i.Maps {
		if id != z.Maps[n] {
			return i, z, ErrInvalid
		}
	}
	key := fmt.Sprintf("dungeon-death:%s:%d:%d:%d:%d", strings.ReplaceAll(i.RunId, "-", ""), i.MazeIndex, i.RoomX, i.RoomY, i.Actor)
	b, err := base64.StdEncoding.DecodeString(i.Body)
	if p.ReceiptKey != key || err != nil || len(b) < 74 || len(b) != 74+14*int(b[30]) || binary.LittleEndian.Uint32(b) != uint32(i.Actor) || binary.LittleEndian.Uint16(b[4:]) != p.CharacterID {
		return i, z, ErrInvalid
	}
	return i, z, nil
}

func (s *Store) CommitTutorialDeath(ctx context.Context, l Lease, p TutorialDeath) (TutorialDeathResult, error) {
	i, z, err := deathFacts(l, p)
	if err != nil {
		return TutorialDeathResult{}, err
	}
	return writeTx(ctx, s.db, func(tx *sql.Tx) (TutorialDeathResult, error) {
		var out TutorialDeathResult
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return out, err
		}
		if r.Phase != TownReady || r.SelectedID != p.CharacterID {
			return out, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, p.CharacterID))
		if err != nil {
			return out, err
		}
		current, err := readOpening(c, p.Opening)
		if err != nil {
			return out, err
		}
		if !current.Initialized || current.Progression.Hp == 0 {
			return out, ErrPhase
		}
		var run, layout, xp sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT run_id::text,dungeon_resource_hash,experience_hash FROM s9a1.opening_entry
			WHERE account=$1 AND owner=$2 AND epoch=$3 AND character_id=$4 AND resource_hash=$5`, l.Account, l.Owner, l.Epoch, p.CharacterID, p.Opening.ResourceHash).Scan(&run, &layout, &xp)
		if errors.Is(err, sql.ErrNoRows) {
			return out, ErrPhase
		}
		if err != nil {
			return out, err
		}
		if run.Valid && (run.String != i.RunId || layout.String != i.ResourceHash || xp.String != i.ExperienceHash) {
			return out, ErrConflict
		}
		var previous string
		err = tx.QueryRowContext(ctx, "SELECT payload::text FROM s9a1.reward_receipts WHERE character_id=$1 AND event_key=$2", p.CharacterID, p.ReceiptKey).Scan(&previous)
		if err == nil {
			var receipt deathReceipt
			if !run.Valid || json.Unmarshal([]byte(previous), &receipt) != nil || receipt.Key != p.ReceiptKey || receipt.Input != p.Input || receipt.Result != p.Result || receipt.Grants == nil || len(receipt.Grants) != 0 || receipt.Revision <= 0 || receipt.Revision > c.Revision {
				return out, ErrConflict
			}
			out.Replayed = true
		} else {
			if !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
			if c.Revision != p.Revision {
				return out, ErrConflict
			}
			if p.Level < c.Level || p.Level > 100 || !json.Valid(p.Stats) || !json.Valid(p.Gameplay) || !bytes.HasPrefix(bytes.TrimSpace(p.Stats), []byte("{")) {
				return out, ErrInvalid
			}
			next := c
			next.Gameplay = p.Gameplay
			profile, err := readOpening(next, p.Opening)
			if err != nil {
				return out, err
			}
			var oldXP, newXP struct{ Progression struct{ Experience uint64 } }
			if json.Unmarshal(c.Gameplay, &oldXP) != nil || json.Unmarshal(p.Gameplay, &newXP) != nil {
				return out, ErrInvalid
			}
			delta := min(uint64(i.ExperienceAward), math.MaxUint64-oldXP.Progression.Experience)
			if uint64(z.ExperienceAward) != delta || newXP.Progression.Experience != oldXP.Progression.Experience+delta ||
				!profile.Initialized || profile.Progression.Hp != current.Progression.Hp || profile.Progression.Fatigue != current.Progression.Fatigue ||
				profile.Progression.FatigueLimit != current.Progression.FatigueLimit || profile.Progression.FatigueDay != current.Progression.FatigueDay ||
				profile.Progression.LuckPoints != current.Progression.LuckPoints || profile.Progression.LuckPlayMinutes != current.Progression.LuckPlayMinutes ||
				(p.Level == c.Level && !equalJSON(c.Stats, p.Stats)) {
				return out, ErrInvalid
			}
			if !run.Valid {
				_, err = tx.ExecContext(ctx, "UPDATE s9a1.opening_entry SET run_id=$2,dungeon_resource_hash=$3,experience_hash=$4 WHERE account=$1", l.Account, i.RunId, i.ResourceHash, i.ExperienceHash)
				if err != nil {
					return out, err
				}
			}
			c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET level=$4,stats_json=$5::json,gameplay_json=$6::json,revision=revision+1
				WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns, l.Account, p.CharacterID, p.Revision, p.Level, string(p.Stats), string(p.Gameplay)))
			if errors.Is(err, sql.ErrNoRows) {
				return out, ErrConflict
			}
			if err != nil {
				return out, err
			}
			body, err := json.Marshal(deathReceipt{Key: p.ReceiptKey, Input: p.Input, Result: p.Result, Revision: c.Revision, Grants: []json.RawMessage{}})
			if err != nil {
				return out, err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)", p.CharacterID, p.ReceiptKey, string(body)); err != nil {
				return out, err
			}
		}
		worn := []Character{c}
		err = loadWorn(ctx, tx, l.Account, worn)
		out.Character = worn[0]
		return out, err
	})
}
