package questwire

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

type Packet struct {
	Class      byte
	Opcode     uint16
	Body       []byte
	Compressed bool
}

var little = binary.LittleEndian

const (
	meet = iota
	clearMap
	huntEnemy
	huntMonster
	seeking
	seekMeet
	questClear
	reach
	clearCondition
	unsupported
	cinematic
	question
	communicator
	duel
)

func validActive(q *Quest, a active) bool {
	count := 1
	switch q.Kind {
	case duel:
		count = 3
	case huntEnemy:
		count = len(q.Data) / 5
	case huntMonster:
		count = len(q.Data) / 4
	}
	if !q.Supported || a.ObjectiveHash != q.Hash || a.Remaining == nil || len(a.Remaining) != count || a.Receipts == nil {
		return false
	}
	for _, n := range a.Remaining {
		if n > 511 {
			return false
		}
	}
	switch q.Kind {
	case duel:
		if !slices.Contains([]uint16{2672, 2673, 2674}, q.Id) || len(q.Data) != 4 {
			return false
		}
		keys := 0
		for _, s := range a.Receipts {
			if name, yes := strings.CutPrefix(s, "duel-accept:"); yes {
				_, err := hex.DecodeString(name)
				if len(name) != 32 || err != nil {
					return false
				}
				keys++
			}
		}
		if keys != 1 {
			return false
		}
		for i := 0; i < 3; i++ {
			got := slices.Contains(a.Receipts, fmt.Sprintf("duel-training:%d", i))
			if int(a.Remaining[i]) > q.Data[i] || got != (q.Data[i] != 0 && a.Remaining[i] == 0) {
				return false
			}
		}
	case communicator:
		n := a.Remaining[0]
		done := n == 0
		return q.AcceptanceSupported && n <= 1 && a.Answer == nil && a.UsedItemCount == 1-n && len(a.Receipts) == int(1-n) && (!done || slices.Contains(a.Receipts, fmt.Sprintf("communicator:%d:%s", q.Id, q.Hash))) && (!a.NpcMet || done)
	case question:
		if !q.AcceptanceSupported || a.NpcMet || a.UsedItemCount != 0 {
			return false
		}
		if a.Answer == nil {
			return a.Remaining[0] == 1 && len(a.Receipts) == 0
		}
		return *a.Answer < uint16(q.QuestionChoices) && a.Remaining[0] == 0 && len(a.Receipts) == 1 && slices.Contains(a.Receipts, fmt.Sprintf("question-answer:%d:%s:%d", q.Id, q.Hash, *a.Answer))
	case cinematic:
		if q.Subtype != -1 || len(q.Data) != 1 || q.Data[0] <= 0 || a.Remaining[0] > 1 {
			return false
		}
		movie := uint32(q.Data[0])
		done := a.Remaining[0] == 0
		if dungeon := q.DungeonCinematicTarget; dungeon != nil {
			return dungeon.Movie == movie && dungeon.Map > 0 && len(q.DestinationDungeons) > 0 && q.TownCinematicTarget == nil && ((!done && len(a.Receipts) == 0) || (done && len(a.Receipts) == 1 && strings.HasPrefix(a.Receipts[0], fmt.Sprintf("dungeon-cinematic:%d:", movie))))
		}
		return q.TownCinematicTarget != nil && q.TownCinematicTarget.Movie == movie && len(q.DestinationDungeons) == 0 && slices.Contains(a.Receipts, fmt.Sprintf("cinematic:%d", movie)) == done
	}
	return true
}
func (q *Quest) contains(town, area, x, y int) bool {
	if x > math.MaxInt16 || y > math.MaxInt16 {
		return false
	}
	if q.Subtype == 0 && q.NpcRangeTarget != nil && len(q.Data) == 3 {
		n := q.NpcRangeTarget
		return town == int(n.Town) && area == int(n.Area) && x >= int(n.X)-q.Data[1]-40 && x <= int(n.X)+q.Data[1]+40 && y >= int(n.Y)-q.Data[2]-40 && y <= int(n.Y)+q.Data[2]+40
	}
	return q.Subtype == 1 && len(q.Data) == 6 && town == q.Data[0] && area == q.Data[1] && x >= q.Data[2] && y >= q.Data[3] && x < q.Data[2]+q.Data[4] && y < q.Data[3]+q.Data[5]
}
func (q *Quest) arrived(a active) bool {
	if q.Kind != reach || !q.AcceptanceSupported || a.ObjectiveHash != q.Hash || len(a.Remaining) != 1 || a.Remaining[0] != 0 {
		return false
	}
	prefix := fmt.Sprintf("town-range:%d:%s:", q.Id, q.Hash)
	for _, s := range a.Receipts {
		tail, ok := strings.CutPrefix(s, prefix)
		if !ok || len(s) > 192 {
			continue
		}
		fields := strings.Split(tail, ":")
		if len(fields) != 4 {
			continue
		}
		var nums [4]int
		for i, f := range fields {
			n, err := strconv.ParseUint(f, 10, 16)
			if err != nil || strconv.FormatUint(n, 10) != f {
				ok = false
				break
			}
			nums[i] = int(n)
		}
		if ok && q.contains(nums[0], nums[1], nums[2], nums[3]) {
			return true
		}
	}
	return false
}

