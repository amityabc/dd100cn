package storage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"reflect"
	"time"
)

// MoveRequest uses the two native template hints. Inventory instance UIDs
// are resolved inside the account transaction and never come from the wire.
type MoveRequest struct {
	Sequence                                                                 uint16
	SourceList, DestinationList                                              uint8
	SourceSlot, DestinationSlot                                              uint16
	SourceTemplate, Count, DestinationTemplate, DestinationStack, ActorIndex uint32
	State0, State1                                                           uint8
}

func (r MoveRequest) NativeBody() []byte {
	b := make([]byte, 28)
	b[0], b[11], b[26], b[27] = r.SourceList, r.DestinationList, r.State0, r.State1
	binary.LittleEndian.PutUint16(b[1:], r.SourceSlot)
	binary.LittleEndian.PutUint32(b[3:], r.SourceTemplate)
	binary.LittleEndian.PutUint32(b[7:], r.Count)
	binary.LittleEndian.PutUint16(b[12:], r.DestinationSlot)
	binary.LittleEndian.PutUint32(b[14:], r.DestinationTemplate)
	binary.LittleEndian.PutUint32(b[18:], r.DestinationStack)
	binary.LittleEndian.PutUint32(b[22:], r.ActorIndex)
	return b
}

type MoveOutcome struct {
	SourceList, DestinationList uint8
	SourceSlot, DestinationSlot uint16
	Count                       uint32
	Replayed                    bool
	Character                   Character
	Inventory                   Inventory
}

type qualification struct {
	Verified bool
	Jobs     []struct {
		Job   uint8
		Skill uint16
	}
}
type ordinaryTemplate struct {
	Kind                                                                    int
	TemplateID, StackLimit                                                  uint32
	FirstBagSlot, LastBagSlot, WornSlot, MaximumDurability, UsableJobs      uint16
	MinimumLevel                                                            uint8
	PetConsumable, FateCollection, AccountCommonInventory, WornSlotIsNative bool
	ResourceHash                                                            string
	WeaponQualification                                                     qualification
}

func moveTemplate(a *Asset) (ordinaryTemplate, error) {
	var t ordinaryTemplate
	if a.Source == "weapon" && a.Weapon != nil {
		w := a.Weapon
		t = ordinaryTemplate{Kind: 0, TemplateID: a.TemplateID, StackLimit: 1, FirstBagSlot: 9, LastBagSlot: 64, WornSlot: 12,
			MaximumDurability: w.MaximumDurability, MinimumLevel: w.MinimumLevel, UsableJobs: w.UsableJobs, ResourceHash: w.ResourceHash}
		if len(a.Template) > 0 && !bytes.Equal(bytes.TrimSpace(a.Template), []byte("null")) {
			var frozen ordinaryTemplate
			if err := json.Unmarshal(a.Template, &frozen); err != nil {
				return t, err
			}
			if frozen.Kind != 0 || frozen.TemplateID != t.TemplateID || frozen.FirstBagSlot != 9 || frozen.LastBagSlot != 64 || frozen.WornSlot != 12 ||
				frozen.StackLimit != 1 || frozen.MaximumDurability != w.MaximumDurability || frozen.MinimumLevel != w.MinimumLevel || frozen.UsableJobs != w.UsableJobs || frozen.ResourceHash != w.ResourceHash {
				return t, ErrInvalid
			}
			t = frozen
		}
		var stored qualification
		if len(w.Qualification) > 0 && json.Unmarshal(w.Qualification, &stored) != nil {
			return t, ErrInvalid
		}
		if stored.Verified {
			if t.WeaponQualification.Verified && !reflect.DeepEqual(t.WeaponQualification, stored) {
				return t, ErrInvalid
			}
			t.WeaponQualification = stored
		}
	} else if a.Source == "item" {
		if err := json.Unmarshal(a.Template, &t); err != nil {
			return t, err
		}
	} else {
		return t, ErrInvalid
	}
	if t.TemplateID != a.TemplateID || t.Kind < 0 || t.Kind > 2 || t.StackLimit == 0 || t.MinimumLevel == 0 || t.UsableJobs == 0 || t.FirstBagSlot < 3 || t.FirstBagSlot > t.LastBagSlot || t.LastBagSlot > 360 ||
		t.PetConsumable || t.FateCollection || t.AccountCommonInventory {
		return t, ErrInvalid
	}
	return t, nil
}

