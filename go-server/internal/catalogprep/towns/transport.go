package towns

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
)

type Destination struct {
	Region, Phase int
	Point         Point
	Level         byte
	Quests        []uint16
	MoveChannel   int
}
type DockPhase struct {
	Index int
	Quest uint16
	Count int
}
type Menus struct {
	Dock, Board []Destination
	Phases      []DockPhase
	OpenQuest   uint16
}

func mapMenus(text string) (airship, dock bool) {
	if b, ok := single(text, "teleport area option"); ok {
		rows := tokensRE.FindAllString(b, -1)
		if len(rows) == 10 && rows[0] == "`dock`" && rows[5] == "`airship`" && rows[3] == "0" && rows[4] == "0" && rows[8] == "0" && rows[9] == "0" {
			airship = true
			for _, i := range []int{1, 2, 6, 7} {
				v, ok := integer(rows[i])
				if !ok || !coordinate(v) {
					airship = false
					break
				}
			}
		}
		if len(rows) > 0 && len(rows) <= 320 && len(rows)%5 == 0 {
			seen := map[string]bool{}
			valid := true
			for i := 0; i < len(rows); i += 5 {
				name := strings.ToLower(rows[i])
				if len(name) < 3 || name[0] != '`' || name[len(name)-1] != '`' || seen[name] {
					valid = false
					break
				}
				seen[name] = true
				for _, s := range rows[i+1 : i+5] {
					n, ok := integer(s)
					if !ok || !coordinate(n) {
						valid = false
						break
					}
				}
			}
			dock = valid && seen["`dock`"]
		}
	}
	if dock {
		return
	}
	container, ok := single(text, "popup window areas")
	if !ok {
		return
	}
	windows, ok := pattern("window area").blocks(container)
	if !ok || len(windows) > 128 {
		return
	}
	for _, w := range windows {
		f := fields(w)
		if len(f) != 2 || !slices.Equal(f["popup window"], []string{"`dock`"}) || len(pattern("area").open.FindAllStringIndex(w, -1)) != 1 || len(pattern("popup window").open.FindAllStringIndex(w, -1)) != 1 {
			continue
		}
		row, ok := numbers(f["area"])
		if ok && len(row) == 4 && row[2] >= 0 && row[3] >= 0 && !slices.ContainsFunc(row, func(n int) bool { return n < -32768 || n > 32767 }) {
			dock = true
			return
		}
	}
	return
}

func transport(a Archive) (Menus, error) {
	out := Menus{Dock: []Destination{}, Board: []Destination{}, Phases: []DockPhase{}}
	for _, v := range []struct {
		path string
		dock bool
	}{{"etc/dock/docksysteminfo.etc", true}, {"etc/airshipportalinfo.etc", false}} {
		text, err := a.ReadText(v.path, budget)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, err
		}
		routes, phases, open, err := transportRows(text, v.dock)
		// Each invalid native menu stays closed independently of the other one.
		if err != nil {
			continue
		}
		if v.dock {
			out.Dock, out.Phases, out.OpenQuest = routes, phases, open
		} else {
			out.Board = routes
		}
	}
	return out, nil
}
func numericField(text, tag string, minimum, maximum, limit int) ([]int, error) {
	if len(pattern(tag).open.FindAllStringIndex(text, -1)) != 1 {
		return nil, fmt.Errorf("missing or duplicate %s", tag)
	}
	v, ok := numbers(fields(text)[tag])
	if !ok || len(v) < minimum || len(v) > maximum || slices.ContainsFunc(v, func(n int) bool { return n < 0 || n > limit }) {
		return nil, fmt.Errorf("invalid %s", tag)
	}
	return v, nil
}

var variantIndex = regexp.MustCompile("(?s)^\\s*(\\d+)\\s*\\[")

