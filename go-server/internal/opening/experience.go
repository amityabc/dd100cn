package opening

import "s9a1/server/internal/storage"

// ApplyExperience shares resource-backed level growth and skill-point
// projection between the opening and ordinary dungeon flows.
func (cat *CombatCatalog) ApplyExperience(c storage.Character, award uint32) (storage.Character, uint32, error) {
	next, _, actual, err := cat.apply(c, award)
	return next, actual, err
}
