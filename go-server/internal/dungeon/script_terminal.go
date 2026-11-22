package dungeon

import (
	"math"
	"slices"

	"s9a1/server/internal/opening"
)

const NoireQuest uint16 = 3275

func (r *Run) forgottenForestRoom() bool {
	d, room := r.Definition.Dungeon, r.Room
	if d.ID != 7157 || d.MazeIndex != 0 || d.BossX != 6 || d.BossY != 0 ||
		!slices.Equal(d.QuestConnections, []uint16{3449}) || room.X != 6 || room.Y != 0 ||
		room.MapID != 70192 || len(room.MapAliases) != 0 || len(room.Monsters) != 4 || room.CinematicLayer == nil {
		return false
	}
	codes := [...]uint32{61251, 61254, 61253, 61252}
	for i, actor := range room.Monsters {
		if actor.Code != codes[i] || actor.Type != 0 || actor.Dummy || !actor.BlocksRoom || actor.AiScriptIndex != nil {
			return false
		}
	}
	layer := room.CinematicLayer
	var terminalObjects int
	for _, code := range layer.PassiveObjectCodes {
		if code == 69292 {
			terminalObjects++
		}
	}
	return layer.X == 6 && layer.Y == 0 && layer.MapID == 70350 && len(layer.Monsters) == 0 &&
		len(layer.Companions) == 0 && terminalObjects == 1
}

func (r *Run) forgottenForestClearVictim() (uint16, bool) {
	key := cell{r.Room.X, r.Room.Y}
	if !r.Cleared || !r.InLayer || !r.seen[scene{key, true}] || !r.forgottenForestRoom() {
		return 0, false
	}
	var victim uint16
	base := r.bases[key]
	for i := range r.Room.Monsters {
		death, ok := r.Dead[base+uint16(i)]
		if !ok {
			return 0, false
		}
		if death.Result.DungeonCleared {
			if victim != 0 {
				return 0, false
			}
			victim = base + uint16(i)
		}
	}
	return victim, victim != 0
}

func sharedLayerQuest(d Definition, room *opening.Room) uint16 {
	def, stage := d.Dungeon, room.CinematicLayer
	if stage == nil || len(stage.Monsters) != 1 || len(room.Monsters) == 0 ||
		len(def.QuestConnections) != 1 || stage.X != room.X || stage.Y != room.Y {
		return 0
	}
	m, projection := room.Monsters[0], stage.Monsters[0]
	if m.Type != 3 || !m.Dummy || !m.BlocksRoom || m.AiScriptIndex != nil {
		return 0
	}
	switch def.QuestConnections[0] {
	case NoireQuest:
		projection.Dummy = true
		if def.ID == 239 && def.MazeIndex == 2 && room.X == 5 && room.Y == 0 &&
			room.MapID == 70168 && stage.MapID == 70171 && slices.Equal(room.MapAliases, []uint32{70171}) &&
			len(room.Monsters) == 1 && m.Code == 70150 && !stage.Monsters[0].Dummy && m == projection &&
			len(stage.Companions) == 1 && stage.Companions[0].Code == 6517 && !stage.Companions[0].CreditsPlayerKills &&
			len(room.CinematicDestroys) == 1 && room.CinematicDestroys[0].Index == 0 &&
			room.CinematicDestroys[0].Movie == 426 && room.CinematicDestroys[0].TriggerIndex == nil &&
			isHash(room.CinematicDestroys[0].Hash) {
			return NoireQuest
		}
	case 6422:
		if def.ID == 100000194 && def.MazeIndex == 0 && room.X == 1 && room.Y == 0 &&
			room.MapID == 100000355 && stage.MapID == 100000442 && slices.Equal(room.MapAliases, []uint32{100000442}) &&
			m.Code == 107009972 && m.Level == 100 && m == projection && len(stage.Companions) == 0 {
			return 6422
		}
	}
	return 0
}

func (r *Run) awakeningPredecessors() ([]uint16, bool) {
	expected := [...]uint32{25437, 5449, 10456}
	if len(r.Room.Monsters) != 4 {
		return nil, false
	}
	base := r.bases[cell{r.Room.X, r.Room.Y}]
	actors := make([]uint16, 0, len(expected))
	for i, code := range expected {
		m := r.Room.Monsters[i+1]
		actor := base + uint16(i+1)
		row, dead := r.Dead[actor]
		if m.Code != code || m.AiScriptIndex == nil || int(*m.AiScriptIndex) != i ||
			!dead || row.Removal || row.Result.Actor != actor {
			return nil, false
		}
		actors = append(actors, actor)
	}
	return actors, true
}

// boundTerminal recognizes the resource-bound targets before ordinary
// attribution so that a malformed or premature report cannot kill one.
func (r *Run) boundTerminal(room *opening.Room, index int, m opening.Monster, body []byte, layer bool) (quest uint16, prerequisites []uint16, target bool) {
	d := r.Definition.Dungeon
	owner := le.Uint16(body[4:])
	valid := index == 0 && m.Type == 3 && m.BlocksRoom && body[68+14*int(body[30])] == 0
	destroy := len(body) == 74 && owner == math.MaxUint16 && body[68] == 7
	plain := len(body) == 74 && m.Dummy && m.AiScriptIndex == nil
	switch {
	case d.ID == 100000194 && m.Code == 107009972:
		if required, ok := r.awakeningPredecessors(); valid && ok && plain && owner == r.Character &&
			layer && sharedLayerQuest(r.Definition, room) == 6422 && !d.UsesOrdinaryRewards {
			return 6422, required, true
		}
	case d.ID == 100000325 && m.Code == 107009972:
		if index == 0 && m.Type == 3 && m.BlocksRoom && plain && destroy && m.Level == 100 &&
			d.MazeIndex == 0 && room.X == 0 && room.Y == 0 && room.MapID == 100001118 && len(room.MapAliases) == 0 &&
			!d.UsesOrdinaryRewards {
			return 12301, nil, true
		}
	case d.ID == 9 && m.Code == 107000909:
		if index == 0 && m.Type == 3 && m.BlocksRoom && plain && destroy && m.Level == 11 &&
			d.MazeIndex == 1 && room.X == 2 && room.Y == 1 && room.MapID == 76155 && len(room.MapAliases) == 0 {
			return 3148, nil, true
		}
	case d.ID == 26 && m.Code == 2004 && slices.Contains(d.QuestConnections, uint16(3213)):
		if valid && !m.Dummy && m.Level == 32 && m.AiScriptIndex != nil && *m.AiScriptIndex == 0 &&
			(owner == r.Character || owner == math.MaxUint16) && d.MazeIndex == 2 &&
			room.X == 3 && room.Y == 0 && room.MapID == 53529 && len(room.MapAliases) == 0 {
			return 3213, nil, true
		}
	default:
		return 0, nil, false
	}
	return 0, nil, true
}
