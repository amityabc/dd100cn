package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/rewardcount"
	"s9a1/server/internal/savejson"
	"s9a1/server/internal/storage"
)

func (g *game) settlementKey(run *dungeon.Run, prefix string) string {
	return fmt.Sprintf("%s:v1:%s:%d:%d:%s", prefix, strings.ReplaceAll(g.lease.Owner, "-", ""),
		g.lease.Epoch, run.Character, strings.ReplaceAll(run.ID, "-", ""))
}

func (g *game) settlementInput(ctx context.Context, run *dungeon.Run, c storage.Character, actor uint16, body []byte) ([]byte, error) {
	def := run.Definition.Dungeon
	version := "local100-settlement-v1-no-generated-card-awards"
	var clear *storage.OculusClearQualification
	var named []dungeon.NamedExperience
	if run.OculusEntry != nil {
		var err error
		clear, err = g.oculusClearQualification(ctx, run, c, actor)
		if err != nil {
			return nil, err
		}
		version = storage.OculusClearPolicy
	}
	if clear != nil || def.ID < 100000000 {
		_, named = run.RewardProof()
		if len(named) > 256 {
			return nil, storage.ErrConflict
		}
	}
	return savejson.Marshal(struct {
		Version, Owner          string
		Epoch                   int64
		CharacterId             uint16
		RunId                   string
		Dungeon                 uint32
		Maze                    byte
		BossMap                 uint32
		BossActor               uint16
		ResourceHash            string
		ElapsedMilliseconds     uint32
		MonsterExperienceTotals [4]uint32
		Body                    string
		NamedExperience         []dungeon.NamedExperience         `json:",omitempty"`
		Oculus                  *storage.OculusClearQualification `json:",omitempty"`
	}{version, g.lease.Owner, g.lease.Epoch, run.Character, run.ID, def.ID,
		def.MazeIndex, run.CombatRoom().MapID, actor, def.ResourceHash, run.Elapsed, run.Totals, base64.StdEncoding.EncodeToString(body), named, clear})
}

func (g *game) dungeonTryConfirm(ctx context.Context, run *dungeon.Run) error {
	if run.WisdomEntry != nil || run.RubyEntry != nil {
		return nil
	}
	body, ready := run.ScriptedConfirmation()
	if !ready {
		return nil
	}
	roster, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(roster)
	if err != nil {
		return err
	}
	if run.Settlement.Pending == nil {
		pending, err := g.server.Characters.Quests.PendingDungeonRoomObjective(c, run.Definition.Dungeon.ID, run.Room)
		if err != nil || pending {
			return err
		}
	}
	return g.dungeonConfirmation(ctx, run, c, body, true)
}

func (g *game) dungeonTryMovieSettlement(ctx context.Context, run *dungeon.Run, questCallback bool) error {
	victim, ready := run.MovieSettlementActor()
	if !g.server.delegated || !ready {
		return nil
	}
	roster, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(roster)
	if err != nil {
		return err
	}
	quests := g.server.Characters.Quests
	def := run.Definition.Dungeon
	pending, err := quests.PendingDungeonRoomObjective(c, def.ID, run.Room)
	if err != nil || pending {
		return err
	}
	if !questCallback {
		pending, err := quests.HasActiveDungeonCinematicQuest(c, def.ID)
		if err != nil || pending {
			return err
		}
	}
	body := make([]byte, 39)
	binary.LittleEndian.PutUint16(body, c.ID)
	binary.LittleEndian.PutUint16(body[2:], victim)
	return g.dungeonConfirmation(ctx, run, c, body, true)
}

