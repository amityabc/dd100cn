package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const AbaddonPolicy = "LocalAbaddonAdmission/v1"

type AbaddonEntry struct {
	Policy    string
	Character uint16
	Run       string
	Dungeon   uint32
	Day       int64
	Seed      uint32
	Hash      string
	Fatigue   uint16
}

func (e AbaddonEntry) Key() string { return "abaddon-entry:" + strings.ReplaceAll(e.Run, "-", "") }
func (e AbaddonEntry) valid() bool {
	return e.Policy == AbaddonPolicy && e.Character != 0 && e.Dungeon >= 100000157 && e.Dungeon <= 100000161 &&
		matchesUUID(e.Run) && matchesSHA(e.Hash) && e.Day >= 20000101 &&
		(e.Fatigue == 0 || e.Fatigue == 10 && e.Dungeon > 100000158)
}

type AbaddonRoom struct {
	Run      string
	X, Y     byte
	Death    string
	Terminal bool
}

func (r AbaddonRoom) key() string {
	return fmt.Sprintf("abaddon-room:%s:%d:%d", strings.ReplaceAll(r.Run, "-", ""), r.X, r.Y)
}

type AbaddonFixedReward struct {
	SourceHash string
	Template   json.RawMessage
	Count      uint32
}

type AbaddonMailReward struct {
	Floor                byte
	Table, Sender, Title string
	Reward               AbaddonFixedReward
}

type AbaddonMail struct {
	DeliveryKey string
	MailId      int64
	Reward      AbaddonMailReward
}

type AbaddonClearTimes struct{ Dungeon, Elapsed, Best uint32 }

type AbaddonProgress struct {
	Id                            uint16
	Slot                          byte
	Name                          string
	Floors                        byte
	Registered, Charged, Rewarded bool
}

type abaddonHistory struct {
	progress AbaddonProgress
	receipts map[string]deathReceipt
}

