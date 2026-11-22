package host

import (
	"context"
	"errors"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
	"time"
)

func (g *game) avatarAction(ctx context.Context, body []byte) error {
	act, err := questwire.ReadAvatarAction(body)
	if err != nil {
		return err
	}
	reject := func() error { return g.send(1, 1786, []byte{0, 19, 0}) }
	if !g.server.native140 || !g.commerceInTown() {
		return reject()
	}
	roster, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	if roster.Phase != storage.TownReady {
		return reject()
	}
	c, err := selected(roster)
	if err != nil {
		return err
	}
	q, err := resources.ReadTownQuests(c.Gameplay)
	if err != nil {
		return err
	}
	cat := g.server.Characters
	if !cat.AllowsOrdinaryTownPosition(c.Level, q, point(c.Position)) {
		return reject()
	}
	state, err := cat.Quests.AvatarAction(c, act, time.Now())
	if errors.Is(err, storage.ErrInvalid) {
		return reject()
	}
	if err != nil {
		return err
	}
	a, _, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	g.questAvatarAction = state
	b, err := g.actorMode0(a)
	if err != nil {
		return err
	}
	return g.send(0, 2, b)
}
