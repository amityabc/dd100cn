package storage

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
)

type PotionRecovery struct {
	Percent, NativeFloatPercent, NativeWideMaximum, NativeMultiplyThenScale bool
	Amount                                                                  json.Number
	DurationMilliseconds                                                    uint32
}

type PotionDefinition struct {
	TemplateID                                                       uint32 `json:"TemplateId"`
	ResourceHash                                                     string
	MinimumLevel, MaximumLevel                                       byte
	UsableJobs                                                       uint16
	HP                                                               *PotionRecovery `json:"Hp"`
	MP                                                               *PotionRecovery `json:"Mp"`
	CooldownMilliseconds                                             uint32
	CooldownGroup                                                    *uint32
	AcquisitionTemplate                                              json.RawMessage
	NativeAnyStatusRecoveryLevel                                     *uint32
	NativeSelfStatusRecovery, NativeSelfStatChanges, NativeSelfStats json.RawMessage
	Dungeons                                                         []uint32
	WeeklyDeletionDay                                                *byte
	NativeApcSummon                                                  *PotionSummon
}

type PotionSummon struct {
	ActorCode            uint32
	Level                byte
	DurationMilliseconds uint32
	NativeFlag           int
}

func (s *PotionSummon) Supported() bool {
	return s != nil && s.ActorCode == 5409 && s.Level == 60 && s.DurationMilliseconds == 900000 && s.NativeFlag == 1
}

func (s PotionSummon) MarshalJSON() ([]byte, error) {
	type wire PotionSummon
	return json.Marshal(struct {
		wire
		Supported bool
	}{wire: wire(s), Supported: s.Supported()})
}

type FatigueItemDefinition struct {
	Template                 json.RawMessage
	Amount                   uint16
	MaximumLevel, DailyLimit byte
	CooldownMilliseconds     uint32
	CooldownGroup            *uint32
	UsablePlaces             []string
}

type useState struct {
	Initialized bool
	Progression struct {
		HP, MP                uint32
		Fatigue, FatigueLimit uint16
		FatigueDay            int64
	}
	PotionCooldowns map[string]int64
	FatigueItemUses map[uint32]struct {
		Day   int64
		Count byte
	}
}

func (x *CommerceTx) useSource(slot uint16, now time.Time) (index int, template ordinaryTemplate, lifetime materialDefinition, state useState, err error) {
	index = slices.IndexFunc(x.Inventory.Items, func(a Asset) bool {
		return a.CharacterID == x.Character.ID && a.Location == 0 && a.Slot == slot && a.Count > 0
	})
	if index < 0 {
		return index, template, lifetime, state, ErrInvalid
	}
	a := x.Inventory.Items[index]
	if json.Unmarshal(a.Template, &template) != nil || json.Unmarshal(a.Template, &lifetime) != nil ||
		template.TemplateID != a.TemplateID || template.Kind != 2 || template.FirstBagSlot != 65 || template.LastBagSlot != 120 ||
		(slot < 3 || slot > 8) && (slot < 65 || slot > template.bagEnd(x.Inventory.BagExpansion)) ||
		template.UsableJobs&(1<<x.Character.Job) == 0 || template.MinimumLevel > x.Character.Level ||
		json.Unmarshal(x.Character.Gameplay, &state) != nil || !state.Initialized || state.Progression.HP == 0 ||
		lifetime.ResourceExpiration != nil && *lifetime.ResourceExpiration <= now.Unix() {
		return index, template, lifetime, state, ErrInvalid
	}
	attr, err := movementAttributes(&a)
	if err != nil {
		return index, template, lifetime, state, err
	}
	valid, err := plainMaterial(a, lifetime)
	if err != nil {
		return index, template, lifetime, state, err
	}
	if !valid || attr.Locked || attr.ExpiresAt != nil && *attr.ExpiresAt <= now.Unix() {
		return index, template, lifetime, state, ErrInvalid
	}
	return index, template, lifetime, state, nil
}

