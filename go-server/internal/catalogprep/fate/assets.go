package fate

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	gameplay "s9a1/server/internal/fate"
	"s9a1/server/internal/opening"
)

func (s *source) assets(r *Catalog, first, second string) {
	equips := s.numbers(s.block(first, "equip index info"))
	if !s.require(len(equips) == 48, "activity requires twenty-four collection/equipment pairs") {
		return
	}
	seen := map[uint32]bool{}
	effects := map[int]bool{}
	for at := 0; at < len(equips); at += 2 {
		item, effect := s.id(equips[at]), equips[at+1]
		eq := s.resolveText("equipment", s.id(effect))
		s.require(!effects[effect] && strings.HasPrefix(strings.ToLower(s.section(eq, "equipment type", false)), "`[wrist]`") && has(eq, "disable random drop"), "invalid or duplicate activity wrist effect")
		effects[effect] = true
		r.Collection = append(r.Collection, gameplay.CollectionItem{EffectId: int32(effect), Slot: uint16(at / 2), Template: s.item(item, true, false, seen)})
		r.ShopProducts = append(r.ShopProducts, int(item))
	}
	slices.Sort(r.ShopProducts)
	rows := sections(s.block(first, "dungeon clear reward"), "reward phase info")
	if !s.require(len(rows) == 5, "activity phase reward list is incomplete") {
		return
	}
	ids := make([]uint32, len(rows))
	for i, row := range rows {
		values := s.numbers(row)
		if !s.require(len(values) == 2 && values[1] > 0, "invalid activity phase reward") {
			return
		}
		ids[i] = s.id(values[0])
		r.Rewards = append(r.Rewards, gameplay.Reward{Template: s.item(ids[i], false, false, seen), Count: uint32(values[1])})
	}
	common := map[uint32]bool{}
	for _, id := range ids {
		body := s.resolveText("stackable", id)
		grant := s.numbers(s.block(s.block(body, "booster info"), "etc"))
		if !s.require(len(grant) == 4 && grant[0] == 1 && grant[2] == 1000 && grant[3] > 0 && s.scalar(body, "hide booster info") == 1, "unsupported activity stage box policy") {
			return
		}
		target := s.id(grant[1])
		if !common[target] {
			s.item(target, false, true, seen)
			common[target] = true
		}
	}
	for _, values := range [][]int{s.numbers(s.section(first, "bonus item", false)), s.numbers(s.section(second, "reward storybook", false))} {
		if !s.require(len(values) == 2 && values[1] > 0, "invalid bonus or storybook reference") {
			return
		}
		s.resolveText("stackable", s.id(values[0]))
	}
}

// activityItem matches the LocalItemTemplate serialized into durable activity
// claims. The live event's reviewed reopening policy leaves expiry unset.
type activityItem struct {
	TemplateId                                                                                                        uint32
	Kind                                                                                                              byte
	FirstBagSlot, LastBagSlot, WornSlot                                                                               uint16
	StackLimit                                                                                                        uint32
	MaximumDurability                                                                                                 uint16
	MinimumLevel                                                                                                      byte
	UsableJobs                                                                                                        uint16
	ResourceHash                                                                                                      string
	WornSlotIsNative, CashPackage, CashPackageReward, PvfGiftItem, AccountAfterFirstTrade, MonsterCard, InitialSealed bool
	ResourceExpiration                                                                                                *int64
	FateCollection, AccountCommonInventory, PetConsumable, CharmEnergy                                                bool
	WeaponQualification                                                                                               struct {
		Verified bool
		Jobs     []uint16
		Valid    bool
	}
	RelativeLifetimeSeconds    uint32
	DailyDeletion              bool
	WeeklyDeletionDay          *byte
	FatigueTimeMonthlyDeletion bool
}

func (s *source) item(id uint32, collection, common bool, seen map[uint32]bool) json.RawMessage {
	text := s.resolveText("stackable", id)
	path := s.resolve("stackable", id)
	expected := "`[booster]` 0"
	lo, hi := uint16(65), uint16(120)
	if collection || common {
		lo, hi = 121, 176
		expected = "`[material]` 0"
		if collection {
			expected = "`[material]` 1"
		}
	}
	s.require(s.section(text, "stackable type", false) == expected && s.section(text, "attach type", false) == "`[account]`" &&
		strings.TrimSpace(s.block(text, "usable job")) == "`[all]`", "unsupported activity item ownership, type or job policy")
	if collection {
		s.require(s.scalar(text, "stack in account inventory") == 1, "collection item lacks its account marker")
	}
	if common {
		tag := "stack in account common item inventory"
		s.require(has(text, tag) && s.section(text, tag, false) == "", "activity material lacks its common inventory marker")
	}
	_, err := time.Parse("2006-01-02 15:04:05", s.quoted(s.section(text, "expiration date", false)))
	s.require(err == nil && !seen[id], "invalid or repeated activity item expiry")
	seen[id] = true
	minimum := s.scalar(text, "minimum level")
	s.require(minimum > 0 && minimum <= 255, "invalid activity item minimum level")
	stack := uint32(math.MaxUint32)
	if limit := s.section(text, "stack limit", true); collection {
		stack = 1
	} else if limit != "" {
		stack = s.id(s.one(limit))
	}
	if common {
		stack = min(stack, math.MaxInt32)
	}
	row := activityItem{TemplateId: id, Kind: 2, FirstBagSlot: lo, LastBagSlot: hi, WornSlot: math.MaxUint16, StackLimit: stack,
		MinimumLevel: byte(minimum), UsableJobs: math.MaxUint16, ResourceHash: s.hash(path), FateCollection: collection, AccountCommonInventory: common}
	row.WeaponQualification.Jobs = []uint16{}
	row.WeaponQualification.Valid = true
	raw, err := json.Marshal(row)
	s.require(err == nil, "invalid activity item template")
	return raw
}

