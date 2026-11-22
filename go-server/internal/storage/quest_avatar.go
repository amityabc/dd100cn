package storage

import (
	"encoding/binary"
	"encoding/json"
	"slices"
	"time"
)

type AvatarEditRequest struct {
	Opcode, Source, Avatar         uint16
	SourceTemplate, AvatarTemplate uint32
	Ability                        byte
}

type avatarEditSource struct {
	Source                   json.RawMessage
	Grade, NativeRestriction byte
	Inclusive                bool
	Color                    int16
}

type avatarEditTarget struct {
	Template     specialPurchaseTemplate
	TemplateID   uint32
	ResourceHash string
	Grade        byte
	Enabled      bool
	Abilities    []byte
	Jobs, Skills map[byte]uint16
}

type AvatarEditCatalog struct {
	AbilitySources, DyeSources               map[uint32]avatarEditSource
	AbilityTargets, DyeTargets, DrillTargets map[uint32]avatarEditTarget
	DrillSource                              *avatarEditSource
	ActionParts                              map[uint32]AvatarActionPart
	Disassembly                              AvatarDisassemblyCatalog
	Compound                                 AvatarCompoundCatalog
	SkinTickets                              map[uint32]json.RawMessage
	Conversions                              AvatarConversionCatalog
}

type AvatarActionPart struct {
	ID         uint32
	Hash       string
	Kind, Slot byte
	Set        int
}

type editableAvatar struct {
	InstanceID string
	Template   struct {
		specialPurchaseTemplate
		ResourceExpiration           *int64
		WeeklyDeletionDay            *uint8
		DailyDeletion, InitialSealed bool
	}
	Attributes json.RawMessage
	Core153    []byte
	Avatar     *struct {
		Sockets, Colors []byte
		Option          uint32
	}
}

func (x *CommerceTx) avatarEditSource(q AvatarEditRequest, c AvatarEditCatalog, now time.Time) (int, avatarEditSource, error) {
	var source avatarEditSource
	i, t, err := x.emblemBag(q.Source, now)
	if err != nil {
		return i, source, err
	}
	if t.FirstBagSlot != 65 {
		return i, source, ErrInvalid
	}
	switch q.Opcode {
	case 206:
		if t.TemplateID != 7421 || c.DrillSource == nil || c.DrillSource.NativeRestriction != 2 {
			return i, source, ErrInvalid
		}
		source = *c.DrillSource
	case 451:
		if q.SourceTemplate != t.TemplateID {
			return i, source, ErrInvalid
		}
		source = c.AbilitySources[t.TemplateID]
	case 490:
		source = c.DyeSources[t.TemplateID]
	default:
		return i, source, ErrInvalid
	}
	return i, source, x.eligibleAvatarCatalyst(i, source.Source, now)
}

func (x *CommerceTx) eligibleAvatarCatalyst(i int, expected json.RawMessage, now time.Time) error {
	a := x.Inventory.Items[i]
	var policy ordinaryTemplate
	var lifetime materialDefinition
	var attrs materialAttributes
	if !sameCommerceTemplate(expected, a.Template) || json.Unmarshal(a.Template, &policy) != nil ||
		json.Unmarshal(a.Template, &lifetime) != nil || json.Unmarshal(a.Attributes, &attrs) != nil || policy.Kind != 2 ||
		!(a.Slot >= 3 && a.Slot <= 8 && !policy.FateCollection && !policy.AccountCommonInventory && !policy.PetConsumable ||
			a.Slot >= policy.FirstBagSlot && a.Slot <= policy.bagEnd(x.Inventory.BagExpansion)) ||
		len(a.Detail) != 0 || lifetime.ResourceExpiration != nil && *lifetime.ResourceExpiration <= now.Unix() ||
		x.Character.Level < policy.MinimumLevel || x.Character.Job > 15 || policy.UsableJobs&(1<<x.Character.Job) == 0 {
		return ErrInvalid
	}
	plain, err := ordinaryNewCore(a, lifetime, attrs)
	if err != nil {
		return err
	}
	if !plain {
		return ErrInvalid
	}
	return nil
}

