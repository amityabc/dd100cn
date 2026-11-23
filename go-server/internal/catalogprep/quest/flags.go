package quest

import (
	"slices"

	"s9a1/server/internal/questwire"
)

func deriveFlags(q *questwire.Quest, rows map[uint16]*questwire.Quest) {
	if !q.Supported {
		return
	}
	q.MixedDungeonParent = mixedParent(q, rows)
	q.OrderedDungeonParent = orderedParent(q, rows)
	q.DungeonNpcHandIn = dungeonHandIn(q, rows)
	q.TownHandIn = townHandIn(q, rows)
	q.OutsideDungeonClaim = outsideDungeonClaim(q)
	q.Operations.Daily = q.Operations.Daily && q.AwakeningAward == nil && len(q.CascadeClear) == 0
	if q.Kind == question {
		q.AcceptanceSupported = len(q.DestinationDungeons) == 0 && len(q.Children) == 0
	}
	if q.Kind != huntEnemy || len(q.Data) < 5 || len(q.Data)%5 != 0 {
		return
	}
	if q.Parent != 0 {
		parent := rows[q.Parent]
		if q.Data[3] == 3 && parent != nil && orderedParent(parent, rows) && parent.Children[0] == q.Id {
			q.TownPassive, q.DungeonPassive = true, true
		}
		return
	}
	if q.Operations.Grade != "epic" || q.Subtype != -1 || q.AwakeningAward != nil || len(q.Materials) != 0 {
		return
	}
	q.DungeonPassive = true
	for row := range slices.Chunk(q.Data, 5) {
		if row[3] != 3 {
			q.DungeonPassive = false
		}
	}
	passive := townPassives[q.Id]
	q.TownPassive = passive != [3]int{} && len(q.Data) == 5 && q.Data[0] == passive[0] && q.Data[1] == -1 &&
		q.Data[2] == passive[1] && q.Data[3] == 3 && (passive[2] == 0 || q.Data[4] == passive[2]) &&
		slices.Contains(q.DestinationDungeons, passive[0])
}

var townPassives = map[uint16][3]int{
	3205: {25, 80458, 1}, 3210: {27, 80456, 1}, 3258: {152, 69268, 1},
	3425: {61, 109001851, 0}, 3446: {146, 80454, 0}, 3455: {157, 18568, 0}, 3519: {80, 8824, 0},
}

func parentShape(q *questwire.Quest) bool {
	if q.Kind != questClear || q.Subtype != 2 || !q.Supported || len(q.Materials) != 0 || q.AwakeningAward != nil ||
		len(q.Children) == 0 || len(q.Data) != len(q.Children) {
		return false
	}
	for i, id := range q.Children {
		if int(id) != q.Data[i] {
			return false
		}
	}
	return true
}

func ordinaryChild(q *questwire.Quest, parent uint16) bool {
	return q != nil && q.Parent == parent && q.Supported
}

func dungeonObjective(kind int) bool {
	return anyOf(kind, clearMap, huntEnemy, huntMonster, clearCondition)
}

func orderedParent(q *questwire.Quest, rows map[uint16]*questwire.Quest) bool {
	if !parentShape(q) {
		return false
	}
	last := rows[q.Children[len(q.Children)-1]]
	if last == nil || last.Kind != clearMap {
		return false
	}
	if mixedParent(q, rows) {
		return true
	}
	for _, id := range q.Children {
		child := rows[id]
		if !ordinaryChild(child, q.Id) || !dungeonObjective(child.Kind) || len(child.Materials) != 0 || child.AwakeningAward != nil {
			return false
		}
	}
	return true
}

func dungeonHandIn(q *questwire.Quest, rows map[uint16]*questwire.Quest) bool {
	if !parentShape(q) || q.Operations.Grade != "epic" {
		return false
	}
	last := rows[q.Children[len(q.Children)-1]]
	if !ordinaryChild(last, q.Id) || last.Kind != seekAndMeet || len(last.Materials) == 0 || last.AwakeningAward != nil {
		return false
	}
	for _, destination := range last.DestinationDungeons {
		if !slices.Contains(q.DestinationDungeons, destination) {
			return false
		}
	}
	for _, id := range q.Children[:len(q.Children)-1] {
		child := rows[id]
		if !ordinaryChild(child, q.Id) || !dungeonObjective(child.Kind) || len(child.Materials) != 0 {
			return false
		}
	}
	return true
}

func outsideDungeonClaim(q *questwire.Quest) bool {
	if q.Operations.Grade != "sub" || q.Subtype != -1 {
		return false
	}
	switch q.Id {
	case 3510:
		return q.Parent == 3492 && q.Kind == meetNPC && slices.Equal(q.Data, []int{563}) && len(q.DestinationDungeons) == 0
	case 5149:
		return q.Parent == 3273 && q.Kind == seeking && slices.Equal(q.Data, []int{10165046, 3}) &&
			slices.Equal(q.Materials, []questwire.Material{{Template: 10165046, Count: 3}}) && len(q.DestinationDungeons) == 0
	case 3817:
		movie := q.DungeonCinematicTarget
		return q.Parent == 3494 && q.Kind == cinematic && slices.Equal(q.Data, []int{2123}) &&
			slices.Equal(q.DestinationDungeons, []int{163}) && movie != nil && movie.Movie == 2123 && movie.Map == 91597 &&
			len(movie.ResourceHash) == 64 && q.TownCinematicTarget == nil
	}
	return false
}

