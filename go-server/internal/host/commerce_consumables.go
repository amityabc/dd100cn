package host

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) usePotion(ctx context.Context, body []byte) error {
	le := binary.LittleEndian
	if len(body) != 15 {
		return storage.ErrInvalid
	}
	slot, list, ref, item := le.Uint16(body), body[2], le.Uint32(body[3:]), le.Uint32(body[7:])
	if list != 0 && list != 7 || list == 7 && (slot < 216 || slot > 271) ||
		ref == 0 || item == 0 || item > math.MaxInt32 || le.Uint32(body[11:]) != 0 {
		return storage.ErrInvalid
	}
	failure := append([]byte{0, 0, 0}, body[2:11]...)
	reject := func() error { return g.send(1, 44, failure) }
	if list == 7 {
		return g.feedPet(ctx, slot, item, body)
	}
	if item == 490708126 {
		return g.disguiseUse(ctx, body)
	}
	if g.channel != 19 || g.ordinary.Returning() || g.opening != nil && !g.opening.Ready() {
		return reject()
	}
	var dungeon uint32
	scene := "town"
	run := g.ordinary.Run
	if run != nil {
		if !run.Ready || run.PlayerDeath != nil || run.Cleared || run.Room == nil {
			return reject()
		}
		dungeon = run.Definition.Dungeon.ID
		scene = fmt.Sprintf("%s:%d:%d:%t", run.ID, run.Room.X, run.Room.Y, run.InLayer)
	} else if g.opening != nil {
		scene = "opening"
	}
	if g.hasQuestExperience(item) {
		return g.questExperienceItem(ctx, body)
	}
	registry := g.catalog().Commerce.Potions
	rule, ok := registry[item]
	if !ok {
		return reject()
	}
	if rule.NativeApcSummon != nil && (!g.server.delegated || run == nil || run.InLayer || !run.GameMasterReady()) {
		return reject()
	}
	replay := g.commerceState
	if replay.Scene != scene {
		replay = storage.CommerceReplay{}
	}
	now := time.Now()
	out, err := g.server.DB.Commerce(ctx, g.lease, 44, g.seq, body, replay, func(x *storage.CommerceTx) error {
		if err := x.UsePotion(slot, item, rule, dungeon, now); err != nil {
			return err
		}
		if rule.NativeApcSummon != nil {
			if x.Character.ID != run.Character {
				return storage.ErrPhase
			}
			return x.AuthorizePotionSummon(slot, g.seq, body, run.ID, rule, now)
		}
		return nil
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	bag, err := mailBag(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	ack := append([]byte{1}, body[:11]...)
	if out.Replayed {
		ack = failure
	} else if out.Replay.Summon != nil && !run.RegisterPotionSummon(out.Replay.Summon) {
		return storage.ErrConflict
	}
	g.commerceState = out.Replay
	g.commerceState.Scene = scene
	if err = g.send(1, 44, ack); err != nil {
		return err
	}
	return g.send(0, 13, bag)
}

// useConsumable parses the shared C507 envelope once. Each supported native
// action owns its own persistent effect and response sequence.
func (g *game) useConsumable(ctx context.Context, body []byte) error {
	le := binary.LittleEndian
	if len(body) != 19 {
		return storage.ErrInvalid
	}
	slot, list := le.Uint16(body), body[2]
	action := le.Uint32(body[7:])
	fields := append(append([]byte{}, body[:3]...), body[7:11]...)
	failure := append([]byte{0, 0, 0}, fields...)
	reject := func() error { return g.send(1, 507, failure) }
	switch action {
	case 65:
		return g.convertAvatar(ctx, body)
	case 77, 99, 101, 119, 122, 130, 137, 171, 181, 183, 241:
		return g.questTicket(ctx, body)
	case 230:
		return g.petSkinSlot(ctx, body)
	case 239:
		return g.questCommunicator(ctx, body)
	case 193:
		return g.questCapsule(ctx, body)
	case 58:
		if list != 0 {
			return storage.ErrInvalid
		}
		if !g.commerceInTown() || le.Uint32(body[3:]) != 0 || le.Uint32(body[11:]) != 0 || le.Uint32(body[15:]) != 0 {
			return reject()
		}
	default:
		return reject()
	}
	catalog := g.catalog()
	out, err := g.server.DB.Commerce(ctx, g.lease, 507, g.seq, body, g.commerceState, func(x *storage.CommerceTx) error {
		place, err := catalog.ConsumableTown(x.Character)
		if err != nil {
			return err
		}
		return x.UseFatigue(slot, catalog.Commerce.FatigueItems, place, time.Now())
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	bag, err := mailBag(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	a, _, err := readActor(out.Character, g.channel)
	if err != nil {
		return err
	}
	ack := append([]byte{1}, fields...)
	if out.Replayed {
		ack = failure
	}
	g.commerceState = out.Replay
	if err = g.send(1, 507, ack); err != nil {
		return err
	}
	if err = g.send(0, 36, protocol.Fatigue(a)); err != nil {
		return err
	}
	return g.send(0, 13, bag)
}
