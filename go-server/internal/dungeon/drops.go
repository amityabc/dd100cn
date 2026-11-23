package dungeon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strings"
	"time"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/storage"
)

type dropRow struct{ Item, Probability uint32 }
type monsterDrops struct {
	Ordinary, Champion, SuperChampion, Boss []dropRow
	Restrictions                            byte
	Named                                   *bool
}
type probabilityRow struct {
	MinimumLevel, MaximumLevel byte
	Per10000                   []uint32
}
type dropRate struct {
	Probabilities                             []probabilityRow
	DifficultyBonus, PartyBonus, MonsterBonus []json.Number
	RarityThresholds                          []uint32
}
type dropPool struct {
	Kind, Level, Rarity, Channel byte
	Ids                          []uint32
}
type dropCandidate struct {
	Id, CreationWeight          uint32
	Grade                       int
	Rarity                      byte
	Equipment, CanRandomUpgrade bool
	Recipe, Creature            bool
	Template, Artifact          json.RawMessage
}
type equipmentGeneration struct {
	Hash, InitialDurabilityHash string
	DamagedEquipmentRatio       *byte
	Rarities                    map[byte][]struct {
		Level  byte
		Weight uint64
	}
	Rates []struct {
		Gap    int
		Factor float32
	}
}

type EquipmentGeneration = equipmentGeneration
type DropData struct {
	Hash, MonsterHash string
	Native            struct {
		dropRate
		Auxiliary struct {
			ConditionRate, GoldQuantity, GoldVolume []int64
		}
	}
	Gold                      map[byte]struct{ Amount, Variance int64 }
	Monsters                  map[uint32]monsterDrops
	Templates                 map[uint32]json.RawMessage
	Weights                   map[uint32]int64
	Candidates                map[uint32]dropCandidate
	MainPools, SecondaryPools []dropPool
	Secondary                 []struct {
		Kind byte
		Hash string
		dropRate
	}
	EquipmentGeneration           *equipmentGeneration
	FirstHunt                     *FirstHuntPolicy
	Independent, IndependentCards *independentDropData
	PvfRewards                    *pvfRewardData
	mainPools, secondaryPools     map[[4]byte][]uint32
}

type dropTemplate struct {
	PvfGiftItem                                                                                                                       bool
	MythologyHash                                                                                                                     string
	MythologyOptions                                                                                                                  byte
	TemplateID                                                                                                                        uint32
	Kind                                                                                                                              byte
	FirstBagSlot, LastBagSlot, WornSlot, MaximumDurability, UsableJobs                                                                uint16
	MinimumLevel                                                                                                                      byte
	ResourceHash                                                                                                                      string
	StackLimit                                                                                                                        uint32
	WornSlotIsNative, InitialSealed, PetConsumable, AccountCommonInventory, FateCollection, DailyDeletion, FatigueTimeMonthlyDeletion bool
	ResourceExpiration                                                                                                                *int64
	RelativeLifetimeSeconds                                                                                                           uint32
	WeeklyDeletionDay                                                                                                                 *byte
	WeaponQualification                                                                                                               json.RawMessage
}

type GroundReward struct {
	Gold               uint32
	Template           json.RawMessage
	QualitySeed        uint32
	Durability         uint16
	Artifact           json.RawMessage `json:",omitempty"`
	Reinforcement      byte            `json:",omitempty"`
	AmplificationType  byte            `json:",omitempty"`
	AmplificationValue uint16          `json:",omitempty"`
	Mythology          *MythologyState `json:",omitempty"`
	ExpiresAt          *int64          `json:",omitempty"`
	StackCount         uint32          `json:",omitempty"`
	PvfDungeonReward   bool            `json:",omitempty"`
}

type GroundDrop struct {
	ObjectKey                   uint32
	RoomX, RoomY                byte
	MonsterActor, OwnerActorKey uint16
	DeathKey                    string
	Reward                      GroundReward
	SceneSlot                   uint16
}

type groundKey struct {
	cell
	object uint32
}

type stableDraw struct {
	state   [36]byte
	counter uint32
}

func newDraw(run *Run, actor uint16, m opening.Monster, hash string) (stableDraw, error) {
	var input [60]byte
	id, err := guidWire(run.ID)
	if err != nil || !isHash(hash) {
		return stableDraw{}, errors.New("malformed frozen drop identities")
	}
	le.PutUint32(input[:], run.Seed)
	copy(input[4:20], id)
	le.PutUint16(input[20:], actor)
	le.PutUint32(input[22:], m.Code)
	input[26], input[27] = m.Level, m.Type
	_, _ = hex.Decode(input[28:], []byte(hash))
	h := sha256.Sum256(input[:])
	var r stableDraw
	copy(r.state[:], h[:])
	return r, nil
}

