package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const OculusEntryPolicy = "OfficialSharedOculusEntryCnSchedule/v2"

type OculusEntry struct {
	Policy, Owner                                        string
	Epoch                                                int64
	CharacterId                                          uint16
	SelectionGeneration                                  int64
	RunId                                                string
	Mode                                                 uint16
	Dungeon                                              uint32
	ModeHash, DungeonHash                                string
	Maze                                                 byte
	Seed                                                 uint32
	ActorFirst                                           uint16
	StartX, StartY                                       byte
	StartMap                                             uint32
	EnteredAt                                            time.Time
	Day, Week                                            int64
	DailyOrdinal, WeeklyOrdinal, DailyLimit, WeeklyLimit byte
	FatigueCost, FatigueBefore, FatigueAfter             uint16
	DailyTicket                                          int64 `json:",omitempty"`
	WeeklyTicket                                         int64 `json:",omitempty"`
}

func (p OculusEntry) MarshalJSON() ([]byte, error) {
	type wire OculusEntry
	p.EnteredAt = p.EnteredAt.UTC().Truncate(100 * time.Nanosecond)
	b, err := json.Marshal(wire(p))
	if err != nil {
		return nil, err
	}
	old := `"EnteredAt":"` + p.EnteredAt.Format(time.RFC3339Nano) + `"`
	legacy := `"EnteredAt":"` + p.EnteredAt.Format("2006-01-02T15:04:05.9999999+00:00") + `"`
	return bytes.Replace(b, []byte(old), []byte(legacy), 1), nil
}

func (p OculusEntry) Key() string { return "oculus-entry:run:" + strings.ReplaceAll(p.RunId, "-", "") }

type OculusCounts struct {
	Day, Week             int64
	DailyUsed, WeeklyUsed byte
}

func (s *Store) OculusRewardWeek(ctx context.Context, l Lease, c Character, now time.Time) (int64, bool, error) {
	_, week, used, err := s.OculusCountsView(ctx, l, c, now)
	return week, used, err
}

type oculusBinding struct {
	Week        int64
	CharacterId uint16
	EntryInput  string
	Legacy      bool
}

type oculusAllowance struct {
	entry                         OculusEntry
	receipt                       deathReceipt
	releasedDaily, releasedWeekly bool
	ended                         bool
}

func OculusPeriods(now time.Time) (int64, int64) {
	t := now.UTC().Add(2 * time.Hour)
	w := t.AddDate(0, 0, -((7 + int(t.Weekday()) - int(time.Thursday)) % 7))
	return int64(t.Year()*10000 + int(t.Month())*100 + t.Day()), int64(w.Year()*10000 + int(w.Month())*100 + w.Day())
}

func oculusDungeon(mode uint16) uint32 {
	switch mode {
	case 0:
		return 100000007
	case 2:
		return 400000389
	case 6:
		return 100002669
	}
	return 0
}

