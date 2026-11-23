package host

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) trackPetCare(c storage.Character) {
	g.petClockTracked = storage.HasCaredPet(c, g.catalog().Commerce.PetCare)
}

func (g *game) petClockMode(pause bool) storage.PetClockMode {
	if pause || g.ordinary.Returning() {
		return storage.PetPaused
	}
	if run := g.ordinary.Run; run != nil {
		if run.InLayer || run.Definition.Dungeon.Tournament != nil {
			return storage.PetPaused
		}
		if run.PetClockSpending() {
			return storage.PetSpending
		}
	}
	if g.opening != nil && g.opening.Ready() {
		return storage.PetSpending
	}
	return storage.PetRecovering
}

func (g *game) syncPetClock(ctx context.Context, pause, silent bool) error {
	if !g.petClockTracked {
		return nil
	}
	rule := g.catalog().Commerce.PetCare
	duel := duellobby.IsChannel(g.channel) && g.duelPosition != nil
	updates, err := g.server.DB.SyncPetClock(ctx, g.lease, rule, g.petClockMode(pause), duel, time.Now())
	if errors.Is(err, storage.ErrPhase) {
		return nil
	}
	if err != nil {
		return err
	}
	if silent {
		return nil
	}
	for _, u := range updates {
		if err = g.petSatiety(u.Serial, u.Value); err != nil {
			return err
		}
	}
	return nil
}

func (g *game) petSatiety(id uint32, n byte) error {
	b := binary.LittleEndian.AppendUint32(nil, id)
	return g.send(0, 103, binary.LittleEndian.AppendUint32(b, uint32(n)))
}

func (g *game) feedPet(ctx context.Context, slot uint16, id uint32, body []byte) error {
	failure := append([]byte{0, 0, 0}, body[2:11]...)
	reject := func() error { return g.send(1, 44, failure) }
	if id != 24 || g.channel != 19 || g.ordinary.Returning() || g.opening != nil && !g.opening.Ready() {
		return reject()
	}
	mode := g.petClockMode(false)
	if mode == storage.PetPaused {
		return reject()
	}
	scene := "pet-town"
	if run := g.ordinary.Run; run != nil {
		if !run.Ready || run.PlayerDeath != nil || run.Cleared || run.Room == nil {
			return reject()
		}
		scene = fmt.Sprintf("pet:%s:%d:%d:%d", run.ID, run.Room.X, run.Room.Y, run.DeathCycle)
	} else if g.opening != nil {
		scene = "pet-opening"
	}
	c := g.catalog().Commerce
	replay := g.commerceState
	if replay.Scene != scene {
		replay = storage.CommerceReplay{}
	}
	out, err := g.server.DB.Commerce(ctx, g.lease, 44, g.seq, body, replay, func(x *storage.CommerceTx) error {
		return x.FeedPet(slot, c.PetFeed, c.PetCare, mode, time.Now())
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	frames, err := g.petInventory(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	var actor protocol.Actor
	if err = inventorywire.SpecialActor(out.Character.Gameplay, &actor, time.Now()); err != nil {
		return err
	}
	if actor.Pet == nil {
		return storage.ErrInvalid
	}
	g.commerceState = out.Replay
	g.commerceState.Scene = scene
	ack := append([]byte{1}, body[:11]...)
	if out.Replayed {
		ack = failure
	}
	if err = g.send(1, 44, ack); err != nil {
		return err
	}
	if err = g.petSatiety(actor.Pet.NativeSerial, actor.Pet.Satiety); err != nil {
		return err
	}
	return g.projected(frames[1:2])
}
