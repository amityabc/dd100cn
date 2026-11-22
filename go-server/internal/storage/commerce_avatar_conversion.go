package storage

import (
	"encoding/json"
	"errors"
	"time"
)

type AvatarConversionCatalog struct {
	Sources map[uint32]AvatarConversionSource
	Targets map[uint32]AvatarConversionTarget
}

type AvatarConversionSource struct {
	Source       json.RawMessage
	Replacements map[uint32]uint32
}

type AvatarConversionTarget struct {
	Template json.RawMessage
	Grade    byte
	IsClone  bool
}

var (
	ErrAvatarConversionSource = errors.New("avatar conversion ticket identity or qualification changed")
	ErrAvatarConversionInput  = errors.New("avatar is outside the ticket's prepared conversion mapping")
)

func (x *CommerceTx) ConvertAvatar(source, target uint16, catalog AvatarConversionCatalog, now time.Time) error {
	if !x.livingEmblemUser(now) {
		return ErrPhase
	}
	slot, t, err := x.emblemBag(source, now)
	if err != nil || t.FirstBagSlot != 65 {
		return ErrAvatarConversionSource
	}
	def, ok := catalog.Sources[t.TemplateID]
	if !ok {
		return ErrInvalid
	}
	if err := x.eligibleAvatarCatalyst(slot, def.Source, now); err != nil {
		return ErrAvatarConversionSource
	}
	state, items, capacity, err := x.loadSpecial()
	if err != nil {
		return err
	}
	if target >= 120+capacity {
		return ErrInvalid
	}
	index, err := specialSlot(items, 1, target)
	if err != nil {
		return err
	}
	items[index], err = planAvatarConversion(x.Character, items[index], def, catalog.Targets)
	if err != nil {
		return err
	}
	state["Items"], err = json.Marshal(items)
	if err != nil {
		return err
	}
	if err := x.debitAsset(slot, 1); err != nil {
		return err
	}
	return replaceMailFields(&x.Character, map[string]any{"SpecialAssets": state})
}

func planAvatarConversion(c Character, item json.RawMessage, def AvatarConversionSource, targets map[uint32]AvatarConversionTarget) (json.RawMessage, error) {
	var avatar editableAvatar
	var attrs materialAttributes
	var asset map[string]json.RawMessage
	var detail map[string]json.RawMessage
	var detailID string
	if json.Unmarshal(item, &avatar) != nil || json.Unmarshal(item, &asset) != nil ||
		json.Unmarshal(avatar.Attributes, &attrs) != nil || json.Unmarshal(asset["Avatar"], &detail) != nil ||
		json.Unmarshal(detail["DetailId"], &detailID) != nil || !matchesUUID(detailID) || detailID == avatar.InstanceID ||
		!matchesUUID(avatar.InstanceID) || len(avatar.Core153) != 153 || avatar.Avatar == nil ||
		len(avatar.Avatar.Sockets) > 30 || len(avatar.Avatar.Colors) > 4 || avatar.Core153[0x27] > 2 || avatar.Core153[0x3c] > 2 || avatar.Core153[0x47] > 3 ||
		attrs.Locked || attrs.ExpiresAt != nil || attrs.Durability > 254 || attrs.QualitySeed == 0 || attrs.QualitySeed >= 999999999 ||
		avatar.Template.Kind != 0 || avatar.Template.WornSlot > 7 {
		return nil, ErrInvalid
	}
	id := avatar.Template.TemplateID
	replacement := def.Replacements[id]
	if replacement == 0 {
		return nil, ErrAvatarConversionInput
	}
	before, ok := targets[id]
	if !ok {
		return nil, ErrInvalid
	}
	after, ok := targets[replacement]
	if !ok || before.Grade != 3 && before.Grade != 4 || after.Grade != before.Grade ||
		avatar.Avatar.Option != 0 && (!before.IsClone || after.IsClone) {
		return nil, ErrInvalid
	}
	var expected, output editableAvatar
	if json.Unmarshal(before.Template, &expected.Template) != nil || json.Unmarshal(after.Template, &output.Template) != nil ||
		expected.Template.TemplateID != id || expected.Template.ResourceHash != avatar.Template.ResourceHash ||
		expected.Template.WornSlot != avatar.Template.WornSlot || output.Template.TemplateID != replacement ||
		output.Template.WornSlot != avatar.Template.WornSlot || output.Template.Kind != 0 ||
		output.Template.ResourceExpiration != nil || output.Template.RelativeLifetimeSeconds != 0 ||
		output.Template.DailyDeletion || output.Template.WeeklyDeletionDay != nil ||
		!matchesSHA(expected.Template.ResourceHash) || !matchesSHA(output.Template.ResourceHash) ||
		c.Level < output.Template.MinimumLevel || output.Template.UsableJobs&(1<<c.Job) == 0 {
		return nil, ErrInvalid
	}
	asset["Template"] = after.Template
	if !after.IsClone {
		detail["Option"] = json.RawMessage("0")
		data, err := json.Marshal(detail)
		if err != nil {
			return nil, err
		}
		asset["Avatar"] = data
	}
	return json.Marshal(asset)
}
