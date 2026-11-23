package commerce

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/storage"
)

var (
	dynamicNPC       = regexp.MustCompile("(?im)^\\s*\\[(?:upgrade|set invisible on proc|default npc visible|visible trigger quest|hide trigger quest)\\]")
	plainTargets     = regexp.MustCompile("^\\s*(?:[0-9]+\\s*)*$")
	npcTargets       = regexp.MustCompile("[0-9]+")
	visibilityFields = regexp.MustCompile("(?im)^\\s*\\[([^/\\]\\r\\n]+)\\]")
	requirementRows  = regexp.MustCompile("(?ims)^[ \t]*\\[pre required quest\\][ \t]*\\r?\\n(.*?)^[ \t]*\\[/pre required quest\\][ \t]*\\r?$")
	requirementOpen  = regexp.MustCompile("(?im)^[ \t]*\\[pre required quest\\]")
	requirementClose = regexp.MustCompile("(?im)^[ \t]*\\[/pre required quest\\]")
)

func buildVisibility(a gm.ItemArchive, npcs map[uint32]string) (storage.CommerceVisibility, error) {
	visibility := storage.CommerceVisibility{Hidden: []uint32{}, Blocked: []uint32{}, Rules: map[uint32][]storage.CommerceVisibilityRule{}}
	hidden, blocked := map[uint32]bool{}, map[uint32]bool{}
	sequenceQuests := map[int]bool{}
	if text, err := a.ReadText("etc/hiddenNpc.etc", scriptLimit); err == nil {
		body, err := commerceBlock(text, "hidden npc")
		if err != nil {
			return visibility, err
		}
		ids, err := pvftext.Integers(body)
		if err != nil {
			return visibility, err
		}
		for _, id := range ids {
			if id <= 0 {
				return visibility, fmt.Errorf("hidden NPC identity is invalid")
			}
			hidden[uint32(id)] = true
		}
		body, err = commerceBlock(text, "npc visibility quest sequence")
		if err != nil {
			return visibility, err
		}
		n, err := pvftext.Integers(body)
		if err != nil || len(n)%3 != 0 {
			return visibility, fmt.Errorf("NPC visibility sequence is malformed")
		}
		for at := 0; at < len(n); at += 3 {
			sequenceQuests[n[at]], sequenceQuests[n[at+1]] = true, true
		}
	}
	for id, text := range npcs {
		if dynamicNPC.MatchString(text) {
			blocked[id] = true
		}
	}
	quests, err := a.ReadIndex("quest")
	if err != nil {
		return visibility, err
	}
	ids := make([]int64, 0, len(quests))
	for id := range quests {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	required := map[uint16][][]uint16{}
	for _, id := range ids {
		text, err := a.ReadText(quests[id], scriptLimit)
		if err != nil {
			return visibility, err
		}
		if id > 0 && id <= math.MaxUint16 {
			required[uint16(id)] = questRequirements(text)
		}
		legacy, err := commerceSection(text, "visible npc")
		if err != nil {
			return visibility, err
		}
		if v, err := strconv.ParseUint(legacy, 10, 32); err == nil && npcs[uint32(v)] != "" {
			blocked[uint32(v)] = true
		}
		for _, body := range pvftext.Blocks(text, "npc visibility") {
			targetText, err := pvftext.Section(body, "npc")
			if err != nil {
				return visibility, err
			}
			targets := map[uint32]bool{}
			for _, num := range npcTargets.FindAllString(targetText, -1) {
				if n, err := strconv.ParseUint(num, 10, 32); err == nil && npcs[uint32(n)] != "" {
					targets[uint32(n)] = true
				}
			}
			if len(targets) == 0 {
				continue
			}
			condition, err := pvftext.Section(body, "condition")
			if err != nil {
				return visibility, err
			}
			show, err := pvftext.Section(body, "visibility")
			if err != nil {
				return visibility, err
			}
			supported := plainTargets.MatchString(targetText) && id > 0 && id <= math.MaxUint16 && !sequenceQuests[int(id)] &&
				(condition == "`[accept]`" || condition == "`[clear]`") && (show == "`[show]`" || show == "`[hide]`" || show == "`[delete]`")
			for _, f := range visibilityFields.FindAllStringSubmatch(body, -1) {
				if f[1] != "npc" && f[1] != "condition" && f[1] != "visibility" {
					supported = false
				}
			}
			for npc := range targets {
				if !supported {
					blocked[npc] = true
					continue
				}
				r := storage.CommerceVisibilityRule{Quest: uint16(id), Clear: condition == "`[clear]`", Show: show == "`[show]`", Earlier: []uint16{}}
				visibility.Rules[npc] = append(visibility.Rules[npc], r)
			}
		}
	}
	for id := range hidden {
		if npcs[id] != "" && len(visibility.Rules[id]) == 0 {
			blocked[id] = true
		}
	}
	order := questOrder{required: required, proofs: map[[2]uint16]bool{}}
	for id, rules := range visibility.Rules {
		if len(rules) > 256 {
			blocked[id] = true
			continue
		}
		for j, later := range rules {
			if !later.Show || !later.Clear {
				continue
			}
			first := map[uint16]bool{}
			for _, earlier := range rules {
				if earlier.Show || earlier.Quest == later.Quest {
					continue
				}
				if order.after(later.Quest, earlier.Quest) && !order.after(earlier.Quest, later.Quest) {
					first[earlier.Quest] = true
				}
			}
			for q := range first {
				rules[j].Earlier = append(rules[j].Earlier, q)
			}
			slices.Sort(rules[j].Earlier)
		}
	}
	visibility.Hidden, visibility.Blocked = sortedIDs(hidden), sortedIDs(blocked)
	return visibility, nil
}

func questRequirements(text string) [][]uint16 {
	matches := requirementRows.FindAllStringSubmatch(strings.ReplaceAll(text, "\r", ""), -1)
	if len(matches) > 64 || len(requirementOpen.FindAllStringIndex(text, -1)) != len(matches) ||
		len(requirementClose.FindAllStringIndex(text, -1)) != len(matches) {
		return nil
	}
	out := make([][]uint16, 0, len(matches))
	for _, m := range matches {
		ids, err := pvftext.Integers(m[1])
		if err != nil || len(ids) < 1 || len(ids) > 64 {
			return nil
		}
		p := make([]uint16, 0, len(ids))
		for _, id := range ids {
			if id <= 0 || id > math.MaxUint16 {
				return nil
			}
			p = append(p, uint16(id))
		}
		out = append(out, p)
	}
	return out
}

type questOrder struct {
	required map[uint16][][]uint16
	proofs   map[[2]uint16]bool
}

func (q *questOrder) after(later, earlier uint16) bool {
	if later == earlier {
		return false
	}
	key := [2]uint16{later, earlier}
	if known, ok := q.proofs[key]; ok {
		return known
	}
	if len(q.proofs) >= 65536 {
		return false
	}
	budget, valid := 8192, true
	visiting := map[uint16]bool{}
	var visit func(uint16, int) bool
	visit = func(id uint16, depth int) bool {
		if id == earlier {
			return true
		}
		budget--
		if depth >= 128 || budget < 0 || visiting[id] {
			valid = false
			return false
		}
		visiting[id] = true
		defer delete(visiting, id)
		groups := q.required[id]
		if groups == nil {
			valid = false
			return false
		}
		if len(groups) == 0 {
			return false
		}
		all := true
		for _, group := range groups {
			inside := false
			for _, r := range group {
				found := visit(r, depth+1)
				inside = inside || found
			}
			all = all && inside
		}
		return all
	}
	answer := visit(later, 0) && valid
	q.proofs[key] = answer
	return answer
}
