package dungeon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"s9a1/server/internal/storage"
)

type cardProbability struct {
	Minimum, Maximum          byte
	Probability, SpecialGroup uint32
}
type specificCard struct {
	Dungeons  map[uint32]struct{ Rows []struct{ Maximum, Item int } }
	Templates map[uint32]json.RawMessage
	Luck      map[uint32]cardLuckItem
}
type groupCard struct {
	Dungeons map[uint32]struct{ Type *uint32 }
	Groups   map[uint32]struct {
		DungeonTypes []uint32
		Items        []struct{ Item, Weight, Quantity uint32 }
	}
	Templates map[uint32]json.RawMessage
	Luck      map[uint32]cardLuckItem
}
type paidCardData struct {
	Hash, KindHash               string
	Costs                        map[byte]uint32
	Profiles                     map[string][]cardProbability
	DropKinds, ItemTypes, Rarity []uint32
	DifficultyBonuses            []json.Number
	CreateRateParts              uint32
	Pools                        []struct {
		Type, Level, Rarity byte
		Items               []uint32
	}
	Equipment  map[uint32]dropCandidate
	Specific   *specificCard
	Groups     *groupCard
	Generation *equipmentGeneration
}
type cardLuckItem struct {
	Item   uint32
	Grade  int
	Rarity byte
}
type CardItem struct {
	Reward   GroundReward
	Metadata cardLuckItem
	frozen   *SpecialCardItem
}

func (c specificCard) selectItem(id uint32, roll uint64) dropCandidate {
	for _, row := range c.Dungeons[id].Rows {
		if row.Maximum < int(roll)+1 {
			continue
		}
		i := uint32(row.Item)
		m := c.Luck[i]
		return dropCandidate{Id: i, Template: c.Templates[i], Grade: m.Grade, Rarity: m.Rarity, Equipment: m.Grade > 0}
	}
	return dropCandidate{}
}

func pickCardType(weights []uint32, sample uint64) byte {
	left := sample % 10000
	for i, weight := range weights {
		if left < uint64(weight) {
			return byte(i + 1)
		}
		left -= uint64(weight)
	}
	return 1
}

func matchesCardType(c dropCandidate, kind byte) bool {
	switch kind {
	case 1:
		return !c.Equipment
	case 2:
		return c.Equipment
	case 3:
		return !c.Equipment && c.Recipe
	case 4:
		return c.Equipment && c.Creature
	default:
		return false
	}
}

func setCardRarity(rarity []uint32, kind byte, sample uint64, luck *LuckRoll) int {
	roll := int64(sample % 1000000)
	for i, value := range rarity {
		threshold := int64(value)
		if kind == 2 && len(rarity) >= 8 && i < 8 {
			threshold = luck.threshold(rarity, 0, byte(i), 2)
		}
		if roll < threshold {
			return i
		}
	}
	return len(rarity)
}

func cardSampleInput(r *Run, weight uint32, level byte, hash string) ([57]byte, error) {
	var input [57]byte
	id, err := guidWire(r.ID)
	if err != nil || !isHash(hash) {
		return input, errors.New("invalid card reward random source")
	}
	copy(input[:], id)
	le.PutUint32(input[16:], r.Seed)
	input[20] = level
	le.PutUint32(input[21:], weight)
	_, _ = hex.Decode(input[25:], []byte(hash))
	return input, nil
}

func cardSample(input [57]byte, attempt uint32) uint64 {
	var buf [61]byte
	copy(buf[:], input[:])
	le.PutUint32(buf[57:], attempt)
	hash := sha256.Sum256(buf[:])
	return le.Uint64(hash[:])
}

func paidDraw(r *Run, weight uint32, level byte, hash string) (stableDraw, error) {
	in, err := cardSampleInput(r, weight, level, hash)
	if err != nil {
		return stableDraw{}, err
	}
	s := sha256.Sum256(in[:])
	var d stableDraw
	copy(d.state[:], s[:])
	return d, nil
}

