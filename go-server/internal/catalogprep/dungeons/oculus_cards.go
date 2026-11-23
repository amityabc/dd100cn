package dungeons

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"math"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/pvftext"
)

const specialRewardPath = "etc/dungeonspecialreward.etc"
const oculusRewardPolicy = "LocalOculusResourceReward/v2"

type OculusCards struct {
	DungeonId, CounterDungeonId uint32
	Limit, CycleRaw, RoundCount byte
	ResourceHash                string
	Rows                        []oculusRewardRow
	Containers                  map[uint32]oculusContainer
	Templates                   map[uint32]json.RawMessage
}

type oculusRewardPool struct {
	Kind, Difficulty byte
	PartyKindRaw     int
}
type oculusRewardRow struct {
	Index      int
	Container  uint32
	Pool       oculusRewardPool
	Weight     uint32
	NoticeRaw  int
	Count      uint32
	RawColumns []int
}
type oculusContainer struct {
	Id                 uint32
	Path, ResourceHash string
	Groups             []oculusRewardGroup
}
type oculusRewardGroup struct {
	DrawCount byte
	Choices   []oculusRewardChoice
}
type oculusRewardChoice struct{ TemplateId, Weight, Count uint32 }

type oculusCardReader struct {
	items     *commerce.Items
	data      *OculusCards
	cache     map[uint32]commerce.ItemSource
	templates map[uint32]*commerce.ItemTemplate
	digest    hash.Hash
}

func (s *catalogSource) oculusCards(items *commerce.Items, defs []Definition) (map[uint32]OculusCards, error) {
	text, err := s.a.ReadText(specialRewardPath, budget)
	if err != nil {
		return nil, err
	}
	shares := map[int]int{}
	for _, block := range pvftext.Blocks(text, "dungeon special reward count share") {
		v, err := pvftext.Integers(block)
		if err != nil || len(v)%2 != 0 {
			return nil, fmt.Errorf("incomplete Oculus special counter share")
		}
		for i := 0; i < len(v); i += 2 {
			if v[i] <= 0 || v[i+1] <= 0 || shares[v[i]] != 0 {
				return nil, fmt.Errorf("invalid or repeated Oculus shared counter")
			}
			shares[v[i]] = v[i+1]
		}
	}
	if shares[400000389] != 100000007 || shares[100002669] != 100000007 || shares[100000007] != 0 {
		return nil, fmt.Errorf("Oculus three modes lack their canonical counter")
	}
	body, err := pvftext.Section(text, "dungeon special reward add")
	if err != nil {
		return nil, err
	}
	byID := map[int][]int{}
	for _, block := range pvftext.Blocks(body, "dungeon") {
		fields := strings.Fields(block)
		if len(fields) == 0 {
			continue
		}
		id := int(uintToken(fields[0], 31))
		if !slices.ContainsFunc(defs, func(d Definition) bool { return int(d.ID) == id }) {
			continue
		}
		if byID[id] != nil {
			return nil, fmt.Errorf("Oculus %d reward row duplicated", id)
		}
		row, err := pvftext.Integers(block)
		if err != nil {
			return nil, err
		}
		byID[id] = row
	}
	doubled, err := pvftext.Numbers(text, "double reward dungeon list")
	if err != nil || len(doubled) > 1024 {
		return nil, fmt.Errorf("invalid Oculus double reward list")
	}
	sorted := slices.Clone(doubled)
	slices.Sort(sorted)
	if len(sorted) != len(slices.Compact(sorted)) || len(doubled) > 0 && slices.Min(doubled) <= 0 {
		return nil, fmt.Errorf("invalid Oculus double-round mode identity")
	}
	data := map[uint32]OculusCards{}
	cache := map[uint32]commerce.ItemSource{}
	for _, d := range defs {
		z := OculusCards{DungeonId: d.ID, CounterDungeonId: 100000007, Limit: 1, CycleRaw: 1, RoundCount: 1,
			Containers: map[uint32]oculusContainer{}, Templates: map[uint32]json.RawMessage{}}
		if slices.Contains(doubled, int(d.ID)) {
			z.RoundCount = 2
		}
		v := byID[int(d.ID)]
		z.Rows, err = oculusRewardRows(v)
		if err != nil {
			return nil, fmt.Errorf("Oculus rewards %d: %w", d.ID, err)
		}
		r := oculusCardReader{items: items, data: &z, cache: cache, templates: map[uint32]*commerce.ItemTemplate{}, digest: sha256.New()}
		for _, value := range []string{oculusRewardPolicy, fmt.Sprint(d.ID), text} {
			addLengthText(r.digest, value)
		}
		for _, row := range z.Rows {
			if _, ok := z.Containers[row.Container]; ok {
				continue
			}
			container, err := r.container(row.Container)
			if err != nil {
				return nil, err
			}
			z.Containers[row.Container] = container
		}
		addLengthText(r.digest, d.ResourceHash)
		z.ResourceHash = fmt.Sprintf("%X", r.digest.Sum(nil))
		data[d.ID] = z
	}
	return data, nil
}