func guidWire(id string) ([]byte, error) {
	b, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil || len(b) != 16 {
		return nil, errors.New("invalid dungeon run GUID")
	}
	b[0], b[1], b[2], b[3] = b[3], b[2], b[1], b[0]
	b[4], b[5], b[6], b[7] = b[5], b[4], b[7], b[6]
	return b, nil
}

func (r *stableDraw) next(bound uint64) uint64 {
	threshold := -bound % bound
	for {
		le.PutUint32(r.state[32:], r.counter)
		r.counter++
		h := sha256.Sum256(r.state[:])
		if v := le.Uint64(h[:]); v >= threshold {
			return v % bound
		}
	}
}

func scaledFloor(n uint64, cap uint64, factors ...json.Number) (uint64, error) {
	p := new(big.Rat).SetUint64(n)
	for _, factor := range factors {
		f, ok := new(big.Rat).SetString(string(factor))
		if !ok || f.Sign() < 0 {
			return 0, errors.New("invalid frozen decimal multiplier")
		}
		p.Mul(p, f)
	}
	q := new(big.Int).Quo(p.Num(), p.Denom())
	if !q.IsUint64() {
		return cap, nil
	}
	return min(cap, q.Uint64()), nil
}

func (table *DropData) rollGold(run *Run, d Death, random *stableDraw, basis uint32) ([]GroundReward, error) {
	mon := d.Monster
	rules := &table.Native
	gold, ok := table.Gold[mon.Level]
	if !ok || gold.Amount <= 0 || gold.Variance < 0 || gold.Variance > 99 ||
		int(run.Definition.Dungeon.Difficulty) >= len(rules.DifficultyBonus) || len(rules.PartyBonus) < 1 ||
		int(mon.Type) >= len(rules.MonsterBonus) {
		return nil, errors.New("missing ordinary gold probability or basis")
	}
	chance, err := scaledFloor(uint64(basis), 10000, rules.DifficultyBonus[run.Definition.Dungeon.Difficulty], rules.MonsterBonus[mon.Type], rules.PartyBonus[0])
	if err != nil || random.next(10000) >= chance {
		return nil, err
	}
	percent := int64(random.next(uint64(gold.Variance*2+1))) - gold.Variance
	amount := max(int64(1), gold.Amount+gold.Amount*percent/100)
	if mon.Type != 3 || len(rules.Auxiliary.ConditionRate) == 0 {
		return []GroundReward{{Gold: uint32(amount)}}, nil
	}
	a := rules.Auxiliary
	if len(a.ConditionRate) != 8 || len(a.GoldQuantity) != 4 || len(a.GoldVolume) != 2 {
		return nil, errors.New("missing boss gold dimensions")
	}
	tier := 0
	if random.next(uint64(a.ConditionRate[0]+a.ConditionRate[1])+1) < uint64(a.ConditionRate[1]) {
		tier = 1
	}
	low, high := a.GoldQuantity[tier*2], a.GoldQuantity[tier*2+1]
	if low < 0 || high < low || high > 16 || a.GoldVolume[tier] < 0 {
		return nil, errors.New("invalid boss gold quantity")
	}
	count := low + int64(random.next(uint64(high-low+1)))
	value := max(int64(1), amount*a.GoldVolume[tier]/100)
	if value > math.MaxUint32 {
		return nil, errors.New("boss gold amount overflow")
	}
	out := make([]GroundReward, count)
	for i := range out {
		out[i].Gold = uint32(value)
	}
	return out, nil
}

func (table *DropData) rollMonster(run *Run, d Death, p monsterDrops, now time.Time) ([]GroundReward, error) {
	if len(p.Ordinary)+len(p.Champion)+len(p.SuperChampion)+len(p.Boss) == 0 {
		return nil, nil
	}
	var bonus []dropRow
	switch d.Monster.Type {
	case 1:
		bonus = p.Champion
	case 2:
		bonus = p.SuperChampion
	case 3:
		bonus = p.Boss
	}
	random, err := newDraw(run, d.Actor, d.Monster, table.MonsterHash)
	if err != nil {
		return nil, err
	}
	var out []GroundReward
	for _, rows := range [][]dropRow{p.Ordinary, bonus} {
		for _, row := range rows {
			if random.next(10000) >= uint64(row.Probability) || len(table.Templates[row.Item]) == 0 {
				continue
			}
			var t dropTemplate
			raw := table.Templates[row.Item]
			if err := json.Unmarshal(raw, &t); err != nil {
				return nil, err
			}
			deadline := dropExpiration(t, now)
			reward := GroundReward{Template: raw, ExpiresAt: deadline}
			if t.Kind == 2 {
				reward.StackCount = 1
			} else {
				reward.QualitySeed, reward.Durability = uint32(random.next(999999998))+1, t.MaximumDurability
			}
			if deadline != nil && *deadline <= now.Unix() {
				continue
			}
			out = append(out, reward)
		}
	}
	return out, nil
}

