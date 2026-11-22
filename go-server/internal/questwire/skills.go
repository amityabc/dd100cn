package questwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"slices"

	"s9a1/server/internal/storage"
)

type skillEntry struct {
	ID     uint16
	Levels byte
	Refund bool
}

type skillCommand struct {
	ID   uint16
	Keys []byte
}

type SkillRequest struct {
	Opcode                          uint16
	Tree, Mode, Source, Destination byte
	Context                         int32
	Slots                           []skillEntry
	Locks                           []byte
	Commands                        []skillCommand
}

func ReadSkill(op uint16, b []byte) (SkillRequest, error) {
	r := SkillRequest{Opcode: op}
	bad := errors.New("native skill request boundary mismatch")
	switch op {
	case 439:
		if len(b) != 516 || little.Uint32(b) != 512 {
			return r, bad
		}
		r.Locks = bytes.Clone(b[4:])
	case 331:
		if len(b) == 0 || b[0] > 64 {
			return r, bad
		}
		left := b[1:]
		for range b[0] {
			if len(left) < 3 {
				return r, bad
			}
			id, n := little.Uint16(left), int(left[2])
			left = left[3:]
			if id == 0 || n > 8 || n > len(left) {
				return r, bad
			}
			r.Commands = append(r.Commands, skillCommand{id, bytes.Clone(left[:n])})
			left = left[n:]
		}
		if len(left) != 0 {
			return r, bad
		}
	case 260:
		if len(b) != 5 || b[0] > 1 {
			return r, bad
		}
		r.Tree = b[0]
	case 483:
		if len(b) != 2 {
			return r, bad
		}
		r.Tree, r.Mode = b[0], b[1]
	case 28:
		if len(b) != 12 {
			return r, bad
		}
		r.Tree, r.Source, r.Destination, r.Context, r.Mode = b[0], b[1], b[2], int32(little.Uint32(b[3:])), b[7]
	case 29:
		if len(b) < 3 || b[1] == 0 || !skillTree(b[0]) {
			return r, bad
		}
		count := int(b[1])
		n := 3 + 4*count
		if len(b) != n && len(b) != n+4 {
			return r, bad
		}
		r.Tree, r.Mode = b[0], b[n-1]
		if r.Mode != 0 && r.Mode != 1 && r.Mode != 3 {
			return r, bad
		}
		seen := map[uint16]bool{}
		for i := 0; i < count; i++ {
			at := 2 + 4*i
			id, refund, levels := little.Uint16(b[at:]), b[at+2], b[at+3]
			if id == 0 || levels == 0 || refund > 1 || seen[id] {
				return r, bad
			}
			seen[id] = true
			r.Slots = append(r.Slots, skillEntry{id, levels, refund == 1})
		}
	default:
		return r, bad
	}
	return r, nil
}

func skillTree(t byte) bool { return t == 255 || t <= 1 }

func (r SkillRequest) Overlay() bool {
	return r.Opcode == 483 && (r.Mode == 2 || r.Mode == 3) || r.Opcode == 29 && r.Mode == 3 || r.Opcode == 28 && r.Mode == 2
}

type skillError uint16

func (e skillError) Error() string { return "skill request is not admitted" }

func SkillError(err error) uint16 {
	var e skillError
	if errors.As(err, &e) {
		return uint16(e)
	}
	return 7
}

type skillBuild struct {
	Rows   []questSkill `json:"Skills"`
	Sp, Tp uint16
}

type skillProgress struct {
	Sp, Tp   uint16
	GrowType byte
}

type SkillContext struct {
	Duel, SkillMaster, TrainingLobby bool
	FreeSkills                       func(byte, byte, byte) map[uint16]byte
	Session                          SkillSession
}

type SkillSession struct {
	auto    skillAuto
	overlay skillOverlay
}

type skillAuto struct {
	Acceptance string
	ID         uint16
	Character  uint16
	Revision   int64
	Tree       byte
}

