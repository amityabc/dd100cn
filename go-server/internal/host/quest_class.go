package host

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

func (g *game) questClassChange(ctx context.Context, f protocol.Frame) error {
	req, err := questwire.ReadClass(f.Opcode, f.Body)
	if err != nil {
		return err
	}
	var meta struct{ ClassReplacementCount byte }
	var count byte
	reply := func(code uint16) error {
		var b []byte
		if f.Opcode == 777 {
			if count > 127 {
				return storage.ErrInvalid
			}
			b = append(binary.LittleEndian.AppendUint32([]byte{1}, uint32(code)), count)
		} else if code != 0 {
			b = binary.LittleEndian.AppendUint16([]byte{0}, code)
		} else {
			b = []byte{1}
		}
		return g.send(1, f.Opcode, b)
	}
	rejectPhase := func() error {
		if f.Opcode == 777 {
			entry, err := g.server.DB.Current(ctx, g.lease)
			if err != nil {
				return err
			}
			if len(entry.Characters) != 0 {
				if err := json.Unmarshal(entry.Characters[0].Gameplay, &meta); err != nil {
					return err
				}
				count = meta.ClassReplacementCount
			}
		}
		return reply(7)
	}
	if f.Opcode == 494 && g.channel != 19 || g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() ||
		duellobby.IsChannel(g.channel) {
		return rejectPhase()
	}
	cat := g.catalog()
	var refusal error
	now := time.Now()
	opts := questwire.ClassChangeContext{Now: now, Tickets: cat.OpeningCombat.ClassTickets, SupportsBranch: cat.OpeningCombat.SupportsClassBranch,
		FreeSkills: cat.OpeningCombat.QuestFreeSkills, Cost: cat.OpeningCombat.ClassReplacementCost, Stats: cat.OpeningCombat.ClassStats}
	out, err := g.server.DB.ChangeQuest(ctx, g.lease, storage.TownReady, cat.Weights, func(c storage.Character, in storage.Inventory, _ map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		if f.Opcode == 494 {
			q, err := resources.ReadTownQuests(c.Gameplay)
			if err != nil {
				return storage.TownQuestMutation{}, err
			}
			if !cat.AllowsOrdinaryTownPosition(c.Level, q, point(c.Position)) ||
				g.fateSession.Character == c.ID && (len(g.fateSession.Presets) != 0 || g.fateSession.Run != "") {
				refusal = storage.ErrInvalid
				return storage.TownQuestMutation{}, nil
			}
		}
		plan, err := cat.Quests.ClassChange(c, in, req, opts)
		if err != nil {
			refusal = err
			return storage.TownQuestMutation{}, nil
		}
		return plan, nil
	})
	if errors.Is(err, storage.ErrPhase) {
		return rejectPhase()
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(out.Character.Gameplay, &meta); err != nil {
		return err
	}
	count = meta.ClassReplacementCount
	if refusal != nil {
		code := uint16(7)
		if f.Opcode == 777 {
			code = questwire.SkillError(refusal)
		}
		return reply(code)
	}
	c, err := questActorInventory(out)
	if err != nil {
		return err
	}
	actor, _, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	body, err := g.actorMode0(actor)
	if err != nil {
		return err
	}
	if err = g.send(0, 2, body); err != nil {
		return err
	}
	if f.Opcode != 1880 {
		body, err = protocol.Mode1(actor, true)
		if err != nil {
			return err
		}
		if err = g.send(0, 2, body); err != nil {
			return err
		}
	}
	if err = g.sendSkillBuild(c, false, nil); err != nil {
		return err
	}
	if err = g.sendQuestBook(out); err != nil {
		return err
	}
	if f.Opcode != 1880 {
		usage, err := inventorywire.BagAndWear(out.Inventory, c)
		if err != nil {
			return err
		}
		if err = g.send(0, usage[0].Opcode, usage[0].Body); err != nil {
			return err
		}
	}
	return reply(0)
}
