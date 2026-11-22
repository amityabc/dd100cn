package host

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

type oculusSession struct {
	position                   *storage.Position
	mode                       uint16
	active                     bool
	generation                 int64
	index, character           uint16
	id                         string
	phase                      byte
	armed                      time.Time
	request                    []byte
	selected, movie            bool
	coins, used, prayer        byte
	ritualStarted, ritualUntil time.Time
	clearedAt                  time.Time
	parked                     *dungeon.Run
	reductions                 []storage.OculusRitualReduction
}

const (
	oculusRecruit byte = 1 + iota
	oculusStarting
	oculusRunning
	oculusCleared
	oculusFailed
)

func (g *game) resetOculus() {
	old := g.oculus
	g.oculus = oculusSession{index: old.index, generation: old.generation + 1}
}

func (g *game) oculusLobbyParty() (mode uint16, current, recruiting, endedExit bool) {
	o := &g.oculus
	return o.mode, o.active, o.active && o.phase == oculusRecruit,
		o.active && (o.phase == oculusFailed || o.phase == oculusCleared && !g.ordinary.Active())
}

func (g *game) readOculusCharacter(ctx context.Context) (storage.Character, error) {
	r, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return storage.Character{}, err
	}
	if g.channel != 63 || r.Phase != storage.TownReady || g.oculus.position == nil {
		return storage.Character{}, storage.ErrPhase
	}
	return selected(r)
}

func (o *oculusSession) in(area dungeon.OculusArea) bool {
	return o.position != nil && o.position.Town == uint16(area.Town) && o.position.Area == uint16(area.Area)
}