func transportRows(text string, dock bool) ([]Destination, []DockPhase, uint16, error) {
	invalid := func(why string) ([]Destination, []DockPhase, uint16, error) {
		return nil, nil, 0, fmt.Errorf("transport menu: %s", why)
	}
	root, ok := single(text, "dock system")
	if !ok {
		return invalid("dock system boundary")
	}
	system, ok := single(root, "portal system")
	if !ok {
		return invalid("portal system boundary")
	}
	regions, ok := pattern("portal region").blocks(system)
	if !ok || len(regions) < 1 || len(regions) > 128 {
		return invalid("region boundaries")
	}
	phases := []DockPhase{}
	var open uint16
	known := map[int]bool{}
	if dock {
		at := strings.Index(strings.ToLower(root), "[portal system]")
		header := root[:at]
		quests, err := numericField(header, "open questindex condition", 1, 1, 65535)
		if err != nil || quests[0] == 0 {
			return invalid("opening quest")
		}
		open = uint16(quests[0])
		rows, err := numericField(header, "phase shift", 0, 192, 65535)
		if err != nil || len(rows)%3 != 0 {
			return invalid("phase table")
		}
		for i := 0; i < len(rows); i += 3 {
			index, quest, count := rows[i], rows[i+1], rows[i+2]
			if index < 1 || index > 255 || quest == 0 || count < 1 || count > 128 || known[index] {
				return invalid("duplicate or invalid phase")
			}
			known[index] = true
			phases = append(phases, DockPhase{index, uint16(quest), count})
		}
		slices.SortFunc(phases, func(a, b DockPhase) int { return cmp.Compare(a.Index, b.Index) })
	}
	routes := []Destination{}
	regionIDs := map[int]bool{}
	distinct := map[string]bool{}
	for _, body := range regions {
		p := pattern("phase shift dock info")
		variants, ok := p.blocks(body)
		if !ok || len(variants) > 64 || !dock && len(variants) > 0 {
			return invalid("phase overrides")
		}
		base := p.block.ReplaceAllString(body, "")
		tag := "slot index"
		if dock {
			tag = "region index"
		}
		id, err := numericField(base, tag, 1, 1, 127)
		if err != nil || dock && (id[0] < 1 || regionIDs[id[0]]) {
			return invalid("region id")
		}
		regionIDs[id[0]] = true
		add := func(body string, phase int) bool {
			row, err := destination(body, id[0], phase)
			if err != nil {
				return false
			}
			key := fmt.Sprint(row)
			if distinct[key] {
				return false
			}
			distinct[key] = true
			routes = append(routes, row)
			return len(routes) <= 256
		}
		if !add(base, 0) {
			return invalid("destination")
		}
		selected := map[int]bool{}
		for _, v := range variants {
			m := variantIndex.FindStringSubmatch(v)
			if m == nil {
				return invalid("override has no phase")
			}
			phase, ok := integer(m[1])
			if !ok || selected[phase] || !known[phase] || !add(v, phase) {
				return invalid("invalid phase override")
			}
			selected[phase] = true
		}
	}
	return routes, phases, open, nil
}

var lineTags = regexp.MustCompile("(?m)^[ \\t\\r]*\\[([^/\\]\\r\\n]+)\\]")

func destination(body string, region, phase int) (Destination, error) {
	row := Destination{Region: region, Phase: phase, MoveChannel: -1}
	p, err := numericField(body, "portal area", 4, 4, 32767)
	if err != nil || p[0] > 255 || p[1] > 255 || p[0] == 38 && p[1] == 1 {
		return row, fmt.Errorf("invalid portal area")
	}
	row.Point = Point{Town: uint16(p[0]), Area: uint16(p[1]), X: uint16(p[2]), Y: uint16(p[3]), Direction: 5}
	level, err := numericField(body, "open level condition", 1, 1, 255)
	if err != nil {
		return row, err
	}
	row.Level = byte(level[0])
	quests, err := numericField(body, "open questindex condition", 1, 64, 65535)
	if err != nil {
		return row, err
	}
	for _, n := range quests {
		if n == 0 || slices.Contains(row.Quests, uint16(n)) {
			return row, fmt.Errorf("invalid portal quest")
		}
		row.Quests = append(row.Quests, uint16(n))
	}
	if pattern("move channel").open.MatchString(body) {
		v, err := numericField(body, "move channel", 1, 1, 255)
		if err != nil {
			return row, err
		}
		row.MoveChannel = v[0]
	}
	for _, m := range lineTags.FindAllStringSubmatch(body, -1) {
		t := strings.ToLower(m[1])
		if strings.Contains(t, "condition") && t != "open level condition" && t != "open questindex condition" {
			return row, fmt.Errorf("unsupported portal condition %s", t)
		}
	}
	return row, nil
}