func cardBoundDurabilityHash(hash string, generation *equipmentGeneration) string {
	digest := sha256.Sum256([]byte(hash + "\ninitial-durability-callers-reference/v1\n" + generation.InitialDurabilityHash))
	return strings.ToUpper(hex.EncodeToString(digest[:]))
}

func cardItem(c dropCandidate, quantity uint32, quality uint32, generation *equipmentGeneration, durabilityDraw func() uint64) (CardItem, bool, error) {
	if len(c.Artifact) > 0 && string(c.Artifact) != "null" {
		var t dropTemplate
		if err := json.Unmarshal(c.Artifact, &t); err != nil {
			return CardItem{}, false, err
		}
		if t.Kind != 2 || t.TemplateID != c.Id || t.WornSlot < 27 || t.WornSlot > 29 || quantity != 1 || quality == 0 {
			return CardItem{}, false, storage.ErrInvalid
		}
		return CardItem{Reward: GroundReward{Artifact: c.Artifact, QualitySeed: quality, Durability: t.MaximumDurability},
			Metadata: cardLuckItem{Item: c.Id}}, true, nil
	}
	if len(c.Template) == 0 || string(c.Template) == "null" {
		return CardItem{}, false, nil
	}
	var t dropTemplate
	if err := json.Unmarshal(c.Template, &t); err != nil {
		return CardItem{}, false, err
	}
	if t.Kind > 2 || t.TemplateID != c.Id || quantity == 0 || t.Kind != 2 && quantity != 1 {
		return CardItem{}, false, storage.ErrInvalid
	}
	w := GroundReward{Template: c.Template}
	if t.Kind == 2 {
		w.StackCount = quantity
	} else {
		w.QualitySeed, w.Durability = quality, t.MaximumDurability
		if generation != nil && generation.DamagedEquipmentRatio != nil && *generation.DamagedEquipmentRatio < 100 && durabilityDraw != nil {
			budget := int(float32(w.Durability) * (float32(100-*generation.DamagedEquipmentRatio) / 100))
			next := int(float32(w.Durability) + float32(budget)*(float32(durabilityDraw())/-100))
			if next < 0 {
				next = 1
			}
			w.Durability = uint16(min(next, int(w.Durability)))
		}
	}
	return CardItem{Reward: w, Metadata: cardLuckItem{c.Id, c.Grade, c.Rarity}}, true, nil
}

func clearChance(base, served uint64, bonus json.Number) (uint64, error) {
	b, err := scaledFloor(1, 10000, bonus)
	if err != nil {
		return 0, err
	}
	return min(10000, base+b+served), nil
}

func (c *CardData) freeItem(r *Run, weight uint32, level byte, hash string, luck *LuckRoll) ([]CardItem, error) {
	if len(c.Probabilities) == 0 || len(c.DifficultyBonuses) <= int(r.Definition.Dungeon.Difficulty) {
		return nil, nil
	}
	identity, err := cardSampleInput(r, weight, level, hash)
	if err != nil {
		return nil, err
	}
	var selected dropCandidate
	qualitySample := uint32(3)
	if len(c.DropKinds) > 0 && cardSample(identity, 7)%10000 >= uint64(c.DropKinds[0]) {
		if c.Specific == nil {
			return nil, nil
		}
		selected = c.Specific.selectItem(r.Definition.Dungeon.ID, cardSample(identity, 8)%100)
		qualitySample = 9
	} else {
		prob := cardProbability{}
		for _, p := range c.Probabilities {
			if level >= p.Minimum && level <= p.Maximum {
				prob = p
				break
			}
		}
		if prob.Maximum == 0 || len(r.rooms) == 0 {
			return nil, nil
		}
		rate := c.MapRates[byte(min(math.MaxUint8, len(r.rooms)))]
		if rate == 0 {
			rate = 10000
		}
		base := uint64(prob.Probability) * uint64(rate) * uint64(min(len(r.visited), len(r.rooms))) / (10000 * uint64(len(r.rooms)))
		chance, err := clearChance(base, 0, c.DifficultyBonuses[r.Definition.Dungeon.Difficulty])
		if err != nil || cardSample(identity, 0)%10000 >= chance {
			return nil, err
		}
		kind := pickCardType(c.ItemTypes, cardSample(identity, 5))
		rarity := setCardRarity(c.Rarity, kind, cardSample(identity, 1), luck)
		w, ok := c.Windows[level]
		if !ok || rarity >= len(c.Rarity) {
			return nil, nil
		}
		minimum, maximum := max(1, int(level)-int(w.Down)), min(201, int(level)+int(w.Up))
		var pool []dropCandidate
		var total uint64
		for _, candidate := range c.Equipment {
			if matchesCardType(candidate, kind) && int(candidate.Rarity) == rarity && candidate.Grade >= minimum && candidate.Grade < maximum {
				pool = append(pool, candidate)
				total += uint64(candidate.CreationWeight)
			}
		}
		if total == 0 {
			return nil, nil
		}
		draw := cardSample(identity, 2) % total
		for _, candidate := range pool {
			if draw < uint64(candidate.CreationWeight) {
				selected = candidate
				break
			}
			draw -= uint64(candidate.CreationWeight)
		}
	}
	var durability func() uint64
	if gen := c.Generation; gen != nil {
		in, err := cardSampleInput(r, weight, level, cardBoundDurabilityHash(hash, gen))
		if err != nil {
			return nil, err
		}
		durability = func() uint64 { return cardSample(in, 6) % 100 }
	}
	row, ok, err := cardItem(selected, 1, uint32(cardSample(identity, qualitySample)%999999998)+1, c.Generation, durability)
	if err != nil || !ok {
		return nil, err
	}
	return []CardItem{row}, nil
}

