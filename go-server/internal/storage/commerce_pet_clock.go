package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"
)

type PetCareRule struct {
	TemplateID                                                          uint32 `json:"TemplateId"`
	EquipmentHash, CommonHash                                           string
	SpendSeconds, RecoverSeconds                                        uint32
	MaximumSatiety, MaximumLevel                                        byte
	SpeciesId                                                           uint32
	SpeciesHash                                                         string
	StartLevel, PermissionLevel, OverskillLevel, AutomaticFeedThreshold byte
}

type PetFeedRule struct {
	Source                           json.RawMessage
	MinimumPetLevel, SatietyRecovery byte
	CooldownMilliseconds             uint32
	NativeBuffSeconds                uint32
	NativeChargePercent              int
}

type PetClockMode byte

const (
	PetPaused PetClockMode = iota
	PetRecovering
	PetSpending
)

type savedPetClock struct {
	Owner                        string
	Epoch                        int64
	SinceMilliseconds            int64
	Baseline                     float64
	Mode                         PetClockMode
	SpendSeconds, RecoverSeconds uint32
	Maximum                      byte
	ResourceHash                 string
}

type PetSatiety struct {
	Serial uint32
	Value  byte
}

func HasCaredPet(c Character, rule *PetCareRule) bool {
	if rule == nil {
		return false
	}
	var p struct {
		SpecialAssets struct {
			Items []struct {
				Template specialPurchaseTemplate
				Pet      json.RawMessage
			}
		}
	}
	if json.Unmarshal(c.Gameplay, &p) != nil {
		return false
	}
	for _, row := range p.SpecialAssets.Items {
		if row.Template.TemplateID == rule.TemplateID && hasEffect(row.Pet) {
			return true
		}
	}
	return false
}

func (s *Store) SyncPetClock(ctx context.Context, l Lease, rule *PetCareRule, mode PetClockMode, duelReady bool, now time.Time) ([]PetSatiety, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) ([]PetSatiety, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return nil, err
		}
		if r.Phase != TownReady && !(duelReady && r.Phase == Selected) || r.SelectedID == 0 {
			return nil, ErrPhase
		}
		c, err := mailCharacter(ctx, tx, l.Account, r.SelectedID)
		if err != nil {
			return nil, err
		}
		x := CommerceTx{Character: c, lease: l}
		updates, changed, err := x.advancePetClock(rule, mode, now)
		if err != nil || !changed {
			return updates, err
		}
		_, err = saveMailCharacter(ctx, tx, l.Account, x.Character)
		return updates, err
	})
}

// Disconnection freezes clocks owned by the lease using their saved rates.
func freezePetClocks(c *Character, l Lease, now time.Time) (bool, error) {
	x := CommerceTx{Character: *c}
	state, items, _, err := x.loadSpecial()
	if err != nil {
		return false, err
	}
	changed := false
	for i, raw := range items {
		var row map[string]json.RawMessage
		if err = json.Unmarshal(raw, &row); err != nil {
			return false, err
		}
		if !hasEffect(row["Pet"]) {
			continue
		}
		var pet map[string]json.RawMessage
		if err = json.Unmarshal(row["Pet"], &pet); err != nil {
			return false, err
		}
		clock, err := rawPetValue[*savedPetClock](pet, "SatietyClock")
		if err != nil {
			return false, err
		}
		if clock == nil || clock.Epoch != l.Epoch || clock.Mode == PetPaused ||
			!strings.EqualFold(strings.ReplaceAll(clock.Owner, "-", ""), strings.ReplaceAll(l.Owner, "-", "")) {
			continue
		}
		value, err := petClockValue(*clock, now.UnixMilli())
		if err != nil {
			return false, err
		}
		clock.Baseline, clock.SinceMilliseconds, clock.Mode = value, now.UnixMilli(), PetPaused
		if err = setPetValue(pet, "Satiety", byte(value)); err != nil {
			return false, err
		}
		if err = setPetValue(pet, "SatietyClock", clock); err != nil {
			return false, err
		}
		if err = setPetValue(row, "Pet", pet); err != nil {
			return false, err
		}
		items[i], err = json.Marshal(row)
		if err != nil {
			return false, err
		}
		changed = true
	}
	if !changed {
		return false, nil
	}
	if err = setPetValue(state, "Items", items); err != nil {
		return false, err
	}
	return true, replaceMailFields(c, map[string]any{"SpecialAssets": state})
}