var gatePoint = regexp.MustCompile("`\\[gate\\]`\\s+(\\d+)\\s+(\\d+)")

func (s *source) town(r *Catalog, dgn string) {
	areas := blocks(s.resolveText("town", 135), "area")
	if !s.require(len(areas) == 1, "activity town must have one area") {
		return
	}
	mapPath := s.areaMap(areas[0], "0")
	campMap := s.text(mapPath)
	gate := s.role(areas[0], "dungeon gate")
	s.require(s.scalar(areas[0], "need level") == int(r.MinimumLevel) && gate == 261, "invalid activity camp gate or level")
	world := s.resolveText("worldmap", s.id(gate))
	s.require(len(s.numbers(s.block(world, "dungeon")))%2 == 0, "invalid activity worldmap directory rows")
	npcs := s.block(campMap, "NPC")
	s.require(strings.Contains(npcs, "400001286") && strings.Contains(npcs, "400001287"), "activity camp NPC is missing")
	s.resolveText("npc", 400001287)
	quest := s.one(s.block(areas[0], "need quest"))
	s.require(quest == 8884, "unsupported activity camp entry quest")
	s.resolveText("quest", s.id(quest))
	var room string
	for _, area := range blocks(s.resolveText("town", 38), "area") {
		head, _, _ := strings.Cut(area, "[permission]")
		if rows := tokens(head); len(rows) > 0 && rows[0] == "1" {
			s.require(room == "", "ambiguous Seria return area")
			room = area
		}
	}
	path := s.areaMap(room, "1")
	if !strings.HasPrefix(path, "map/") {
		path = "map/" + path
	}
	s.text(path)
	p := gatePoint.FindAllStringSubmatch(room, -1)
	if !s.require(len(p) == 1, "Seria return point missing or ambiguous") {
		return
	}
	r.Town = TownRoute{Camp: opening.Point{Town: 135, Area: 0, X: 454, Y: 207, Direction: 5},
		Seria:    opening.Point{Town: 38, Area: 1, X: s.coordinate(s.integer(p[0][1])), Y: s.coordinate(s.integer(p[0][2])), Direction: 5},
		EntryNpc: 400001285, CampNpc: 400001286, Gate: gate, EntryQuest: uint16(quest)}
	r.ShopNpc = 400001293
	entry, camp, shop := s.resolveText("npc", r.Town.EntryNpc), s.resolveText("npc", r.Town.CampNpc), s.resolveText("npc", r.ShopNpc)
	event := s.scalar(entry, "event id")
	s.require(event == int(gameplay.Event) && s.scalar(entry, "open visible event") == event && s.scalar(camp, "open visible event") == event,
		"activity NPC event identity mismatch")
	s.require(s.role(entry, "enter town") == int(r.Town.CampNpc) && s.role(camp, "exit town") == 0, "activity NPC route mismatch")
	found := false
	for _, id := range s.numbers(s.section(shop, "int data", false)) {
		found = found || id == event
	}
	s.require(s.role(shop, "open link event popup") == 0 && strings.Contains(shop, "`Decisions unto Destiny Shop`") && found, "activity shop does not identify its source event")
	r.EventId = s.id(event)
	buff := s.block(dgn, "instance buff")
	levels := s.numbers(s.section(buff, "apply level", false))
	r.InstanceBuff = s.scalar(buff, "character variouse buff")
	if s.require(r.InstanceBuff == 1149 && len(levels) == 2 && levels[0] == 1 && levels[1] == 100, "activity instance buff definition mismatch") {
		r.BuffMinimumLevel, r.BuffMaximumLevel = byte(levels[0]), byte(levels[1])
	}
}

func (s *source) areaMap(body, id string) string {
	head, _, _ := strings.Cut(body, "[permission]")
	rows := tokens(head)
	if !s.require(len(rows) == 2 && rows[0] == id, "invalid activity town area header") {
		return ""
	}
	return s.path(rows[1])
}

func (s *source) role(body, tag string) int {
	p := regexp.MustCompile("`\\["+regexp.QuoteMeta(tag)+"\\]`\\s+(\\d+)").FindAllStringSubmatch(body, -1)
	if !s.require(len(p) == 1, "missing or ambiguous activity NPC role "+tag) {
		return 0
	}
	return s.integer(p[0][1])
}