func dropExpiration(t dropTemplate, now time.Time) *int64 {
	deadline := t.ResourceExpiration
	limit := func(next int64) {
		if deadline == nil || next < *deadline {
			deadline = &next
		}
	}
	if t.RelativeLifetimeSeconds > 0 {
		limit(now.Unix() + int64(t.RelativeLifetimeSeconds))
	}
	local := now.In(time.FixedZone("UTC+8", 28800))
	reset := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, local.Location())
	if t.DailyDeletion {
		if !reset.After(local) {
			reset = reset.AddDate(0, 0, 1)
		}
		limit(reset.Unix())
	} else if t.WeeklyDeletionDay != nil {
		reset = reset.AddDate(0, 0, (7+int(*t.WeeklyDeletionDay)-int(reset.Weekday()))%7)
		if !reset.After(local) {
			reset = reset.AddDate(0, 0, 7)
		}
		limit(reset.Unix())
	}
	if t.FatigueTimeMonthlyDeletion {
		reset = time.Date(local.Year(), local.Month(), 1, 6, 0, 0, 0, local.Location())
		if !reset.After(local) {
			reset = reset.AddDate(0, 1, 0)
		}
		limit(reset.Unix())
	}
	return deadline
}

func (table *DropData) MonsterAllowsItems(code uint32) bool {
	return table.Monsters[code].Restrictions&4 == 0
}

func (table *DropData) SupportsApcIndependent(run *Run, d Death) bool {
	m, def := d.Monster, run.Definition.Dungeon
	return !def.DisableDrops && def.UsesOrdinaryMonsterDrops && (def.UsesOrdinaryRewards || def.Difficulty != 0) &&
		!def.TowerOfGrief && def.Tournament == nil && !d.Removal && m.AiScriptIndex != nil && *m.AiScriptIndex < 64 &&
		m.BlocksRoom && !m.Dummy && m.HellPhase == 0 && (m.Type == 0 || m.Type == 3) && len(table.Independent.source(m.Code, true)) != 0
}

func (table *DropData) Plan(run *Run, d Death, now time.Time, luck *LuckRoll, first *FirstHunt) ([]GroundDrop, error) {
	def, m := run.Definition.Dungeon, d.Monster
	if def.DisableDrops || !def.UsesOrdinaryMonsterDrops || !def.UsesOrdinaryRewards && def.Difficulty == 0 ||
		d.Removal || m.HellPhase == 8 || table.Hash == "" {
		return []GroundDrop{}, nil
	}
	p := table.Monsters[m.Code]
	multiplier := uint32(1)
	if first != nil {
		multiplier = first.Multiplier
	}
	var rewards []GroundReward
	if m.AiScriptIndex != nil {
		if table.SupportsApcIndependent(run, d) {
			items, err := table.Independent.roll(run, d, now, table.EquipmentGeneration, false)
			if err != nil {
				return nil, err
			}
			rewards = items
		}
	} else {
		if m.BlocksRoom && !m.Dummy && m.HellPhase == 0 {
			items, err := table.rollRegular(run, d, p.Restrictions, now, luck, multiplier)
			if err != nil {
				return nil, err
			}
			rewards = items
			if p.Restrictions&4 == 0 {
				items, err = table.rollMonster(run, d, p, now)
				if err != nil {
					return nil, err
				}
				rewards = append(rewards, items...)
			}
		}
		if p.Restrictions&4 == 0 {
			if w := run.Definition.WisdomReward; w != nil && m.HellPhase == 3 {
				items, err := w.phaseDrops(def.Difficulty, m.HellPhase, now)
				if err != nil {
					return nil, err
				}
				rewards = append(rewards, items...)
			}
			cards, err := table.IndependentCards.roll(run, d, now, nil, true)
			if err != nil {
				return nil, err
			}
			items, err := table.Independent.roll(run, d, now, table.EquipmentGeneration, false)
			if err != nil {
				return nil, err
			}
			rewards = append(rewards, cards...)
			rewards = append(rewards, items...)
			items, err = table.PvfRewards.ground(run, d, now)
			if err != nil {
				return nil, err
			}
			rewards = append(rewards, items...)
		}
	}
	return run.freezeDrops(d.Actor, d.Key, rewards)
}