type mixedChain struct {
	first, last    uint16
	dungeon, mapID int
}

var mixedChains = map[uint16]mixedChain{3273: {5149, 5150, 239, 70154}, 3492: {3510, 3511, 163, 91577}, 3494: {3817, 3818, 163, 91599}}

func mixedParent(q *questwire.Quest, rows map[uint16]*questwire.Quest) bool {
	chain, known := mixedChains[q.Id]
	if !known || !parentShape(q) || q.Operations.Grade != "epic" || !slices.Equal(q.Children, []uint16{chain.first, chain.last}) ||
		!slices.Equal(q.DestinationDungeons, []int{chain.dungeon}) {
		return false
	}
	child, terminal := rows[chain.first], rows[chain.last]
	return ordinaryChild(child, q.Id) && ordinaryChild(terminal, q.Id) && outsideDungeonClaim(child) &&
		child.AwakeningAward == nil && terminal.AwakeningAward == nil && (chain.first == 5149 || len(child.Materials) == 0) && len(terminal.Materials) == 0 &&
		terminal.Kind == clearMap && terminal.Subtype == -1 && slices.Equal(terminal.Data, []int{chain.mapID}) &&
		slices.Equal(terminal.DestinationDungeons, q.DestinationDungeons)
}

type handInObjective struct {
	id        uint16
	kind      int
	data      []int
	materials []questwire.Material
}
type handInChain [2]handInObjective

var townHandInChains = map[uint16]handInChain{
	3249: {{3609, meetNPC, []int{303}, nil}, {3610, seekAndMeet, []int{10164786, 1, 303}, []questwire.Material{{Template: 10164786, Count: 1}}}},
	3286: {{3613, meetNPC, []int{41}, nil}, {3614, meetNPC, []int{40}, nil}},
	3670: {{3694, meetNPC, []int{126}, nil}, {3695, meetNPC, []int{28}, nil}},
	3617: {{3680, seekAndMeet, []int{3150, 50, 3020, 30, 11}, []questwire.Material{{Template: 3150, Count: 50}, {Template: 3020, Count: 30}}}, {3681, seekAndMeet, []int{10164779, 1, 122}, []questwire.Material{{Template: 10164779, Count: 1}}}},
	3698: {{3758, meetNPC, []int{59}, nil}, {3759, reachRange, []int{78, 50, 50}, nil}},
	3699: {{3760, meetNPC, []int{76}, nil}, {3761, meetNPC, []int{77}, nil}},
	3729: {{3762, meetNPC, []int{79}, nil}, {3763, seekAndMeet, []int{3033, 200, 3034, 200, 3035, 200, 3036, 200, 79}, []questwire.Material{{Template: 3033, Count: 200}, {Template: 3034, Count: 200}, {Template: 3035, Count: 200}, {Template: 3036, Count: 200}}}},
	3736: {{3764, seekAndMeet, []int{10164801, 50, 136}, []questwire.Material{{Template: 10164801, Count: 50}}}, {3765, meetNPC, []int{76}, nil}},
	3830: {{3831, meetNPC, []int{133}, nil}, {3832, reachRange, []int{344, 200, 200}, nil}},
}

func townHandIn(q *questwire.Quest, rows map[uint16]*questwire.Quest) bool {
	if !parentShape(q) || q.Parent != 0 || len(q.Children) != 2 || q.Operations.Grade != "epic" {
		return false
	}
	first, last := rows[q.Children[0]], rows[q.Children[1]]
	if !ordinaryChild(first, q.Id) || !ordinaryChild(last, q.Id) || first.AwakeningAward != nil || last.AwakeningAward != nil ||
		first.Operations.Grade != "sub" || last.Operations.Grade != "sub" {
		return false
	}
	if q.Id == 3851 {
		return first.Id == 3852 && first.Kind == clearMap && first.Subtype == -1 && slices.Equal(first.Data, []int{70486}) &&
			slices.Equal(q.DestinationDungeons, []int{193}) && slices.Equal(first.DestinationDungeons, []int{193}) &&
			last.Id == 3853 && last.Kind == meetNPC && last.Subtype == -1 && slices.Equal(last.Data, []int{347}) &&
			len(last.DestinationDungeons) == 0 && len(first.Materials) == 0 && len(last.Materials) == 0
	}
	chain, ok := townHandInChains[q.Id]
	matches := func(child *questwire.Quest, objective handInObjective) bool {
		return child.Id == objective.id && child.Kind == objective.kind &&
			(child.Subtype == -1 || anyOf(child.Id, 3759, 3832) && child.Kind == reachRange && child.Subtype == 0) &&
			slices.Equal(child.Data, objective.data) && slices.Equal(child.Materials, objective.materials) && len(child.DestinationDungeons) == 0
	}
	return ok && len(q.DestinationDungeons) == 0 && matches(first, chain[0]) && matches(last, chain[1])
}
