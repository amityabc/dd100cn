package dungeon

import (
	"crypto/rand"
	"encoding/json"
	"math"
	"math/big"
	"time"

	"s9a1/server/internal/storage"
)

type oculusRewardPool struct {
	Kind, Difficulty byte
	PartyKindRaw     int
}
type oculusRewardRow struct {
	Index             int
	Container, Weight uint32
	Pool              oculusRewardPool
}
type oculusRewardChoice struct{ TemplateId, Weight, Count uint32 }
type OculusCardData struct {
	DungeonId, CounterDungeonId uint32
	Limit, RoundCount           byte
	CycleRaw                    int
	ResourceHash                string
	Rows                        []oculusRewardRow
	Containers                  map[uint32]struct {
		Groups []struct {
			DrawCount byte
			Choices   []oculusRewardChoice
		}
	}
	Templates map[uint32]json.RawMessage
}

type OculusCardPlan struct {
	Policy         string
	Week           int64
	Limit, Ordinal byte
	DungeonId      uint32
	Reward         *oculusReward
}

type oculusRewardAttributes struct {
	QualitySeed                             uint32
	Durability                              uint16
	Reinforcement, Forge, AmplificationType byte
	AmplificationValue                      uint16
	Enchantment                             uint32
	Binding                                 byte
	ExpiresAt                               *int64
	Locked                                  bool
	EnchantUpgradeCount                     byte
}

type oculusRewardItem struct {
	Template   json.RawMessage
	Count      uint32
	Attributes oculusRewardAttributes
	Raw119     [119]byte
	Detail     []uint16
}
type oculusRewardRound struct {
	SelectedRow int
	Container   uint32
	Items       []oculusRewardItem
}
type oculusReward struct {
	Policy, ResourceHash        string
	DungeonId, CounterDungeonId uint32
	Limit                       byte
	CycleRaw                    int
	Pool                        oculusRewardPool
	RoundCount                  byte
	Rounds                      []oculusRewardRound
}

func oculusWeighted[T any](rows []T, weight func(T) uint32) (T, error) {
	var zero T
	var total uint64
	for _, r := range rows {
		total += uint64(weight(r))
	}
	if total == 0 || total > math.MaxUint32 {
		return zero, storage.ErrInvalid
	}
	value, err := rand.Int(rand.Reader, new(big.Int).SetUint64(total))
	if err != nil {
		return zero, err
	}
	ticket := value.Uint64()
	for _, r := range rows {
		n := uint64(weight(r))
		if ticket < n {
			return r, nil
		}
		ticket -= n
	}
	return zero, storage.ErrInvalid
}

func (c *Catalog) OculusCard(dungeon uint32, week int64, used bool, now time.Time) (*OculusCardPlan, error) {
	d, ok := c.OculusCards[dungeon]
	if !ok || d.DungeonId != dungeon || d.CounterDungeonId != 100000007 || d.Limit != 1 || d.CycleRaw != 1 || !isHash(d.ResourceHash) ||
		d.RoundCount == 0 || d.RoundCount > 2 {
		return nil, storage.ErrInvalid
	}
	plan := &OculusCardPlan{Policy: "OfficialSharedOculusSpecialCnSchedule/v1", Week: week, Limit: d.Limit, DungeonId: dungeon}
	if used {
		return plan, nil
	}
	fixed := oculusRewardPool{Difficulty: 4}
	rows := make([]oculusRewardRow, 0, len(d.Rows))
	for _, r := range d.Rows {
		if r.Pool == fixed {
			rows = append(rows, r)
		}
	}
	z := &oculusReward{Policy: "LocalOculusResourceReward/v2", ResourceHash: d.ResourceHash, DungeonId: dungeon, CounterDungeonId: d.CounterDungeonId,
		Limit: d.Limit, CycleRaw: d.CycleRaw, Pool: fixed, RoundCount: d.RoundCount}
	for range d.RoundCount {
		row, err := oculusWeighted(rows, func(a oculusRewardRow) uint32 { return a.Weight })
		if err != nil {
			return nil, err
		}
		selected := oculusRewardRound{SelectedRow: row.Index, Container: row.Container}
		for _, group := range d.Containers[row.Container].Groups {
			for range group.DrawCount {
				choice, err := oculusWeighted(group.Choices, func(a oculusRewardChoice) uint32 { return a.Weight })
				if err != nil {
					return nil, err
				}
				raw := d.Templates[choice.TemplateId]
				var t dropTemplate
				if json.Unmarshal(raw, &t) != nil || t.TemplateID != choice.TemplateId || choice.Count == 0 || choice.Count > t.StackLimit {
					return nil, storage.ErrInvalid
				}
				item := oculusRewardItem{Template: raw, Count: choice.Count, Detail: []uint16{}}
				if t.Kind != 2 {
					n, err := rand.Int(rand.Reader, big.NewInt(999999998))
					if err != nil {
						return nil, err
					}
					item.Attributes.QualitySeed, item.Attributes.Durability = uint32(n.Uint64())+1, t.MaximumDurability
				}
				if t.InitialSealed {
					item.Attributes.Binding, item.Raw119[13] = 1, 1
				}
				if expiry := dropExpiration(t, now); expiry != nil {
					if *expiry <= now.Unix() || *expiry > math.MaxInt32 {
						return nil, storage.ErrInvalid
					}
					item.Attributes.ExpiresAt = expiry
					le.PutUint32(item.Raw119[56:], uint32(*expiry))
				}
				selected.Items = append(selected.Items, item)
			}
		}
		if len(selected.Items) == 0 {
			return nil, storage.ErrInvalid
		}
		z.Rounds = append(z.Rounds, selected)
	}
	plan.Ordinal, plan.Reward = 1, z
	return plan, nil
}

func (r *OculusCardPlan) itemViews() [][2]uint32 {
	if r == nil || r.Reward == nil {
		return nil
	}
	var items [][2]uint32
	for _, round := range r.Reward.Rounds {
		for _, i := range round.Items {
			var d dropTemplate
			if json.Unmarshal(i.Template, &d) == nil {
				items = append(items, [2]uint32{d.TemplateID, i.Count})
			}
		}
	}
	return items
}

func (r *OculusCardPlan) Grants(in storage.Inventory) ([]storage.QuestGrant, error) {
	if r == nil || r.Reward == nil {
		return nil, nil
	}
	card := &CardReward{}
	for _, round := range r.Reward.Rounds {
		for _, i := range round.Items {
			card.Items = append(card.Items, CardItem{Reward: GroundReward{Template: i.Template, StackCount: i.Count,
				Durability: i.Attributes.Durability, QualitySeed: i.Attributes.QualitySeed, ExpiresAt: i.Attributes.ExpiresAt}})
		}
	}
	return card.Grants(in)
}
