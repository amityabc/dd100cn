package resources

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
)

type TownPoint struct {
	Town, Area, X, Y uint16
	Direction        byte
}

type areaKey struct{ Town, Area uint16 }
type permission struct {
	Level                     byte
	All, Any, Except, Started []uint16
	Blocked                   bool
}
type townArea struct {
	Town, Area uint16
	Path       string
	Permission permission
	Phases     []string
}
type phaseRule struct {
	Kind      string
	Id, Index int
}
type npcPlacement struct {
	Id   uint32
	X, Y uint16
}
type townMap struct {
	Path                      string
	AirshipRoomMenu, DockMenu bool
	Npcs                      []npcPlacement
	Routes                    []struct {
		Town, Area uint16
		Permission permission
	}
}
type roomReturn struct {
	Town, Area uint16
	Point      TownPoint
}
type townData struct {
	Areas       []townArea
	Maps        []townMap
	Phases      map[uint16][]phaseRule
	RoomReturns []roomReturn
	Warps       []seriaWarp
	NpcWarps    []npcWarp
	Menus       transportMenus
	Room, Exit  TownPoint
	Ranges      []rangeQuest
}
type TownCatalog struct {
	data   townData
	areas  map[areaKey]townArea
	maps   map[string]townMap
	ranges map[uint16]rangeQuest
}
type activeQuest struct {
	ObjectiveHash string
	Remaining     []uint32
	Receipts      []string
}
type TownQuests struct {
	Active  map[uint16]activeQuest
	Cleared map[uint16]int
}

func ReadTownQuests(gameplay json.RawMessage) (TownQuests, error) {
	var p struct{ Quests TownQuests }
	err := json.Unmarshal(gameplay, &p)
	return p.Quests, err
}

func newTownCatalog(d townData) (*TownCatalog, error) {
	t := &TownCatalog{data: d, areas: make(map[areaKey]townArea), maps: make(map[string]townMap), ranges: make(map[uint16]rangeQuest)}
	for _, a := range d.Areas {
		k := areaKey{a.Town, a.Area}
		if _, duplicate := t.areas[k]; duplicate || a.Path == "" {
			return nil, errors.New("invalid town area in resource catalog")
		}
		t.areas[k] = a
	}
	for _, m := range d.Maps {
		path := strings.ToLower(m.Path)
		if _, dup := t.maps[path]; path == "" || dup {
			return nil, errors.New("invalid town map in resource catalog")
		}
		t.maps[path] = m
	}
	for _, q := range d.Ranges {
		if _, dup := t.ranges[q.Id]; dup || q.Id == 0 || q.Hash == "" || !(q.Subtype == 1 && len(q.Data) == 6 || q.Subtype == 0 && len(q.Data) == 3 && q.NpcRangeTarget != nil) {
			return nil, errors.New("invalid range quest in resource catalog")
		}
		t.ranges[q.Id] = q
	}
	if !t.IsRoom(d.Room) || !t.validPoint(d.Room) || !t.validPoint(d.Exit) || len(t.maps) == 0 {
		return nil, errors.New("incomplete town resource catalog")
	}
	return t, nil
}
func (p TownPoint) key() areaKey { return areaKey{p.Town, p.Area} }
func (t *TownCatalog) validPoint(p TownPoint) bool {
	_, ok := t.areas[p.key()]
	return ok && p.Direction <= 7 && p.X <= math.MaxInt16 && p.Y <= math.MaxInt16
}
func (t *TownCatalog) IsRoom(p TownPoint) bool {
	_, ok := t.areas[p.key()]
	return ok && p.Town == 38 && p.Area == 1
}
func (q TownQuests) clear(id uint16) bool   { return q.Cleared[id] != 0 }
func (q TownQuests) started(id uint16) bool { _, ok := q.Active[id]; return ok || q.clear(id) }
func (p permission) allows(level byte, q TownQuests) bool {
	if p.Blocked || level < p.Level {
		return false
	}
	for _, id := range p.All {
		if !q.clear(id) {
			return false
		}
	}
	for _, id := range p.Except {
		if q.clear(id) {
			return false
		}
	}
	for _, id := range p.Started {
		if !q.started(id) {
			return false
		}
	}
	return len(p.Any) == 0 || slices.ContainsFunc(p.Any, q.clear)
}
func exempt(a townArea) bool {
	if a.Town == 38 && a.Area == 1 {
		return true
	}
	if a.Area != 0 {
		return false
	}
	switch a.Town {
	case 19, 32, 51, 57, 59, 65, 129, 67, 81, 119, 124, 83, 126, 93, 94, 85, 47, 98:
		return true
	}
	return false
}

