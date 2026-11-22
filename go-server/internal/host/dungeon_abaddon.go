package host

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) abaddonRosterPackets(ctx context.Context) ([]opening.Packet, error) {
	rows, err := g.server.DB.AbaddonRoster(ctx, g.lease, time.Now())
	if err != nil {
		return nil, err
	}
	if len(rows) > 255 {
		return nil, storage.ErrInvalid
	}
	slices.SortFunc(rows, func(a, b storage.AbaddonProgress) int { return int(a.Slot) - int(b.Slot) })
	le := binary.LittleEndian
	first, second := []byte{byte(len(rows))}, le.AppendUint16(nil, uint16(len(rows)))
	bit := func(v bool) byte {
		if v {
			return 1
		}
		return 0
	}
	for _, r := range rows {
		name, err := protocol.EncodeText(r.Name, 30)
		if err != nil {
			return nil, err
		}
		if len(name) == 30 {
			for text := r.Name; text != ""; {
				_, size := utf8.DecodeLastRuneInString(text)
				text = text[:len(text)-size]
				b, e := protocol.EncodeText(text, 29)
				if e == nil {
					name = b
					break
				}
			}
		}
		first = le.AppendUint32(first, uint32(r.Slot))
		first = append(le.AppendUint32(first, uint32(len(name))), name...)
		first = append(first, r.Floors, bit(r.Registered))
		second = append(second, r.Slot)
		second = append(le.AppendUint32(second, uint32(len(name))), name...)
		second = append(second, bit(r.Charged), bit(r.Rewarded))
	}
	return []opening.Packet{{Opcode: 1722, Body: first}, {Opcode: 1772, Body: second}}, nil
}

func (g *game) abaddonControl(ctx context.Context, f protocol.Frame) error {
	run := g.ordinary.Run
	if run == nil || run.AbaddonEntry == nil || !g.server.delegated {
		return g.server.DB.ValidateLease(ctx, g.lease)
	}
	roster, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(roster)
	if err != nil {
		return err
	}
	if !run.Ready || run.Character != c.ID || run.AbaddonFailed() {
		return nil
	}
	if err := g.server.DB.ValidateDungeonProof(ctx, g.lease, c, []string{run.AbaddonEntry.Key()}); err != nil {
		return err
	}
	switch f.Opcode {
	case 1701:
		run.StartAbaddonClock(time.Now())
	case 1702:
		return g.failAbaddon(ctx, run, c, "NativeMapTimeout")
	case 1703:
		run.ObserveAbaddonHP(f.Body, time.Now())
	}
	return nil
}

func (g *game) failAbaddon(ctx context.Context, run *dungeon.Run, c storage.Character, source string) error {
	clock, ok := run.FreezeAbaddonTimeout()
	if !ok {
		return nil
	}
	proof, err := json.Marshal(struct {
		Version, Owner string
		Epoch          int64
		CharacterId    uint16
		RunId          string
		Id             uint32
		ResourceHash   string
		X, Y           byte
		MapId          uint32
		Clock          dungeon.AbaddonClock
		Source         string
	}{"ClientDelegatedAbaddonClock/v1", g.lease.Owner, g.lease.Epoch, c.ID, run.ID, run.Definition.Dungeon.ID,
		run.Definition.Dungeon.ResourceHash, run.Room.X, run.Room.Y, run.Room.MapID, clock, source})
	if err != nil {
		return err
	}
	key := fmt.Sprintf("abaddon-timeout:%s:%d:%d", strings.ReplaceAll(run.ID, "-", ""), run.Room.X, run.Room.Y)
	if err = g.server.DB.CommitDungeonCombat(ctx, g.lease, c, storage.DungeonChange{Key: key, Input: string(proof),
		Result: "failed;reward:none", Next: c, Proof: []string{run.AbaddonEntry.Key()}, Abaddon: &storage.AbaddonMutation{Entry: *run.AbaddonEntry}}); err != nil {
		return err
	}
	run.ApplyAbaddonTimeout(time.Now())
	return nil
}

func (g *game) dungeonAbaddonTick(ctx context.Context, run *dungeon.Run) error {
	if run.AbaddonFailed() {
		return g.dungeonRequest(ctx, protocol.Frame{Class: 1, Opcode: 42})
	}
	roster, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(roster)
	if err != nil {
		return err
	}
	return g.failAbaddon(ctx, run, c, "Watchdog")
}

func (g *game) reviveAbaddon(ctx context.Context, run *dungeon.Run, acknowledge bool) error {
	roster, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(roster)
	if err != nil {
		return err
	}
	coins, err := g.server.DB.ReviveAbaddon(ctx, g.lease, c, *run.AbaddonEntry, run.DeathCycle)
	if err != nil {
		return err
	}
	run.Revive()
	var frames []opening.Packet
	le := binary.LittleEndian
	if acknowledge {
		frames = append(frames, opening.Packet{Class: 1, Opcode: 41, Body: append(le.AppendUint16([]byte{1}, c.ID), 0)})
	}
	state := append(le.AppendUint16(nil, c.ID), 1, 0)
	packet := make([]byte, 122)
	packet[1], packet[3], packet[5] = 1, 1, 1
	le.PutUint32(packet[9:], coins)
	return g.packets(append(frames, opening.Packet{Opcode: 32, Body: state}, opening.Packet{Opcode: 14, Body: packet}))
}

func (g *game) publishAbaddonMail(c storage.Character, mail *storage.AbaddonMail) error {
	if mail == nil {
		return nil
	}
	box, err := storage.ReadMailbox(c)
	if err != nil {
		return err
	}
	for _, row := range box.Mails {
		if row.MailID != mail.MailId || row.DeliveryKey != mail.DeliveryKey {
			continue
		}
		if len(row.Attachments) != 1 || row.Attachments[0].Claimed {
			return storage.ErrConflict
		}
		a := row.Attachments[0].Asset
		var tpl struct{ TemplateId uint32 }
		if err := json.Unmarshal(a.Template, &tpl); err != nil {
			return err
		}
		if tpl.TemplateId == 0 || a.Count != mail.Reward.Reward.Count {
			return storage.ErrConflict
		}
		body := make([]byte, 84)
		binary.LittleEndian.PutUint32(body, tpl.TemplateId)
		binary.LittleEndian.PutUint32(body[4:], a.Count)
		if err := g.send(0, 1723, body); err != nil {
			return err
		}
		return g.publishMailAlarm(c.ID, box.Page().Unread, false)
	}
	return storage.ErrConflict
}