func oculusRewardRows(values []int) ([]oculusRewardRow, error) {
	if len(values) < 4 || values[1] != 0 || values[2] != 1 || values[3] != 1 {
		return nil, fmt.Errorf("aliases/limit/cycleRaw changed from 0/1/1")
	}
	total := map[oculusRewardPool]uint64{}
	var out []oculusRewardRow
	for i := 4; i < len(values); {
		start := i
		if len(values)-i < 6 || len(out) >= 128 {
			return nil, fmt.Errorf("truncated or oversized source row")
		}
		item, kind, difficulty, weight, notice, count := values[i], values[i+1], values[i+2], values[i+3], values[i+4], values[i+5]
		i += 6
		if item <= 0 || kind < 0 || kind > 5 || difficulty != 4 || weight < 0 || notice < 0 || notice > 2 || count != 1 {
			return nil, fmt.Errorf("unsupported original columns")
		}
		party := 0
		if kind == 1 {
			if i == len(values) || values[i] < 0 || values[i] > 255 {
				return nil, fmt.Errorf("partyKindRaw missing or outside native width")
			}
			party = values[i]
			i++
		}
		key := oculusRewardPool{byte(kind), byte(difficulty), party}
		total[key] += uint64(weight)
		out = append(out, oculusRewardRow{Index: len(out), Container: uint32(item), Pool: key, Weight: uint32(weight),
			NoticeRaw: notice, Count: uint32(count), RawColumns: values[start:i]})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty weighted pool")
	}
	for _, sum := range total {
		if sum == 0 || sum > math.MaxUint32 {
			return nil, fmt.Errorf("empty or unbounded weighted pool")
		}
	}
	return out, nil
}

func (r *oculusCardReader) read(id uint32) (commerce.ItemSource, error) {
	src, ok := r.cache[id]
	if !ok {
		var err error
		src, err = r.items.Source(id)
		if err != nil {
			return src, fmt.Errorf("Oculus dependency %d: %w", id, err)
		}
		r.cache[id] = src
	}
	for _, value := range []string{src.Path, src.Hash, src.Text} {
		addLengthText(r.digest, value)
	}
	return src, nil
}

func (r *oculusCardReader) template(id uint32) (*commerce.ItemTemplate, error) {
	if t, ok := r.templates[id]; ok {
		return t, nil
	}
	if len(r.templates) >= 4096 {
		return nil, fmt.Errorf("Oculus reward template graph exceeds capacity")
	}
	src, err := r.read(id)
	if err != nil {
		return nil, err
	}
	random, err := pvftext.Section(src.Text, "random option")
	if err != nil || src.Equipment && random != "" && random != "0" {
		return nil, fmt.Errorf("Oculus reward equipment %d requires unsupported random detail", id)
	}
	t, err := src.Gift()
	if err != nil || t == nil || t.FateCollection || t.AccountCommonInventory {
		return nil, fmt.Errorf("Oculus reward template %d cannot be granted: %v", id, err)
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	r.templates[id] = t
	r.data.Templates[id] = raw
	return t, nil
}

var rewardETCBlock = regexp.MustCompile(`(?ms)^\[etc\].*?^\[/etc\]`)

func (r *oculusCardReader) container(id uint32) (oculusContainer, error) {
	src, err := r.read(id)
	if err != nil {
		return oculusContainer{}, err
	}
	out := oculusContainer{Id: id, Path: src.Path, ResourceHash: src.Hash}
	kind, err := pvftext.Section(src.Text, "stackable type")
	if err != nil || src.Equipment || kind != "`[booster]` 0" {
		return out, fmt.Errorf("Oculus reward container %d lacks source booster type", id)
	}
	body, err := pvftext.Section(src.Text, "booster info")
	if err != nil {
		return out, err
	}
	groupSpecs := pvftext.Blocks(body, "etc")
	if len(groupSpecs) == 0 || len(groupSpecs) > 16 || strings.TrimSpace(rewardETCBlock.ReplaceAllString(body, "")) != "" {
		return out, fmt.Errorf("Oculus container %d has unsupported group grammar", id)
	}
	draws := 0
	for _, group := range groupSpecs {
		values, err := pvftext.Integers(group)
		if err != nil || len(values) < 4 || len(values) > 12289 || (len(values)-1)%3 != 0 || values[0] < 1 || values[0] > 255 {
			return out, fmt.Errorf("Oculus container %d has unsupported draw group", id)
		}
		newGroup := oculusRewardGroup{DrawCount: byte(values[0])}
		draws += values[0]
		sum := uint64(0)
		for i := 1; i < len(values); i += 3 {
			item, w, count := values[i], values[i+1], values[i+2]
			if item <= 0 || w < 0 || count < 1 || count > 1000000 {
				return out, fmt.Errorf("Oculus container %d has invalid item/weight/quantity", id)
			}
			t, err := r.template(uint32(item))
			if err != nil {
				return out, err
			}
			if t.Kind != 2 && count != 1 || uint32(count) > t.StackLimit {
				return out, fmt.Errorf("Oculus item %d cannot grant source count %d", item, count)
			}
			sum += uint64(w)
			newGroup.Choices = append(newGroup.Choices, oculusRewardChoice{uint32(item), uint32(w), uint32(count)})
		}
		if sum == 0 || sum > math.MaxUint32 {
			return out, fmt.Errorf("Oculus container %d has empty or unbounded draw", id)
		}
		out.Groups = append(out.Groups, newGroup)
	}
	if draws > 256 {
		return out, fmt.Errorf("Oculus container %d exceeds total draw capacity", id)
	}
	return out, nil
}
