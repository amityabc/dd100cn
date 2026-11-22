package dungeon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/questwire"
)

type PassiveResult struct {
	Actor, ExperienceAward uint32
	QuestChanged           bool
	Drops                  []GroundDrop
	DropRuleHash           string
	QuestDrop              *questwire.QuestDropReceipt `json:",omitempty"`
}

type passiveReceipt struct {
	body, input string
	result      PassiveResult
}

type PassiveDeath struct {
	Code                    uint32
	Ordinal                 int
	Version, Key            string
	Replay, Wide, Duplicate bool
	Placements              int
	PriorInput              string
	PriorResult             PassiveResult
}

func (r *Run) PlanPassive(body []byte) (PassiveDeath, bool, bool) {
	if len(body) < 4 || !r.Ready || r.InLayer || r.PlayerDeath != nil {
		return PassiveDeath{}, false, false
	}
	code := le.Uint32(body)
	ordinal := slices.Index(r.Room.PassiveObjectCodes, code)
	if ordinal < 0 {
		return PassiveDeath{}, false, false
	}
	if code == 0 || len(body) != 74 || le.Uint16(body[4:]) != math.MaxUint16 || body[30] != 0 ||
		slices.ContainsFunc(body[6:30], func(b byte) bool { return b != 0 }) {
		return PassiveDeath{Code: code}, true, false
	}
	wideQuest := r.widePassiveQuest(code)
	if r.Settlement.Confirmation != nil && (!r.Cleared || code != 69268 || wideQuest == 0) {
		return PassiveDeath{Code: code}, true, false
	}
	if code >= math.MaxUint16 && wideQuest == 0 {
		return PassiveDeath{Code: code}, true, false
	}
	placements := 0
	for _, known := range r.Room.PassiveObjectCodes {
		if known == code {
			placements++
		}
	}
	key := fmt.Sprintf("dungeon-passive-death:%s:%d:%d:%d:%d", r.idKey(), r.Definition.Dungeon.MazeIndex, r.Room.X, r.Room.Y, code)
	base := key
	if placements > 1 {
		sum := sha256.Sum256(body)
		key += ":" + strings.ToUpper(hex.EncodeToString(sum[:]))
	}
	wide := code >= math.MaxUint16 || code == 80458 || code == 80456 || code == 69268 || code == 80454
	version := "TrustedLocalClientPassiveObjectDeath/v1"
	switch code {
	case 80458:
		version = "TrustedLocalClientQuest3205PassiveDeath/v2"
	case 80456:
		version = "TrustedLocalClientQuest3210PassiveDeath/v1"
	case 80454:
		version = "TrustedLocalClientQuest3446PassiveDeath/v1"
	}
	if previous, ok := r.passive[key]; ok {
		return PassiveDeath{Code: code, Ordinal: ordinal, Version: version, Key: key, Replay: true, Wide: wide, Duplicate: placements > 1,
			Placements: placements, PriorInput: previous.input, PriorResult: previous.result}, true, previous.body == string(body)
	}
	if r.OculusLayout != nil {
		for _, archive := range r.OculusLayout.previous {
			if _, alreadyCommitted := archive.passive[key]; alreadyCommitted {
				return PassiveDeath{Code: code}, true, false
			}
		}
	}
	if r.Cleared && !(code == 69268 && wideQuest != 0) {
		return PassiveDeath{Code: code}, true, false
	}
	consumed := 0
	for k := range r.passive {
		if k == base || strings.HasPrefix(k, base+":") {
			consumed++
		}
	}
	if consumed >= placements {
		return PassiveDeath{Code: code}, true, false
	}
	return PassiveDeath{Code: code, Ordinal: ordinal, Version: version, Key: key, Wide: wide, Duplicate: placements > 1, Placements: placements},
		true, true
}

func (r *Run) widePassiveQuest(code uint32) uint16 {
	def := r.Definition.Dungeon
	var quest uint16
	switch {
	case code == 80458 && def.ID == 25 && r.Room.MapID == 53493:
		quest = 3205
	case code == 80456 && def.ID == 27 && r.Room.MapID == 53508:
		quest = 3210
	case code == 69268 && def.ID == 152 && r.Room.MapID == 70092:
		quest = 3258
	case code == 109001851 && def.ID == 61 && r.Room.MapID == 76331:
		quest = 3425
	case code == 80454 && def.ID == 146 && r.Room.MapID == 53627:
		quest = 3446
	case code == 109000551 && def.ID == 516 && r.Room.MapID == 91734:
		quest = 3682
	case code == 109034854 && def.ID == 291100293:
		quest = 5947
	case code == 109022849 && def.ID == 291100308:
		quest = 5964
	}
	if slices.Contains(def.QuestConnections, quest) || quest == 3682 && slices.Contains(def.QuestConnections, uint16(3627)) {
		return quest
	}
	return 0
}

func (r *Run) ApplyPassive(death PassiveDeath, body []byte, input string, result PassiveResult) {
	if death.Replay {
		return
	}
	r.passive[death.Key] = passiveReceipt{body: string(body), input: input, result: result}
	r.applyDrops(result.Drops)
}

func (p *DropData) ObjectDrops(r *Run, d PassiveDeath, now time.Time) ([]GroundDrop, bool, error) {
	def := r.Definition.Dungeon
	if def.DisableDrops || d.Placements != 1 || d.Code < 402 || d.Code >= math.MaxUint16 {
		return nil, false, nil
	}
	const sourceKind byte = 2
	for _, s := range p.Secondary {
		if s.Kind != sourceKind || len(s.Probabilities) == 0 {
			continue
		}
		level := r.RewardLevel
		if level == 0 {
			level = def.MinimumLevel
		}
		basis := s.forLevel(level)
		gold, goldSupported := p.Gold[level]
		if len(basis) < 5 || !goldSupported {
			return nil, true, nil
		}
		actor := uint16(d.Code)
		death := Death{Actor: actor, Monster: opening.Monster{Code: d.Code, Level: level}, Key: d.Key}
		draw, err := newDraw(r, actor, death.Monster, s.Hash)
		if err != nil {
			return nil, true, err
		}
		var rewards []GroundReward
		if basis[0] != 0 {
			i := min(int(def.Difficulty), 4)
			chance, err := scaledFloor(uint64(basis[0]), 10000, dropBonus(s.DifficultyBonus, i),
				dropBonus(s.PartyBonus, 0), dropBonus(s.MonsterBonus, 0))
			if err != nil {
				return nil, true, err
			}
			if draw.next(10000) < chance {
				value := max(int64(1), gold.Amount+gold.Amount*(int64(draw.next(uint64(gold.Variance*2+1)))-gold.Variance)/100)
				rewards = append(rewards, GroundReward{Gold: uint32(value)})
			}
		}
		reward, _, err := p.rollItemChannels(r, death, basis, &s.dropRate, &draw, now, nil, p.secondaryPools, sourceKind, s.Hash, 1)
		if err != nil {
			return nil, true, err
		}
		drops, err := r.freezeDrops(actor, d.Key, append(rewards, reward...))
		return drops, true, err
	}
	return nil, false, nil
}
