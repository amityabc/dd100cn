package storage

import (
	"encoding/binary"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode"

	"s9a1/server/internal/protocol"
)

func (c CommerceCatalog) PetDefaultName(id uint32, hash string) (string, bool) {
	r, ok := c.PetNames[id]
	return r.DefaultName, ok && r.ResourceHash == hash
}

type PetHatchDefinition struct {
	Egg, Pet json.RawMessage
	Name     string
}

type PetNameRule struct {
	TemplateID                uint32
	ResourceHash, DefaultName string
}

type PetRenameCard struct {
	Source               json.RawMessage
	MinimumPetLevel      byte
	CooldownMilliseconds uint32
}

type petProfileState struct {
	template                specialPurchaseTemplate
	fields, attributes, pet map[string]json.RawMessage
	level, satiety          byte
	serial                  uint32
	index                   int
}

func readPetAt(items []json.RawMessage, location byte, slot uint16) (petProfileState, error) {
	var out petProfileState
	out.index = -1
	for i, b := range items {
		var row struct {
			Location byte
			Slot     uint16
			Template specialPurchaseTemplate
		}
		if err := json.Unmarshal(b, &row); err != nil {
			return out, err
		}
		if row.Location != location || row.Slot != slot {
			continue
		}
		if out.index >= 0 || row.Template.Kind != 1 {
			return out, ErrInvalid
		}
		out.index, out.template = i, row.Template
		if err := json.Unmarshal(b, &out.fields); err != nil {
			return out, err
		}
		if err := json.Unmarshal(out.fields["Attributes"], &out.attributes); err != nil {
			return out, err
		}
		if err := json.Unmarshal(out.fields["Pet"], &out.pet); err != nil {
			return out, err
		}
		if out.attributes == nil || out.pet == nil {
			return out, ErrInvalid
		}
		id, err := rawPetValue[string](out.fields, "InstanceId")
		if err != nil || !mailUID(id) {
			return out, ErrInvalid
		}
		detail, err := rawPetValue[string](out.pet, "DetailId")
		if err != nil || !mailUID(detail) {
			return out, ErrInvalid
		}
		out.level, err = rawPetValue[byte](out.pet, "Level")
		if err != nil || out.level == 0 || out.level > 100 {
			return out, ErrInvalid
		}
		out.satiety, err = rawPetValue[byte](out.pet, "Satiety")
		if err != nil || out.satiety > 100 {
			return out, ErrInvalid
		}
		out.serial, err = rawPetValue[uint32](out.pet, "NativeSerial")
		if err != nil || out.serial == 0 {
			return out, ErrInvalid
		}
	}
	if out.index < 0 {
		return out, ErrInvalid
	}
	return out, nil
}

func rawPetValue[T any](m map[string]json.RawMessage, key string) (T, error) {
	var v T
	if raw := m[key]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return v, err
		}
	}
	return v, nil
}

func setPetValue(m map[string]json.RawMessage, key string, v any) error {
	raw, err := json.Marshal(v)
	if err == nil {
		m[key] = raw
	}
	return err
}

func (x *CommerceTx) savePetAt(state map[string]json.RawMessage, items []json.RawMessage, p petProfileState, values map[string]any) error {
	if err := setPetValue(p.fields, "Pet", p.pet); err != nil {
		return err
	}
	if err := setPetValue(p.fields, "Attributes", p.attributes); err != nil {
		return err
	}
	raw, err := json.Marshal(p.fields)
	if err != nil {
		return err
	}
	items[p.index] = raw
	if err = setPetValue(state, "Items", items); err != nil {
		return err
	}
	if values == nil {
		values = map[string]any{}
	}
	values["SpecialAssets"] = state
	return replaceMailFields(&x.Character, values)
}

func (x *CommerceTx) HatchPet(slot uint16, rules map[uint32]PetHatchDefinition, now time.Time) error {
	var p useState
	if err := json.Unmarshal(x.Character.Gameplay, &p); err != nil {
		return err
	}
	if !p.Initialized || p.Progression.HP == 0 || slot > 159 {
		return ErrPhase
	}
	state, items, _, err := x.loadSpecial()
	if err != nil {
		return err
	}
	pet, err := readPetAt(items, 7, slot)
	if err != nil {
		return err
	}
	r, ok := rules[pet.template.TemplateID]
	if !ok {
		return ErrInvalid
	}
	var egg, target specialPurchaseTemplate
	var dates materialDefinition
	locked, err := rawPetValue[bool](pet.attributes, "Locked")
	expiry, exErr := rawPetValue[*int64](pet.attributes, "ExpiresAt")
	if json.Unmarshal(r.Egg, &egg) != nil || json.Unmarshal(r.Pet, &target) != nil || json.Unmarshal(pet.fields["Template"], &dates) != nil ||
		egg.TemplateID != pet.template.TemplateID || egg.ResourceHash != pet.template.ResourceHash ||
		x.Character.Level < egg.MinimumLevel || egg.UsableJobs&(1<<x.Character.Job) == 0 ||
		target.Kind != 1 || target.TemplateID == egg.TemplateID || !validPetName(r.Name) ||
		locked || err != nil || expiry != nil || exErr != nil || dates.ResourceExpiration != nil || dates.RelativeLifetimeSeconds != 0 ||
		dates.DailyDeletion || dates.WeeklyDeletionDay != nil || now.Unix() <= 0 {
		return ErrInvalid
	}
	var core []byte
	var t materialDefinition
	if json.Unmarshal(pet.fields["Core153"], &core) != nil || len(core) != 153 || json.Unmarshal(r.Pet, &t) != nil {
		return ErrInvalid
	}
	binary.LittleEndian.PutUint32(core[2:], target.TemplateID)
	core[13] = 0
	if t.InitialSealed {
		core[13] = 1
	}
	if err = setPetValue(pet.fields, "Core153", legacyByteArray(core)); err != nil {
		return err
	}
	pet.fields["Template"] = r.Pet
	if err = setPetValue(pet.attributes, "Binding", core[13]); err != nil {
		return err
	}
	if err = setPetValue(pet.pet, "Name", r.Name); err != nil {
		return err
	}
	return x.savePetAt(state, items, pet, nil)
}

