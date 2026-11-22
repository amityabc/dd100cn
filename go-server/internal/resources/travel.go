package resources

import (
	"math"
	"slices"
)

type seriaWarp struct {
	Town, Area, X, Y, Width, Height uint16
	Kind, MinimumLevel              byte
	RequiredQuest                   *uint16
}

type npcWarp struct {
	Source, Target       areaKey
	SourceNpc, TargetNpc uint32
	SourceX, SourceY     uint16
	Destination          TownPoint
	Entering             bool
}

type transportDestination struct {
	Region, Phase int
	Point         TownPoint
	Level         byte
	Quests        []uint16
}

type transportMenus struct {
	Dock, Board []transportDestination
	Phases      []struct {
		Index, Count int
		Quest        uint16
	}
	OpenQuest uint16
}

type FateTownRoute struct {
	Camp, Seria       TownPoint
	CampNpc, EntryNpc uint32
	Gate              uint16
	EntryQuest        uint16
}

func (t *TownCatalog) roomSource(k areaKey) bool {
	return k == (areaKey{117, 1}) || slices.ContainsFunc(t.data.RoomReturns, func(r roomReturn) bool {
		return k == (areaKey{r.Town, r.Area})
	})
}

func (t *TownCatalog) RecordsRoomReturnFrom(p TownPoint) bool { return t.roomSource(p.key()) }

func (t *TownCatalog) neutralPosition(level byte, p TownPoint) bool {
	a, ok := t.areas[p.key()]
	if !ok {
		return false
	}
	v := a.Permission
	return len(v.All)+len(v.Any)+len(v.Except)+len(v.Started) == 0 && t.allows(level, TownQuests{}, p, false)
}

func (c *CharacterCatalog) RoomExit(level byte, q TownQuests, room TownPoint, saved *TownPoint) (*TownPoint, bool) {
	t := c.Towns
	if !t.IsRoom(room) {
		return nil, false
	}
	if saved != nil {
		return saved, !t.IsRoom(*saved) && saved.Direction <= 7 && c.AllowsTownPosition(level, q, *saved)
	}
	return &t.data.Exit, t.neutralPosition(level, TownPoint{Town: room.Town, Area: room.Area}) && t.neutralPosition(level, t.data.Exit)
}

func (c *CharacterCatalog) AllowsTownPosition(level byte, q TownQuests, p TownPoint) bool {
	return c.Towns.allows(level, q, p, false) || c.inFateCamp(level, p)
}

func (c *CharacterCatalog) AllowsOrdinaryTownPosition(level byte, q TownQuests, p TownPoint) bool {
	return !c.inFateCamp(level, p) && c.Towns.allows(level, q, p, false)
}

func (c *CharacterCatalog) inFateCamp(level byte, p TownPoint) bool {
	f := c.Fate
	return f != nil && p.key() == f.Town.Camp.key() && level >= f.MinimumLevel && p.X <= math.MaxInt16 && p.Y <= math.MaxInt16
}

func (c *CharacterCatalog) AllowsTownMovement(level byte, q TownQuests, from, to TownPoint) bool {
	return from.key() == to.key() && (c.inFateCamp(level, to) || c.Towns.AllowsMovement(level, q, from, to))
}

