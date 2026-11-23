package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"

	"s9a1/server/internal/ruby"
)

// dungeonCharacter locks the current lease and checks the exact character
// revision and town gate used to plan a dungeon mutation.
func dungeonCharacter(ctx context.Context, tx *sql.Tx, l Lease, expected Character) (Character, error) {
	r, err := lockAccount(ctx, tx, l, true)
	if err != nil {
		return Character{}, err
	}
	if r.SelectedID != expected.ID || r.Phase != TownReady {
		return Character{}, ErrPhase
	}
	c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, expected.ID))
	if err == nil && (c.Revision != expected.Revision || c.Position.Town != expected.Position.Town || c.Position.Area != expected.Position.Area) {
		return Character{}, ErrConflict
	}
	return c, err
}

type dungeonProgression struct {
	Initialized bool
	Progression struct {
		Hp, Mp                      uint32
		Fatigue, FatigueLimit       uint16
		FatigueDay                  int64
		LuckPoints, LuckPlayMinutes int32
	}
}

type DungeonFatigue struct {
	Remaining, Limit uint16
}

// AvailableDungeonFatigue reads the effective 06:00 China Standard Time
// fatigue balance. Only a consuming gameplay transaction records the reset.
func AvailableDungeonFatigue(c Character, now time.Time) (DungeonFatigue, error) {
	var profile dungeonProgression
	if err := json.Unmarshal(c.Gameplay, &profile); err != nil {
		return DungeonFatigue{}, err
	}
	p := profile.Progression
	if p.Fatigue > p.FatigueLimit || p.FatigueDay < 0 {
		return DungeonFatigue{}, ErrInvalid
	}
	day := dungeonFatigueDay(now)
	left := p.Fatigue
	if p.FatigueDay != 0 && day > p.FatigueDay {
		left = p.FatigueLimit
	}
	return DungeonFatigue{left, p.FatigueLimit}, nil
}

func dungeonFatigueDay(now time.Time) int64 {
	date := now.UTC().Add(2 * time.Hour)
	return int64(date.Year()*10000 + int(date.Month())*100 + date.Day())
}

type StormAdmission struct {
	Policy                   string
	Character                uint16
	RunId                    string
	Requested, Dungeon, Seed uint32
	ResourceHash             string
}

func (a StormAdmission) key() string {
	return "storm-lane-entry:" + strings.ReplaceAll(a.RunId, "-", "")
}

func stormRoute(id uint32) bool { return id == 100000003 || id >= 100000008 && id <= 100000150 }

func (s *Store) StormRecent(ctx context.Context, l Lease, c Character) ([]uint32, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) ([]uint32, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return nil, err
		}
		rows, err := tx.QueryContext(ctx, `SELECT event_key,payload::text FROM s9a1.reward_receipts
			WHERE character_id=$1 AND event_key LIKE 'storm-lane-entry:%'
			ORDER BY (payload->>'Revision')::bigint DESC LIMIT 7`, current.ID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []uint32
		for rows.Next() {
			var key, payload string
			if err := rows.Scan(&key, &payload); err != nil {
				return nil, err
			}
			var r deathReceipt
			var a StormAdmission
			if json.Unmarshal([]byte(payload), &r) != nil || json.Unmarshal([]byte(r.Input), &a) != nil ||
				r.Key != key || r.Key != a.key() || r.Revision <= 0 || r.Revision > current.Revision || r.Result != "admitted" ||
				r.Grants == nil || len(r.Grants) != 0 || a.Character != current.ID || !matchesSHA(a.ResourceHash) || !matchesUUID(a.RunId) ||
				!stormRoute(a.Requested) || !stormRoute(a.Dungeon) ||
				a.Policy != "LocalStormLaneRoutes/v2" && (a.Policy != "LocalStormLaneNormalRoutes/v1" || a.Requested > 100000138 || a.Dungeon > 100000138) {
				return nil, ErrConflict
			}
			out = append(out, a.Dungeon)
		}
		return out, rows.Err()
	})
}

func (s *Store) ChargeDungeonFatigue(ctx context.Context, l Lease, expected Character, now time.Time, cost uint16,
	entry bool, pointsPerFatigue int64, storm *StormAdmission, rubyEntry *ruby.Entry) (DungeonFatigue, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (DungeonFatigue, error) {
		c, err := dungeonCharacter(ctx, tx, l, expected)
		if err != nil {
			return DungeonFatigue{}, err
		}
		if storm != nil && (!entry || cost != 8 || storm.Policy != "LocalStormLaneRoutes/v2" || storm.Character != c.ID ||
			!matchesUUID(storm.RunId) || !matchesSHA(storm.ResourceHash) || !stormRoute(storm.Requested) || !stormRoute(storm.Dungeon)) {
			return DungeonFatigue{}, ErrInvalid
		}
		if rubyEntry != nil {
			if !entry || c.Level < 100 || storm != nil {
				return DungeonFatigue{}, ErrInvalid
			}
			if err := requireRubyAdmission(ctx, tx, c, *rubyEntry, now); err != nil {
				return DungeonFatigue{}, err
			}
		}
		fatigue, err := s.chargeDungeonProgress(ctx, tx, l, c, now, cost, entry, pointsPerFatigue, nil)
		if err == nil && storm != nil {
			body, encodeErr := json.Marshal(storm)
			if encodeErr != nil {
				return DungeonFatigue{}, encodeErr
			}
			err = putDungeonReceipt(ctx, tx, c, storm.key(), string(body), "admitted")
		}
		if err == nil && rubyEntry != nil {
			body, encodeErr := json.Marshal(rubyEntry)
			if encodeErr != nil {
				return DungeonFatigue{}, encodeErr
			}
			err = putDungeonReceipt(ctx, tx, c, ruby.EntryKey(rubyEntry.Run), string(body), "admitted")
		}
		return fatigue, err
	})
}

