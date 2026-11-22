package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"s9a1/server/internal/ruby"
)

type WisdomPass struct{ Item, Count uint32 }

const WisdomDungeonID uint32 = 100000151
const WisdomEntryPolicy = "Wisdom/explicit-replica-equipment-and-material-reference;not-native-probability/v1"
const wisdomStoneCost uint32 = 18

var ErrWisdomTickets = errors.New("insufficient Wisdom entry tickets")

type WisdomAdmission struct {
	Policy, Owner     string
	Epoch             int64
	Character         uint16
	Run               string
	Dungeon           uint32
	ResourceHash      string
	Variant           uint32
	Difficulty        byte
	TicketCount, Seed uint32
	Reward            json.RawMessage
	Debits            []wisdomDebit
	Passes            []WisdomPass `json:"-"`
}

type wisdomDebit struct {
	InstanceId string
	Count      uint32
	Expected   savedItemAsset
}

type savedItemAsset struct {
	InstanceId     string
	Template       json.RawMessage
	Count          uint32
	Location       byte
	Slot           uint16
	Attributes     json.RawMessage
	Raw119, Detail []uint16
}

func (a WisdomAdmission) Key() string {
	return "wisdom-entry:" + strings.ReplaceAll(a.Run, "-", "")
}

func (a WisdomAdmission) valid(l Lease, c Character) bool {
	return a.Policy == WisdomEntryPolicy && a.Owner == l.Owner && matchesUUID(a.Owner) &&
		a.Epoch == l.Epoch && a.Character == c.ID && matchesUUID(a.Run) &&
		a.Dungeon == WisdomDungeonID && matchesSHA(a.ResourceHash) && a.Difficulty <= 1 &&
		a.TicketCount == wisdomStoneCost && json.Valid(a.Reward)
}

// The native selector checks for one complete pass stack before summing stones.
// Locked or expired stone stacks count in the preview. The atomic debit rejects
// them if the same stacks are selected when entering.
func wisdomDebits(in Inventory, count uint32, passes []WisdomPass, now time.Time) ([]wisdomDebit, error) {
	if count != wisdomStoneCost {
		return nil, ErrInvalid
	}
	items := slices.Clone(in.Items)
	slices.SortStableFunc(items, func(a, b Asset) int { return int(a.Slot) - int(b.Slot) })
	for _, pass := range passes {
		if pass.Item == 0 || pass.Item == WisdomStone || pass.Count == 0 {
			return nil, ErrInvalid
		}
		for _, a := range items {
			if a.Location != 0 || a.TemplateID != pass.Item || a.Count < pass.Count {
				continue
			}
			attr, err := movementAttributes(&a)
			if err != nil {
				return nil, err
			}
			if !attr.Locked && (attr.ExpiresAt == nil || *attr.ExpiresAt > now.Unix()) {
				return []wisdomDebit{freezeWisdomDebit(a, pass.Count)}, nil
			}
		}
	}
	var rows []wisdomDebit
	for _, a := range items {
		if a.Location != 0 || a.TemplateID != WisdomStone || a.Count == 0 {
			continue
		}
		take := min(count, a.Count)
		rows = append(rows, freezeWisdomDebit(a, take))
		count -= take
		if count == 0 {
			return rows, nil
		}
	}
	return nil, ErrWisdomTickets
}

func freezeWisdomDebit(a Asset, count uint32) wisdomDebit {
	item := freezeItemAsset(a)
	return wisdomDebit{item.InstanceId, count, item}
}

func freezeItemAsset(a Asset) savedItemAsset {
	toNumbers := func(data []byte) []uint16 {
		values := make([]uint16, len(data))
		for i, b := range data {
			values[i] = uint16(b)
		}
		return values
	}
	id := a.UID
	if len(id) == 32 {
		id = materialGUID(id)
	}
	return savedItemAsset{
		id, a.Template, a.Count, a.Location, a.Slot, a.Attributes, toNumbers(a.Core), toNumbers(a.Detail),
	}
}

func (s *Store) PreviewWisdom(ctx context.Context, l Lease, c Character, count uint32, passes []WisdomPass, now time.Time) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		if _, err := dungeonCharacter(ctx, tx, l, c); err != nil {
			return struct{}{}, err
		}
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil {
			return struct{}{}, err
		}
		in, err := readInventory(ctx, tx, l.Account, r)
		if err != nil {
			return struct{}{}, err
		}
		_, err = wisdomDebits(in, count, passes, now)
		return struct{}{}, err
	})
	return err
}