func (x *CommerceTx) ApplyAvatarEdit(q AvatarEditRequest, c AvatarEditCatalog, now time.Time, skillAvailable func(Character, uint16) bool) ([]byte, error) {
	if !x.livingEmblemUser(now) {
		return nil, ErrInvalid
	}
	i, source, err := x.avatarEditSource(q, c, now)
	if err != nil {
		return nil, err
	}
	state, items, _, err := x.loadSpecial()
	if err != nil {
		return nil, err
	}
	target, err := specialSlot(items, 1, q.Avatar)
	if err != nil {
		return nil, err
	}
	var a editableAvatar
	var row map[string]json.RawMessage
	if json.Unmarshal(items[target], &a) != nil || json.Unmarshal(items[target], &row) != nil ||
		!validEmblemID(a.InstanceID) || a.Template.Kind != 0 || a.Avatar == nil || len(a.Core153) != 153 ||
		!accessibleEmblem(a.Attributes, now.Unix()) {
		return nil, ErrInvalid
	}
	id := a.Template.TemplateID
	var result []byte
	switch q.Opcode {
	case 206:
		t, ok := c.DrillTargets[id]
		if !ok || id != q.AvatarTemplate || t.Template.ResourceHash != a.Template.ResourceHash ||
			t.Template.WornSlot != a.Template.WornSlot || t.Grade != 3 || a.Template.WornSlot > 7 ||
			slices.ContainsFunc(a.Avatar.Sockets, func(b byte) bool { return b != 0 }) {
			return nil, ErrInvalid
		}
		raw, err := initialSpecialSockets(t.Template)
		if err != nil {
			return nil, err
		}
		if len(raw) != 30 {
			return nil, ErrInvalid
		}
		if err = replaceAvatarBytes(row, "Sockets", raw); err != nil {
			return nil, err
		}
	case 451:
		t, ok := c.AbilityTargets[id]
		if !ok || t.TemplateID != id || id != q.AvatarTemplate || t.ResourceHash != a.Template.ResourceHash ||
			a.Template.ResourceExpiration != nil && *a.Template.ResourceExpiration <= now.Unix() ||
			a.Template.UsableJobs&(1<<x.Character.Job) == 0 || !slices.Contains(t.Abilities, q.Ability) ||
			len(t.Jobs) != 0 && t.Jobs[q.Ability]&(1<<x.Character.Job) == 0 ||
			t.Skills[q.Ability] != 0 && !skillAvailable(x.Character, t.Skills[q.Ability]) || source.Grade > 4 ||
			source.Grade != 0 && (source.Inclusive && t.Grade > source.Grade || !source.Inclusive && t.Grade != source.Grade) {
			return nil, ErrInvalid
		}
		var attrs map[string]json.RawMessage
		var durability uint16
		if json.Unmarshal(a.Attributes, &attrs) != nil || attrs == nil || json.Unmarshal(attrs["Durability"], &durability) != nil ||
			durability == uint16(q.Ability) || binary.LittleEndian.Uint16(a.Core153[11:]) != durability {
			return nil, ErrInvalid
		}
		attrs["Durability"], err = json.Marshal(q.Ability)
		if err != nil {
			return nil, err
		}
		row["Attributes"], err = json.Marshal(attrs)
		if err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint16(a.Core153[11:], uint16(q.Ability))
		row["Core153"], err = nativeByteList(a.Core153)
		if err != nil {
			return nil, err
		}
	case 490:
		t, ok := c.DyeTargets[id]
		colors := a.Avatar.Colors
		if !ok || t.TemplateID != id || t.ResourceHash != a.Template.ResourceHash || !t.Enabled || a.Avatar.Option != 0 ||
			len(colors) != 0 && len(colors) != 4 || len(colors) == 4 && binary.LittleEndian.Uint16(colors[2:]) != 0 ||
			source.Grade > 3 || source.Color < 0 || source.Color > 47 || source.Color == 0 && (len(colors) == 0 || binary.LittleEndian.Uint16(colors) == 0) ||
			source.Grade == 1 && t.Grade != 1 && t.Grade != 2 || source.Grade == 2 && t.Grade != 3 {
			return nil, ErrInvalid
		}
		result = make([]byte, 4)
		binary.LittleEndian.PutUint16(result, uint16(source.Color))
		if err = replaceAvatarBytes(row, "Colors", result); err != nil {
			return nil, err
		}
	}
	if err = x.debitAsset(i, 1); err != nil {
		return nil, err
	}
	items[target], err = json.Marshal(row)
	if err != nil {
		return nil, err
	}
	state["Items"], err = json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return result, replaceMailFields(&x.Character, map[string]any{"SpecialAssets": state})
}

func specialSlot(items []json.RawMessage, list byte, slot uint16) (int, error) {
	target := -1
	for i, item := range items {
		var f struct {
			Location byte
			Slot     uint16
		}
		if json.Unmarshal(item, &f) != nil {
			return -1, ErrInvalid
		}
		if f.Location == list && f.Slot == slot {
			if target >= 0 {
				return -1, ErrInvalid
			}
			target = i
		}
	}
	if target < 0 {
		return -1, ErrInvalid
	}
	return target, nil
}

func nativeByteList(raw []byte) (json.RawMessage, error) {
	return json.Marshal(legacyByteArray(raw))
}

func replaceAvatarBytes(row map[string]json.RawMessage, field string, value []byte) error {
	var detail map[string]json.RawMessage
	if json.Unmarshal(row["Avatar"], &detail) != nil || detail == nil {
		return ErrInvalid
	}
	var err error
	detail[field], err = nativeByteList(value)
	if err != nil {
		return err
	}
	row["Avatar"], err = json.Marshal(detail)
	return err
}