func (g *game) dungeonConfirmation(ctx context.Context, run *dungeon.Run, c storage.Character, body []byte, script bool) error {
	actor, owner, err := dungeon.ReadConfirmation(body)
	if err != nil {
		return err
	}
	s := &run.Settlement
	reject := func() error { return g.send(0, 115, []byte{0, 0}) }
	if !g.server.delegated || !run.Ready || run.PlayerDeath != nil || run.AbaddonFailed() || owner != c.ID {
		return reject()
	}
	mon, boss, dead := run.CurrentBoss(actor)
	if run.OculusEntry != nil {
		mode, ok := g.oculusMode()
		boss = boss && ok && run.Room.MapID == mode.ClearMapId && mon.Code == mode.Rules.BossMonsterId
		if run.Cleared && g.oculus.phase != oculusCleared {
			return reject()
		}
	}
	if actor == 0 && script && run.EmptyBossRoom() {
		boss, dead = true, true
	}
	if !boss {
		if mon.Code != 0 {
			return g.send(0, 115, dungeon.BossNotice(actor, false))
		}
		return reject()
	}
	if !run.Cleared {
		if s.Confirmation != nil || !run.SetPendingConfirmation(actor, body) {
			return reject()
		}
		return g.send(0, 115, dungeon.BossNotice(actor, false))
	}
	if !dead || !run.ClearingConfirmationMatches(actor, body) {
		return reject()
	}
	replay := s.Confirmation != nil
	if replay && (s.Boss != actor || !bytes.Equal(s.Confirmation, body)) {
		return reject()
	}
	proofBody := body
	if marker, ok := run.TempleConfirmationBody(actor); ok {
		proofBody = marker
	}
	input, err := g.settlementInput(ctx, run, c, actor, proofBody)
	if err != nil {
		if errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) {
			return reject()
		}
		return err
	}
	result, err := run.SettlementResultJSON(dungeon.ClearResult{ElapsedMilliseconds: run.Elapsed, MonsterExperienceTotals: run.Totals})
	if err != nil {
		return err
	}
	change := storage.DungeonChange{Key: g.settlementKey(run, "dungeon-settlement") + ":entry",
		Input: string(input), Result: string(result), Replay: replay, Next: c, Proof: run.ClearProofKeys(actor)}
	if run.OculusEntry != nil {
		change.Proof = append(run.OculusProofKeys(), run.OculusEntry.Key())
	}
	if run.AbaddonEntry != nil {
		change.Proof = append(change.Proof, run.AbaddonEntry.Key())
	}
	if err := g.server.DB.CommitDungeonCombat(ctx, g.lease, c, change); err != nil {
		if errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) {
			return reject()
		}
		return err
	}
	s.Boss, s.Confirmation, s.Pending = actor, bytes.Clone(body), nil
	s.EntryInput, s.EntryResult = change.Input, change.Result
	var frames []opening.Packet
	if run.OculusEntry != nil && !replay {
		frames = append(frames, opening.Packet{Opcode: 1771, Body: g.oculus.state(3, 1, run.Elapsed)})
	}
	frames = append(frames, opening.Packet{Opcode: 115, Body: dungeon.BossNotice(actor, true)})
	if !replay && run.OculusEntry == nil {
		frames = append(frames, opening.Packet{Opcode: 31, Body: []byte{0}})
	}
	return g.packets(frames)
}