func (x *CommerceTx) UsePotion(slot uint16, item uint32, d PotionDefinition, dungeon uint32, now time.Time) error {
	i, t, lifetime, p, err := x.useSource(slot, now)
	if err != nil {
		return err
	}
	if d.TemplateID != item || t.TemplateID != item || t.ResourceHash != d.ResourceHash || d.MinimumLevel > x.Character.Level ||
		d.MaximumLevel < x.Character.Level || d.UsableJobs&(1<<x.Character.Job) == 0 ||
		len(d.Dungeons) != 0 && !slices.Contains(d.Dungeons, dungeon) ||
		d.WeeklyDeletionDay != nil && (lifetime.WeeklyDeletionDay == nil || *d.WeeklyDeletionDay != *lifetime.WeeklyDeletionDay) ||
		d.HP == nil && d.MP == nil && d.NativeAnyStatusRecoveryLevel == nil && !hasEffect(d.NativeSelfStats) &&
			!hasEffect(d.NativeSelfStatusRecovery) && !hasEffect(d.NativeSelfStatChanges) && !d.NativeApcSummon.Supported() {
		return ErrInvalid
	}
	if hasEffect(d.AcquisitionTemplate) {
		var frozen materialDefinition
		if json.Unmarshal(d.AcquisitionTemplate, &frozen) != nil || !sameDeadline(frozen.ResourceExpiration, lifetime.ResourceExpiration) ||
			frozen.RelativeLifetimeSeconds != lifetime.RelativeLifetimeSeconds || frozen.DailyDeletion != lifetime.DailyDeletion ||
			!sameDeletionWeekday(frozen, lifetime) {
			return ErrInvalid
		}
	}
	if err = p.useCooldown(t.TemplateID, d.CooldownGroup, d.CooldownMilliseconds, now.UnixMilli()); err != nil {
		return err
	}
	var stats struct{ Values map[string]int64 }
	if err = json.Unmarshal(x.Character.Stats, &stats); err != nil {
		return err
	}
	hp, err := potionRecovery(p.Progression.HP, stats.Values["hp_max"], d.HP)
	if err != nil {
		return err
	}
	mp, err := potionRecovery(p.Progression.MP, stats.Values["mp_max"], d.MP)
	if err != nil {
		return err
	}
	if err = x.useSave(p, map[string]any{"Hp": hp, "Mp": mp}, false); err != nil {
		return err
	}
	return x.debitAsset(i, 1)
}

func (x *CommerceTx) UseFatigue(slot uint16, catalog map[uint32]FatigueItemDefinition, place string, now time.Time) error {
	i, t, _, p, err := x.useSource(slot, now)
	if err != nil {
		return err
	}
	d, ok := catalog[t.TemplateID]
	if !ok || d.Amount == 0 || d.Amount > 156 || d.MaximumLevel > 100 || x.Character.Level > d.MaximumLevel ||
		!slices.Contains(d.UsablePlaces, place) {
		return ErrInvalid
	}
	if !sameCommerceTemplate(d.Template, x.Inventory.Items[i].Template) {
		return ErrInvalid
	}
	dayDate := now.UTC().Add(2 * time.Hour)
	day := int64(dayDate.Year()*10000 + int(dayDate.Month())*100 + dayDate.Day())
	fatigue := p.Progression.Fatigue
	last := p.Progression.FatigueDay
	if last < 0 || fatigue > p.Progression.FatigueLimit {
		return ErrInvalid
	}
	if last != 0 && last < day {
		fatigue = p.Progression.FatigueLimit
	}
	next := int(fatigue) + int(d.Amount)
	if next > 156 || next > int(p.Progression.FatigueLimit) {
		return ErrInvalid
	}
	if err = p.useCooldown(t.TemplateID, d.CooldownGroup, d.CooldownMilliseconds, now.UnixMilli()); err != nil {
		return err
	}
	if d.DailyLimit > 0 {
		if p.FatigueItemUses == nil {
			p.FatigueItemUses = make(map[uint32]struct {
				Day   int64
				Count byte
			})
		}
		if len(p.FatigueItemUses) > 4096 {
			return ErrInvalid
		}
		for id, use := range p.FatigueItemUses {
			if id == 0 || id == math.MaxUint32 || use.Day <= 0 {
				return ErrInvalid
			}
			if use.Day < day {
				delete(p.FatigueItemUses, id)
			}
		}
		use := p.FatigueItemUses[t.TemplateID]
		if use.Day > day || use.Count >= d.DailyLimit || use.Day == 0 && len(p.FatigueItemUses) >= 4096 {
			return ErrInvalid
		}
		use.Day, use.Count = day, use.Count+1
		p.FatigueItemUses[t.TemplateID] = use
	}
	if err = x.useSave(p, map[string]any{"Fatigue": next, "FatigueDay": max(day, last)}, d.DailyLimit > 0); err != nil {
		return err
	}
	return x.debitAsset(i, 1)
}

