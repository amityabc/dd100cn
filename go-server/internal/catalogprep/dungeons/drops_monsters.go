package dungeons

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
)

const monsterDropPolicy = "MonsterMob/inferred-independent-per10000;last-section-wins;rank-additive;quantity1;death-frozen-lifetime;resource-action-sources;native-item-gold-restriction-mask;native-named-marker-last-field/v5"

type monsterDropRow struct{ Item, Probability uint32 }
type monsterPools struct {
	Ordinary, Champion, SuperChampion, Boss []monsterDropRow
	Restrictions                            byte
	Named                                   *bool
}
type MonsterDropTables struct {
	Hash      string
	Monsters  map[uint32]monsterPools
	Templates map[uint32]*gm.ItemTemplate
}
type monsterTag struct{ open, section *regexp.Regexp }

var monsterPoolTags = func() []monsterTag {
	var out []monsterTag
	for _, tag := range []string{"item", "common champion drop item", "super champion drop item", "boss drop item"} {
		out = append(out, monsterTag{regexp.MustCompile(`(?m)^\[` + tag + `\][ \t]*\r?$`), regexp.MustCompile(`(?ms)^\[` + tag + `\][ \t]*\r?\n(.*?)^\[/` + tag + `\][ \t]*\r?$`)})
	}
	return out
}()
var restrictStart = regexp.MustCompile(`(?im)^[ \t]*\[drop restriction\][ \t]*`)
var restrictNext = regexp.MustCompile(`(?m)^[ \t]*\[`)
var restrictWord = regexp.MustCompile("\x60([^\x60]*)\x60")

func readMonsterPool(t string, tag monsterTag) ([]monsterDropRow, error) {
	out := []monsterDropRow{}
	opens := tag.open.FindAllStringIndex(t, -1)
	if len(opens) == 0 {
		return out, nil
	}
	sections := tag.section.FindAllStringSubmatch(t, -1)
	if len(opens) != len(sections) {
		return nil, fmt.Errorf("unclosed monster drop pool")
	}
	for _, s := range sections {
		words := strings.Fields(s[1])
		if len(words)%2 != 0 || len(words) > 8192 {
			return nil, fmt.Errorf("invalid monster drop pairs")
		}
		out = make([]monsterDropRow, 0, len(words)/2)
		for i := 0; i < len(words); i += 2 {
			id, e1 := strconv.ParseUint(words[i], 10, 32)
			p, e2 := strconv.ParseUint(words[i+1], 10, 32)
			if e1 != nil || e2 != nil || id == 0 || p > 10000 {
				return nil, fmt.Errorf("invalid monster-local item or probability")
			}
			out = append(out, monsterDropRow{uint32(id), uint32(p)})
		}
	}
	return out, nil
}

func BuildMonsterDrops(a Archive, items *commerce.Items, codes map[uint32]bool) (MonsterDropTables, error) {
	out := MonsterDropTables{Monsters: map[uint32]monsterPools{}, Templates: map[uint32]*gm.ItemTemplate{}}
	index, err := a.ReadIndex("monster")
	if err != nil {
		return out, err
	}
	all := map[uint32]monsterPools{}
	allIDs, projectedIDs := map[uint32]bool{}, map[uint32]bool{}
	for _, k := range slices.Sorted(maps.Keys(index)) {
		if k <= 0 || k > math.MaxUint32 {
			return out, fmt.Errorf("invalid monster %d", k)
		}
		id := uint32(k)
		text, err := a.ReadText(index[k], budget)
		if err != nil {
			return out, err
		}
		p := monsterPools{}
		for i, rows := range []*[]monsterDropRow{&p.Ordinary, &p.Champion, &p.SuperChampion, &p.Boss} {
			*rows, err = readMonsterPool(text, monsterPoolTags[i])
			if err != nil {
				return out, fmt.Errorf("monster %d: %w", id, err)
			}
			for _, r := range *rows {
				if r.Probability > 0 {
					allIDs[r.Item] = true
				}
				if codes == nil || codes[id] {
					projectedIDs[r.Item] = true
				}
			}
		}
		for _, start := range restrictStart.FindAllStringIndex(text, -1) {
			body := text[start[1]:]
			if next := restrictNext.FindStringIndex(body); next != nil {
				body = body[:next[0]]
			}
			if strings.TrimSpace(restrictWord.ReplaceAllString(body, "")) != "" {
				return out, fmt.Errorf("invalid drop restrictions in %d", id)
			}
			for _, m := range restrictWord.FindAllStringSubmatch(body, -1) {
				switch m[1] {
				case "gold":
					p.Restrictions |= 1
				case "exp":
					p.Restrictions |= 2
				case "item":
					p.Restrictions |= 4
				}
			}
		}
		for _, s := range pvftext.Blocks(text, "is named monster") {
			n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 32)
			if err != nil {
				return out, err
			}
			v := n != 0
			p.Named = &v
		}
		if len(p.Ordinary)+len(p.Champion)+len(p.SuperChampion)+len(p.Boss) > 0 || p.Restrictions != 0 || p.Named != nil {
			all[id] = p
			if codes == nil || codes[id] {
				out.Monsters[id] = p
			}
		}
	}
	templates := map[uint32]*gm.ItemTemplate{}
	for id := range allIDs {
		if t := monsterDropTemplate(items, id); t != nil {
			templates[id] = t
			if projectedIDs[id] {
				out.Templates[id] = t
			}
		}
	}
	out.Hash, err = catalogHash(monsterDropPolicy, "\n", orderedPairs(all), "\n", orderedPairs(templates))
	return out, err
}
