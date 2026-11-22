package ruby

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"slices"

	"s9a1/server/internal/savejson"
)

type Tier struct {
	Item              uint32
	Points, Threshold int
}
type Limit struct{ Minimum, Maximum, MaximumInputTier int }
type CompoundRules struct {
	Tiers         []Tier
	Limits        []Limit
	DisplayRanges []int
}
type Composition struct {
	Policy     string
	Week       int64
	Clear, Run string
	Body       savejson.String
	Slots      []int
	Items      []uint32
	Product    uint32
	Sample     int
	Hash       string
	Rules      CompoundRules
}
type Weight struct {
	Item   uint32
	Weight uint64
}
type Resources struct {
	Hash   string
	Prizes []struct {
		Floor   int
		Weights []Weight
	}
	Jumps []struct {
		Floor, Rate int
		Distances   []Weight
	}
	HiddenRates                                                 []struct{ Floor, Rate, Reserved int }
	HiddenIds                                                   []uint32
	UpgradedRate, BossRate, NamedMinimum, BossMinimum, Cooldown int
	HiddenPrizes                                                []struct {
		Kind, Minimum, Maximum int
		Rolls                  [][]Weight
	}
	BoxIds   []uint32
	Compound *CompoundRules
}

func (r CompoundRules) valid() bool {
	if len(r.Tiers) != 4 || len(r.Limits) != 3 || len(r.DisplayRanges) != 10 || r.DisplayRanges[0] != 0 || r.DisplayRanges[9] != 100000 {
		return false
	}
	ids := [4]uint32{10312271, 10312272, 10312273, 10319994}
	for i, t := range r.Tiers {
		if t.Item != ids[i] || t.Threshold <= 0 || 1500%t.Threshold != 0 || i == 0 && t.Threshold != 1 || i > 0 && t.Threshold < r.Tiers[i-1].Threshold || i < 3 && (t.Points <= 0 || t.Points > 1500) || i == 3 && t.Points != -1 {
			return false
		}
	}
	for _, l := range r.Limits {
		if l.Minimum < 2 || l.Maximum > 5 || l.Minimum > l.Maximum || l.MaximumInputTier < 1 || l.MaximumInputTier > 2 {
			return false
		}
	}
	for i := 0; i < 10; i += 2 {
		if r.DisplayRanges[i] > r.DisplayRanges[i+1] || i > 0 && r.DisplayRanges[i] != r.DisplayRanges[i-1]+1 {
			return false
		}
	}
	return true
}
func (r CompoundRules) Product(dungeon uint32, items []uint32, sample int) (uint32, error) {
	if !r.valid() || !Hidden(dungeon) || sample < 0 || sample >= 1500 {
		return 0, ErrInvalid
	}
	kind := 0
	if dungeon == 100000650 {
		kind = 2
	} else if dungeon >= 100000645 {
		kind = 1
	}
	l := r.Limits[kind]
	if len(items) < l.Minimum || len(items) > l.Maximum {
		return 0, ErrInvalid
	}
	sum := 0
	for _, item := range items {
		i := slices.IndexFunc(r.Tiers, func(t Tier) bool { return t.Item == item })
		if i < 0 || i > l.MaximumInputTier {
			return 0, ErrInvalid
		}
		sum += r.Tiers[i].Points
	}
	for i := l.MaximumInputTier + 1; i >= 0; i-- {
		t := r.Tiers[i]
		if int64(sample+1)*int64(t.Threshold) <= int64(sum)*1500 {
			return t.Item, nil
		}
	}
	return 0, ErrInvalid
}
func (l *Ledger) replayCompound(r Receipt) error {
	var p Composition
	if json.Unmarshal([]byte(r.Input), &p) != nil || !exact(r.Input, p) || p.Policy != CompoundPolicy || p.Week != l.View.Week || r.Result != "compounded" || len(r.Grants) != 0 || !hash(p.Hash) {
		return ErrInvalid
	}
	clear, ok := l.Clears[p.Clear]
	if !ok || clear.Run != p.Run || clear.Week != p.Week || !Hidden(clear.Dungeon) || clear.Advance == nil || l.receipts[p.Clear].Revision >= r.Revision || p.Clear != fmt.Sprintf("ruby-hidden-clear:%d:%d", p.Week, clear.Floor) || r.Key != fmt.Sprintf("ruby-compound:%d:%d", p.Week, clear.Floor) {
		return ErrInvalid
	}
	b, err := base64.StdEncoding.DecodeString(string(p.Body))
	if err != nil {
		return err
	}
	slots, err := ReadSlots(b, true)
	if err != nil || !slices.Equal(slots, p.Slots) || len(slots) != len(p.Items) || len(l.View.Boxes) >= 100 {
		return ErrInvalid
	}
	indices, err := l.View.Indices(slots, false)
	if err != nil {
		return err
	}
	for i, k := range indices {
		if l.View.Boxes[k] != p.Items[i] {
			return ErrInvalid
		}
	}
	product, err := p.Rules.Product(clear.Dungeon, p.Items, p.Sample)
	if err != nil || product != p.Product {
		return ErrInvalid
	}
	for _, k := range indices {
		l.View.Boxes[k] = math.MaxUint32
	}
	l.View.Boxes = append(l.View.Boxes, product)
	return nil
}
func terminal(r Receipt, c Clear) bool {
	var src struct {
		Source, Version, RunId, ResourceHash                string
		Dungeon                                             uint32
		Character, Opcode, CleanupBossActor, CompanionActor uint16
		Class                                               byte
		StormCleanup                                        *struct{ Arrival, PlayerAttributed bool }
		ScriptTerminal                                      *struct {
			QuestId   int
			Cinematic json.RawMessage
		}
		SummonedActor *struct{ Actor uint16 }
	}
	var result struct{ RoomCleared, DungeonCleared bool }
	if json.Unmarshal([]byte(r.Input), &src) != nil || json.Unmarshal([]byte(r.Result), &result) != nil || !result.RoomCleared || !result.DungeonCleared || src.RunId != c.Run || src.ResourceHash != c.Hash || src.Dungeon != c.Dungeon || src.Character != c.Character {
		return false
	}
	ordinary := src.Source == "ClientDelegated" && src.Class == 1 && src.Opcode == 39
	clean := src.CleanupBossActor == 0 && src.CompanionActor == 0
	if z := src.StormCleanup; z != nil {
		v := "TrustedLocalClientStormLaneCleanup/v1"
		if z.Arrival {
			v = "TrustedLocalClientStormLaneArrival/v1"
		} else if z.PlayerAttributed {
			v = "TrustedLocalClientStormLaneAttributedCleanup/v1"
		}
		return ordinary && clean && src.SummonedActor == nil && src.ScriptTerminal == nil && src.Version == v
	}
	if z := src.SummonedActor; z != nil {
		return ordinary && clean && src.ScriptTerminal == nil && z.Actor > 0 && z.Actor < 402 && src.Version == "TrustedLocalClientItemSummonCombat/v1"
	}
	if src.Source == "GameMaster" {
		return src.ScriptTerminal == nil && clean && src.Version == "AuthorizedLocalGmKill/v1" && src.Class == 0 && src.Opcode == 0
	}
	if !ordinary {
		return false
	}
	if z := src.ScriptTerminal; z != nil {
		v := "TrustedLocalClientScriptTerminal/v1"
		if len(z.Cinematic) > 0 && string(z.Cinematic) != "null" {
			v = "TrustedLocalClientCinematicDestroy/v1"
		} else if z.QuestId == 3213 {
			v = "TrustedLocalClientAganzoTerminal/v1"
		}
		return clean && src.Version == v
	}
	if src.CompanionActor != 0 {
		return src.CompanionActor >= 402 && src.CompanionActor < 65535 && src.CleanupBossActor == 0 && src.Version == "TrustedLocalClientCompanionCombat/v1"
	}
	if src.CleanupBossActor != 0 {
		return src.Version == "TrustedLocalClientBossCleanup/v1"
	}
	return src.Version == "TrustedLocalClientCombat/v1" || src.Version == "TrustedLocalClientUnattributedDeath/v1"
}