type skillOverlay struct {
	Acceptance string
	ID         uint16
	Character  uint16
	Grow, Tree byte
	Build      skillBuild
}

type SkillResult struct {
	Mutation, Display                 json.RawMessage
	Ack                               []byte
	ProjectionBefore, ProjectionAfter bool
	TrainingID                        uint16
	Remaining                         uint32
	Session                           SkillSession
}

func (catalog *Catalog) definition(job byte, id uint16) (skillDefinition, bool) {
	d, ok := catalog.effectSkills[[2]uint16{uint16(job), id}]
	return d, ok
}

func validSkills(rows []questSkill) bool {
	if rows == nil || len(rows) > 255 {
		return false
	}
	ids, slots := map[uint16]bool{}, map[uint16]bool{}
	for _, r := range rows {
		if r.Id == 0 || r.Level == 0 || r.Slot > 254 || ids[r.Id] || slots[r.Slot] {
			return false
		}
		ids[r.Id], slots[r.Slot] = true, true
	}
	return true
}

func nextSkillSlot(used map[uint16]bool, active bool) uint16 {
	if active {
		for i := uint16(0); i < 12; i++ {
			if !used[i] {
				return i
			}
		}
	}
	for i := uint16(102); i <= 254; i++ {
		if !used[i] {
			return i
		}
	}
	return 255
}

func (catalog *Catalog) train(c storage.Character, p player, b skillBuild, rows []skillEntry, free map[uint16]byte, premium bool) (skillBuild, []questSkill, error) {
	next := slices.Clone(b.Rows)
	used := map[uint16]bool{}
	for _, s := range next {
		used[s.Slot] = true
	}
	sp, tp := int64(b.Sp), int64(b.Tp)
	changed := make([]questSkill, 0, len(rows))
	for _, req := range rows {
		d, ok := catalog.definition(c.Job, req.ID)
		if !ok {
			return b, nil, skillError(21)
		}
		if !d.OrdinarySp && len(d.TpCosts) == 0 {
			return b, nil, skillError(217)
		}
		if len(d.Costs) == 0 || len(d.Maximum) == 0 || d.Interval < 0 || d.RequiredLevel < 1 || d.QuestDiscount != nil && len(d.QuestDiscount.Amounts) == 0 {
			return b, nil, storage.ErrInvalid
		}
		at := slices.IndexFunc(next, func(row questSkill) bool { return row.Id == req.ID })
		previous := questSkill{}
		if at >= 0 {
			previous = next[at]
		}
		old := int(previous.Level)
		target := old + int(req.Levels)
		from, to, sign := old+1, target, int64(-1)
		if req.Refund {
			target = old - int(req.Levels)
			if at < 0 || target < int(max(d.FreeLevel, free[req.ID])) {
				return b, nil, skillError(18)
			}
			from, to, sign = target+1, old, 1
		} else if target > d.cap(c.Level, p.grow, premium, p.hist.Cleared) {
			return b, nil, skillError(217)
		}
		for rank := from; rank <= to; rank++ {
			if len(d.TpCosts) > 0 {
				tp += sign * int64(d.TpCosts[min(rank, len(d.TpCosts))-1])
			} else {
				sp += sign * int64(d.cost(rank, p.hist.Cleared))
			}
		}
		slot := previous.Slot
		if at < 0 {
			slot = nextSkillSlot(used, d.Active)
			if slot > 254 {
				return b, nil, storage.ErrInvalid
			}
			used[slot] = true
		}
		row := questSkill{Id: req.ID, Slot: slot, Level: byte(target)}
		switch {
		case target == 0:
			next = slices.Delete(next, at, at+1)
		case at < 0:
			next = append(next, row)
		default:
			next[at] = row
		}
		changed = append(changed, row)
	}
	for _, row := range next {
		d, ok := catalog.definition(c.Job, row.Id)
		if !ok {
			return b, nil, storage.ErrInvalid
		}
		for _, first := range d.Prerequisites {
			if !slices.ContainsFunc(next, func(row questSkill) bool { return row.Id == first.Id && row.Level >= first.Level }) {
				return b, nil, skillError(19)
			}
		}
	}
	if sp < 0 || sp > math.MaxUint16 || tp < 0 || tp > math.MaxUint16 {
		return b, nil, skillError(18)
	}
	slices.SortFunc(next, func(a, b questSkill) int { return int(a.Id) - int(b.Id) })
	if !validSkills(next) {
		return b, nil, storage.ErrInvalid
	}
	return skillBuild{next, uint16(sp), uint16(tp)}, changed, nil
}

