package commerce

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/storage"
)

type cashStage struct {
	sku, item        uint32
	previous, target uint16
}

var bagStages = []cashStage{{101098, 2660296, 0, 8}, {102046, 2660297, 8, 16}, {102531, 2683675, 16, 24}}
var personalStages = []cashStage{
	{100064, 57, 24, 40}, {100065, 58, 40, 56}, {100066, 59, 56, 72}, {100067, 60, 72, 88}, {100068, 61, 88, 104}, {100069, 62, 104, 120},
	{101062, 2660235, 120, 136}, {101063, 2660236, 136, 152}, {103662, 10098631, 152, 168}, {103663, 10098632, 168, 184}, {103664, 10098633, 184, 200},
	{104889, 10304724, 200, 216}, {104890, 10304725, 216, 232}, {104891, 10304726, 232, 248}, {104892, 10304727, 248, 264},
}
var accountInitial = []cashStage{{100534, 2681921, 8, 16}, {102045, 2682968, 32, 40}, {102108, 2683046, 40, 48}, {102316, 2683324, 48, 56}, {102659, 2683926, 56, 64}}
var capacityLifecycle = regexp.MustCompile("(?i)expiration|expiry|usable period|avatar|creature|artifact|package|contract|action|required|cash|purchase|buy|limit count")

func stageRange(sku, item uint32, previous, count int) []cashStage {
	out := make([]cashStage, 0, count)
	for n := range count {
		out = append(out, cashStage{sku + uint32(n), item + uint32(n), uint16(previous + n*8), uint16(previous + (n+1)*8)})
	}
	return out
}

func accountStages() []cashStage {
	rows := slices.Clone(accountInitial)
	rows = append(rows, stageRange(103665, 490700152, 64, 7)...)
	return append(rows, stageRange(104893, 490702625, 120, 15)...)
}

func avatarStages() []cashStage {
	rows := stageRange(102644, 2683901, 0, 15)
	rows = append(rows, stageRange(104415, 490701718, 120, 5)...)
	return append(rows, stageRange(104908, 490702640, 160, 25)...)
}

func (s *cashResources) buildCapacity() (storage.CargoCatalog, error) {
	rows, err := s.rows("item etc")
	if err != nil {
		return storage.CargoCatalog{}, err
	}
	m := map[uint32]cashRow{}
	for _, row := range rows {
		m[row.sku] = row
	}
	account := accountStages()
	for idx, stages := range [][]cashStage{bagStages, personalStages, account, avatarStages()} {
		for _, stage := range stages {
			row, found := m[stage.sku]
			if !found {
				continue
			}
			if row.item != stage.item || row.count != 1 || !row.ceraQuote() || row.display != 0 || row.code != 0 ||
				!s.ordinarySupplemental(row, false) {
				return storage.CargoCatalog{}, fmt.Errorf("cash capacity stage %d differs from its complete native quote", stage.sku)
			}
			r, err := s.items.Source(row.item)
			if err != nil {
				return storage.CargoCatalog{}, err
			}
			if err := capacityTemplate(r, idx); err != nil {
				return storage.CargoCatalog{}, err
			}
			q := row.quote()
			switch idx {
			case 0:
				q.TargetBagExpansion = stage.target
			case 1:
				q.TargetPersonalCargoCapacity, q.PreviousPersonalCargoCapacity = stage.target, stage.previous
			case 2:
				q.TargetAccountCargoCapacity = stage.target
			case 3:
				q.TargetAvatarExpansion = stage.target
			}
			if err := s.add(q, nil); err != nil {
				return storage.CargoCatalog{}, err
			}
		}
	}
	return readAccountCargo(s.items.source, account)
}

