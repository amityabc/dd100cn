package dungeon

import (
	"math"
	"slices"
)

type GameMasterAPC struct {
	Actor          uint16
	Code           uint32
	Level, Faction byte
	X, Y           byte
	Map            uint32
	ResourceHash   string
}

func (r *Run) GameMasterReady() bool {
	if r == nil || r.ID == "" || !r.Ready || r.Room == nil || r.PlayerDeath != nil || r.Settlement.Confirmation != nil {
		return false
	}
	return r.seen[scene{cell{r.Room.X, r.Room.Y}, r.InLayer}]
}

func (r *Run) GameMasterTargets() []uint16 {
	if !r.GameMasterReady() || r.Cleared {
		return nil
	}
	room := r.CombatRoom()
	base := r.combatActorBase()
	var alive []int
	for i := range room.Monsters {
		if _, dead := r.Dead[base+uint16(i)]; !dead {
			alive = append(alive, i)
		}
	}
	slices.SortStableFunc(alive, func(a, b int) int {
		if room.Monsters[a].BlocksRoom == room.Monsters[b].BlocksRoom {
			return 0
		}
		if room.Monsters[a].BlocksRoom {
			return 1
		}
		return -1
	})
	out := make([]uint16, 0, len(alive))
	for _, i := range alive {
		out = append(out, base+uint16(i))
	}
	return out
}

func (r *Run) GameMasterDefeated(actor uint16) bool {
	_, dead := r.Dead[actor]
	return dead
}

func (r *Run) AddGameMasterAPC(actor uint16, code uint32, level, faction byte, hash string) (GameMasterAPC, bool) {
	if !r.GameMasterReady() || r.InLayer || len(r.GmApcs) >= 32 || actor < 402 || actor >= math.MaxUint16-1 ||
		code == 0 || level < 1 || level > 100 || faction > 2 || len(hash) != 64 {
		return GameMasterAPC{}, false
	}
	a := GameMasterAPC{actor, code, level, faction, r.Room.X, r.Room.Y, r.Room.MapID, hash}
	r.GmApcs = append(r.GmApcs, a)
	return a, true
}

func (r *Run) GameMasterFollowers() []GameMasterAPC {
	if !r.GameMasterReady() || r.InLayer {
		return nil
	}
	var out []GameMasterAPC
	for _, a := range r.GmApcs {
		if a.Faction != 0 || r.gmApcDead[a.Actor] || !r.gameMasterAPCInRoom(a) {
			continue
		}
		a.X, a.Y = r.Room.X, r.Room.Y
		out = append(out, a)
	}
	return out
}

func (r *Run) gameMasterAPCInRoom(a GameMasterAPC) bool {
	if !r.Ready || r.InLayer || r.Room == nil || !r.seen[scene{cell{r.Room.X, r.Room.Y}, false}] || !r.seen[scene{cell{a.X, a.Y}, false}] {
		return false
	}
	if a.X == r.Room.X && a.Y == r.Room.Y && a.Map == r.Room.MapID {
		return true
	}
	origin := r.rooms[cell{a.X, a.Y}]
	return a.Faction == 0 && origin != nil && origin.MapID == a.Map
}

func (r *Run) GameMasterAPCDeath(body []byte) (uint16, bool, bool) {
	if !r.GameMasterReady() || len(body) < 74 || len(body) != 74+14*int(body[30]) {
		return 0, false, false
	}
	raw := le.Uint32(body)
	if raw < 402 || raw >= math.MaxUint16 {
		return 0, false, false
	}
	actor := uint16(raw)
	for _, a := range r.GmApcs {
		if a.Actor != actor || !r.gameMasterAPCInRoom(a) {
			continue
		}
		if r.gmApcDead == nil {
			r.gmApcDead = make(map[uint16]bool)
		}
		previous := r.gmApcDead[actor]
		r.gmApcDead[actor] = true
		return actor, true, previous
	}
	return 0, false, false
}

func (r *Run) reportedCompanion(body []byte) uint16 {
	if le.Uint16(body[4:]) != math.MaxUint16 {
		return 0
	}
	participated := func(actor, kind uint16) bool {
		for i := range int(body[30]) {
			row := body[31+i*14:]
			if le.Uint16(row) == kind && le.Uint16(row[2:]) == actor && le.Uint64(row[4:]) > 0 && le.Uint16(row[12:]) > 0 {
				return true
			}
		}
		return false
	}
	for _, a := range r.GmApcs {
		if a.Faction == 0 && r.gameMasterAPCInRoom(a) && participated(a.Actor, 0xf111) {
			return a.Actor
		}
	}
	// Actor ranges were reserved in original room order. Keep that order
	// when a report contains two eligible participants in different rooms.
	var companion, friendly uint16
	for k, room := range r.rooms {
		if !r.seen[scene{k, false}] {
			continue
		}
		base := r.bases[k]
		for i, c := range room.Companions {
			known := c.Code == 65006 && c.ScriptIndex == 0 || c.Code == 2012 && c.ScriptIndex <= 1 || c.Code == 6114 && c.ScriptIndex == 0 || c.Code == 1521 && c.ScriptIndex == 0
			if c.Type != 5 || c.Level == 0 || !c.CreditsPlayerKills && !known || c.Code == 1521 && (r.Definition.Dungeon.ID != 7123 || room.MapID != 53546) {
				continue
			}
			id := base + uint16(len(room.Monsters)+i)
			if (companion == 0 || id < companion) && (participated(id, 0xf111) || c.CreditsPlayerKills && participated(id, 0x0111)) {
				companion = id
			}
		}
		for i, m := range room.Monsters {
			id := base + uint16(i)
			if !m.BlocksRoom && m.Type < 3 && !m.Dummy && m.AiScriptIndex == nil && uint32(id) != le.Uint32(body) && (friendly == 0 || id < friendly) && participated(id, 0x0211) {
				friendly = id
			}
		}
	}
	if companion != 0 {
		return companion
	}
	return friendly
}
