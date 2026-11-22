package dungeon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
)

type deathRecord struct {
	Body      []byte
	Key       string
	Result    DeathResult
	Removal   bool
	Cinematic bool
	Input     string
}

type Death struct {
	Actor          uint16
	Target         uint32
	Monster        opening.Monster
	Body           []byte
	Key            string
	Award          uint32
	Prior          *DeathResult
	PriorInput     string
	ClientAfterGM  bool
	Ignored        bool
	Removal        bool
	Cinematic      *opening.CinematicDestroy
	WisdomTerminal bool
	ScriptQuest    uint16
	Storm          *StormDeathProof
	Unattributed   bool
	CleanupBoss    uint16
	Companion      uint16
	Dependencies   []uint16
	Waiting        bool
	Summon         *storage.PotionSummonKill
}

type DeathResult struct {
	Actor                       uint16
	ExperienceAward             uint32
	RoomCleared, DungeonCleared bool
	Maps                        []uint32
	ClearElapsedMilliseconds    uint32
	Totals                      [4]uint32
	Drops                       []GroundDrop
	DropRuleHash                string
	Luck                        *LuckRoll                   `json:",omitempty"`
	FirstHunt                   *FirstHunt                  `json:",omitempty"`
	QuestDrop                   *questwire.QuestDropReceipt `json:",omitempty"`
	OculusBossPolicy            *OculusBossProof            `json:",omitempty"`
	MaterialPolicy              *WorldDropProof             `json:",omitempty"`
	ApcIndependentPolicy        *ApcIndependentProof        `json:",omitempty"`
}

type ApcIndependentProof struct{ Policy, SourceHash string }

func (r *Run) EmptyClear() (DeathResult, bool) {
	k := cell{r.Room.X, r.Room.Y}
	if r.Ready || r.InLayer || r.PlayerDeath != nil || r.clearedRooms[k] ||
		slices.ContainsFunc(r.Room.Monsters, func(m opening.Monster) bool { return m.BlocksRoom }) {
		return DeathResult{}, false
	}
	def := r.Definition.Dungeon
	z := DeathResult{RoomCleared: true, DungeonCleared: k == (cell{def.BossX, def.BossY}), Maps: roomMaps(r.Room), Totals: r.Totals}
	if z.DungeonCleared {
		z.ClearElapsedMilliseconds = uint32(min(int64(math.MaxUint32), max(int64(0), time.Since(r.Started).Milliseconds())))
	}
	return z, true
}

func (r *Run) ApplyClear(z DeathResult) {
	if z.RoomCleared {
		r.clearedRooms[cell{r.Room.X, r.Room.Y}] = true
	}
	if z.DungeonCleared {
		r.Cleared, r.Elapsed = true, z.ClearElapsedMilliseconds
	}
}

func (r *Run) idKey() string { return strings.ReplaceAll(r.ID, "-", "") }

