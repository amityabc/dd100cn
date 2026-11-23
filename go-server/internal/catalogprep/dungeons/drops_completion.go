package dungeons

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
)

const completionDropPath = "etc/secretshop.etc"
const completionClearPath = "etc/itemdropinfo_clearreward.etc"
const completionDropPolicy = "LocalDungeonPvFCompletion/v9;difficulty-allow-list;secretshop-dungeon-npc;clearreward-dungeon-index-override;ground-and-clear-pools;difficulty-subpools;native-six-field-pool;weighted-without-replacement;final-boss-only"

type CompletionDrops struct {
	Hash    string
	Groups  []CompletionGroup
	Indexed []IndexedCompletionGroup
}

type completionKey struct {
	Dungeon          uint32
	Maze, Difficulty byte
	ChallengeStage   uint16
}
type CompletionGroup struct {
	completionKey
	Branches []CompletionBranch
}
type IndexedCompletionGroup struct {
	Dungeon uint32
	Maze    byte
	Pool    *CompletionPool
}
type CompletionBranch struct {
	Weight               uint32
	Pool, CompletionPool *CompletionPool
	npc                  uint32
}
type CompletionPool struct {
	SelectionCount byte
	Candidates     []CompletionCandidate
}
type CompletionCandidate struct {
	Quantity, Weight uint32
	Template         json.RawMessage
	source           completionRow
}
type completionRow struct{ Item, Price, Quantity, Weight uint32 }
type completionPool struct {
	draws byte
	rows  []completionRow
}
type completionNPC struct {
	dungeon, npc uint32
	weights      [5]uint32
}
type completionBranch struct {
	choice        completionNPC
	slot          int
	ground, clear *completionPool
}
type completionVariant struct {
	difficulty byte
	stage      uint16
	slot       byte
}

