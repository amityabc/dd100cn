// Package towns prepares the native town map, permission and travel catalogs.
package towns

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"s9a1/server/internal/resources"
)

const budget = 1 << 20

type Archive interface {
	ReadText(string, int) (string, error)
	ReadIndex(string, ...int) (map[int64]string, error)
}
type Key struct{ Town, Area uint16 }
type Point = resources.TownPoint
type Permission struct {
	Level                     byte
	All, Any, Except, Started []uint16
	Blocked                   bool
}
type Area struct {
	Key
	Path        string
	Permission  Permission
	Phases      []string
	DungeonGate *uint16 `json:"-"`
}
type Rule struct {
	Kind      string
	Id, Index int
}
type NPC struct {
	Id   uint32
	X, Y uint16
}
type Route struct {
	Key
	Permission Permission
}
type Map struct {
	Path                      string
	Npcs                      []NPC
	AirshipRoomMenu, DockMenu bool
	Routes                    []Route
}
type roomReturn struct {
	Key
	Point Point
}
type Catalog struct {
	Areas         []Area
	CommerceAreas []Area `json:"-"`
	Maps          []Map
	Phases        map[uint16][]Rule
	RoomReturns   []roomReturn
	Warps         []Warp
	NpcWarps      []NPCWarp
	Menus         Menus
	Room, Exit    Point
	Ranges        any
}

var home = Key{38, 1}
var outside = Key{38, 0}
var areaHeader = regexp.MustCompile("(?i)^\\s*([0-9]+)\\s+" + "`" + "([^`]+\\.map)" + "`")
var quotedMap = regexp.MustCompile("(?i)`([^`]+\\.map)`")
var gate = regexp.MustCompile("(?i)`\\[gate\\]`\\s*(?:(-?\\d+)\\s+(-?\\d+))?")
var dungeonGate = regexp.MustCompile("(?i)`\\[dungeon gate\\]`\\s*(-?\\d+)")
var condition = regexp.MustCompile("(?im)^\\s*\\[condition\\]\\s*`\\[([^`\\]]+)\\]`\\s*(-?\\d+)")
var phaseIndex = regexp.MustCompile(`(?im)^\s*\[phase index\]\s*(-?\d+)`)
var tokensRE = regexp.MustCompile("`[^`]*`|\\[[^\\]\\r\\n]+\\]|-?\\d+|[^\\s]+")

type tagPattern struct{ open, close, block *regexp.Regexp }

var tags sync.Map

