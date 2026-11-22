package questwire

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"slices"

	"s9a1/server/internal/storage"
)

const questDropVersion = "QuestCollectionDrops/v2"

type QuestDropRule struct {
	Dungeon, Difficulty   int
	Template, Count, Code uint32
	EnemyKind, Rate       byte
	MaximumHeld           int
}

type QuestDropRoll struct {
	Quest                  uint16
	ObjectiveHash          string
	Row                    int
	Rule                   QuestDropRule
	HeldBefore             uint64
	Limit, Rolled, Granted uint32
}

type QuestDropReceipt struct {
	Version string
	Rolls   []QuestDropRoll
}

type QuestDropSource struct {
	EventKey, RunID     string
	Seed, Dungeon, Code uint32
	Kind, Difficulty    byte
	RepeatedPassive     bool
}

func (s QuestDropSource) match(row QuestDropRule) bool {
	return row.Code == s.Code && row.EnemyKind == s.Kind && (row.Dungeon == int(s.Dungeon) || row.Dungeon == -1) &&
		row.Difficulty >= -1 && row.Difficulty <= int(s.Difficulty)
}

func (c *Catalog) dropQuests(ch storage.Character, source QuestDropSource) ([]*Quest, error) {
	p, err := playerOf(ch)
	if err != nil {
		return nil, err
	}
	if source.Difficulty > 4 || source.Kind < 1 || source.Kind > 3 || !p.initialized {
		return nil, nil
	}
	var result []*Quest
	for id, a := range p.hist.Active {
		q := c.ordinary[id]
		if q == nil || q.Kind != seeking || !q.Supported || q.Operations == nil ||
			!validActive(q, a) || !c.ordinaryAvailable(p, q) || source.RepeatedPassive && id != 3545 && id != 3735 {
			continue
		}
		switch q.Operations.Grade {
		case "epic", "side", "mission", "achievement":
		case "daily mission":
			if !q.Operations.Daily {
				continue
			}
		default:
			continue
		}
		if len(q.DestinationDungeons) != 0 && !slices.Contains(q.DestinationDungeons, int(source.Dungeon)) {
			continue
		}
		if len(q.DestinationRequirements) != 0 && !slices.ContainsFunc(q.DestinationRequirements, func(row struct{ Dungeon, MinimumDifficulty int }) bool {
			return row.Dungeon == int(source.Dungeon) && row.MinimumDifficulty >= -1 && row.MinimumDifficulty <= int(source.Difficulty)
		}) {
			continue
		}
		if len(q.DestinationDungeons) == 0 && !slices.ContainsFunc(q.DropRules, func(row QuestDropRule) bool {
			return row.Dungeon == int(source.Dungeon) && row.Difficulty >= -1 && row.Difficulty <= int(source.Difficulty)
		}) {
			continue
		}
		if slices.ContainsFunc(q.DropRules, source.match) {
			result = append(result, q)
		}
	}
	slices.SortFunc(result, func(a, b *Quest) int { return int(a.Id) - int(b.Id) })
	return result, nil
}

func (s QuestDropSource) sample(q *Quest, row int, trial uint32) (uint32, error) {
	for salt := range 32 {
		b := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s|%d|%s|%d|%d|%d", questDropVersion, guidKey(s.RunID), s.Seed,
			s.EventKey, q.Id, q.Hash, row, trial, salt)))
		if v := little.Uint32(b[:]); v < 4294967200 {
			return v % 100, nil
		}
	}
	return 0, storage.ErrInvalid
}

func (c *Catalog) RollQuestDrops(ch storage.Character, s QuestDropSource, inventory func() (storage.Inventory, error)) (*QuestDropReceipt, []storage.QuestGrant, int64, error) {
	quests, err := c.dropQuests(ch, s)
	if err != nil || len(quests) == 0 {
		return nil, nil, 0, err
	}
	in, err := inventory()
	if err != nil {
		return nil, nil, 0, err
	}
	if in.CharacterID != ch.ID || in.Revision != ch.Revision {
		return nil, nil, 0, storage.ErrConflict
	}
	held := map[uint32]uint64{}
	for _, a := range in.Items {
		if a.Location == 0 {
			held[a.TemplateID] += uint64(a.Count)
		}
	}
	plan := &QuestDropReceipt{Version: questDropVersion}
	var grants []storage.QuestGrant
	var trials uint32
	for _, q := range quests {
		for i, row := range q.DropRules {
			if !s.match(row) {
				continue
			}
			if row.Template == 0 || row.Count == 0 || row.Count > 100 || row.Rate > 100 || row.MaximumHeld < -1 ||
				len(plan.Rolls) >= 128 || trials+row.Count > 4096 {
				return nil, nil, 0, storage.ErrInvalid
			}
			var required uint64
			for _, m := range q.Materials {
				if m.Template == row.Template {
					required += uint64(m.Count)
				}
			}
			if required == 0 || required > math.MaxUint32 {
				return nil, nil, 0, storage.ErrInvalid
			}
			if row.MaximumHeld >= 0 {
				required = min(required, uint64(row.MaximumHeld))
			}
			proof := QuestDropRoll{Quest: q.Id, ObjectiveHash: q.Hash, Row: i, Rule: row, HeldBefore: held[row.Template], Limit: uint32(required)}
			if proof.HeldBefore < required {
				trials += row.Count
				for trial := range row.Count {
					if row.Rate == 100 {
						proof.Rolled++
					} else if row.Rate != 0 {
						value, err := s.sample(q, i, trial)
						if err != nil {
							return nil, nil, 0, err
						}
						if value < uint32(row.Rate) {
							proof.Rolled++
						}
					}
				}
				proof.Granted = min(proof.Rolled, uint32(required-proof.HeldBefore))
			}
			plan.Rolls = append(plan.Rolls, proof)
			if proof.Granted == 0 {
				continue
			}
			raw := c.rewards[row.Template]
			var t grantTemplate
			if json.Unmarshal(raw, &t) != nil || !t.valid() || t.Kind != 2 || t.FirstBagSlot != 177 || t.LastBagSlot != 232 ||
				t.ResourceExpiration != nil || t.AccountCommonInventory || t.PetConsumable {
				return nil, nil, 0, ErrRewardResource
			}
			grants = append(grants, storage.QuestGrant{
				Asset: storage.Asset{CharacterID: in.CharacterID, Source: "item", TemplateID: row.Template, Count: proof.Granted,
					Template: raw, Attributes: json.RawMessage("{}"), Core: make([]byte, 119), Detail: []byte{}},
				Kind: t.Kind, First: t.FirstBagSlot, Last: min(t.LastBagSlot, t.FirstBagSlot+31+in.BagExpansion), StackLimit: t.StackLimit,
			})
			held[row.Template] += uint64(proof.Granted)
		}
	}
	return plan, grants, in.AssetRevision, nil
}

func (p *QuestDropReceipt) HasGrants() bool {
	return p != nil && slices.ContainsFunc(p.Rolls, func(roll QuestDropRoll) bool { return roll.Granted != 0 })
}