type WisdomAdmissionResult struct {
	Entry   WisdomAdmission
	Fatigue DungeonFatigue
}

func (s *Store) AdmitWisdom(ctx context.Context, l Lease, expected Character, a WisdomAdmission, now time.Time,
	cost uint16, project func(Inventory) error) (WisdomAdmissionResult, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (WisdomAdmissionResult, error) {
		var out WisdomAdmissionResult
		c, err := dungeonCharacter(ctx, tx, l, expected)
		if err != nil {
			return out, err
		}
		if !a.valid(l, c) || c.Level < 96 || cost != 8 || len(a.Debits) != 0 || project == nil {
			return out, ErrInvalid
		}
		debit := func() error {
			r, err := lockAccount(ctx, tx, l, false)
			if err != nil {
				return err
			}
			in, err := readInventory(ctx, tx, l.Account, r)
			if err != nil {
				return err
			}
			a.Debits, err = wisdomDebits(in, a.TicketCount, a.Passes, now)
			if err != nil {
				return err
			}
			if !a.validDebits() {
				return ErrInvalid
			}
			after := in
			after.Items = slices.Clone(in.Items)
			for _, row := range a.Debits {
				id := strings.ReplaceAll(row.InstanceId, "-", "")
				i := slices.IndexFunc(after.Items, func(item Asset) bool { return item.UID == id })
				if i < 0 {
					return ErrConflict
				}
				item := &after.Items[i]
				attr, err := movementAttributes(item)
				if err != nil || attr.Locked || attr.ExpiresAt != nil && *attr.ExpiresAt <= now.Unix() {
					return ErrInvalid
				}
				item.Count -= row.Count
			}
			after.Items = slices.DeleteFunc(after.Items, func(item Asset) bool { return item.Count == 0 })
			if err := project(after); err != nil {
				return err
			}
			_, err = writeQuestInventory(ctx, tx, l, c, in, after)
			return err
		}
		out.Fatigue, err = s.chargeDungeonProgress(ctx, tx, l, c, now, cost, true, 0, debit)
		if err != nil {
			return out, err
		}
		body, err := json.Marshal(a)
		if err != nil {
			return out, err
		}
		out.Entry = a
		return out, putDungeonReceipt(ctx, tx, c, a.Key(), string(body), "admitted")
	})
}

func (a WisdomAdmission) validDebits() bool {
	if len(a.Debits) == 0 {
		return false
	}
	seen := map[string]bool{}
	var template uint32
	var total uint64
	for _, row := range a.Debits {
		item := row.Expected
		var t struct{ TemplateId uint32 }
		if row.Count == 0 || row.Count > item.Count || item.Location != 0 || row.InstanceId != item.InstanceId ||
			!matchesUUID(row.InstanceId) || seen[row.InstanceId] || json.Unmarshal(item.Template, &t) != nil ||
			t.TemplateId == 0 || template != 0 && t.TemplateId != template || !json.Valid(item.Attributes) ||
			len(item.Raw119) != 119 {
			return false
		}
		template = t.TemplateId
		seen[row.InstanceId] = true
		total += uint64(row.Count)
	}
	if template == WisdomStone {
		return total == uint64(a.TicketCount)
	}
	return len(a.Debits) == 1 && slices.Contains(a.Passes, WisdomPass{template, uint32(total)})
}

func requireWisdomAdmission(ctx context.Context, tx *sql.Tx, l Lease, c Character, a WisdomAdmission) error {
	if !a.valid(l, c) || !a.validDebits() {
		return ErrInvalid
	}
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	r, err := readDungeonReceipt(ctx, tx, c, a.Key())
	if err != nil {
		return err
	}
	if r.Result != "admitted" || r.Grants == nil || len(r.Grants) != 0 || !equalJSON([]byte(r.Input), body) {
		return ErrConflict
	}
	return nil
}

func (s *Store) ValidateWisdomEntry(ctx context.Context, l Lease, c Character, a WisdomAdmission) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, requireWisdomAdmission(ctx, tx, l, current, a)
	})
	return err
}

type DungeonObjectProof struct {
	Key, Input string
	Result     json.RawMessage
	Actor      uint16
}

