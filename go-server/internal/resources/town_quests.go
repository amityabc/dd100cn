package resources

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

type rangeQuest struct {
	Id             uint16
	Hash           string
	Subtype        int
	Data           []int64
	NpcRangeTarget *struct {
		NpcId            uint32
		Town, Area, X, Y uint16
		Visibility       *struct {
			Quest        uint16
			ResourceHash string
		}
	}
	ChildRangeBinding *struct {
		Parent, PreviousChild uint16
		ParentHash            string
	}
}

func (q rangeQuest) contains(p TownPoint) bool {
	if p.X > math.MaxInt16 || p.Y > math.MaxInt16 {
		return false
	}
	x, y := int64(p.X), int64(p.Y)
	d := q.Data
	if n := q.NpcRangeTarget; q.Subtype == 0 && n != nil {
		dx, dy := x-int64(n.X), y-int64(n.Y)
		return p.Town == n.Town && p.Area == n.Area && dx >= -d[1]-40 && dx <= d[1]+40 && dy >= -d[2]-40 && dy <= d[2]+40
	}
	return int64(p.Town) == d[0] && int64(p.Area) == d[1] && x >= d[2] && y >= d[3] && x < d[2]+d[4] && y < d[3]+d[5]
}

func (q rangeQuest) arrived(a activeQuest) bool {
	if a.Remaining[0] != 0 {
		return false
	}
	for _, receipt := range a.Receipts {
		if len(receipt) > 192 || !strings.HasPrefix(receipt, "town-range:") {
			continue
		}
		f := strings.Split(receipt, ":")
		if len(f) != 7 || f[1] != strconv.Itoa(int(q.Id)) || f[2] != q.Hash {
			continue
		}
		var coords [4]uint16
		valid := true
		for i, s := range f[3:] {
			v, err := strconv.ParseUint(s, 10, 16)
			if err != nil || strconv.FormatUint(v, 10) != s {
				valid = false
				break
			}
			coords[i] = uint16(v)
		}
		if valid && q.contains(TownPoint{Town: coords[0], Area: coords[1], X: coords[2], Y: coords[3]}) {
			return true
		}
	}
	return false
}

func (q rangeQuest) visible(s TownQuests) bool {
	if binding := q.ChildRangeBinding; binding != nil {
		p, ok := s.Active[binding.Parent]
		_, previousActive := s.Active[binding.PreviousChild]
		if !ok || s.clear(binding.Parent) || s.Cleared[binding.PreviousChild] != 1 || previousActive || p.ObjectiveHash != binding.ParentHash || len(p.Remaining) != 1 || p.Receipts == nil {
			return false
		}
		switch q.Id {
		case 3759:
			return !s.clear(12121) && !s.clear(12122)
		case 3832:
			return !s.clear(6359) && !s.clear(12119)
		default:
			return false
		}
	}
	switch q.Id {
	case 6301:
		return q.NpcRangeTarget.Visibility != nil && s.clear(6300) && !s.started(6302)
	case 12302, 12312, 12314:
		if q.NpcRangeTarget.Visibility == nil || !s.clear(12301) || s.started(12384) || s.started(12401) || s.clear(12312) {
			return false
		}
		if q.Id != 12312 {
			return s.clear(12166) && !s.started(12303) && !s.started(12315)
		}
		_, left := s.Active[12303]
		_, right := s.Active[12315]
		return (s.clear(12311) || s.clear(12323)) && !left && !right
	}
	return true
}

func (t *TownCatalog) hasNPC(q rangeQuest, level byte, s TownQuests, p TownPoint) bool {
	n := q.NpcRangeTarget
	if n == nil || n.NpcId == 0 || p.Town != n.Town || p.Area != n.Area || !t.allows(level, s, p, false) {
		return false
	}
	a := t.areas[p.key()]
	rules := t.data.Phases[p.Town]
	emptyEvents := p.Town == 40 && p.Area == 3 && n.NpcId == 13
	for _, r := range rules {
		if !exempt(a) && r.Kind != "completed quest" && r.Kind != "by event" && r.Kind != "depend townarea event" {
			return false
		}
		if r.Kind != "completed quest" && !(r.Kind == "depend townarea event" && r.Id >= 1 && r.Id <= 3) {
			emptyEvents = false
		}
	}
	return t.everyMap(a, s, !emptyEvents, func(m townMap) bool {
		return slices.Contains(m.Npcs, npcPlacement{n.NpcId, n.X, n.Y})
	})
}

// Arrival updates only matching original objective fields, inside the same position write.
// Raw subtrees keep imported extensions, wide integer IDs and other players' quest history intact.
func (t *TownCatalog) Arrival(gameplay json.RawMessage, level byte, s TownQuests, p TownPoint) (json.RawMessage, error) {
	if len(s.Active) > 20 {
		return nil, nil
	}
	var active, quests, doc map[string]json.RawMessage
	for id, a := range s.Active {
		q, ok := t.ranges[id]
		if !ok || q.Hash != a.ObjectiveHash || len(a.Remaining) != 1 || a.Remaining[0] > 1 || a.Receipts == nil || q.arrived(a) || !q.contains(p) || !q.visible(s) {
			continue
		}
		if q.Subtype == 0 && !t.hasNPC(q, level, s, p) {
			continue
		}
		if doc == nil {
			if err := json.Unmarshal(gameplay, &doc); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(doc["Quests"], &quests); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(quests["Active"], &active); err != nil {
				return nil, err
			}
		}
		key := strconv.Itoa(int(id))
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(active[key], &obj); err != nil {
			return nil, err
		}
		obj["Remaining"] = json.RawMessage("[0]")
		receipt := fmt.Sprintf("town-range:%d:%s:%d:%d:%d:%d", id, q.Hash, p.Town, p.Area, p.X, p.Y)
		if !slices.Contains(a.Receipts, receipt) {
			a.Receipts = append(a.Receipts, receipt)
		}
		var err error
		obj["Receipts"], err = json.Marshal(a.Receipts)
		if err != nil {
			return nil, err
		}
		active[key], err = json.Marshal(obj)
		if err != nil {
			return nil, err
		}
	}
	if doc == nil {
		return nil, nil
	}
	var err error
	quests["Active"], err = json.Marshal(active)
	if err != nil {
		return nil, err
	}
	doc["Quests"], err = json.Marshal(quests)
	if err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

func (t *TownCatalog) NewArrivals(before, after TownQuests) []uint16 {
	var ids []uint16
	for id, a := range after.Active {
		q, ok := t.ranges[id]
		if !ok || len(a.Remaining) != 1 || a.ObjectiveHash != q.Hash || !q.arrived(a) {
			continue
		}
		if old, exists := before.Active[id]; exists && len(old.Remaining) == 1 && old.ObjectiveHash == q.Hash && q.arrived(old) {
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
