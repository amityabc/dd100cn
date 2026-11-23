package host

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/savejson"
	"s9a1/server/internal/storage"
)

func (g *game) dungeonMonsterDeath(ctx context.Context, run *dungeon.Run, c storage.Character, a protocol.Actor, body []byte) error {
	return g.dungeonMonsterDeathAuthorized(ctx, run, c, a, body, false)
}

func (g *game) dungeonMonsterDeathAuthorized(ctx context.Context, run *dungeon.Run, c storage.Character, a protocol.Actor, body []byte, gm bool) error {
	if len(body) < 74 || len(body) != 74+14*int(body[30]) {
		return storage.ErrInvalid
	}
	target, owner := binary.LittleEndian.Uint32(body), binary.LittleEndian.Uint16(body[4:])
	if target == 0 && owner == math.MaxUint16 {
		return nil
	}
	if run.AbaddonFailed() {
		return g.send(1, 39, dungeon.DeathRejected(target))
	}
	profile, err := readGameplay(c.Gameplay)
	if err != nil {
		return err
	}
	if !profile.Initialized || profile.Progression.Hp == 0 {
		if run.Definition.Dungeon.ID == 100000197 {
			return g.send(1, 39, dungeon.DeathRejected(target))
		}
		return nil
	}
	if run.OculusEntry != nil && g.oculus.phase == oculusRunning && !time.Now().Before(g.oculus.ritualUntil) {
		return g.endOculus(ctx, c, true, false)
	}
	var budget storage.OculusClearCoins
	if run.OculusEntry != nil {
		budget, err = g.currentOculusCoins(ctx, run, c)
		if err != nil {
			if errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrInvalid) {
				return g.send(1, 39, dungeon.DeathRejected(target))
			}
			return err
		}
	}
	if !gm && g.server.delegated {
		if actor, ok, _ := run.GameMasterAPCDeath(body); ok {
			n, err := dungeon.DeathNotice(actor, nil)
			if err != nil {
				return err
			}
			return g.send(0, 38, n)
		}
	}
	if passive, known, valid := run.PlanPassive(body); known {
		if !valid {
			return g.send(1, 39, dungeon.DeathRejected(passive.Code))
		}
		return g.dungeonPassiveDeath(ctx, run, c, body, passive)
	}
	death, err := run.PlanDeath(body, gm)
	if err != nil || death.Ignored {
		return err
	}
	if death.Actor == 0 {
		return g.send(1, 39, dungeon.DeathRejected(death.Target))
	}
	if !g.server.delegated && !gm {
		return nil
	}
	if run.OculusEntry != nil && death.Prior == nil {
		mode, _ := g.oculusMode()
		for _, bonus := range mode.Rules.KillCoinLimitBonuses {
			if bonus.Id == death.Monster.Code && int(budget.Total)+bonus.RawValue > math.MaxUint8 {
				return g.send(1, 39, dungeon.DeathRejected(death.Target))
			}
		}
	}
	def := run.Definition.Dungeon
	room := run.CombatRoom()
	type scriptTerminal struct {
		QuestId            uint16
		QuestHash          string
		PrerequisiteActors []uint16
		Cinematic          *opening.CinematicDestroy
	}
	var script *scriptTerminal
	source, version := "ClientDelegated", "TrustedLocalClientCombat/v1"
	class, opcode := byte(1), uint16(39)
	if gm {
		source, version = "GameMaster", "AuthorizedLocalGmKill/v1"
		class, opcode = 0, 0
	} else if death.Companion != 0 {
		version = "TrustedLocalClientCompanionCombat/v1"
	}
	if death.Summon != nil {
		version = "TrustedLocalClientItemSummonCombat/v1"
	}
	if death.Storm != nil {
		version = death.Storm.Version()
	} else if death.Unattributed {
		version = "TrustedLocalClientUnattributedDeath/v1"
	} else if death.CleanupBoss != 0 {
		version = "TrustedLocalClientBossCleanup/v1"
	}
	if (death.Cinematic != nil || death.ScriptQuest != 0) && death.Prior == nil {
		var id uint16
		var hash string
		var err error
		if death.ScriptQuest != 0 {
			id, hash, err = g.catalog().Quests.ActiveDungeonScriptQuestProof(c, def.ID, def.QuestConnections, death.ScriptQuest)
		} else {
			id, hash, err = g.catalog().Quests.ActiveDungeonQuestProof(c, def.ID, def.QuestConnections)
		}
		if err != nil {
			return err
		}
		if id == 0 || death.ScriptQuest != 0 && id != death.ScriptQuest {
			return g.send(1, 39, dungeon.DeathRejected(death.Target))
		}
		if death.Waiting {
			if !run.FreezeCinematicDeath(death) {
				return g.send(1, 39, dungeon.DeathRejected(death.Target))
			}
			return nil
		}
		version = "TrustedLocalClientCinematicDestroy/v1"
		if death.ScriptQuest == 3213 {
			version = "TrustedLocalClientAganzoTerminal/v1"
		} else if death.Cinematic == nil {
			version = "TrustedLocalClientScriptTerminal/v1"
		}
		prerequisites := append([]uint16{}, death.Dependencies...)
		script = &scriptTerminal{id, hash, prerequisites, death.Cinematic}
	}
	if death.Waiting {
		if !run.FreezeCinematicDeath(death) {
			return g.send(1, 39, dungeon.DeathRejected(death.Target))
		}
		return nil
	}
	next := c
	var result dungeon.DeathResult
	var questItems dungeonQuestGrantPlan
	questChanged := false
	if death.Prior != nil {
		result = *death.Prior
		questItems.receipt = result.QuestDrop
	} else {
		var actual uint32
		if !death.Removal {
			next, actual, err = g.catalog().OpeningCombat.ApplyExperience(c, death.Award)
			if err != nil {
				return err
			}
		}
		result = run.Result(death, actual)
		drops := &g.catalog().Dungeons.Drops
		result.Drops = []dungeon.GroundDrop{}
		if !death.Removal {
			result.FirstHunt = g.catalog().Dungeons.FirstHunt(run, death)
			if result.FirstHunt != nil && result.FirstHunt.Kind != 0 {
				receipts, err := g.server.DB.Receipts(ctx, g.lease)
				if err != nil {
					return err
				}
				if err := g.catalog().Dungeons.ReadFirstHunt(result.FirstHunt, c, g.lease.Account, receipts); err != nil {
					return err
				}
			}
			if !def.DisableDrops && def.UsesOrdinaryMonsterDrops && (def.UsesOrdinaryRewards || def.Difficulty != 0) &&
				death.Monster.AiScriptIndex == nil && death.Monster.BlocksRoom && !death.Monster.Dummy && death.Monster.HellPhase == 0 && drops.MonsterAllowsItems(death.Monster.Code) {
				result.Luck, err = g.catalog().Dungeons.Luck.Start(c)
				if err != nil {
					return err
				}
			}
			result.Drops, err = drops.Plan(run, death, time.Now(), result.Luck, result.FirstHunt)
			if err != nil {
				return err
			}
			next, err = result.Luck.Apply(next)
			if err != nil {
				return err
			}
		}
		apc := drops.SupportsApcIndependent(run, death)
		if !death.Removal && !def.DisableDrops && def.UsesOrdinaryMonsterDrops && (def.UsesOrdinaryRewards || def.Difficulty != 0) &&
			death.Monster.HellPhase != 8 && (death.Monster.AiScriptIndex == nil || apc) {
			result.DropRuleHash = drops.Hash
		}
		if apc {
			result.ApcIndependentPolicy = &dungeon.ApcIndependentProof{Policy: "PVFApcIndependent/hostile-generated-solo;owner1;no-ordinary-random-channels/v1", SourceHash: result.DropRuleHash}
		}
		if !gm && drops.MonsterAllowsItems(death.Monster.Code) && binary.LittleEndian.Uint16(body[4:]) == c.ID {
			if err := g.catalog().Dungeons.WorldDrops.Plan(run, death, &result); err != nil {
				return err
			}
		}
		if run.OculusEntry != nil && !death.Removal && death.Monster.AiScriptIndex == nil && binary.LittleEndian.Uint16(body[4:]) == c.ID {
			mode, ok := g.oculusMode()
			if !ok {
				return storage.ErrConflict
			}
			if death.Monster.Code == mode.Rules.BossMonsterId {
				used, admission, err := g.server.DB.OculusBossAllowance(ctx, g.lease, c, *run.OculusEntry)
				if err != nil {
					return err
				}
				if err = g.catalog().Dungeons.PlanOculusBoss(run, mode, death, &result, used, admission, time.Now()); err != nil {
					return err
				}
			}
		}
		kind := byte(1)
		if death.Monster.AiScriptIndex != nil {
			kind = 2
		}
		if !death.Removal {
			questItems, err = g.dungeonQuestDrops(ctx, run, c, death.Key, death.Monster.Code, kind, false)
			if err != nil {
				return err
			}
			result.QuestDrop = questItems.receipt
			questChanged = questItems.receipt.HasGrants()
		}
		quests := g.catalog().Quests
		questTarget := !death.Removal || death.Cinematic != nil && death.Monster.Type == 3
		if !questTarget && death.Monster.Type == 3 {
			questTarget, err = quests.HasDungeonHuntTarget(c, def.ID, def.Difficulty, []opening.Monster{death.Monster})
			if err != nil {
				return err
			}
		}
		if questTarget {
			var changed bool
			next, changed, err = quests.AdvanceDungeon(next, []questwire.DungeonEvent{{Key: death.Key, Kind: questwire.DungeonMonsterKilled,
				Dungeon: def.ID, Map: room.MapID, Difficulty: def.Difficulty, Monster: death.Monster.Code, MonsterKind: kind}})
			if err != nil {
				return err
			}
			questChanged = questChanged || changed
		}
		if result.RoomCleared {
			pending, err := quests.HasDungeonHuntTarget(next, def.ID, def.Difficulty, room.Monsters)
			if err != nil {
				return err
			}
			if pending {
				result.RoomCleared, result.DungeonCleared, result.ClearElapsedMilliseconds = false, false, run.Elapsed
			}
		}
		if events := dungeonClearEvents(run, result); len(events) > 0 {
			var updated bool
			next, updated, err = quests.AdvanceDungeon(next, events)
			if err != nil {
				return err
			}
			questChanged = questChanged || updated
		}
	}
	m := death.Monster
	multiplier := def.ExperienceMultiplier
	if multiplier == "" {
		multiplier = "1"
	}
	input, err := json.Marshal(struct {
		Source, Version         string
		Account                 savejson.String
		OwnerGuid               string
		Epoch                   int64
		Character               uint16
		RunId                   string
		Dungeon                 uint32
		MazeIndex, RoomX, RoomY byte
		MapId                   uint32
		Maps                    []uint32
		Actor                   uint16
		Code                    uint32
		Level, Type             byte
		BlocksRoom              bool
		ResourceHash            string
		ExperienceMultiplier    json.Number
		RewardLevel             byte
		ExperienceHash          string
		ExperienceAward         uint32
		Class                   byte
		Opcode                  uint16
		Body                    savejson.String
		CleanupBossActor        uint16                    `json:",omitempty"`
		CompanionActor          uint16                    `json:",omitempty"`
		SummonedActor           *storage.PotionSummonKill `json:",omitempty"`
		AiScriptIndex           *byte                     `json:",omitempty"`
		Dummy                   bool                      `json:",omitempty"`
		ScriptTerminal          *scriptTerminal           `json:",omitempty"`
		StormCleanup            *dungeon.StormDeathProof  `json:",omitempty"`
	}{source, version, savejson.String(g.lease.Account), g.lease.Owner, g.lease.Epoch, c.ID, run.ID, def.ID,
		def.MazeIndex, room.X, room.Y, room.MapID, result.Maps, death.Actor, m.Code, m.Level, m.Type, m.BlocksRoom,
		def.ResourceHash, multiplier, run.RewardLevel, g.catalog().Dungeons.ExperienceHash, death.Award, class, opcode, savejson.String(base64.StdEncoding.EncodeToString(death.Body)),
		death.CleanupBoss, death.Companion, death.Summon, m.AiScriptIndex, m.Dummy, script, death.Storm})
	if err != nil {
		return err
	}
	frozen, err := json.Marshal(result)
	if err != nil {
		return err
	}
	write := storage.DungeonChange{Key: death.Key, Input: string(input), Result: string(frozen),
		Next: next, ExperienceAward: result.ExperienceAward, Replay: death.Prior != nil, Proof: run.DeathDependencyKeys(death.Dependencies), Summon: death.Summon}
	write.PetGrowth = run.PetGrowthProof(death, result, g.catalog().Quests.PetGrowthRule(), time.Now())
	write.BattlePassEligible = death.Prior == nil && !gm && !run.InLayer && !death.Removal && death.Companion == 0 && death.Summon == nil &&
		!death.Monster.Dummy && death.Monster.AiScriptIndex == nil && binary.LittleEndian.Uint16(body[4:]) == c.ID
	questItems.attach(&write)
	if def.ID == dungeon.WisdomDungeon {
		if run.WisdomEntry == nil {
			return storage.ErrConflict
		}
		write.Wisdom = run.WisdomEntry
	}
	if run.RubyEntry != nil {
		bindRubyDeath(run, result, &write, g.catalog().Dungeons.Ruby)
	}
	if run.OculusEntry != nil {
		write.Proof = append(write.Proof, run.OculusEntry.Key())
	}
	if p := result.OculusBossPolicy; p != nil {
		if death.Prior == nil {
			write.OculusBossMarker = p.Key()
		} else {
			write.Proof = append(write.Proof, p.Key())
		}
	}
	if run.AbaddonEntry != nil {
		write.Proof = append(write.Proof, run.AbaddonEntry.Key())
		write.Abaddon = &storage.AbaddonMutation{Entry: *run.AbaddonEntry, Now: time.Now()}
		if result.RoomCleared && death.Prior == nil {
			write.Abaddon.Room = &storage.AbaddonRoom{Run: run.ID, X: room.X, Y: room.Y, Death: death.Key, Terminal: result.DungeonCleared}
			write.Abaddon.Reward = run.AbaddonFloorReward()
		}
	}
	if key := run.StormEntryKey(); key != "" {
		write.Proof = append(write.Proof, key)
	}
	if result.Luck != nil {
		write.Luck = &storage.DungeonLuckChange{Before: result.Luck.Before, After: result.Luck.After}
	}
	if hunt := result.FirstHunt; hunt != nil {
		write.FirstHunt = &storage.DungeonFirstHunt{Code: hunt.Code, Kind: hunt.Kind, Difficulty: hunt.Difficulty,
			History: hunt.History, PreviousKey: hunt.PreviousKey, PreviousDigest: hunt.PreviousDigest}
	}
	if death.Prior != nil {
		write.Input = death.PriorInput
	}
	drops := result.Drops
	if death.Prior != nil {
		drops = nil
	}
	notice, err := dungeon.DeathNotice(death.Actor, drops)
	if err != nil {
		return err
	}
	if err = g.server.DB.CommitDungeonCombat(ctx, g.lease, c, write); err != nil {
		return err
	}
	if death.ClientAfterGM {
		return g.send(0, 38, notice)
	}
	run.ApplyDeath(death, result, write.Input)
	run.CommitSummonKill(death.Summon)
	run.ApplyAbaddonKill(death, result, time.Now())
	frames := []opening.Packet{{Opcode: 38, Body: notice}}
	oculus, err := g.oculusCommittedDeath(run, death, result)
	if err != nil {
		return err
	}
	if len(oculus) > 0 && oculus[0].Opcode == 1770 {
		frames = append(frames, oculus[0])
		oculus = oculus[1:]
	}
	if death.Prior == nil && (result.ExperienceAward > 0 || next.Level != c.Level) {
		actor, p, err := readActor(next, g.channel)
		if err != nil {
			return err
		}
		frames = append(frames, opening.Packet{Opcode: 37, Body: dungeon.ExperienceNotice(actor, p.Progression.Sp, p.Progression.Tp)})
	}
	if err = g.packets(frames); err != nil {
		return err
	}
	if questChanged {
		if err := g.dungeonQuestProjection(ctx, next); err != nil {
			return err
		}
	}
	if item := run.TournamentReward(); item != 0 && death.Prior == nil && result.DungeonCleared {
		if err := g.send(0, 374, dungeon.TournamentClearNotice(item)); err != nil {
			return err
		}
	}
	if err = g.packets(oculus); err != nil {
		return err
	}
	if death.Prior == nil && result.DungeonCleared {
		if run.RubyEntry != nil {
			if err = g.rubyClearProjection(ctx, run); err != nil {
				return err
			}
		} else if def.ID == dungeon.WisdomDungeon {
			if err = g.send(0, 31, []byte{0}); err != nil {
				return err
			}
		}
	}
	if questItems.receipt.HasGrants() {
		if err := g.dungeonQuestBag(ctx, next); err != nil {
			return err
		}
		if death.Prior != nil {
			if err := g.dungeonQuestProjection(ctx, next); err != nil {
				return err
			}
		}
	}
	if write.PetGrowth != nil && write.PetGrowth.Applied != nil {
		pet := write.PetGrowth.Applied
		if err = g.send(0, 102, binary.LittleEndian.AppendUint32([]byte{pet.Level, pet.Mode}, pet.Experience)); err != nil {
			return err
		}
	}
	if write.Abaddon != nil && write.Abaddon.Reward != nil {
		roster, err := g.server.DB.Current(ctx, g.lease)
		if err != nil {
			return err
		}
		current, err := selected(roster)
		if err != nil {
			return err
		}
		mail, err := g.server.DB.AbaddonFloorMail(ctx, g.lease, current, *run.AbaddonEntry, room.X, death.Key)
		if err != nil {
			return err
		}
		if err := g.publishAbaddonMail(current, mail); err != nil {
			return err
		}
	}
	if run.RubyEntry != nil || def.ID == dungeon.WisdomDungeon {
		return nil
	}
	if pending := run.PendingCinematicDeaths(); len(pending) > 0 {
		roster, err := g.server.DB.Current(ctx, g.lease)
		if err != nil {
			return err
		}
		current, err := selected(roster)
		if err != nil {
			return err
		}
		return g.dungeonMonsterDeath(ctx, run, current, a, pending[0])
	}
	if x, y, sequence, ok := run.PendingWisdomMove(); ok {
		return g.drainWisdomMove(ctx, run, x, y, sequence)
	}
	return g.dungeonTryConfirm(ctx, run)
}

