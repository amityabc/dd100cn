package dungeon

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"s9a1/server/internal/storage"
)

type specialCardChoice struct{ Id, Weight, Count uint32 }
type specialCardContainer struct {
	Smart  bool
	Groups [][]specialCardChoice
}
type specialCardContainers map[uint32]specialCardContainer

func (c *specialCardContainers) UnmarshalJSON(raw []byte) error {
	if len(raw) == 0 || raw[0] != '[' {
		return json.Unmarshal(raw, (*map[uint32]specialCardContainer)(c))
	}
	var rows []struct {
		Key   uint32
		Value specialCardContainer
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return err
	}
	out := make(specialCardContainers, len(rows))
	for _, row := range rows {
		if _, duplicate := out[row.Key]; row.Key == 0 || duplicate {
			return storage.ErrInvalid
		}
		out[row.Key] = row.Value
	}
	*c = out
	return nil
}

type SpecialCardData struct {
	Policy, Hash string
	Counter      uint32
	Limit        byte
	Rows         map[byte][]specialCardChoice
	Containers   specialCardContainers
	Templates    map[uint32]json.RawMessage
	SmartPools   map[uint16][]uint32
	Definitions  map[byte]string
}

type SpecialCardPlan struct {
	Policy, ResourceHash string
	Day                  int64
	Limit, Ordinal       byte
	Container            uint32
	Items                []SpecialCardItem
	DungeonId            uint32 `json:",omitempty"`
}

type SpecialCardItem struct {
	Template       json.RawMessage
	Count          uint32
	Attributes     CardAttributes
	Raw119, Detail []uint16
}

type CardAttributes struct {
	QualitySeed                                                 uint32
	Durability                                                  uint16
	Reinforcement, Forge, AmplificationType                     byte
	AmplificationValue                                          uint16
	Enchantment                                                 uint32
	Binding                                                     byte
	ExpiresAt                                                   *int64
	Locked                                                      bool
	EnchantUpgradeCount                                         byte
	EpicExchangeInstance, DisjointRestricted, BanTransportStone bool            `json:",omitempty"`
	InitialAmplification                                        bool            `json:",omitempty"`
	MagicSeal                                                   json.RawMessage `json:",omitempty"`
	Mythology                                                   *MythologyState `json:",omitempty"`
}

type cardCounterItem struct {
	Template       json.RawMessage
	Attributes     CardAttributes
	Raw119, Detail []uint16
	StackCount     uint32 `json:",omitempty"`
}

type CardCounterReward struct {
	Policy, ResourceHash string
	Gold                 uint32
	Items                []cardCounterItem    `json:",omitempty"`
	Luck                 *CardLuckSnapshot    `json:",omitempty"`
	Artifact             *CardCounterArtifact `json:",omitempty"`
}

type CardCounterArtifact struct {
	Template    json.RawMessage
	QualitySeed uint32
}

type CardCounterCompletion struct {
	Policy, ResourceHash string
	DungeonId            uint32
	Maze, Tier           byte
	Total                uint32
	Items                []SpecialCardItem
}

func counterItem(item CardItem) (cardCounterItem, error) {
	var row cardCounterItem
	if frozen := item.frozen; frozen != nil {
		row = cardCounterItem{Template: frozen.Template, Attributes: frozen.Attributes, Raw119: frozen.Raw119, Detail: frozen.Detail}
		if item.Reward.StackCount != 0 {
			row.StackCount = frozen.Count
		}
		return row, nil
	}
	grants, err := (GroundDrop{Reward: item.Reward}).Grant(storage.Inventory{})
	if err != nil {
		return row, err
	}
	if len(grants) != 1 || json.Unmarshal(grants[0].Attributes, &row.Attributes) != nil {
		return row, storage.ErrInvalid
	}
	row.Template, row.Raw119, row.Detail = item.Reward.Template, make([]uint16, 119), make([]uint16, len(grants[0].Detail))
	for n, value := range grants[0].Core {
		row.Raw119[n] = uint16(value)
	}
	for n, value := range grants[0].Detail {
		row.Detail[n] = uint16(value)
	}
	if grants[0].Kind == 2 {
		row.StackCount = grants[0].Count
	}
	return row, nil
}

func (r *CardReward) CounterReward() (*CardCounterReward, error) {
	if r == nil {
		return nil, storage.ErrInvalid
	}
	out := &CardCounterReward{Policy: r.Policy, ResourceHash: r.ResourceHash, Gold: r.Gold, Luck: r.luckSnapshot}
	if artifact := r.Artifact(); artifact != nil {
		out.Artifact = &CardCounterArtifact{Template: artifact.Template, QualitySeed: artifact.QualitySeed}
		return out, nil
	}
	for _, item := range r.Items {
		row, err := counterItem(item)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, row)
	}
	return out, nil
}

func (r *Run) CounterCompletion(card *CardReward) (*CardCounterCompletion, error) {
	if card == nil {
		return nil, nil
	}
	d := r.Definition.Dungeon
	if frozen := card.completionSnapshot; frozen != nil {
		if frozen.DungeonId != d.ID || frozen.Maze != d.MazeIndex || frozen.Tier != d.Difficulty {
			return nil, storage.ErrConflict
		}
		return frozen, nil
	}
	out := &CardCounterCompletion{
		Policy: completionPlanPolicy, ResourceHash: card.ResourceHash,
		DungeonId: d.ID, Maze: d.MazeIndex, Tier: d.Difficulty,
	}
	for _, item := range card.Items {
		row, err := counterItem(item)
		if err != nil {
			return nil, err
		}
		count := max(1, row.StackCount)
		if math.MaxUint32-out.Total < count {
			return nil, storage.ErrInvalid
		}
		out.Total += count
		out.Items = append(out.Items, SpecialCardItem{Template: row.Template, Count: count,
			Attributes: row.Attributes, Raw119: row.Raw119, Detail: row.Detail})
	}
	return out, nil
}