func (g *game) dungeonResult(ctx context.Context, run *dungeon.Run, c storage.Character, body []byte, a protocol.Actor) error {
	if _, err := dungeon.ReadResult(body); err != nil {
		return err
	}
	s := &run.Settlement
	if s.Confirmation == nil || !run.Cleared || run.PlayerDeath != nil || run.AbaddonFailed() {
		return storage.ErrPhase
	}
	if run.OculusEntry != nil && !g.oculus.movie {
		return storage.ErrPhase
	}
	next := c
	var z dungeon.ClearResult
	input, err := g.settlementInput(ctx, run, c, s.Boss, body)
	if err != nil {
		return err
	}
	key := g.settlementKey(run, "dungeon-settlement")
	frozen, err := g.server.DB.FrozenDungeonResult(ctx, g.lease, c, key, s.EntryInput, s.EntryResult, string(input))
	if err != nil {
		return err
	}
	replay := frozen != ""
	if s.Result != nil && !replay {
		return storage.ErrConflict
	}
	var actual uint32
	if replay {
		z, err = run.ReadSettlementResult([]byte(frozen), body)
		if err != nil {
			return err
		}
	} else {
		cat := g.server.Characters.Dungeons
		luck, err := cat.Luck.Start(c)
		if err != nil {
			return err
		}
		z, err = run.ClearResult(cat, body, run.RewardLevel, a.Experience, luck)
		if err != nil {
			return err
		}
		if source, ok := cat.SpecialRewards[run.Definition.Dungeon.ID]; ok && z.FreeCardReward != nil {
			now := time.Now()
			day := rewardcount.Day(now)
			receipts, err := g.server.DB.DailyRewardReceipts(ctx, g.lease, now)
			if err != nil {
				return err
			}
			used, limit, err := rewardcount.Allowance(c, receipts, day, source.Counter, source.Limit)
			if err != nil {
				return err
			}
			z.SpecialReward, err = source.PlanAt(run, c.Job, a.GrowType, day, used, limit, now)
			if err != nil {
				return err
			}
		}
		if award := z.ClearExperienceAward + z.RankExperienceAward; award > 0 {
			next, actual, err = g.server.Characters.OpeningCombat.ApplyExperience(c, award)
			if err != nil {
				return err
			}
			if actual != award {
				return storage.ErrConflict
			}
		}
		if run.AbaddonEntry != nil {
			times, mail, err := g.server.DB.AbaddonSettlement(ctx, g.lease, c, *run.AbaddonEntry, run.Elapsed, run.AbaddonFinalReward())
			if err != nil {
				return err
			}
			z.AbaddonTimes, z.AbaddonMail = &times, mail
			if mail != nil {
				z.AbaddonReward = &mail.Reward.Reward
			}
		}
		if run.OculusEntry != nil {
			now := time.Now()
			week, used, err := g.server.DB.OculusRewardWeek(ctx, g.lease, c, now)
			if err != nil {
				return err
			}
			z.OculusReward, err = cat.OculusCard(run.Definition.Dungeon.ID, week, used, now)
			if err != nil {
				return err
			}
		}
	}
	if !replay {
		output, err := run.SettlementResultJSON(z)
		if err != nil {
			return err
		}
		frozen = string(output)
	}
	change := storage.DungeonChange{Key: key + ":result", Input: string(input), Result: frozen, ExperienceAward: actual,
		Replay: replay, Next: next, Proof: []string{key + ":entry"}}
	if run.OculusEntry != nil {
		change.Proof = append(change.Proof, run.OculusEntry.Key())
		change.Proof = append(change.Proof, run.OculusProofKeys()...)
	}
	if run.AbaddonEntry != nil {
		change.Abaddon = &storage.AbaddonMutation{Entry: *run.AbaddonEntry, Final: z.AbaddonMail, Now: time.Now()}
	}
	if z.FreeCardReward == nil && z.CompletionReward != nil {
		change.ImmediateGrants, change.GrantTime = z.CompletionReward.Grants, time.Now()
	}
	if err := g.server.DB.CommitDungeonCombat(ctx, g.lease, c, change); err != nil {
		return err
	}
	s.Result = &z
	if run.AbaddonEntry != nil || z.FreeCardReward == nil && z.CompletionReward != nil {
		s.FreeClaimed = true
	}
	if replay && (s.FreeClaimed || s.PaidClaimed) {
		return nil
	}
	a, actorProfile, err := readActor(next, g.channel)
	if err != nil {
		return err
	}
	frames := []opening.Packet{
		{Opcode: 34, Body: z.PlayNotice(c.ID)}, {Opcode: 37, Body: dungeon.ExperienceNotice(a, actorProfile.Progression.Sp, actorProfile.Progression.Tp)},
		{Opcode: 35, Body: z.RewardNotice()},
	}
	if z.PaidCardReward != nil && z.PaidCardReward.Surprise && !s.PaidClaimed {
		frames = append(frames, opening.Packet{Opcode: 301})
	}
	frames = append(frames, opening.Packet{Opcode: 36, Body: protocol.Fatigue(a)})
	if !replay && z.AbaddonTimes != nil {
		b := binary.LittleEndian.AppendUint32(nil, z.AbaddonTimes.Best)
		b = binary.LittleEndian.AppendUint32(b, z.AbaddonTimes.Elapsed)
		frames = append(frames, opening.Packet{Opcode: 1724, Body: append(b, 0, 0, 0, 0)})
	}
	if err := g.packets(frames); err != nil || replay || z.AbaddonMail == nil {
		return err
	}
	roster, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	current, err := selected(roster)
	if err != nil {
		return err
	}
	return g.publishAbaddonMail(current, z.AbaddonMail)
}

