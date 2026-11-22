package dungeon

import (
	"encoding/json"
	"slices"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/storage"
)

func (r *Run) SpecialConfirmationTarget(actor uint16) (*storage.DungeonObjectProof, bool) {
	if r == nil || r.Room == nil || !r.Ready || r.PlayerDeath != nil ||
		!r.seen[scene{cell{r.Room.X, r.Room.Y}, r.InLayer}] {
		return nil, false
	}
	room := r.CombatRoom()
	if actor == 0 {
		return nil, !slices.ContainsFunc(room.Monsters, func(m opening.Monster) bool { return m.BlocksRoom })
	}
	index := int(actor) - int(r.combatActorBase())
	if actor == ^uint16(0) || index < 0 || index >= len(room.Monsters) || room.Monsters[index].Type > 3 {
		return nil, false
	}
	record, dead := r.Dead[actor]
	if !dead {
		return nil, true
	}
	return combatObjectProof(actor, record)
}

func combatObjectProof(actor uint16, record deathRecord) (*storage.DungeonObjectProof, bool) {
	if record.Key == "" || record.Input == "" {
		return nil, false
	}
	result, err := json.Marshal(record.Result)
	if err != nil {
		return nil, false
	}
	return &storage.DungeonObjectProof{Key: record.Key, Input: record.Input, Result: result, Actor: actor}, true
}

func (r *Run) WisdomClearProof() ([]storage.DungeonObjectProof, bool) {
	if r == nil || r.WisdomEntry == nil || r.Room == nil || !r.Ready || !r.Cleared || r.PlayerDeath != nil {
		return nil, false
	}
	room := r.CombatRoom()
	base := r.combatActorBase()
	var deaths []storage.DungeonObjectProof
	boss, clear := false, false
	for i, m := range room.Monsters {
		if !m.BlocksRoom {
			continue
		}
		actor := base + uint16(i)
		rec, dead := r.Dead[actor]
		if !dead {
			return nil, false
		}
		proof, ok := combatObjectProof(actor, rec)
		if !ok {
			return nil, false
		}
		deaths = append(deaths, *proof)
		boss = boss || m.HellPhase == 3
		clear = clear || rec.Result.DungeonCleared
	}
	return deaths, boss && clear
}