func (r *Run) PlanDeath(body []byte, gameMaster bool) (Death, error) {
	if len(body) < 74 || len(body) != 74+14*int(body[30]) {
		return Death{}, errors.New("invalid native C39 death report")
	}
	target, owner := le.Uint32(body), le.Uint16(body[4:])
	out := Death{Target: target}
	if target == 0 && owner == math.MaxUint16 {
		out.Ignored = true
		return out, nil
	}
	if !r.Ready || r.Room == nil {
		return out, nil
	}
	shared := sharedLayerQuest(r.Definition, r.Room)
	layer := r.InLayer && r.seen[scene{cell{r.Room.X, r.Room.Y}, true}]
	if !r.Ready || r.InLayer && !r.wisdomStoryLayerLoaded() && (!layer || shared == 0) || r.PlayerDeath != nil {
		out.Ignored = !gameMaster && !r.AbaddonFailed() && r.Definition.Dungeon.ID != 100000197
		return out, nil
	}
	if r.AbaddonFailed() || r.Settlement.Confirmation != nil || target < 402 || target >= math.MaxUint16 {
		return out, nil
	}
	room := r.CombatRoom()
	base := r.combatActorBase()
	index := int(target) - int(base)
	if index < 0 || index >= len(room.Monsters) {
		if r.Definition.Dungeon.ID >= 291100420 && r.Definition.Dungeon.ID <= 291100424 &&
			index >= len(room.Monsters) && index < len(room.Monsters)+len(room.Companions) &&
			owner == math.MaxUint16 && len(body) == 74 && body[68] == 7 {
			out.Ignored = true
		}
		return out, nil
	}
	if r.InLayer && index != 0 {
		return out, nil
	}
	m := room.Monsters[index]
	actor := uint16(target)
	prior, duplicate := r.Dead[actor]
	if r.Cleared && !duplicate {
		return out, nil
	}
	if r.Definition.Dungeon.ID == WisdomDungeon && m.HellPhase > 1 &&
		!(m.HellPhase == 8 && owner == math.MaxUint16 && body[30] == 0 && (m.Type < 3 || m.Dummy)) {
		for i, earlier := range room.Monsters {
			if earlier.HellPhase > 0 && earlier.HellPhase < m.HellPhase {
				if _, committed := r.Dead[base+uint16(i)]; !committed {
					return out, nil
				}
			}
		}
	}
	var storm *StormDeathProof
	var cinematic *opening.CinematicDestroy
	var unattributed bool
	var cleanup uint16
	var dependencies []uint16
	var scriptQuest uint16
	wisdomTerminal := room != r.Room
	if wisdomTerminal {
		dependency, ok := r.wisdomTerminal(body, owner, index)
		if !ok {
			return out, nil
		}
		dependencies = []uint16{dependency}
		scriptQuest = WisdomStoryQuest
	} else if shared == NoireQuest && index == 0 {
		if !layer {
			return out, nil
		}
		cinematic = r.cinematicRule(index, body)
		if cinematic == nil {
			return out, nil
		}
		scriptQuest = NoireQuest
	} else if quest, required, target := r.boundTerminal(room, index, m, body, layer); target {
		if quest == 0 {
			return out, nil
		}
		scriptQuest, dependencies = quest, required
	} else {
		var err error
		storm, err = r.stormProof(index, body)
		if err != nil {
			return out, nil
		}
		cinematic = r.cinematicRule(index, body)
		if storm == nil && cinematic == nil {
			unattributed, cleanup, dependencies = r.uncredited(index, body)
		}
	}
	removal := storm != nil || cinematic != nil || scriptQuest != 0 || unattributed || cleanup != 0
	var original struct {
		Source, Version string
		Class           byte
		Opcode          uint16
		SummonedActor   *storage.PotionSummonKill
	}
	if duplicate {
		if err := json.Unmarshal([]byte(prior.Input), &original); err != nil {
			return out, err
		}
		priorGM := original.Source == "GameMaster" && original.Version == "AuthorizedLocalGmKill/v1" && original.Class == 0 && original.Opcode == 0
		out.ClientAfterGM = priorGM && !gameMaster && !removal
		if !out.ClientAfterGM && (priorGM != gameMaster || !bytes.Equal(prior.Body, body)) {
			return out, nil
		}
	}
	if ruleIndex := slices.IndexFunc(room.CinematicDestroys, func(rule opening.CinematicDestroy) bool { return int(rule.Index) == index }); ruleIndex >= 0 && m.Dummy && cinematic == nil {
		return out, nil
	}
	if !removal && owner != r.Character {
		out.Companion = r.reportedCompanion(body)
		if out.Companion == 0 {
			if owner == math.MaxUint16 && !gameMaster {
				if duplicate {
					out.Summon = original.SummonedActor
				} else {
					out.Summon = r.nativeSummonKill(body, time.Now())
				}
			}
			if out.Summon == nil && !assistedKill(body, m, r.Character) {
				return out, nil
			}
		}
	}
	if r.TournamentReward() != 0 && (!duplicate && (index != len(r.Dead) || removal || owner != r.Character)) {
		return out, nil
	}
	if !removal && m.Code == 0 {
		return out, nil
	}
	out.Actor, out.Monster, out.Body = actor, m, slices.Clone(body)
	out.Key = fmt.Sprintf("dungeon-death:%s:%d:%d:%d:%d", r.idKey(), r.Definition.Dungeon.MazeIndex, r.Room.X, r.Room.Y, actor)
	if duplicate {
		out.Prior = &prior.Result
		out.PriorInput = prior.Input
	}
	out.Removal, out.Cinematic = removal, cinematic
	out.WisdomTerminal = wisdomTerminal
	out.ScriptQuest = scriptQuest
	out.Storm = storm
	out.Unattributed, out.CleanupBoss, out.Dependencies = unattributed, cleanup, dependencies
	if storm != nil {
		out.Dependencies = storm.Prerequisites
	}
	if cinematic != nil && cinematic.TriggerIndex != nil {
		index := int(*cinematic.TriggerIndex)
		if index >= len(r.Room.Monsters) || r.Room.Monsters[index].Type != 3 || r.Room.Monsters[index].Dummy {
			return Death{Target: target}, nil
		}
		boss := r.actorBase() + uint16(index)
		out.Dependencies = []uint16{boss}
	}
	for _, dependency := range out.Dependencies {
		if _, committed := r.Dead[dependency]; !committed {
			out.Waiting = true
			break
		}
	}
	if removal || r.Definition.Dungeon.DisableExperience || m.AiScriptIndex != nil || m.HellPhase == 8 {
		return out, nil
	}
	typ := m.Type
	if r.named(m) {
		typ = 2
	}
	for _, difficulty := range r.Definition.Awards {
		if difficulty.Difficulty != r.Definition.Dungeon.Difficulty {
			continue
		}
		for _, reward := range difficulty.Monsters {
			if reward.Level == m.Level && reward.Type == typ && r.RewardLevel > 0 && int(r.RewardLevel) <= len(reward.ByPlayerLevel) {
				out.Award = reward.ByPlayerLevel[r.RewardLevel-1]
				return out, nil
			}
		}
	}
	return out, fmt.Errorf("no experience resource for level %d native type %d", m.Level, typ)
}

