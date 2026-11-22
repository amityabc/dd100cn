// Package fate implements the original Destiny2 activity rules and packet formats.
package fate

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"

	"s9a1/server/internal/opening"
)

var ErrInvalid = errors.New("invalid activity state or request")

const Event uint32 = 4171

type RankRule struct{ Maximum, Cost int32 }
type CollectionItem struct {
	EffectId int32
	Slot     uint16
	Template json.RawMessage
}
type Reward struct {
	Template json.RawMessage
	Count    uint32
}
type Node struct {
	Id, Type   int
	Next       []int
	Hidden     bool
	FixedWaves [][]int
}
type Layout struct {
	Grid, Preset int
	Nodes        map[int]Node
}
type Definition struct {
	EventId, DungeonId                     uint32
	ResourceHash                           string
	MinimumLevel                           byte
	StoryCapacity, DefaultGrid             int
	PointLimit, SinPointLimit              int32
	SinCosts                               []int32
	Blessings, Sins                        []RankRule
	Collection                             []CollectionItem
	Rewards                                []Reward
	Layouts                                []Layout
	Points                                 map[int][]int
	ShopNodeTypes, ShopProducts            []int
	DropWeight, NoDropWeight, InstanceBuff int
	BuffMinimumLevel, BuffMaximumLevel     byte
	BossPositions                          []struct{ X, Y uint16 }
	LoadingDungeon                         opening.Dungeon
}
type Drop struct {
	ObjectKey                     uint32
	SceneSlot                     uint16
	Phase                         int
	EffectId                      int32
	Template                      json.RawMessage
	X, Y                          uint16
	Claimed                       bool
	RuleHash, NodeKey, PickupBody string
}
type Run struct {
	RunId, ResourceHash                                   string
	EventId, DungeonId                                    uint32
	Presets                                               []int
	Phase                                                 int
	PhaseRewards                                          []Reward
	Collection                                            []CollectionItem
	ShopNodes                                             []string
	ShopProducts                                          []int
	LastNode                                              *int
	Dead, Settled                                         bool
	DeathBody, ResultBody, SettlementDay, SettlementCause string
	PendingRewards, DeliveredRewards                      []Reward
	EarnedPoints                                          int32
	CompletedPhases                                       []int
	CompletedNodes                                        map[string]string
	Drops                                                 []Drop
}
type Profile struct {
	ArchangelPoints, StoryCount, FullClears int32
	Blessings, Sins, EquippedEffects        []int32
	StoryWeek                               string
	DailyClaims                             []string
	Run                                     *Run
}

func Read(data json.RawMessage) (Profile, error) {
	p := Profile{Blessings: make([]int32, 7), Sins: make([]int32, 7), EquippedEffects: make([]int32, 3), DailyClaims: []string{}}
	var g struct{ Fate json.RawMessage }
	if err := json.Unmarshal(data, &g); err != nil {
		return p, err
	}
	if len(g.Fate) == 0 {
		return p, nil
	}
	if bytes.Equal(g.Fate, []byte("null")) {
		return p, ErrInvalid
	}
	err := json.Unmarshal(g.Fate, &p)
	return p, err
}

