package dungeons

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/gm"
)

const worldPolicy = "AwakeningSeals/v4;user-authorized-A21-port;complete-world-sources"

type sealDrop struct {
	Monster, Item uint32
	Probabilities []uint32
	Difficulty    int
}
type WorldDrops struct {
	Hash      string
	Rows      [][]uint32
	Seals     []sealDrop
	Templates map[uint32]*gm.ItemTemplate
}

func readWorldRows(text string) ([][]uint32, error) {
	text = strings.TrimSpace(text)
	body, ok := strings.CutPrefix(text, "[world drop]")
	if !ok {
		return nil, errors.New("world drop table is missing its opening tag")
	}
	body, ok = strings.CutSuffix(body, "[/world drop]")
	if !ok {
		return nil, errors.New("world drop table is missing its terminator")
	}
	fields := strings.Fields(body)
	if len(fields) > 200*(3+2*256) {
		return nil, errors.New("too many world drop tokens")
	}
	rows := make([][]uint32, 200)
	for i := 0; i < len(fields); {
		key, err := strconv.ParseUint(fields[i], 10, 8)
		if err != nil || key < 1 || key > 200 || rows[key-1] != nil || i+1 >= len(fields) {
			return nil, errors.New("invalid world drop level")
		}
		initial, err := strconv.ParseInt(fields[i+1], 10, 32)
		if err != nil {
			return nil, err
		}
		row := []uint32{uint32(key), uint32(initial)}
		i += 2
		var total uint64
		for i < len(fields) && fields[i] != "-1" {
			if i+1 >= len(fields) || len(row) >= 514 {
				return nil, errors.New("invalid world drop pair count")
			}
			item, e1 := strconv.ParseUint(fields[i], 10, 32)
			weight, e2 := strconv.ParseUint(fields[i+1], 10, 32)
			total += weight
			if e1 != nil || e2 != nil || total > math.MaxInt32 {
				return nil, errors.New("invalid world drop item or weight")
			}
			row = append(row, uint32(item), uint32(weight))
			i += 2
		}
		if i >= len(fields) {
			return nil, errors.New("world drop row requires -1")
		}
		i++
		rows[key-1] = row
	}
	for i, v := range rows {
		if v == nil {
			rows[i] = []uint32{uint32(i + 1), 0}
		}
	}
	return rows, nil
}

func BuildWorldDrops(a Archive, items *commerce.Items, defs []Definition, oculus json.RawMessage) (*WorldDrops, error) {
	world, err := a.ReadText("etc/worlddrop.etc", budget)
	if err != nil {
		return nil, err
	}
	independent, err := a.ReadText("etc/independent_drop.etc", budget)
	if err != nil {
		return nil, err
	}
	rows, err := readWorldRows(world)
	if err != nil {
		return nil, err
	}
	seals, _, err := independentRecords(a, independent, worldIndependent)
	if err != nil {
		return nil, err
	}
	out := &WorldDrops{Rows: rows, Seals: []sealDrop{}, Templates: map[uint32]*gm.ItemTemplate{}}
	seen := map[[2]uint32]bool{}
	for _, r := range seals {
		if r.Flag == 2 || r.DirectItem != 3051 && r.DirectItem != 3052 {
			continue
		}
		if r.OwnerKind != 0 || r.Flag != 0 || r.LevelMin != 0 || r.LevelMax != 0 || r.ResultLimit != 1 || slices.Min(r.AttemptsByPartySize) != 1 || slices.Max(r.AttemptsByPartySize) != 1 || slices.Max(r.Probabilities) > 1_000_000 {
			return nil, errors.New("invalid independent seal source")
		}
		k := [2]uint32{r.Monster, r.DirectItem}
		if seen[k] {
			return nil, errors.New("duplicate independent seal source")
		}
		seen[k] = true
		out.Seals = append(out.Seals, sealDrop{r.Monster, r.DirectItem, r.Probabilities, r.Difficulty})
	}
	ids := map[uint32]bool{3051: true, 3052: true}
	for _, r := range rows {
		for i := 2; i < len(r); i += 2 {
			if r[i] > 0 {
				ids[r[i]] = true
			}
		}
	}
	stack, err := a.ReadIndex("stackable")
	if err != nil {
		return nil, err
	}
	for id := range ids {
		path := stack[int64(id)]
		if path == "" || eventOwned(path, true) {
			continue
		}
		t := monsterDropTemplate(items, id)
		if t != nil && t.Kind == 2 && t.FirstBagSlot >= 65 && t.LastBagSlot <= 360 && !hasDropLifetime(t) {
			out.Templates[id] = t
		}
	}
	for _, id := range []uint32{3051, 3052} {
		t := out.Templates[id]
		if t == nil || t.FirstBagSlot != 121 || t.LastBagSlot != 176 || t.MinimumLevel != 1 || t.UsableJobs != math.MaxUint16 {
			return nil, fmt.Errorf("seal %d has no supported ordinary material template", id)
		}
	}
	type identity struct {
		Id           uint32
		MazeIndex    byte
		ResourceHash string
	}
	if len(defs) == 0 || len(defs) > 4096 {
		return nil, errors.New("missing or excessive world drop dungeons")
	}
	layouts := map[identity]bool{}
	for _, d := range defs {
		if len(d.ResourceHash) != 64 {
			return nil, errors.New("world drop layout lacks resource hash")
		}
		layouts[identity{d.ID, d.MazeIndex, d.ResourceHash}] = true
	}
	distinct := slices.Collect(maps.Keys(layouts))
	slices.SortFunc(distinct, func(a, b identity) int {
		return cmp.Or(cmp.Compare(a.Id, b.Id), cmp.Compare(a.MazeIndex, b.MazeIndex), cmp.Compare(a.ResourceHash, b.ResourceHash))
	})
	parts := []any{worldPolicy, "\nWorldSource/terminated-pairs-empty-weights/v2\n", hashText(world), "\n", hashText(independent), "\n", orderedPairs(out.Templates), "\n", distinct}
	if len(oculus) > 0 && string(oculus) != "[]" {
		parts = append(parts, "\nOculus\n", oculus)
	}
	out.Hash, err = catalogHash(parts...)
	return out, err
}
