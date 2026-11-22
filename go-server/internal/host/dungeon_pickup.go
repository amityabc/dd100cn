package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/savejson"
	"s9a1/server/internal/storage"
)

func (g *game) dungeonPickup(ctx context.Context, run *dungeon.Run, c storage.Character, body []byte) error {
	drop, valid, replay, err := run.Pickup(body)
	if err != nil {
		return err
	}
	reject := func(code byte) error { return g.send(1, 43, []byte{0, code, 0}) }
	now := time.Now()
	if !valid || !g.server.delegated || drop.Reward.ExpiresAt != nil && *drop.Reward.ExpiresAt <= now.Unix() {
		return reject(4)
	}
	def := run.Definition.Dungeon
	row, err := json.Marshal(drop)
	if err != nil {
		return err
	}
	input := storage.DungeonPickupInput{Version: "local100-ground-pickup/v1", Account: savejson.String(g.lease.Account), OwnerGuid: g.lease.Owner,
		Epoch: g.lease.Epoch, Character: c.ID, RunId: run.ID, Dungeon: def.ID, Maze: def.MazeIndex, MapId: run.Room.MapID, Drop: row,
		DropRuleHash: run.GroundSourceHash(drop), Body: savejson.String(base64.StdEncoding.EncodeToString(body))}
	out, err := g.server.DB.PickupDungeon(ctx, g.lease, c, storage.DungeonPickup{
		Key: run.PickupKey(drop.ObjectKey), OriginKey: drop.DeathKey, Input: input,
		Gold: int64(drop.Reward.Gold), Replay: replay, Now: now, Create: drop.Grant, Wisdom: run.WisdomEntry,
	})
	if errors.Is(err, storage.ErrCapacity) || errors.Is(err, storage.ErrFunds) {
		return reject(22)
	}
	if errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrInvalid) {
		return reject(4)
	}
	if err != nil {
		return err
	}
	run.ApplyPickup(drop.ObjectKey, body)
	c = out.Character
	bags, err := inventorywire.BagAndWear(out.Inventory, c)
	if err != nil {
		return err
	}
	frames := []opening.Packet{{Class: 1, Opcode: 43, Body: []byte{1}}}
	if !replay {
		frames = append(frames, opening.Packet{Opcode: 39, Body: drop.PickupNotice(out.Slot)})
	}
	delta, err := inventorywire.DungeonBagDelta(out.Inventory, c, out.Grants, drop.Reward.Gold != 0)
	if err != nil {
		return err
	}
	frames = append(frames, opening.Packet{Opcode: 14, Body: delta})
	if drop.Reward.Artifact != nil {
		pets, err := inventorywire.CommerceSpecial(out.Inventory, c)
		if err != nil {
			return err
		}
		frames = append(frames, opening.Packet{Opcode: pets[1].Opcode, Body: pets[1].Body})
	}
	if drop.IsMedal() {
		update, err := inventorywire.DungeonMedalDelta(c, out.Grants)
		if err != nil {
			return err
		}
		medals, err := inventorywire.CommerceMedal(out.Inventory, c)
		if err != nil {
			return err
		}
		frames = append(frames, opening.Packet{Opcode: 14, Body: update}, opening.Packet{Opcode: medals.Opcode, Body: medals.Body})
	}
	return g.packets(append(frames, opening.Packet{Opcode: bags[0].Opcode, Body: bags[0].Body}))
}