func readOculusLedger(ctx context.Context, tx *sql.Tx, c Character, now time.Time) (OculusCounts, int64, int64, error) {
	day, week := OculusPeriods(now)
	z := OculusCounts{Day: day, Week: week}
	rows, err := tx.QueryContext(ctx, `SELECT event_key,payload::text FROM s9a1.reward_receipts WHERE character_id=$1 AND
		(event_key LIKE 'oculus-entry:run:%' OR event_key LIKE 'oculus-entry:end:%' OR event_key LIKE 'oculus-entry:credit:%')
		ORDER BY (payload->>'Revision')::bigint`, c.ID)
	if err != nil {
		return z, 0, 0, err
	}
	defer rows.Close()
	ledger := map[string]*oculusAllowance{}
	var revision int64
	for rows.Next() {
		var key, raw string
		if err = rows.Scan(&key, &raw); err != nil {
			return z, 0, 0, err
		}
		var r deathReceipt
		if json.Unmarshal([]byte(raw), &r) != nil || r.Key != key || r.Revision <= revision || r.Revision > c.Revision || r.Grants == nil || len(r.Grants) != 0 {
			return z, 0, 0, ErrConflict
		}
		revision = r.Revision
		if strings.HasPrefix(key, "oculus-entry:run:") {
			var e OculusEntry
			if json.Unmarshal([]byte(r.Input), &e) != nil || !matchesUUID(e.RunId) || e.Key() != key || e.Dungeon == 0 || e.Dungeon != oculusDungeon(e.Mode) ||
				e.CharacterId != c.ID || e.Epoch <= 0 || e.DailyOrdinal == 0 || e.WeeklyOrdinal == 0 || e.FatigueBefore < e.FatigueCost ||
				e.FatigueAfter != e.FatigueBefore-e.FatigueCost || r.Result != "admitted" || ledger[e.RunId] != nil ||
				e.Policy != OculusEntryPolicy && e.Policy != "OfficialSharedOculusEntryCnSchedule/v1" {
				return z, 0, 0, ErrConflict
			}
			ledger[e.RunId] = &oculusAllowance{entry: e, receipt: r}
			z.Day, z.Week = max(z.Day, e.Day), max(z.Week, e.Week)
			continue
		}
		var release struct {
			Policy, RunId, EntryInput   string
			EntryRevision, Day, Week    int64
			Reason                      json.RawMessage
			ReleaseDaily, ReleaseWeekly bool
			Proof                       struct{ RemainingMilliseconds int64 }
		}
		if json.Unmarshal([]byte(r.Input), &release) != nil {
			return z, 0, 0, ErrConflict
		}
		source := ledger[release.RunId]
		if source == nil || release.EntryRevision != source.receipt.Revision || release.EntryInput != source.receipt.Input {
			return z, 0, 0, ErrConflict
		}
		if strings.HasPrefix(key, "oculus-entry:end:") {
			var reason byte
			if release.Policy != OculusEntryPolicy || r.Result != "ended" || source.ended || json.Unmarshal(release.Reason, &reason) != nil || reason > 1 {
				return z, 0, 0, ErrConflict
			}
			source.ended = true
			if reason == 0 {
				if release.Proof.RemainingMilliseconds != 0 || source.releasedDaily || source.releasedWeekly {
					return z, 0, 0, ErrConflict
				}
				source.releasedDaily, source.releasedWeekly = true, true
			}
		} else {
			if release.Policy != "OfflineOculusEntryAllowance/v1" || r.Result != "entry-allowance-restored" ||
				!release.ReleaseDaily && !release.ReleaseWeekly || release.ReleaseDaily && source.releasedDaily || release.ReleaseWeekly && source.releasedWeekly {
				return z, 0, 0, ErrConflict
			}
			source.releasedDaily = source.releasedDaily || release.ReleaseDaily
			source.releasedWeekly = source.releasedWeekly || release.ReleaseWeekly
		}
		z.Day, z.Week = max(z.Day, release.Day), max(z.Week, release.Week)
	}
	if err = rows.Err(); err != nil {
		return z, 0, 0, err
	}
	var dt, wt int64
	for _, s := range ledger {
		e := s.entry
		if e.Day == z.Day {
			dt = max(dt, e.DailyTicket, int64(e.DailyOrdinal))
			if !s.releasedDaily {
				if z.DailyUsed == 127 {
					return z, 0, 0, ErrConflict
				}
				z.DailyUsed++
			}
		}
		if e.Week == z.Week {
			wt = max(wt, e.WeeklyTicket, int64(e.WeeklyOrdinal))
			if !s.releasedWeekly {
				if z.WeeklyUsed == 127 {
					return z, 0, 0, ErrConflict
				}
				z.WeeklyUsed++
			}
		}
	}
	return z, dt, wt, nil
}

func (s *Store) OculusStatus(ctx context.Context, l Lease, c Character, mode uint16, need uint16, daily, weekly byte, now time.Time) (OculusCounts, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (OculusCounts, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return OculusCounts{}, err
		}
		z, _, _, err := readOculusLedger(ctx, tx, current, now)
		if err != nil {
			return z, err
		}
		_, _, err = checkOculusAllowance(ctx, tx, l, current, z, mode, need, daily, weekly, now)
		return z, err
	})
}

