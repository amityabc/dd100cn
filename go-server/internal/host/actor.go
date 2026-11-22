package host

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

type skillPage struct {
	Sp, Tp uint16
	Skills []protocol.ActorSkill
}
type actorProfile struct {
	Progression struct {
		Experience                    uint64
		GrowType                      byte
		Sp, Tp, Fatigue, FatigueLimit uint16
	}
	Initialized, AuraSkinUnlocked, PetSkinUnlocked bool
	Skills                                         []protocol.ActorSkill
	SkillStyles                                    *struct {
		Active byte
		Other  skillPage
	}
	SkillCommands    map[uint16][]byte
	CommandLocks     []byte
	Quests           resources.TownQuests
	BuffRegistration *struct {
		Skill uint16
		Items map[byte]string
	}
	NameTag *struct {
		TemplateID uint32
		ExpiresAt  int64
	}
	Fate struct {
		ArchangelPoints, StoryCount, FullClears int32
		Blessings, Sins, EquippedEffects        []int32
		StoryWeek                               string
	}
	ClassReplacementCount, GoldExpansion, CrystalContractSelection byte
	SpecialAssets                                                  struct{ Items []json.RawMessage }
}

func readActor(c storage.Character, channel uint16) (protocol.Actor, actorProfile, error) {
	var p actorProfile
	p.CrystalContractSelection = 255
	if err := json.Unmarshal(c.Gameplay, &p); err != nil {
		return protocol.Actor{}, p, err
	}
	var stats struct{ Values map[string]int64 }
	if err := json.Unmarshal(c.Stats, &stats); err != nil {
		return protocol.Actor{}, p, err
	}
	b, err := protocol.Stats(stats.Values)
	if err != nil {
		return protocol.Actor{}, p, err
	}
	if channel > 255 || c.Position.Town > 255 || c.Position.Area > 255 {
		return protocol.Actor{}, p, storage.ErrInvalid
	}
	a := protocol.Actor{RosterCharacter: protocol.RosterCharacter{ID: c.ID, Name: c.Name, Job: c.Job, Level: c.Level, GrowType: p.Progression.GrowType, Worn: wornEquipment(c.Worn)}, Stats: b, Experience: p.Progression.Experience,
		Channel: byte(channel), Town: byte(c.Position.Town), Area: byte(c.Position.Area), Direction: c.Position.Direction, AreaState: c.Position.State, X: c.Position.X, Y: c.Position.Y, FatigueLimit: p.Progression.FatigueLimit, SkillStyle: 255, Skills: p.Skills}
	if len(p.SpecialAssets.Items) > 0 {
		if err = inventorywire.SpecialActor(c.Gameplay, &a, time.Now()); err != nil {
			return a, p, err
		}
		a.AvatarActionProof = questwire.AvatarActionProof(c.Gameplay)
	}
	if p.Initialized {
		if p.Progression.Fatigue > a.FatigueLimit {
			return a, p, storage.ErrInvalid
		}
		a.FatigueUsed = a.FatigueLimit - p.Progression.Fatigue
	}
	for i, id := range [...]uint16{649, 650, 2636} {
		if p.Quests.Cleared[id] != 0 {
			a.EquipmentSlotMask |= [...]byte{1, 2, 16}[i]
		}
	}
	if p.AuraSkinUnlocked {
		a.EquipmentSlotMask |= 8
	}
	if p.PetSkinUnlocked {
		a.EquipmentSlotMask |= 32
	}
	if reg := p.BuffRegistration; reg != nil && slices.ContainsFunc(p.Skills, func(s protocol.ActorSkill) bool { return s.ID == reg.Skill && s.Level > 0 }) {
		a.BuffSkill = reg.Skill
	}
	if tag := p.NameTag; tag != nil && tag.ExpiresAt > time.Now().Unix() {
		if tag.ExpiresAt > math.MaxInt32 || tag.TemplateID == 0 || tag.TemplateID == math.MaxUint32 {
			return a, p, storage.ErrInvalid
		}
		a.NameTag = &protocol.ActorNameTag{TemplateID: tag.TemplateID, ExpiresAt: uint32(tag.ExpiresAt)}
	}
	if style := p.SkillStyles; style != nil {
		if style.Active > 1 || !p.Initialized {
			return a, p, storage.ErrInvalid
		}
		a.SkillStyle = style.Active
		a.SecondSkills = style.Other.Skills
		if style.Active == 1 {
			a.Skills, a.SecondSkills = a.SecondSkills, a.Skills
		}
	}
	return a, p, nil
}