func (run *Run) freezeDrops(actor uint16, key string, rewards []GroundReward) ([]GroundDrop, error) {
	firstSlot := run.nextGroundSlot
	if len(rewards) > 16 || int(firstSlot)+len(rewards) > math.MaxUint16 {
		return nil, errors.New("ground object slots exceed native width")
	}
	out := make([]GroundDrop, 0, len(rewards))
	for i, reward := range rewards {
		out = append(out, GroundDrop{uint32(actor)<<16 | uint32(i+1), run.Room.X, run.Room.Y,
			actor, run.Character, key, reward, firstSlot + uint16(i)})
	}
	return out, nil
}

func (d GroundDrop) Grant(in storage.Inventory) ([]storage.QuestGrant, error) {
	if d.Reward.Gold > 0 || d.Reward.Artifact != nil {
		return nil, nil
	}
	var t dropTemplate
	if err := json.Unmarshal(d.Reward.Template, &t); err != nil {
		return nil, err
	}
	if t.Kind > 2 || t.TemplateID == 0 || t.FirstBagSlot > t.LastBagSlot || t.StackLimit == 0 || t.AccountCommonInventory ||
		t.PetConsumable || t.FateCollection {
		return nil, storage.ErrInvalid
	}
	attr := struct {
		QualitySeed, Enchantment                                              uint32
		Durability, AmplificationValue                                        uint16
		Reinforcement, Forge, AmplificationType, Binding, EnchantUpgradeCount byte
		ExpiresAt                                                             *int64
		Locked                                                                bool
		Mythology                                                             *MythologyState `json:",omitempty"`
		InitialAmplification                                                  bool            `json:",omitempty"`
	}{QualitySeed: d.Reward.QualitySeed, Durability: d.Reward.Durability, Reinforcement: d.Reward.Reinforcement,
		AmplificationType: d.Reward.AmplificationType, AmplificationValue: d.Reward.AmplificationValue, ExpiresAt: d.Reward.ExpiresAt}
	core := make([]byte, 119)
	if myth := d.Reward.Mythology; myth != nil {
		if t.MythologyHash != myth.SourceHash || t.MythologyOptions != myth.Count || !myth.Valid || myth.Count != 3 && myth.Count != 4 {
			return nil, storage.ErrInvalid
		}
		attr.Mythology = myth
		copy(core[114:], []byte{myth.First, myth.Second, myth.Third, myth.Fourth, myth.Fifth})
	}
	core[10] = attr.Reinforcement
	attr.InitialAmplification = attr.AmplificationType != 0
	typ, value := d.Reward.visibleAmplification()
	core[19] = typ
	le.PutUint16(core[20:], value)
	if t.InitialSealed {
		attr.Binding, core[13] = 1, 1
	}
	if attr.ExpiresAt != nil {
		if *attr.ExpiresAt < 1 || *attr.ExpiresAt > math.MaxInt32 {
			return nil, storage.ErrInvalid
		}
		le.PutUint32(core[56:], uint32(*attr.ExpiresAt))
	}
	raw, err := json.Marshal(attr)
	if err != nil {
		return nil, err
	}
	g := storage.QuestGrant{Asset: storage.Asset{CharacterID: in.CharacterID, TemplateID: t.TemplateID, Source: "item",
		Template: d.Reward.Template, Count: max(1, d.Reward.StackCount), Attributes: raw, Core: core, Detail: []byte{}},
		Kind: t.Kind, First: t.FirstBagSlot, Last: min(t.LastBagSlot, t.FirstBagSlot+31+in.BagExpansion), StackLimit: t.StackLimit}
	switch {
	case t.Kind == 0:
		g.Source = "weapon"
		g.Weapon = &storage.WeaponFacts{QualitySeed: attr.QualitySeed, Durability: attr.Durability, MaximumDurability: t.MaximumDurability,
			MinimumLevel: t.MinimumLevel, UsableJobs: t.UsableJobs, ResourceHash: t.ResourceHash, Qualification: t.WeaponQualification}
	case t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32:
		g.Location, g.First, g.Last = 38, 0, 55
	}
	return []storage.QuestGrant{g}, nil
}