func readAbaddonHistory(ctx context.Context, tx *sql.Tx, account string) (map[uint16]*abaddonHistory, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.wire_id,c.storage_slot,c.name,c.revision,COALESCE(r.event_key,''),COALESCE(r.payload::text,'')
		FROM s9a1.characters c LEFT JOIN s9a1.reward_receipts r ON r.character_id=c.wire_id AND
		(r.event_key LIKE 'abaddon-entry:%' OR r.event_key LIKE 'abaddon-room:%' OR r.event_key LIKE 'abaddon-fixed:%'
		 OR r.event_key LIKE 'dungeon-settlement:%:result')
		WHERE c.account=$1 ORDER BY c.storage_slot,r.event_key`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := map[uint16]*abaddonHistory{}
	for rows.Next() {
		var c AbaddonProgress
		var revision int64
		var key, raw string
		if err := rows.Scan(&c.Id, &c.Slot, &c.Name, &revision, &key, &raw); err != nil {
			return nil, err
		}
		h := history[c.Id]
		if h == nil {
			h = &abaddonHistory{progress: c, receipts: map[string]deathReceipt{}}
			history[c.Id] = h
		}
		if key == "" {
			continue
		}
		var receipt deathReceipt
		if json.Unmarshal([]byte(raw), &receipt) != nil || receipt.Key != key || receipt.Revision <= 0 || receipt.Revision > revision {
			return nil, ErrConflict
		}
		h.receipts[key] = receipt
	}
	return history, rows.Err()
}

func readAbaddonEntry(receipt deathReceipt) (AbaddonEntry, error) {
	var e AbaddonEntry
	if json.Unmarshal([]byte(receipt.Input), &e) != nil || !e.valid() || receipt.Key != e.Key() ||
		receipt.Result != "admitted" || receipt.Grants == nil || len(receipt.Grants) != 0 {
		return e, ErrConflict
	}
	return e, nil
}

func abaddonDay(history map[uint16]*abaddonHistory, now time.Time) (int64, error) {
	t := now.UTC().Add(2 * time.Hour)
	day := int64(t.Year()*10000 + int(t.Month())*100 + t.Day())
	for _, h := range history {
		for key, r := range h.receipts {
			if !strings.HasPrefix(key, "abaddon-entry:") {
				continue
			}
			e, err := readAbaddonEntry(r)
			if err != nil || e.Character != h.progress.Id {
				return 0, ErrConflict
			}
			day = max(day, e.Day)
		}
	}
	return day, nil
}

func (h *abaddonHistory) state(day int64) (AbaddonProgress, error) {
	p := h.progress
	floors := [11]bool{}
	for key, r := range h.receipts {
		switch {
		case strings.HasPrefix(key, "abaddon-entry:"):
			entry, err := readAbaddonEntry(r)
			if err != nil {
				return p, err
			}
			if entry.Day == day {
				p.Registered = p.Registered || entry.Dungeon <= 100000158
				p.Charged = p.Charged || entry.Fatigue == 10
			}
		case strings.HasPrefix(key, "abaddon-room:"):
			var room AbaddonRoom
			if json.Unmarshal([]byte(r.Input), &room) != nil || key != room.key() || r.Result != "cleared;item-selection-paused" {
				return p, ErrConflict
			}
			entry, err := readAbaddonEntry(h.receipts[(AbaddonEntry{Run: room.Run}).Key()])
			if err != nil {
				return p, err
			}
			if entry.Day == day && entry.Dungeon <= 100000158 && room.Y == 0 && room.X >= 1 && room.X <= 10 && !floors[room.X] {
				floors[room.X] = true
				p.Floors++
			}
		case key == fmt.Sprintf("abaddon-fixed:%d", day):
			if r.Result != "mailed-fixed-material" && r.Result != "claimed-fixed-material" {
				return p, ErrConflict
			}
			p.Rewarded = true
		}
	}
	return p, nil
}

func (s *Store) AbaddonRoster(ctx context.Context, l Lease, now time.Time) ([]AbaddonProgress, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) ([]AbaddonProgress, error) {
		if _, err := lockAccount(ctx, tx, l, false); err != nil {
			return nil, err
		}
		history, err := readAbaddonHistory(ctx, tx, l.Account)
		if err != nil {
			return nil, err
		}
		day, err := abaddonDay(history, now)
		if err != nil {
			return nil, err
		}
		out := make([]AbaddonProgress, 0, len(history))
		for _, h := range history {
			p, err := h.state(day)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return out, nil
	})
}

func (s *Store) AdmitAbaddon(ctx context.Context, l Lease, expected Character, entry AbaddonEntry, now time.Time) (AbaddonEntry, DungeonFatigue, error) {
	type result struct {
		Entry   AbaddonEntry
		Fatigue DungeonFatigue
	}
	z, err := writeTx(ctx, s.db, func(tx *sql.Tx) (result, error) {
		c, err := dungeonCharacter(ctx, tx, l, expected)
		if err != nil {
			return result{}, err
		}
		history, err := readAbaddonHistory(ctx, tx, l.Account)
		if err != nil {
			return result{}, err
		}
		day, err := abaddonDay(history, now)
		if err != nil {
			return result{}, err
		}
		entry.Day = day
		if !entry.valid() || entry.Character != c.ID || history[c.ID] == nil {
			return result{}, ErrInvalid
		}
		self, err := history[c.ID].state(day)
		if err != nil {
			return result{}, err
		}
		if entry.Dungeon <= 100000158 && !self.Registered {
			count := 0
			for _, h := range history {
				p, err := h.state(day)
				if err != nil {
					return result{}, err
				}
				if p.Registered {
					count++
				}
			}
			if count >= 3 {
				return result{}, ErrPhase
			}
		}
		if entry.Dungeon > 100000158 && !self.Charged {
			entry.Fatigue = 10
		}
		var p dungeonProgression
		if json.Unmarshal(c.Gameplay, &p) != nil || !p.Initialized || p.Progression.Hp == 0 {
			return result{}, ErrPhase
		}
		f := p.Progression
		if f.Fatigue > f.FatigueLimit || f.FatigueDay < 0 {
			return result{}, ErrInvalid
		}
		date := now.UTC().Add(2 * time.Hour)
		reset := int64(date.Year()*10000 + int(date.Month())*100 + date.Day())
		fatigue := f.Fatigue
		if f.FatigueDay != 0 && reset > f.FatigueDay {
			fatigue = f.FatigueLimit
		}
		if fatigue < entry.Fatigue {
			return result{}, ErrFunds
		}
		fatigue -= entry.Fatigue
		_, err = tx.ExecContext(ctx, `UPDATE s9a1.characters SET gameplay_json=
			jsonb_set(gameplay_json::jsonb, '{Progression}', gameplay_json::jsonb->'Progression' ||
				jsonb_build_object('FatigueDay',$4::bigint,'Fatigue',$5::int))::json,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, c.ID, c.Revision, max(reset, f.FatigueDay), fatigue)
		if err != nil {
			return result{}, err
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			return result{}, err
		}
		if err = putDungeonReceipt(ctx, tx, c, entry.Key(), string(raw), "admitted"); err != nil {
			return result{}, err
		}
		if entry.Fatigue != 0 {
			if _, err = s.recordBattlePass(ctx, tx, l.Account, 0, false, now, BattlePassEvent{Type: 3, Count: int(entry.Fatigue), Parameter: -1}); err != nil {
				return result{}, err
			}
		}
		return result{entry, DungeonFatigue{fatigue, f.FatigueLimit}}, nil
	})
	return z.Entry, z.Fatigue, err
}