func EqualTemplate(left, right json.RawMessage) bool {
	var a, b any
	read := func(raw json.RawMessage, v *any) bool {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		return d.Decode(v) == nil
	}
	return read(left, &a) && read(right, &b) && reflect.DeepEqual(a, b)
}
func (d *Definition) layout(grid, preset int) *Layout {
	for i := range d.Layouts {
		l := &d.Layouts[i]
		if l.Grid == grid && l.Preset == preset {
			return l
		}
	}
	return nil
}
func (d *Definition) effect(id int32) *CollectionItem {
	for i := range d.Collection {
		if d.Collection[i].EffectId == id {
			return &d.Collection[i]
		}
	}
	return nil
}
func (d *Definition) validRun(r *Run) bool {
	if r == nil {
		return true
	}
	if len(r.RunId) != 36 || r.RunId == "00000000-0000-0000-0000-000000000000" || r.ResourceHash != d.ResourceHash || r.EventId != d.EventId || r.DungeonId != d.DungeonId || r.Phase < 0 || r.Phase > 4 || len(r.Presets) != 10 || len(r.Drops) > 5 || r.EarnedPoints < 0 || r.EarnedPoints > d.PointLimit || len(r.CompletedNodes) > 1280 || len(r.PhaseRewards) != len(d.Rewards) || len(r.Collection) != len(d.Collection) {
		return false
	}
	for i := 0; i < 10; i += 2 {
		if d.layout(r.Presets[i], r.Presets[i+1]) == nil {
			return false
		}
	}
	for _, v := range r.CompletedPhases {
		if v < 0 || v > 4 {
			return false
		}
	}
	for i, v := range r.PhaseRewards {
		if v.Count != d.Rewards[i].Count || !EqualTemplate(v.Template, d.Rewards[i].Template) {
			return false
		}
	}
	for i, v := range r.Collection {
		o := d.Collection[i]
		if v.EffectId != o.EffectId || v.Slot != o.Slot || !EqualTemplate(v.Template, o.Template) {
			return false
		}
	}
	seen := map[uint32]bool{}
	for _, v := range r.Drops {
		c := d.effect(v.EffectId)
		if c == nil || v.ObjectKey != uint32(v.SceneSlot) || v.SceneSlot == 0 || v.SceneSlot == 65535 || v.Phase < 0 || v.Phase > 4 || v.RuleHash != d.ResourceHash || seen[v.ObjectKey] || !EqualTemplate(v.Template, c.Template) {
			return false
		}
		seen[v.ObjectKey] = true
	}
	return true
}
func (d *Definition) Validate(p Profile) error {
	if d == nil || d.EventId != Event || len(d.Blessings) != 7 || len(d.Sins) != 7 || p.ArchangelPoints < 0 || p.ArchangelPoints > d.PointLimit || p.FullClears < 0 || len(p.Blessings) != 7 || len(p.Sins) != 7 || len(p.EquippedEffects) != 3 || p.StoryCount < 0 || int(p.StoryCount) > d.StoryCapacity || len(p.StoryWeek) > 8 || p.DailyClaims == nil {
		return ErrInvalid
	}
	for _, k := range p.DailyClaims {
		if len(k) != 10 || k[8] != ':' || k[9] < '0' || k[9] > '4' {
			return ErrInvalid
		}
	}
	var spent, sin int64
	for i := range d.Blessings {
		b, s := p.Blessings[i], p.Sins[i]
		if b < 0 || s < 0 || b > d.Blessings[i].Maximum || s > d.Sins[i].Maximum || p.FullClears == 0 && s != 0 {
			return ErrInvalid
		}
		spent += int64(b) * int64(d.Blessings[i].Cost)
		sin += int64(s) * int64(d.Sins[i].Cost)
	}
	if spent > int64(p.ArchangelPoints) || sin > int64(d.SinPointLimit) {
		return ErrInvalid
	}
	for i, e := range p.EquippedEffects {
		if e != 0 && (d.effect(e) == nil || slices.Contains(p.EquippedEffects[:i], e)) {
			return ErrInvalid
		}
	}
	if !d.validRun(p.Run) {
		return ErrInvalid
	}
	return nil
}

func (d *Definition) Rank(p Profile, action, id int32) (Profile, error) {
	if err := d.Validate(p); err != nil {
		return p, err
	}
	reset := action == 3 || action == 6
	if action < 1 || action > 6 || reset && id != 0 || !reset && (id < 1 || id > 7) || action >= 4 && p.FullClears == 0 {
		return p, ErrInvalid
	}
	ranks := slices.Clone(p.Blessings)
	if action >= 4 {
		ranks = slices.Clone(p.Sins)
	}
	if reset {
		clear(ranks)
	} else if action == 1 || action == 4 {
		if ranks[id-1] == math.MaxInt32 {
			return p, ErrInvalid
		}
		ranks[id-1]++
	} else {
		ranks[id-1]--
	}
	if action < 4 {
		p.Blessings = ranks
	} else {
		p.Sins = ranks
	}
	return p, d.Validate(p)
}

func (d *Definition) Equip(p Profile, effect, slot int32, owns func(CollectionItem) bool) (Profile, error) {
	if err := d.Validate(p); err != nil {
		return p, err
	}
	if slot < 0 || slot > 2 || effect < 0 {
		return p, ErrInvalid
	}
	if effect != 0 {
		item := d.effect(effect)
		if item == nil || !owns(*item) {
			return p, ErrInvalid
		}
		for i, e := range p.EquippedEffects {
			if i != int(slot) && e == effect {
				return p, ErrInvalid
			}
		}
	}
	p.EquippedEffects = slices.Clone(p.EquippedEffects)
	p.EquippedEffects[slot] = effect
	return p, nil
}

// PatchFate edits only the selected character's named activity fields.
func PatchFate(data json.RawMessage, fields map[string]any) (json.RawMessage, error) {
	var g, raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	if g == nil {
		return nil, ErrInvalid
	}
	if b := g["Fate"]; len(b) > 0 && !strings.EqualFold(string(b), "null") {
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, err
		}
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	for k, v := range fields {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if k == "Run" && len(raw[k]) > 0 && !bytes.Equal(raw[k], []byte("null")) {
			var old, next map[string]json.RawMessage
			if err = json.Unmarshal(raw[k], &old); err != nil {
				return nil, err
			}
			if err = json.Unmarshal(b, &next); err != nil {
				return nil, err
			}
			if next != nil && bytes.Equal(old["RunId"], next["RunId"]) {
				for name, x := range next {
					old[name] = x
				}
				b, err = json.Marshal(old)
				if err != nil {
					return nil, err
				}
			}
		}
		raw[k] = b
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	g["Fate"] = b
	return json.Marshal(g)
}