func (s *Store) chargeDungeonProgress(ctx context.Context, tx *sql.Tx, l Lease, c Character, now time.Time,
	cost uint16, entry bool, pointsPerFatigue int64, beforeCharge func() error) (DungeonFatigue, error) {
	var profile dungeonProgression
	if err := json.Unmarshal(c.Gameplay, &profile); err != nil {
		return DungeonFatigue{}, err
	}
	p := profile.Progression
	if !profile.Initialized || p.Hp == 0 {
		return DungeonFatigue{}, ErrPhase
	}
	if p.Fatigue > p.FatigueLimit || p.FatigueDay < 0 || p.LuckPoints < 0 || p.LuckPoints > 100000 || p.LuckPlayMinutes < 0 ||
		pointsPerFatigue < 0 || pointsPerFatigue > math.MaxInt32 {
		return DungeonFatigue{}, ErrInvalid
	}
	day := dungeonFatigueDay(now)
	fatigue := p.Fatigue
	if p.FatigueDay != 0 && day > p.FatigueDay {
		fatigue = p.FatigueLimit
	}
	if entry && cost != 0 && fatigue == 0 {
		return DungeonFatigue{}, ErrFunds
	}
	if beforeCharge != nil {
		if err := beforeCharge(); err != nil {
			return DungeonFatigue{}, err
		}
	}
	charged := min(fatigue, cost)
	fatigue -= charged
	luck := int64(p.LuckPoints)
	if award := int64(charged) * pointsPerFatigue; award != 0 {
		switch {
		case luck == 100000 || award == 100000:
			luck = 100000
		default:
			luck = min(99999, luck+award)
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE s9a1.characters SET gameplay_json=
			jsonb_set(gameplay_json::jsonb, '{Progression}', gameplay_json::jsonb->'Progression' ||
				jsonb_build_object('FatigueDay',$4::bigint,'Fatigue',$5::int,'LuckPoints',$6::int))::json,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, c.ID, c.Revision, max(day, p.FatigueDay), fatigue, luck)
	if err == nil && charged != 0 {
		_, err = s.recordBattlePass(ctx, tx, l.Account, 0, false, now, BattlePassEvent{Type: 3, Count: int(charged), Parameter: -1})
	}
	return DungeonFatigue{fatigue, p.FatigueLimit}, err
}

func dungeonVitals(c Character) (int64, int64, error) {
	var stats struct{ Values map[string]int64 }
	if err := json.Unmarshal(c.Stats, &stats); err != nil {
		return 0, 0, err
	}
	hp, mp := stats.Values["hp_max"], stats.Values["mp_max"]
	if hp <= 0 || hp > math.MaxUint32 || mp < 0 || mp > math.MaxUint32 {
		return 0, 0, ErrInvalid
	}
	return hp, mp, nil
}

// ReturnDungeon clears the city display state and restores vitals only when a
// real run has been admitted. Cancelling a selector keeps the saved vitals.
func (s *Store) ReturnDungeon(ctx context.Context, l Lease, expected Character, run bool) error {
	return s.returnDungeonWithProof(ctx, l, expected, run, nil)
}

func (s *Store) returnDungeonWithProof(ctx context.Context, l Lease, expected Character, run bool,
	proof func(*sql.Tx, Character) error) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		c, err := dungeonCharacter(ctx, tx, l, expected)
		if err != nil {
			return struct{}{}, err
		}
		if proof != nil {
			if err := proof(tx, c); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, returnDungeon(ctx, tx, l, c, run)
	})
	return err
}

func returnDungeon(ctx context.Context, tx *sql.Tx, l Lease, c Character, run bool) error {
	if run {
		hp, mp, err := dungeonVitals(c)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE s9a1.characters SET gameplay_json=
				jsonb_set(gameplay_json::jsonb, '{Progression}', gameplay_json::jsonb->'Progression' || jsonb_build_object('Hp',$4::bigint,'Mp',$5::bigint))::json,
				state=0,revision=revision+1 WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, c.ID, c.Revision, hp, mp)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE s9a1.characters SET state=0,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, c.ID, c.Revision)
	return err
}