func (g *game) dungeonPassiveDeath(ctx context.Context, run *dungeon.Run, c storage.Character, body []byte, death dungeon.PassiveDeath) error {
	if !g.server.delegated {
		return nil
	}
	def := run.Definition.Dungeon
	catalog := g.catalog().Quests
	next := c
	result := death.PriorResult
	var items dungeonQuestGrantPlan
	if death.Replay {
		items.receipt = result.QuestDrop
	} else {
		qualified, err := catalog.OwnsPassiveObjective(c, def.ID, def.Difficulty, death.Code, def.QuestConnections, death.Duplicate)
		if err != nil {
			return err
		}
		items, err = g.dungeonQuestDrops(ctx, run, c, death.Key, death.Code, 3, death.Duplicate)
		if err != nil {
			return err
		}
		drops := &g.catalog().Dungeons.Drops
		ground, object, err := drops.ObjectDrops(run, death, time.Now())
		if err != nil {
			return err
		}
		result = dungeon.PassiveResult{Actor: death.Code, Drops: ground, QuestDrop: items.receipt}
		if object {
			death.Version = "TrustedLocalClientPassiveObjectDropDeath/v2"
			result.DropRuleHash = drops.Hash
		}
		if result.Drops == nil {
			result.Drops = []dungeon.GroundDrop{}
		}
		var changed bool
		if qualified {
			next, changed, err = catalog.AdvanceDungeon(c, []questwire.DungeonEvent{{Key: death.Key, Kind: questwire.DungeonMonsterKilled,
				Dungeon: def.ID, Map: run.Room.MapID, Difficulty: def.Difficulty, Monster: death.Code, MonsterKind: 3}})
			if err != nil {
				return err
			}
		}
		result.QuestChanged = changed || items.receipt.HasGrants()
		if !result.QuestChanged && !object && items.receipt == nil {
			return g.send(1, 39, dungeon.DeathRejected(death.Code))
		}
	}
	input, err := json.Marshal(struct {
		Source, Version         string
		Account                 savejson.String
		OwnerGuid               string
		Epoch                   int64
		Character               uint16
		RunId                   string
		Dungeon                 uint32
		MazeIndex, RoomX, RoomY byte
		MapId                   uint32
		PassiveOrdinal          int
		Code                    uint32
		ResourceHash            string
		Class                   byte
		Opcode                  uint16
		Body                    savejson.String
	}{"ClientDelegated", death.Version, savejson.String(g.lease.Account), g.lease.Owner, g.lease.Epoch, c.ID, run.ID, def.ID,
		def.MazeIndex, run.Room.X, run.Room.Y, run.Room.MapID, death.Ordinal, death.Code, def.ResourceHash, 1, 39, savejson.String(base64.StdEncoding.EncodeToString(body))})
	if err != nil {
		return err
	}
	frozen, err := json.Marshal(result)
	if err != nil {
		return err
	}
	change := storage.DungeonChange{Key: death.Key, Input: string(input), Result: string(frozen), Next: next, Replay: death.Replay}
	if run.OculusEntry != nil {
		change.Proof = []string{run.OculusEntry.Key()}
	}
	if death.Replay {
		change.Input = death.PriorInput
	}
	items.attach(&change)
	if err := g.server.DB.CommitDungeonCombat(ctx, g.lease, c, change); err != nil {
		return err
	}
	run.ApplyPassive(death, body, change.Input, result)
	if !death.Wide {
		drops := result.Drops
		if death.Replay {
			drops = nil
		}
		packet, err := dungeon.DeathNotice(uint16(death.Code), drops)
		if err != nil {
			return err
		}
		if err := g.send(0, 38, packet); err != nil {
			return err
		}
	}
	if !death.Replay && result.QuestChanged || death.Replay && death.Wide {
		if err := g.dungeonQuestProjection(ctx, next); err != nil {
			return err
		}
	}
	if items.receipt.HasGrants() {
		if err := g.dungeonQuestBag(ctx, next); err != nil {
			return err
		}
		if death.Replay && !death.Wide {
			if err := g.dungeonQuestProjection(ctx, next); err != nil {
				return err
			}
		}
	}
	return g.dungeonTryConfirm(ctx, run)
}