func (g *game) dungeonCards(ctx context.Context, run *dungeon.Run, c storage.Character, f protocol.Frame, fatigue uint16) error {
	s := &run.Settlement
	reject := func() error { return g.send(1, 71, dungeon.CardOwners(false, s.FreeClaimed, false)) }
	switch f.Opcode {
	case 69, 70:
		if len(f.Body) != 0 {
			return storage.ErrInvalid
		}
		if err := g.validateDungeonResult(ctx, run, c); err != nil {
			return err
		}
		if f.Opcode == 69 {
			return g.send(1, 69, []byte{1})
		}
		if !s.LayoutSent && run.AbaddonEntry == nil {
			s.LayoutSent, s.LayoutOpenedAt = true, time.Now()
		}
		return g.send(1, 70, dungeon.CardLayout(fatigue))
	case 71:
		b := f.Body
		if !(len(b) == 1 && b[0] == 0 || len(b) == 2 && b[0] <= 1 && b[1] == 0) {
			return storage.ErrInvalid
		}
		if run.AbaddonEntry != nil || !s.LayoutSent || s.Result == nil || run.PlayerDeath != nil {
			return reject()
		}
		return g.claimDungeonCard(ctx, run, c, b[0] == 1, false)
	}
	return storage.ErrPhase
}

func (g *game) validateDungeonResult(ctx context.Context, run *dungeon.Run, c storage.Character) error {
	s := &run.Settlement
	if s.Result == nil || !run.Cleared {
		return storage.ErrPhase
	}
	body := s.Result.Body
	input, err := g.settlementInput(ctx, run, c, s.Boss, body)
	if err != nil {
		return err
	}
	frozen, err := g.server.DB.FrozenDungeonResult(ctx, g.lease, c, g.settlementKey(run, "dungeon-settlement"),
		s.EntryInput, s.EntryResult, string(input))
	if err != nil {
		return err
	}
	if frozen == "" {
		return storage.ErrConflict
	}
	_, err = run.ReadSettlementResult([]byte(frozen), body)
	return err
}

