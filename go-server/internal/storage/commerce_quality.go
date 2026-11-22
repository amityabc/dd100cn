package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type QualityCatalog struct {
	Sources   map[uint32]json.RawMessage
	Targets   map[uint32]QualityTarget
	Mythology map[uint32]MythologyOptions
}

type QualityTarget struct {
	Template json.RawMessage
	Facts    struct {
		ResourceHash                                  string
		Rarity, CustomAbilityCount, CustomAbilityType byte
	}
}

type MythologyOptions struct {
	Hash    string
	Count   byte
	Options [][]byte
}

type QualityRequest struct {
	Target                               uint32
	TargetSlot, SourceSlot, Mode, Opcode uint16
}

type qualityMythology struct {
	SourceHash                          string
	Count                               byte
	First, Second, Third, Fourth, Fifth byte
}

const qualityMythologySource = 490017880

func qualitySourceFits(id uint32, t ordinaryTemplate, d QualityTarget) bool {
	if id == 2683895 && t.Kind != 0 || (id == 490013009 || id == 490013699) && t.WornSlot == 13 {
		return false
	}
	if rarity, restricted := map[uint32]byte{10150860: 2, 10150861: 3, 10150862: 6, 10150863: 4, 10150903: 5, 10302434: 4}[id]; restricted {
		return t.WornSlot != 13 && d.Facts.ResourceHash == t.ResourceHash && d.Facts.Rarity == rarity &&
			(id != 10302434 || d.Facts.CustomAbilityCount > 0 && d.Facts.CustomAbilityType == 2)
	}
	return id != 490000155 || (t.WornSlot == 22 || t.WornSlot == 23) && d.Facts.ResourceHash == t.ResourceHash && d.Facts.Rarity != 7
}

func qualityMatches(t ordinaryTemplate, a Asset, d QualityTarget) bool {
	var other ordinaryTemplate
	if json.Unmarshal(d.Template, &other) != nil || t.TemplateID != other.TemplateID || t.Kind != other.Kind ||
		t.WornSlot != other.WornSlot || t.ResourceHash != other.ResourceHash || t.MinimumLevel != other.MinimumLevel ||
		t.MaximumDurability != other.MaximumDurability || t.UsableJobs != other.UsableJobs ||
		t.FirstBagSlot != other.FirstBagSlot || t.LastBagSlot != other.LastBagSlot {
		return false
	}
	if a.Source == "weapon" && string(a.Template) == "null" {
		return true // Pre-frozen weapons carry the canonical fields in their dedicated storage row.
	}
	if sameCommerceTemplate(d.Template, a.Template) {
		return true
	}
	var stored, expected map[string]json.RawMessage
	if json.Unmarshal(a.Template, &stored) != nil || json.Unmarshal(d.Template, &expected) != nil ||
		(len(stored["MythologyHash"]) != 0 && string(stored["MythologyHash"]) != "null") ||
		(len(stored["MythologyOptions"]) != 0 && string(stored["MythologyOptions"]) != "0") {
		return false
	}
	stored["MythologyHash"], stored["MythologyOptions"] = expected["MythologyHash"], expected["MythologyOptions"]
	frozen, err := json.Marshal(stored)
	return err == nil && sameCommerceTemplate(d.Template, frozen)
}

