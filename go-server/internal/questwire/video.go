package questwire

import "s9a1/server/internal/storage"

// The upstream apparatus recognizes only these two story connections.
func (catalog *Catalog) VideoCall(c storage.Character) (uint16, error) {
	p, err := playerOf(c)
	if err != nil {
		return 0, err
	}
	for _, pair := range [...]struct {
		id, previous uint16
		npc          int
	}{{3734, 3733, 2000}, {3741, 3740, 2001}} {
		q := catalog.ordinary[pair.id]
		a, ok := p.hist.Active[pair.id]
		if !ok || q == nil || !q.Supported || q.Operations == nil || !q.Operations.Available || q.Operations.Grade != "epic" || q.Kind != meet || q.Subtype != -1 || q.Parent != 0 || q.AwakeningAward != nil || len(q.Materials) != 0 || len(q.Rewards) != 0 || len(q.Children) != 0 || len(q.DestinationDungeons) != 0 {
			continue
		}
		if len(q.Data) == 1 && q.Data[0] == pair.npc && len(q.Required) == 1 && len(q.Required[0]) == 1 && q.Required[0][0] == pair.previous && len(a.Remaining) > 0 && (a.Remaining[0] != 0 || a.NpcMet) {
			return pair.id, nil
		}
	}
	return 0, nil
}

func VideoMet(c storage.Character, id uint16) bool {
	p, err := playerOf(c)
	if err != nil {
		return false
	}
	a, ok := p.hist.Active[id]
	return ok && len(a.Remaining) != 0 && a.Remaining[0] == 0
}
