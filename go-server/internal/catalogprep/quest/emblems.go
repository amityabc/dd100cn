package quest

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/pvf"
	"s9a1/server/internal/storage"
)

var socketBlock = regexp.MustCompile("(?is)\\[equipment type\\](.*?)\\[/socket(?:\\s+info)?\\]")
var socketLabel = regexp.MustCompile("(?i)\x60\\[([A-DSM]) socket\\]\x60")
var equipmentLabel = regexp.MustCompile("\x60\\[([^\\]]+)\\]\x60")
var socketBits = map[string]uint16{"A": 1, "B": 2, "C": 4, "D": 8, "S": 16, "M": 0xFFEF}
var socketEquipment = []string{"coat", "shoulder", "pants", "shoes", "waist", "amulet", "wrist", "ring", "support", "magic stone"}

type emblemSource = struct {
	Template json.RawMessage
	Grade    int
}

func loadEmblemSockets(a *pvf.Archive) (map[uint16][]uint16, error) {
	text, err := a.ReadText("etc/chn_equipmentsockettypelist.etc", budget)
	if err != nil {
		return nil, err
	}
	sockets := make(map[uint16][]uint16)
	for _, body := range socketBlock.FindAllStringSubmatch(text, -1) {
		label := equipmentLabel.FindStringSubmatch(body[1])
		if len(label) != 2 {
			return nil, fmt.Errorf("socket layout has no equipment label")
		}
		slot := slices.Index(socketEquipment, strings.ToLower(strings.TrimSpace(label[1])))
		if slot < 0 || sockets[uint16(slot+14)] != nil {
			return nil, fmt.Errorf("socket layout has unknown or duplicate equipment type")
		}
		var colors []uint16
		for _, match := range socketLabel.FindAllStringSubmatch(body[1], -1) {
			if key := strings.ToUpper(match[1]); key != "M" {
				colors = append(colors, socketBits[key])
			}
		}
		if len(colors) < 1 || len(colors) > 2 {
			return nil, fmt.Errorf("invalid equipment socket count")
		}
		sockets[uint16(slot+14)] = colors
	}
	if len(sockets) == 0 {
		return nil, fmt.Errorf("emblem socket type list is empty")
	}
	for i := uint16(14); i <= 23; i++ {
		sockets[i] = emptySlice(sockets[i])
	}
	return sockets, nil
}

func addEmblem(out *storage.EmblemCatalog, items ItemLookup, id uint32, text string) {
	s := readItemScript(text)
	stackable := s.get("stackable type")
	if strings.Contains(strings.ToLower(stackable), "[avatar emblem]") {
		if color := socketLabel.FindStringSubmatch(s.get("avatar emblem target type")); len(color) == 2 {
			out.Colors[id] = socketBits[strings.ToUpper(color[1])]
		}
	}
	if stackable != "\x60[avatar emblem]\x60 0" {
		return
	}
	grade := s.number("grade", 0, math.MaxInt32, true)
	if s.err != nil {
		return
	}
	src, err := items.Source(id)
	if err != nil || src.Equipment {
		return
	}
	template, err := src.Gift()
	if err != nil || template == nil || template.FirstBagSlot != 289 || template.LastBagSlot != 360 || template.Kind != 2 {
		return
	}
	if b, err := json.Marshal(template); err == nil {
		out.Compound.Sources[id] = emblemSource{b, int(grade)}
	}
}

func loadEmblemGraph(a *pvf.Archive, out *storage.EmblemCatalog, items ItemLookup) error {
	config, err := a.ReadText("etc/emblemcompound_chn.etc", budget)
	if err != nil {
		return err
	}
	s := readItemScript(config)
	rows := s.numbers(s.get("emblem compound info"))
	if s.err != nil {
		return s.err
	}
	roots := make(map[int]map[int]uint32)
	for at := 0; at < len(rows); {
		if at+2 > len(rows) || rows[at] > math.MaxInt32 || rows[at+1] < 2 || rows[at+1] > 5 {
			return fmt.Errorf("invalid emblem recipe header")
		}
		grade, count := int(rows[at]), int(rows[at+1])
		at += 2
		if at+count-1 > len(rows) {
			return fmt.Errorf("incomplete sorted emblem recipe")
		}
		if roots[grade] == nil {
			roots[grade] = make(map[int]uint32)
		}
		for i := 2; i <= count; i++ {
			if _, dup := roots[grade][i]; dup {
				return fmt.Errorf("duplicate emblem compound recipe")
			}
			roots[grade][i] = rows[at]
			at++
		}
	}
	if len(roots) == 0 {
		return fmt.Errorf("no emblem compound recipes")
	}
	var rootIDs []uint32
	for _, grades := range roots {
		for _, root := range grades {
			rootIDs = append(rootIDs, root)
		}
	}
	nodes, err := readRewardGraph(items, rootIDs, true)
	if err != nil {
		return err
	}
	out.Compound.Nodes = nodes
	out.Compound.Roots = make(map[int]map[int]uint32)
	for _, source := range out.Compound.Sources {
		if root := roots[source.Grade]; root != nil {
			out.Compound.Roots[source.Grade] = root
		}
	}
	return nil
}