func (catalog *Catalog) reset(c storage.Character, p player, b skillBuild, req SkillRequest, free map[uint16]byte) (skillBuild, error) {
	initial, ok := catalog.initialSkills[c.Job]
	if !ok || !validSkills(initial) {
		return b, storage.ErrInvalid
	}
	ranks := map[uint16]byte{}
	for _, s := range initial {
		ranks[s.Id] = s.Level
	}
	var rows []skillEntry
	for _, s := range b.Rows {
		d, ok := catalog.definition(c.Job, s.Id)
		if !ok || !d.OrdinarySp && len(d.TpCosts) == 0 {
			continue
		}
		n := max(d.FreeLevel, ranks[s.Id], free[s.Id])
		if s.Level > n {
			rows = append(rows, skillEntry{s.Id, s.Level - n, true})
		}
	}
	if len(rows) == 0 {
		return b, nil
	}
	next, _, err := catalog.train(c, p, b, rows, free, false)
	return next, err
}

func (catalog *Catalog) swap(c storage.Character, b skillBuild, req SkillRequest) (skillBuild, error) {
	if !skillTree(req.Tree) || req.Context != -1 || req.Source == 255 || req.Destination == 255 || req.Source == req.Destination {
		return b, storage.ErrInvalid
	}
	index := func(slot byte) int {
		return slices.IndexFunc(b.Rows, func(s questSkill) bool { return s.Slot == uint16(slot) })
	}
	s, t := index(req.Source), index(req.Destination)
	if s < 0 {
		return b, storage.ErrInvalid
	}
	canPlace := func(id uint16, slot byte) bool {
		d, ok := catalog.definition(c.Job, id)
		return ok && (slot >= 14 || d.Active) && (!(c.Job == 9 || c.Job == 10) || slot != 6 && slot != 13)
	}
	if !canPlace(b.Rows[s].Id, req.Destination) || t >= 0 && !canPlace(b.Rows[t].Id, req.Source) {
		return b, storage.ErrInvalid
	}
	b.Rows = slices.Clone(b.Rows)
	b.Rows[s].Slot = uint16(req.Destination)
	if t >= 0 {
		b.Rows[t].Slot = uint16(req.Source)
	}
	return b, nil
}

func commandBody(rows map[uint16][]byte) []byte {
	b := []byte{1, byte(len(rows))}
	for _, id := range sortedKeys(rows) {
		code := rows[id]
		b = little.AppendUint16(b, id)
		b = append(append(b, byte(len(code))), code...)
	}
	return b
}

func skillTrainAck(req SkillRequest, b skillBuild, changed []questSkill) []byte {
	ack := little.AppendUint16([]byte{1, req.Tree}, b.Sp)
	ack = little.AppendUint16(ack, b.Tp)
	ack = append(ack, byte(len(changed)))
	for _, s := range changed {
		ack = little.AppendUint16(append(ack, byte(s.Slot)), s.Id)
		ack = append(ack, s.Level, 0)
	}
	return append(ack, req.Mode)
}

func encodeCodes(code []byte) []uint16 {
	res := make([]uint16, len(code))
	for i, v := range code {
		res[i] = uint16(v)
	}
	return res
}
