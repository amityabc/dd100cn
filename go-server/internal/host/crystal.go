package host

import (
	"context"
	"errors"
	"time"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

type crystalProjection struct {
	loaded, inTown, active bool
	selection              byte
}

func (g *game) restoreCrystal(ctx context.Context) error {
	p := &g.crystal
	active := g.contracts[2] > time.Now().Unix()
	if !p.loaded || g.opening != nil || g.ordinary.Active() {
		p.inTown, p.active = false, active
		return nil
	}
	if !p.inTown || !p.active && active {
		if duellobby.IsChannel(g.channel) {
			in, err := g.server.DB.Inventory(ctx, g.lease)
			if err != nil {
				return err
			}
			bag, err := inventorywire.OrdinaryBag(in, storage.Character{ID: in.CharacterID})
			if err != nil {
				return err
			}
			if err = g.send(0, 13, bag); err != nil {
				return err
			}
		}
		if err := g.send(0, 889, []byte{0, p.selection}); err != nil {
			return err
		}
	}
	p.inTown, p.active = true, active
	return nil
}

func (g *game) selectCrystal(ctx context.Context, b []byte) error {
	if len(b) != 2 || b[0] > 1 || b[1] > 5 && b[1] != 255 {
		return errors.New("invalid crystal contract selection")
	}
	duelReady := duellobby.IsChannel(g.channel) && g.duelPosition != nil
	err := g.server.DB.SelectCrystal(ctx, g.lease, b[1], duelReady, time.Now())
	if err == nil {
		g.crystal.selection = b[1]
		return g.send(1, 527, []byte{1, b[0], b[1]})
	}
	if errors.Is(err, storage.ErrLease) || errors.Is(err, storage.ErrCommitUnknown) || ctx.Err() != nil {
		return err
	}
	message := "晶之契约选择保存失败，请稍后重试。"
	switch {
	case errors.Is(err, storage.ErrPhase):
		message = "角色尚未就绪，请稍后重试晶之契约选择。"
	case errors.Is(err, storage.ErrInvalid):
		message = "晶之契约选择失败，请确认角色状态后重试。"
	case errors.Is(err, storage.ErrCrystalMissing):
		message = "所选晶块数量不足，无法更改选择。"
	case errors.Is(err, storage.ErrCrystalInactive):
		message = "晶之契约未生效，无法更改晶块选择。"
	}
	n, encodeErr := protocol.Announcement(message)
	if encodeErr != nil {
		return encodeErr
	}
	if err := g.send(1, 527, []byte{0, 0, 0}); err != nil {
		return err
	}
	return g.send(0, 488, n)
}