func (p SpecialCardData) PlanAt(r *Run, job, grow byte, day int64, used, limit byte, now time.Time) (*SpecialCardPlan, error) {
	d := r.Definition.Dungeon
	if r.Settlement.Confirmation == nil || !r.Ready || !r.Cleared || d.DisableClearReward || !d.UsesOrdinaryRewards {
		return nil, storage.ErrInvalid
	}
	if p.Definitions[d.MazeIndex] != d.ResourceHash || !isHash(p.Hash) || p.Limit == 0 || limit < p.Limit || limit > 32 ||
		used > limit || job > 15 || day < 20000101 || len(p.Rows[d.Difficulty]) == 0 {
		return nil, storage.ErrInvalid
	}
	result := &SpecialCardPlan{Policy: p.Policy, ResourceHash: p.Hash, Day: day, Limit: limit, Items: []SpecialCardItem{}}
	if d.ID != 100000002 {
		result.DungeonId = d.ID
	}
	if used == limit {
		return result, nil
	}
	draw := newSpecialCardDraw(fmt.Appendf(nil, "%s\n%d\n%s\n%d\n%d\n%d", r.idKey(), r.Seed, p.Hash, job, grow, day))
	choose := func(rows []specialCardChoice) (specialCardChoice, error) { return chooseSpecialCard(rows, draw) }
	add := func(item, count uint32) error {
		template := p.Templates[item]
		var t dropTemplate
		if json.Unmarshal(template, &t) != nil || t.TemplateID != item || count == 0 || count > 1000000 ||
			t.Kind > 2 || t.FateCollection || t.AccountCommonInventory || t.Kind < 2 && count != 1 {
			return storage.ErrInvalid
		}
		reward := GroundReward{Template: template, StackCount: count, Durability: t.MaximumDurability, ExpiresAt: dropExpiration(t, now)}
		if reward.ExpiresAt != nil && *reward.ExpiresAt <= now.Unix() {
			return storage.ErrInvalid
		}
		if t.Kind < 2 {
			value, err := draw(999999998)
			if err != nil {
				return err
			}
			reward.QualitySeed = value + 1
		}
		row, err := counterItem(CardItem{Reward: reward})
		if err != nil {
			return err
		}
		result.Items = append(result.Items, SpecialCardItem{Template: row.Template, Count: count,
			Attributes: row.Attributes, Raw119: row.Raw119, Detail: row.Detail})
		return nil
	}
	container, err := choose(p.Rows[d.Difficulty])
	if err != nil {
		return nil, err
	}
	row, ok := p.Containers[container.Id]
	if !ok || len(row.Groups) == 0 || len(row.Groups) > 15 {
		return nil, storage.ErrInvalid
	}
	result.Container, result.Ordinal = container.Id, used+1
	for _, group := range row.Groups {
		item, err := choose(group)
		if err != nil {
			return nil, err
		}
		if err := add(item.Id, item.Count); err != nil {
			return nil, err
		}
	}
	if row.Smart {
		pool := p.SmartPools[uint16(job)*16+uint16(grow&15)]
		if len(pool) == 0 {
			return nil, storage.ErrInvalid
		}
		index, err := draw(uint32(len(pool)))
		if err != nil {
			return nil, err
		}
		if err := add(pool[index], 1); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func newSpecialCardDraw(seedInput []byte) func(uint32) (uint32, error) {
	seed := sha256.Sum256(seedInput)
	var input [36]byte
	copy(input[:], seed[:])
	var counter uint32
	return func(upper uint32) (uint32, error) {
		if upper == 0 {
			return 0, storage.ErrInvalid
		}
		threshold := -upper % upper
		for range 16 {
			le.PutUint32(input[32:], counter)
			counter++
			h := sha256.Sum256(input[:])
			if sample := le.Uint32(h[:]); sample >= threshold {
				return sample % upper, nil
			}
		}
		return 0, storage.ErrInvalid
	}
}

func chooseSpecialCard(rows []specialCardChoice, draw func(uint32) (uint32, error)) (specialCardChoice, error) {
	var total uint64
	for _, row := range rows {
		total += uint64(row.Weight)
	}
	if total == 0 || total > math.MaxUint32 {
		return specialCardChoice{}, storage.ErrInvalid
	}
	value, err := draw(uint32(total))
	if err != nil {
		return specialCardChoice{}, err
	}
	for _, row := range rows {
		if value < row.Weight {
			return row, nil
		}
		value -= row.Weight
	}
	return specialCardChoice{}, storage.ErrInvalid
}

func (p *SpecialCardPlan) Grants(in storage.Inventory) ([]storage.QuestGrant, error) {
	var grants []storage.QuestGrant
	if p == nil {
		return grants, nil
	}
	for _, item := range p.Items {
		card, err := cardItemFromReceipt(item, nil)
		if err != nil {
			return nil, err
		}
		rows, err := grantFrozenCardItem(in, card)
		if err != nil {
			return nil, err
		}
		grants = append(grants, rows...)
	}
	return grants, nil
}

func (p *SpecialCardPlan) itemViews() [][2]uint32 {
	if p == nil {
		return nil
	}
	var out [][2]uint32
	for _, item := range p.Items {
		var template struct{ TemplateId uint32 }
		if json.Unmarshal(item.Template, &template) == nil {
			out = append(out, [2]uint32{template.TemplateId, item.Count})
		}
	}
	return out
}