func dungeonClearEvents(run *dungeon.Run, z dungeon.DeathResult) []questwire.DungeonEvent {
	def := run.Definition.Dungeon
	var events []questwire.DungeonEvent
	if z.RoomCleared {
		key := run.RoomKey()
		for _, m := range run.ClearQuestMaps(z) {
			events = append(events, questwire.DungeonEvent{Key: questwire.DungeonRoomEventKey(key, m), Kind: questwire.DungeonRoomCleared,
				Dungeon: def.ID, Map: m, Difficulty: def.Difficulty})
		}
	}
	if z.DungeonCleared {
		events = append(events, questwire.DungeonEvent{Key: run.ClearKey(), Kind: questwire.DungeonCleared,
			Dungeon: def.ID, Map: run.CombatRoom().MapID, Difficulty: def.Difficulty})
	}
	return events
}

func (g *game) dungeonEmptyClear(ctx context.Context, run *dungeon.Run, c storage.Character) (storage.Character, bool, error) {
	z, ok := run.EmptyClear()
	if !ok || !g.server.delegated {
		return c, false, nil
	}
	cat := g.catalog().Quests
	def := run.Definition.Dungeon
	target, err := cat.HasDungeonHuntTarget(c, def.ID, def.Difficulty, run.Room.Monsters)
	if err != nil || target {
		return c, false, err
	}
	next, changed, err := cat.AdvanceDungeon(c, dungeonClearEvents(run, z))
	if err != nil {
		return c, false, err
	}
	input, err := json.Marshal(struct {
		Source, Version, Account, OwnerGuid string
		Epoch                               int64
		Character                           uint16
		RunId                               string
		Id                                  uint32
		MazeIndex, X, Y                     byte
		MapId                               uint32
		Maps                                []uint32
		ResourceHash                        string
		Spawns                              []opening.Monster
	}{"ServerGeneratedEmptyRoom", "TrustedLocalClientCombat/v1", g.lease.Account, g.lease.Owner, g.lease.Epoch, c.ID, run.ID,
		def.ID, def.MazeIndex, run.Room.X, run.Room.Y, run.Room.MapID, z.Maps, def.ResourceHash, run.Room.Monsters})
	if err != nil {
		return c, false, err
	}
	write := storage.DungeonChange{Key: run.RoomKey(), Input: string(input), Result: "room-cleared:no-blocking-spawns", Next: next}
	if err := g.server.DB.CommitDungeonCombat(ctx, g.lease, c, write); err != nil {
		return c, false, err
	}
	run.ApplyClear(z)
	return next, changed, nil
}

