package host

import (
	"context"
	"errors"
	"fmt"
	"time"

	"s9a1/server/internal/commercewire"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) discardInventory(ctx context.Context, body []byte) error {
	reject := func() error { return g.send(1, 18, commercewire.DiscardRejected()) }
	q, err := commercewire.ReadDiscard(body)
	if err != nil || q.List != 0 || q.Context != 0 || g.channel != 19 {
		return reject()
	}
	skill := len(q.Rows) == 1 && q.State == 1 && q.Rows[0].Action == 2
	run := g.ordinary.Run
	var scene string
	if skill {
		if run == nil || !run.Ready || run.InLayer || run.Room == nil || run.PlayerDeath != nil || run.Cleared {
			return reject()
		}
		scene = fmt.Sprintf("skill:%s:%d:%d:%d", run.ID, run.Room.X, run.Room.Y, run.DeathCycle)
	} else if q.State != 0 || !g.commerceInTown() {
		return reject()
	}
	now := time.Now()
	catalog := g.catalog().Commerce
	replay := g.commerceState
	if replay.Scene != scene {
		replay = storage.CommerceReplay{}
	}
	out, err := g.server.DB.Commerce(ctx, g.lease, 18, g.seq, body, replay, func(x *storage.CommerceTx) error {
		if !skill {
			return x.Discard(q.Rows, catalog, now)
		}
		p, err := readGameplay(x.Character.Gameplay)
		if err != nil {
			return err
		}
		if !p.Initialized || p.Progression.Hp == 0 || run.Character != x.Character.ID {
			return storage.ErrPhase
		}
		return x.SpendSkillCube(q.Rows[0], catalog.Templates[3037], now)
	})
	if expectedCommerceError(err) {
		if err = reject(); err != nil || skill {
			return err
		}
		return g.commerceAnnouncement("丢弃失败：该物品不允许丢弃，或物品状态已发生变化。")
	}
	if err != nil {
		return err
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, true)
	if err != nil {
		return err
	}
	g.commerceState = out.Replay
	g.commerceState.Scene = scene
	if err = g.send(1, 18, commercewire.DiscardAccepted(q.Rows)); err != nil {
		return err
	}
	return g.projected(bags)
}

func (g *game) disassembleInventory(ctx context.Context, body []byte) error {
	reject := func(code uint16) error { return g.send(1, 26, mailwire.Rejected(code)) }
	q, err := commercewire.ReadDisassembly(body)
	if err != nil {
		return reject(4)
	}
	if !g.commerceInTown() {
		return reject(0)
	}
	now := time.Now()
	c := g.catalog()
	out, err := g.server.DB.Commerce(ctx, g.lease, 26, g.seq, body, g.commerceState, func(x *storage.CommerceTx) error {
		if !c.TownDisassembler(x.Character, q.NPC) {
			return storage.ErrPhase
		}
		if err := x.Disassemble(q.Slots, c.Commerce, now); err != nil {
			return err
		}
		return x.CheckCarry(c.GoldCarryLimits, c.Weights)
	})
	if expectedCommerceError(err) {
		msg := "分解失败：当前装备状态不满足操作条件，请重新确认物品及所在位置。"
		code := uint16(0)
		if errors.Is(err, storage.ErrCapacity) {
			code = 4
			msg = "分解失败：分解材料所在物品栏已满，请整理对应栏位。"
		}
		if err = reject(code); err != nil {
			return err
		}
		return g.commerceAnnouncement(msg)
	}
	if err != nil {
		return err
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, true)
	if err != nil {
		return err
	}
	g.commerceState = out.Replay
	if err = g.send(1, 26, commercewire.Disassembled(q, out.Grants)); err != nil {
		return err
	}
	return g.projected(bags)
}

func (g *game) commerceAnnouncement(msg string) error {
	body, err := protocol.Announcement(msg)
	if err != nil {
		return err
	}
	return g.send(0, 488, body)
}