func BuildCompletionDrops(a Archive, items *commerce.Items, definitions []Definition) (*CompletionDrops, []uint32, error) {
	if len(definitions) == 0 {
		return nil, nil, nil
	}
	source, err := a.ReadText(completionDropPath, budget)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	sum, err := rawHash(a, completionDropPath)
	if err != nil {
		return nil, nil, err
	}
	prepared := []Definition{}
	wanted := map[uint32]bool{}
	seen := map[[2]uint32]bool{}
	for _, d := range definitions {
		if !d.UsesOrdinaryRewards || d.meta.disableDrops || d.ID == 100000151 || d.ID == 100000007 || d.ID == 100002669 || d.ID == 400000389 ||
			len(d.meta.difficultyItems) != 5 || d.Difficulty >= 5 {
			continue
		}
		k := [2]uint32{d.ID, uint32(d.MazeIndex)}
		if seen[k] {
			continue
		}
		seen[k], wanted[d.ID] = true, true
		prepared = append(prepared, d)
	}
	native, err := readCompletionNative(source, wanted)
	if err != nil {
		return nil, nil, err
	}
	clearHash, indexedRows, err := readIndexedCompletion(a, wanted)
	if err != nil {
		return nil, nil, err
	}
	needed := map[uint32]bool{}
	selected := map[completionKey][]completionBranch{}
	for _, d := range prepared {
		for _, v := range completionVariants(d) {
			allow := d.meta.difficultyItems[v.slot]
			if len(allow) == 0 {
				continue
			}
			present := false
			var branches []completionBranch
			for _, npc := range native.npcs[d.ID] {
				ground, clear, err := native.pools(npc, v)
				if err != nil {
					return nil, nil, err
				}
				branches = append(branches, completionBranch{npc, int(v.slot),
					filterCompletionPool(ground, allow, needed), filterCompletionPool(clear, allow, needed)})
				present = present || npc.weights[v.slot] > 0
			}
			if present {
				selected[completionKey{d.ID, d.MazeIndex, v.difficulty, v.stage}] = branches
			}
		}
	}
	for _, rows := range indexedRows {
		for _, row := range rows {
			needed[row.Item] = true
		}
	}
	templates := map[uint32]json.RawMessage{}
	for id := range needed {
		t, err := items.Prepared(id)
		if err != nil || t == nil {
			continue
		}
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, nil, err
		}
		templates[id] = raw
	}
	out := &CompletionDrops{Groups: []CompletionGroup{}, Indexed: []IndexedCompletionGroup{}}
	resultIDs := map[uint32]bool{}
	buildPool := func(native *completionPool) *CompletionPool {
		if native == nil || len(native.rows) == 0 {
			return nil
		}
		sum := uint64(0)
		pool := &CompletionPool{SelectionCount: native.draws, Candidates: make([]CompletionCandidate, 0, len(native.rows))}
		for _, r := range native.rows {
			sum += uint64(r.Weight)
			pool.Candidates = append(pool.Candidates, CompletionCandidate{r.Quantity, r.Weight, templates[r.Item], r})
		}
		if sum == 0 || sum > math.MaxInt32 {
			return nil
		}
		for _, r := range native.rows {
			if templates[r.Item] != nil {
				resultIDs[r.Item] = true
			}
		}
		return pool
	}
	keys := slices.SortedFunc(maps.Keys(selected), compareCompletionKey)
	for _, key := range keys {
		group := CompletionGroup{completionKey: key, Branches: []CompletionBranch{}}
		for _, b := range selected[key] {
			group.Branches = append(group.Branches, CompletionBranch{Weight: b.choice.weights[b.slot], Pool: buildPool(b.ground),
				CompletionPool: buildPool(b.clear), npc: b.choice.npc})
		}
		out.Groups = append(out.Groups, group)
	}
	for _, d := range prepared {
		if rows := indexedRows[d.ID]; len(rows) > 0 {
			if pool := buildPool(&completionPool{draws: 1, rows: rows}); pool != nil {
				out.Indexed = append(out.Indexed, IndexedCompletionGroup{Dungeon: d.ID, Maze: d.MazeIndex, Pool: pool})
			}
		}
	}
	slices.SortFunc(out.Indexed, func(a, b IndexedCompletionGroup) int {
		return compareCompletionKey(completionKey{Dungeon: a.Dungeon, Maze: a.Maze}, completionKey{Dungeon: b.Dungeon, Maze: b.Maze})
	})
	out.Hash = completionHash(out, keys, selected, sum, clearHash)
	return out, slices.Sorted(maps.Keys(resultIDs)), nil
}

func completionVariants(d Definition) []completionVariant {
	var out []completionVariant
	if len(d.ChallengeStages) > 0 {
		slots := []byte{}
		for i, row := range d.meta.difficultyItems {
			if len(row) > 0 {
				slots = append(slots, byte(i))
			}
		}
		if len(slots) != len(d.ChallengeStages) {
			return nil
		}
		for i, stage := range d.ChallengeStages {
			out = append(out, completionVariant{4, stage, slots[i]})
		}
		return out
	}
	diffs := slices.Clone(d.meta.selectable)
	if len(diffs) == 0 {
		diffs = []byte{d.Difficulty}
	}
	slices.Sort(diffs)
	for _, difficulty := range slices.Compact(diffs) {
		if difficulty < 5 {
			out = append(out, completionVariant{difficulty, 0, difficulty})
		}
	}
	return out
}

func filterCompletionPool(pool *completionPool, allow []uint32, needed map[uint32]bool) *completionPool {
	if pool == nil {
		return nil
	}
	out := &completionPool{draws: pool.draws}
	for _, row := range pool.rows {
		if slices.Contains(allow, row.Item) && row.Weight > 0 && row.Quantity > 0 {
			out.rows = append(out.rows, row)
			needed[row.Item] = true
		}
	}
	if len(out.rows) == 0 {
		return nil
	}
	return out
}

