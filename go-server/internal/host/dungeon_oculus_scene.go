package host

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) oculusDeadline() time.Time {
	o := &g.oculus
	if o.phase == oculusStarting && (o.mode == 2 || o.mode == 6) {
		return o.armed.Add(5 * time.Second)
	}
	if o.phase != oculusRunning || o.ritualUntil.IsZero() {
		return time.Time{}
	}
	m, ok := g.oculusMode()
	if !ok {
		return time.Time{}
	}
	full := time.Duration(m.Rules.PlayTimeSeconds) * time.Second
	return o.ritualUntil.Add(-full + time.Duration(int64(full)*int64(o.prayer+1)/100))
}

func (g *game) oculusTick(ctx context.Context, now time.Time) error {
	o := &g.oculus
	m, ok := g.oculusMode()
	if !ok || !o.active {
		return nil
	}
	switch o.phase {
	case oculusStarting:
		c, err := g.readOculusCharacter(ctx)
		if err != nil {
			return err
		}
		a, _, err := readActor(c, g.channel)
		if err != nil {
			return err
		}
		return g.admitOculus(ctx, c, a, m, true)
	case oculusRunning:
		if !now.Before(o.ritualUntil) {
			c, err := g.readOculusCharacter(ctx)
			if err != nil {
				return err
			}
			return g.endOculus(ctx, c, true, false)
		}
		old := o.prayer
		o.updatePrayer(m, now)
		if o.prayer != old {
			return g.send(0, 1771, o.state(2, 0, 0))
		}
	}
	return nil
}

func (o *oculusSession) updatePrayer(m dungeon.OculusMode, now time.Time) {
	full := int64(m.Rules.PlayTimeSeconds) * 1000
	left := min(full, int64((max(0, o.ritualUntil.Sub(now))+time.Millisecond-1)/time.Millisecond))
	o.prayer = byte((full - left) * 100 / full)
}