func (g *game) claimDungeonCard(ctx context.Context, run *dungeon.Run, c storage.Character, paid, automatic bool) error {
	if run.OculusEntry != nil {
		return g.claimOculusCard(ctx, run, c, paid, automatic)
	}
	s := &run.Settlement
	card := s.Result.FreeCardReward
	completion := s.Result.CompletionReward
	special := s.Result.SpecialReward
	if paid {
		card = s.Result.PaidCardReward
		completion = nil
		special = nil
	}
	if card == nil && !paid {
		firstFree := !s.FreeClaimed
		s.FreeClaimed = true
		ack := dungeon.CardOwners(true, true, false)
		if s.PaidClaimed {
			ack = s.Result.PaidCardReward.PaidNotice(true)
		}
		if err := g.send(1, 71, ack); err != nil {
			return err
		}
		var announcement uint32
		if firstFree {
			announcement = run.Definition.Dungeon.ID
		}
		return g.projectSpecialCounters(ctx, c, announcement)
	}
	key, prefix := g.settlementKey(run, "dungeon-settlement")+":result", "dungeon-free-card"
	if paid {
		prefix = "dungeon-empty-paid-card"
	}
	var plan any
	var version, body string
	switch {
	case paid && card == nil:
		version, body = "local100-zero-price-empty-paid-v1", "AQA="
	case paid:
		version, body = "local100-paid-card-v2", "AQA="
		if card.Artifact() != nil {
			version = "local100-paid-artifact-v1"
		}
		paidPlan, err := card.PaidReceiptPlan()
		if err != nil {
			return err
		}
		plan = paidPlan
	default:
		version = "local100-free-gold-v1"
		switch {
		case card.Artifact() != nil:
			version = "local100-free-artifact-v1"
		case special != nil:
			version = "local100-free-special/v1"
		case completion != nil:
			version = "pvf-native-completion/v1"
		case len(card.Items) != 0:
			version = "local100-free-card-v2"
		}
		freePlan, err := card.FreeReceiptPlan()
		if err != nil {
			return err
		}
		plan = freePlan
	}
	completionPlan, err := run.CounterCompletion(completion)
	if err != nil {
		return err
	}
	input, err := savejson.Marshal(struct {
		Version, Owner   string
		Epoch            int64
		CharacterId      uint16
		RunId, ResultKey string
		Member           byte
		Body             string                         `json:",omitempty"`
		Reward           any                            `json:",omitempty"`
		SpecialReward    *dungeon.SpecialCardPlan       `json:",omitempty"`
		CompletionReward *dungeon.CardCounterCompletion `json:",omitempty"`
	}{version, g.lease.Owner, g.lease.Epoch, c.ID, run.ID, key, 0, body, plan, special, completionPlan})
	if err != nil {
		return err
	}
	var rewardRaw, completionRaw, specialRaw json.RawMessage
	for _, pair := range []struct {
		value any
		raw   *json.RawMessage
	}{{plan, &rewardRaw}, {completionPlan, &completionRaw}, {special, &specialRaw}} {
		raw, err := savejson.Marshal(pair.value)
		if err != nil {
			return err
		}
		if string(raw) != "null" {
			*pair.raw = raw
		}
	}
	next, luck, err := card.ApplyLuck(g.server.Characters.Dungeons.Luck, c)
	if err != nil {
		return err
	}
	request := storage.DungeonCardClaim{Key: g.settlementKey(run, prefix), ResultKey: key, Input: string(input), Paid: paid,
		Reward: rewardRaw, Completion: completionRaw, Special: specialRaw, Next: next, Now: time.Now(),
		PreviousReward: card.PreviousReceiptPlan(), PreviousCompletion: completion.PreviousReceiptPlan(),
		Create: func(in storage.Inventory) ([]storage.QuestGrant, error) {
			grants, err := card.Grants(in)
			if err != nil {
				return nil, err
			}
			specialGrants, err := special.Grants(in)
			if err != nil {
				return nil, err
			}
			grants = append(grants, specialGrants...)
			clearGrants, err := completion.Grants(in)
			return append(grants, clearGrants...), err
		}}
	if luck != nil {
		request.Luck = &storage.DungeonLuckChange{Before: luck.Before, After: luck.After}
		request.LuckProof, err = savejson.Marshal(luck)
		if err != nil {
			return err
		}
	}
	saved, err := g.server.DB.ClaimDungeonCard(ctx, g.lease, c, request)
	if errors.Is(err, storage.ErrFunds) || errors.Is(err, storage.ErrCapacity) {
		if automatic {
			s.FreeTimeoutBlocked = true
			return nil
		}
		return g.send(1, 71, dungeon.CardOwners(false, s.FreeClaimed, false))
	}
	if err != nil {
		return err
	}
	firstFree := !paid && !s.FreeClaimed
	if paid {
		s.PaidClaimed = true
	} else {
		s.FreeClaimed = true
	}
	ack := dungeon.CardOwners(true, s.FreeClaimed, s.PaidClaimed)
	if s.PaidClaimed {
		ack = s.Result.PaidCardReward.PaidNotice(s.FreeClaimed)
	}
	frames := []opening.Packet{{Class: 1, Opcode: 71, Body: ack}}
	if card != nil && (!paid || card.Gold != 0 || card.Cost != 0 || len(card.Items) != 0) {
		delta, err := inventorywire.DungeonBagDelta(saved.Inventory, saved.Character, saved.Grants, true)
		if err != nil {
			return err
		}
		frames = append(frames, opening.Packet{Opcode: 14, Body: delta})
		if card.Artifact() != nil {
			pets, err := inventorywire.CommerceSpecial(saved.Inventory, saved.Character)
			if err != nil {
				return err
			}
			frames = append(frames, opening.Packet{Opcode: pets[1].Opcode, Body: pets[1].Body})
		}
		if paid || completion != nil || special != nil {
			bags, err := inventorywire.BagAndWear(saved.Inventory, saved.Character)
			if err != nil {
				return err
			}
			frames = append(frames, opening.Packet{Opcode: bags[0].Opcode, Body: bags[0].Body})
		}
	}
	if err := g.packets(frames); err != nil || paid {
		return err
	}
	current := c
	current.Revision = saved.Inventory.Revision
	var announcement uint32
	if firstFree {
		announcement = run.Definition.Dungeon.ID
	}
	return g.projectSpecialCounters(ctx, current, announcement)
}