func (x *CommerceTx) RenamePet(slot uint16, name string, card *PetRenameCard, rules map[uint32]PetNameRule, now time.Time) error {
	if card == nil || !validPetName(name) {
		return ErrInvalid
	}
	profile, source, err := x.petSupply(slot, 25, card.Source, now)
	if err != nil {
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
	r, ok := rules[pet.template.TemplateID]
	if !ok || r.ResourceHash != pet.template.ResourceHash || r.TemplateID != pet.template.TemplateID ||
		!activePet(pet, now) || pet.level < card.MinimumPetLevel {
		return ErrInvalid
	}
	if name == "没有名字" {
		name = r.DefaultName
	}
	old, err := rawPetValue[string](pet.pet, "Name")
	if err != nil || name == old || !validPetName(name) {
		return ErrInvalid
	}
	if err = profile.useCooldown(25, nil, card.CooldownMilliseconds, now.UnixMilli()); err != nil {
		return err
	}
	if err = setPetValue(pet.pet, "Name", name); err != nil {
		return err
	}
	if err = x.savePetAt(state, items, pet, map[string]any{"PotionCooldowns": profile.PotionCooldowns}); err != nil {
		return err
	}
	return x.debitAsset(source, 1)
}

func validPetName(s string) bool {
	if strings.TrimSpace(s) == "" || strings.ContainsFunc(s, unicode.IsControl) {
		return false
	}
	_, err := protocol.EncodeText(s, 19)
	return err == nil
}

func activePet(p petProfileState, now time.Time) bool {
	locked, err := rawPetValue[bool](p.attributes, "Locked")
	binding, bindErr := rawPetValue[byte](p.attributes, "Binding")
	expiry, expiryErr := rawPetValue[*int64](p.attributes, "ExpiresAt")
	var dates materialDefinition
	return !locked && err == nil && binding == 0 && bindErr == nil && expiryErr == nil && (expiry == nil || *expiry > now.Unix()) &&
		json.Unmarshal(p.fields["Template"], &dates) == nil && (dates.ResourceExpiration == nil || *dates.ResourceExpiration > now.Unix())
}

func (x *CommerceTx) petSupply(slot uint16, id uint32, template json.RawMessage, now time.Time) (useState, int, error) {
	var profile useState
	if err := json.Unmarshal(x.Character.Gameplay, &profile); err != nil {
		return profile, -1, err
	}
	if !profile.Initialized || profile.Progression.HP == 0 || slot < 216 || slot > 271 {
		return profile, -1, ErrPhase
	}
	idx := slices.IndexFunc(x.Inventory.Items, func(a Asset) bool {
		return a.CharacterID == x.Character.ID && a.Location == 7 && a.Slot == slot && a.TemplateID == id && a.Count > 0
	})
	if idx < 0 {
		return profile, -1, ErrInvalid
	}
	a := x.Inventory.Items[idx]
	var t ordinaryTemplate
	var life materialDefinition
	if json.Unmarshal(a.Template, &t) != nil || json.Unmarshal(a.Template, &life) != nil || !t.PetConsumable || t.Kind != 2 ||
		t.TemplateID != id || !sameCommerceTemplate(template, a.Template) || x.Character.Level < t.MinimumLevel ||
		t.UsableJobs&(1<<x.Character.Job) == 0 || life.ResourceExpiration != nil && *life.ResourceExpiration <= now.Unix() {
		return profile, -1, ErrInvalid
	}
	plain, err := plainMaterial(a, life)
	if err != nil {
		return profile, -1, err
	}
	var v materialAttributes
	if !plain || json.Unmarshal(a.Attributes, &v) != nil || v.ExpiresAt != nil && *v.ExpiresAt <= now.Unix() {
		return profile, -1, ErrInvalid
	}
	return profile, idx, nil
}
