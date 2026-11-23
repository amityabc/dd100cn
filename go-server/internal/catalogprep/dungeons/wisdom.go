package dungeons

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/storage"
)

type WisdomProduct struct {
	Id       uint32
	Template json.RawMessage
	Weight   uint32
}
type WisdomData struct {
	Hash          string
	PillarRewards bool
	Products      []WisdomProduct
	Materials     map[uint32]json.RawMessage
}

func BuildWisdom(a Archive, xp *Experience, boss *dungeon.BossEquipmentCatalog) (Layouts, *WisdomData, error) {
	s, err := newCatalogSource(a, xp, nil, nil)
	if err != nil {
		return Layouts{}, nil, err
	}
	data, err := BuildWisdomResources(a, boss)
	if err != nil {
		return Layouts{}, nil, err
	}
	layouts, err := s.wisdomLayouts(data.Hash)
	return layouts, data, err
}

func BuildWisdomResources(a Archive, boss *dungeon.BossEquipmentCatalog) (*WisdomData, error) {
	items, err := commerce.NewItems(a)
	if err != nil {
		return nil, err
	}
	return wisdomRewards(a, items, boss)
}

var (
	wisdomDropArea = regexp.MustCompile(`(?s)\[drop area\]\s*(\d+)(.*?)\[/drop area\]`)
	wisdomDirect   = regexp.MustCompile(`0\s+(109010632|109010633)\s+(\d+)\s+((?:-?\d+\s+){13}-?\d+)`)
	wisdomGroups   = regexp.MustCompile("(?ms)^\\[group index\\]\\s*(\\d+)\\s*^\\[group\\]\\s*`A`\\s*(.*?)^\\[/group\\]")
	wisdomBossMap  = regexp.MustCompile("`boss`\\s+1\\s+0\\s+100000222\\s+100000491\\s+100000492\\s+100000493")
)

func wisdomRewards(a Archive, items *commerce.Items, boss *dungeon.BossEquipmentCatalog) (*WisdomData, error) {
	if boss == nil || len(boss.Hash) != 64 {
		return nil, fmt.Errorf("Wisdom requires prepared boss equipment pools")
	}
	table, err := a.ReadText(hellItemPath, budget)
	if err != nil {
		return nil, err
	}
	var pool []int
	areas := 0
	for _, area := range wisdomDropArea.FindAllStringSubmatch(table, -1) {
		if area[1] != "78" {
			continue
		}
		areas++
		for _, body := range pvftext.Blocks(area[2], "drop epic item list") {
			row, err := pvftext.Integers(body)
			if err != nil || len(row) == 0 {
				return nil, fmt.Errorf("invalid Wisdom item pool")
			}
			if row[0] == 1 {
				if pool != nil {
					return nil, fmt.Errorf("multiple Wisdom product pools")
				}
				pool = row
			}
		}
	}
	if areas != 1 || len(pool) < 3 || len(pool)%2 != 1 {
		return nil, fmt.Errorf("incomplete Wisdom area78 product pool")
	}
	independent, err := a.ReadText("etc/independent_drop.etc", budget)
	if err != nil {
		return nil, err
	}
	want := []int{1000000, 1000000, 1000000, 1000000, 1000000, 1, 2, 3, 4, 1, 1, 255, -1, 0}
	direct := map[string]bool{}
	for _, at := range wisdomDirect.FindAllStringSubmatchIndex(independent, -1) {
		if at[0] > 0 && independent[at[0]-1] >= '0' && independent[at[0]-1] <= '9' ||
			at[1] < len(independent) && independent[at[1]] >= '0' && independent[at[1]] <= '9' {
			continue
		}
		row := matchFields(independent, at)
		values, err := pvftext.Integers(row[3])
		if err != nil || direct[row[1]] || row[2] != "490020105" || !slices.Equal(values, want) {
			return nil, fmt.Errorf("Wisdom direct material lacks its complete flag0 source")
		}
		direct[row[1]] = true
	}
	if len(direct) != 2 {
		return nil, fmt.Errorf("Wisdom direct materials lack two source monsters")
	}
	result := &WisdomData{Materials: map[uint32]json.RawMessage{}}
	identity := []string{dungeon.WisdomPolicy, boss.Hash, hashText(table), hashText(independent)}
	seen := map[uint32]bool{}
	for i := 1; i < len(pool); i += 2 {
		id, w := uint32(pool[i]), uint32(pool[i+1])
		if pool[i] <= 0 || pool[i+1] <= 0 || seen[id] {
			return nil, fmt.Errorf("invalid or duplicate Wisdom product")
		}
		seen[id] = true
		src, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		tmpl, err := src.Ordinary()
		if err != nil || tmpl == nil {
			tmpl, err = src.GiftFor(gm.GiftMonster)
		}
		if err != nil || tmpl == nil || tmpl.Kind > 1 {
			return nil, fmt.Errorf("Wisdom product %d cannot be acquired: %v", id, err)
		}
		raw, err := json.Marshal(tmpl)
		if err != nil {
			return nil, err
		}
		result.Products = append(result.Products, WisdomProduct{id, raw, w})
		identity = append(identity, fmt.Sprintf("%d:%d:%s", id, w, tmpl.ResourceHash))
	}
	materialHashes := map[uint32]string{}
	for _, id := range []uint32{10313459, 10312874, 10158124, 10313659, 10313633, 490020105} {
		src, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		tmpl, err := src.Ordinary()
		if err != nil || tmpl == nil || id == 490020105 {
			tmpl, err = src.GiftFor(gm.GiftMonster)
		}
		if err != nil || tmpl == nil || tmpl.Kind != 2 {
			return nil, fmt.Errorf("Wisdom material %d cannot be acquired: %v", id, err)
		}
		raw, err := json.Marshal(tmpl)
		if err != nil {
			return nil, err
		}
		result.Materials[id] = raw
		materialHashes[id] = tmpl.ResourceHash
	}
	for _, id := range slices.Sorted(maps.Keys(materialHashes)) {
		identity = append(identity, fmt.Sprintf("%d:%s", id, materialHashes[id]))
	}
	identity = append(identity, "ReferenceMaterials:fragments=7,10;ticket=0,2+500000/1000000;soul=10000;remnant=1000;crystal=6000")
	result.Hash = hashText(strings.Join(identity, "\n"))
	return result, nil
}