func checkOculusAllowance(ctx context.Context, tx *sql.Tx, l Lease, c Character, z OculusCounts, mode uint16, need uint16, daily, weekly byte, now time.Time) (DungeonFatigue, []oculusBinding, error) {
	if z.DailyUsed >= daily || z.WeeklyUsed >= weekly {
		return DungeonFatigue{}, nil, ErrCapacity
	}
	var p struct {
		dungeonProgression
		Quests struct{ Cleared map[uint16]byte }
	}
	if err := json.Unmarshal(c.Gameplay, &p); err != nil {
		return DungeonFatigue{}, nil, err
	}
	_, eligible := p.Quests.Cleared[12167]
	if !p.Initialized || !eligible || c.Level != 100 || p.Progression.Hp == 0 {
		return DungeonFatigue{}, nil, ErrPhase
	}
	f := p.Progression
	left := f.Fatigue
	day, _ := OculusPeriods(now)
	if f.Fatigue > f.FatigueLimit || f.FatigueDay < 0 {
		return DungeonFatigue{}, nil, ErrInvalid
	}
	if f.FatigueDay != 0 && day > f.FatigueDay {
		left = f.FatigueLimit
	}
	if need == 0 || left < need {
		return DungeonFatigue{}, nil, ErrFunds
	}
	var bindings []oculusBinding
	if mode == 2 {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT oculus_guide_bindings_json::text FROM s9a1.accounts WHERE account=$1`, l.Account).Scan(&raw); err != nil {
			return DungeonFatigue{}, nil, err
		}
		if ValidateOculusBindings([]byte(raw)) != nil || json.Unmarshal([]byte(raw), &bindings) != nil {
			return DungeonFatigue{}, nil, ErrConflict
		}
		count, bound := 0, false
		for _, b := range bindings {
			if b.Week > z.Week {
				return DungeonFatigue{}, nil, ErrConflict
			}
			if b.Week == z.Week {
				count++
				bound = bound || b.CharacterId == c.ID
			}
		}
		if count >= 4 && !bound {
			return DungeonFatigue{}, nil, ErrConflict
		}
	}
	return DungeonFatigue{left, f.FatigueLimit}, bindings, nil
}

func (s *Store) AdmitOculus(ctx context.Context, l Lease, c Character, e OculusEntry, now time.Time) (OculusEntry, OculusCounts, DungeonFatigue, error) {
	type admitted struct {
		Entry   OculusEntry
		Counts  OculusCounts
		Fatigue DungeonFatigue
	}
	out, err := writeTx(ctx, s.db, func(tx *sql.Tx) (admitted, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return admitted{}, err
		}
		if e.Policy != OculusEntryPolicy || e.Owner != l.Owner || e.Epoch != l.Epoch || e.CharacterId != current.ID ||
			e.SelectionGeneration <= 0 || !matchesUUID(e.RunId) || e.Dungeon != oculusDungeon(e.Mode) || e.Dungeon == 0 || !matchesSHA(e.ModeHash) ||
			!matchesSHA(e.DungeonHash) || e.StartMap == 0 || e.ActorFirst < 402 || e.DailyLimit > 127 || e.WeeklyLimit > 127 {
			return admitted{}, ErrInvalid
		}
		count, dt, wt, err := readOculusLedger(ctx, tx, current, now)
		if err != nil {
			return admitted{}, err
		}
		f, bind, err := checkOculusAllowance(ctx, tx, l, current, count, e.Mode, e.FatigueCost, e.DailyLimit, e.WeeklyLimit, now)
		if err != nil {
			return admitted{}, err
		}
		e.EnteredAt = now.UTC()
		e.Day, e.Week = count.Day, count.Week
		e.DailyOrdinal, e.WeeklyOrdinal = count.DailyUsed+1, count.WeeklyUsed+1
		e.DailyTicket, e.WeeklyTicket = dt+1, wt+1
		e.FatigueBefore, e.FatigueAfter = f.Remaining, f.Remaining-e.FatigueCost
		body, err := json.Marshal(e)
		if err != nil {
			return admitted{}, err
		}
		if e.Mode == 2 {
			known := false
			for _, b := range bind {
				known = known || b.Week == e.Week && b.CharacterId == current.ID
			}
			if !known {
				bind = append(bind, oculusBinding{e.Week, current.ID, string(body), false})
				if _, err = tx.ExecContext(ctx, `UPDATE s9a1.accounts SET oculus_guide_bindings_json=$2::jsonb WHERE account=$1`, l.Account, string(mustDungeonJSON(bind))); err != nil {
					return admitted{}, err
				}
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE s9a1.characters SET gameplay_json=jsonb_set(gameplay_json::jsonb,'{Progression}',
			gameplay_json::jsonb->'Progression' || jsonb_build_object('Fatigue',$4::int,'FatigueDay',
			GREATEST($5::bigint,COALESCE((gameplay_json::jsonb->'Progression'->>'FatigueDay')::bigint,0))))::json,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, current.ID, current.Revision, e.FatigueAfter, e.Day)
		if err != nil {
			return admitted{}, err
		}
		for _, key := range []string{e.Key(), fmt.Sprintf("oculus-entry:day:100000007:%d:%d", e.Day, e.DailyTicket), fmt.Sprintf("oculus-entry:week:100000007:%d:%d", e.Week, e.WeeklyTicket)} {
			if err = putDungeonReceipt(ctx, tx, current, key, string(body), "admitted"); err != nil {
				return admitted{}, err
			}
		}
		if _, err = s.recordBattlePass(ctx, tx, l.Account, 0, false, now, BattlePassEvent{Type: 3, Count: int(e.FatigueCost), Parameter: -1}); err != nil {
			return admitted{}, err
		}
		count.DailyUsed, count.WeeklyUsed = e.DailyOrdinal, e.WeeklyOrdinal
		f.Remaining = e.FatigueAfter
		return admitted{e, count, f}, nil
	})
	return out.Entry, out.Counts, out.Fatigue, err
}

type OculusRitualReduction struct {
	Actor       uint16
	Monster     uint32
	DeathKey    string
	Percent     int
	AtTimestamp int64
}

type OculusRitualProof struct {
	Policy, RunId                    string
	Mode                             uint16
	Dungeon                          uint32
	ResourceHash                     string
	StartedAt, ObservedAt, Frequency int64
	LimitSeconds                     int
	Reductions                       []OculusRitualReduction
	RemainingMilliseconds            int64
}

func (p OculusRitualProof) MarshalJSON() ([]byte, error) {
	type wire OculusRitualProof
	return json.Marshal(struct {
		wire
		Expired bool
	}{wire(p), p.RemainingMilliseconds == 0})
}

func (p OculusRitualProof) ValidClock() bool { return validOculusRitualClock(p) }

func verifyOculusRitual(ctx context.Context, tx *sql.Tx, c Character, e OculusEntry, p OculusRitualProof) (bool, error) {
	if p.RunId != e.RunId || p.Mode != e.Mode || p.Dungeon != e.Dungeon || p.ResourceHash != e.ModeHash ||
		p.Policy != "OculusCommittedRitualClock/v1" || !p.ValidClock() {
		return false, ErrConflict
	}
	for _, r := range p.Reductions {
		death, err := readDungeonReceipt(ctx, tx, c, r.DeathKey)
		if err != nil {
			return false, err
		}
		var src struct {
			RunId string
			Actor uint16
			Code  uint32
		}
		if json.Unmarshal([]byte(death.Input), &src) != nil || src.RunId != e.RunId || src.Actor != r.Actor || src.Code != r.Monster {
			return false, ErrConflict
		}
	}
	return p.RemainingMilliseconds == 0, nil
}

func (s *Store) EndOculus(ctx context.Context, l Lease, c Character, e OculusEntry, p OculusRitualProof, timeout bool, now time.Time) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return struct{}{}, err
		}
		prior, err := readDungeonReceipt(ctx, tx, current, e.Key())
		if err != nil {
			return struct{}{}, err
		}
		var saved OculusEntry
		if json.Unmarshal([]byte(prior.Input), &saved) != nil || saved.Key() != e.Key() || saved.Owner != l.Owner || saved.Epoch != l.Epoch ||
			saved.Mode != e.Mode || saved.ModeHash != e.ModeHash || saved.Dungeon != e.Dungeon || saved.CharacterId != current.ID ||
			prior.Result != "admitted" {
			return struct{}{}, ErrConflict
		}
		expired, err := verifyOculusRitual(ctx, tx, current, e, p)
		if err != nil {
			return struct{}{}, err
		}
		if timeout != expired {
			return struct{}{}, ErrConflict
		}
		count, _, _, err := readOculusLedger(ctx, tx, current, now)
		if err != nil {
			return struct{}{}, err
		}
		reason := byte(1)
		if timeout {
			reason = 0
		}
		body, err := json.Marshal(struct {
			Policy, RunId string
			EntryRevision int64
			EntryInput    string
			Reason        byte
			EndedAt       time.Time
			Day, Week     int64
			Proof         OculusRitualProof
		}{OculusEntryPolicy, e.RunId, prior.Revision, prior.Input, reason, now.UTC(), count.Day, count.Week, p})
		if err != nil {
			return struct{}{}, err
		}
		hp, mp, err := dungeonVitals(current)
		if err != nil {
			return struct{}{}, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE s9a1.characters SET gameplay_json=
			jsonb_set(gameplay_json::jsonb,'{Progression}',gameplay_json::jsonb->'Progression' || jsonb_build_object('Hp',$4::bigint,'Mp',$5::bigint))::json,
			state=0,revision=revision+1 WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, current.ID, current.Revision, hp, mp)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, putDungeonReceipt(ctx, tx, current, "oculus-entry:end:"+strings.ReplaceAll(e.RunId, "-", ""), string(body), "ended")
	})
	return err
}
