package host

import (
	"context"
	"errors"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/storage"
)

func (g *game) wearEquipment(ctx context.Context, body []byte) error {
	if len(body) != 1 {
		return errors.New("Durability report must contain one actor equipment slot byte")
	}
	var scene storage.WearScene
	if r := g.opening; r != nil {
		id, c, room, ok := r.WearScene()
		if !ok {
			return nil
		}
		scene = storage.WearScene{Run: id, Character: c, Map: room.MapID, X: room.X, Y: room.Y}
	} else if r := g.ordinary.Run; r != nil && r.Ready && r.PlayerDeath == nil && r.Room != nil {
		room := r.CurrentScene()
		if room == nil {
			return nil
		}
		scene = storage.WearScene{Run: r.ID, Character: r.Character, Map: room.MapID, X: room.X, Y: room.Y}
	} else {
		return nil
	}
	out, err := g.server.DB.Wear(ctx, g.lease, scene, body[0], g.seq, g.wearReplay)
	if err != nil || !out.Applied {
		return err
	}
	g.wearReplay = out.Replay
	packet, err := inventorywire.OrdinaryWornUpdate(out.Character, out.Asset)
	if err != nil {
		return err
	}
	return g.send(0, 14, packet)
}
