package host

import (
	"context"
	"time"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) trackDungeonLuck() {
	if g.server.delegated && g.dungeonLuckAt.IsZero() && g.catalog().Dungeons != nil &&
		g.catalog().Dungeons.Luck.PlayIntervalMinutes > 0 {
		g.dungeonLuckAt = time.Now().Add(time.Minute)
	}
}

func (g *game) dungeonDeadline() time.Time {
	r := g.ordinary.Run
	at := g.dungeonLuckAt
	death := r.DeathDeadline()
	if r != nil && r.ID == g.fateSession.Run {
		death = time.Time{}
	}
	for _, next := range [...]time.Time{death, r.CardDeadline(), r.AbaddonDeadline(), g.oculusDeadline()} {
		if !next.IsZero() && (at.IsZero() || next.Before(at)) {
			at = next
		}
	}
	return at
}

func (g *game) dungeonTick(ctx context.Context) error {
	if !g.server.delegated {
		return nil
	}
	if now := time.Now(); !g.dungeonLuckAt.IsZero() && !now.Before(g.dungeonLuckAt) {
		roster, err := g.server.DB.Current(ctx, g.lease)
		if err != nil {
			return err
		}
		g.dungeonLuckAt = time.Time{}
		if roster.Phase == storage.TownReady {
			character, err := selected(roster)
			if err != nil {
				return err
			}
			luck := g.catalog().Dungeons.Luck
			if err := g.server.DB.AccrueDungeonPlayMinute(ctx, g.lease, character, luck.PlayIntervalMinutes, luck.PointsPerPlayInterval); err != nil {
				return err
			}
			g.dungeonLuckAt = now.Add(time.Minute)
		}
	}
	sequence := g.seq
	g.seq = 0
	defer func() { g.seq = sequence }()
	if at, now := g.oculusDeadline(), time.Now(); !at.IsZero() && !now.Before(at) {
		return g.oculusTick(ctx, now)
	}
	r := g.ordinary.Run
	if r == nil {
		return nil
	}
	if at := r.AbaddonDeadline(); !at.IsZero() && !time.Now().Before(at) {
		return g.dungeonAbaddonTick(ctx, r)
	}
	if r.ID != g.fateSession.Run && r.DeathDue() {
		if r.OculusEntry != nil {
			c, err := g.readOculusCharacter(ctx)
			if err != nil {
				return err
			}
			return g.parkOculus(ctx, r, c, nil)
		}
		if r.AbaddonEntry != nil {
			return g.reviveAbaddon(ctx, r, false)
		}
		return g.dungeonRequest(ctx, protocol.Frame{Class: 1, Opcode: 42})
	}
	if at := r.CardDeadline(); !at.IsZero() && !time.Now().Before(at) {
		roster, err := g.server.DB.Current(ctx, g.lease)
		if err != nil {
			return err
		}
		c, err := selected(roster)
		if err != nil {
			return err
		}
		return g.claimDungeonCard(ctx, r, c, false, true)
	}
	return nil
}
