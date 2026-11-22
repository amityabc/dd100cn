package questwire

import (
	"slices"

	"s9a1/server/internal/storage"
)

func (c *Catalog) HasActiveDungeonCinematicQuest(character storage.Character, dungeon uint32) (bool, error) {
	p, err := playerOf(character)
	if err != nil {
		return false, err
	}
	for id := range p.hist.Active {
		q := c.ordinary[id]
		if q != nil && q.DungeonCinematicTarget != nil && slices.Contains(q.DestinationDungeons, int(dungeon)) {
			return true, nil
		}
	}
	return false, nil
}