func requireDungeonObjectProof(ctx context.Context, tx *sql.Tx, c Character, run string, p DungeonObjectProof) (bool, error) {
	if !strings.HasPrefix(p.Key, "dungeon-death:"+strings.ReplaceAll(run, "-", "")+":") ||
		!strings.HasSuffix(p.Key, fmt.Sprintf(":%d", p.Actor)) || p.Actor == 0 {
		return false, ErrInvalid
	}
	r, err := readDungeonReceipt(ctx, tx, c, p.Key)
	if err != nil {
		return false, err
	}
	var result struct {
		Actor          uint16
		DungeonCleared bool
	}
	if r.Input != p.Input || !equalJSON([]byte(r.Result), p.Result) ||
		json.Unmarshal([]byte(r.Result), &result) != nil || result.Actor != p.Actor {
		return false, ErrConflict
	}
	return result.DungeonCleared, nil
}

func requireWisdomClear(ctx context.Context, tx *sql.Tx, l Lease, c Character, a WisdomAdmission, deaths []DungeonObjectProof) error {
	if err := requireWisdomAdmission(ctx, tx, l, c, a); err != nil {
		return err
	}
	var profile dungeonProgression
	if json.Unmarshal(c.Gameplay, &profile) != nil || !profile.Initialized || profile.Progression.Hp == 0 {
		return ErrPhase
	}
	clear := false
	for _, p := range deaths {
		cleared, err := requireDungeonObjectProof(ctx, tx, c, a.Run, p)
		if err != nil {
			return err
		}
		clear = clear || cleared
	}
	if !clear {
		return ErrPhase
	}
	return nil
}

func (s *Store) WisdomClear(ctx context.Context, l Lease, expected Character, a WisdomAdmission, deaths []DungeonObjectProof,
	now time.Time, retry, leave bool) (DungeonFatigue, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (DungeonFatigue, error) {
		c, err := dungeonCharacter(ctx, tx, l, expected)
		if err != nil {
			return DungeonFatigue{}, err
		}
		if err := requireWisdomClear(ctx, tx, l, c, a, deaths); err != nil {
			return DungeonFatigue{}, err
		}
		var fatigue DungeonFatigue
		if retry {
			var profile dungeonProgression
			if err := json.Unmarshal(c.Gameplay, &profile); err != nil {
				return DungeonFatigue{}, err
			}
			p := profile.Progression
			date := now.UTC().Add(2 * time.Hour)
			day := int64(date.Year()*10000 + int(date.Month())*100 + date.Day())
			fatigue = DungeonFatigue{p.Fatigue, p.FatigueLimit}
			if p.Fatigue > p.FatigueLimit || p.FatigueDay < 0 {
				return DungeonFatigue{}, ErrInvalid
			}
			if p.FatigueDay != 0 && day > p.FatigueDay {
				fatigue.Remaining = fatigue.Limit
			}
			if fatigue.Remaining == 0 {
				return DungeonFatigue{}, ErrFunds
			}
		}
		if leave {
			err = returnDungeon(ctx, tx, l, c, true)
		}
		return fatigue, err
	})
}

func (s *Store) ReturnWisdomDungeon(ctx context.Context, l Lease, c Character, a WisdomAdmission) error {
	return s.returnDungeonWithProof(ctx, l, c, true, func(tx *sql.Tx, current Character) error {
		return requireWisdomAdmission(ctx, tx, l, current, a)
	})
}

func (s *Store) ValidateDungeonObject(ctx context.Context, l Lease, expected Character, run string,
	wisdom *WisdomAdmission, rubyEntry *ruby.Entry, death *DungeonObjectProof) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		c, err := dungeonCharacter(ctx, tx, l, expected)
		if err != nil {
			return struct{}{}, err
		}
		var profile dungeonProgression
		if json.Unmarshal(c.Gameplay, &profile) != nil || !profile.Initialized || profile.Progression.Hp == 0 {
			return struct{}{}, ErrPhase
		}
		if wisdom != nil {
			err = requireWisdomAdmission(ctx, tx, l, c, *wisdom)
		} else if rubyEntry != nil && rubyEntry.Run == run {
			var a deathReceipt
			a, err = readDungeonReceipt(ctx, tx, c, ruby.EntryKey(run))
			if err == nil && (a.Result != "admitted" || a.Grants == nil || len(a.Grants) != 0 ||
				!equalJSON([]byte(a.Input), mustDungeonJSON(rubyEntry))) {
				err = ErrConflict
			}
		} else {
			err = ErrInvalid
		}
		if err == nil && death != nil {
			_, err = requireDungeonObjectProof(ctx, tx, c, run, *death)
		}
		return struct{}{}, err
	})
	return err
}