func (r *Run) cinematicRule(index int, body []byte) *opening.CinematicDestroy {
	if len(body) != 74 || le.Uint16(body[4:]) != math.MaxUint16 || body[68] != 7 {
		return nil
	}
	for _, rule := range r.Room.CinematicDestroys {
		if int(rule.Index) == index && rule.Movie != 0 && isHash(rule.Hash) {
			return &rule
		}
	}
	return nil
}

func (r *Run) FreezeCinematicDeath(d Death) bool {
	if pending, ok := r.cinematicDeaths[d.Actor]; ok {
		return bytes.Equal(pending, d.Body)
	}
	r.cinematicDeaths[d.Actor] = d.Body
	return true
}

func (r *Run) PendingCinematicDeaths() [][]byte {
	var batches [][]byte
	for _, report := range r.cinematicDeaths {
		death, err := r.PlanDeath(report, false)
		if err == nil && death.Actor != 0 && !death.Waiting {
			batches = append(batches, report)
		}
	}
	slices.SortFunc(batches, func(a, b []byte) int { return int(le.Uint32(a)) - int(le.Uint32(b)) })
	return batches
}

func (r *Run) DeathDependencyKeys(actors []uint16) []string {
	keys := make([]string, 0, len(actors))
	for _, actor := range actors {
		if d, ok := r.Dead[actor]; ok {
			keys = append(keys, d.Key)
		}
	}
	return keys
}

func assistedKill(b []byte, m opening.Monster, player uint16) bool {
	count := int(b[30])
	if le.Uint16(b[4:]) != math.MaxUint16 || m.Dummy || m.Type >= 3 || count < 1 || count > 8 || b[68+14*count] != 0 {
		return false
	}
	for i := 0; i < count; i++ {
		row := b[31+i*14:]
		kind, actor, damage := le.Uint16(row), le.Uint16(row[2:]), le.Uint64(row[4:])
		if i == 0 {
			if kind != 0x0111 || actor != player || damage == 0 || le.Uint16(row[12:]) == 0 {
				return false
			}
		} else if kind != 0x0111 && kind != 0xf111 && kind != 0x0211 || actor == 0 || damage == 0 {
			return false
		}
	}
	return true
}

func (r *Run) Result(d Death, actual uint32) DeathResult {
	room := r.CombatRoom()
	result := DeathResult{Actor: d.Actor, ExperienceAward: actual, Maps: roomMaps(room), Totals: r.Totals}
	k := cell{r.Room.X, r.Room.Y}
	blocked, boss, hasBoss, fakeBoss, allDead := false, false, false, false, true
	base := r.combatActorBase()
	for i, m := range room.Monsters {
		id := base + uint16(i)
		_, dead := r.Dead[id]
		dead = dead || id == d.Actor
		blocked = blocked || m.BlocksRoom && !dead
		boss = boss || m.Type == 3 && dead
		hasBoss = hasBoss || m.Type == 3
		fakeBoss = fakeBoss || m.Code == 75100
		allDead = allDead && dead
	}
	result.RoomCleared = !blocked && (!r.clearedRooms[k] || d.WisdomTerminal && !r.Cleared)
	def := r.Definition.Dungeon
	result.DungeonCleared = result.RoomCleared && k == (cell{def.BossX, def.BossY}) &&
		(boss || r.TournamentReward() != 0 && allDead || r.placeholderBossRoom() ||
			!hasBoss && (allDead && (fakeBoss || r.forgottenForestRoom()) || d.Cinematic != nil || r.markerRoom()))
	result.ClearElapsedMilliseconds = r.Elapsed
	if result.DungeonCleared {
		result.ClearElapsedMilliseconds = uint32(min(int64(math.MaxUint32), max(int64(0), time.Since(r.Started).Milliseconds())))
	}
	result.Totals[d.Monster.Type] = uint32(min(uint64(math.MaxUint32), uint64(result.Totals[d.Monster.Type])+uint64(actual)))
	return result
}

