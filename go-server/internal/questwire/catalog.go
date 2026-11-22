// Package questwire projects the saved quest book for the matching client.
package questwire

import (
	"encoding/json"
	"errors"
	"slices"

	"s9a1/server/internal/storage"
)

type Target struct{ Job, Grow, Awakening int }
type Award struct {
	Job, Grow int
	Stage     byte
}
type Material struct{ Template, Count uint32 }
type RewardRow struct {
	Item      Material
	Job, Grow int
}
type Criterion struct {
	Id, Parent                              uint16
	MinimumLevel, MaximumLevel              byte
	Job, Grow, GrowNumber, AdvancementChain int
	AwakeningAward                          *Award
	Required                                [][]uint16
	Answers                                 []struct {
		Quest  uint16
		Answer byte
	}
	Collisions, Children            []uint16
	Targets, NonTargets             []Target
	Supported, LegacyUpperAwakening bool
}
type Quest struct {
	Criterion
	Hash                                                                                                           string
	Kind, Subtype                                                                                                  int
	Data, DestinationDungeons                                                                                      []int
	Materials, Rewards                                                                                             []Material
	RewardRows                                                                                                     []RewardRow
	FateIntroduction, AcceptanceSupported, TownHandIn, OrderedDungeonParent, DungeonNpcHandIn, OutsideDungeonClaim bool
	QuestionChoices                                                                                                byte
	NpcRangeTarget                                                                                                 *struct{ Town, Area, X, Y uint16 }
	TownCinematicTarget, DungeonCinematicTarget                                                                    *struct{ Movie, Map uint32 }
	Operations                                                                                                     *operationRules
}
type Ticket struct {
	Criterion
	Equipment            bool
	Grade, ObjectiveType string
	ProhibitedFlags      []string
	CascadeClear         []uint16
	QuestionChoices      byte
}
type Data struct {
	Ordinary         []Quest
	Tickets          []Ticket
	AcceptanceGrants []grant
}
type Catalog struct {
	ordinary map[uint16]*Quest
	tickets  map[uint16]*Ticket
	quests   []Quest
	extra    []Ticket
	grants   map[uint32]grant
}

func New(d Data) (*Catalog, error) {
	if len(d.Ordinary) == 0 || len(d.Ordinary) >= 30000 {
		return nil, errors.New("quest definitions are missing")
	}
	c := &Catalog{ordinary: make(map[uint16]*Quest, len(d.Ordinary)), tickets: make(map[uint16]*Ticket, len(d.Tickets)), quests: d.Ordinary, extra: d.Tickets}
	c.grants = make(map[uint32]grant, len(d.AcceptanceGrants))
	for _, g := range d.AcceptanceGrants {
		var t grantTemplate
		if json.Unmarshal(g.Template, &t) != nil || !t.valid() || t.Kind != 2 || t.FirstBagSlot != 177 || t.LastBagSlot != 232 || t.AccountCommonInventory || t.ResourceExpiration != nil || len(g.Core) != 119 || !json.Valid(g.Attributes) {
			return nil, errors.New("invalid frozen quest acceptance item")
		}
		if _, exists := c.grants[t.TemplateID]; exists {
			return nil, errors.New("duplicate quest acceptance template")
		}
		c.grants[t.TemplateID] = g
	}
	for i := range c.quests {
		q := &c.quests[i]
		if q.Id == 0 || q.Id >= 30000 || c.ordinary[q.Id] != nil || q.Hash == "" {
			return nil, errors.New("invalid quest catalog entry")
		}
		c.ordinary[q.Id] = q
	}
	for i := range c.extra {
		q := &c.extra[i]
		if q.Id == 0 || q.Id >= 30000 || c.tickets[q.Id] != nil {
			return nil, errors.New("invalid quest ticket entry")
		}
		c.tickets[q.Id] = q
	}
	return c, nil
}

type active struct {
	ObjectiveHash string
	Remaining     []uint32
	Receipts      []string
	Answer        *uint16
	NpcMet        bool
	UsedItemCount uint32
}
type history struct {
	Active  map[uint16]active
	Cleared map[uint16]byte
}
type state struct {
	Initialized bool
	Progression struct{ GrowType byte }
	Quests      history
}
type player struct {
	job, level, grow byte
	pos              storage.Position
	hist             history
	initialized      bool
}

