package host

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"s9a1/server/internal/commercewire"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/storage"
)

func (g *game) openCommerceBox(ctx context.Context, op uint16, body []byte) error {
	c := g.server.Characters
	now := time.Now()
	var apply func(*storage.CommerceTx) error
	var success func(storage.CommerceResult) ([]byte, error)
	switch op {
	case 27:
		slot, double, err := commercewire.ReadLottery(body)
		if err != nil {
			return err
		}
		apply = func(x *storage.CommerceTx) error {
			if double {
				return storage.ErrInvalid
			}
			_, place := c.ConsumableTown(x.Character)
			return x.OpenLottery(slot, place == nil, c.Commerce, now, c.GoldCarryLimits, c.Weights)
		}
		success = func(out storage.CommerceResult) ([]byte, error) {
			return commercewire.LotteryOpened(slot, out.Granted)
		}
	case 1122:
		q, err := commercewire.ReadKeyedBox(body)
		if err != nil {
			return err
		}
		apply = func(x *storage.CommerceTx) error { return x.OpenKeyedBox(q, c.Commerce, now) }
		success = func(out storage.CommerceResult) ([]byte, error) { return commercewire.KeyedBoxOpened(q, out.Grants) }
	default:
		return storage.ErrInvalid
	}
	reply := mailwire.Rejected(0)
	if !g.commerceInTown() {
		return g.send(1, op, reply)
	}
	out, err := g.server.DB.Commerce(ctx, g.lease, op, g.seq, body, g.commerceState, apply)
	if expectedCommerceError(err) {
		snapshot, err := g.server.DB.Snapshot(ctx, g.lease)
		if err != nil {
			return err
		}
		out.Character, err = selected(snapshot)
		if err != nil {
			return err
		}
		out.Inventory, err = g.server.DB.Inventory(ctx, g.lease)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !out.Replayed {
		reply, err = success(out)
		if err != nil {
			return err
		}
		g.commerceState = out.Replay
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, false)
	if err != nil {
		return err
	}
	if err = g.send(1, op, reply); err != nil {
		return err
	}
	return g.projected(bags)
}

func (g *game) luckyBox(ctx context.Context, op uint16, body []byte) error {
	if op == 1830 {
		if len(body) != 13 {
			return storage.ErrInvalid
		}
		if err := g.server.DB.ValidateLease(ctx, g.lease); err != nil {
			return err
		}
		return g.send(1, 1830, []byte{1})
	}
	q, err := commercewire.ReadLuckyBox(body)
	if err != nil {
		return err
	}
	fail := func(msg string) error {
		return g.commerceAnnouncement("圣者遗物箱开启失败：" + msg)
	}
	reply := mailwire.Rejected(0)
	if !g.commerceInTown() {
		if err = g.send(1, 1829, reply); err != nil {
			return err
		}
		return fail("请在赛丽亚房间开启。")
	}
	c := g.server.Characters
	now := time.Now()
	out, err := g.server.DB.Commerce(ctx, g.lease, 1829, g.seq, body[13:], g.commerceState, func(x *storage.CommerceTx) error {
		place, err := c.ConsumableTown(x.Character)
		if err != nil || place != "seria room" {
			return storage.ErrPhase
		}
		if err = x.OpenLuckyBox(q, c.Commerce, now); err != nil {
			return err
		}
		return x.CheckCarry(c.GoldCarryLimits, c.Weights)
	})
	var message string
	if expectedCommerceError(err) {
		switch {
		case errors.Is(err, storage.ErrCapacity):
			message = "物品栏对应分类空间不足或超过负重上限，请整理背包后再开启。"
		case errors.Is(err, storage.ErrPhase):
			message = "请在赛丽亚房间开启。"
		default:
			message = "箱子的数量、期限或使用资格不符合要求。"
		}
		snapshot, err := g.server.DB.Current(ctx, g.lease)
		if err != nil {
			return err
		}
		out.Character, err = selected(snapshot)
		if err != nil {
			return err
		}
		out.Inventory, err = g.server.DB.Inventory(ctx, g.lease)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if out.Replayed {
		message = "服务端未提供具体原因。"
	} else {
		reply, err = commercewire.LuckyBoxOpened(q, out.Inventory.TreasureGauge, out.Replay.LuckyRewards)
		if err != nil {
			return err
		}
		g.commerceState = out.Replay
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, true)
	if err != nil {
		return err
	}
	common, err := inventorywire.CommerceCommon(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	bags = append(bags[:1], append([]inventorywire.Packet{common}, bags[1:]...)...)
	if err = g.projected(bags); err != nil {
		return err
	}
	if err = g.send(1, 1829, reply); err != nil {
		return err
	}
	if err = g.send(0, 1987, binary.LittleEndian.AppendUint32(nil, out.Inventory.TreasureGauge)); err != nil {
		return err
	}
	if message != "" {
		return fail(message)
	}
	return nil
}

func (g *game) sendTreasureGauge(ctx context.Context) error {
	if !g.commerceInTown() {
		return nil
	}
	r, err := g.server.DB.Current(ctx, g.lease)
	if err != nil || r.Phase != storage.TownReady || r.SelectedID == 0 {
		return err
	}
	return g.send(0, 1987, binary.LittleEndian.AppendUint32(nil, r.TreasureGauge))
}