func (g *game) dungeonQuestProjection(ctx context.Context, c storage.Character) error {
	in, err := g.server.DB.Inventory(ctx, g.lease)
	if err != nil {
		return err
	}
	receipts, err := g.server.DB.Receipts(ctx, g.lease)
	if err != nil {
		return err
	}
	frames, err := g.catalog().Quests.ProjectScene(c, in, receipts, true)
	if err != nil {
		return err
	}
	for _, f := range frames {
		if err := g.send(f.Class, f.Opcode, f.Body); err != nil {
			return err
		}
	}
	return nil
}

func (g *game) dungeonRevival(ctx context.Context, run *dungeon.Run, c storage.Character, f protocol.Frame) error {
	if !g.server.delegated || run.AbaddonFailed() {
		return nil
	}
	switch f.Opcode {
	case 40:
		if len(f.Body) != 6 {
			return storage.ErrInvalid
		}
		related := binary.LittleEndian.Uint16(f.Body[4:])
		if related != 0 && related != c.ID {
			return storage.ErrPhase
		}
		if run.OculusEntry != nil && g.oculus.phase == oculusCleared {
			_, err := g.currentOculusCoins(ctx, run, c)
			return err
		}
		if run.Settlement.Confirmation != nil {
			key := g.settlementKey(run, "dungeon-settlement")
			keys := []string{key + ":entry"}
			if run.Settlement.Result != nil {
				keys = append(keys, key+":result")
			}
			return g.server.DB.ValidateDungeonProof(ctx, g.lease, c, keys)
		}
		if run.Cleared && run.RubyEntry != nil {
			_, err := g.validateRubyClear(ctx, c, run)
			return err
		}
		if run.Cleared && run.WisdomEntry != nil {
			return g.checkWisdomClear(ctx, run, c, false, false)
		}
		next, replay := run.PlanPlayerDeath(f.Body)
		if replay {
			input := run.CurrentPlayerDeathInput()
			_, err := g.server.DB.DungeonPlayerDeath(ctx, g.lease, c, run.PlayerDeathReceiptKey(run.DeathCycle), input, false)
			return err
		}
		if !next {
			return storage.ErrPhase
		}
		key := run.PlayerDeathKey(false)
		input, err := run.PlayerDeathInput(f.Body, run.DeathCycle+1)
		if err != nil {
			return err
		}
		at := time.Now()
		if _, err := g.server.DB.DungeonPlayerDeath(ctx, g.lease, c, key, input, false); err != nil {
			return err
		}
		run.CommitPlayerDeath(f.Body, input, at)
		return g.send(0, 32, binary.LittleEndian.AppendUint32(nil, uint32(c.ID)))
	case 41:
		if len(f.Body) != 2 {
			return storage.ErrInvalid
		}
		reject := func() error { return g.send(1, 41, []byte{0, 0, 0}) }
		if run.RubyEntry != nil {
			return reject()
		}
		target := binary.LittleEndian.Uint16(f.Body)
		if target != c.ID || !run.Ready || run.PlayerDeath == nil || run.Settlement.Confirmation != nil {
			return reject()
		}
		if run.AbaddonEntry != nil {
			if !run.DeathDue() {
				return reject()
			}
			return g.reviveAbaddon(ctx, run, true)
		}
		var budget storage.OculusClearCoins
		if run.OculusEntry != nil {
			if g.oculus.phase != oculusRunning {
				return reject()
			}
			var err error
			budget, err = g.currentOculusCoins(ctx, run, c)
			if err != nil {
				if errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrInvalid) {
					return reject()
				}
				return err
			}
			if !time.Now().Before(g.oculus.ritualUntil) {
				return g.endOculus(ctx, c, true, false)
			}
			if !run.DeathDue() && budget.Used >= budget.Total {
				return reject()
			}
		}
		if run.DeathDue() {
			return g.expiredDungeonRevival(ctx, run, c)
		}
		key := run.PlayerDeathKey(true)
		coins, err := g.server.DB.DungeonPlayerDeath(ctx, g.lease, c, key, run.CurrentPlayerDeathInput(), true)
		if errors.Is(err, storage.ErrFunds) || errors.Is(err, storage.ErrConflict) {
			return reject()
		}
		if err != nil {
			return err
		}
		run.Revive()
		ack := append(binary.LittleEndian.AppendUint16([]byte{1}, c.ID), 0)
		state := append(binary.LittleEndian.AppendUint16(nil, c.ID), 1, 0)
		packet := make([]byte, 122)
		packet[1], packet[3], packet[5] = 1, 1, 1
		binary.LittleEndian.PutUint32(packet[9:], min(coins, math.MaxUint32))
		frames := []opening.Packet{{Class: 1, Opcode: 41, Body: ack}, {Opcode: 32, Body: state}, {Opcode: 14, Body: packet}}
		if run.OculusEntry != nil {
			g.oculus.used = budget.Used + 1
			frames = append(frames, opening.Packet{Opcode: 1771, Body: g.oculus.state(2, 0, 0)})
		}
		return g.packets(frames)
	}
	return storage.ErrPhase
}

func (g *game) expiredDungeonRevival(ctx context.Context, run *dungeon.Run, c storage.Character) error {
	if run.OculusEntry != nil {
		return g.parkOculusTransition(ctx, run, c, nil, true)
	}
	var err error
	if run.WisdomEntry != nil {
		err = g.server.DB.ReturnWisdomDungeon(ctx, g.lease, c, *run.WisdomEntry)
	} else {
		err = g.server.DB.ReturnDungeon(ctx, g.lease, c, true)
	}
	if err != nil {
		return err
	}
	a, _, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	a.AreaState = 0
	g.ordinary.BeginReturn()
	g.rubyView = nil
	return g.packets([]opening.Packet{{Class: 1, Opcode: 41, Body: []byte{0, 0, 0}},
		{Opcode: 23, Body: protocol.Area(a)}, {Opcode: 24, Body: protocol.Town(a)}})
}