func (p *paidCardData) plan(r *Run, weight uint32, level byte, luck *LuckRoll) (*CardReward, error) {
	if p == nil || p.Hash == "" {
		return nil, nil
	}
	price, ok := p.Costs[level]
	if !ok || int(r.Definition.Dungeon.Difficulty) >= len(p.DifficultyBonuses) {
		return nil, storage.ErrInvalid
	}
	base := &CardReward{Policy: "LocalNormalPaid/v1", ResourceHash: p.Hash}
	miss := func(reason string) (*CardReward, error) { base.UnavailableReason = reason; return base, nil }
	random, err := paidDraw(r, weight, level, p.Hash)
	if err != nil {
		return nil, err
	}
	var candidate dropCandidate
	quantity := uint32(1)
	if len(p.DropKinds) > 0 {
		kind, err := paidDraw(r, weight, level, p.KindHash)
		if err != nil {
			return nil, err
		}
		if kind.next(10000) >= uint64(p.DropKinds[0]) {
			if p.Specific == nil {
				return miss("paid-card-specific-unavailable")
			}
			candidate = p.Specific.selectItem(r.Definition.Dungeon.ID, kind.next(100))
			if candidate.Template == nil {
				return miss("paid-card-specific-unavailable")
			}
			random = kind
			base.Policy = "PVFSpecificPaidCard/v1"
		}
	}
	if candidate.Template == nil {
		profile := cardProbability{}
		for _, row := range p.Profiles["event"] {
			if level >= row.Minimum && level <= row.Maximum {
				profile = row
				break
			}
		}
		if profile.Maximum == 0 {
			return miss("paid-card-profile-unavailable:event")
		}
		prob := uint64(profile.Probability) * uint64(p.CreateRateParts) / 10000
		chance, err := clearChance(prob, 0, p.DifficultyBonuses[r.Definition.Dungeon.Difficulty])
		if err != nil {
			return nil, err
		}
		if random.next(100000000) >= chance*10000 {
			return miss("paid-card-not-created")
		}
		if profile.SpecialGroup != 0 {
			if p.Groups == nil {
				return miss("paid-card-group-source-unavailable")
			}
			rule, ok := p.Groups.Groups[profile.SpecialGroup]
			if !ok {
				return miss("paid-card-group-absent")
			}
			def := p.Groups.Dungeons[r.Definition.Dungeon.ID]
			if def.Type == nil || !slices.Contains(rule.DungeonTypes, *def.Type) {
				return miss("paid-card-group-dungeon-unavailable")
			}
			var total uint64
			for _, row := range rule.Items {
				total += uint64(row.Weight)
			}
			if total == 0 {
				return miss("paid-card-group-empty")
			}
			draw := random.next(total)
			for _, row := range rule.Items {
				if draw < uint64(row.Weight) {
					meta := p.Groups.Luck[row.Item]
					candidate = dropCandidate{Id: row.Item, Template: p.Groups.Templates[row.Item], Grade: meta.Grade, Rarity: meta.Rarity, Equipment: meta.Grade > 0}
					quantity = row.Quantity
					break
				}
				draw -= uint64(row.Weight)
			}
			if candidate.Template == nil || quantity > 1000000 {
				return miss("paid-card-group-item-unavailable")
			}
			base.Policy = "PVFGroupPaidCard/v1"
		} else {
			kind := pickCardType(p.ItemTypes, random.next(10000))
			rarity := setCardRarity(p.Rarity, kind, random.next(1000000), luck)
			var pool []uint32
			for _, set := range p.Pools {
				if set.Type == kind && set.Level == level && int(set.Rarity) == rarity {
					pool = set.Items
					break
				}
			}
			var total uint64
			for _, id := range pool {
				total += uint64(p.Equipment[id].CreationWeight)
			}
			if total == 0 {
				return miss("paid-card-empty-rarity-pool")
			}
			draw := random.next(total)
			for _, id := range pool {
				candidate = p.Equipment[id]
				if draw < uint64(candidate.CreationWeight) {
					break
				}
				draw -= uint64(candidate.CreationWeight)
			}
		}
	}
	var durability func() uint64
	if p.Generation != nil {
		d, err := paidDraw(r, weight, level, cardBoundDurabilityHash(p.Hash, p.Generation))
		if err != nil {
			return nil, err
		}
		durability = func() uint64 { return d.next(100) }
	}
	row, ok, err := cardItem(candidate, quantity, uint32(random.next(999999998))+1, p.Generation, durability)
	if err != nil {
		return nil, err
	}
	if !ok {
		return miss(fmt.Sprintf("paid-card-unsupported-instance:%d", candidate.Id))
	}
	base.Cost = price
	base.Items = []CardItem{row}
	if row.Reward.Artifact != nil {
		base.Policy = "PVFNormalPaidArtifactCard/v1"
	}
	return base, nil
}

