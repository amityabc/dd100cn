package towns

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
)

var npcTokens = regexp.MustCompile("`[^`]*`|\\[[^\\]\\r\\n]+\\]|[^\\s]+")

// NPCs reads unconditional, top-level five-token placements from a town map.
func NPCs(text string) []NPC {
	tokens := npcTokens.FindAllString(text, -1)
	closed := map[string]bool{}
	for _, v := range tokens {
		if tag, ok := tagValue(v); ok && strings.HasPrefix(tag, "/") {
			closed[tag[1:]] = true
		}
	}
	scope := []string{}
	out := []NPC{}
	invalid := false
	for i := 0; i < len(tokens); i++ {
		tag, ok := tagValue(tokens[i])
		if !ok {
			continue
		}
		if tag == "npc" {
			next := slices.IndexFunc(tokens[i+1:], func(s string) bool { return strings.EqualFold(s, "[/NPC]") })
			if next < 0 {
				return []NPC{}
			}
			end := i + 1 + next
			if len(scope) == 0 && !invalid && (end-i-1)%5 == 0 {
				rows := []NPC{}
				valid := true
				for at := i + 1; at < end; at += 5 {
					id, e := strconv.ParseUint(tokens[at], 10, 32)
					x, xe := strconv.ParseInt(tokens[at+2], 10, 16)
					y, ye := strconv.ParseInt(tokens[at+3], 10, 16)
					_, indexOK := integer(tokens[at+4])
					if e != nil || id == 0 || tokens[at+1] != "`[left]`" && tokens[at+1] != "`[right]`" || xe != nil || ye != nil || !indexOK {
						valid = false
						break
					}
					if x >= 0 && y >= 0 {
						rows = append(rows, NPC{uint32(id), uint16(x), uint16(y)})
					}
				}
				if valid {
					out = append(out, rows...)
				}
			}
			i = end
		} else if strings.HasPrefix(tag, "/") {
			if tag == "/npc" {
				return []NPC{}
			}
			if len(scope) == 0 || scope[len(scope)-1] != tag[1:] {
				invalid = true
			} else {
				scope = scope[:len(scope)-1]
			}
		} else if closed[tag] {
			scope = append(scope, tag)
		}
	}
	return out
}

type NPCWarp struct {
	Source, Target       Key
	SourceNpc, TargetNpc uint32
	SourceX, SourceY     uint16
	Destination          Point
	Entering             bool
}

func episode(a Archive, areas map[Key]Area, townMaps map[string]Map) ([]NPCWarp, error) {
	empty := []NPCWarp{}
	from, to := Key{22, 4}, Key{82, 0}
	if areas[to].Path == "" || areas[from].Path == "" {
		return empty, nil
	}
	idx, err := a.ReadIndex("npc", budget)
	if err != nil {
		return nil, err
	}
	texts := map[int64]string{}
	for _, id := range []int64{352, 100000338} {
		if idx[id] == "" {
			return empty, nil
		}
		t, err := a.ReadText(idx[id], budget)
		if errors.Is(err, fs.ErrNotExist) {
			return empty, nil
		}
		if err != nil {
			return nil, err
		}
		texts[id] = t
	}
	for id, other := range map[int64]int64{352: 100000338, 100000338: 352} {
		role, err := pvftext.Section(texts[id], "role")
		if err != nil || !slices.Equal(strings.Fields(role), []string{"`[move", "town]`", fmt.Sprint(other)}) {
			return empty, nil
		}
	}
	data, err := pvftext.Section(texts[352], "npc role data")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(data), "`[move town]`") {
		return empty, nil
	}
	remaining := strings.TrimPrefix(strings.TrimSpace(data), "`[move town]`")
	for _, condition := range []struct {
		tag      string
		required []int
	}{{"in progress quest", []int{6420}}, {"open quest", []int{6420, 6421, 6422}}, {"close quest", []int{6423}}} {
		values, err := pvftext.Numbers(remaining, condition.tag)
		if err != nil || !slices.Equal(values, condition.required) {
			return empty, nil
		}
		remaining = pattern(condition.tag).block.ReplaceAllString(remaining, "")
	}
	other, err := pvftext.Section(texts[100000338], "npc role data")
	if err != nil || other != "" || strings.TrimSpace(remaining) != "" {
		return empty, nil
	}
	placement := func(k Key, id uint32) (NPC, bool) {
		var result NPC
		for _, p := range townMaps[strings.ToLower(areas[k].Path)].Npcs {
			if p.Id == id {
				if result.Id != 0 {
					return NPC{}, false
				}
				result = p
			}
		}
		return result, result.Id != 0 && result.Y <= 32767-30
	}
	first, ok := placement(from, 352)
	second, valid := placement(to, 100000338)
	if !ok || !valid {
		return empty, nil
	}
	return []NPCWarp{
		{from, to, first.Id, second.Id, first.X, first.Y, Point{Town: to.Town, Area: to.Area, X: second.X, Y: second.Y + 30, Direction: 5}, true},
		{to, from, second.Id, first.Id, second.X, second.Y, Point{Town: from.Town, Area: from.Area, X: first.X, Y: first.Y + 30, Direction: 5}, false},
	}, nil
}
