package dungeon

import (
	"math"
	"slices"

	"s9a1/server/internal/opening"
)

const WisdomStoryQuest uint16 = 12176

func (r *Run) wisdomStoryRoom() bool {
	def, room := r.Definition.Dungeon, r.Room
	if def.ID != 100000197 || def.MazeIndex != 0 || !slices.Equal(def.QuestConnections, []uint16{WisdomStoryQuest}) ||
		room.MapID != 100000485 || room.X != 2 || room.Y != 0 || len(room.Monsters) != 1 || len(room.MapAliases) != 0 {
		return false
	}
	mon, scene := room.Monsters[0], room.CinematicLayer
	if mon.Code != 109011083 || mon.Level != 135 || mon.Type != 0 || mon.Dummy || !mon.BlocksRoom ||
		scene == nil || scene.MapID != 100000656 || scene.X != 2 || scene.Y != 0 || len(scene.Monsters) != 1 || len(scene.Companions) != 0 {
		return false
	}
	terminal := scene.Monsters[0]
	return terminal.Code == 107002770 && terminal.Level == 135 && terminal.Type == 3 &&
		terminal.Dummy && terminal.BlocksRoom && terminal.AiScriptIndex == nil
}

func (r *Run) wisdomStoryLayerLoaded() bool {
	k := cell{r.Room.X, r.Room.Y}
	return r.InLayer && r.wisdomStoryRoom() && r.seen[scene{k, true}]
}

// CombatRoom tracks the layer's terminal after a completed battle so that
// reports and settlement use the same stage and actor identity as the client.
func (r *Run) CombatRoom() *opening.Room {
	if r.wisdomStoryRoom() && r.seen[scene{cell{r.Room.X, r.Room.Y}, true}] && (r.InLayer || r.Cleared) {
		return r.Room.CinematicLayer
	}
	return r.Room
}

func (r *Run) combatActorBase() uint16 {
	base := r.bases[cell{r.Room.X, r.Room.Y}]
	if r.CombatRoom() != r.Room {
		base += uint16(len(r.Room.Monsters) + len(r.Room.Companions))
	}
	return base
}

func (r *Run) wisdomBattleProof() (uint16, string, bool) {
	actor := r.bases[cell{r.Room.X, r.Room.Y}]
	death, ok := r.Dead[actor]
	if !ok || !death.Result.RoomCleared || death.Result.DungeonCleared || !slices.Equal(death.Result.Maps, []uint32{100000485}) {
		return 0, "", false
	}
	return actor, death.Key, true
}

func (r *Run) wisdomTerminal(body []byte, owner uint16, index int) (uint16, bool) {
	if !r.wisdomStoryLayerLoaded() || index != 0 || len(body) != 74 ||
		owner != r.Character && owner != math.MaxUint16 || body[68] != 0 && body[68] != 7 {
		return 0, false
	}
	actor, _, ok := r.wisdomBattleProof()
	return actor, ok
}

func (r *Run) PendingWisdomMove() (x, y byte, sequence uint16, ok bool) {
	if r.pendingWisdomLayer == nil || r.InLayer || !r.Ready || !r.wisdomStoryRoom() {
		return 0, 0, 0, false
	}
	if _, _, ok := r.wisdomBattleProof(); !ok {
		return 0, 0, 0, false
	}
	return r.Room.X, r.Room.Y, *r.pendingWisdomLayer, true
}
