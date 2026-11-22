package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
)

const StatsBytes = 92

// Stats maps the same signed resistances and checked integer fields as the
// native actor projection. The remaining cells belong to the legacy layout.
func Stats(values map[string]int64) ([]byte, error) {
	block := make([]byte, StatsBytes)
	fields := [...]struct {
		name   string
		offset int
		width  int // Negative width denotes a signed word.
	}{
		{"hp_max", 0, 4}, {"mp_max", 4, 4}, {"strength", 8, 2}, {"vitality", 10, 2},
		{"intelligence", 12, 2}, {"spirit", 14, 2}, {"fire_resistance", 16, -2},
		{"water_resistance", 18, -2}, {"dark_resistance", 20, -2}, {"light_resistance", 22, -2},
		{"inventory_limit", 60, 4}, {"hp_regen_speed", 64, 2}, {"mp_regen_speed", 66, 2},
		{"move_speed", 68, 4}, {"attack_speed", 72, 2}, {"cast_speed", 74, 2},
		{"hit_recovery", 76, 2}, {"jump_power", 78, 2}, {"weight", 80, 4},
	}
	for _, f := range fields {
		n, ok := values[f.name]
		if !ok {
			return nil, fmt.Errorf("missing native actor stat %s", f.name)
		}
		switch {
		case f.width == -2 && n >= math.MinInt16 && n <= math.MaxInt16, f.width == 2 && n >= 0 && n <= math.MaxUint16:
			little.PutUint16(block[f.offset:], uint16(n))
		case f.width == 4 && n >= 0 && n <= math.MaxUint32:
			little.PutUint32(block[f.offset:], uint32(n))
		default:
			return nil, fmt.Errorf("actor stat %s outside its native field", f.name)
		}
	}
	block[84] = 100
	return block, nil
}

type ActorSkill struct {
	Slot, ID uint16
	Level    byte
}

type ActorCreature struct {
	TemplateID, NativeSerial           uint32
	Name                               string
	Level, Satiety, ModeFlag, TailFlag byte
	TownDisplay                        bool
	Core119                            []byte
	RemainingSeconds                   int32 // -1 is permanent, zero is expired.
}

type ActorNameTag struct{ TemplateID, ExpiresAt uint32 }
type ActorDisguise struct {
	Kind       byte
	TemplateID uint32
}
type ActorAvatarAction struct{ Action, Kind byte }

type Actor struct {
	RosterCharacter
	Stats                                            []byte
	Experience                                       uint64
	Group, Channel, Town, Area, Direction, AreaState byte
	X, Y, FatigueUsed, FatigueLimit                  uint16
	EquipmentSlotMask, SkillStyle                    byte
	BuffSkill                                        uint16
	Skills, SecondSkills                             []ActorSkill
	Pet, PetSkin                                     *ActorCreature
	NameTag                                          *ActorNameTag
	Disguise                                         *ActorDisguise
	UniqueAvatarAction                               *ActorAvatarAction
	AvatarActionProof                                [32]byte
}

func validateSkills(skills []ActorSkill) error {
	if len(skills) > 255 {
		return errors.New("actor skill count exceeds native capacity")
	}
	ids, slots := map[uint16]bool{}, map[uint16]bool{}
	for _, s := range skills {
		if s.ID == 0 || s.Level == 0 || ids[s.ID] || slots[s.Slot] {
			return errors.New("invalid or duplicate learned skill")
		}
		ids[s.ID], slots[s.Slot] = true, true
	}
	return nil
}

func (a Actor) validate() error {
	if a.ID == 0 || a.Job > 15 || a.Level == 0 || len(a.Stats) != StatsBytes || a.Stats[84] > 100 || a.FatigueUsed > a.FatigueLimit || len(a.Worn) > 32 {
		return errors.New("invalid native actor")
	}
	if _, err := encodeName(a.Name); err != nil {
		return err
	}
	if tag := a.NameTag; tag != nil && (tag.TemplateID == 0 || tag.TemplateID == math.MaxUint32 || tag.ExpiresAt == 0 || tag.ExpiresAt > math.MaxInt32) {
		return errors.New("invalid actor name tag")
	}
	if d := a.Disguise; d != nil && (d.Kind < 1 || d.Kind > 3 || d.TemplateID == 0 || d.TemplateID > math.MaxInt32) {
		return errors.New("invalid actor disguise")
	}
	if a.SkillStyle != 0 && a.SkillStyle != 1 && a.SkillStyle != 255 || a.SkillStyle != 255 && a.SecondSkills == nil {
		return errors.New("actor skill style lacks its second tree")
	}
	if err := validateSkills(a.Skills); err != nil {
		return err
	}
	if err := validateSkills(a.SecondSkills); err != nil {
		return err
	}
	slots := map[byte]bool{}
	for _, row := range a.Worn {
		if slots[row.Slot] {
			return errors.New("duplicate worn slot")
		}
		if err := row.validate(); err != nil {
			return err
		}
		slots[row.Slot] = true
	}
	for _, p := range []*ActorCreature{a.Pet, a.PetSkin} {
		if p == nil {
			continue
		}
		if p.NativeSerial == 0 || p.Level == 0 || p.Level > 100 || p.Satiety > 100 || p.ModeFlag > 1 || p.TailFlag > 1 {
			return errors.New("invalid creature identity or state")
		}
		if _, err := encodeText(p.Name, 29); err != nil {
			return err
		}
	}
	if a.Pet != nil && a.PetSkin != nil && a.Pet.NativeSerial == a.PetSkin.NativeSerial {
		return errors.New("duplicate worn creature serial")
	}
	return nil
}

