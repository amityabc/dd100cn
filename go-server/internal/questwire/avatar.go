package questwire

import (
	"encoding/json"
	"errors"
	"math"
	"s9a1/server/internal/storage"
	"slices"
)

func ReadAvatarEdit(op uint16, b []byte) (storage.AvatarEditRequest, error) {
	q := storage.AvatarEditRequest{Opcode: op}
	bad := errors.New("invalid native avatar editing request")
	validID := func(id uint32) bool { return id != 0 && id != math.MaxUint32 }
	switch op {
	case 206:
		if len(b) != 8 {
			return q, bad
		}
		q.Avatar, q.AvatarTemplate, q.Source = little.Uint16(b), little.Uint32(b[2:]), little.Uint16(b[6:])
		if q.Source > 120 || !validID(q.AvatarTemplate) {
			return q, bad
		}
	case 451:
		if len(b) != 13 {
			return q, bad
		}
		q.Source, q.SourceTemplate, q.Avatar, q.AvatarTemplate, q.Ability = little.Uint16(b), little.Uint32(b[2:]), little.Uint16(b[6:]), little.Uint32(b[8:]), b[12]
		if q.Ability == 255 || !validID(q.SourceTemplate) || !validID(q.AvatarTemplate) {
			return q, bad
		}
	case 490:
		if len(b) != 4 {
			return q, bad
		}
		q.Source, q.Avatar = little.Uint16(b), little.Uint16(b[2:])
	default:
		return q, bad
	}
	if q.Avatar > 479 {
		return q, bad
	}
	return q, nil
}

func AvatarEditAccepted(q storage.AvatarEditRequest, result []byte) []byte {
	b := little.AppendUint16([]byte{1}, q.Avatar)
	switch q.Opcode {
	case 206:
		return little.AppendUint16(b, q.Source)
	case 451:
		return append(b, q.Ability)
	case 490:
		b = little.AppendUint32(b, uint32(len(result)))
		return append(b, result...)
	}
	return nil
}

func (c *Catalog) AvatarEdits() storage.AvatarEditCatalog { return c.growth.AvatarEdits }

func ReadAvatarDisassembly(b []byte) (uint16, uint32, error) {
	bad := errors.New("invalid native avatar disassembly request")
	if len(b) != 6 {
		return 0, 0, bad
	}
	slot, id := little.Uint16(b), little.Uint32(b[2:])
	if slot >= 480 || id == 0 || id == math.MaxUint32 {
		return 0, 0, bad
	}
	return slot, id, nil
}

func AvatarDisassemblyAccepted(slot uint16, grants []storage.Asset, in storage.Inventory) ([]byte, error) {
	b := little.AppendUint16([]byte{1}, slot)
	b = little.AppendUint16(b, uint16(len(grants)))
	for _, g := range grants {
		i := slices.IndexFunc(in.Items, func(a storage.Asset) bool { return a.Location == 0 && a.Slot == g.Slot && a.TemplateID == g.TemplateID })
		if i < 0 {
			return nil, storage.ErrInvalid
		}
		b = little.AppendUint16(b, g.Slot)
		b = little.AppendUint32(b, g.TemplateID)
		b = little.AppendUint32(b, in.Items[i].Count)
	}
	return b, nil
}

func (c *Catalog) AvatarSkillAvailable(char storage.Character, skill uint16) bool {
	var p struct {
		Progression struct{ GrowType byte }
		Skills      []questSkill
	}
	if json.Unmarshal(char.Gameplay, &p) != nil {
		return false
	}
	if d, ok := c.definition(char.Job, skill); ok {
		grow := p.Progression.GrowType
		if grow&15 > 5 || grow>>4 > 3 {
			return false
		}
		d.ThirdAwakeningRequirements = nil
		return d.cap(math.MaxUint8, grow, false, nil) > 0
	}
	return slices.ContainsFunc(p.Skills, func(s questSkill) bool { return s.Id == skill && s.Level > 0 })
}
