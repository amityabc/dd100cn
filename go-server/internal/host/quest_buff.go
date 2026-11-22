package host

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
)

type buffProjection struct {
	character uint16
	revision  int64
	hasItems  bool
}

func (g *game) buffRegistration(ctx context.Context, body []byte) error {
	r, err := questwire.ReadBuff(body)
	if err != nil {
		return err
	}
	cat := g.server.Characters
	now := time.Now()
	var out storage.TownQuestResult
	var issue error
	if g.channel != 19 || g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() {
		issue = storage.ErrPhase
	} else {
		out, err = g.server.DB.ChangeTownQuest(ctx, g.lease, cat.Weights, func(c storage.Character, in storage.Inventory, _ map[string]json.RawMessage) (storage.TownQuestMutation, error) {
			m, problem := cat.Quests.RegisterBuff(c, in, r, now)
			if problem != nil {
				issue = problem
				return storage.TownQuestMutation{}, nil
			}
			return m, nil
		})
		if errors.Is(err, storage.ErrPhase) {
			issue = err
		} else if err != nil {
			return err
		}
	}
	ack := []byte{1}
	if issue != nil {
		ack = []byte{0, 0, 0}
	}
	if err = g.send(1, 1421, ack); err != nil {
		return err
	}
	if out.Character.ID != 0 {
		if err = g.restoreBuffRegistration(out.Character, out.Inventory, now); err != nil {
			return err
		}
	}
	if issue != nil {
		return g.battlePassNotice("BUFF强化登记未保存，请确认装备仍在当前角色背包或穿戴栏内后重试。")
	}
	return nil
}

func (g *game) restoreBuffRegistration(c storage.Character, in storage.Inventory, now time.Time) error {
	skill, body, err := questwire.BuffProjection(c, in, now)
	if err != nil {
		return err
	}
	if err = g.send(0, 1361, append([]byte{0, 0}, body...)); err != nil {
		return err
	}
	if skill != 0 {
		if err = g.send(0, 1361, append(binary.LittleEndian.AppendUint16(nil, skill), body...)); err != nil {
			return err
		}
	}
	g.stampBuffProjection(c, in)
	return nil
}

func (g *game) stampBuffProjection(c storage.Character, in storage.Inventory) {
	var p actorProfile
	if json.Unmarshal(c.Gameplay, &p) == nil && p.Initialized {
		g.buffProjection = buffProjection{c.ID, in.Revision, p.BuffRegistration != nil && len(p.BuffRegistration.Items) != 0}
	}
}

func (g *game) refreshBuffProjection(ctx context.Context, op uint16, in *storage.Inventory) error {
	if in == nil || g.opening != nil || g.ordinary.Active() || g.ordinary.Selecting() || g.ordinary.Returning() || op == 1421 {
		return nil
	}
	last := g.buffProjection
	if last.character == in.CharacterID && (!last.hasItems || last.revision == in.Revision && op != 19 && op != 20) {
		return nil
	}
	r, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	ready := r.Phase == storage.TownReady || duellobby.IsChannel(g.channel) && r.Phase == storage.Selected && g.duelPosition != nil && (g.arcade == nil || g.arcade.Closed)
	if !ready || r.SelectedID != in.CharacterID {
		return nil
	}
	c, err := selected(r)
	if err != nil || c.Revision != in.Revision {
		return err
	}
	var p actorProfile
	if err = json.Unmarshal(c.Gameplay, &p); err != nil || !p.Initialized {
		return err
	}
	now := time.Now()
	if last.character != c.ID {
		return g.restoreBuffRegistration(c, *in, now)
	}
	if p.BuffRegistration != nil && len(p.BuffRegistration.Items) > 0 {
		_, body, err := questwire.BuffProjection(c, *in, now)
		if err != nil {
			return err
		}
		if err = g.send(0, 1360, body); err != nil {
			return err
		}
	}
	g.stampBuffProjection(c, *in)
	return nil
}