func playerOf(c storage.Character) (player, error) {
	var p state
	err := json.Unmarshal(c.Gameplay, &p)
	return player{c.Job, c.Level, p.Progression.GrowType, c.Position, p.Quests, p.Initialized}, err
}
func (p player) matches(t Target) bool {
	return (t.Job < 0 || t.Job == int(p.job)) && (t.Grow < 0 || t.Grow == int(p.grow&15)) && (t.Awakening < 0 || t.Awakening == int(p.grow>>4))
}
func (p player) character(q Criterion, checkStage bool) bool {
	if p.level < q.MinimumLevel || p.level > q.MaximumLevel || q.Job >= 0 && q.Job != int(p.job) || q.Grow >= 0 && q.Grow != int(p.grow&15) || checkStage && q.GrowNumber >= 0 && q.GrowNumber != int(p.grow>>4) {
		return false
	}
	ok := len(q.Targets) == 0
	for _, t := range q.Targets {
		if p.matches(t) {
			ok = true
			break
		}
	}
	for _, t := range q.NonTargets {
		if p.matches(t) {
			return false
		}
	}
	return ok
}
func (p player) choices(q Criterion) bool {
	for _, a := range q.Answers {
		if int(p.hist.Cleared[a.Quest]) != int(a.Answer)+1 {
			return false
		}
	}
	for _, id := range q.Collisions {
		if _, ok := p.hist.Active[id]; ok || p.hist.Cleared[id] != 0 {
			return false
		}
	}
	return true
}
func requirement(groups [][]uint16, allowEmpty bool, can func(uint16) bool) bool {
	if len(groups) == 0 {
		return true
	}
	for _, g := range groups {
		ok := allowEmpty || len(g) > 0
		for _, id := range g {
			if !can(id) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
func (p player) advancement(q Criterion) bool {
	a := q.AwakeningAward
	if q.AdvancementChain == 0 && a == nil {
		return true
	}
	if q.AdvancementChain != 0 && q.AdvancementChain != 2 && q.AdvancementChain != 3 && q.AdvancementChain != 4 {
		return false
	}
	branch, stage := int(p.grow&15), int(p.grow>>4)
	if p.job > 15 || stage > 3 || branch > 5 || ((p.job == 9 || p.job == 10) != (branch == 0)) {
		return false
	}
	required := q.AdvancementChain - 2
	if q.AdvancementChain == 0 {
		required = int(a.Stage) - 1
	}
	if required != stage {
		return false
	}
	if a == nil {
		return true
	}
	return a.Stage >= 1 && a.Stage <= 3 && int(a.Stage) == required+1 && a.Job >= -1 && a.Job <= 15 && a.Grow >= -1 && a.Grow <= 5 && (a.Job < 0 || a.Job == int(p.job)) && (a.Grow < 0 || a.Grow == branch)
}
func (h history) preceding(parent *Criterion, child uint16) bool {
	if parent == nil {
		return false
	}
	i := slices.Index(parent.Children, child)
	if i < 0 {
		return false
	}
	for _, id := range parent.Children[:i] {
		if h.Cleared[id] == 0 {
			return false
		}
	}
	return true
}
func (c *Catalog) ordinaryAvailable(p player, q *Quest) bool {
	if !p.initialized || !q.AcceptanceSupported || p.hist.Cleared[q.Id] != 0 || q.FateIntroduction && (p.pos.Town != 38 || p.pos.Area != 1) || !p.character(q.Criterion, false) || !p.advancement(q.Criterion) || !p.choices(q.Criterion) {
		return false
	}
	skip := q.LegacyUpperAwakening && q.Id == 6413 && c.ordinary[6410] == nil && c.ordinary[6411] == nil && c.ordinary[6412] == nil
	if !skip && !requirement(q.Required, true, func(id uint16) bool { return c.ordinary[id] != nil && p.hist.Cleared[id] != 0 }) {
		return false
	}
	if q.Parent != 0 {
		parent := c.ordinary[q.Parent]
		if _, active := p.hist.Active[q.Parent]; parent == nil || !active || !p.hist.preceding(&parent.Criterion, q.Id) {
			return false
		}
	}
	return true
}
