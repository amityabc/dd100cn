package host

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"time"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/storage"
)

func (g *game) convertAvatar(ctx context.Context, body []byte) error {
	le := binary.LittleEndian
	slot, target := le.Uint16(body), le.Uint32(body[15:])
	fields := append(append([]byte{}, body[:3]...), body[7:11]...)
	failure := append([]byte{0, 0, 0}, fields...)
	reject := func(reason error) error {
		if err := g.send(1, 507, failure); err != nil {
			return err
		}
		message := "装扮无法变更，请选择该券支持的永久、未锁定装扮，并放入装扮栏。"
		switch {
		case errors.Is(reason, storage.ErrAvatarConversionInput):
			message = "所选装扮不在该变更券的转换清单中，请核对券说明和目标装扮。"
		case errors.Is(reason, storage.ErrPhase):
			message = "请在城镇中使用装扮变更券。"
		case errors.Is(reason, storage.ErrAvatarConversionSource):
			message = "变更券状态或使用条件已变化，请重新确认道具。"
		}
		return g.commerceAnnouncement(message)
	}
	if !g.commerceInTown() {
		return reject(storage.ErrPhase)
	}
	if body[2] != 0 || le.Uint32(body[3:]) != 0 || le.Uint32(body[11:]) != 1 || target > math.MaxUint16 {
		return reject(storage.ErrAvatarConversionSource)
	}
	catalog := g.catalog().Quests.AvatarEdits().Conversions
	now := time.Now()
	result, err := g.server.DB.Commerce(ctx, g.lease, 507, g.seq, body, g.commerceState, func(tx *storage.CommerceTx) error {
		return tx.ConvertAvatar(slot, uint16(target), catalog, now)
	})
	if expectedCommerceError(err) || errors.Is(err, storage.ErrAvatarConversionSource) || errors.Is(err, storage.ErrAvatarConversionInput) {
		return reject(err)
	}
	if err != nil {
		return err
	}
	bag, err := inventorywire.OrdinaryBag(result.Inventory, result.Character)
	if err != nil {
		return err
	}
	special, err := inventorywire.CommerceSpecial(result.Inventory, result.Character)
	if err != nil {
		return err
	}
	ack := append([]byte{1}, fields...)
	if result.Replayed {
		ack = failure
	}
	g.commerceState = result.Replay
	if err := g.send(1, 507, ack); err != nil {
		return err
	}
	if err := g.send(0, 13, bag); err != nil {
		return err
	}
	return g.projected(special[:1])
}
