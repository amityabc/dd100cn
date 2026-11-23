package dungeons

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/dungeon"
)

const (
	bossEquipmentPolicy = "OculusBossEquipment/user-authorized-wisdom-reference;PVF-weighted-pools;retained-unavailable-outcomes/v2"
	supportPath         = "etc/supportdropsystem.etc"
	hellItemPath        = "etc/helldropepicitemtable.etc"
	mythTypePath        = "etc/customability/mythoptions.etc"
	mythIndexPath       = "etc/customability/mythoptions.lst"
)

type weightedEquipment struct{ id, weight uint32 }

var hellAreaPool = regexp.MustCompile(`(?s)\[drop area\]\s*(\d+)|\[drop epic item list\](.*?)\[/drop epic item list\]`)

func rawHash(a Archive, path string) (string, error) {
	raw, err := a.Read(path, budget)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%X", sha256.Sum256(raw)), nil
}

func BuildBossEquipment(a Archive) (*dungeon.BossEquipmentCatalog, error) {
	return PrepareBossEquipment(a, nil)
}

// PrepareBossEquipment also registers each usable base item in the shared
// catalog. Mythology affixes belong to the acquired instance, not its base.
func PrepareBossEquipment(a Archive, prepared map[uint32]*gm.ItemTemplate) (*dungeon.BossEquipmentCatalog, error) {
	items, err := commerce.NewItems(a)
	if err != nil {
		return nil, err
	}
	support, err := a.ReadText(supportPath, budget)
	if err != nil {
		return nil, err
	}
	hell, err := a.ReadText(hellItemPath, budget)
	if err != nil {
		return nil, err
	}
	area, found := 0, 0
	for _, block := range pvftext.Blocks(support, "info") {
		values, err := pvftext.Numbers(block, "dungeon index")
		if err != nil {
			return nil, err
		}
		if !slices.Equal(values, []int{int(dungeon.WisdomDungeon)}) {
			continue
		}
		values, err = pvftext.Numbers(block, "area")
		if err != nil || len(values) != 2 || values[0] <= 0 {
			return nil, fmt.Errorf("Wisdom has no unique PVF drop area mapping")
		}
		area, found = values[0], found+1
	}
	if found != 1 {
		return nil, fmt.Errorf("Wisdom has no unique PVF drop area mapping")
	}
	pools, err := equipmentPools(hell, area)
	if err != nil {
		return nil, err
	}
	catalog := &dungeon.BossEquipmentCatalog{Epic: 100000, Mythic: 600, Pools: map[byte][]dungeon.BossCandidate{}}
	supportHash, err := rawHash(a, supportPath)
	if err != nil {
		return nil, err
	}
	tableHash, err := rawHash(a, hellItemPath)
	if err != nil {
		return nil, err
	}
	identity := []string{bossEquipmentPolicy, strconv.Itoa(int(catalog.Epic)), strconv.Itoa(int(catalog.Mythic)), supportHash, tableHash}
	myth, err := newMythology(a)
	if err != nil {
		return nil, err
	}
	available := items.EquipmentIds()
	for _, rarity := range []byte{2, 3} {
		candidates := []dungeon.BossCandidate{}
		for _, row := range pools[rarity] {
			candidate := dungeon.BossCandidate{Id: row.id, Weight: row.weight, Options: [][]byte{}}
			source := fmt.Sprintf("%d:%d:", row.id, row.weight)
			if row.weight == 0 || !slices.Contains(available, row.id) {
				identity = append(identity, source+"unavailable")
				candidates = append(candidates, candidate)
				continue
			}
			item, err := items.Source(row.id)
			if err != nil {
				return nil, err
			}
			wanted := 4
			if rarity == 3 {
				wanted = 7
			}
			p := &input{}
			level, err := pvftext.Numbers(item.Text, "minimum level")
			if err != nil {
				return nil, err
			}
			kind, err := pvftext.Numbers(item.Text, "rarity")
			if err != nil {
				return nil, err
			}
			if !p.require(slices.Equal(level, []int{100}) && slices.Equal(kind, []int{wanted}), "Wisdom pool item outside source level100 rarity") {
				return nil, p.err
			}
			template, err := item.Ordinary()
			if err != nil || template == nil {
				template, _ = item.GiftFor(gm.GiftMonster)
			}
			if template == nil {
				identity = append(identity, source+"unavailable:"+item.Hash)
				candidates = append(candidates, candidate)
				continue
			}
			if prepared != nil {
				base := *template
				prepared[row.id] = &base
			}
			if rarity == 3 {
				candidate.Options, template.MythologyHash, err = myth.profile(item.Text, item.Hash)
				if err != nil {
					return nil, err
				}
				template.MythologyOptions = byte(len(candidate.Options))
			}
			if template.Kind > 1 {
				return nil, fmt.Errorf("Wisdom pool item %d is not ordinary equipment", row.id)
			}
			candidate.Template, err = json.Marshal(template)
			if err != nil {
				return nil, err
			}
			identity = append(identity, source+template.ResourceHash+":"+template.MythologyHash)
			candidates = append(candidates, candidate)
		}
		catalog.Pools[rarity] = candidates
	}
	catalog.Hash = hashText(strings.Join(identity, "\n"))
	return catalog, nil
}