func (g *game) specialCardMessage(dungeonID uint32, remaining byte) error {
	name := map[uint32]string{100000002: "根特皇宫", 100000176: "无底坑道", 100000177: "记忆之地", 100000178: "痛苦地下室", 100000179: "暗黑神殿", 100000007: "复活圣殿", 400000389: "复活圣殿（引导）", 100002669: "复活圣殿（单人）"}[dungeonID]
	if name == "" {
		return storage.ErrInvalid
	}
	raw, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(fmt.Sprintf("%s地下城奖励还剩%d次。", name, remaining)))
	if err != nil {
		return err
	}
	body := binary.LittleEndian.AppendUint32(make([]byte, 4), uint32(len(raw)))
	body = append(body, raw...)
	return g.send(0, 12, binary.LittleEndian.AppendUint32(body, 0))
}

func (g *game) dungeonExit(ctx context.Context, run *dungeon.Run, c storage.Character, a protocol.Actor, body []byte) error {
	if run.TournamentReward() != 0 || run == nil && g.ordinary.TournamentExit != nil {
		return g.dungeonTournamentExit(ctx, run, c, a, body)
	}
	if !(len(body) == 2 || len(body) == 3) || body[0] != 1 && body[0] != 2 || body[1] > 3 ||
		body[0] == 2 && (len(body) != 3 || body[2] != 1) {
		return storage.ErrInvalid
	}
	action, option := body[0], body[1]
	if g.channel == 63 && g.oculus.phase != oculusCleared {
		return g.oculusRetreatExit(ctx, run, c, body)
	}
	outcome := func() error {
		a.AreaState = 0
		if g.channel == 63 {
			a = g.oculusTownActor(a)
		}
		frames := []opening.Packet{{Class: 1, Opcode: 72, Body: []byte{1, action, option}},
			{Opcode: 23, Body: protocol.Area(a)}, {Opcode: 24, Body: protocol.Town(a)}}
		if option == 0 {
			frames = append([]opening.Packet{{Opcode: 36, Body: protocol.Fatigue(a)}}, frames...)
		}
		if err := g.packets(frames); err != nil {
			return err
		}
		return g.specialCounters(ctx, c)
	}
	if run != nil && run.WisdomEntry != nil {
		if err := g.checkWisdomClear(ctx, run, c, option == 0, action == 1); err != nil {
			if wisdomRejected(err) || errors.Is(err, storage.ErrFunds) {
				return g.send(1, 72, []byte{0, 4, 0, 0})
			}
			return err
		}
		if action == 2 {
			return g.send(1, 72, []byte{1, 2, option})
		}
		g.ordinary.BeginExit(body)
		g.ordinary.WisdomExit = true
		return outcome()
	}
	if run != nil && run.RubyEntry != nil {
		if _, err := g.validateRubyClear(ctx, c, run); err != nil {
			return err
		}
		if option == 0 && a.FatigueUsed == a.FatigueLimit {
			return storage.ErrPhase
		}
		if action == 2 {
			return g.send(1, 72, []byte{1, 2, option})
		}
		if err := g.server.DB.ReturnDungeon(ctx, g.lease, c, true); err != nil {
			return err
		}
		g.ordinary.BeginExit(body)
		g.rubyView = nil
		return outcome()
	}
	if run == nil {
		old := g.ordinary.ExitBody
		if _, selecting := g.ordinary.GateSelection(c.ID); len(old) == 0 || selecting {
			return storage.ErrPhase
		}
		if action == 2 {
			return storage.ErrPhase
		}
		if bytes.Equal(body, old) {
			if err := g.server.DB.ValidateDungeonProof(ctx, g.lease, c, nil); err != nil {
				return err
			}
			return outcome()
		}
		if option == 3 && action == 1 && len(body) == 3 && body[2] == 1 &&
			len(old) == 3 && (old[1] == 1 || old[1] == 2) && old[2] == 1 {
			return nil
		}
		return storage.ErrConflict
	}
	s := &run.Settlement
	if err := g.validateDungeonResult(ctx, run, c); err != nil {
		return err
	}
	if action == 2 {
		return g.send(1, 72, []byte{1, action, option})
	}
	if run.AbaddonEntry == nil && (option == 0 && a.FatigueUsed == a.FatigueLimit ||
		option == 1 && !s.LayoutSent && run.OculusEntry == nil) {
		return storage.ErrPhase
	}
	if err := g.server.DB.ReturnDungeon(ctx, g.lease, c, true); err != nil {
		return err
	}
	g.ordinary.BeginExit(body)
	return outcome()
}
