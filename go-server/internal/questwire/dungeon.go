package questwire

import (
	"encoding/json"
	"slices"
	"strconv"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/storage"
)

func (c *Catalog) DungeonCandidates(character storage.Character, dungeon uint32) ([]uint16, error) {
	return c.DungeonCandidatesFor(character, dungeon, nil)
}

func (c *Catalog) DungeonCandidatesFor(character storage.Character, dungeon uint32, extra []uint16) ([]uint16, error) {
	p, err := playerOf(character)
	if err != nil {
		return nil, err
	}
	var ids []uint16
	for id, a := range p.hist.Active {
		q := c.ordinary[id]
		if q != nil && (slices.Contains(q.DestinationDungeons, int(dungeon)) || slices.Contains(extra, id)) &&
			c.ordinaryAvailable(p, q) && validActive(q, a) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (c *Catalog) DungeonQualification(character storage.Character) (initialized bool, active map[uint16]uint16, cleared map[uint16]byte, err error) {
	p, err := playerOf(character)
	if err != nil {
		return false, nil, nil, err
	}
	active = make(map[uint16]uint16, len(p.hist.Active))
	for id, a := range p.hist.Active {
		q := c.ordinary[id]
		if q != nil && c.ordinaryAvailable(p, q) && validActive(q, a) {
			active[id] = q.Parent
		}
	}
	return p.initialized, active, p.hist.Cleared, nil
}

func (c *Catalog) ActiveDungeonQuestProof(ch storage.Character, dungeon uint32, connections []uint16) (uint16, string, error) {
	p, err := playerOf(ch)
	if err != nil {
		return 0, "", err
	}
	ids := slices.Clone(connections)
	slices.Sort(ids)
	for _, id := range ids {
		q := c.ordinary[id]
		a, active := p.hist.Active[id]
		if q != nil && active && slices.Contains(q.DestinationDungeons, int(dungeon)) && c.ordinaryAvailable(p, q) && validActive(q, a) {
			return id, q.Hash, nil
		}
	}
	return 0, "", nil
}

func (c *Catalog) ActiveDungeonScriptQuestProof(ch storage.Character, dungeon uint32, connections []uint16, id uint16) (uint16, string, error) {
	q := c.ordinary[id]
	if q == nil || !slices.Contains(connections, id) {
		return 0, "", nil
	}
	var target int
	switch id {
	case 3275:
		if q.Parent != 0 || q.Kind != huntMonster || !slices.Equal(q.Data, []int{239, -1, 70150, 1}) {
			return 0, "", nil
		}
	case 3148:
		target = 76156
	case 3213:
		target = 53529
	case 6422:
		target = 100000442
	case 12176:
		target = 100000656
	case 12301:
		target = 100001118
	default:
		return 0, "", nil
	}
	if target != 0 && (q.Kind != clearMap || !slices.Equal(q.Data, []int{target})) {
		return 0, "", nil
	}
	p, err := playerOf(ch)
	if err != nil || p.hist.Cleared[id] != 0 {
		return 0, "", err
	}
	return c.ActiveDungeonQuestProof(ch, dungeon, []uint16{id})
}

type DungeonEventKind uint8

const (
	DungeonMonsterKilled DungeonEventKind = iota
	DungeonRoomCleared
	DungeonCleared
)

type DungeonEvent struct {
	Key                     string
	Kind                    DungeonEventKind
	Dungeon, Monster, Map   uint32
	MonsterKind, Difficulty byte
}

func (c *Catalog) AdvanceDungeon(character storage.Character, events []DungeonEvent) (storage.Character, bool, error) {
	var root, quests map[string]json.RawMessage
	var rows map[uint16]json.RawMessage
	var state struct{ Quests history }
	if err := json.Unmarshal(character.Gameplay, &state); err != nil {
		return character, false, err
	}
	changed := false
	for id, a := range state.Quests.Active {
		q := c.ordinary[id]
		if q == nil || !validActive(q, a) {
			continue
		}
		updated := false
		for _, v := range events {
			if !slices.Contains(a.Receipts, v.Key) && advanceObjective(q, &a, v) {
				a.Receipts = append(a.Receipts, v.Key)
				updated = true
			}
		}
		if !updated {
			continue
		}
		if !changed {
			if err := json.Unmarshal(character.Gameplay, &root); err != nil {
				return character, false, err
			}
			if err := json.Unmarshal(root["Quests"], &quests); err != nil {
				return character, false, err
			}
			if err := json.Unmarshal(quests["Active"], &rows); err != nil {
				return character, false, err
			}
			changed = true
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(rows[id], &entry); err != nil {
			return character, false, err
		}
		entry["Remaining"], _ = json.Marshal(a.Remaining)
		entry["Receipts"], _ = json.Marshal(a.Receipts)
		rows[id], _ = json.Marshal(entry)
	}
	if changed {
		quests["Active"], _ = json.Marshal(rows)
		root["Quests"], _ = json.Marshal(quests)
		character.Gameplay, _ = json.Marshal(root)
	}
	return character, changed, nil
}

func advanceObjective(q *Quest, a *active, e DungeonEvent) bool {
	switch {
	case (q.Kind == huntEnemy || q.Kind == huntMonster) && e.Kind == DungeonMonsterKilled:
		if int(e.Monster) < 1 {
			return false
		}
		stride := 4
		if q.Kind == huntEnemy {
			stride = 5
		}
		changed := false
		for i := range a.Remaining {
			row := q.Data[i*stride : (i+1)*stride]
			kind := 1
			if q.Kind == huntEnemy {
				kind = row[3]
			}
			if a.Remaining[i] != 0 && (row[0] == -1 || row[0] == int(e.Dungeon)) && row[1] <= int(e.Difficulty) &&
				(row[2] == int(e.Monster) || q.Kind == huntMonster && row[2] == -5 && e.MonsterKind == 1) && kind == int(e.MonsterKind) {
				a.Remaining[i]--
				changed = true
			}
		}
		return changed
	case q.Kind == clearMap && e.Kind == DungeonRoomCleared && len(q.Data) > 0 && q.Data[0] == int(e.Map) &&
		(len(q.DestinationDungeons) == 0 || slices.Contains(q.DestinationDungeons, int(e.Dungeon))):
		if a.Remaining[0] != 0 {
			a.Remaining[0] = 0
			return true
		}
	case q.Kind == clearCondition && len(q.Data) >= 2 && q.Data[0] == int(e.Dungeon) && q.Data[1] <= int(e.Difficulty):
		required := DungeonCleared
		if q.Subtype == 11 && len(q.Data) >= 3 && q.Data[2] > 1 {
			required = DungeonRoomCleared
		}
		if a.Remaining[0] != 0 && e.Kind == required {
			a.Remaining[0]--
			return true
		}
	}
	return false
}

func DungeonRoomEventKey(key string, id uint32) string {
	return key + ":" + strconv.FormatUint(uint64(id), 10)
}

func (c *Catalog) HasDungeonHuntTarget(ch storage.Character, dungeon uint32, difficulty byte, monsters []opening.Monster) (bool, error) {
	p, err := playerOf(ch)
	if err != nil {
		return false, err
	}
	for id, a := range p.hist.Active {
		q := c.ordinary[id]
		if q == nil || q.Kind != huntMonster || !slices.Contains(q.DestinationDungeons, int(dungeon)) || !validActive(q, a) {
			continue
		}
		for i, left := range a.Remaining {
			row := q.Data[i*4 : i*4+4]
			if left == 0 || row[0] != int(dungeon) || row[1] != -1 && row[1] != int(difficulty) {
				continue
			}
			if slices.ContainsFunc(monsters, func(m opening.Monster) bool { return int(m.Code) == row[2] }) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (c *Catalog) OwnsPassiveObjective(ch storage.Character, dungeon uint32, difficulty byte, code uint32,
	connections []uint16, duplicate bool) (bool, error) {
	p, err := playerOf(ch)
	if err != nil {
		return false, err
	}
	for id, a := range p.hist.Active {
		q := c.ordinary[id]
		if q == nil || q.Kind != huntEnemy || !slices.Contains(q.DestinationDungeons, int(dungeon)) || !validActive(q, a) {
			continue
		}
		if q.Parent == 0 {
			if (!q.TownPassive && !q.DungeonPassive) || !slices.Contains(connections, id) {
				continue
			}
		} else {
			parent := c.ordinary[q.Parent]
			active, ok := p.hist.Active[q.Parent]
			if duplicate || !slices.Contains(connections, q.Parent) || !ok || parent == nil ||
				parent.Kind != questClear || parent.Subtype != 2 || !slices.Contains(parent.Children, id) || !validActive(parent, active) {
				continue
			}
		}
		for i, left := range a.Remaining {
			row := q.Data[i*5 : i*5+5]
			if left != 0 && row[0] == int(dungeon) && (row[1] == -1 || row[1] == int(difficulty)) && row[2] == int(code) && row[3] == 3 {
				return true, nil
			}
		}
	}
	return false, nil
}

func (c *Catalog) PendingDungeonRoomObjective(ch storage.Character, dungeon uint32, room *opening.Room) (bool, error) {
	p, err := playerOf(ch)
	if err != nil {
		return false, err
	}
	for id, a := range p.hist.Active {
		q := c.ordinary[id]
		if q == nil || !validActive(q, a) {
			continue
		}
		switch q.Kind {
		case huntMonster, huntEnemy:
			stride := 4
			if q.Kind == huntEnemy {
				stride = 5
			}
			for i, left := range a.Remaining {
				row := q.Data[i*stride : (i+1)*stride]
				if left == 0 || row[0] != -1 && row[0] != int(dungeon) {
					continue
				}
				code := uint32(row[2])
				if q.Kind == huntEnemy && row[3] == 3 {
					if slices.Contains(room.PassiveObjectCodes, code) {
						return true, nil
					}
				} else if slices.ContainsFunc(room.Monsters, func(m opening.Monster) bool { return m.Code == code }) {
					return true, nil
				}
			}
		case clearMap:
			if a.Remaining[0] != 0 && len(q.Data) > 0 && (uint32(q.Data[0]) == room.MapID || slices.Contains(room.MapAliases, uint32(q.Data[0]))) {
				return true, nil
			}
		case clearCondition:
			if a.Remaining[0] != 0 && len(q.Data) > 0 && q.Data[0] == int(dungeon) {
				return true, nil
			}
		}
	}
	return false, nil
}