type view struct {
	catalog   *Catalog
	p         player
	in        storage.Inventory
	held      map[uint32]uint64
	receipts  map[string]json.RawMessage
	inDungeon bool
}

func (v view) remaining(q *Quest, visiting map[uint16]bool) (uint32, error) {
	a, ok := v.p.hist.Active[q.Id]
	if !ok || !validActive(q, a) || visiting[q.Id] {
		return 0, fmt.Errorf("persisted quest %d differs from frozen objective", q.Id)
	}
	visiting[q.Id] = true
	defer delete(visiting, q.Id)
	switch q.Kind {
	case huntEnemy, huntMonster, duel:
		var n uint32
		for i, r := range a.Remaining {
			n |= r << ((9 * i) % 32)
		}
		return n, nil
	case communicator:
		n := a.Remaining[0]
		if !a.NpcMet {
			n |= 1 << 9
		}
		return n, nil
	case seeking, seekMeet:
		needs := map[uint32]uint64{}
		for _, r := range q.Materials {
			if needs[r.Template] > math.MaxUint64-uint64(r.Count) {
				return 0, errors.New("quest material quantity overflow")
			}
			needs[r.Template] += uint64(r.Count)
		}
		if q.Kind == seeking {
			var mask uint32
			for i, r := range q.Materials {
				if v.held[r.Template] < needs[r.Template] {
					mask |= 1 << (i % 32)
				}
			}
			return mask, nil
		}
		var missing uint64
		for id, need := range needs {
			missing += need - min(need, v.held[id])
		}
		n := uint32(min(missing, 511))
		if !a.NpcMet {
			n |= 1 << 9
		}
		return n, nil
	case questClear:
		if !v.inDungeon && q.TownHandIn && v.handInReady(q, visiting) {
			return 0, nil
		}
		var left uint32
		for _, id := range q.Children {
			if v.p.hist.Cleared[id] == 0 {
				left++
			}
		}
		if left == 1 && q.OrderedDungeonParent && len(q.Children) > 1 {
			last := q.Children[len(q.Children)-1]
			terminal, active := v.p.hist.Active[last]
			if child := v.catalog.ordinary[last]; v.p.hist.Cleared[last] == 0 && active && child != nil {
				if !validActive(child, terminal) {
					return 0, fmt.Errorf("invalid terminal quest %d", last)
				}
				preceding := true
				for _, id := range q.Children[:len(q.Children)-1] {
					child := v.catalog.ordinary[id]
					run, valid := v.childReceipt(child)
					if !valid || run == "" && !child.OutsideDungeonClaim {
						preceding = false
						break
					}
				}
				if preceding && !slices.ContainsFunc(terminal.Remaining, func(n uint32) bool { return n != 0 }) && slices.ContainsFunc(terminal.Receipts, func(s string) bool {
					return strings.HasPrefix(s, "dungeon-room-clear:") && len(s) > len("dungeon-room-clear:")+33
				}) {
					left = 0
				}
			}
		}
		return left, nil
	case reach:
		if q.arrived(a) || !v.inDungeon && q.Subtype == 1 && q.AcceptanceSupported && q.contains(int(v.p.pos.Town), int(v.p.pos.Area), int(v.p.pos.X), int(v.p.pos.Y)) {
			return 0, nil
		}
		return 1, nil
	}
	if len(a.Remaining) == 0 {
		return 0, errors.New("missing quest progress")
	}
	return a.Remaining[0], nil
}
func sortedKeys[V any](m map[uint16]V) []uint16 {
	ids := make([]uint16, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
func (c *Catalog) Project(character storage.Character, in storage.Inventory, receipts map[string]json.RawMessage) ([]Packet, error) {
	return c.ProjectScene(character, in, receipts, false)
}

func (c *Catalog) ProjectScene(character storage.Character, in storage.Inventory, receipts map[string]json.RawMessage, inDungeon bool) ([]Packet, error) {
	p, err := playerOf(character)
	if err != nil {
		return nil, err
	}
	if p.level == 0 || len(p.hist.Active) > 20 || in.CharacterID != character.ID {
		return nil, errors.New("invalid quest projection owner or level")
	}
	v := projectionView(c, p, in, receipts)
	v.inDungeon = inDungeon
	activeBody := little.AppendUint32(nil, uint32(len(p.hist.Active)))
	available := map[uint16]bool{}
	for _, id := range sortedKeys(p.hist.Active) {
		q := c.ordinary[id]
		if q == nil {
			return nil, fmt.Errorf("persisted quest %d definition missing", id)
		}
		left, err := v.remaining(q, map[uint16]bool{})
		if err != nil {
			return nil, err
		}
		activeBody = little.AppendUint16(activeBody, id)
		activeBody = little.AppendUint32(activeBody, left)
		available[id] = true
	}
	for i := range c.quests {
		q := &c.quests[i]
		if c.ordinaryAvailable(p, q) {
			available[q.Id] = true
		}
	}
	for i := range c.extra {
		q := &c.extra[i]
		if c.equipment(p, q) {
			available[q.Id] = true
		}
	}
	for id := range c.dpl(p, []uint16{6225, 6226, 6227, 6228, 6229, 8725, 8726}, 50, false) {
		available[id] = true
	}
	for id := range c.dpl(p, []uint16{6410, 6411, 6412, 6413}, 100, true) {
		available[id] = true
	}
	proto := binary.AppendUvarint([]byte{8, 21, 16}, uint64(p.level))
	for _, id := range sortedKeys(available) {
		proto = append(proto, 32)
		proto = binary.AppendUvarint(proto, uint64(id))
	}
	manual := append(little.AppendUint32(nil, uint32(len(proto))), proto...)
	flags := make([]byte, 30004)
	little.PutUint32(flags, 30000)
	for id, flag := range p.hist.Cleared {
		if id == 0 || id >= 30000 || flag == 0 {
			return nil, errors.New("invalid saved quest clear flag")
		}
		flags[4+int(id)] = flag
	}
	cleared, err := protocol.CompressPayload(flags)
	if err != nil {
		return nil, err
	}
	return []Packet{{0, 561, activeBody, false}, {0, 21, manual, false}, {0, 356, cleared, true}}, nil
}

// Arrivals returns the native C33 acknowledgements for newly committed town-range receipts.
func (c *Catalog) Arrivals(before, after storage.Character) ([]Packet, error) {
	if before.ID != after.ID {
		return nil, errors.New("range arrival character changed")
	}
	a, err := playerOf(before)
	if err != nil {
		return nil, err
	}
	b, err := playerOf(after)
	if err != nil {
		return nil, err
	}
	var packets []Packet
	if len(b.hist.Active) > 20 {
		return nil, nil
	}
	for _, id := range sortedKeys(b.hist.Active) {
		q := c.ordinary[id]
		if q != nil && q.arrived(b.hist.Active[id]) && !q.arrived(a.hist.Active[id]) {
			body := little.AppendUint16([]byte{1}, id)
			packets = append(packets, Packet{1, 33, little.AppendUint32(body, 0), false})
		}
	}
	return packets, nil
}
