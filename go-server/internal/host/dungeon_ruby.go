package host

import (
	"context"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/ruby"
	"s9a1/server/internal/storage"
)

func (g *game) readRubyLedger(ctx context.Context, c storage.Character) (ruby.Ledger, error) {
	return g.server.DB.RubyLedger(ctx, g.lease, c, time.Now())
}

func (g *game) validateRubyClear(ctx context.Context, c storage.Character, run *dungeon.Run) (ruby.Ledger, error) {
	if run.RubyEntry == nil || !run.Ready || !run.Cleared || run.PlayerDeath != nil {
		return ruby.Ledger{}, storage.ErrPhase
	}
	l, err := g.readRubyLedger(ctx, c)
	if err != nil {
		return ruby.Ledger{}, err
	}
	clear, ok := l.Clears[ruby.ClearKey(*run.RubyEntry)]
	if !ok || clear.Run != run.ID {
		return ruby.Ledger{}, storage.ErrConflict
	}
	return l, nil
}

func (g *game) rubyProgressFrames(c storage.Character, l ruby.Ledger, run *dungeon.Run) ([]opening.Packet, error) {
	var actual uint32
	if run == nil {
		actual = ruby.FloorID(min(60, l.Cleared+1))
	} else {
		actual = run.Definition.Dungeon.ID
	}
	if l.Cleared < 60 && (run == nil || run.Cleared && !ruby.Hidden(actual)) {
		next, err := l.Next(9700)
		if err != nil {
			return nil, err
		}
		actual = next
	}
	progress, floor, treasure, err := l.ProgressWire(actual)
	if err != nil {
		return nil, err
	}
	g.rubyView = &rubySelection{Character: c.ID, View: l.View}
	return []opening.Packet{{Opcode: 1736, Body: progress}, {Opcode: 1737, Body: floor}, {Opcode: 1738, Body: treasure}}, nil
}

func (g *game) rubyClearProjection(ctx context.Context, run *dungeon.Run) error {
	r, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(r)
	if err != nil {
		return err
	}
	l, err := g.validateRubyClear(ctx, c, run)
	if err != nil {
		return err
	}
	frames, err := g.rubyProgressFrames(c, l, run)
	if err != nil {
		return err
	}
	return g.packets(append(frames, opening.Packet{Opcode: 31, Body: []byte{0}}))
}

func rubyEntryGrant(c storage.Character, l ruby.Ledger, run *dungeon.Run) (*ruby.Entry, error) {
	entry, err := l.Admission(c.ID, run.ID, run.Requested.Dungeon, run.Definition.Dungeon.ID, run.Seed, run.Definition.Dungeon.ResourceHash)
	return &entry, err
}

func bindRubyDeath(run *dungeon.Run, z dungeon.DeathResult, change *storage.DungeonChange, resources *ruby.Resources) {
	change.Proof = append(change.Proof, ruby.EntryKey(run.ID))
	change.Ruby = &storage.RubyMutation{Entry: *run.RubyEntry, Clear: z.DungeonCleared, Resources: resources, Now: time.Now()}
}
