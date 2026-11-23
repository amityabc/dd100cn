package host

import (
	"encoding/binary"
	"time"

	"s9a1/server/internal/fate"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) projectFate(f protocol.Frame, out storage.TownQuestResult, next fateAction, now time.Time) error {
	def := g.catalog().Fate
	actor, p, err := readActor(out.Character, g.channel)
	if err != nil {
		return err
	}
	state := func() error {
		b, err := fateState(p, def.StoryCapacity, def.SinCosts, now)
		if err != nil {
			return err
		}
		return g.send(0, 2055, b)
	}
	collection := func() error {
		packets, err := inventorywire.FateContainers(out.Inventory, out.Character)
		if err != nil {
			return err
		}
		return g.send(0, 13, packets[0].Body)
	}
	le := binary.LittleEndian
	ack := func() error { return g.send(1, f.Opcode, []byte{1}) }
	switch f.Opcode {
	case 1918:
		if err = collection(); err != nil {
			return err
		}
		if err = state(); err != nil {
			return err
		}
		if err = g.send(0, 5, append(le.AppendUint32([]byte{1, 0}, def.DungeonId), 1)); err != nil {
			return err
		}
		b := []byte{1, 0, 0, 0, 0}
		for _, v := range next.Session.Presets {
			if v < 0 || v > 255 {
				return fate.ErrInvalid
			}
			b = append(b, byte(v))
		}
		if err = g.send(1, 1918, b); err != nil {
			return err
		}
	case 15:
		return ack()
	case 16:
		if next.Replayed {
			return ack()
		}
		return g.packets(next.Entry)
	case 37:
		if next.Replayed {
			return ack()
		}
		return g.packets(fate.LoadPackets(actor, p.Progression.Sp, p.Progression.Tp))
	case 1919:
		if drop := next.Drop; drop != nil && !drop.Claimed {
			b, err := fate.GroundPacket(actor.ID, *drop)
			if err != nil {
				return err
			}
			if err = g.send(0, 40, b); err != nil {
				return err
			}
		}
		if err = ack(); err != nil {
			return err
		}
		return state()
	case 1894:
		p, err := fate.Read(out.Character.Gameplay)
		if err != nil {
			return err
		}
		r := p.Run
		if r == nil {
			return fate.ErrInvalid
		}
		var grants []struct{ Template, Count uint32 }
		for _, v := range r.DeliveredRewards {
			id, err := fate.TemplateID(v.Template)
			if err != nil {
				return err
			}
			grants = append(grants, struct{ Template, Count uint32 }{id, v.Count})
		}
		if err = g.send(1, 1894, fate.ResultPacket(uint32(len(r.CompletedPhases)), uint32(r.EarnedPoints), grants)); err != nil {
			return err
		}
		if err = state(); err != nil {
			return err
		}
	case 681:
		q, err := fate.ReadControl(f.Body)
		if err != nil {
			return err
		}
		return g.send(1, 681, fate.ControlAck(true, q.Action == 8))
	case 18:
		return g.send(1, 18, []byte{1, 4, 0, 0, 0, 8, 0, 24, 0})
	case 43:
		if err = ack(); err != nil {
			return err
		}
		if !next.Replayed {
			if err = g.send(0, 39, fate.PickedPacket(next.Drop.ObjectKey, actor.ID, next.Slot)); err != nil {
				return err
			}
		}
		if err = collection(); err != nil {
			return err
		}
		return state()
	case 40:
		return g.send(0, 32, append(le.AppendUint16(nil, actor.ID), 0, 0))
	case 42, 72, 132:
		if f.Opcode == 42 {
			if err = ack(); err != nil {
				return err
			}
		}
		if f.Opcode == 72 {
			if err = g.send(1, 72, []byte{1, f.Body[0], 2}); err != nil {
				return err
			}
			if f.Body[0] == 2 || next.Replayed {
				return nil
			}
		}
		if next.RemoveBuff && def.InstanceBuff > 0 {
			if err = g.send(0, 474, le.AppendUint32([]byte{1}, uint32(def.InstanceBuff))); err != nil {
				return err
			}
		}
		if err = g.send(0, 23, protocol.Area(actor)); err != nil {
			return err
		}
		if err = g.send(0, 24, protocol.Town(actor)); err != nil {
			return err
		}
	}
	if next.Mail {
		packets, err := mailwire.Town(out.Character)
		if err != nil {
			return err
		}
		for _, p := range packets {
			if err = g.send(0, p.Opcode, p.Body); err != nil {
				return err
			}
		}
	}
	return nil
}
