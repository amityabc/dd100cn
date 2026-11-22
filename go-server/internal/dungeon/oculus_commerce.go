package dungeon

import "slices"

// OculusRescue derives the single resource-bound rescue from the loaded
// challenge. A resumed scene can retain the original committed death key.
func (r *Run) OculusRescue(mode OculusMode) (OculusPosition, string, bool) {
	if r == nil || r.Room == nil || !r.Ready || r.Cleared || r.InLayer || r.PlayerDeath != nil ||
		r.OculusEntry == nil || r.OculusLayout == nil || r.OculusGeneration == 0 || r.OculusLayout.mode.SourceHash != mode.SourceHash ||
		!r.visited[cell{r.Room.X, r.Room.Y}] || r.rooms[cell{r.Room.X, r.Room.Y}] != r.Room {
		return OculusPosition{}, "", false
	}
	rescue := mode.Rules.ShopOpenMonsterId
	kaiju := map[uint32]bool{}
	for _, k := range mode.Rules.Kaiju {
		if k.Id == 0 || k.Id > 0x7fffffff || k.Id < rescue || kaiju[k.Id] {
			return OculusPosition{}, "", false
		}
		kaiju[k.Id] = true
	}
	if !kaiju[rescue] {
		return OculusPosition{}, "", false
	}
	cellIndex := slices.IndexFunc(r.OculusLayout.cells, func(c oculusCell) bool { return c.Monster == rescue })
	if cellIndex < 0 || slices.ContainsFunc(r.OculusLayout.cells[cellIndex+1:], func(c oculusCell) bool { return c.Monster == rescue }) {
		return OculusPosition{}, "", false
	}
	c := r.OculusLayout.cells[cellIndex]
	key, ok := r.oculusDead(c)
	if !ok || c.Position.Floor == oculusFloor(r.Room.X, r.Room.Y) && (c.Position.X != r.Room.X || c.Position.Y != r.Room.Y) {
		return OculusPosition{}, "", false
	}
	return c.Position, key, true
}