func (p *useState) useCooldown(template uint32, group *uint32, duration uint32, now int64) error {
	if duration > math.MaxInt32 || len(p.PotionCooldowns) > 4096 {
		return ErrInvalid
	}
	key := fmt.Sprintf("item:%d", template)
	if group != nil {
		key = fmt.Sprintf("group:%d", *group)
	}
	if p.PotionCooldowns[key] > now {
		return ErrInvalid
	}
	if p.PotionCooldowns == nil {
		p.PotionCooldowns = map[string]int64{}
	}
	for id, end := range p.PotionCooldowns {
		prefix, value, ok := strings.Cut(id, ":")
		_, err := strconv.ParseUint(value, 10, 32)
		if !ok || prefix != "item" && prefix != "group" || err != nil || end < 0 {
			return ErrInvalid
		}
		if end <= now {
			delete(p.PotionCooldowns, id)
		}
	}
	if duration > 0 {
		if len(p.PotionCooldowns) >= 4096 {
			return ErrInvalid
		}
		p.PotionCooldowns[key] = now + int64(duration)
	}
	return nil
}

func (x *CommerceTx) useSave(p useState, changes map[string]any, saveFatigueUses bool) error {
	var profile struct{ Progression map[string]json.RawMessage }
	if err := json.Unmarshal(x.Character.Gameplay, &profile); err != nil {
		return err
	}
	if profile.Progression == nil {
		return ErrInvalid
	}
	for field, value := range changes {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		profile.Progression[field] = raw
	}
	fields := map[string]any{"Progression": profile.Progression, "PotionCooldowns": p.PotionCooldowns}
	if saveFatigueUses {
		fields["FatigueItemUses"] = p.FatigueItemUses
	}
	return replaceMailFields(&x.Character, fields)
}

func hasEffect(raw json.RawMessage) bool { return len(raw) != 0 && string(raw) != "null" }

func sameDeadline(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameDeletionWeekday(prepared, saved materialDefinition) bool {
	a, b := prepared.WeeklyDeletionDay, saved.WeeklyDeletionDay
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b || *a == 0 && *b == 4 && prepared.PvfGiftItem && saved.PvfGiftItem
}

func potionRecovery(current uint32, maximum int64, e *PotionRecovery) (uint32, error) {
	if maximum < 0 || maximum > math.MaxUint32 || int64(current) > maximum {
		return 0, ErrInvalid
	}
	if e == nil {
		return current, nil
	}
	r, ok := new(big.Rat).SetString(e.Amount.String())
	if !ok || r.Sign() <= 0 || e.Percent && r.Cmp(big.NewRat(100, 1)) > 0 || e.DurationMilliseconds > math.MaxInt32 {
		return 0, ErrInvalid
	}
	if e.NativeFloatPercent {
		if !e.Percent || !e.NativeWideMaximum && maximum > math.MaxInt32 {
			return 0, ErrInvalid
		}
		num, _ := r.Float32()
		amount := float32(maximum) * (num / 100)
		if e.NativeMultiplyThenScale {
			amount = (float32(maximum) * num) * 0.01
		}
		if amount >= 2147483648 {
			return 0, ErrInvalid
		}
		return uint32(min(maximum, int64(current)+int64(amount))), nil
	}
	multiplier := big.NewRat(10, 1)
	if e.Percent {
		multiplier = big.NewRat(maximum, 100)
	}
	r.Mul(r, multiplier)
	added := new(big.Int).Quo(r.Num(), r.Denom())
	if !added.IsInt64() || added.Int64() >= maximum-int64(current) {
		return uint32(maximum), nil
	}
	return uint32(int64(current) + added.Int64()), nil
}