func (a Actor) nameTag(w *bodyWriter) {
	if a.NameTag == nil {
		w.zeros(8)
		return
	}
	w.u32(a.NameTag.TemplateID)
	w.u32(a.NameTag.ExpiresAt)
}

func Mode0(a Actor, native140 bool) ([]byte, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	pad := make([]byte, 71)
	if native140 {
		pad = make([]byte, 140)
		pad[131] = 1
	}
	if u := a.UniqueAvatarAction; u != nil {
		if !native140 || u.Action < 1 || u.Action > 3 || u.Kind > 31 {
			return nil, errors.New("invalid unique avatar action or profile")
		}
		pad[131], pad[132] = u.Action, u.Kind
	}
	pad[7], pad[44] = 1, 1
	little.PutUint32(pad[67:], math.MaxUint32)
	w := bodyWriter{0, 1, 0, a.Group, a.Channel}
	w.raw(pad)
	w.u16(a.ID)
	name, _ := encodeName(a.Name)
	w.block(name)
	w.raw([]byte{a.Job, a.GrowType, a.Level})
	w.zeros(3)
	w.u8(14)
	worn := [14]*Equipment{}
	for i := range a.Worn {
		if a.Worn[i].Slot < 14 {
			worn[a.Worn[i].Slot] = &a.Worn[i]
		}
	}
	for slot, row := range worn {
		if row != nil {
			if err := row.summary(&w); err != nil {
				return nil, err
			}
			continue
		}
		w.u8(byte(slot))
		w.u32(math.MaxUint32)
		w.zeros(14)
	}
	a.nameTag(&w)
	w.u32(math.MaxUint32)
	w.u32(math.MaxUint32)
	w.zeros(6)
	var petTemplate uint32
	var petName []byte
	if a.Pet != nil && a.Pet.TownDisplay {
		petTemplate = a.Pet.TemplateID
		if a.PetSkin != nil && a.PetSkin.RemainingSeconds != 0 {
			petTemplate = a.PetSkin.TemplateID
		}
		petName, _ = encodeText(a.Pet.Name, 29)
	}
	w.u32(petTemplate)
	w.block(petName)
	w.u8(flag(a.Pet != nil))
	w.zeros(14)
	w.u32(1)
	w.zeros(8)
	if a.Disguise == nil {
		w.zeros(5)
	} else {
		w.u8(a.Disguise.Kind)
		w.u32(a.Disguise.TemplateID)
	}
	w.zeros(6)
	w.zeros(12)
	w.u8(6)
	w.u32(0)
	w.u8(a.Stats[84])
	w.zeros(8)
	w.u8(a.SkillStyle)
	w.zeros(5)
	return w, nil
}

// actorRow writes the conditional actor grammar; the 119-byte inventory core
// carries additional values used only for certain rows.
func actorRow(w *bodyWriter, row Equipment, creatureLifetime *int32) {
	raw := row.Raw119
	w.u8(row.Slot)
	w.u32(row.TemplateID)
	if row.Slot <= 11 {
		w.u32(uint32(row.AvatarLifetimeSeconds))
	} else {
		w.u32(row.QualitySeed)
	}
	w.u8(row.Reinforcement)
	w.u16(row.Durability)
	w.u32(row.LinkedTemplateID)
	if row.LinkedTemplateID > 0 && row.Slot == 9 {
		w.block(row.LinkedRaw)
	}
	w.raw(raw[14:19])
	w.u8(row.AmplificationType)
	w.u16(row.AmplificationValue)
	if creatureLifetime != nil {
		w.u32(uint32(*creatureLifetime))
	}
	if row.Slot <= 11 {
		w.block(row.AvatarSockets)
		w.block(row.AvatarColors)
	}
	w.u8(raw[0x27])
	for i := 0; i < int(raw[0x27]); i++ {
		w.raw(raw[0x28+i*4 : 0x2c+i*4])
		w.raw([]byte{raw[0x30+i], raw[0x32+i], raw[0x34+i], raw[0x36+i]})
	}
	w.raw(raw[0x38:0x3d])
	w.raw(raw[0x3d : 0x3d+int(raw[0x3c])*4])
	w.raw(raw[0x45:0x48])
	for i := 0; i < int(raw[0x47]); i++ {
		w.raw([]byte{raw[0x48+i], raw[0x4b+i], raw[0x4e+i]})
	}
	if raw[0x47] > 0 {
		w.raw(raw[0x51:0x53])
		if raw[0x52] != 255 {
			w.raw(raw[0x53:0x57])
		}
	}
	w.u8(row.Forge)
	w.raw(raw[0x58:0x61])
	late := func(field []byte) {
		if len(bytes.Trim(field, "\x00")) == 0 {
			w.u32(0)
		} else {
			w.block(field)
		}
	}
	late(raw[0x61:0x65])
	late(raw[0x65:0x6d])
	w.u8(raw[0x6d])
	late(raw[0x72:0x77])
}

