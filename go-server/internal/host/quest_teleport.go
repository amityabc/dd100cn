package host

import (
	"context"
	"encoding/json"
	"errors"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
	"time"
)

func (g *game) questTeleport(ctx context.Context, body []byte) error {
	req, err := questwire.ReadTeleport(g.seq, body)
	if err != nil {
		return err
	}
	reject := func() error { return g.send(1, 237, []byte{0, 0, 0}) }
	if g.channel != 19 || g.opening != nil || g.ordinary.Active() {
		return reject()
	}
	cat := g.server.Characters
	now := time.Now()
	var repeated bool
	out, err := g.server.DB.ChangeTownQuest(ctx, g.lease, cat.Weights, func(c storage.Character, in storage.Inventory, _ map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		q, err := resources.ReadTownQuests(c.Gameplay)
		if err != nil {
			return storage.TownQuestMutation{}, err
		}
		allows := func(p storage.Position) bool { return cat.AllowsTownPosition(c.Level, q, point(p)) }
		isRoom := func(p storage.Position) bool { return cat.Towns.IsRoom(point(p)) }
		recordsOrigin := func(p storage.Position) bool { return cat.Towns.RecordsRoomReturnFrom(point(p)) }
		m, replay, err := cat.Quests.UseTeleport(c, in, req, now, g.ordinary.Returning(), g.questTeleportReplay, allows, isRoom, recordsOrigin)
		repeated = replay
		return m, err
	})
	if errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrInvalid) {
		return reject()
	}
	if err != nil {
		return err
	}
	if !repeated {
		g.questTeleportReplay = questwire.TeleportReplay{Request: req, Character: out.Character.ID, Revision: out.Character.Revision, Destination: out.Character.Position}
		g.ordinary.BeginReturn()
	}
	a, _, err := readActor(out.Character, g.channel)
	if err != nil {
		return err
	}
	if err = g.send(1, 237, []byte{1}); err != nil {
		return err
	}
	if err = g.send(0, 23, protocol.Area(a)); err != nil {
		return err
	}
	return g.send(0, 24, protocol.Town(a))
}