func (r *Run) PickupKey(object uint32) string {
	return fmt.Sprintf("dungeon-pickup:%s:%d:%d:%d:%d", r.idKey(), r.Definition.Dungeon.MazeIndex, r.Room.X, r.Room.Y, object)
}

func (r *Run) Pickup(body []byte) (GroundDrop, bool, bool, error) {
	if len(body) != 20 || body[4] != 0 {
		return GroundDrop{}, false, false, errors.New("invalid native pickup body")
	}
	if !r.Ready || r.InLayer || r.PlayerDeath != nil {
		return GroundDrop{}, false, false, nil
	}
	k := groundKey{cell{r.Room.X, r.Room.Y}, le.Uint32(body)}
	drop, exists := r.ground[k]
	if !exists || drop.RoomX != r.Room.X || drop.RoomY != r.Room.Y || drop.OwnerActorKey != r.Character {
		return GroundDrop{}, false, false, nil
	}
	if previous, picked := r.picked[k]; picked {
		return drop, bytes.Equal(previous, body), true, nil
	}
	return drop, true, false, nil
}

func (r *Run) ApplyPickup(object uint32, body []byte) {
	r.picked[groundKey{cell{r.Room.X, r.Room.Y}, object}] = slices.Clone(body)
}

func (r *Run) GroundSourceHash(d GroundDrop) string {
	if death, ok := r.Dead[d.MonsterActor]; ok && death.Key == d.DeathKey {
		return death.Result.DropRuleHash
	}
	if passive, ok := r.passive[d.DeathKey]; ok {
		return passive.result.DropRuleHash
	}
	return ""
}

func (r GroundReward) visibleAmplification() (byte, uint16) {
	if r.AmplificationType&0x80 != 0 {
		return 0x80, 0
	}
	return r.AmplificationType, r.AmplificationValue
}

func (d GroundDrop) IsMedal() bool {
	var t dropTemplate
	return json.Unmarshal(d.Reward.Template, &t) == nil && t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32
}

func (d GroundDrop) deathWire() ([]byte, error) {
	var t dropTemplate
	template := d.Reward.Template
	if d.Reward.Artifact != nil {
		template = d.Reward.Artifact
	}
	if len(template) > 0 {
		if err := json.Unmarshal(template, &t); err != nil {
			return nil, err
		}
	}
	var binding byte
	if t.InitialSealed {
		binding = 1
	}
	amount := d.Reward.Gold
	if t.TemplateID != 0 {
		amount = max(d.Reward.StackCount, d.Reward.QualitySeed)
	}
	w := make([]byte, 127)
	le.PutUint32(w[0:], d.ObjectKey)
	le.PutUint16(w[4:], d.SceneSlot)
	le.PutUint32(w[6:], t.TemplateID)
	le.PutUint32(w[10:], amount)
	w[14] = d.Reward.Reinforcement
	le.PutUint16(w[15:], d.Reward.Durability)
	w[17] = binding
	typ, value := d.Reward.visibleAmplification()
	w[23] = typ
	le.PutUint16(w[24:], value)
	if d.Reward.ExpiresAt != nil {
		if *d.Reward.ExpiresAt <= 0 || *d.Reward.ExpiresAt > math.MaxInt32 {
			return nil, storage.ErrInvalid
		}
		le.PutUint32(w[60:], uint32(*d.Reward.ExpiresAt))
	}
	le.PutUint16(w[123:], math.MaxUint16)
	le.PutUint16(w[125:], d.OwnerActorKey)
	// The core and two trailing U16 fields consume 127 bytes per native row.
	// The offsets include the object U32 and the in-scene U16 at the start.
	return w, nil
}

func (d GroundDrop) PickupNotice(slot uint16) []byte {
	w := le.AppendUint16(le.AppendUint32(nil, d.ObjectKey), d.OwnerActorKey)
	if d.Reward.Gold == 0 {
		var t dropTemplate
		if json.Unmarshal(d.Reward.Template, &t) == nil && t.Kind == 2 && slot < t.FirstBagSlot {
			slot = t.FirstBagSlot
		}
		w = append(w, make([]byte, 8)...)
		return append(le.AppendUint16(le.AppendUint16(w, d.OwnerActorKey), slot), 0)
	}
	w = le.AppendUint32(append(w, 1), d.Reward.Gold)
	w = append(w, 1)
	return append(w, make([]byte, 43)...)
}
