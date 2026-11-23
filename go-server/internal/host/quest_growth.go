package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
	"time"
)

func (g *game) hasQuestExperience(id uint32) bool {
	return g.catalog().Quests.HasExperience(id)
}

func (g *game) questExperienceBook(ctx context.Context, body []byte) error {
	return g.useQuestGrowth(ctx, 30, body)
}
func (g *game) questExperienceItem(ctx context.Context, body []byte) error {
	return g.useQuestGrowth(ctx, 44, body)
}
func (g *game) questLevelTicket(ctx context.Context, body []byte) error {
	return g.useQuestGrowth(ctx, 410, body)
}

func (g *game) useQuestGrowth(ctx context.Context, op uint16, b []byte) error {
	req, err := questwire.ReadGrowth(op, g.seq, b)
	if err != nil {
		return err
	}
	reject := func() error {
		if err := g.send(1, op, req.Reply(false, 0, 0, 0)); err != nil {
			return err
		}
		if op == 30 {
			n, err := protocol.Announcement("经验书无法使用，请确认角色未满100级、道具未锁定且在物品栏中。")
			if err != nil {
				return err
			}
			return g.send(0, 488, n)
		}
		return nil
	}
	if g.channel != 19 || g.ordinary.Returning() {
		return reject()
	}
	cat := g.catalog()
	now := time.Now()
	var action questwire.GrowthResult
	inDungeon := g.opening != nil || g.ordinary.Active()
	out, err := g.server.DB.ChangeTownQuest(ctx, g.lease, cat.Weights, func(c storage.Character, in storage.Inventory, _ map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		opts := questwire.GrowthContext{Now: now, Range: cat.OpeningCombat.ExperienceRange, Apply: cat.OpeningCombat.ApplyItemExperience, Replay: g.questGrowthReplay}
		if run := g.ordinary.Run; run != nil {
			scene := g.questDungeon()
			if scene == nil || scene.Character != c.ID || run.Settlement.Confirmation != nil {
				return storage.TownQuestMutation{}, storage.ErrInvalid
			}
			opts.Place = "dungeon"
			opts.Scene = fmt.Sprintf("%s:%d:%d:%d:%t", scene.RunID, scene.X, scene.Y, scene.Map, run.InLayer)
		} else if g.opening != nil {
			opts.Scene = g.opening.ConsumableScene(c.ID)
			if opts.Scene == "" {
				return storage.TownQuestMutation{}, storage.ErrInvalid
			}
			opts.Place = "dungeon"
		} else {
			q, err := resources.ReadTownQuests(c.Gameplay)
			if err != nil {
				return storage.TownQuestMutation{}, err
			}
			opts.Place = "village"
			if cat.Towns.IsRoom(point(c.Position)) {
				opts.Place = "seria room"
			}
			opts.TownValid = cat.AllowsTownPosition(c.Level, q, point(c.Position))
		}
		var err error
		action, err = cat.Quests.UseGrowth(ctx, c, in, req, opts)
		return action.Mutation, err
	})
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) {
		return reject()
	}
	if err != nil {
		return err
	}
	if !action.Repeated {
		g.questGrowthReplay = action.Replay
		for _, id := range action.Removed {
			delete(g.questAcceptSeen, id)
			delete(g.questConfirm, id)
		}
	}
	if err = g.send(1, op, req.Reply(!action.Repeated, action.Award, action.Sp, action.Tp)); err != nil {
		return err
	}
	if inDungeon {
		c, err := questActorInventory(out)
		if err != nil {
			return err
		}
		actor, profile, err := readActor(c, g.channel)
		if err != nil {
			return err
		}
		if err = g.send(0, 37, dungeon.ExperienceNotice(actor, profile.Progression.Sp, profile.Progression.Tp)); err != nil {
			return err
		}
		bag, err := inventorywire.BagAndWear(out.Inventory, c)
		if err != nil {
			return err
		}
		return g.send(0, bag[0].Opcode, bag[0].Body)
	}
	return g.publishTownGrowth(out, now)
}

func (g *game) publishTownGrowth(out storage.TownQuestResult, now time.Time) error {
	c, err := questActorInventory(out)
	if err != nil {
		return err
	}
	actor, _, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	m0, err := g.actorMode0(actor)
	if err != nil {
		return err
	}
	m1, err := protocol.Mode1(actor, true)
	if err != nil {
		return err
	}
	if err = g.send(0, 2, m0); err != nil {
		return err
	}
	if err = g.send(0, 2, m1); err != nil {
		return err
	}
	if err = g.sendSkillBuild(c, false, nil); err != nil {
		return err
	}
	if err = g.sendQuestBookOrdered(out, true); err != nil {
		return err
	}
	assets, _, err := inventorywire.Town(out.Inventory, c, inventorywire.Options{CashPolicies: g.catalog().CashPolicies, Now: now})
	if err != nil {
		return err
	}
	return g.projected(assets)
}

func (g *game) questCapsule(ctx context.Context, body []byte) error {
	r, err := questwire.ReadCapsule(g.seq, body)
	if err != nil {
		return err
	}
	reject := func() error { return g.send(1, 507, r.Reply(false)) }
	if g.channel != 19 || g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() {
		return reject()
	}
	cat := g.catalog()
	now := time.Now()
	var action questwire.GrowthResult
	out, err := g.server.DB.ChangeTownQuest(ctx, g.lease, cat.Weights, func(c storage.Character, in storage.Inventory, _ map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		q, err := resources.ReadTownQuests(c.Gameplay)
		if err != nil {
			return storage.TownQuestMutation{}, err
		}
		position := point(c.Position)
		if !cat.Towns.IsRoom(position) || !cat.AllowsTownPosition(c.Level, q, position) {
			return storage.TownQuestMutation{}, storage.ErrInvalid
		}
		action, err = cat.Quests.UseCapsule(c, in, r, now, g.questGrowthReplay, cat.OpeningCombat.ApplyLevelCapsule)
		return action.Mutation, err
	})
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) {
		return reject()
	}
	if err != nil {
		return err
	}
	if !action.Repeated {
		g.questGrowthReplay = action.Replay
	}
	if err = g.send(1, 507, r.Reply(!action.Repeated)); err != nil {
		return err
	}
	return g.publishTownGrowth(out, now)
}