func skillPackets(p actorProfile) ([]byte, []byte, error) {
	locks := p.CommandLocks
	if locks == nil {
		locks = bytes.Repeat([]byte{255}, 512)
		locks[186], locks[187] = 0, 0
	}
	if len(locks) != 512 {
		return nil, nil, storage.ErrInvalid
	}
	appendValue := func(b []byte, field byte, v uint64) []byte { return binary.AppendUvarint(append(b, field<<3), v) }
	appendMessage := func(b []byte, field byte, v []byte) []byte {
		return append(binary.AppendUvarint(append(b, field<<3|2), uint64(len(v))), v...)
	}
	tree := func(page skillPage) ([]byte, error) {
		b := appendValue(nil, 1, uint64(page.Sp))
		b = appendValue(b, 2, uint64(page.Tp))
		skills := slices.Clone(page.Skills)
		slices.SortFunc(skills, func(a, b protocol.ActorSkill) int { return int(a.ID) - int(b.ID) })
		occupied := map[uint16]bool{}
		for i, s := range skills {
			if len(skills) > 255 || s.ID == 0 || s.Level == 0 || s.Slot > 254 || occupied[s.Slot] || i > 0 && skills[i-1].ID == s.ID {
				return nil, storage.ErrInvalid
			}
			occupied[s.Slot] = true
			row := appendValue(nil, 1, uint64(s.Slot))
			row = appendValue(row, 2, uint64(s.ID))
			row = appendValue(row, 3, uint64(s.Level))
			if keys := p.SkillCommands[s.ID]; len(keys) > 0 {
				var data []byte
				for _, k := range keys {
					data = binary.AppendUvarint(data, uint64(k))
				}
				row = appendMessage(row, 4, data)
			}
			b = appendMessage(b, 3, row)
		}
		return b, nil
	}
	first := skillPage{p.Progression.Sp, p.Progression.Tp, p.Skills}
	second := first
	if style := p.SkillStyles; style != nil {
		second = style.Other
		if style.Active == 1 {
			first, second = second, first
		}
	}
	page0, err := tree(first)
	if err != nil {
		return nil, nil, err
	}
	page1, err := tree(second)
	if err != nil {
		return nil, nil, err
	}
	body := appendMessage(appendMessage(appendValue(nil, 1, 19), 2, page0), 2, page1)
	return append(binary.LittleEndian.AppendUint32(nil, 512), locks...), append(binary.LittleEndian.AppendUint32(nil, uint32(len(body))), body...), nil
}

func buffReset(p actorProfile, in storage.Inventory) ([]byte, error) {
	b := make([]byte, 193)
	b[2] = 38
	for part := byte(0); part < 38; part++ {
		i := 3 + 5*int(part)
		b[i+1], b[i+2] = part, 49
		binary.LittleEndian.PutUint16(b[i+3:], 65535)
		if p.BuffRegistration == nil {
			continue
		}
		uid, exists := p.BuffRegistration.Items[part]
		if !exists {
			continue
		}
		for _, asset := range in.Items {
			if !strings.EqualFold(strings.ReplaceAll(uid, "-", ""), strings.ReplaceAll(asset.UID, "-", "")) || asset.Location != 0 && asset.Location != 3 {
				continue
			}
			var expires struct{ ExpiresAt *int64 }
			if err := json.Unmarshal(asset.Attributes, &expires); err != nil {
				return nil, err
			}
			if expires.ExpiresAt != nil && *expires.ExpiresAt <= time.Now().Unix() {
				break
			}
			wornSlot := uint16(12)
			if asset.Source != "weapon" {
				var t struct {
					Kind             int
					WornSlot         uint16
					WornSlotIsNative bool
				}
				if err := json.Unmarshal(asset.Template, &t); err != nil {
					return nil, err
				}
				if t.Kind != 1 || !t.WornSlotIsNative {
					break
				}
				wornSlot = t.WornSlot
			}
			if wornSlot == uint16(part) {
				b[i], b[i+2] = 1, asset.Location
				binary.LittleEndian.PutUint16(b[i+3:], asset.Slot)
			}
			break
		}
	}
	return b, nil
}
