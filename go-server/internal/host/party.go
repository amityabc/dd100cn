package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) party(ctx context.Context, op uint16, body []byte) error {
	var create protocol.PartyCreate
	var err error
	if op == 12 {
		create, err = protocol.ReadPartyCreate(body)
		if err != nil {
			return err
		}
	} else if len(body) != 4 {
		return errors.New("party leave requires four bytes")
	}
	r, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	reject := func(message string) error {
		n, err := protocol.Announcement(message)
		if err != nil {
			return err
		}
		if err = g.send(1, op, []byte{0, 0, 0}); err != nil {
			return err
		}
		return g.send(0, 488, n)
	}
	ready := g.channel == 19 && r.SelectedID != 0 && r.Phase == storage.TownReady && g.opening == nil
	if op == 13 {
		if !ready || binary.LittleEndian.Uint32(body) != 0 {
			return reject("当前无法退出队伍，请先结束挑战、返回城镇并等待加载完成。")
		}
		if err = g.send(1, 13, protocol.Ack()); err != nil {
			return err
		}
		if g.partySettings == nil {
			return nil
		}
		g.partySettings = nil
		return g.send(0, 9, protocol.PartyRemove(r.SelectedID, 0, byte(g.channel), true))
	}
	if create.RaidType == 6 {
		if r.SelectedID == 0 {
			return reject("请先选择角色再创建洞察之眼队伍。")
		}
		return reject("角色不满足洞察之眼的频道或入场条件。")
	}
	if !ready || !g.server.nativeParty || !create.Ordinary() || g.partySettings != nil && !bytes.Equal(g.partySettings, body) {
		return reject("当前无法创建普通队伍，请在普通城镇中结束挑战并退出已有队伍后重试。")
	}
	g.partySettings = bytes.Clone(body)
	if err = g.send(1, 12, protocol.Ack()); err != nil {
		return err
	}
	return g.send(0, 9, create.Self(r.SelectedID, 0, byte(g.channel)))
}