func (g *game) oculusCommittedDeath(run *dungeon.Run, death dungeon.Death, z dungeon.DeathResult) ([]opening.Packet, error) {
	if run.OculusEntry == nil {
		return nil, nil
	}
	o := &g.oculus
	m, ok := g.oculusMode()
	if !ok || o.id != run.ID {
		return nil, storage.ErrConflict
	}
	var frames []opening.Packet
	if run.OculusNamedDeath(death) {
		frames = append(frames, opening.Packet{Opcode: 1770, Body: run.OculusRooms()})
	}
	if death.Prior != nil {
		return frames, nil
	}
	now := time.Now()
	for _, r := range m.Rules.KillCoinLimitBonuses {
		if r.Id == death.Monster.Code {
			if r.RawValue <= 0 || int(o.coins)+r.RawValue > 255 {
				return nil, storage.ErrConflict
			}
			o.coins += byte(r.RawValue)
			break
		}
	}
	if !z.DungeonCleared {
		for _, r := range m.Rules.KillTimeoverBonuses {
			if r.Id != death.Monster.Code {
				continue
			}
			full := time.Duration(m.Rules.PlayTimeSeconds) * time.Second
			until := o.ritualUntil.Add(full * time.Duration(r.RawValue) / 100)
			o.ritualUntil = minTime(until, now.Add(full))
			o.reductions = append(o.reductions, storage.OculusRitualReduction{Actor: death.Actor, Monster: death.Monster.Code,
				DeathKey: death.Key, Percent: r.RawValue, AtTimestamp: int64(now.Sub(o.ritualStarted))})
			break
		}
	}
	o.updatePrayer(m, now)
	if z.DungeonCleared {
		o.phase, o.clearedAt = oculusCleared, now
		return frames, nil
	}
	return append(frames, opening.Packet{Opcode: 1771, Body: o.state(2, 0, 0)}), nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (g *game) oculusSceneHistory(ctx context.Context, run *dungeon.Run, c storage.Character) (dungeon.OculusSceneHistory, error) {
	if run == nil || run.OculusEntry == nil {
		return dungeon.OculusSceneHistory{}, storage.ErrConflict
	}
	receipts, err := g.server.DB.OculusSceneReceipts(ctx, g.lease, c, *run.OculusEntry)
	if err != nil {
		return dungeon.OculusSceneHistory{}, err
	}
	return run.OculusHistory(receipts)
}

func (o *oculusSession) ritualProof(now time.Time, mode dungeon.OculusMode) storage.OculusRitualProof {
	left := max(time.Duration(0), o.ritualUntil.Sub(now))
	return storage.OculusRitualProof{Policy: "OculusCommittedRitualClock/v1", RunId: o.id, Mode: o.mode, Dungeon: mode.Dungeon.ID,
		ResourceHash: mode.SourceHash, ObservedAt: int64(now.Sub(o.ritualStarted)), Frequency: int64(time.Second), LimitSeconds: mode.Rules.PlayTimeSeconds,
		Reductions: append([]storage.OculusRitualReduction{}, o.reductions...), RemainingMilliseconds: int64((left + time.Millisecond - 1) / time.Millisecond)}
}

func (g *game) oculusClearQualification(ctx context.Context, run *dungeon.Run, c storage.Character, actor uint16) (*storage.OculusClearQualification, error) {
	o := &g.oculus
	mode, ok := g.oculusMode()
	def := run.Definition.Dungeon
	p, err := readGameplay(c.Gameplay)
	if err != nil {
		return nil, err
	}
	if !ok || run.OculusEntry == nil || !p.Initialized || p.Progression.Hp == 0 || !run.Ready || !run.Cleared || run.PlayerDeath != nil ||
		def.UsesOrdinaryRewards || def.ID != mode.Dungeon.ID || run.Character != c.ID || o.character != c.ID || o.phase != oculusCleared || o.id != run.ID ||
		o.clearedAt.IsZero() || !o.clearedAt.Before(o.ritualUntil) || run.Room == nil || run.Room.MapID != mode.ClearMapId ||
		run.Room.X != def.BossX || run.Room.Y != def.BossY || o.ritualStarted != run.Started {
		return nil, storage.ErrConflict
	}
	monster, boss, dead := run.CurrentBoss(actor)
	if !boss || !dead || monster.Type != 3 || monster.Code != mode.Rules.BossMonsterId {
		return nil, storage.ErrConflict
	}
	receipts, err := g.server.DB.OculusSceneReceipts(ctx, g.lease, c, *run.OculusEntry)
	if err != nil {
		return nil, err
	}
	history, err := run.OculusHistory(receipts)
	if err != nil {
		return nil, err
	}
	ritual := o.ritualProof(o.clearedAt, mode)
	if !ritual.ValidClock() || ritual.RemainingMilliseconds == 0 {
		return nil, storage.ErrConflict
	}
	for _, reduction := range ritual.Reductions {
		row, exists := receipts[reduction.DeathKey]
		var source struct {
			RunId string
			Actor uint16
			Code  uint32
		}
		if !exists || json.Unmarshal([]byte(row.Input), &source) != nil || source.RunId != run.ID || source.Actor != reduction.Actor || source.Code != reduction.Monster {
			return nil, storage.ErrConflict
		}
		matched := false
		for _, bonus := range mode.Rules.KillTimeoverBonuses {
			if bonus.Id == source.Code && bonus.RawValue == reduction.Percent {
				matched = true
				break
			}
		}
		if !matched {
			return nil, storage.ErrConflict
		}
	}
	coins, err := g.oculusClearCoins(ctx, run, c, mode, receipts)
	if err != nil {
		return nil, err
	}
	if coins.Total != o.coins || coins.Used != o.used {
		return nil, storage.ErrConflict
	}
	admission, err := json.Marshal(run.OculusEntry)
	if err != nil {
		return nil, err
	}
	return &storage.OculusClearQualification{Policy: storage.OculusClearPolicy, RunId: run.ID, Mode: mode.Mode, Dungeon: def.ID,
		ModeHash: mode.SourceHash, SceneHash: history.DefinitionHash, SceneGeneration: history.Generation, ActorFirst: history.ActorFirst,
		AdmissionInput: string(admission), Ritual: ritual, Coins: coins}, nil
}

func (g *game) endOculus(ctx context.Context, c storage.Character, timeout, reply bool) error {
	o := &g.oculus
	run := g.ordinary.Run
	if run == nil {
		run = o.parked
	}
	m, ok := g.oculusMode()
	if !ok || run == nil || run.OculusEntry == nil || run.Cleared || o.id != run.ID || o.phase != oculusRunning {
		if reply {
			return g.oculusReject(1735, "")
		}
		return storage.ErrPhase
	}
	now := time.Now()
	timeout = timeout || !now.Before(o.ritualUntil)
	if _, err := g.oculusSceneHistory(ctx, run, c); err != nil {
		return err
	}
	if err := g.server.DB.EndOculus(ctx, g.lease, c, *run.OculusEntry, o.ritualProof(now, m), timeout, now); err != nil {
		return err
	}
	o.phase, o.parked, o.selected = oculusFailed, nil, false
	if timeout {
		o.prayer = 100
	}
	g.ordinary.BeginReturn()
	if reply {
		if err := g.send(1, 1735, []byte{1}); err != nil {
			return err
		}
	}
	if err := g.send(0, 1771, o.state(5, 2, 0)); err != nil {
		return err
	}
	return g.projectOculusWaiting(c, m)
}

func (g *game) projectOculusWaiting(c storage.Character, m dungeon.OculusMode) error {
	p := m.Rules.WaitingArea
	g.oculus.position = &storage.Position{Town: uint16(p.Town), Area: uint16(p.Area), X: p.X, Y: p.Y, Direction: 5}
	return g.oculusWaitingPackets(c, m)
}

func (g *game) oculusWaitingPackets(c storage.Character, m dungeon.OculusMode) error {
	p := m.Rules.WaitingArea
	a, _, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	a.Town, a.Area, a.X, a.Y, a.Direction, a.AreaState = p.Town, p.Area, p.X, p.Y, 5, 0
	return g.packets([]opening.Packet{{Opcode: 23, Body: protocol.Area(a)}, {Opcode: 24, Body: protocol.Town(a)}})
}

func (g *game) oculusTownActor(a protocol.Actor) protocol.Actor {
	if p := g.oculus.position; p != nil {
		a.Town, a.Area, a.X, a.Y, a.Direction = byte(p.Town), byte(p.Area), p.X, p.Y, p.Direction
	}
	return a
}

func (g *game) oculusRetreatExit(ctx context.Context, run *dungeon.Run, c storage.Character, body []byte) error {
	reject := func() error { return g.send(1, 72, []byte{0, 0, 0, body[1]}) }
	o := &g.oculus
	if !o.active || o.phase != oculusRunning || o.character != c.ID || !bytes.Equal(body, []byte{1, 2, 1}) ||
		!time.Now().Before(o.ritualUntil) {
		return reject()
	}
	profile, err := readGameplay(c.Gameplay)
	if err != nil {
		return err
	}
	if profile.Progression.Hp == 0 {
		return reject()
	}
	if run == nil {
		if o.parked == nil || !o.parked.CanChangeEquipment() || o.parked.ID != o.id || o.parked.Character != c.ID {
			return reject()
		}
		_, err = g.oculusSceneHistory(ctx, o.parked, c)
	} else if !run.CanChangeEquipment() || run.Settlement.Confirmation != nil {
		return reject()
	} else {
		err = g.parkOculus(ctx, run, c, body)
	}
	if errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, sql.ErrNoRows) {
		return reject()
	}
	if err == nil && run == nil {
		return g.send(1, 72, []byte{1, 1, 2})
	}
	return err
}