func (t ordinaryTemplate) bagEnd(expansion uint16) uint16 {
	if t.Kind == 2 && t.FirstBagSlot == 289 && t.LastBagSlot == 360 {
		return 360
	}
	return min(t.LastBagSlot, t.FirstBagSlot+31+expansion)
}

type moveAttributes struct {
	Locked, InitialAmplification bool
	AmplificationType            uint8
	ExpiresAt                    *int64
}

func movementAttributes(a *Asset) (moveAttributes, error) {
	var p moveAttributes
	if a.Source == "weapon" && len(a.Core) == 119 {
		p.AmplificationType = a.Core[19]
	}
	err := json.Unmarshal(a.Attributes, &p)
	return p, err
}

func questWeaponSkill(template uint32, job uint8) uint16 {
	switch template {
	case 101040193:
		if job == 0 || job == 9 {
			return 33
		}
		if job == 11 {
			return 95
		}
	case 102030225, 402030091, 102030227:
		if job == 1 || job == 7 {
			return 34
		}
	case 106040148, 106040494, 106040150:
		if job == 3 || job == 8 || job == 10 {
			return 91
		}
	case 109040035, 409040106, 109040037:
		if job == 6 {
			return 83
		}
	default:
		return 0
	}
	return math.MaxUint16
}

func allowsEquipment(c Character, a *Asset, t ordinaryTemplate, slot uint16) bool {
	e := *a
	e.Location, e.Slot = 3, slot
	if t.MinimumLevel > c.Level || (t.UsableJobs&(1<<c.Job)) == 0 {
		return false
	}
	attrs, err := movementAttributes(a)
	if err != nil || attrs.AmplificationType == 0x80 || (attrs.InitialAmplification && attrs.AmplificationType&0x80 != 0) {
		return false
	}
	if t.Kind == 0 {
		// A historical unverified weapon remains visible and removable.
		q := t.WeaponQualification
		if !q.Verified || q.Jobs == nil || len(q.Jobs) > 16 {
			return false
		}
		var profile struct{ Skills []struct{ ID, Level uint16 } }
		if json.Unmarshal(c.Gameplay, &profile) != nil {
			return false
		}
		learned := func(id uint16) bool {
			for _, s := range profile.Skills {
				if s.ID == id && s.Level > 0 {
					return true
				}
			}
			return false
		}
		prev := -1
		allowed := len(q.Jobs) == 0
		for _, j := range q.Jobs {
			if int(j.Job) <= prev || j.Job > 15 || j.Skill == 0 || j.Skill >= 512 {
				return false
			}
			prev = int(j.Job)
			if j.Job == c.Job && learned(j.Skill) {
				allowed = true
			}
		}
		if skill := questWeaponSkill(a.TemplateID, c.Job); !allowed || skill != 0 && !learned(skill) {
			return false
		}
	}
	_, err = e.WornEquipment(c)
	return err == nil
}

func placeOrdinary(c Character, a *Asset, list uint8, slot, expansion uint16) error {
	t, err := moveTemplate(a)
	if err != nil {
		return err
	}
	p, err := movementAttributes(a)
	if err != nil || p.Locked {
		return ErrInvalid
	}
	if list == 0 && !(t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32) && (slot >= 3 && slot <= 8 || slot >= t.FirstBagSlot && slot <= t.bagEnd(expansion)) {
		return nil
	}
	if list == 3 && (p.ExpiresAt == nil || *p.ExpiresAt > time.Now().Unix()) && allowsEquipment(c, a, t, slot) {
		return nil
	}
	return ErrInvalid
}

func equalJSON(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	d := json.NewDecoder(bytes.NewReader(a))
	d.UseNumber()
	e := json.NewDecoder(bytes.NewReader(b))
	e.UseNumber()
	return d.Decode(&x) == nil && e.Decode(&y) == nil && reflect.DeepEqual(x, y)
}

func stacks(a, b *Asset) bool {
	if a.Source != "item" || b.Source != "item" || a.TemplateID != b.TemplateID {
		return false
	}
	if _, shared := SharedMaterialSlot(a.TemplateID); shared {
		same, err := compatibleMaterial(*a, *b)
		return err == nil && same
	}
	return len(a.Core) == 119 && len(b.Core) == 119 && bytes.Equal(a.Core[10:], b.Core[10:]) && bytes.Equal(a.Detail, b.Detail) &&
		equalJSON(a.Template, b.Template) && equalJSON(a.Attributes, b.Attributes)
}