func (a Actor) equipmentRows(w *bodyWriter, full bool) error {
	if !full {
		w.zeros(5)
		return nil
	}
	count := len(a.Worn) + int(flag(a.Pet != nil)) + int(flag(a.PetSkin != nil))
	w.u8(byte(count))
	sorted := slices.Clone(a.Worn)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Slot < sorted[j].Slot })
	for _, row := range sorted {
		actorRow(w, row, nil)
	}
	for i, p := range []*ActorCreature{a.Pet, a.PetSkin} {
		if p == nil {
			continue
		}
		core := p.Core119
		if core == nil {
			core = make([]byte, 119)
		}
		if p.TemplateID == 0 || p.TemplateID == math.MaxUint32 || p.RemainingSeconds < -1 || len(core) != 119 || core[0x27] > 2 || core[0x3c] > 2 || core[0x47] > 3 {
			return errors.New("invalid creature actor equipment core")
		}
		slot := byte(26)
		if i == 1 {
			slot = 33
		}
		row := Equipment{Slot: slot, TemplateID: p.TemplateID, QualitySeed: p.NativeSerial, Raw119: core}
		actorRow(w, row, &p.RemainingSeconds)
	}
	w.u32(0)
	return nil
}

func (a Actor) actorStats(w *bodyWriter) {
	w.u16(a.ID)
	w.u64(a.Experience)
	w.block(a.Stats)
	w.u8(a.EquipmentSlotMask)
}

func (a Actor) petLevel() byte {
	if a.Pet != nil {
		return a.Pet.Level
	}
	return 0
}

func Mode1(a Actor, full bool) ([]byte, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	w := bodyWriter{1, 1, 0, a.Group, a.Channel}
	w.zeros(16)
	a.actorStats(&w)
	if err := a.equipmentRows(&w, full); err != nil {
		return nil, err
	}
	w.u16(a.BuffSkill)
	w.u8(0)
	a.nameTag(&w)
	w.u8(a.SkillStyle)
	second := a.SecondSkills
	if second == nil {
		second = a.Skills
	}
	for _, tree := range [][]ActorSkill{a.Skills, second} {
		sorted := slices.Clone(tree)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
		w.u8(byte(len(sorted)))
		for _, s := range sorted {
			w.u16(s.ID)
			w.u8(s.Level)
		}
	}
	w.u8(a.petLevel())
	w.zeros(19)
	return w, nil
}

func Mode3(a Actor) ([]byte, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	w := bodyWriter{3, 1, 0, a.Group, a.Channel}
	w.zeros(8)
	a.actorStats(&w)
	if err := a.equipmentRows(&w, true); err != nil {
		return nil, err
	}
	a.nameTag(&w)
	w.u8(a.SkillStyle)
	w.zeros(2)
	w.u8(a.petLevel())
	w.zeros(39)
	return w, nil
}

func Position(a Actor) []byte {
	var w bodyWriter
	w.u16(a.ID)
	w.u16(a.X)
	w.u16(a.Y)
	w.u8(a.Direction)
	w.u16(100)
	return w
}

func Area(a Actor) []byte {
	var w bodyWriter
	w.u16(a.ID)
	w.u32(uint32(a.Town))
	w.u8(a.Area)
	w.u16(a.X)
	w.u16(a.Y)
	w.u8(a.Direction)
	w.u8(a.AreaState)
	return w
}

func Town(a Actor) []byte {
	w := bodyWriter{a.Town, a.Area, 1, 0}
	w.u16(a.ID)
	w.u16(a.X)
	w.u16(a.Y)
	w.u8(a.Direction)
	w.u8(a.AreaState)
	return w
}

func Fatigue(a Actor) []byte {
	var w bodyWriter
	w.u16(a.FatigueUsed)
	w.u16(a.FatigueLimit)
	w.zeros(6)
	return w
}

type PositionRequest struct {
	X, Y          uint16
	GridDirection byte
	Speed         uint16
}

func ReadPosition(body []byte) (PositionRequest, error) {
	if len(body) != 7 || body[4] < 1 || body[4] > 9 {
		return PositionRequest{}, errors.New("invalid native position length or grid direction")
	}
	return PositionRequest{little.Uint16(body), little.Uint16(body[2:]), body[4], little.Uint16(body[5:])}, nil
}

type MotionRequest struct {
	PositionRequest
	VisualState    byte
	StateParameter uint16
}

func ReadMotion(body []byte) (MotionRequest, error) {
	if len(body) != 13 || body[10] != 0 || little.Uint16(body[11:]) != 0 {
		return MotionRequest{}, errors.New("invalid native motion length or reserved tail")
	}
	position, err := ReadPosition(body[:7])
	if err != nil {
		return MotionRequest{}, err
	}
	return MotionRequest{position, body[7], little.Uint16(body[8:])}, nil
}
