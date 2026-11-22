package host

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"s9a1/server/internal/fate"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/savejson"
	"s9a1/server/internal/storage"
)

func (e *fateEdit) receipt(key string, input, result any, grants json.RawMessage) error {
	b, err := json.Marshal(input)
	if err != nil {
		return err
	}
	var answer string
	if text, ok := result.(string); ok {
		answer = text
	} else {
		r, err := json.Marshal(result)
		if err != nil {
			return err
		}
		answer = string(r)
	}
	e.Receipts, err = storage.ActivityReceipt(e.Character, e.Prior, key, string(b), answer, grants)
	return err
}
func (e *fateEdit) settle(d *fate.Definition, now time.Time, forfeit bool, body string, next *fateAction) error {
	before := e.Profile.Run
	p, phases, err := d.Settle(e.Profile, now, forfeit, body)
	if err != nil {
		return err
	}
	if p.Run == before {
		return nil
	}
	day := p.Run.SettlementDay
	type reward struct{ TemplateId, Count uint32 }
	rewards := make([]reward, 0, len(phases))
	for _, phase := range phases {
		r := d.Rewards[phase]
		id, err := fate.TemplateID(r.Template)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("fate:%d:%s:%d", d.EventId, day, phase)
		var fresh bool
		e.Character, fresh, err = storage.ActivityRewardMail(e.Character, key, "活动系统", "命运的抉择Ⅱ阶段奖励", fmt.Sprintf("第%d阶段奖励", phase+1), r.Template, r.Count, now)
		if err != nil {
			return err
		}
		next.Mail = next.Mail || fresh
		rewards = append(rewards, reward{id, r.Count})
	}
	stages := append([]int{}, before.CompletedPhases...)
	slices.Sort(stages)
	input := struct {
		ResourceHash, Day string
		Body              savejson.String
		Forfeit           bool `json:"forfeit"`
		Stages            []int
	}{d.ResourceHash, day, savejson.String(body), forfeit, stages}
	result := struct {
		FullClears  int32
		DailyClaims []string
		Delivered   []reward
	}{p.FullClears, p.DailyClaims, rewards}
	if err = e.receipt("fate-result:"+strings.ReplaceAll(before.RunId, "-", ""), input, result, nil); err != nil {
		return err
	}
	e.Profile = p
	return e.save()
}

func (g *game) visitFate(f protocol.Frame, e *fateEdit, next *fateAction, d *fate.Definition, now time.Time) error {
	v, err := fate.ReadVisit(f.Body)
	if err != nil {
		return err
	}
	p, kind, replay, err := d.Visit(e.Profile, v, now)
	if err != nil {
		return err
	}
	r := p.Run
	if replay {
		if kind == 3 && v.Phase == r.Phase && !r.Dead && !r.Settled {
			for i := range r.Drops {
				if r.Drops[i].Phase == v.Phase && !r.Drops[i].Claimed {
					next.Drop = &r.Drops[i]
					break
				}
			}
		}
		return nil
	}
	if kind == 3 {
		item, err := d.RollEffect(e.Profile)
		if err != nil {
			return err
		}
		if item != nil {
			id := g.fateScene + 1
			if id == 0 || id >= math.MaxUint16 || len(d.BossPositions) != 5 {
				return fate.ErrInvalid
			}
			pos := d.BossPositions[v.Phase]
			drop := fate.Drop{ObjectKey: id, SceneSlot: uint16(id), Phase: v.Phase, EffectId: item.EffectId, Template: item.Template, X: pos.X, Y: pos.Y, RuleHash: d.ResourceHash, NodeKey: v.Key()}
			r.Drops = append(slices.Clone(r.Drops), drop)
			next.Drop = &drop
			next.NextScene = id
		}
	}
	if err = d.Validate(p); err != nil {
		return err
	}
	input := struct {
		Policy, ResourceHash string
		Phase, Node          int
		Body                 savejson.String
		Ground               *fate.Drop `json:"ground"`
	}{"LocalClientDelegatedDestiny2/v1", d.ResourceHash, v.Phase, v.Node, savejson.String(v.Body), next.Drop}
	completed := append([]int{}, r.CompletedPhases...)
	slices.Sort(completed)
	result := struct {
		ArchangelPoints, StoryCount int32
		Completed                   []int
	}{p.ArchangelPoints, p.StoryCount, completed}
	key := fmt.Sprintf("fate-node:%s:%d:%d", strings.ReplaceAll(r.RunId, "-", ""), v.Phase, v.Node)
	if err = e.receipt(key, input, result, nil); err != nil {
		return err
	}
	e.Profile = p
	return e.save()
}
func (g *game) pickupFate(f protocol.Frame, e *fateEdit, next *fateAction, d *fate.Definition) error {
	id, x, y, err := fate.ReadPickup(f.Body)
	if err != nil {
		return err
	}
	r := e.Profile.Run
	for i := range r.Drops {
		drop := &r.Drops[i]
		if drop.ObjectKey != id {
			continue
		}
		if drop.Phase != r.Phase || r.Dead || r.Settled || drop.RuleHash != d.ResourceHash || drop.X != x || drop.Y != y {
			return fate.ErrInvalid
		}
		row := slices.IndexFunc(d.Collection, func(item fate.CollectionItem) bool {
			return item.EffectId == drop.EffectId && fate.EqualTemplate(item.Template, drop.Template)
		})
		if row < 0 {
			return fate.ErrInvalid
		}
		item := d.Collection[row]
		body := fateIdentity(f.Body)
		next.Slot, next.Drop = item.Slot, drop
		if drop.Claimed {
			if drop.PickupBody != body {
				return fate.ErrInvalid
			}
			next.Replayed = true
			return nil
		}
		var phase, node int
		if _, err = fmt.Sscanf(drop.NodeKey, "%d:%d", &phase, &node); err != nil || phase != drop.Phase || drop.NodeKey != fmt.Sprintf("%d:%d", phase, node) {
			return fate.ErrInvalid
		}
		template, err := fate.TemplateID(item.Template)
		if err != nil {
			return err
		}
		added, err := storage.ActivityCollection(e.Inventory, item.Slot, item.Template)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("fate-pickup:%s:%d", strings.ReplaceAll(r.RunId, "-", ""), id)
		origin := struct {
			Source, RunId, RuleHash string
			Phase, Node             int
			ObjectKey               uint32
			EffectId                int32
			TemplateId              uint32
			Slot                    uint16
			Body                    savejson.String
		}{"pickup", r.RunId, d.ResourceHash, phase, node, id, item.EffectId, template, item.Slot, savejson.String(body)}
		if err = e.receipt(key, origin, fmt.Sprintf("collection:%d:%d", item.Slot, template), storage.QuestGranted(e.Inventory, added)); err != nil {
			return err
		}
		drop.Claimed, drop.PickupBody = true, body
		e.Inventory = added
		return e.save()
	}
	return fate.ErrInvalid
}
