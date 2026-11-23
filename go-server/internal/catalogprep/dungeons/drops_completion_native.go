package dungeons

import (
	"fmt"
	"regexp"
	"slices"

	"s9a1/server/internal/catalogprep/pvftext"
)

type completionDifficulty struct {
	npc, dungeon        uint32
	difficulty, variant byte
	pool                completionPool
}

type completionNative struct {
	npcs          map[uint32][]completionNPC
	ground, clear map[[2]uint32]*completionPool
	difficulty    map[[2]uint32][]completionDifficulty
}

var (
	completionHeaderEnd   = regexp.MustCompile(`(?m)^\s*\[/?[^\]\r\n]+\]`)
	completionGroundBlock = regexp.MustCompile(`(?ims)^\[dungeon\]\s*(.*?)^\[/dungeon(?: item)?\]`)
)

func readCompletionNative(text string, wanted map[uint32]bool) (completionNative, error) {
	out := completionNative{npcs: map[uint32][]completionNPC{}, ground: map[[2]uint32]*completionPool{},
		clear: map[[2]uint32]*completionPool{}, difficulty: map[[2]uint32][]completionDifficulty{}}
	if len(text) > budget {
		return out, fmt.Errorf("PVF secret-shop source exceeds its preparation budget")
	}
	n, err := pvftext.Numbers(text, "dungeon npc")
	if err != nil || len(n) == 0 || n[0] < 0 {
		return out, fmt.Errorf("PVF secret-shop NPC table is truncated")
	}
	for i := 1; i+6 < len(n); i++ {
		id, npc, v := n[i], n[i+1], n[i+2:i+7]
		if id <= 0 || npc <= 0 || slices.Min(v) < 0 || slices.Max(v) > 10000 || !wanted[uint32(id)] {
			continue
		}
		row := completionNPC{dungeon: uint32(id), npc: uint32(npc)}
		for k, val := range v {
			row.weights[k] = uint32(val)
		}
		out.npcs[row.dungeon] = append(out.npcs[row.dungeon], row)
		i += 6
	}
	if len(out.npcs) == 0 && len(wanted) > 0 {
		return out, fmt.Errorf("PVF secret-shop NPC table has no prepared dungeon rows")
	}
	for _, group := range out.npcs {
		var sums [5]uint64
		for _, row := range group {
			for i, w := range row.weights {
				sums[i] += uint64(w)
			}
		}
		for _, total := range sums {
			if total != 10000 {
				return out, fmt.Errorf("PVF dungeon NPC weights lack native column total")
			}
		}
	}
	for _, raw := range pvftext.Blocks(text, "npc") {
		loc := completionHeaderEnd.FindStringIndex(raw)
		if loc == nil || loc[0] == 0 {
			return out, fmt.Errorf("PVF secret-shop NPC header missing")
		}
		header, err := pvftext.Integers(raw[:loc[0]])
		if err != nil || len(header) != 1 || header[0] <= 0 {
			return out, fmt.Errorf("PVF secret-shop NPC header invalid")
		}
		npc := uint32(header[0])
		for _, row := range completionGroundBlock.FindAllStringSubmatch(raw, -1) {
			if err := readCompletionPool(out.ground, npc, row[1]); err != nil {
				return out, err
			}
		}
		for _, row := range pvftext.Blocks(raw, "dungeon cash item") {
			if err := readCompletionPool(out.clear, npc, row); err != nil {
				return out, err
			}
		}
		for _, row := range pvftext.Blocks(raw, "dungeon item difficulty") {
			v, err := pvftext.Integers(row)
			if err != nil || len(v) < 4 || (len(v)-4)%6 != 0 || v[0] < 0 || slices.Min(v[1:4]) < 0 || slices.Max(v[1:4]) > 255 {
				return out, fmt.Errorf("PVF secret-shop difficulty row is malformed")
			}
			rows, err := completionRows(v[4:])
			if err != nil {
				return out, err
			}
			d := completionDifficulty{npc: npc, dungeon: uint32(v[0]), difficulty: byte(v[1]), variant: byte(v[3]),
				pool: completionPool{draws: byte(v[2]), rows: rows}}
			key := [2]uint32{npc, d.dungeon}
			out.difficulty[key] = append(out.difficulty[key], d)
		}
	}
	return out, nil
}

func readCompletionPool(pools map[[2]uint32]*completionPool, npc uint32, body string) error {
	v, err := pvftext.Integers(body)
	if err != nil || len(v) < 2 || (len(v)-2)%6 != 0 || v[0] < 0 || v[1] < 0 || v[1] > 255 {
		return fmt.Errorf("PVF secret-shop completion pool malformed")
	}
	rows, err := completionRows(v[2:])
	if err != nil {
		return err
	}
	k := [2]uint32{npc, uint32(v[0])}
	pool := pools[k]
	if pool == nil {
		pools[k] = &completionPool{draws: byte(v[1]), rows: rows}
	} else {
		if pool.draws != byte(v[1]) {
			return fmt.Errorf("PVF duplicate secret-shop pool changes draw count")
		}
		pool.rows = append(pool.rows, rows...)
	}
	return nil
}

func completionRows(v []int) ([]completionRow, error) {
	out := make([]completionRow, 0, len(v)/6)
	for i := 0; i < len(v); i += 6 {
		if v[i] <= 0 || v[i+2] < 0 || v[i+4] <= 0 || v[i+5] <= 0 {
			return nil, fmt.Errorf("PVF secret-shop reward row has invalid value")
		}
		out = append(out, completionRow{Item: uint32(v[i]), Price: uint32(v[i+2]), Quantity: uint32(v[i+4]), Weight: uint32(v[i+5])})
	}
	return out, nil
}

func (n completionNative) pools(npc completionNPC, v completionVariant) (ground, clear *completionPool, err error) {
	key := [2]uint32{npc.npc, npc.dungeon}
	clear = n.clear[key]
	if v.stage != 0 {
		rows := n.difficulty[key]
		chosen := []completionDifficulty{}
		for _, row := range rows {
			if row.difficulty == v.slot+1 || uint16(row.variant) == v.stage {
				chosen = append(chosen, row)
			}
		}
		if len(chosen) == 0 && len(rows) == 1 {
			chosen = rows
		}
		if len(chosen) > 0 {
			ground = &completionPool{draws: chosen[0].pool.draws}
			for _, row := range chosen {
				if row.pool.draws != ground.draws {
					return nil, nil, fmt.Errorf("PVF challenge pools disagree on draw count")
				}
				ground.rows = append(ground.rows, row.pool.rows...)
			}
		}
	}
	if ground == nil {
		ground = n.ground[key]
	}
	if ground == nil && v.stage != 0 {
		ground = clear
	}
	return ground, clear, nil
}