func (g *game) parkOculus(ctx context.Context, run *dungeon.Run, c storage.Character, quit []byte) error {
	return g.parkOculusTransition(ctx, run, c, quit, false)
}

func (g *game) parkOculusTransition(ctx context.Context, run *dungeon.Run, c storage.Character, quit []byte, refusedRevival bool) error {
	o := &g.oculus
	m, ok := g.oculusMode()
	if !ok || run == nil || run.OculusEntry == nil || o.phase != oculusRunning || o.id != run.ID || run.Cleared || !run.Ready || run.InLayer ||
		(len(quit) == 0) != (run.PlayerDeath != nil) || len(quit) != 0 && !bytes.Equal(quit, []byte{1, 2, 1}) {
		return storage.ErrPhase
	}
	if !time.Now().Before(o.ritualUntil) {
		if len(quit) != 0 {
			return storage.ErrPhase
		}
		return g.endOculus(ctx, c, true, false)
	}
	if _, err := g.oculusSceneHistory(ctx, run, c); err != nil {
		return err
	}
	if err := g.server.DB.ReturnDungeon(ctx, g.lease, c, true); err != nil {
		return err
	}
	run.Revive()
	run.LastRevived = false
	o.parked, o.selected = run, false
	g.ordinary.BeginReturn()
	if refusedRevival {
		if err := g.send(1, 41, []byte{0, 0, 0}); err != nil {
			return err
		}
	}
	if len(quit) != 0 {
		if err := g.send(1, 72, []byte{1, 1, 2}); err != nil {
			return err
		}
	}
	return g.projectOculusWaiting(c, m)
}

func (g *game) resumeOculus(ctx context.Context, c storage.Character, a protocol.Actor, m dungeon.OculusMode) error {
	o := &g.oculus
	run := o.parked
	if run == nil || run.OculusEntry == nil || !time.Now().Before(o.ritualUntil) || g.ordinary.Returning() {
		return g.send(1, 16, []byte{0, 4, 0, 0})
	}
	history, err := g.oculusSceneHistory(ctx, run, c)
	if err != nil {
		return err
	}
	resumed, next, err := run.ResumeOculus(max(g.nextActor, 402), a, history)
	if err != nil {
		return g.send(1, 16, []byte{0, 4, 0, 0})
	}
	o.parked, o.selected = nil, false
	o.updatePrayer(m, time.Now())
	g.ordinary.Run, g.nextActor = resumed, next
	return g.projectOculusEntry(resumed, a, true, nil)
}