func checkAbaddonEntry(ctx context.Context, tx *sql.Tx, c Character, e AbaddonEntry) error {
	receipt, err := readDungeonReceipt(ctx, tx, c, e.Key())
	if err != nil {
		return err
	}
	got, err := readAbaddonEntry(receipt)
	if err != nil || got != e {
		return ErrConflict
	}
	return nil
}

func (s *Store) AbaddonSettlement(ctx context.Context, l Lease, c Character, e AbaddonEntry, elapsed uint32, row *AbaddonMailReward) (AbaddonClearTimes, *AbaddonMail, error) {
	type response struct {
		Times AbaddonClearTimes
		Mail  *AbaddonMail
	}
	z, err := writeTx(ctx, s.db, func(tx *sql.Tx) (response, error) {
		if elapsed > math.MaxInt32 {
			return response{}, ErrInvalid
		}
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return response{}, err
		}
		if err = checkAbaddonEntry(ctx, tx, current, e); err != nil {
			return response{}, err
		}
		history, err := readAbaddonHistory(ctx, tx, l.Account)
		if err != nil {
			return response{}, err
		}
		best := elapsed
		for _, h := range history {
			for key, rec := range h.receipts {
				if !strings.HasPrefix(key, "dungeon-settlement:") {
					continue
				}
				var input struct {
					Dungeon             uint32
					RunId, ResourceHash string
					ElapsedMilliseconds uint32
				}
				if json.Unmarshal([]byte(rec.Input), &input) != nil || input.Dungeon != e.Dungeon {
					continue
				}
				entryReceipt := h.receipts[(AbaddonEntry{Run: input.RunId}).Key()]
				record, err := readAbaddonEntry(entryReceipt)
				if err != nil || record.Hash != input.ResourceHash || entryReceipt.Revision >= rec.Revision {
					return response{}, ErrConflict
				}
				var result struct {
					AbaddonTimes        *AbaddonClearTimes
					ElapsedMilliseconds uint32
				}
				if json.Unmarshal([]byte(rec.Result), &result) != nil || result.ElapsedMilliseconds != input.ElapsedMilliseconds {
					return response{}, ErrConflict
				}
				if times := result.AbaddonTimes; times != nil {
					if times.Dungeon != e.Dungeon || times.Elapsed != result.ElapsedMilliseconds || times.Best > times.Elapsed {
						return response{}, ErrConflict
					}
					best = min(best, times.Best)
				} else {
					best = min(best, result.ElapsedMilliseconds)
				}
			}
		}
		times := AbaddonClearTimes{e.Dungeon, elapsed, best}
		if row == nil {
			return response{Times: times}, nil
		}
		key := fmt.Sprintf("abaddon-fixed:%d", e.Day)
		if receipt, err := readDungeonReceipt(ctx, tx, current, key); err == nil {
			if receipt.Result != "mailed-fixed-material" && receipt.Result != "claimed-fixed-material" {
				return response{}, ErrConflict
			}
			return response{Times: times}, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return response{}, err
		}
		delivery := fmt.Sprintf("abaddon-final-mail:%d", e.Day)
		mail := &AbaddonMail{DeliveryKey: delivery, MailId: mailIdentity(fmt.Sprintf("mail:%d:%s", c.ID, delivery)), Reward: *row}
		return response{times, mail}, nil
	})
	return z.Times, z.Mail, err
}

type AbaddonMutation struct {
	Entry  AbaddonEntry
	Room   *AbaddonRoom
	Reward *AbaddonMailReward
	Final  *AbaddonMail
	Now    time.Time
}

