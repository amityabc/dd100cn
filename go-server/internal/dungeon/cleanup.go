package dungeon

import (
	"math"
	"slices"

	"s9a1/server/internal/opening"
)

func (r *Run) placeholderBossRoom() bool {
	d := r.Definition.Dungeon
	if r.Room.X != d.BossX || r.Room.Y != d.BossY {
		return false
	}
	placeholder := false
	for _, m := range r.Room.Monsters {
		if m.BlocksRoom && m.Type == 3 && !m.Dummy {
			return false
		}
		placeholder = placeholder || m.BlocksRoom && m.Dummy
	}
	return placeholder
}

func (r *Run) markerRoom() bool {
	return r.Definition.Dungeon.ID == 5109 && r.Room.MapID == 312116 &&
		slices.ContainsFunc(r.Room.Monsters, func(m opening.Monster) bool { return m.Code == 74669 }) &&
		!slices.ContainsFunc(r.Room.Monsters, func(m opening.Monster) bool { return m.Type == 3 })
}

func (r *Run) uncredited(index int, body []byte) (unattributed bool, cleanup uint16, deps []uint16) {
	if le.Uint16(body[4:]) != math.MaxUint16 {
		return
	}
	d, room := r.Definition.Dungeon, r.Room
	inBoss := room.X == d.BossX && room.Y == d.BossY
	m := room.Monsters[index]
	rows, reason := int(body[30]), body[68+14*int(body[30])]
	actor := r.actorBase() + uint16(index)
	if rows == 0 {
		storyBoss := m.Type == 3 && !m.Dummy && reason == 0 &&
			(d.ID == 100000006 || d.ID >= 100000326 && d.ID < 291100000 || d.ID >= 291100420 && d.ID <= 291100460)
		scriptedBoss := reason == 7 && (m.Type == 3 && (d.ID == 312 || d.ID == 291100308 || d.ID == 291100317 ||
			d.ID == 100002481 && room.MapID == 100003308 && m.Code == 109013171 && !m.Dummy && m.AiScriptIndex == nil))
		transformed := reason == 5 && d.ID == 5114 && room.MapID == 312205 && index == 0 && m.Code == 74637
		if storyBoss || scriptedBoss || transformed {
			return true, 0, nil
		}
	}
	self := rows > 0
	if m.Type < 3 && !m.Dummy && m.AiScriptIndex == nil && reason == 0 {
		rowsValid, sameOrKnown, emitted := true, true, rows != 0
		for i := range rows {
			one := body[31+i*14:]
			other := le.Uint16(one[2:])
			if le.Uint16(one) != 0x0211 || le.Uint64(one[4:]) == 0 || le.Uint16(one[12:]) == 0 {
				rowsValid = false
				break
			}
			self = self && other == actor
			if other != actor && !r.generatedMonster(other) {
				sameOrKnown = false
			}
			if other == actor || other < 1024 && !r.generatedMonster(other) {
				emitted = false
			}
		}
		if rowsValid && (!inBoss || self && m.Code == 1082) && (!self || m.Code == 1082) && (sameOrKnown || emitted) {
			return true, 0, nil
		}
	}
	if rows != 0 {
		return
	}
	haze := d.ID == 92 && (room.MapID == 91247 || room.MapID == 91277) && m.Type == 3 && !m.Dummy && m.AiScriptIndex == nil &&
		(m.Code == 107000145 || m.Code == 107000147) && slices.ContainsFunc(room.Monsters, func(v opening.Monster) bool {
		return v.Type == 3 && !v.Dummy && v.Code != 107000145 && v.Code != 107000147
	})
	palace := d.ID == 3190 && room.MapID == 57979 && m.Type == 3 && !m.Dummy && m.Code == 63525
	if m.Type >= 3 && !m.Dummy && !(reason == 7 && (haze || palace)) {
		return
	}
	if !inBoss {
		flee := d.ID == 85 && room.X == 0 && room.Y == 1 && room.MapID == 92426 &&
			m.Code == 65682 && m.Type == 0 && !m.Dummy && m.AiScriptIndex == nil
		story := d.ID >= 291100420 && d.ID <= 291100460 || d.ID == 100000006 || d.ID >= 100000326 && d.ID < 291100000
		awakening := d.ID == 7123 && room.CinematicLayer != nil &&
			!slices.ContainsFunc(room.Monsters, func(v opening.Monster) bool { return v.Type == 3 })
		if d.ID == 291100373 && reason == 7 || awakening && reason == 7 || (flee || story) && (reason == 6 || reason == 7) {
			return false, actor, nil
		}
		return
	}
	base := r.actorBase()
	for i, v := range room.Monsters {
		if i != index && v.Type == 3 && !v.Dummy {
			deps = append(deps, base+uint16(i))
		}
	}
	if len(deps) == 0 {
		if reason == 7 {
			return false, actor, nil
		}
		return false, 0, nil
	}
	return false, deps[0], deps
}

func (r *Run) generatedMonster(actor uint16) bool {
	for k, room := range r.rooms {
		if !r.seen[scene{k, false}] {
			continue
		}
		idx := int(actor) - int(r.bases[k])
		if idx >= 0 && idx < len(room.Monsters) {
			m := room.Monsters[idx]
			return m.Type < 3 && !m.Dummy && m.AiScriptIndex == nil
		}
	}
	return false
}