func (r *CardReward) Grants(in storage.Inventory) ([]storage.QuestGrant, error) {
	var grants []storage.QuestGrant
	if r == nil {
		return grants, nil
	}
	for _, i := range r.Items {
		rows, err := grantFrozenCardItem(in, i)
		if err != nil {
			return nil, err
		}
		grants = append(grants, rows...)
	}
	return grants, nil
}

func (r *CardReward) ApplyLuck(p LuckPolicy, c storage.Character) (storage.Character, *LuckRoll, error) {
	if r == nil || r.luckSnapshot == nil {
		return c, nil, nil
	}
	luck, err := p.Start(c)
	if err != nil {
		return c, nil, err
	}
	if r.luckSnapshot.Hash != p.Hash {
		return c, nil, storage.ErrConflict
	}
	luck.Version = cardLuckVersion
	for _, m := range r.luckSnapshot.Items {
		luck.deduct(dropCandidate{Id: m.Item, Grade: m.Grade, Rarity: m.Rarity, Equipment: m.Grade > 0})
	}
	next, err := luck.Apply(c)
	return next, luck, err
}

func (r *CardReward) Artifact() *storage.DungeonArtifact {
	if r == nil || len(r.Items) != 1 {
		return nil
	}
	item := r.Items[0].Reward
	if item.Artifact == nil {
		return nil
	}
	return &storage.DungeonArtifact{Template: item.Artifact, QualitySeed: item.QualitySeed, Durability: item.Durability}
}

func (r *CardReward) itemViews() [][2]uint32 {
	if r == nil {
		return nil
	}
	rows := make([][2]uint32, 0, len(r.Items))
	for _, item := range r.Items {
		rows = append(rows, [2]uint32{item.Metadata.Item, max(1, item.Reward.StackCount)})
	}
	return rows
}
