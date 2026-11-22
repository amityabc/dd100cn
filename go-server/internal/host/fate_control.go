package host

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"s9a1/server/internal/fate"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

type fateMutation struct {
	Character, Sequence uint16
	Revision            int64
	Body                string
}

func (g *game) fateControl(ctx context.Context, f protocol.Frame) error {
	var q fate.Control
	var err error
	switch f.Opcode {
	case 18:
		err = fate.ReadConsumption(f.Body)
	case 681:
		q, err = fate.ReadControl(f.Body)
	case 1895:
		q.ID, q.Slot, err = fate.ReadEquip(f.Body)
	case 1894:
		err = fate.ReadResult(f.Body)
	case 1896:
		err = fate.ReadAbility(f.Body)
	case 1929, 1930:
		// LocalFateRules.AuxiliaryReportOpcodes has no response outside the activity.
		// The active run intercept checks the lease and opaque body size separately.
		return nil
	}
	if err != nil {
		return err
	}
	reject := func() error {
		switch f.Opcode {
		case 18:
			return g.send(1, 18, []byte{0, 0, 0, 4, 0, 0, 0, 8, 0, 24, 0})
		case 681:
			return g.send(1, 681, fate.ControlAck(false, q.Action == 8))
		case 1894:
			return g.send(1, 1894, []byte{0, 0, 0})
		case 1895:
			return g.send(1, 1895, []byte{0, 21, 0})
		}
		return nil
	}
	def := g.server.Characters.Fate
	if def == nil || g.ordinary.Returning() {
		return reject()
	}
	if f.Opcode == 18 || f.Opcode == 1896 || f.Opcode == 681 && (q.Action >= 7 && q.Action <= 9) {
		return reject()
	}
	var next fate.Profile
	rank := f.Opcode == 681
	out, err := g.server.DB.ChangeFateActivity(ctx, g.lease, g.server.Characters.Weights, false, func(c storage.Character, in storage.Inventory, _ map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		var m storage.TownQuestMutation
		before, err := fate.Read(c.Gameplay)
		if err != nil {
			return m, err
		}
		if err = def.Validate(before); err != nil {
			return m, err
		}
		if f.Opcode == 1894 {
			return m, nil
		}
		if c.Level < def.MinimumLevel || c.Position.Town != def.Town.Camp.Town || c.Position.Area != def.Town.Camp.Area {
			return m, fate.ErrInvalid
		}
		if rank && g.fateMutation == (fateMutation{c.ID, f.Sequence, c.Revision, string(f.Body)}) {
			next = before
			return m, nil
		}
		fields := map[string]any{}
		if rank {
			next, err = def.Rank(before, q.Action, q.ID)
			if err != nil {
				return m, err
			}
			if slices.Equal(next.Blessings, before.Blessings) && slices.Equal(next.Sins, before.Sins) {
				return m, nil
			}
			fields["Blessings"], fields["Sins"] = next.Blessings, next.Sins
		} else {
			next, err = def.Equip(before, q.ID, q.Slot, func(item fate.CollectionItem) bool {
				for _, a := range in.Items {
					if a.Location == 44 && a.Slot == item.Slot && fate.EqualTemplate(a.Template, item.Template) {
						return true
					}
				}
				return false
			})
			if err != nil {
				return m, err
			}
			if slices.Equal(next.EquippedEffects, before.EquippedEffects) {
				return m, nil
			}
			fields["EquippedEffects"] = next.EquippedEffects
		}
		c.Gameplay, err = fate.PatchFate(c.Gameplay, fields)
		if err != nil {
			return m, err
		}
		m.Reward = &storage.QuestRewardMutation{Character: c, Inventory: in}
		return m, nil
	})
	if errors.Is(err, storage.ErrPhase) || errors.Is(err, fate.ErrInvalid) || errors.Is(err, storage.ErrInvalid) {
		return reject()
	}
	if err != nil {
		return err
	}
	if f.Opcode == 1894 {
		return g.send(1, 1894, fate.ResultPacket(0, 0, nil))
	}
	if rank {
		g.fateMutation = fateMutation{out.Character.ID, f.Sequence, out.Character.Revision, string(f.Body)}
	}
	var p actorProfile
	if err = json.Unmarshal(out.Character.Gameplay, &p); err != nil {
		return err
	}
	b, err := fateState(p, def.StoryCapacity, def.SinCosts, time.Now())
	if err != nil {
		return err
	}
	if err = g.send(0, 2055, b); err != nil {
		return err
	}
	if rank {
		return g.send(1, 681, fate.ControlAck(true, false))
	}
	return g.send(1, 1895, []byte{1})
}