func compareCompletionKey(a, b completionKey) int {
	switch {
	case a.Dungeon < b.Dungeon:
		return -1
	case a.Dungeon > b.Dungeon:
		return 1
	case a.Maze != b.Maze:
		return int(a.Maze) - int(b.Maze)
	case a.Difficulty != b.Difficulty:
		return int(a.Difficulty) - int(b.Difficulty)
	default:
		return int(a.ChallengeStage) - int(b.ChallengeStage)
	}
}

func completionHash(data *CompletionDrops, keys []completionKey, selected map[completionKey][]completionBranch, source, clear string) string {
	var identity strings.Builder
	fmt.Fprintf(&identity, "%s\n%s|%s\nclearreward=%s\n", completionDropPolicy, completionDropPath, source, clear)
	for _, g := range data.Indexed {
		fmt.Fprintf(&identity, "indexed|%d|%d|", g.Dungeon, g.Maze)
		for _, c := range g.Pool.Candidates {
			fmt.Fprintf(&identity, "%d,%d,%d;", c.source.Item, c.Quantity, c.Weight)
		}
		identity.WriteByte('\n')
	}
	for _, g := range data.Groups {
		fmt.Fprintf(&identity, "%d|%d|%d|%d\n", g.Dungeon, g.Maze, g.Difficulty, g.ChallengeStage)
		for _, b := range g.Branches {
			fmt.Fprintf(&identity, "npc=%d@%d|", b.npc, b.Weight)
			writePreparedCompletion(&identity, "ground", b.Pool)
			writePreparedCompletion(&identity, "completion", b.CompletionPool)
		}
		identity.WriteByte('\n')
	}
	for _, k := range keys {
		fmt.Fprintf(&identity, "source|%d|%d|%d|%d|", k.Dungeon, k.Maze, k.Difficulty, k.ChallengeStage)
		for _, b := range selected[k] {
			weights := make([]string, len(b.choice.weights))
			for i, w := range b.choice.weights {
				weights[i] = strconv.FormatUint(uint64(w), 10)
			}
			fmt.Fprintf(&identity, "npc=%d@%d|%s|", b.choice.npc, b.slot, strings.Join(weights, ","))
			writeSourceCompletion(&identity, "ground", b.ground)
			writeSourceCompletion(&identity, "completion", b.clear)
		}
		identity.WriteByte('\n')
	}
	return hashText(identity.String())
}

func writePreparedCompletion(b *strings.Builder, name string, pool *CompletionPool) {
	if pool == nil {
		writeSourceCompletion(b, name, nil)
		return
	}
	p := &completionPool{draws: pool.SelectionCount, rows: make([]completionRow, 0, len(pool.Candidates))}
	for _, c := range pool.Candidates {
		p.rows = append(p.rows, c.source)
	}
	writeSourceCompletion(b, name, p)
}

func writeSourceCompletion(b *strings.Builder, name string, p *completionPool) {
	if p == nil {
		fmt.Fprintf(b, "%s=none\n", name)
		return
	}
	fmt.Fprintf(b, "%s=draws=%d|", name, p.draws)
	for _, r := range p.rows {
		fmt.Fprintf(b, "%d,%d,%d,%d;", r.Item, r.Price, r.Quantity, r.Weight)
	}
	b.WriteByte('\n')
}

func readIndexedCompletion(a Archive, wanted map[uint32]bool) (string, map[uint32][]completionRow, error) {
	text, err := a.ReadText(completionClearPath, budget)
	if errors.Is(err, fs.ErrNotExist) {
		return "missing", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	sum, err := rawHash(a, completionClearPath)
	if err != nil {
		return "", nil, err
	}
	native, err := parseClearRewardSource(text, sum, false)
	if err != nil {
		return "", nil, err
	}
	out := map[uint32][]completionRow{}
	for _, row := range native.DungeonIndexRewardRates {
		if row.Variant != 0 || !wanted[row.DungeonIndex] {
			continue
		}
		for _, item := range row.Items {
			out[row.DungeonIndex] = append(out[row.DungeonIndex], completionRow{Item: item.Item, Quantity: item.Quantity, Weight: item.Weight})
		}
	}
	return native.Hash, out, nil
}