func pattern(tag string) tagPattern {
	if p, ok := tags.Load(tag); ok {
		return p.(tagPattern)
	}
	t := regexp.QuoteMeta(tag)
	o, c := `^[ \t\r]*\[`+t+`\]`, `^[ \t\r]*\[/`+t+`\]`
	p := tagPattern{regexp.MustCompile("(?im)" + o), regexp.MustCompile("(?im)" + c), regexp.MustCompile("(?ims)" + o + `\s*(.*?)` + c)}
	tags.Store(tag, p)
	return p
}
func (p tagPattern) blocks(text string) ([]string, bool) {
	m := p.block.FindAllStringSubmatch(text, -1)
	r := make([]string, len(m))
	for i, row := range m {
		r[i] = row[1]
	}
	return r, len(p.open.FindAllStringIndex(text, -1)) == len(m) && len(p.close.FindAllStringIndex(text, -1)) == len(m)
}
func single(text, tag string) (string, bool) {
	blocks, ok := pattern(tag).blocks(text)
	if len(blocks) != 1 || !ok {
		return "", false
	}
	return blocks[0], true
}
func integer(s string) (int, bool) { n, e := strconv.ParseInt(s, 10, 32); return int(n), e == nil }
func coordinate(n int) bool        { return n >= 0 && n <= 32767 }
func keyOrder(a, b Key) int {
	if a.Town != b.Town {
		return cmp.Compare(a.Town, b.Town)
	}
	return cmp.Compare(a.Area, b.Area)
}
func qualifiedPath(p, prefix, suffix string) (string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(strings.ToLower(p), prefix+"/") {
		p = prefix + "/" + p
	}
	if !strings.HasSuffix(strings.ToLower(p), suffix) || strings.Contains(p, ":") || strings.IndexFunc(p, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("invalid town resource %q", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == ".." || part == "." {
			return "", fmt.Errorf("invalid town resource %q", p)
		}
	}
	return p, nil
}

func phaseRules(text string) []Rule {
	type id struct {
		n    int
		kind string
	}
	rows := map[id]Rule{}
	blocks, closed := pattern("phase shift").blocks(text)
	unknown := func() { rows[id{-1, "unknown"}] = Rule{"unknown", -1, -1} }
	if !closed {
		unknown()
	}
	for _, body := range blocks {
		c, i := condition.FindStringSubmatch(body), phaseIndex.FindStringSubmatch(body)
		if c == nil || i == nil {
			unknown()
			continue
		}
		n, ok := integer(c[2])
		index, valid := integer(i[1])
		kind := strings.ToLower(c[1])
		if !ok || !valid || index < -1 || index > 255 || n < 0 {
			kind = "unknown"
		}
		rows[id{n, kind}] = Rule{kind, n, index}
	}
	out := slices.SortedFunc(maps.Values(rows), func(a, b Rule) int {
		if a.Id != b.Id {
			return cmp.Compare(a.Id, b.Id)
		}
		return cmp.Compare(a.Kind, b.Kind)
	})
	return append([]Rule{}, out...)
}

func Build(a Archive) (*Catalog, error) {
	index, err := a.ReadIndex("town", budget)
	if err != nil {
		return nil, err
	}
	if len(index) > 256 {
		return nil, fmt.Errorf("too many town definitions")
	}
	result := &Catalog{Areas: []Area{}, Maps: []Map{}, Phases: map[uint16][]Rule{}, RoomReturns: []roomReturn{}, Ranges: []any{}}
	declared := map[Key]Area{}
	paths := map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(index)) {
		if id < 0 || id > 255 {
			return nil, fmt.Errorf("town %d outside native range", id)
		}
		path, err := qualifiedPath(index[id], "town", ".twn")
		if err != nil {
			return nil, err
		}
		txt, err := a.ReadText(path, budget)
		if err != nil {
			return nil, err
		}
		town := uint16(id)
		result.Phases[town] = phaseRules(txt)
		blocks, _ := pattern("area").blocks(txt)
		if len(blocks) > 256 {
			return nil, fmt.Errorf("too many areas in town %d", id)
		}
		for _, body := range blocks {
			header := areaHeader.FindStringSubmatch(body)
			if header == nil {
				return nil, fmt.Errorf("invalid area header in %s", path)
			}
			area, valid := integer(header[1])
			if !valid || area > 255 {
				return nil, fmt.Errorf("invalid area in %s", path)
			}
			k := Key{town, uint16(area)}
			if _, dup := declared[k]; dup {
				return nil, fmt.Errorf("duplicate town area %+v", k)
			}
			mp, err := qualifiedPath(header[2], "map", ".map")
			if err != nil {
				return nil, err
			}
			perms, ok := pattern("permission").blocks(body)
			phases, phaseOK := pattern("phase").blocks(body)
			if len(perms) > 1 || len(phases) > 1 {
				return nil, fmt.Errorf("duplicate permissions or phases in %+v", k)
			}
			policy := ""
			if len(perms) == 1 {
				policy = perms[0]
			}
			p := permission(policy, false, k == (Key{82, 0}), town == 97)
			p.Blocked = p.Blocked || !ok || !phaseOK
			record := Area{Key: k, Path: mp, Permission: p, Phases: []string{}}
			if g := dungeonGate.FindStringSubmatch(body); g != nil {
				if v, ok := integer(g[1]); ok && v >= 0 && v <= 65535 {
					gate := uint16(v)
					record.DungeonGate = &gate
				}
			}
			if len(phases) == 1 {
				for _, v := range quotedMap.FindAllStringSubmatch(phases[0], -1) {
					vp, err := qualifiedPath(v[1], "map", ".map")
					if err != nil {
						return nil, err
					}
					record.Phases = append(record.Phases, vp)
				}
			}
			if len(record.Phases) > 256 {
				return nil, fmt.Errorf("too many phases in %+v", k)
			}
			declared[k] = record
			commerceArea := record
			if town == 97 {
				commerceArea.Permission = permission(policy, false, false, false)
				commerceArea.Permission.Blocked = commerceArea.Permission.Blocked || !ok || !phaseOK
			}
			result.CommerceAreas = append(result.CommerceAreas, commerceArea)
			for _, v := range append([]string{mp}, record.Phases...) {
				key := strings.ToLower(v)
				if _, exists := paths[key]; !exists {
					paths[key] = v
				}
			}
			if k == home {
				g := gate.FindStringSubmatch(body)
				if g == nil || g[1] == "" {
					return nil, fmt.Errorf("Seria room has no spawn gate")
				}
				x, xok := integer(g[1])
				y, yok := integer(g[2])
				if !xok || !yok || !coordinate(x) || !coordinate(y) {
					return nil, fmt.Errorf("invalid Seria spawn")
				}
				result.Room = Point{Town: town, Area: k.Area, X: uint16(x), Y: uint16(y), Direction: 5}
			}
		}
	}
	if result.Room.Town != home.Town || declared[outside].Path == "" || len(paths) > 4096 {
		return nil, fmt.Errorf("mandatory town definitions missing or map count exceeds limit")
	}
	validMaps := map[string]Map{}
	var roomText string
	returns := map[Key]Point{}
	for _, path := range slices.Sorted(maps.Values(paths)) {
		txt, err := a.ReadText(path, budget)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		room := strings.EqualFold(path, declared[home].Path)
		record, r := readMap(path, txt, room)
		validMaps[strings.ToLower(path)] = record
		result.Maps = append(result.Maps, record)
		if room {
			roomText, returns = txt, r
		}
	}
	for _, k := range slices.SortedFunc(maps.Keys(declared), keyOrder) {
		row := declared[k]
		if _, exists := validMaps[strings.ToLower(row.Path)]; !exists {
			continue
		}
		result.Areas = append(result.Areas, row)
		if p, ok := returns[k]; ok && k != home {
			result.RoomReturns = append(result.RoomReturns, roomReturn{k, p})
			if k == outside {
				result.Exit = p
			}
		}
	}
	if roomText == "" || result.Exit.Town != outside.Town {
		return nil, fmt.Errorf("Seria outdoor return missing")
	}
	ui, err := a.ReadText("UI/WarpGate/WarpGate.xui", budget)
	if err != nil {
		return nil, err
	}
	result.Warps, err = warps(ui, roomText)
	if err != nil {
		return nil, err
	}
	result.NpcWarps, err = episode(a, declared, validMaps)
	if err != nil {
		return nil, err
	}
	result.Menus, err = transport(a)
	return result, err
}
