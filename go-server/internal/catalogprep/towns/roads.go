package towns

import (
	"maps"
	"reflect"
	"slices"
	"strings"
)

func tagValue(token string) (string, bool) {
	if len(token) > 2 && token[0] == '[' && token[len(token)-1] == ']' {
		return strings.ToLower(token[1 : len(token)-1]), true
	}
	return "", false
}

func fields(text string) map[string][]string {
	result := map[string][]string{}
	current := ""
	for _, v := range tokensRE.FindAllString(text, -1) {
		if tag, ok := tagValue(v); ok {
			if strings.HasPrefix(tag, "/") {
				current = ""
			} else {
				current = tag
				if _, known := result[tag]; !known {
					result[tag] = []string{}
				}
			}
		} else if current != "" {
			result[current] = append(result[current], v)
		}
	}
	return result
}

func boundary(text string, route bool) bool {
	current := ""
	scalar := func(s string) bool { return !route && (s == "need level" || s == "legend area show gate rate") }
	for _, v := range tokensRE.FindAllString(text, -1) {
		tag, ok := tagValue(v)
		if !ok {
			if current == "" {
				return false
			}
			continue
		}
		if strings.HasPrefix(tag, "/") {
			if current != tag[1:] {
				return false
			}
			current = ""
		} else {
			if current != "" && !scalar(current) {
				return false
			}
			current = tag
		}
	}
	return current == "" || scalar(current)
}
func permission(text string, route, memory, offQuest bool) Permission {
	p := Permission{All: []uint16{}, Any: []uint16{}, Except: []uint16{}, Started: []uint16{}, Blocked: !boundary(text, route)}
	for key, values := range fields(text) {
		var dest *[]uint16
		switch {
		case key == "need level" && !route:
			if len(values) != 1 {
				p.Blocked = true
				continue
			}
			n, ok := integer(values[0])
			if !ok || n < 0 || n > 255 {
				p.Blocked = true
			} else {
				p.Level = byte(n)
			}
			continue
		case key == "legend area show gate rate" && !route:
			continue
		case !route && key == "need quest" || route && key == "quest condition":
			dest = &p.All
		case !route && key == "need quest or" || route && key == "quest condition or":
			dest = &p.Any
		case !route && (key == "except quest" || key == "off quest" && (memory || offQuest)) || route && key == "close quest condition":
			dest = &p.Except
		case !route && key == "in progress quest" && memory:
			dest = &p.Started
		default:
			p.Blocked = true
			continue
		}
		for _, v := range values {
			if key == "need quest" && v == "-1" {
				continue
			}
			n, ok := integer(v)
			if !ok || n <= 0 || n > 65535 {
				p.Blocked = true
			} else {
				*dest = append(*dest, uint16(n))
			}
		}
	}
	if memory && (p.Level != 95 || !slices.Equal(p.Started, []uint16{6420}) || !slices.Equal(p.Except, []uint16{6423}) || len(p.All)+len(p.Any) != 0) {
		p.Blocked = true
	}
	for _, list := range []*[]uint16{&p.All, &p.Any, &p.Except} {
		slices.Sort(*list)
		*list = slices.Compact(*list)
	}
	return p
}
func numbers(tokens []string) ([]int, bool) {
	out := make([]int, len(tokens))
	for i, v := range tokens {
		n, ok := integer(v)
		if !ok {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

type roads struct {
	routes    map[Key]Permission
	returns   map[Key]Point
	conflicts map[Key]bool
}

func (r roads) add(town, area int, p Permission) (Key, bool) {
	if town < 0 || town > 255 || area < 0 || area > 255 {
		return Key{}, false
	}
	k := Key{uint16(town), uint16(area)}
	if r.conflicts[k] {
		return k, false
	}
	if prev, dup := r.routes[k]; dup && !reflect.DeepEqual(prev, p) {
		r.close(k)
		return k, false
	}
	r.routes[k] = p
	return k, true
}
func (r roads) close(k Key) { delete(r.routes, k); delete(r.returns, k); r.conflicts[k] = true }
func (r roads) room(body string) {
	rows, ok := numbers(tokensRE.FindAllString(body, -1))
	n := len(rows)
	if !ok || n < 12 || rows[n-2] != -1 || rows[n-1] != -1 || (n-6)%6 != 0 {
		return
	}
	for at := 4; at < n-2; at += 6 {
		row := rows[at : at+6]
		valid := rows[2] >= 0 && rows[3] >= 0 && row[4] == 0 && row[5] == 0 && coordinate(row[2]) && coordinate(row[3])
		p := permission("", false, false, false)
		p.Blocked = !valid
		k, added := r.add(row[0], row[1], p)
		if !valid || !added {
			continue
		}
		point := Point{Town: k.Town, Area: k.Area, X: uint16(row[2]), Y: uint16(row[3]), Direction: 5}
		if before, dup := r.returns[k]; dup && before != point {
			r.close(k)
		} else {
			r.returns[k] = point
		}
	}
}
func (r roads) ordinary(body string) {
	tokens := tokensRE.FindAllString(body, -1)
	for at := 0; at+6 <= len(tokens); {
		row, ok := numbers(tokens[at : at+6])
		if !ok {
			return
		}
		at += 6
		start := at
		for at < len(tokens) && strings.HasPrefix(tokens[at], "[") {
			tag := strings.ToLower(tokens[at])
			if strings.HasPrefix(tag, "[/") {
				return
			}
			close := "[/" + tag[1:]
			at++
			for at < len(tokens) && !strings.EqualFold(tokens[at], close) {
				at++
			}
			if at == len(tokens) {
				return
			}
			at++
		}
		if row[2] >= 0 && row[3] >= 0 {
			r.add(row[4], row[5], permission(strings.Join(tokens[start:at], " "), true, false, false))
		}
	}
}
func (r roads) animated(body string) {
	all, _ := pattern("movable area").blocks(body)
	for _, movable := range all {
		f := fields(movable)
		target, ok := numbers(f["move town"])
		rect, valid := numbers(f["area"])
		if !ok || !valid || len(target) != 2 || len(rect) != 4 || rect[2] < 0 || rect[3] < 0 {
			continue
		}
		var cond strings.Builder
		for tag, values := range f {
			switch tag {
			case "move town", "area", "basic animation", "open animation", "loop animation", "close animation", "animation change area", "draw layer", "is seria room warp":
				continue
			}
			cond.WriteString("[" + tag + "] " + strings.Join(values, " ") + " [/" + tag + "] ")
		}
		r.add(target[0], target[1], permission(cond.String(), true, false, false))
	}
}
func readMap(path, text string, room bool) (Map, map[Key]Point) {
	r := roads{map[Key]Permission{}, map[Key]Point{}, map[Key]bool{}}
	if b, ok := single(text, "town movable area"); ok {
		if room {
			r.room(b)
		} else {
			r.ordinary(b)
		}
	}
	animation, _ := pattern("town movable area with animation").blocks(text)
	for _, b := range animation {
		r.animated(b)
	}
	out := Map{Path: path, Npcs: NPCs(text), Routes: []Route{}}
	out.AirshipRoomMenu, out.DockMenu = mapMenus(text)
	for _, k := range slices.SortedFunc(maps.Keys(r.routes), keyOrder) {
		out.Routes = append(out.Routes, Route{k, r.routes[k]})
	}
	return out, r.returns
}