func (x *CommerceTx) ChangeQuality(q QualityRequest, c QualityCatalog, now time.Time) error {
	if q.TargetSlot == q.SourceSlot || now.Unix() <= 0 || (q.Opcode != 81 && q.Opcode != 1733 && q.Opcode != 1826) {
		return ErrInvalid
	}
	source, _, _, _, err := x.giftSource(q.SourceSlot, now)
	if err != nil {
		return err
	}
	src := x.Inventory.Items[source]
	isMythBox := src.TemplateID == qualityMythologySource
	if !sameCommerceTemplate(c.Sources[src.TemplateID], src.Template) || !isMythBox && (q.Opcode != 81 || q.Mode != 0) {
		return ErrInvalid
	}
	target := x.bagSource(q.TargetSlot)
	if target < 0 {
		return ErrInvalid
	}
	a := x.Inventory.Items[target]
	t, err := moveTemplate(&a)
	if err != nil || a.CharacterID != x.Character.ID || q.Opcode == 81 && q.Target != a.TemplateID || t.Kind == 2 ||
		t.WornSlot < 12 || t.WornSlot > 25 || a.Count != 1 || a.UID == "" || q.TargetSlot > t.bagEnd(x.Inventory.BagExpansion) {
		return ErrInvalid
	}
	d, ok := c.Targets[a.TemplateID]
	if !ok || !qualityMatches(t, a, d) || !qualitySourceFits(src.TemplateID, t, d) {
		return ErrInvalid
	}
	var v materialAttributes
	var lifetime materialDefinition
	if json.Unmarshal(a.Attributes, &v) != nil || json.Unmarshal(a.Template, &lifetime) != nil ||
		len(a.Core) != 119 || lifetime.ResourceExpiration != nil && *lifetime.ResourceExpiration <= now.Unix() ||
		v.QualitySeed == 0 || v.QualitySeed > 999999998 || a.Weapon != nil && v.QualitySeed != a.Weapon.QualitySeed {
		return ErrInvalid
	}
	core := bytes.Clone(a.Core)
	extra := bytes.Clone(a.Core)
	snapshot := a
	var state qualityMythology
	var native [5]byte
	options := c.Mythology[a.TemplateID]
	if v.Mythology != nil {
		if json.Unmarshal(*v.Mythology, &state) != nil || state.SourceHash != options.Hash || len(options.Hash) != 64 ||
			state.Count != options.Count || state.Count < 3 || state.Count > 4 || len(options.Options) != int(state.Count) ||
			state.Fifth != 0 || state.Count == 3 && state.Fourth != 0 {
			return ErrInvalid
		}
		native = [5]byte{state.First, state.Second, state.Third, state.Fourth, state.Fifth}
		if !bytes.Equal(native[:], a.Core[114:]) {
			return ErrInvalid
		}
		for i, list := range options.Options {
			if !slices.Contains(list, native[i]) {
				return ErrInvalid
			}
		}
		clear(extra[114:])
		v.Mythology = nil
	} else if isMythBox {
		return ErrInvalid
	}
	snapshot.Core = extra
	if v.MagicSeal != nil {
		var seal magicSealResult
		if json.Unmarshal(*v.MagicSeal, &seal) != nil || seal.First == nil || seal.Third != nil && seal.Second == nil ||
			seal.PolicyVersion != 1 || seal.GoldCost == 0 || seal.ResourceHash != t.ResourceHash {
			return ErrInvalid
		}
		var names [256]bool
		for _, o := range []*magicSealChoice{seal.First, seal.Second, seal.Third} {
			if o == nil {
				continue
			}
			if o.ID == 0 || names[o.ID] || o.FirstRatio > 100 || o.SecondRatio > 100 {
				return ErrInvalid
			}
			names[o.ID] = true
		}
		v.MagicSeal = nil
		plain, err := ordinaryNewCore(snapshot, lifetime, v)
		if err != nil || !plain {
			return ErrInvalid
		}
	}
	if !repairable(snapshot, v) {
		return ErrInvalid
	}
	var attrs map[string]json.RawMessage
	if err := json.Unmarshal(a.Attributes, &attrs); err != nil || attrs == nil {
		return ErrInvalid
	}
	draw, err := giftRandom(999999997)
	if err != nil {
		return err
	}
	seed := uint32(draw) + 1
	if isMythBox {
		if q.Opcode != 1733 && q.Mode >= uint16(state.Count) {
			return ErrInvalid
		}
		for i, list := range options.Options {
			if q.Opcode != 1733 && i != int(q.Mode) {
				continue
			}
			option, err := rollQualityMyth(list, a.TemplateID, i, seed)
			if err != nil {
				return err
			}
			native[i] = option
		}
		state.First, state.Second, state.Third, state.Fourth = native[0], native[1], native[2], native[3]
		copy(core[114:], native[:])
		attrs["Mythology"], err = json.Marshal(state)
	} else {
		switch src.TemplateID {
		case 10150860, 10150861, 10150862, 10150863, 10150903, 490000155, 2683895, 490013009, 490013699:
			seed = 999999998
		}
		binary.LittleEndian.PutUint32(core[6:], seed)
		attrs["QualitySeed"], err = json.Marshal(seed)
		if a.Weapon != nil {
			weapon := *a.Weapon
			weapon.QualitySeed = seed
			a.Weapon = &weapon
		}
	}
	if err != nil {
		return err
	}
	if err = x.debitAsset(source, 1); err != nil {
		return err
	}
	if err = x.updateCraftAsset(a, attrs, core); err != nil {
		return err
	}
	x.moveCount = src.Count - 1
	return nil
}

func rollQualityMyth(options []byte, id uint32, slot int, seed uint32) (byte, error) {
	if len(options) == 0 {
		return 0, ErrInvalid
	}
	n := uint64(len(options))
	limit := (uint64(1) << 32) / n * n
	for attempt := range 64 {
		hash := sha256.Sum256([]byte(fmt.Sprintf("quality:%d:%d:%d:%d", id, slot, seed, attempt)))
		v := uint64(binary.LittleEndian.Uint32(hash[:]))
		if v < limit {
			return options[v%n], nil
		}
	}
	return 0, ErrInvalid
}

func (x *CommerceTx) updateCraftAsset(a Asset, attrs map[string]json.RawMessage, core []byte) error {
	updated, err := json.Marshal(attrs)
	if err != nil {
		return err
	}
	w, err := json.Marshal(a.Weapon)
	if err != nil {
		return err
	}
	_, err = x.tx.ExecContext(x.ctx, `UPDATE s9a1.assets SET attributes_json=$2::json,core=$3,weapon_facts=$4::json WHERE instance_id=$1`,
		a.UID, string(updated), core, string(w))
	if err == nil {
		a.Attributes, a.Core = updated, core
		x.Granted = a
	}
	return err
}
