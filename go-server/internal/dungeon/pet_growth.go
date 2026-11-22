package dungeon

import (
	"s9a1/server/internal/storage"
	"time"
)

func (r *Run) PetGrowthProof(death Death, z DeathResult, rule *storage.PetGrowthRule, now time.Time) *storage.DungeonPetGrowth {
	if rule == nil || death.Prior != nil || !z.RoomCleared || !r.Ready || r.InLayer || r.PlayerDeath != nil || r.Room == nil ||
		r.clearedRooms[cell{r.Room.X, r.Room.Y}] || r.TournamentReward() != 0 || !r.Definition.Dungeon.UsesOrdinaryRewards {
		return nil
	}
	p := &storage.DungeonPetGrowth{Rule: rule, Now: now}
	base := r.combatActorBase()
	for i, member := range r.Room.Monsters {
		actor := base + uint16(i)
		if actor == death.Actor || !member.BlocksRoom {
			continue
		}
		prior, ok := r.Dead[actor]
		if !ok {
			return nil
		}
		p.BlockingDeaths = append(p.BlockingDeaths, storage.PetGrowthDeath{Actor: actor, Key: prior.Key})
	}
	return p
}
