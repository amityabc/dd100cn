package dungeon

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const WisdomDungeon uint32 = 100000151
const WisdomGate uint16 = 182
const WisdomPolicy = "Wisdom/explicit-replica-equipment-and-material-reference;not-native-probability/v1"
const bossPolicy = "OculusBossEquipment/user-authorized-wisdom-reference;PVF-weighted-pools;retained-unavailable-outcomes/v2"
const equipmentDenominator uint32 = 1000000

var errWisdom = errors.New("invalid Wisdom or Oculus reward resources")

type BossCandidate struct {
	Id, Weight uint32
	Template   json.RawMessage
	Options    [][]byte
}
type BossEquipmentCatalog struct {
	Hash         string
	Epic, Mythic uint32
	Pools        map[byte][]BossCandidate
}
type BossEquipmentPlan struct {
	Policy, Hash                     string
	Epic, Mythic, Outcome            uint32
	Pool                             byte
	TotalWeight, Draw, Start, Weight uint32
	Reward                           *GroundReward
	EmptyPool                        bool    `json:",omitempty"`
	UnavailableItem                  *uint32 `json:",omitempty"`
}
type MythologyState struct {
	SourceHash                                 string
	Count, First, Second, Third, Fourth, Fifth byte
	Valid                                      bool
}
type WisdomCatalog struct {
	Hash          string
	PillarRewards bool
	Products      []BossCandidate
	Materials     map[uint32]json.RawMessage
}
type WisdomReward struct {
	Policy, Hash                        string
	Outcome                             uint32
	Pool                                byte
	Reward                              *GroundReward
	PillarRewards                       bool `json:",omitempty"`
	NormalMaterials, AdventureMaterials []GroundReward
}

func (p *WisdomReward) display() byte {
	switch p.Pool {
	case 3:
		return 6
	case 2:
		return 3
	default:
		return 0
	}
}

// Both source owners use the same rejection sampler with different seed prefixes.
type equipmentDraw struct {
	prefix   string
	sequence uint32
	err      error
}

func (d *equipmentDraw) next(bound uint32) uint32 {
	if bound == 0 || d.err != nil {
		d.err = errWisdom
		return 0
	}
	limit := (uint64(1) << 32) / uint64(bound) * uint64(bound)
	for attempt := 0; attempt < 64; attempt++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\n%d\n%d", d.prefix, d.sequence, attempt)))
		value := le.Uint32(sum[:])
		if uint64(value) < limit {
			d.sequence++
			return value % bound
		}
	}
	d.err = errWisdom
	return 0
}

func (c *BossEquipmentCatalog) Plan(run string, actor uint16) (*BossEquipmentPlan, error) {
	if c == nil || !isHash(c.Hash) || len(run) != 36 || actor == 0 {
		return nil, errWisdom
	}
	draw := equipmentDraw{prefix: fmt.Sprintf("%s\n%s\n%d", c.Hash, strings.ReplaceAll(run, "-", ""), actor)}
	return c.pick(draw.next(equipmentDenominator), &draw)
}

func (c *BossEquipmentCatalog) pick(outcome uint32, d *equipmentDraw) (*BossEquipmentPlan, error) {
	if c == nil || !isHash(c.Hash) || outcome >= equipmentDenominator || uint64(c.Epic)+uint64(c.Mythic) > uint64(equipmentDenominator) {
		return nil, errWisdom
	}
	p := &BossEquipmentPlan{Policy: bossPolicy, Hash: c.Hash, Epic: c.Epic, Mythic: c.Mythic, Outcome: outcome}
	switch {
	case outcome < c.Mythic:
		p.Pool = 3
	case outcome < c.Mythic+c.Epic:
		p.Pool = 2
	default:
		return p, d.err
	}
	pool, ok := c.Pools[p.Pool]
	if !ok {
		return nil, errWisdom
	}
	var sum uint64
	for _, row := range pool {
		sum += uint64(row.Weight)
	}
	if sum > math.MaxUint32 {
		return nil, errWisdom
	}
	p.TotalWeight = uint32(sum)
	if sum == 0 {
		p.EmptyPool = true
		return p, d.err
	}
	p.Draw = d.next(p.TotalWeight)
	for _, row := range pool {
		if p.Draw >= p.Start+row.Weight {
			p.Start += row.Weight
			continue
		}
		p.Weight = row.Weight
		if len(row.Template) == 0 || string(row.Template) == "null" {
			id := row.Id
			p.UnavailableItem = &id
			return p, d.err
		}
		var template dropTemplate
		if json.Unmarshal(row.Template, &template) != nil || template.TemplateID != row.Id || template.Kind > 1 || template.MinimumLevel != 100 {
			return nil, errWisdom
		}
		item := &GroundReward{Template: row.Template, Durability: template.MaximumDurability}
		if p.Pool == 3 {
			n := len(row.Options)
			if n != 3 && n != 4 || n != int(template.MythologyOptions) || !isHash(template.MythologyHash) {
				return nil, errWisdom
			}
			var values [5]byte
			for i, list := range row.Options {
				if len(list) == 0 {
					return nil, errWisdom
				}
				values[i] = list[d.next(uint32(len(list)))]
			}
			item.Mythology = &MythologyState{template.MythologyHash, byte(n), values[0], values[1], values[2], values[3], values[4], true}
		}
		item.QualitySeed = d.next(999999998) + 1
		p.Reward = item
		return p, d.err
	}
	return nil, errWisdom
}