func (c *CharacterCatalog) AllowsTravel(level byte, q TownQuests, from, to TownPoint, kind byte, npc uint32, native bool) bool {
	t := c.Towns
	if from.Direction > 7 || to.Direction > 7 {
		return false
	}
	if native {
		f := c.Fate
		if f == nil || level < f.MinimumLevel {
			return false
		}
		r := f.Town
		if npc == 0 {
			return from.key() == r.Camp.key() && to.key() == r.Seria.key() && to.Direction == r.Seria.Direction && t.allows(level, q, to, false)
		}
		return q.clear(r.EntryQuest) && from.key() == r.Seria.key() && to == r.Camp && npc == r.CampNpc && t.neutralPosition(level, from)
	}
	if npc != 0 {
		return t.episodeTravel(level, q, from, to, npc)
	}
	if !t.allows(level, q, from, false) || !t.allows(level, q, to, false) {
		return false
	}
	if kind != 0 {
		return t.IsRoom(from) && slices.ContainsFunc(t.data.Warps, func(w seriaWarp) bool {
			return w.Kind == kind && w.Town == to.Town && w.Area == to.Area && level >= w.MinimumLevel && (w.RequiredQuest == nil || q.clear(*w.RequiredQuest)) &&
				to.X >= w.X && uint32(to.X) < uint32(w.X)+uint32(w.Width) && to.Y >= w.Y && uint32(to.Y) < uint32(w.Y)+uint32(w.Height)
		})
	}
	if from.key() == to.key() {
		return false
	}
	a := t.areas[from.key()]
	known := !slices.ContainsFunc(t.data.Phases[from.Town], func(r phaseRule) bool {
		return r.Kind != "completed quest" && r.Kind != "by event" && r.Kind != "depend townarea event"
	})
	if (known || exempt(a)) && (!t.IsRoom(to) || t.roomSource(from.key())) && t.everyMap(a, q, true, func(m townMap) bool {
		return slices.ContainsFunc(m.Routes, func(r struct {
			Town, Area uint16
			Permission permission
		}) bool {
			return r.Town == to.Town && r.Area == to.Area && r.Permission.allows(level, q)
		})
	}) {
		return true
	}
	if !known {
		return false
	}
	menu := t.data.Menus
	qualified := func(r transportDestination) bool {
		return r.Point == to && level >= r.Level && !slices.ContainsFunc(r.Quests, func(id uint16) bool { return !q.clear(id) })
	}
	if from.key() == (areaKey{76, 0}) && t.everyMap(a, q, true, func(m townMap) bool { return m.AirshipRoomMenu }) {
		if to == (TownPoint{38, 1, 450, 210, 5}) && t.roomSource(from.key()) || to.Direction == 5 && slices.ContainsFunc(menu.Board, qualified) {
			return true
		}
	}
	if to.Direction != 5 || menu.OpenQuest == 0 || !q.clear(menu.OpenQuest) || !t.everyMap(a, q, true, func(m townMap) bool { return m.DockMenu }) {
		return false
	}
	index, maxRegion := 0, math.MaxInt
	for _, phase := range menu.Phases {
		if q.clear(phase.Quest) {
			index, maxRegion = phase.Index, phase.Count
		}
	}
	byRegion := make(map[int]transportDestination)
	for _, r := range menu.Dock {
		prev, ok := byRegion[r.Region]
		if r.Region <= maxRegion && r.Phase <= index && (!ok || r.Phase > prev.Phase) {
			byRegion[r.Region] = r
		}
	}
	for _, r := range byRegion {
		if qualified(r) {
			return true
		}
	}
	return false
}

func (t *TownCatalog) episodeTravel(level byte, q TownQuests, from, to TownPoint, npc uint32) bool {
	for _, w := range t.data.NpcWarps {
		if w.Source != from.key() || w.TargetNpc != npc {
			continue
		}
		alternate := !w.Entering && w.SourceX == 383 && w.SourceY == 210 && w.TargetNpc == 352 && w.Source == (areaKey{82, 0}) &&
			w.Destination == (TownPoint{22, 4, 657, 242, 5}) && to == (TownPoint{22, 4, 657, 254, 5})
		if to != w.Destination && !alternate || !t.allows(level, q, to, false) || from.X > math.MaxInt16 || from.Y > math.MaxInt16 || w.Destination.Y < 30 {
			return false
		}
		if w.Entering {
			if !t.allows(level, q, from, false) || !q.started(6420) || q.clear(6423) {
				return false
			}
		} else if level < 95 || !q.started(6420) && !q.clear(6423) {
			return false
		}
		at := func(k areaKey, id uint32, x, y uint16) bool {
			a, ok := t.areas[k]
			return ok && t.everyMap(a, q, true, func(m townMap) bool {
				return slices.Contains(m.Npcs, npcPlacement{id, x, y})
			})
		}
		return at(w.Source, w.SourceNpc, w.SourceX, w.SourceY) && at(w.Target, w.TargetNpc, w.Destination.X, w.Destination.Y-30)
	}
	return false
}
