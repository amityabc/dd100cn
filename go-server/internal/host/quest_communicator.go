package host

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

func (g *game) questCommunicator(ctx context.Context, body []byte) error {
	if len(body) != 19 || body[2] != 0 || binary.LittleEndian.Uint32(body[3:]) != 0 || binary.LittleEndian.Uint32(body[7:]) != 239 || binary.LittleEndian.Uint64(body[11:]) != 0 {
		return storage.ErrInvalid
	}
	slot := binary.LittleEndian.Uint16(body)
	ack := func(success bool) error {
		b := []byte{1}
		if !success {
			b = []byte{0, 0, 0}
		}
		b = append(binary.LittleEndian.AppendUint16(b, slot), 0)
		return g.send(1, 507, binary.LittleEndian.AppendUint32(b, 239))
	}
	if g.channel != 19 || g.opening != nil || g.ordinary.Active() {
		return ack(false)
	}
	cat := g.server.Characters
	now := time.Now()
	var next questwire.CommunicatorReplay
	out, err := g.server.DB.ChangeTownQuest(ctx, g.lease, cat.Weights, func(c storage.Character, in storage.Inventory, rs map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		q, err := resources.ReadTownQuests(c.Gameplay)
		if err != nil {
			return storage.TownQuestMutation{}, err
		}
		allows := func(c storage.Character, p storage.Position) bool {
			return cat.AllowsTownPosition(c.Level, q, point(p))
		}
		isRoom := func(p storage.Position) bool { return cat.Towns.IsRoom(point(p)) }
		m, replay, err := cat.Quests.UseCommunicator(c, in, rs, slot, now, g.ordinary.Returning(), g.questCommunicatorReplay, allows, isRoom)
		next = replay
		return m, err
	})
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) {
		return ack(false)
	}
	if err != nil {
		return err
	}
	g.questCommunicatorReplay = next
	g.ordinary.BeginReturn()
	a, _, err := readActor(out.Character, g.channel)
	if err != nil {
		return err
	}
	if err = ack(true); err != nil {
		return err
	}
	if err = g.send(0, 23, protocol.Area(a)); err != nil {
		return err
	}
	return g.send(0, 24, protocol.Town(a))
}