func (x *CommerceTx) advancePetClock(rule *PetCareRule, mode PetClockMode, now time.Time) ([]PetSatiety, bool, error) {
	if rule == nil || !HasCaredPet(x.Character, rule) {
		return nil, false, nil
	}
	state, items, _, err := x.loadSpecial()
	if err != nil {
		return nil, false, err
	}
	type basicPetRow struct {
		Location byte
		Slot     uint16
		Template specialPurchaseTemplate
	}
	rows := make([]basicPetRow, len(items))
	artifact := false
	for i, b := range items {
		if err = json.Unmarshal(b, &rows[i]); err != nil {
			return nil, false, err
		}
		if rows[i].Location == 3 && rows[i].Template.Kind == 2 {
			artifact = true
		}
	}
	var profile struct{ Initialized bool }
	if err = json.Unmarshal(x.Character.Gameplay, &profile); err != nil {
		return nil, false, err
	}
	var updates []PetSatiety
	changed := false
	for _, r := range rows {
		if r.Template.TemplateID != rule.TemplateID {
			continue
		}
		pet, err := readPetAt(items, r.Location, r.Slot)
		if err != nil {
			return nil, false, err
		}
		if pet.template.ResourceHash != rule.EquipmentHash {
			return nil, false, ErrInvalid
		}
		old, err := rawPetValue[*savedPetClock](pet.pet, "SatietyClock")
		if err != nil {
			return nil, false, err
		}
		owned := old != nil && strings.EqualFold(old.Owner, x.lease.Owner) && old.Epoch == x.lease.Epoch
		value := float64(pet.satiety)
		if owned {
			if old.ResourceHash == rule.CommonHash && (old.SpendSeconds != rule.SpendSeconds || old.RecoverSeconds != rule.RecoverSeconds || old.Maximum != rule.MaximumSatiety) {
				return nil, false, ErrInvalid
			}
			value, err = petClockValue(*old, now.UnixMilli())
			if err != nil {
				return nil, false, err
			}
		}
		target := mode
		expiry, err := rawPetValue[*int64](pet.attributes, "ExpiresAt")
		if err != nil {
			return nil, false, err
		}
		if !profile.Initialized || r.Location != 3 || r.Slot != 26 || artifact || expiry != nil && *expiry <= now.Unix() {
			target = PetPaused
		}
		clock := savedPetClock{Owner: x.lease.Owner, Epoch: x.lease.Epoch, SinceMilliseconds: now.UnixMilli(), Baseline: value, Mode: target,
			SpendSeconds: rule.SpendSeconds, RecoverSeconds: rule.RecoverSeconds, Maximum: rule.MaximumSatiety, ResourceHash: rule.CommonHash}
		if owned && old.Mode == target && old.ResourceHash == rule.CommonHash {
			clock = *old
		}
		if _, err = petClockValue(clock, now.UnixMilli()); err != nil {
			return nil, false, err
		}
		next := byte(value)
		if old != nil && clock == *old && next == pet.satiety {
			continue
		}
		if next != pet.satiety {
			updates = append(updates, PetSatiety{pet.serial, next})
		}
		if err = setPetValue(pet.pet, "Satiety", next); err != nil {
			return nil, false, err
		}
		if err = setPetValue(pet.pet, "SatietyClock", clock); err != nil {
			return nil, false, err
		}
		if err = setPetValue(pet.fields, "Pet", pet.pet); err != nil {
			return nil, false, err
		}
		items[pet.index], err = json.Marshal(pet.fields)
		if err != nil {
			return nil, false, err
		}
		changed = true
	}
	if !changed {
		return nil, false, nil
	}
	if err = setPetValue(state, "Items", items); err != nil {
		return nil, false, err
	}
	err = replaceMailFields(&x.Character, map[string]any{"SpecialAssets": state})
	return updates, true, err
}

func petClockValue(c savedPetClock, now int64) (float64, error) {
	if !mailUID(c.Owner) || c.Epoch <= 0 || c.SinceMilliseconds <= 0 || math.IsInf(c.Baseline, 0) || math.IsNaN(c.Baseline) ||
		c.Baseline < 0 || c.Baseline > float64(c.Maximum) || c.Maximum == 0 || c.Maximum > 100 ||
		c.SpendSeconds == 0 || c.SpendSeconds > 2147483 || c.RecoverSeconds == 0 || c.RecoverSeconds > 2147483 ||
		c.Mode > PetSpending || len(c.ResourceHash) != 64 {
		return 0, ErrInvalid
	}
	elapsed := float64(max(0, now-c.SinceMilliseconds))
	delta := 0.0
	switch c.Mode {
	case PetSpending:
		spent := elapsed / (float64(c.SpendSeconds) * 1000)
		if spent >= 0.5 {
			delta = -spent
		}
	case PetRecovering:
		delta = elapsed / (float64(c.RecoverSeconds) * 1000)
	}
	return min(float64(c.Maximum), max(0, c.Baseline+delta)), nil
}

func (x *CommerceTx) FeedPet(slot uint16, feed *PetFeedRule, rule *PetCareRule, mode PetClockMode, now time.Time) error {
	if feed == nil || rule == nil || feed.SatietyRecovery == 0 || mode == PetPaused {
		return ErrInvalid
	}
	p, i, err := x.petSupply(slot, 24, feed.Source, now)
	if err != nil {
		return err
	}
	if _, _, err = x.advancePetClock(rule, mode, now); err != nil {
		return err
	}
	state, items, _, err := x.loadSpecial()
	if err != nil {
		return err
	}
	pet, err := readPetAt(items, 3, 26)
	if err != nil {
		return err
	}
	clock, err := rawPetValue[*savedPetClock](pet.pet, "SatietyClock")
	if err != nil || clock == nil || clock.Mode == PetPaused || pet.template.TemplateID != rule.TemplateID ||
		pet.template.ResourceHash != rule.EquipmentHash || !activePet(pet, now) ||
		pet.level < feed.MinimumPetLevel || pet.level > rule.MaximumLevel || pet.satiety >= rule.MaximumSatiety {
		return ErrInvalid
	}
	if err = p.useCooldown(24, nil, feed.CooldownMilliseconds, now.UnixMilli()); err != nil {
		return err
	}
	filled := byte(min(int(rule.MaximumSatiety), int(pet.satiety)+int(feed.SatietyRecovery)))
	clock.Baseline, clock.SinceMilliseconds = float64(filled), now.UnixMilli()
	if err = setPetValue(pet.pet, "Satiety", filled); err != nil {
		return err
	}
	if err = setPetValue(pet.pet, "SatietyClock", clock); err != nil {
		return err
	}
	if err = x.savePetAt(state, items, pet, map[string]any{"PotionCooldowns": p.PotionCooldowns}); err != nil {
		return err
	}
	return x.debitAsset(i, 1)
}