func newOculusId() (string, error) {
	var raw [16]byte
	for i := range 4 {
		seed, err := dungeon.NewSeed()
		if err != nil {
			return "", err
		}
		binary.LittleEndian.PutUint32(raw[i*4:], seed)
	}
	raw[6] = raw[6]&15 | 64
	raw[8] = raw[8]&63 | 128
	h := hex.EncodeToString(raw[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

func (g *game) oculusReject(op uint16, reason string) error {
	if err := g.send(1, op, []byte{0, 0, 0}); err != nil || reason == "" {
		return err
	}
	b, err := protocol.Announcement(reason)
	if err != nil {
		return err
	}
	return g.send(0, 488, b)
}

func (g *game) oculusParty(ctx context.Context, op uint16, body []byte) error {
	c, err := g.readOculusCharacter(ctx)
	if errors.Is(err, storage.ErrPhase) {
		return g.oculusReject(op, "角色不满足洞察之眼的频道或入场条件。")
	}
	if err != nil {
		return err
	}
	o := &g.oculus
	if op == 13 {
		if len(body) != 4 {
			return storage.ErrInvalid
		}
		if binary.LittleEndian.Uint32(body) != 0 || !o.active || o.character != c.ID || o.phase == oculusStarting || o.phase == oculusRunning || g.ordinary.Active() {
			return g.oculusReject(op, "当前无法退出队伍，请先结束挑战、返回城镇并等待加载完成。")
		}
		index, position := o.index, o.position
		g.resetOculus()
		g.oculus.position = position
		if err := g.send(1, 13, []byte{1}); err != nil {
			return err
		}
		return g.send(0, 9, protocol.PartyRemove(index, 0, byte(g.channel), g.server.nativeParty))
	}
	req, err := protocol.ReadPartyCreate(body)
	if err != nil {
		return err
	}
	m, ok := g.server.Characters.Dungeons.Oculus[req.RaidMode]
	if !ok || !g.server.delegated || req.RaidType != 6 || req.Style != 0 || req.Dungeon != 0 || req.Difficulty != 5 || req.DetailWord != 0 || req.Detail != 0 ||
		len(req.Title) > 128 || req.Options&0xf8 != 0 || (req.RaidMode == 0 && req.Capacity != 4) || (req.RaidMode != 0 && req.Capacity != 1) {
		return g.oculusReject(op, "当前洞察之眼模式或建队选项不可用。")
	}
	if !o.in(m.Rules.RecruitingArea) {
		return g.oculusReject(op, "请在洞察之眼招募区域创建队伍。")
	}
	if o.active && o.phase != oculusFailed && o.phase != oculusCleared {
		if o.character != c.ID || !bytes.Equal(o.request, body) {
			return g.oculusReject(op, "已有其他洞察之眼队伍，请先退出当前队伍。")
		}
	} else {
		if o.index == 65535 {
			return g.oculusReject(op, "请重新登录后再创建洞察之眼队伍。")
		}
		id, err := newOculusId()
		if err != nil {
			return err
		}
		o.index++
		o.id, o.active, o.phase, o.mode, o.character, o.request = id, true, oculusRecruit, req.RaidMode, c.ID, bytes.Clone(body)
		o.selected, o.movie, o.parked = false, false, nil
		o.coins, o.used, o.prayer = m.SourceCoinLimit, 0, 0
		o.reductions = nil
	}
	if err := g.send(1, 12, []byte{1}); err != nil {
		return err
	}
	return g.send(0, 9, oculusPartyBody(o.index, c.ID, o.mode, byte(g.channel), g.server.nativeParty))
}

func oculusPartyBody(index, char, mode uint16, channel byte, native bool) []byte {
	le := binary.LittleEndian
	b := le.AppendUint16(le.AppendUint16(le.AppendUint16(nil, 1), 9999), index)
	b = append(b, 0, channel, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	size := byte(1)
	if mode == 0 {
		size = 4
	}
	b = append(b, 0, size, 0, 0, 0, 0, 5, 0, 0, 0, 6)
	b = le.AppendUint16(b, mode)
	b = append(b, make([]byte, 13)...)
	b = append(b, 1, 0)
	b = le.AppendUint16(b, char)
	b = append(b, make([]byte, 18)...)
	b = append(b, 6, 0, 0, 0, 0, 0, 0, 0)
	if native {
		return b
	}
	out := append([]byte(nil), b[:10]...)
	out = append(out, b[12:31]...)
	out = append(out, b[44:58]...)
	out = append(out, b[59:62]...)
	return append(out, b[73])
}

func (o *oculusSession) state(kind, subtype, elapsed uint32) []byte {
	le := binary.LittleEndian
	b := le.AppendUint32(le.AppendUint32(nil, kind), subtype)
	return le.AppendUint32(append(b, o.prayer, o.coins, o.used), elapsed)
}

func oculusCounters(z storage.OculusCounts) []byte {
	out := make([]byte, 22)
	for _, i := range []int{0, 2, 6} {
		out[i], out[7+i] = z.DailyUsed, z.WeeklyUsed
	}
	return out
}

func (g *game) oculusRequest(ctx context.Context, f protocol.Frame) error {
	if f.Opcode == 1645 {
		if !bytes.Equal(f.Body, []byte{1}) {
			return storage.ErrInvalid
		}
		run := g.ordinary.Run
		o := &g.oculus
		if g.channel != 63 || run == nil || run.OculusEntry == nil || run.Settlement.Confirmation == nil || !run.Cleared ||
			o.id != run.ID || o.phase != oculusCleared || o.movie || o.mode != 0 && o.mode != 2 && o.mode != 6 {
			return nil
		}
		c, err := g.readOculusCharacter(ctx)
		if err != nil {
			return err
		}
		s := &run.Settlement
		input, err := g.settlementInput(ctx, run, c, s.Boss, s.Confirmation)
		if errors.Is(err, storage.ErrConflict) || errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(input, []byte(s.EntryInput)) {
			return nil
		}
		_, err = g.server.DB.FrozenDungeonResult(ctx, g.lease, c, g.settlementKey(run, "dungeon-settlement"),
			s.EntryInput, s.EntryResult, "")
		if errors.Is(err, storage.ErrConflict) || errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		o.movie = true
		return g.send(0, 31, []byte{0})
	}
	if len(f.Body) != 0 {
		return storage.ErrInvalid
	}
	c, err := g.readOculusCharacter(ctx)
	if errors.Is(err, storage.ErrPhase) {
		return g.oculusReject(f.Opcode, "请先创建洞察之眼队伍。")
	}
	if err != nil {
		return err
	}
	o := &g.oculus
	m, ok := g.server.Characters.Dungeons.Oculus[o.mode]
	if !ok || !o.active || o.character != c.ID {
		return g.oculusReject(f.Opcode, "请先创建洞察之眼队伍。")
	}
	if f.Opcode == 1735 {
		switch o.phase {
		case oculusFailed:
			return g.send(1, 1735, []byte{1})
		case oculusRunning:
			return g.endOculus(ctx, c, false, true)
		case oculusRecruit, oculusStarting:
			o.phase, o.armed, o.selected = oculusFailed, time.Time{}, false
			if err := g.send(1, 1735, []byte{1}); err != nil {
				return err
			}
			return g.send(0, 1771, o.state(5, 2, 0))
		default:
			return g.oculusReject(1735, "")
		}
	}
	if !o.in(m.Rules.WaitingArea) {
		return g.oculusReject(1736, "请进入当前队伍的等候室后再开始任务。")
	}
	if o.phase == oculusStarting {
		return g.send(1, 1736, []byte{1})
	}
	if o.phase != oculusRecruit || m.Rules.MinimumMembers != 1 {
		return g.oculusReject(1736, "队伍状态或人数不符合当前模式的入场要求。")
	}
	_, err = g.server.DB.OculusStatus(ctx, g.lease, c, o.mode, m.Rules.NeedFatigue, byte(m.Rules.DailyEntryLimit), byte(m.Rules.WeeklyEntryLimit), time.Now())
	switch {
	case errors.Is(err, storage.ErrFunds):
		return g.oculusReject(1736, "疲劳值不足，无法开始洞察之眼任务。")
	case errors.Is(err, storage.ErrCapacity):
		return g.oculusReject(1736, "洞察之眼的每日或每周入场次数已用尽。")
	case errors.Is(err, storage.ErrConflict):
		return g.oculusReject(1736, "本周洞察之眼引导模式已绑定四个角色，请使用已绑定角色挑战。")
	case errors.Is(err, storage.ErrPhase):
		return g.oculusReject(1736, "角色不满足洞察之眼的频道或入场条件。")
	case err != nil:
		return err
	}
	o.phase, o.armed, o.selected = oculusStarting, time.Now(), false
	if err := g.send(1, 1736, []byte{1}); err != nil {
		return err
	}
	return g.send(0, 1771, o.state(1, 0, 0))
}

func (g *game) oculusSelector(ctx context.Context, c storage.Character, a protocol.Actor, f protocol.Frame) error {
	o := &g.oculus
	m, ok := g.server.Characters.Dungeons.Oculus[o.mode]
	if f.Opcode == 15 {
		id, err := dungeon.ReadSelector(f.Body)
		if err != nil {
			return err
		}
		if !ok || !o.active || o.character != c.ID || !o.in(m.Rules.WaitingArea) || id != m.Dungeon.ID || m.Rules.MinimumMembers != 1 ||
			o.phase != oculusStarting && (o.phase != oculusRunning || o.parked == nil) || o.phase == oculusRunning && time.Now().After(o.ritualUntil) {
			return g.send(1, 15, []byte{0, 4, 0})
		}
		o.selected = true
		return g.send(1, 15, []byte{1})
	}
	req, err := dungeon.ReadEntry(f.Body)
	if err != nil {
		return err
	}
	if !ok || !o.active || o.character != c.ID || !o.in(m.Rules.WaitingArea) || req.Dungeon != m.Dungeon.ID || !req.Supported() ||
		req.Difficulty != 4 || req.ChallengeStage != 0 {
		return g.send(1, 16, []byte{0, 4, 0, 0})
	}
	if run := g.ordinary.Run; run != nil {
		if run.OculusEntry != nil && run.EntryReplay(req) {
			if _, err := g.oculusSceneHistory(ctx, run, c); err != nil {
				return err
			}
			return g.send(1, 16, dungeon.EntryAccepted(c.ID))
		}
		return g.send(1, 16, []byte{0, 4, 0, 0})
	}
	if !o.selected {
		return g.send(1, 16, []byte{0, 4, 0, 0})
	}
	if o.phase == oculusRunning && o.parked != nil {
		return g.resumeOculus(ctx, c, a, m)
	}
	if o.phase != oculusStarting {
		return g.send(1, 16, []byte{0, 4, 0, 0})
	}
	return g.admitOculus(ctx, c, a, m, false)
}

func (g *game) admitOculus(ctx context.Context, c storage.Character, a protocol.Actor, m dungeon.OculusMode, automatic bool) error {
	reject := func(code byte) error {
		if automatic {
			g.oculus.phase = oculusFailed
			return g.send(0, 1771, g.oculus.state(5, 2, 0))
		}
		return g.send(1, 16, []byte{0, code, 0, 0})
	}
	o := &g.oculus
	seed, err := dungeon.NewSeed()
	if err != nil {
		return err
	}
	run, next, err := dungeon.PrepareOculus(m, o.id, a, max(g.nextActor, 402), seed)
	if err != nil {
		return reject(4)
	}
	entry := storage.OculusEntry{Policy: storage.OculusEntryPolicy, Owner: g.lease.Owner, Epoch: g.lease.Epoch, CharacterId: c.ID,
		SelectionGeneration: o.generation, RunId: o.id, Mode: o.mode, Dungeon: run.Definition.Dungeon.ID, ModeHash: m.SourceHash, DungeonHash: run.Definition.Dungeon.ResourceHash,
		Maze: run.Definition.Dungeon.MazeIndex, Seed: seed, ActorFirst: run.FirstActor(), StartX: run.Room.X, StartY: run.Room.Y, StartMap: run.Room.MapID,
		DailyLimit: byte(m.Rules.DailyEntryLimit), WeeklyLimit: byte(m.Rules.WeeklyEntryLimit), FatigueCost: m.Rules.NeedFatigue}
	entry, counts, fatigue, err := g.server.DB.AdmitOculus(ctx, g.lease, c, entry, time.Now())
	if errors.Is(err, storage.ErrFunds) {
		return reject(22)
	}
	if errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrCapacity) || errors.Is(err, storage.ErrConflict) {
		return reject(4)
	}
	if err != nil {
		return err
	}
	a.FatigueLimit, a.FatigueUsed = fatigue.Limit, fatigue.Limit-fatigue.Remaining
	o.phase, o.armed, o.ritualStarted, o.ritualUntil = oculusRunning, time.Time{}, run.Started, run.Started.Add(time.Duration(m.Rules.PlayTimeSeconds)*time.Second)
	g.ordinary.Run, g.nextActor, run.OculusEntry = run, next, &entry
	run.OculusGeneration = 1
	return g.projectOculusEntry(run, a, automatic, &counts)
}

func (g *game) projectOculusEntry(run *dungeon.Run, a protocol.Actor, loader bool, counts *storage.OculusCounts) error {
	frames, err := run.EntryFrames(a, g.server.native140, true)
	if err != nil {
		return err
	}
	out := make([]opening.Packet, 0, len(frames)+4)
	if loader {
		out = append(out, opening.Packet{Opcode: 27, Body: dungeon.SelectorPreparation()})
	}
	for _, f := range frames {
		if loader && counts != nil && f.Class == 1 && f.Opcode == 16 {
			continue
		}
		if f.Class == 0 && f.Opcode == 28 {
			out = append(out, opening.Packet{Opcode: 1770, Body: run.OculusRooms()})
		}
		out = append(out, f)
	}
	out = append(out, opening.Packet{Opcode: 1771, Body: g.oculus.state(2, 0, 0)})
	if counts != nil {
		out = append(out, opening.Packet{Opcode: 1769, Body: oculusCounters(*counts)})
	}
	return g.packets(out)
}

func (g *game) oculusMode() (dungeon.OculusMode, bool) {
	if g.server.Characters.Dungeons == nil {
		return dungeon.OculusMode{}, false
	}
	mode, ok := g.server.Characters.Dungeons.Oculus[g.oculus.mode]
	return mode, ok
}

func (o *oculusSession) idKey() string { return strings.ReplaceAll(o.id, "-", "") }
