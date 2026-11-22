package inventorywire

import (
	"bytes"
	"math"
	"slices"

	"s9a1/server/internal/protocol"
)

type specialState struct {
	Items           []specialItem
	AvatarExpansion uint16
}
type avatar struct {
	Sockets, Colors []byte
	Option          uint32
}
type pet struct {
	NativeSerial, Exp                                                uint32
	Name                                                             string
	Level, Satiety, ModeFlag, Evolution, EvolutionProgress, TailFlag byte
}
type specialItem struct {
	Template struct {
		TemplateID   uint32
		Kind         byte
		WornSlot     uint16
		ResourceHash string
	}
	Location   byte
	Slot       uint16
	Attributes attributes
	Core153    []byte
	Avatar     *avatar
	Pet        *pet
	Artifact   *struct{ Color byte }
}

func (s specialItem) view(opts Options) (row, error) {
	t := s.Template
	if len(s.Core153) != 153 || t.TemplateID == 0 || t.TemplateID == math.MaxUint32 {
		return row{}, errShape
	}
	raw := bytes.Clone(s.Core153[:119])
	le.PutUint16(raw, s.Slot)
	le.PutUint32(raw[2:], t.TemplateID)
	bagFits, wearFits := false, false
	switch t.Kind {
	case 0:
		if s.Avatar == nil || s.Pet != nil || s.Artifact != nil || len(s.Avatar.Sockets) > 30 || len(s.Avatar.Colors) > 4 || t.WornSlot > 11 {
			return row{}, errShape
		}
		bagFits = s.Location == 1
		wearFits = s.Slot == t.WornSlot || t.WornSlot == 9 && s.Slot == 11
		// The inventory ItemView adapter leaves avatar lifetime at its default zero.
		le.PutUint32(raw[6:], 0)
	case 1:
		p := s.Pet
		if p == nil || s.Avatar != nil || s.Artifact != nil || p.NativeSerial == 0 || p.Level < 1 || p.Level > 100 || p.Satiety > 100 || p.ModeFlag > 1 || p.TailFlag > 1 || t.WornSlot != 26 {
			return row{}, errShape
		}
		bagFits = s.Location == 7 && s.Slot <= 159
		wearFits = s.Slot == 26 || s.Slot == 33
		le.PutUint32(raw[6:], p.NativeSerial)
		left := int64(-1)
		if s.Attributes.ExpiresAt != nil {
			left = max(0, min(*s.Attributes.ExpiresAt-opts.Now.Unix(), math.MaxInt32))
		}
		le.PutUint32(raw[0x16:], uint32(int32(left)))
	case 2:
		if s.Artifact == nil || s.Pet != nil || s.Avatar != nil || t.WornSlot < 27 || t.WornSlot > 29 || uint16(s.Artifact.Color) != t.WornSlot {
			return row{}, errShape
		}
		bagFits = s.Location == 7 && s.Slot >= 160 && s.Slot <= 215
		wearFits = s.Slot == t.WornSlot
		le.PutUint32(raw[6:], s.Attributes.QualitySeed)
	default:
		return row{}, errShape
	}
	if !bagFits && !(s.Location == 3 && wearFits) {
		return row{}, errShape
	}
	if t.Kind != 1 {
		if s.Attributes.QualitySeed == 0 || s.Attributes.QualitySeed >= 999999999 || raw[0x27] > 2 || raw[0x3c] > 2 || raw[0x47] > 3 {
			return row{}, errShape
		}
		s.Attributes.equip(raw)
	}
	if a := s.Avatar; a != nil {
		w := writer(raw)
		w.block(a.Sockets)
		w.block(a.Colors)
		w.u32(a.Option)
		raw = w
	}
	return row{s.Slot, raw}, nil
}
func specialPackets(state specialState, skin bool, supplies []row, opts Options) ([]Packet, error) {
	if state.AvatarExpansion > 360 || state.AvatarExpansion%8 != 0 || len(state.Items) > 713 {
		return nil, errShape
	}
	avatars := []row{}
	bag := []row{}
	pets := []specialItem{}
	worn := map[uint16][]byte{}
	seen := map[[2]uint16]bool{}
	for _, item := range state.Items {
		key := [2]uint16{uint16(item.Location), item.Slot}
		if seen[key] {
			return nil, errShape
		}
		seen[key] = true
		r, err := item.view(opts)
		if err != nil {
			return nil, err
		}
		switch item.Location {
		case 1:
			if item.Slot >= 120+state.AvatarExpansion {
				return nil, errShape
			}
			avatars = append(avatars, r)
		case 7:
			bag = append(bag, r)
		case 3:
			worn[item.Slot] = r.body
		}
		if item.Pet != nil {
			pets = append(pets, item)
		}
	}
	bySlot := func(a, b row) int { return int(a.slot) - int(b.slot) }
	slices.SortFunc(avatars, bySlot)
	slices.SortFunc(bag, bySlot)
	avatarBag := writer{1}
	avatarBag.u16(state.AvatarExpansion)
	avatarBag.u16(uint16(len(avatars)))
	petBag := counted(7, append(bag, supplies...))
	if len(pets) > 162 {
		return nil, errShape
	}
	slices.SortFunc(pets, func(a, b specialItem) int {
		if a.Pet.NativeSerial < b.Pet.NativeSerial {
			return -1
		}
		if a.Pet.NativeSerial > b.Pet.NativeSerial {
			return 1
		}
		return 0
	})
	creatures := writer{byte(len(pets))}
	for i, s := range pets {
		p := s.Pet
		if i > 0 && pets[i-1].Pet.NativeSerial == p.NativeSerial {
			return nil, errShape
		}
		name := p.Name
		if opts.PetDefaultName != nil {
			if d, ok := opts.PetDefaultName(s.Template.TemplateID, s.Template.ResourceHash); ok && d == name {
				name = ""
			}
		}
		encoded, err := protocol.EncodeText(name, 29)
		if err != nil {
			return nil, err
		}
		creatures.u32(p.NativeSerial)
		creatures.byte(p.Satiety)
		creatures.byte(p.ModeFlag)
		creatures.u32(p.Exp)
		if p.ModeFlag == 1 {
			creatures.byte(p.Evolution)
			creatures.byte(p.EvolutionProgress)
		}
		creatures.byte(p.Level)
		creatures.block(encoded)
		creatures.byte(p.TailFlag)
	}
	petSlots := []uint16{26}
	if skin || worn[33] != nil {
		petSlots = append(petSlots, 33)
	}
	return []Packet{{13, appendRows(avatarBag, avatars)}, {13, petBag}, {105, creatures}, {14, fixedRows([]uint16{27, 28, 29}, worn)}, {14, fixedRows(petSlots, worn)}, {14, fixedRows([]uint16{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, worn)}}, nil
}