func roomMaps(room *opening.Room) []uint32 {
	maps := append(slices.Clone(room.MapAliases), room.MapID)
	slices.Sort(maps)
	return slices.Compact(maps)
}

func (r *Run) ClearQuestMaps(result DeathResult) []uint32 {
	if r.Room.CinematicLayer != nil && !r.wisdomStoryRoom() && !slices.Contains(result.Maps, r.Room.CinematicLayer.MapID) {
		return append(append([]uint32(nil), result.Maps...), r.Room.CinematicLayer.MapID)
	}
	return result.Maps
}

func (r *Run) RoomKey() string {
	return fmt.Sprintf("dungeon-room-clear:%s:%d:%d:%d", r.idKey(), r.Definition.Dungeon.MazeIndex, r.Room.X, r.Room.Y)
}

func (r *Run) ClearKey() string { return "dungeon-clear:" + r.idKey() }

func (r *Run) ApplyDeath(d Death, z DeathResult, input string) {
	if d.Prior != nil {
		return
	}
	r.Dead[d.Actor] = deathRecord{d.Body, d.Key, z, d.Removal, d.Cinematic != nil || d.ScriptQuest != 0, input}
	delete(r.cinematicDeaths, d.Actor)
	r.Totals = z.Totals
	r.applyDrops(z.Drops)
	r.ApplyClear(z)
}

func (r *Run) applyDrops(drops []GroundDrop) {
	for _, drop := range drops {
		r.ground[groundKey{cell{drop.RoomX, drop.RoomY}, drop.ObjectKey}] = drop
	}
	r.nextGroundSlot += uint16(len(drops))
}

func DeathRejected(target uint32) []byte {
	return le.AppendUint16([]byte{0}, uint16(min(target, math.MaxUint16)))
}

func DeathNotice(actor uint16, drops []GroundDrop) ([]byte, error) {
	body := le.AppendUint16(le.AppendUint16(nil, actor), uint16(len(drops)))
	for _, d := range drops {
		row, err := d.deathWire()
		if err != nil {
			return nil, err
		}
		body = append(body, row...)
	}
	return append(body, 0, 0, 255, 0), nil
}

func ExperienceNotice(a protocol.Actor, sp, tp uint16) []byte { return loaded(a, sp, tp)[1].Body }

func (r *Run) PlanPlayerDeath(body []byte) (bool, bool) {
	x, y, related := le.Uint16(body), le.Uint16(body[2:]), le.Uint16(body[4:])
	if !r.Ready || related != 0 && related != r.Character {
		return false, false
	}
	if old := r.PlayerDeath; old != nil {
		return false, old.x == x && old.y == y && old.related == related
	}
	return r.DeathCycle != math.MaxUint32, false
}

func (r *Run) ApplyPlayerDeath(body []byte) {
	r.CommitPlayerDeath(body, "", time.Now())
}

func (r *Run) ReviveAllowed(actor uint16) bool {
	return r.Ready && actor == r.Character && r.PlayerDeath != nil && time.Since(r.PlayerDeath.at) < 10*time.Second
}

func (r *Run) DeathDue() bool {
	t := r.DeathDeadline()
	return !t.IsZero() && !time.Now().Before(t)
}

func (r *Run) DeathDeadline() time.Time {
	if r == nil || r.PlayerDeath == nil || r.AbaddonFailed() {
		return time.Time{}
	}
	if r.AbaddonEntry != nil {
		return r.PlayerDeath.at.Add(3500 * time.Millisecond)
	}
	return r.PlayerDeath.at.Add(10 * time.Second)
}

func (r *Run) Revive() { r.PlayerDeath, r.LastRevived = nil, true }

func (r *Run) PlayerDeathKey(revive bool) string {
	if revive {
		return fmt.Sprintf("revive:%s:%d", r.idKey(), r.DeathCycle)
	}
	return r.PlayerDeathReceiptKey(r.DeathCycle + 1)
}

func (r *Run) PlayerDeathReceiptKey(cycle uint32) string {
	return fmt.Sprintf("death:%s:%d", r.idKey(), cycle)
}
