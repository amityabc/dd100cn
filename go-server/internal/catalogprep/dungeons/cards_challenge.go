package dungeons

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
)

type challengeChoice struct {
	Count, Weight uint32
	Id            uint32
}
type challengeCards struct {
	Dungeon      uint32
	Stage        uint16
	Hash         string
	Chance, Cost uint32
	Choices      []challengeChoice
	Templates    map[uint32]*gm.ItemTemplate
	Definitions  map[byte]string
}

var challengeBooster = regexp.MustCompile("(?ms)^\\[etc\\].*?^\\[/etc\\]")

func loadCardChallenges(a Archive, items *commerce.Items, definitions []Definition) ([]challengeCards, error) {
	special, err := a.ReadText("etc/dungeonspecialreward.etc", budget)
	if err != nil {
		return nil, err
	}
	r := &cardInput{}
	rows := r.numbers(r.section(special, "dungeon premium card reward challenge"), 0, math.MaxUint32)
	if !r.width("premium card reward challenge", len(rows), 5, 1, 409) {
		return nil, r.err
	}
	byDungeon := make(map[uint32][]Definition)
	for _, d := range definitions {
		if slices.Contains(challengeFarming, d.ID) {
			byDungeon[d.ID] = append(byDungeon[d.ID], d)
		}
	}
	out := []challengeCards{}
	for _, id := range slices.Sorted(maps.Keys(byDungeon)) {
		layouts := byDungeon[id]
		if len(layouts) > 16 {
			return nil, fmt.Errorf("challenge %d has too many layouts", id)
		}
		stages := make(map[uint16]bool)
		hashes := make(map[byte]string)
		var hashesOrdered []string
		for _, d := range layouts {
			if d.meta.disableClear || !d.UsesOrdinaryRewards || hashes[d.MazeIndex] != "" {
				return nil, fmt.Errorf("challenge %d lacks qualified ordinary layout", id)
			}
			hashes[d.MazeIndex] = d.ResourceHash
			hashesOrdered = append(hashesOrdered, d.ResourceHash)
			stagesList := d.ChallengeStages
			if len(stagesList) == 0 {
				stagesList = []uint16{d.ChallengeStage}
			}
			for _, v := range stagesList {
				if v != 0 {
					stages[v] = true
				}
			}
		}
		for _, stage := range slices.Sorted(maps.Keys(stages)) {
			var row []uint32
			for candidate := range slices.Chunk(rows, 5) {
				if candidate[0] == id && candidate[1] == uint32(stage) {
					if row != nil {
						return nil, fmt.Errorf("ambiguous premium %d stage %d", id, stage)
					}
					row = candidate
				}
			}
			if len(row) != 5 || row[2] == 0 || row[2] == math.MaxUint32 || row[3] == 0 || row[3] > 10000 || row[4] == 0 {
				return nil, fmt.Errorf("invalid challenge %d stage %d reward", id, stage)
			}
			source, err := items.Source(row[2])
			if err != nil {
				return nil, err
			}
			if source.Equipment || r.section(source.Text, "stackable type") != "\x60[booster]\x60 0" {
				return nil, fmt.Errorf("challenge booster is not an ordinary stackable")
			}
			body := r.section(source.Text, "booster info")
			groups := pvftext.Blocks(body, "etc")
			if len(groups) != 1 || strings.TrimSpace(challengeBooster.ReplaceAllString(body, "")) != "" {
				return nil, fmt.Errorf("challenge booster requires a single unconditional group")
			}
			values := r.numbers(groups[0], 1, math.MaxUint32)
			if len(values) < 4 || len(values) > 769 || (len(values)-1)%3 != 0 || values[0] != 1 {
				return nil, fmt.Errorf("challenge booster requires a single bounded draw")
			}
			c := challengeCards{Dungeon: id, Stage: stage, Chance: row[3], Cost: row[4], Templates: make(map[uint32]*gm.ItemTemplate), Definitions: hashes}
			var total uint64
			for item := range slices.Chunk(values[1:], 3) {
				itemID, weight, count := item[0], item[1], item[2]
				if itemID <= 2 || itemID == math.MaxUint32 || weight == 0 || count == 0 || count > 1_000_000 || total+uint64(weight) > math.MaxUint32 {
					return nil, fmt.Errorf("challenge booster choice is unsupported")
				}
				total += uint64(weight)
				c.Choices = append(c.Choices, challengeChoice{count, weight, itemID})
				if c.Templates[itemID] == nil {
					src, err := items.Source(itemID)
					if err != nil {
						return nil, err
					}
					t, err := src.Gift()
					if err != nil {
						return nil, err
					}
					if t.TemplateId != itemID || (t.Kind != 1 && t.Kind != 2) || t.AccountCommonInventory || t.FateCollection || hasDropLifetime(t) ||
						t.Kind != 2 && count != 1 || t.StackLimit < count {
						return nil, fmt.Errorf("challenge premium reward %d has unsupported acquisition", itemID)
					}
					c.Templates[itemID] = t
				}
			}
			templateHashes := []string{}
			for _, p := range orderedPairs(c.Templates) {
				templateHashes = append(templateHashes, fmt.Sprintf("%d|%s", p.Key, p.Value.ResourceHash))
			}
			identity := fmt.Sprintf("LocalFarmingChallengeResourcePremium/v1/%d/%d\n%s\n%s\n%d\n%d\n%d\n%s\n%s",
				id, stage, special, source.Text, id, stage, row[2], strings.Join(templateHashes, "\n"), strings.Join(hashesOrdered, "\n"))
			c.Hash = hashText(identity)
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no qualified challenge card results")
	}
	return out, r.err
}
