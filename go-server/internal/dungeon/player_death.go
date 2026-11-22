package dungeon

import (
	"encoding/json"
	"time"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/storage"
)

type nativeDeathRoom struct {
	X, Y               byte
	MapId              uint32
	Monsters           []oculusMonsterShape
	Gates              byte
	ScriptTransfer     any
	StormCleanup       any
	StormArrival       any
	CinematicDestroys  []opening.CinematicDestroy
	CinematicLayer     *nativeDeathRoom
	MapAliases         []uint32
	PassiveObjectCodes []uint32
	NpcTemplates       []uint32
	Companions         []opening.Companion
}

func nativeDeathRoomValue(room *opening.Room) *nativeDeathRoom {
	if room == nil {
		return nil
	}
	frozen := freezeOculusRoom(room)
	row := &nativeDeathRoom{X: room.X, Y: room.Y, MapId: room.MapID, Monsters: frozen.Monsters, Gates: room.Gates,
		CinematicDestroys: append([]opening.CinematicDestroy{}, room.CinematicDestroys...),
		CinematicLayer:    nativeDeathRoomValue(room.CinematicLayer), MapAliases: frozen.MapAliases,
		PassiveObjectCodes: frozen.PassiveObjectCodes, NpcTemplates: frozen.NpcTemplates, Companions: frozen.Companions}
	if t := room.ScriptTransfer; t != nil {
		row.ScriptTransfer = struct {
			X, Y           byte
			Map            uint32
			Hash           string
			RequiredDeaths []uint16
		}{t.X, t.Y, t.Map, t.Hash, ordinalValues(t.RequiredDeaths)}
	}
	if c := room.StormCleanup; c != nil {
		row.StormCleanup = struct {
			Index         byte
			Prerequisites []uint16
			Hash          string
		}{c.Index, ordinalValues(c.Prerequisites), c.Hash}
	}
	if a := room.StormArrival; a != nil {
		row.StormArrival = struct {
			Boss    byte
			Targets []uint16
			Hash    string
		}{a.Boss, ordinalValues(a.Targets), a.Hash}
	}
	return row
}

func ordinalValues(values []byte) []uint16 {
	out := make([]uint16, len(values))
	for i, v := range values {
		out[i] = uint16(v)
	}
	return out
}

func (r *Run) playerDeathInput(room *opening.Room, cycle uint32, x, y, related uint16) (string, error) {
	if room == nil {
		return "", storage.ErrInvalid
	}
	raw, err := json.Marshal(struct {
		Version            byte
		RunId              string
		CharacterId        uint16
		Room               *nativeDeathRoom
		DeathCycle         uint32
		X, Y, RelatedActor uint16
	}{1, r.ID, r.Character, nativeDeathRoomValue(room), cycle, x, y, related})
	return string(raw), err
}

func (r *Run) PlayerDeathInput(body []byte, cycle uint32) (string, error) {
	if len(body) != 6 {
		return "", storage.ErrInvalid
	}
	return r.playerDeathInput(r.Room, cycle, le.Uint16(body), le.Uint16(body[2:]), le.Uint16(body[4:]))
}

func (r *Run) CommitPlayerDeath(body []byte, input string, occurred time.Time) {
	r.DeathCycle++
	r.LastRevived = false
	r.PlayerDeath = &playerDeath{le.Uint16(body), le.Uint16(body[2:]), le.Uint16(body[4:]), occurred, input}
	r.Settlement.Pending = nil
	clear(r.Settlement.pendingByActor)
}

func (r *Run) CurrentPlayerDeathInput() string {
	if r.PlayerDeath == nil {
		return ""
	}
	return r.PlayerDeath.input
}
