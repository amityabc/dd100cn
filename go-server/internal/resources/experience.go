package resources

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/questwire"
)

// Apply the configured event to final, already rounded awards from the ordinary PVF tables.
func applyExperienceEvent(rate int, quests []questwire.Quest, tutorial *opening.CombatCatalog, dungeons *dungeon.Data) error {
	if rate < 1 || rate > 100 {
		return errors.New("invalid experience event multiplier")
	}
	if rate == 1 {
		return nil
	}
	scale := func(n uint32) uint32 {
		return uint32(min(uint64(n)*uint64(rate), math.MaxUint32))
	}
	for _, q := range quests {
		for _, award := range q.ExperienceByLevel {
			if award != nil {
				*award = scale(*award)
			}
		}
	}
	update := func(levels []uint32) {
		for i, n := range levels {
			levels[i] = scale(n)
		}
	}
	for _, job := range tutorial.Jobs {
		for _, rule := range job.Awards {
			update(rule.ByPlayerLevel)
		}
	}
	updateRules := func(rules []dungeon.ExperienceRules) {
		for _, rule := range rules {
			for _, m := range rule.Monsters {
				update(m.ByPlayerLevel)
			}
		}
	}
	for _, d := range dungeons.Definitions {
		updateRules(d.Awards)
	}
	for _, mode := range dungeons.Oculus {
		updateRules(mode.Awards)
	}
	for _, hash := range []*string{&tutorial.ExperienceHash, &dungeons.ExperienceHash} {
		if *hash != "" {
			source := fmt.Sprintf("LocalExperienceEvent/v1\nResourceHash:%s\nMultiplier:%d\n", *hash, rate)
			*hash = fmt.Sprintf("%X", sha256.Sum256([]byte(source)))
		}
	}
	return nil
}