func readRewardGraph(items ItemLookup, roots []uint32, material bool) (map[uint32]storage.RewardGraphNode, error) {
	nodes := make(map[uint32]storage.RewardGraphNode)
	visiting := make(map[uint32]bool)
	var load func(uint32, int) error
	load = func(id uint32, depth int) error {
		if visiting[id] || depth > 8 || len(nodes) >= 32768 {
			return fmt.Errorf("cyclic or oversized reward graph")
		}
		if _, exists := nodes[id]; exists {
			return nil
		}
		visiting[id] = true
		defer delete(visiting, id)
		src, err := items.Source(id)
		if err != nil {
			return err
		}
		item := readItemScript(src.Text)
		switch item.get("stackable type") {
		case "\x60[booster]\x60 0", "\x60[booster]\x60 1", "\x60[booster random]\x60 0":
			pools, err := readRewardPools(item, func(id, count uint32) (storage.GiftReward, error) {
				if err := load(id, depth+1); err != nil {
					return storage.GiftReward{}, err
				}
				return storage.GiftReward{TemplateID: id, Count: count, Template: nodes[id].Leaf, Upgrades: []storage.GiftUpgrade{}}, nil
			})
			if err != nil {
				return err
			}
			nodes[id] = storage.RewardGraphNode{Pools: pools}
		case "\x60[avatar emblem]\x60 0", "\x60[material]\x60 0":
			if !material && item.get("stackable type") == "\x60[material]\x60 0" {
				return fmt.Errorf("avatar disassembly reward must be an emblem")
			}
			t, err := src.Gift()
			if err != nil {
				return err
			}
			if _, err := src.Weight(); err != nil {
				return err
			}
			raw, err := json.Marshal(t)
			if err != nil {
				return err
			}
			nodes[id] = storage.RewardGraphNode{Leaf: raw, Pools: []storage.GiftPool{}}
		default:
			return fmt.Errorf("unsupported emblem compound reward %d", id)
		}
		return item.err
	}
	for _, root := range roots {
		if err := load(root, 0); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

type giftPoolEntry = struct {
	Weight uint64
	Reward storage.GiftReward
}

var conditionalRewardJob = regexp.MustCompile("^\x60\\[([^\\]]+)\\]\x60\\s+")

func readRewardPools(s *itemScript, resolve func(uint32, uint32) (storage.GiftReward, error)) ([]storage.GiftPool, error) {
	start := slices.IndexFunc(s.fields, func(f giftField) bool { return f.name == "booster info" })
	end := slices.IndexFunc(s.fields, func(f giftField) bool { return f.name == "/booster info" })
	if start < 0 || end <= start {
		return nil, fmt.Errorf("incomplete reward booster")
	}
	fields := s.fields[start+1 : end]
	var pools []storage.GiftPool
	total := 0
	for i, field := range fields {
		if strings.HasPrefix(field.name, "/") {
			if field.value != "" {
				return nil, fmt.Errorf("reward pool has trailing data")
			}
			continue
		}
		if len(pools) >= 4096 || i+1 >= len(fields) || fields[i+1].name != "/"+field.name {
			return nil, fmt.Errorf("reward pool boundary is invalid")
		}
		part := field.value
		jobMask, header := uint16(math.MaxUint16), 1
		conditional := anyOf(field.name, "charactor", "grow type")
		if conditional {
			job := conditionalRewardJob.FindStringSubmatch(part)
			if len(job) != 2 {
				return nil, fmt.Errorf("reward pool has invalid job condition")
			}
			if job[1] != "all" {
				j := slices.Index(jobNames, job[1])
				if j < 0 {
					return nil, fmt.Errorf("reward pool has unknown job")
				}
				jobMask = 1 << j
			}
			part = part[len(job[0]):]
			header = 3
			if field.name == "grow type" {
				header = 4
			}
		}
		values := s.numbers(part)
		if len(values) < header || values[header-1] == 0 || values[header-1] > 255 {
			return nil, fmt.Errorf("incomplete reward pool header")
		}
		pool := storage.GiftPool{DrawCount: int(values[header-1]), UsableJobs: jobMask, MinimumLevel: 1, MaximumLevel: 255}
		if conditional {
			if values[0] == 0 || values[0] > 255 || values[1] < values[0] || values[1] > 255 || header == 4 && values[2] > 255 {
				return nil, fmt.Errorf("invalid reward pool level range")
			}
			pool.MinimumLevel, pool.MaximumLevel = byte(values[0]), byte(values[1])
			if header == 4 {
				grow := byte(values[2])
				pool.GrowType = &grow
			}
		}
		width := 3
		switch field.name {
		case "broadcast":
			width = 4
		case "etc", "equipment", "stackable", "creature", "cera", "emblem", "booster", "charactor", "grow type":
		default:
			return nil, fmt.Errorf("unknown reward pool %q", field.name)
		}
		if len(values) == header || (len(values)-header)%width != 0 {
			return nil, fmt.Errorf("invalid reward pool row width")
		}
		var weight uint64
		for row := range slices.Chunk(values[header:], width) {
			total++
			if row[0] == math.MaxUint32 || row[2] == 0 || total > 32768 || width == 4 && row[3] > 1 {
				return nil, fmt.Errorf("invalid reward pool entry")
			}
			reward, err := resolve(row[0], row[2])
			if err != nil {
				return nil, err
			}
			reward.Broadcast = width == 4 && row[3] != 0
			pool.Entries = append(pool.Entries, giftPoolEntry{uint64(row[1]), reward})
			weight += uint64(row[1])
		}
		if weight == 0 {
			return nil, fmt.Errorf("reward pool has no positive weight")
		}
		pools = append(pools, pool)
	}
	if len(pools) == 0 {
		return nil, fmt.Errorf("reward booster has no complete pools")
	}
	return pools, s.err
}