func applyAbaddonMutation(ctx context.Context, tx *sql.Tx, c, next Character, source string, m *AbaddonMutation) (Character, error) {
	if m == nil {
		return next, nil
	}
	if err := checkAbaddonEntry(ctx, tx, c, m.Entry); err != nil {
		return next, err
	}
	if m.Room != nil {
		if m.Room.Run != m.Entry.Run || m.Room.Death != source {
			return next, ErrInvalid
		}
		raw, err := json.Marshal(m.Room)
		if err != nil {
			return next, err
		}
		if err = putDungeonReceipt(ctx, tx, c, m.Room.key(), string(raw), "cleared;item-selection-paused"); err != nil {
			return next, err
		}
		if m.Reward == nil {
			return next, nil
		}
		if m.Entry.Dungeon > 100000158 || m.Room.Y != 0 || m.Room.X < 5 || m.Room.X > 10 || m.Reward.Floor != m.Room.X {
			return next, ErrInvalid
		}
		key := fmt.Sprintf("abaddon-floor-mail:%d:%d", m.Entry.Day, m.Reward.Floor)
		if prior, err := readDungeonReceipt(ctx, tx, c, key); err == nil {
			if prior.Result != "mailed-fixed-floor" {
				return next, ErrConflict
			}
			return next, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return next, err
		}
		mail := AbaddonMail{key, mailIdentity(fmt.Sprintf("mail:%d:%s", c.ID, key)), *m.Reward}
		next, err = deliverAbaddonMail(next, mail, m.Now)
		if err != nil {
			return next, err
		}
		body, err := json.Marshal(struct {
			Entry AbaddonEntry
			Death string
			Mail  AbaddonMail
		}{m.Entry, source, mail})
		if err != nil {
			return next, err
		}
		return next, putDungeonReceipt(ctx, tx, c, key, string(body), "mailed-fixed-floor")
	}
	if m.Final != nil {
		if m.Entry.Dungeon <= 100000158 || m.Final.Reward.Floor != 0 ||
			m.Final.DeliveryKey != fmt.Sprintf("abaddon-final-mail:%d", m.Entry.Day) {
			return next, ErrInvalid
		}
		var err error
		next, err = deliverAbaddonMail(next, *m.Final, m.Now)
		if err != nil {
			return next, err
		}
		body, err := json.Marshal(struct {
			Entry     AbaddonEntry
			ResultKey string
			Reward    AbaddonFixedReward
		}{m.Entry, source, m.Final.Reward.Reward})
		if err != nil {
			return next, err
		}
		return next, putDungeonReceipt(ctx, tx, c, fmt.Sprintf("abaddon-fixed:%d", m.Entry.Day), string(body), "mailed-fixed-material")
	}
	return next, nil
}

func deliverAbaddonMail(c Character, mail AbaddonMail, now time.Time) (Character, error) {
	r := mail.Reward
	item := uint32(490014289)
	if r.Floor == 0 {
		item = 10313060
	}
	var t ordinaryTemplate
	if !matchesSHA(r.Reward.SourceHash) || r.Table == "" || r.Sender == "" || r.Title == "" || now.IsZero() ||
		mail.MailId != mailIdentity(fmt.Sprintf("mail:%d:%s", c.ID, mail.DeliveryKey)) ||
		json.Unmarshal(r.Reward.Template, &t) != nil || t.TemplateID != item || t.Kind != 2 || r.Reward.Count == 0 || r.Reward.Count > t.StackLimit {
		return c, ErrInvalid
	}
	next, created, err := ActivityRewardMail(c, mail.DeliveryKey, r.Sender, r.Title, r.Title, r.Reward.Template, r.Reward.Count, now)
	if err == nil && !created {
		err = ErrConflict
	}
	return next, err
}

func (s *Store) AbaddonFloorMail(ctx context.Context, l Lease, c Character, e AbaddonEntry, floor byte, source string) (*AbaddonMail, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (*AbaddonMail, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("abaddon-floor-mail:%d:%d", e.Day, floor)
		receipt, err := readDungeonReceipt(ctx, tx, current, key)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var claim struct {
			Entry AbaddonEntry
			Death string
			Mail  AbaddonMail
		}
		if json.Unmarshal([]byte(receipt.Input), &claim) != nil || receipt.Result != "mailed-fixed-floor" || claim.Mail.DeliveryKey != key {
			return nil, ErrConflict
		}
		if claim.Death != source {
			return nil, nil
		}
		if claim.Entry != e {
			return nil, ErrConflict
		}
		return &claim.Mail, nil
	})
}
