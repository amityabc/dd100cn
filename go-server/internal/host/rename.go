package host

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"s9a1/server/internal/commercewire"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

func (g *game) renameTown(c storage.Character) (bool, error) {
	if g.channel != 19 || g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() {
		return false, nil
	}
	profile, err := readGameplay(c.Gameplay)
	if err != nil {
		return false, err
	}
	if !profile.Initialized || profile.Progression.Hp == 0 {
		return false, nil
	}
	q, err := resources.ReadTownQuests(c.Gameplay)
	if err != nil {
		return false, err
	}
	return g.server.Characters.AllowsTownPosition(c.Level, q, point(c.Position)), nil
}

func (g *game) renameCharacter(ctx context.Context, f protocol.Frame) error {
	cmd := storage.RenameCommand{Sequence: f.Sequence, Opcode: f.Opcode}
	var stamp storage.RenameReplay
	var err error
	code := byte(2)
	switch f.Opcode {
	case 135:
		if cmd.Request, err = protocol.ReadRename(f.Body); err != nil {
			return err
		}
		stamp, err = g.server.DB.RenameCharacter(ctx, g.lease, cmd, g.renameReplay, time.Now())
	case 501:
		if len(f.Body) != 2 {
			return errors.New("invalid rename card slot")
		}
		code = 19
		cmd.Slot = binary.LittleEndian.Uint16(f.Body)
		var out storage.RenameGrant
		out, err = g.server.DB.GrantRename(ctx, g.lease, cmd, g.renameReplay, g.server.Characters.Rename[storage.RenameBag], g.renameTown, time.Now())
		stamp = out.Replay
	}
	if err == nil {
		g.renameReplay = stamp
		return g.send(1, f.Opcode, protocol.Ack())
	}
	if errors.Is(err, storage.ErrLease) || errors.Is(err, storage.ErrCommitUnknown) || ctx.Err() != nil {
		return err
	}
	if errors.Is(err, storage.ErrNameTaken) {
		code = 20
	}
	return g.send(1, f.Opcode, []byte{0, code, 0})
}

func (g *game) buyRename(ctx context.Context, row commercewire.CashSelection, body []byte) error {
	reject := func() error { return g.send(1, 64, commercewire.CashRejected(row.Commodity, 20)) }
	if row.Commodity != storage.RenameCommodity || !row.Ordinary() || row.CouponTemplate != 0 || row.CouponSlot != 0 {
		return reject()
	}
	cmd := storage.RenameCommand{Sequence: g.seq, Opcode: 64, PurchaseModel: row.PurchaseModel}
	var last storage.RenameReplay
	replay := g.commerceState
	if replay.Opcode == 64 && replay.Sequence == g.seq && replay.Body == string(body) {
		last = storage.RenameReplay{Owner: g.lease, Command: cmd, Character: replay.CharacterID, Revision: replay.Revision, AccountRevision: replay.AssetRevision}
	}
	out, err := g.server.DB.GrantRename(ctx, g.lease, cmd, last, g.server.Characters.Rename[storage.RenameCash], g.renameTown, time.Now())
	if err != nil {
		if errors.Is(err, storage.ErrLease) || errors.Is(err, storage.ErrCommitUnknown) || ctx.Err() != nil {
			return err
		}
		return reject()
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, true)
	if err != nil {
		return err
	}
	g.commerceState = storage.CommerceReplay{Opcode: 64, Sequence: g.seq, CharacterID: out.Character.ID, Revision: out.Character.Revision,
		AssetRevision: out.Inventory.AssetRevision, Body: string(body)}
	if err = g.send(1, 64, commercewire.CashBought(row.Commodity, 0, 0, -1)); err != nil {
		return err
	}
	if err = g.wallet(out.Inventory.Cera); err != nil {
		return err
	}
	return g.projected(bags)
}
