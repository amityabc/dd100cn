package host

import (
	"bytes"
	"context"
	"errors"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func tournamentProof(run *dungeon.Run) (storage.TournamentWinProof, bool) {
	keys, ok := run.TournamentDeathKeys()
	if !ok {
		return storage.TournamentWinProof{}, false
	}
	d := run.Definition.Dungeon
	return storage.TournamentWinProof{RunId: run.ID, Dungeon: d.ID, ResourceHash: d.ResourceHash, Deaths: keys}, true
}

func (g *game) dungeonTournamentCards(ctx context.Context, run *dungeon.Run, c storage.Character, f protocol.Frame) error {
	reject := func() error {
		if f.Opcode == 449 {
			return g.send(1, 449, []byte{0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0})
		}
		return g.send(1, 450, []byte{0, 4, 0})
	}
	if f.Opcode == 449 && len(f.Body) != 0 || f.Opcode == 450 && len(f.Body) != 2 {
		return storage.ErrInvalid
	}
	p, ok := tournamentProof(run)
	if !g.server.delegated || !ok {
		return reject()
	}
	db := g.server.DB
	if f.Opcode == 449 {
		if err := db.ValidateTournamentWins(ctx, g.lease, c, p); err != nil {
			if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrPhase) {
				return reject()
			}
			return err
		}
		return g.send(1, 449, []byte{1, 0, 0, 0, 0, 0, 0, 0, 0})
	}
	if f.Body[0] != 0 || f.Body[1] > 1 {
		return reject()
	}
	in, err := db.ClaimTournament(ctx, g.lease, c, p, f.Body[0], f.Body[1], time.Now(), g.catalog().Dungeons.TournamentGrant)
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrCapacity) {
		return reject()
	}
	if err != nil {
		return err
	}
	bag, err := inventorywire.BagAndWear(in, c)
	if err != nil {
		return err
	}
	ack := []byte{1, 2, 255, 255, 0}
	ack[2+f.Body[1]] = 0
	return g.packets([]opening.Packet{{Opcode: bag[0].Opcode, Body: bag[0].Body}, {Class: 1, Opcode: 450, Body: ack}})
}

func (g *game) dungeonTournamentExit(ctx context.Context, run *dungeon.Run, c storage.Character, a protocol.Actor, body []byte) error {
	reject := func() error { return g.send(1, 72, []byte{0, 4, 0, 0}) }
	if len(body) != 3 || body[0] != 1 && body[0] != 2 || body[1] < 1 || body[1] > 3 || body[2] != 1 {
		return reject()
	}
	p, ok := tournamentProof(run)
	if run == nil && g.ordinary.TournamentExit != nil {
		p, ok = *g.ordinary.TournamentExit, body[0] == 1 && (bytes.Equal(body, g.ordinary.ExitBody) || body[1] == 3)
	}
	if !ok {
		return reject()
	}
	db := g.server.DB
	if err := db.ValidateTournamentClaim(ctx, g.lease, c, p); err != nil {
		if errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrInvalid) {
			return reject()
		}
		return err
	}
	if body[0] == 2 {
		return g.send(1, 72, []byte{1, 2, body[1]})
	}
	if run != nil {
		if err := db.ReturnDungeon(ctx, g.lease, c, true); err != nil {
			return err
		}
		g.ordinary.BeginExit(body)
		g.ordinary.TournamentExit = &p
	}
	a.AreaState = 0
	if err := g.packets([]opening.Packet{{Class: 1, Opcode: 72, Body: []byte{1, 1, body[1]}},
		{Opcode: 23, Body: protocol.Area(a)}, {Opcode: 24, Body: protocol.Town(a)}}); err != nil {
		return err
	}
	return g.specialCounters(ctx, c)
}