func capacityTemplate(source ItemSource, kind int) error {
	fields, err := gm.ItemFields(source.Text)
	if err != nil {
		return err
	}
	required := map[string]string{"stackable type": "`[etc]` 0", "attach type": "`[trade]`", "usable job": "`[all]`", "minimum level": "1", "impossible contents": "`gift`"}
	if kind != 0 {
		required["grade"], required["rarity"] = "1", "2"
	}
	for name, w := range required {
		v := cashField(source, name)
		if kind == 0 && name == "minimum level" && v == "" {
			v = "1"
		}
		if v != w {
			return fmt.Errorf("cash capacity template %d differs at %s", source.Id, name)
		}
	}
	allowed := []string{"name", "explain", "grade", "rarity", "usable job", "attach type", "minimum level", "icon", "stackable type", "move wav", "impossible contents"}
	if kind == 2 {
		allowed = append(allowed, "flavor text")
	}
	for _, f := range fields {
		if strings.HasPrefix(f.Name, "/") {
			continue
		}
		if kind == 0 && capacityLifecycle.MatchString(f.Name) || kind != 0 && !slices.Contains(allowed, f.Name) {
			return fmt.Errorf("cash capacity template %d needs additional metadata: %s", source.Id, f.Name)
		}
	}
	return nil
}

func readAccountCargo(a gm.ItemArchive, stages []cashStage) (storage.CargoCatalog, error) {
	text, err := a.ReadText("etc/accountcargo.etc", scriptLimit)
	if err != nil {
		return storage.CargoCatalog{}, err
	}
	fields, err := gm.ItemFields(text)
	if err != nil {
		return storage.CargoCatalog{}, err
	}
	for _, f := range fields {
		switch f.Name {
		case "required level", "upgrade info", "/upgrade info", "account cargo end":
		default:
			return storage.CargoCatalog{}, fmt.Errorf("account cargo opening metadata is unsupported")
		}
	}
	levelText, err := gm.ItemField(text, "required level")
	if err != nil {
		return storage.CargoCatalog{}, err
	}
	lv, err := unsigned(levelText, 8)
	if err != nil || lv < 1 || lv > 100 {
		return storage.CargoCatalog{}, fmt.Errorf("account cargo level is unsupported")
	}
	raw, err := gm.ItemField(text, "upgrade info")
	if err != nil {
		return storage.CargoCatalog{}, err
	}
	v, err := pvftext.Integers(raw)
	if err != nil || len(v) == 0 || len(v) > 1536 || len(v)%6 != 0 {
		return storage.CargoCatalog{}, fmt.Errorf("invalid account cargo six-field table")
	}
	out := storage.CargoCatalog{RequiredLevel: byte(lv), Stages: []storage.CargoStage{}}
	previous, lastGold, seen := 0, 0, map[uint32]bool{}
	for n := 0; n < len(v); n += 6 {
		capacity, limit, material, count, gold, sku := v[n], v[n+1], v[n+2], v[n+3], v[n+4], v[n+5]
		if capacity <= previous || capacity > 256 || limit < lastGold || material < -1 || count < 0 || gold < 0 || sku < -1 ||
			material == -1 && count != 0 || material != -1 && count == 0 || sku != -1 && (material != -1 || gold != 0) || sku == -1 && gold == 0 && count == 0 {
			return storage.CargoCatalog{}, fmt.Errorf("account cargo stage price or adjacency is invalid")
		}
		if sku != -1 {
			index := slices.IndexFunc(stages, func(r cashStage) bool { return r.sku == uint32(sku) })
			if index < 0 || seen[uint32(sku)] || stages[index].previous != uint16(previous) || stages[index].target != uint16(capacity) {
				return storage.CargoCatalog{}, fmt.Errorf("account cargo cash stage %d differs from its matched adjacent service", sku)
			}
			seen[uint32(sku)] = true
		}
		out.Stages = append(out.Stages, storage.CargoStage{Previous: uint16(previous), Capacity: uint16(capacity), GoldLimit: uint32(limit),
			Material: uint32(max(0, material)), MaterialCount: uint32(count), Gold: uint32(gold), Commodity: int32(sku)})
		previous, lastGold = capacity, limit
	}
	if len(seen) != len(stages) {
		return storage.CargoCatalog{}, fmt.Errorf("account cargo table is missing a supported cash stage")
	}
	return out, nil
}

