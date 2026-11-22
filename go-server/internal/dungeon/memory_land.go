package dungeon

import (
	"slices"

	"s9a1/server/internal/opening"
)

var memoryLandPairs = [...]struct{ named, boss uint32 }{
	{107002218, 107002219},
	{107002220, 107002221},
	{107002222, 107002223},
}

func memoryLandBoss(code uint32) bool {
	for _, pair := range memoryLandPairs {
		if pair.boss == code {
			return true
		}
	}
	return false
}

func (r *Run) memoryLandBossRoom(room *opening.Room) (*opening.Room, bool) {
	def := r.Definition.Dungeon
	if def.ID != 100000177 || room.X != def.BossX || room.Y != def.BossY ||
		!slices.ContainsFunc(room.Monsters, func(m opening.Monster) bool { return memoryLandBoss(m.Code) }) {
		return room, true
	}
	k := cell{room.X, room.Y}
	var selected uint32
	for _, pair := range memoryLandPairs {
		defeated := false
		for other, candidate := range r.rooms {
			if k == other {
				continue
			}
			base := r.bases[other]
			for i, mon := range candidate.Monsters {
				_, dead := r.Dead[base+uint16(i)]
				if dead && mon.Code == pair.named {
					defeated = true
					break
				}
			}
			if defeated {
				break
			}
		}
		if !defeated {
			if selected != 0 {
				return nil, false
			}
			selected = pair.boss
		}
	}
	if selected == 0 {
		return nil, false
	}
	index := slices.IndexFunc(room.Monsters, func(m opening.Monster) bool {
		return m.Code == selected && m.Type == 3 && !m.Dummy
	})
	if index < 0 {
		return nil, false
	}
	_, dead := r.Dead[r.bases[k]+uint16(index)]
	out := *room
	out.Monsters = slices.Clone(room.Monsters)
	for i, m := range out.Monsters {
		if m.Type == 3 && memoryLandBoss(m.Code) && (m.Code != selected || dead) {
			out.Monsters[i] = opening.Monster{Level: 1, Dummy: true}
		}
	}
	return &out, true
}
