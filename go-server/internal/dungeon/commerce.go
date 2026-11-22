package dungeon

import "slices"

func (s *Session) Selecting() bool {
	return s.gate != 0 && !s.Active() && !s.Returning()
}

func (r *Run) ShopRoomReady(npc uint32) bool {
	if r == nil || r.Room == nil || r.ID == "" || !r.Ready || !r.Cleared || r.InLayer || r.PlayerDeath != nil ||
		r.Definition.Dungeon.ResourceHash == "" {
		return false
	}
	key := cell{r.Room.X, r.Room.Y}
	if !r.visited[key] || !r.clearedRooms[key] {
		return false
	}
	if slices.Contains(r.Room.NpcTemplates, npc) {
		return true
	}
	d := r.Definition.Dungeon
	return npc == 1000 && d.ID == 13 && d.MazeIndex == 2 && slices.Equal(d.QuestConnections, []uint16{3170}) &&
		r.Room.MapID == 57918 && r.Room.X == d.BossX && r.Room.Y == d.BossY
}