func (w *WisdomCatalog) Plan(seed uint32, boss *BossEquipmentCatalog) (*WisdomReward, error) {
	if w == nil || !isHash(w.Hash) || boss == nil {
		return nil, errWisdom
	}
	draw := equipmentDraw{prefix: fmt.Sprintf("%s\n%d", w.Hash, seed)}
	outcome := draw.next(equipmentDenominator)
	p := &WisdomReward{Policy: WisdomPolicy, Hash: w.Hash, Outcome: outcome, PillarRewards: w.PillarRewards}
	switch {
	case outcome < 600:
		p.Pool = 3
	case outcome < 100600:
		p.Pool = 2
	case outcome < 101600:
		p.Pool = 1
	}
	if p.Pool >= 2 {
		forced := uint32(0)
		if p.Pool == 2 {
			forced = 600
		}
		pick, err := boss.pick(forced, &draw)
		if err != nil || pick.Reward == nil {
			return nil, errWisdom
		}
		p.Reward = pick.Reward
	} else if p.Pool == 1 {
		var sum uint64
		for _, row := range w.Products {
			sum += uint64(row.Weight)
		}
		if sum == 0 || sum > math.MaxUint32 {
			return nil, errWisdom
		}
		n := draw.next(uint32(sum))
		for _, row := range w.Products {
			if n >= row.Weight {
				n -= row.Weight
				continue
			}
			var template dropTemplate
			if json.Unmarshal(row.Template, &template) != nil || template.Kind > 1 || template.TemplateID != row.Id {
				return nil, errWisdom
			}
			p.Reward = &GroundReward{Template: row.Template, QualitySeed: draw.next(999999998) + 1, Durability: template.MaximumDurability}
			break
		}
	}
	extra := uint32(0)
	if draw.next(equipmentDenominator) < 500000 {
		extra = 1
	}
	type material struct{ id, normal, adventure uint32 }
	materials := []material{{10313459, 7, 10}, {10312874, extra, 2 + extra}}
	for _, rule := range [][2]uint32{{10158124, 10000}, {10313659, 1000}, {10313633, 6000}} {
		if draw.next(equipmentDenominator) < rule[1] {
			materials = append(materials, material{rule[0], 1, 1})
		}
	}
	materials = append(materials, material{490020105, 1, 1})
	for _, m := range materials {
		raw := w.Materials[m.id]
		if len(raw) == 0 {
			return nil, errWisdom
		}
		row := GroundReward{Template: raw, PvfDungeonReward: true, StackCount: m.normal}
		if m.normal != 0 {
			p.NormalMaterials = append(p.NormalMaterials, row)
		}
		row.StackCount = m.adventure
		p.AdventureMaterials = append(p.AdventureMaterials, row)
	}
	return p, draw.err
}

func (p *WisdomReward) phaseDrops(difficulty byte, phase uint16, now time.Time) ([]GroundReward, error) {
	materials := p.NormalMaterials
	if difficulty == 1 {
		materials = p.AdventureMaterials
	}
	var items []GroundReward
	if p.Reward != nil && (phase == 3 && !p.PillarRewards || phase == 8 && p.PillarRewards) {
		items = append(items, *p.Reward)
	}
	for _, row := range materials {
		var t dropTemplate
		if json.Unmarshal(row.Template, &t) != nil || row.StackCount == 0 {
			return nil, errWisdom
		}
		split := p.PillarRewards && (t.TemplateID == 10313459 || t.TemplateID == 10312874)
		if phase == 3 && !split {
			if t.TemplateID == 490020105 && t.WeeklyDeletionDay != nil && *t.WeeklyDeletionDay == 4 {
				row.ExpiresAt = dropExpiration(t, now)
			}
			items = append(items, row)
		} else if phase == 8 && split {
			n := row.StackCount
			if n > 13 {
				return nil, errWisdom
			}
			row.StackCount = 1
			for range n {
				items = append(items, row)
			}
		}
	}
	return items, nil
}
