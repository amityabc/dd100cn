package host

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
)

func (g *game) questSkillRequest(ctx context.Context, f protocol.Frame) error {
	req, err := questwire.ReadSkill(f.Opcode, f.Body)
	if err != nil {
		return err
	}
	duel := duellobby.IsChannel(g.channel)
	cat := g.catalog()
	var action questwire.SkillResult
	var refusal error
	c, err := g.server.DB.ChangeSkillBuild(ctx, g.lease, func(c storage.Character, r storage.Roster) (json.RawMessage, error) {
		if !g.skillOwner(c, r.Phase, req, duel) {
			refusal = storage.ErrPhase
			return nil, nil
		}
		training := duel && g.channel == 252 && g.duelPosition != nil && g.duelPosition.Town == 10 && (g.duelPosition.Area == 0 || g.duelPosition.Area == 4) &&
			g.server.delegated && (g.arcade == nil || g.arcade.Closed)
		action, refusal = cat.Quests.PlanSkill(c, req, questwire.SkillContext{
			Duel: duel, TrainingLobby: training, FreeSkills: cat.OpeningCombat.QuestFreeSkills,
			SkillMaster: r.SkillMasterExpiresAt > time.Now().Unix(), Session: g.questSkills,
		})
		if refusal != nil {
			return nil, nil
		}
		return action.Mutation, nil
	})
	if errors.Is(err, storage.ErrPhase) {
		refusal = err
	} else if err != nil {
		return err
	}
	if refusal != nil {
		if f.Opcode == 439 {
			return nil
		}
		if f.Opcode == 331 {
			return g.send(1, 331, []byte{0})
		}
		if f.Opcode == 483 && c.ID != 0 {
			var original actorProfile
			if json.Unmarshal(c.Gameplay, &original) == nil && original.Initialized {
				if err = g.sendSkillBuild(c, duel, nil); err != nil {
					return err
				}
			}
		}
		return g.send(1, f.Opcode, binary.LittleEndian.AppendUint16([]byte{0}, questwire.SkillError(refusal)))
	}
	g.questSkills = action.Session
	if action.ProjectionBefore {
		if err = g.sendSkillBuild(c, duel, action.Display); err != nil {
			return err
		}
	}
	if action.Ack != nil {
		if err = g.send(1, f.Opcode, action.Ack); err != nil {
			return err
		}
	}
	if action.ProjectionAfter {
		if err = g.sendSkillBuild(c, duel, action.Display); err != nil {
			return err
		}
	}
	if action.TrainingID != 0 && action.ProjectionAfter {
		in, err := g.server.DB.Inventory(ctx, g.lease)
		if err != nil {
			return err
		}
		rs, err := g.server.DB.Receipts(ctx, g.lease)
		if err != nil {
			return err
		}
		if err = g.sendQuestBook(storage.TownQuestResult{Character: c, Inventory: in, Receipts: rs}); err != nil {
			return err
		}
		ack := binary.LittleEndian.AppendUint16([]byte{1}, action.TrainingID)
		return g.send(1, 33, binary.LittleEndian.AppendUint32(ack, action.Remaining))
	}
	return nil
}

func (g *game) skillOwner(c storage.Character, phase storage.Phase, req questwire.SkillRequest, duel bool) bool {
	if req.Opcode == 331 || req.Opcode == 439 {
		return phase == storage.Selected || phase == storage.TownLoading || phase == storage.TownReady
	}
	if req.Overlay() {
		return g.channel == 252 && g.server.delegated && phase == storage.Selected && c.Level >= 50 && g.duelPosition != nil && g.duelPosition.Town == 10 && g.duelPosition.Area == 4 &&
			!g.ordinary.Active() && !g.ordinary.Returning() && g.opening == nil && g.arcade.CanCompleteQuest(g.lease, c.ID)
	}
	if duel {
		return phase == storage.Selected && c.Level >= 50 && g.duelPosition != nil && g.duelPosition.Town == 10 &&
			!g.ordinary.Active() && !g.ordinary.Returning() && g.opening == nil && (g.arcade == nil || g.arcade.Closed)
	}
	if g.channel != 19 || phase != storage.TownReady || g.ordinary.Returning() {
		return false
	}
	if g.ordinary.Active() || g.opening != nil {
		var p struct{ Progression struct{ Hp uint32 } }
		if req.Opcode != 28 || json.Unmarshal(c.Gameplay, &p) != nil || p.Progression.Hp == 0 {
			return false
		}
		if run := g.ordinary.Run; run != nil {
			return run.Character == c.ID && run.PlayerDeath == nil && !run.InLayer
		}
		return g.opening != nil
	}
	return true
}

func (g *game) sendSkillBuild(c storage.Character, duel bool, override json.RawMessage) error {
	if override == nil && duel && g.channel == 252 && g.duelPosition != nil && g.duelPosition.Town == 10 && g.duelPosition.Area == 4 {
		override = g.catalog().Quests.OverlayDisplay(c, g.questSkills)
	}
	if override != nil {
		c.Gameplay = override
	} else if duel {
		var err error
		c, err = duellobby.Display(c)
		if err != nil {
			return err
		}
	}
	var p actorProfile
	if err := json.Unmarshal(c.Gameplay, &p); err != nil {
		return err
	}
	_, skills, err := skillPackets(p)
	if err != nil {
		return err
	}
	return g.send(0, 19, skills)
}
