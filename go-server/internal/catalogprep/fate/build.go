// Package fate prepares the Destiny2 activity from its native PVF resources.
package fate

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	gameplay "s9a1/server/internal/fate"
	"s9a1/server/internal/opening"
)

const (
	root        = "live/event/chn/2021/0914_decisionsuntodestiny2/"
	eventPath   = root + "decisionsuntodestiny.evt"
	event2Path  = root + "decisionsuntodestiny2.evt"
	abilityPath = root + "etc/decisionsuntodestinyability.co"
	budget      = 1024 * 1024
)

type Archive interface {
	Read(string, int) ([]byte, error)
	ReadText(string, int) (string, error)
	ReadIndex(string, ...int) (map[int64]string, error)
	ReadFatePortalIndex() (map[int64]string, error)
	ReadFateAbilityText() (string, error)
	ReadFateAbility() ([]byte, error)
}

type TownRoute struct {
	Camp, Seria       opening.Point
	EntryNpc, CampNpc uint32
	Gate              int
	EntryQuest        uint16
}

type NodeMap struct {
	Phase, NodeType int
	Map             uint32
}

type Catalog struct {
	gameplay.Definition
	Town                         TownRoute
	ShopMapId, ShopNpc           uint32
	CarrierResetX, CarrierResetY uint16
	GoldenMonsterId              uint32
	GoldenWave, GoldenPoints     int
	NodeMaps                     []NodeMap
	LoadingDungeon               loadingDungeon
}

func Build(a Archive) (*Catalog, error) {
	s := newSource(a)
	first, err := a.ReadText(eventPath, budget)
	if errors.Is(err, fs.ErrNotExist) {
		_, sibling := a.ReadText(event2Path, budget)
		if errors.Is(sibling, fs.ErrNotExist) {
			return nil, nil
		}
		if sibling != nil {
			return nil, sibling
		}
		return nil, fmt.Errorf("present Destiny2 directory lacks %s", eventPath)
	}
	if err != nil {
		return nil, err
	}
	first = s.recordText(eventPath, first)
	second := s.text(event2Path)
	r := &Catalog{}
	r.DungeonId = s.id(s.one(s.block(first, "dungeon index")))
	s.require(r.DungeonId == 400001963, "unexpected Destiny2 dungeon")
	dgn := s.resolveText("dungeon", r.DungeonId)
	minimum := s.scalar(dgn, "minimum required level")
	s.require(minimum == 100, "Destiny2 requires its source level100 gate")
	r.MinimumLevel = byte(minimum)
	for _, tag := range []string{"dungeon balance table", "monsterapc diff table"} {
		s.text(s.path(s.section(dgn, tag, false)))
	}
	for _, tag := range []string{"no fatigue", "disable exp", "no experience monster and result", "disable clear gold reward", "disable clear reward"} {
		s.require(has(dgn, tag), "activity dungeon lacks ordinary reward suppression")
	}
	layerMaps := s.loading(r, dgn)
	if s.err != nil {
		return nil, s.err
	}
	s.phases(r, second, layerMaps)
	r.DefaultGrid = s.scalar(second, "default map phase")
	s.require(r.DefaultGrid == 0, "unsupported source default grid")
	r.Points = s.points(s.block(second, "archangel point per node info"))
	pointLimit, sinLimit := s.scalar(second, "max archangel point"), s.scalar(second, "max seven sin point")
	s.require(pointLimit > 0 && sinLimit > 0, "invalid activity point limits")
	r.PointLimit, r.SinPointLimit = int32(pointLimit), int32(sinLimit)
	for _, row := range r.Points {
		s.require(slices.Max(row) <= pointLimit, "activity points exceed the source limit")
	}
	r.Blessings, r.Sins = s.ranks(s.block(first, "archangels blessing"), false), s.ranks(s.block(first, "seven sins"), true)
	for _, rank := range r.Sins {
		r.SinCosts = append(r.SinCosts, rank.Cost)
	}
	r.StoryCapacity, r.ShopNodeTypes = 6, []int{5}
	s.assets(r, first, second)
	s.town(r, dgn)
	s.carrier(r)
	if s.err != nil {
		return nil, s.err
	}
	r.ResourceHash = s.ruleHash()
	return r, nil
}

