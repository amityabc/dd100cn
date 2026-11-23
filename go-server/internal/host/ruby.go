package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"s9a1/server/internal/fate"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/ruby"
	"s9a1/server/internal/savejson"
	"s9a1/server/internal/storage"
)

type rubySelection struct {
	Character uint16
	View      ruby.View
}

func (g *game) rubyTreasury(ctx context.Context, op uint16, body []byte) error {
	reject := func() error { return g.send(1, op, []byte{0, 0, 0}) }
	compound := op == 1875
	slots, err := ruby.ReadSlots(body, compound)
	if err != nil {
		return reject()
	}
	dungeons := g.catalog().Dungeons
	if g.channel != 19 || g.opening != nil || g.ordinary.Returning() || dungeons == nil || dungeons.Ruby == nil {
		return reject()
	}
	rules := dungeons.Ruby
	run := g.ordinary.Run
	now := time.Now()
	var frozen *rubySelection
	var bag []storage.Asset
	var bosses []string
	var bossProof bool
	if compound && run != nil {
		bosses, bossProof = run.RubyClearedBosses()
	}
	out, err := g.server.DB.ChangeRubyTreasury(ctx, g.lease, g.catalog().Weights, bosses, func(c storage.Character, in storage.Inventory, prior map[string]json.RawMessage) (storage.TownQuestMutation, error) {
		var m storage.TownQuestMutation
		p, err := readGameplay(c.Gameplay)
		if err != nil {
			return m, err
		}
		if !p.Initialized || c.Level < 100 {
			return m, ruby.ErrInvalid
		}
		gate, selecting := g.ordinary.GateSelection(c.ID)
		if compound {
			if (run == nil || run.Character != c.ID || run.Definition.Gate != ruby.Gate || !run.Ready) && (!selecting || gate != ruby.Gate) {
				return m, ruby.ErrInvalid
			}
		} else if run != nil || dungeons.Gate(c.Position.Town, c.Position.Area) != ruby.Gate || selecting && gate != ruby.Gate {
			return m, ruby.ErrInvalid
		}
		l, err := ruby.Read(prior, c.ID, c.Revision, now)
		if err != nil {
			return m, err
		}
		if len(slots) == 0 {
			frozen = &rubySelection{c.ID, l.View}
			return m, nil
		}
		b64 := savejson.String(base64.StdEncoding.EncodeToString(body))
		next := in
		var key, result string
		var proof any
		if compound {
			if run == nil || !ruby.Hidden(run.Definition.Dungeon.ID) || rules.Compound == nil {
				return m, ruby.ErrInvalid
			}
			entry, ok := l.Entries[run.ID]
			if !ok || entry.Week != l.View.Week || entry.Dungeon != run.Definition.Dungeon.ID {
				return m, ruby.ErrInvalid
			}
			clearKey := ruby.ClearKey(entry)
			clear, ok := l.Clears[clearKey]
			if !ok || clear.Run != run.ID {
				return m, ruby.ErrInvalid
			}
			if !bossProof {
				return m, ruby.ErrInvalid
			}
			for _, key := range bosses {
				if _, ok = prior[key]; !ok {
					return m, ruby.ErrInvalid
				}
			}
			key = fmt.Sprintf("ruby-compound:%d:%d", entry.Week, entry.Floor)
			if prev, ok := l.Receipt(key); ok {
				var saved ruby.Composition
				if json.Unmarshal([]byte(prev.Input), &saved) != nil || saved.Run != run.ID || saved.Body != b64 {
					return m, ruby.ErrInvalid
				}
				return m, nil
			}
			indices, err := l.View.Indices(slots, false)
			if err != nil || len(l.View.Boxes) >= 100 {
				return m, ruby.ErrInvalid
			}
			items := make([]uint32, 0, len(indices))
			for _, i := range indices {
				items = append(items, l.View.Boxes[i])
			}
			if _, err = rules.Compound.Product(entry.Dungeon, items, 0); err != nil {
				return m, err
			}
			sample, err := fate.Random(1500)
			if err != nil {
				return m, err
			}
			product, err := rules.Compound.Product(entry.Dungeon, items, sample)
			if err != nil {
				return m, err
			}
			proof = ruby.Composition{Policy: ruby.CompoundPolicy, Week: entry.Week, Clear: clearKey, Run: run.ID, Body: b64, Slots: slots, Items: items, Product: product, Sample: sample, Hash: rules.Hash, Rules: *rules.Compound}
			result = "compounded"
		} else {
			if g.rubyView == nil || g.rubyView.Character != c.ID || g.rubyView.View.Week != l.View.Week {
				return m, ruby.ErrInvalid
			}
			claim, err := l.PlanClaim(g.rubyView.View, body, rules.Hash)
			if err != nil {
				return m, err
			}
			if claim.Replay {
				return m, nil
			}
			key, proof = claim.Key, claim.Proof
			next, err = storage.RubyBoxGrants(c, in, g.catalog().Commerce.Templates, claim.Proof.Items, now)
			if err != nil {
				return m, err
			}
			result = "claimed"
			old := map[string]uint32{}
			for _, a := range in.Items {
				old[a.UID] = a.Count
			}
			for _, a := range next.Items {
				if a.Location == 0 && a.Count > old[a.UID] {
					bag = append(bag, a)
				}
			}
		}
		input, err := json.Marshal(proof)
		if err != nil {
			return m, err
		}
		receipts, err := storage.ActivityReceipt(c, prior, key, string(input), result, storage.QuestGranted(in, next))
		if err != nil {
			return m, err
		}
		m.Reward = &storage.QuestRewardMutation{Character: c, Inventory: next, Receipts: receipts}
		return m, nil
	})
	if errors.Is(err, ruby.ErrInvalid) || errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrCapacity) || errors.Is(err, storage.ErrConflict) {
		return reject()
	}
	if err != nil {
		return err
	}
	if frozen != nil {
		g.rubyView = frozen
	}
	l, err := ruby.Read(out.Receipts, out.Character.ID, out.Character.Revision, now)
	if err != nil {
		return err
	}
	if len(bag) > 0 {
		b, err := inventorywire.DungeonBagDelta(out.Inventory, out.Character, bag, true)
		if err != nil {
			return err
		}
		if err = g.send(0, 14, b); err != nil {
			return err
		}
	}
	b, err := l.View.Wire()
	if err != nil {
		return err
	}
	if err = g.send(0, 1738, b); err != nil {
		return err
	}
	if compound && len(slots) == 0 {
		return nil
	}
	return g.send(1, op, []byte{1})
}