func (s *cashResources) addSimpleServices() error {
	rows, err := s.rows("item etc")
	if err != nil {
		return err
	}
	for _, r := range rows {
		var path, hash string
		switch r.sku {
		case 103660:
			if r.item != 2660239 || r.cera != 2900 {
				return fmt.Errorf("character slot quote changed")
			}
			path, hash = "stackable/cash/character_slotkit.stk", "DD3677EEACE37E8ED32811E868B008F05B656B8F521E6834BF56B91378E2B963"
		case 100494:
			if r.item != 821 || r.cera != 5800 || s.nonstackable[r.sku] == 0 {
				return fmt.Errorf("skill style quote changed")
			}
			path, hash = "stackable/cash/skilltree_plusitem.stk", "AD89AC933FCCD2B9A780019D9E185D532F4F7594EA32A7676538DF65F6616FA1"
		default:
			continue
		}
		src, err := s.items.Source(r.item)
		if err != nil {
			return err
		}
		if src.Path != path || src.Hash != hash || !r.ceraQuote() || r.count != 1 || r.display != 0 || r.code != 0 ||
			!s.ordinarySupplemental(r, false) || s.ceraOnly[r.item] != 0 || s.mileage[r.sku] != 0 || s.returning[r.sku] != 0 ||
			s.restrictionNumbers[r.sku] != 0 || s.discountNumbers[r.sku] != 0 || s.couponNumbers[r.sku] != 0 {
			return fmt.Errorf("cash character service %d fails its resource qualification", r.sku)
		}
		q := r.quote()
		var template *ItemTemplate
		if r.sku == 103660 {
			q.CharacterSlot = true
			template, err = src.Gift()
			if err != nil {
				return err
			}
		} else {
			q.SkillStyleHash = hash
		}
		if err := s.add(q, template); err != nil {
			return err
		}
	}
	return nil
}

var dyeNumber = regexp.MustCompile("^([0-9]+)\\s+1000$")

func (s *cashResources) addDyes() error {
	rows, err := s.rows("avatar dye")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if !r.ceraQuote() || r.count != 1 || r.display != 1 || r.code != 0 || !s.ordinarySupplemental(r, false) {
			continue
		}
		src, err := s.items.Source(r.item)
		if err != nil || src.Equipment || !validDye(src) {
			continue
		}
		t, err := src.Gift()
		if err != nil || t.Kind != 2 || t.FirstBagSlot != 65 || t.LastBagSlot != 120 || t.StackLimit != 1000 || !t.PvfGiftItem ||
			t.ResourceExpiration != nil || t.RelativeLifetimeSeconds != 0 || t.DailyDeletion || t.WeeklyDeletionDay != nil || t.InitialSealed || t.AccountAfterFirstTrade {
			continue
		}
		if err := s.add(r.quote(), t); err != nil {
			return err
		}
	}
	return nil
}

func validDye(src ItemSource) bool {
	fields, err := gm.ItemFields(src.Text)
	if err != nil {
		return false
	}
	valid := []string{"name", "explain", "grade", "rarity", "usable job", "attach type", "minimum level", "icon", "stackable type", "move wav", "stack limit", "dye type",
		"dye info", "impossible contents", "item category", "price"}
	blocks := []string{"usable job", "dye info", "impossible contents", "item category"}
	m := map[string]string{}
	for _, f := range fields {
		if !slices.Contains(valid, strings.TrimPrefix(f.Name, "/")) ||
			strings.HasPrefix(f.Name, "/") && !slices.Contains(blocks, f.Name[1:]) {
			return false
		}
		if _, dup := m[f.Name]; dup {
			return false
		}
		m[f.Name] = f.Value
	}
	for _, tag := range blocks {
		_, open := m[tag]
		close, end := m["/"+tag]
		if open != end || close != "" {
			return false
		}
	}
	if m["stackable type"] != "`[dye]` 0" || m["usable job"] != "`[all]`" || m["minimum level"] != "1" || m["stack limit"] != "1000" ||
		m["attach type"] != "`[free]`" && m["attach type"] != "`[account]`" || m["dye type"] != "1" || m["item category"] != "" ||
		m["impossible contents"] != "" && m["impossible contents"] != "`gift`" {
		return false
	}
	if value, ok := m["grade"]; ok {
		if n, e := unsigned(value, 8); e != nil || n > 3 {
			return false
		}
	}
	if value, ok := m["price"]; ok {
		if _, e := unsigned(value, 31); e != nil {
			return false
		}
	}
	rarity, err := strconv.Atoi(m["rarity"])
	color := dyeNumber.FindStringSubmatch(m["dye info"])
	if err != nil || rarity < 1 || rarity > 2 || color == nil {
		return false
	}
	n, err := unsigned(color[1], 8)
	return err == nil && n <= 47
}