func (s *source) phases(r *Catalog, second string, layerMaps []uint32) {
	phases := blocks(second, "phase")
	if !s.require(len(phases) == 5, "activity requires five phase definitions") {
		return
	}
	for phase, body := range phases {
		rows := s.numbers(s.block(body, "map phase list"))
		if !s.require(len(rows) == 12, "invalid phase-to-layered-map table") {
			return
		}
		maps := map[int]uint32{}
		for at := 0; at < len(rows); at += 2 {
			node, layer := rows[at], rows[at+1]
			if !s.require(node >= 2 && node <= 7 && layer > 0 && layer < len(layerMaps) && maps[node] == 0, "invalid or duplicate activity node map") {
				return
			}
			maps[node] = layerMaps[layer]
		}
		for nt := 2; nt <= 7; nt++ {
			r.NodeMaps = append(r.NodeMaps, NodeMap{phase, nt, maps[nt]})
		}
		for _, tag := range []string{"normal", "named", "boss"} {
			values := s.numbers(s.block(body, tag))
			s.require(len(values) > 0 && validWaves(values), "invalid source wave list")
		}
		rectangle := s.numbers(s.section(s.resolveText("map", maps[3]), "dungeon start area", false))
		if !s.require(len(rectangle) == 4 && slices.Min(rectangle) >= 0, "invalid activity boss start rectangle") {
			return
		}
		r.BossPositions = append(r.BossPositions, struct{ X, Y uint16 }{s.coordinate(rectangle[0] + rectangle[2]/2), s.coordinate(rectangle[1] + rectangle[3]/2)})
		if phase == 0 {
			r.ShopMapId = maps[5]
		}
		s.require(r.ShopMapId == 400005357 && r.ShopMapId == maps[5], "shop no longer identifies the confirmed activity map")
	}
	directory := tokens(s.block(second, "grid preset script list"))
	if !s.require(len(directory) == 10, "activity grid directory must contain five entries") {
		return
	}
	seen := map[int]bool{}
	for at := 0; at < len(directory); at += 2 {
		grid := s.integer(directory[at])
		path := s.path(directory[at+1])
		if !s.require(grid >= 0 && grid < 5 && !seen[grid] && strings.HasPrefix(path, root+"grid/"), "invalid activity grid definition") {
			return
		}
		seen[grid] = true
		r.Layouts = append(r.Layouts, s.layouts(grid, s.text(path))...)
	}
	slices.SortFunc(r.Layouts, func(a, b gameplay.Layout) int {
		if a.Grid != b.Grid {
			return a.Grid - b.Grid
		}
		return a.Preset - b.Preset
	})
}

var resetPosition = regexp.MustCompile(`(?im)^\[POS\]\s*(\d+)\s+(\d+)\s+0\s*\[USE MAP POS\]`)

func (s *source) carrier(r *Catalog) {
	ability, hash := s.ability()
	for _, tag := range []string{"camp passive equipment", "equipment limit table", "type probability", "equipment subtype probability", "type price", "equipment subtype price"} {
		s.require(len(tagLine(ability, tag)) > 0, "activity ability lacks "+tag)
	}
	s.require(hash == "FECF68CFC317F8B55F60F17C45A539516D4A22B8BE1FF44FAC41D19577A39245", "activity ability table is outside the reviewed source")
	carrier := s.text(root + "monster/itemdropmonster/action/proc.act")
	behaviors := blocks(carrier, "BEHAVIOR")
	if !s.require(len(behaviors) == 5 && has(behaviors[1], "DESTROY"), "carrier drop branch lacks its source destruction behavior") {
		return
	}
	weights := s.numbers(s.block(carrier, "RANDOM GACHA"))
	if !s.require(len(weights) == 4 && weights[0] == 1 && weights[2] == 2 && weights[1] > 0 && weights[3] > 0, "invalid carrier drop policy") {
		return
	}
	r.DropWeight, r.NoDropWeight = weights[1], weights[3]
	reset := resetPosition.FindAllStringSubmatch(carrier, -1)
	if !s.require(len(reset) == 1, "carrier reset position is missing or ambiguous") {
		return
	}
	r.CarrierResetX, r.CarrierResetY = s.coordinate(s.integer(reset[0][1])), s.coordinate(s.integer(reset[0][2]))
	for _, layout := range r.Layouts {
		for _, node := range layout.Nodes {
			if node.Type != 7 {
				continue
			}
			s.require(node.Hidden && len(node.FixedWaves) == 5, "incomplete hidden golden node")
			for _, wave := range node.FixedWaves {
				if !s.require(len(wave) == 1 && (r.GoldenWave == 0 || r.GoldenWave == wave[0]), "inconsistent golden node wave") {
					return
				}
				r.GoldenWave = wave[0]
			}
		}
	}
	s.require(r.GoldenWave > 0, "missing golden wave")
	for _, wave := range blocks(s.text(root+"etc/decisionsuntodestinyspawnsystem.etc"), "wave") {
		header, _, ok := strings.Cut(wave, "[")
		if !s.require(ok, "invalid activity spawn wave") || s.one(header) != r.GoldenWave {
			continue
		}
		row := s.numbers(s.section(wave, "monster", false))
		if !s.require(r.GoldenMonsterId == 0 && len(row) == 3 && row[1] > 0 && row[2] > 0, "invalid golden wave actor") {
			return
		}
		r.GoldenMonsterId = s.id(row[0])
	}
	s.resolveText("monster", r.GoldenMonsterId)
	points := r.Points[7]
	if s.require(len(points) == 5 && slices.Min(points) == slices.Max(points), "golden point rewards disagree between phases") {
		r.GoldenPoints = points[0]
	}
}