// The last matching completed quest selects one phase. Otherwise all applicable
// native event variants must permit the action on this server.
func (t *TownCatalog) everyMap(a townArea, q TownQuests, includeEvents bool, test func(townMap) bool) bool {
	qualifies := func(path string) bool { m, ok := t.maps[strings.ToLower(path)]; return ok && test(m) }
	if exempt(a) {
		return qualifies(a.Path)
	}
	rules := t.data.Phases[a.Town]
	variant := func(index int) bool {
		if index < 0 || len(a.Phases) == 0 {
			return qualifies(a.Path)
		}
		return index < len(a.Phases) && qualifies(a.Phases[index])
	}
	for i := len(rules) - 1; i >= 0; i-- {
		r := rules[i]
		if r.Kind == "completed quest" && r.Index >= 0 && r.Id >= 0 && r.Id <= math.MaxUint16 && q.clear(uint16(r.Id)) {
			return variant(r.Index)
		}
	}
	if !qualifies(a.Path) {
		return false
	}
	if includeEvents {
		for _, r := range rules {
			if (r.Kind == "by event" || r.Kind == "depend townarea event") && !variant(r.Index) {
				return false
			}
		}
	}
	return true
}
func (t *TownCatalog) allows(level byte, q TownQuests, p TownPoint, motion bool) bool {
	if p.X > math.MaxInt16 || p.Y > math.MaxInt16 {
		return false
	}
	a, ok := t.areas[p.key()]
	if !ok {
		return false
	}
	perm := a.Permission
	if motion && p.Town == 82 && p.Area == 0 && q.clear(6423) && slices.Equal(perm.Except, []uint16{6423}) {
		perm.Except = nil
	}
	return perm.allows(level, q) && t.everyMap(a, q, true, func(townMap) bool { return true })
}
func (t *TownCatalog) AllowsMovement(level byte, q TownQuests, from, to TownPoint) bool {
	return from.key() == to.key() && t.allows(level, q, to, true)
}

// LoginPlacement returns the original room and its qualified regional exit. A nil room
// means the saved outdoor location does not qualify for the upstream room-placement policy.
func (t *TownCatalog) LoginPlacement(level byte, q TownQuests, saved TownPoint, previous *TownPoint) (home, exit *TownPoint) {
	if saved.Direction > 7 || !t.allows(level, q, saved, false) || !t.allows(level, q, t.data.Room, false) {
		return nil, nil
	}
	home = &t.data.Room
	origin := &saved
	if t.IsRoom(saved) || (saved.Town == 117 && saved.Area == 1) {
		origin = previous
	}
	if origin == nil {
		return home, nil
	}
	if origin.Direction > 7 || !t.allows(level, q, *origin, false) {
		return home, previous
	}
	var qualified []TownPoint
	for _, r := range t.data.RoomReturns {
		p := r.Point
		if r.Town != origin.Town || p.Direction > 7 || !t.allows(level, q, p, false) {
			continue
		}
		if p.Area == origin.Area {
			return home, &p
		}
		qualified = append(qualified, p)
	}
	if len(qualified) == 1 {
		return home, &qualified[0]
	}
	if t.allows(level, q, t.data.Exit, false) {
		return home, &t.data.Exit
	}
	return home, nil
}