func (s *catalogSource) wisdomLayouts(equipmentHash string) (Layouts, error) {
	var out Layouts
	world, err := s.worldText(int(dungeon.WisdomGate))
	if err != nil {
		return out, err
	}
	selectors, err := worldSelectors(world)
	if err != nil {
		return out, err
	}
	matches := 0
	for _, row := range selectors {
		if row.Dungeon == dungeon.WisdomDungeon {
			if row.InProgress || row.Quest != 12177 {
				return out, fmt.Errorf("Wisdom world selector qualification changed")
			}
			matches++
		}
	}
	if matches != 1 {
		return out, fmt.Errorf("Wisdom requires one original selector")
	}
	dgn, err := s.dungeonText(dungeon.WisdomDungeon)
	if err != nil {
		return out, err
	}
	freePasses, err := s.a.ReadText("etc/freepassitem.etc", budget)
	if err != nil {
		return out, err
	}
	hell, err := s.a.ReadText("etc/hellparty.etc", budget)
	if err != nil {
		return out, err
	}
	p := &input{}
	if !p.require(slices.Equal(p.numbers(p.field(dgn, "hell party required item count add", true)), []int64{-2, 18}) &&
		slices.Equal(p.numbers(p.field(dgn, "difficulty level", true)), []int64{1, 1, 0, 0, 0}) &&
		p.scalar(dgn, "minimum required level", 0) == 96 && p.scalar(dgn, "minimum enter fatigue", 0) == 1 &&
		tagPresent(dgn, "disable clear reward") && p.scalar(dgn, "reward card", -1) == 0 && p.scalar(dgn, "result board", -1) == 0,
		"Wisdom qualification, entry cost or result policy differs from PVF") {
		return out, p.err
	}
	values := p.numbers(p.field(freePasses, "hell guidance of wisdom freepass item", true))
	if !p.require(len(values) > 0 && len(values)%2 == 0 && len(values) <= 1024, "Wisdom free pass table shape changed") {
		return out, p.err
	}
	var passes []any
	seen := map[uint32]bool{}
	for i := 0; i < len(values); i += 2 {
		id := p.id(values[i])
		p.require(id != 1<<32-1 && values[i+1] == 1, "invalid Wisdom free pass")
		if !seen[id] {
			passes = append(passes, storage.WisdomPass{Item: id, Count: 1})
		}
		seen[id] = true
	}
	groups := map[int][]uint32{}
	for _, group := range wisdomGroups.FindAllStringSubmatch(hell, -1) {
		index := int(p.number(group[1], 32))
		if index < 225 || index > 231 {
			continue
		}
		row := p.numbers(group[2])
		if !p.require(groups[index] == nil && len(row) > 0 && len(row)%2 == 0, "Wisdom hell party requires complete monster groups") {
			return out, p.err
		}
		for i := 0; i < len(row); i += 2 {
			p.require(row[i+1] == 0 && s.monsters[row[i]] != "", "Wisdom hell party has unknown monster or kind")
			groups[index] = append(groups[index], p.id(row[i]))
		}
	}
	if !p.require(len(groups) == 7 && wisdomBossMap.MatchString(dgn), "Wisdom native group or boss variants changed") || p.err != nil {
		return out, p.err
	}
	for _, mapID := range []uint32{100000222, 100000491, 100000492, 100000493} {
		text, err := s.readMap(mapID, false)
		if err != nil {
			return out, err
		}
		_, tail, ok := strings.Cut(p.field(text, "special passive object", true), "`[hellparty]`")
		tail, _, _ = strings.Cut(tail, "`")
		expected := []int64{225, 50, 1, 226, 50, 1, 227, 50, 2, 228, 50, 2, 229, 100, 3, 230, 0, 3, 231, 100, 8}
		if !p.require(ok && slices.Equal(p.numbers(tail), expected), "Wisdom pillar phases or weights changed") {
			return out, p.err
		}
		prepared := wisdomBossMap.ReplaceAllString(dgn, fmt.Sprintf("`boss` 1 0 %d", mapID))
		base, err := s.definition(dungeon.WisdomDungeon, prepared, 0, layoutOptions{mainline: true})
		if err != nil {
			return out, err
		}
		i := slices.IndexFunc(base.Rooms, func(r opening.Room) bool { return r.X == 1 && r.Y == 0 })
		if !p.require(i >= 0 && base.EntryFatigueCost == 8 && base.RoomFatigueCost == 0 && base.meta.hell && len(base.Start.Monsters) == 0,
			"Wisdom entry fatigue or start room differs from source") {
			return out, p.err
		}
		for phase1 := 225; phase1 <= 226; phase1++ {
			for phase2 := 227; phase2 <= 228; phase2++ {
				for phase3 := 229; phase3 <= 230; phase3++ {
					d := base
					d.Rooms = slices.Clone(base.Rooms)
					r := &d.Rooms[i]
					r.Monsters = slices.Clone(r.Monsters)
					for _, phase := range []struct{ number, group int }{{1, phase1}, {2, phase2}, {3, phase3}, {8, 231}} {
						var typ byte
						if phase.number == 3 {
							typ = 3
						}
						for _, code := range groups[phase.group] {
							r.Monsters = append(r.Monsters, opening.Monster{Code: code, Level: 135, Type: typ, BlocksRoom: true, HellPhase: uint16(phase.number)})
						}
					}
					d.EntryTicketCount, d.WisdomPasses, d.meta.selectable, d.ResourceVariant = 18, passes, []byte{0, 1}, uint32(len(out.Definitions))
					d.ResourceHash = hashText(base.ResourceHash + "\n" + hell + "\n" + equipmentHash + "\n" + freePasses + fmt.Sprintf("\n%d:%d:%d", phase1, phase2, phase3))
					row, err := s.layout(dungeon.WisdomGate, d, "wisdom", nil, []uint16{12177}, nil, nil)
					if err != nil {
						return out, err
					}
					out.Definitions = append(out.Definitions, row)
				}
			}
		}
	}
	return out, nil
}