func equipmentPools(text string, target int) (map[byte][]weightedEquipment, error) {
	area := 0
	output := map[byte][]weightedEquipment{}
	for _, m := range hellAreaPool.FindAllStringSubmatch(text, -1) {
		if m[1] != "" {
			var err error
			area, err = strconv.Atoi(m[1])
			if err != nil {
				return nil, err
			}
			continue
		}
		if area != target {
			continue
		}
		values, err := pvftext.Integers(m[2])
		if err != nil || len(values) == 0 || len(values)%2 != 1 || len(values) > 16385 || slices.Min(values) < 0 {
			return nil, fmt.Errorf("Oculus pool requires bounded category and item weights")
		}
		if values[0] != 2 && values[0] != 3 {
			continue
		}
		var sum uint64
		rows := make([]weightedEquipment, 0, (len(values)-1)/2)
		for i := 1; i < len(values); i += 2 {
			w := uint32(values[i+1])
			sum += uint64(w)
			if sum > math.MaxUint32 {
				return nil, fmt.Errorf("Oculus equipment draw exceeds native width")
			}
			rows = append(rows, weightedEquipment{uint32(values[i]), w})
		}
		key := byte(values[0])
		if _, ok := output[key]; !ok {
			output[key] = rows
		}
	}
	if len(output) != 2 {
		return nil, fmt.Errorf("Oculus epic and mythology pool declarations required")
	}
	return output, nil
}

type mythEntry struct {
	id   int
	path string
}
type mythOption struct {
	variants []byte
	hash     string
}
type mythology struct {
	a                   Archive
	typeHash, indexHash string
	paths               map[int]string
	options             map[int]mythOption
}

var mythIndex = regexp.MustCompile("(\\d+)\\s+`([^`]+)`")

func readMythIndex(text string) ([]mythEntry, error) {
	out := []mythEntry{}
	seen := map[int]bool{}
	p := &input{}
	rows := mythIndex.FindAllStringSubmatchIndex(text, -1)
	p.completeRows(text, rows, "Mythology index has unsupported tokens")
	for _, row := range rows {
		n := int(p.number(text[row[2]:row[3]], 32))
		p.require(!seen[n], "Mythology index repeats an ID")
		seen[n] = true
		out = append(out, mythEntry{n, text[row[4]:row[5]]})
	}
	return out, p.err
}

func newMythology(a Archive) (*mythology, error) {
	kind, err := a.ReadText(mythTypePath, budget)
	if err != nil {
		return nil, err
	}
	s, err := pvftext.Section(kind, "custom ability type")
	if err != nil || s != "`mythology enchant`" {
		return nil, fmt.Errorf("mythology type differs from PVF")
	}
	text, err := a.ReadText(mythIndexPath, budget)
	if err != nil {
		return nil, err
	}
	rows, err := readMythIndex(text)
	if err != nil {
		return nil, err
	}
	result := &mythology{a: a, paths: map[int]string{}, options: map[int]mythOption{}}
	result.typeHash, err = rawHash(a, mythTypePath)
	if err != nil {
		return nil, err
	}
	result.indexHash, err = rawHash(a, mythIndexPath)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		result.paths[r.id] = r.path
	}
	return result, nil
}

func mythologyPath(root, relative string) (string, error) {
	if relative == "" || strings.HasPrefix(relative, "/") || strings.Contains(relative, "\\") || strings.Contains(relative, "..") {
		return "", fmt.Errorf("mythology source leaves its resource root")
	}
	return strings.ToLower(root + relative), nil
}

func (m *mythology) option(id int) (mythOption, error) {
	if o, ok := m.options[id]; ok {
		return o, nil
	}
	path, err := mythologyPath("etc/customability/", m.paths[id])
	if err != nil {
		return mythOption{}, err
	}
	text, err := m.a.ReadText(path, budget)
	if err != nil {
		return mythOption{}, err
	}
	rows, err := readMythIndex(text)
	if err != nil || len(rows) == 0 || len(rows) > 256 {
		return mythOption{}, fmt.Errorf("invalid mythology variant count")
	}
	base, err := rawHash(m.a, path)
	if err != nil {
		return mythOption{}, err
	}
	identity := []string{base}
	out := mythOption{}
	for _, row := range rows {
		if row.id < 0 || row.id > 255 {
			return mythOption{}, fmt.Errorf("mythology variant exceeds native byte width")
		}
		leaf, err := mythologyPath("etc/customability/mythoptions/", row.path)
		if err != nil {
			return mythOption{}, err
		}
		text, err := m.a.ReadText(leaf, budget)
		if err != nil {
			return mythOption{}, err
		}
		if strings.TrimSpace(text) == "" {
			return mythOption{}, fmt.Errorf("mythology variant is empty")
		}
		hash, err := rawHash(m.a, leaf)
		if err != nil {
			return mythOption{}, err
		}
		identity = append(identity, fmt.Sprintf("%d:%s", row.id, hash))
		out.variants = append(out.variants, byte(row.id))
	}
	out.hash = hashText(strings.Join(identity, "\n"))
	m.options[id] = out
	return out, nil
}

var errMythologyShape = errors.New("unsupported mythology ordered option profile")

func (m *mythology) profile(text, hash string) ([][]byte, string, error) {
	counts, err := pvftext.Numbers(text, "custom ability count")
	if err != nil {
		return nil, "", err
	}
	ids, err := pvftext.Numbers(text, "custom ability info list")
	if err != nil {
		return nil, "", err
	}
	kind, err := pvftext.Section(text, "custom ability type")
	if err != nil {
		return nil, "", err
	}
	if len(counts) != 1 || (counts[0] != 3 && counts[0] != 4) || len(ids) != counts[0] || kind != "`mythology enchant`" {
		return nil, "", errMythologyShape
	}
	seen := map[int]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, "", errMythologyShape
		}
		seen[id] = true
	}
	identity := []string{hash, m.typeHash, m.indexHash}
	var options [][]byte
	for _, id := range ids {
		out, err := m.option(id)
		if err != nil {
			return nil, "", err
		}
		options = append(options, out.variants)
		identity = append(identity, fmt.Sprintf("%d:%s", id, out.hash))
	}
	return options, hashText(strings.Join(identity, "\n")), nil
}
