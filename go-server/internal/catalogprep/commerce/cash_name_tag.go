package commerce

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

var visualRow = regexp.MustCompile("\\s*([0-9]+)\\s+([0-9]+)\\s+(-?[0-9]+)\\s+([0-9]+)\\s+(-?[0-9]+)\\s+([0-9]+)\\s+\x60[^\x60]+\x60\\s+(-?[0-9]+)")
var nameTagIcon = regexp.MustCompile("^\x60[^\x60]+\x60\\s+([0-9]+)$")
var nameTagImage = regexp.MustCompile("^\x60([^\x60]+)\x60\\s+(-?[0-9]+)\\s+(-?[0-9]+)\\s+(-?[0-9]+)$")
var singleLiteral = regexp.MustCompile("^\x60[^\x60]*\x60$")

func (s *cashResources) addNameTags() error {
	text, err := commerceBlock(s.shop, "visual")
	if err != nil {
		return err
	}
	seen := map[uint32]bool{}
	at := 0
	for _, m := range visualRow.FindAllStringSubmatchIndex(text, -1) {
		if m[0] != at {
			return fmt.Errorf("name tag visual catalog is not complete eight-field rows")
		}
		at = m[1]
		r := make([]string, 7)
		for i := range r {
			r[i] = text[m[2+i*2]:m[3+i*2]]
		}
		sku, e := unsigned(r[0], 32)
		id, ie := unsigned(r[1], 32)
		days, de := unsigned(r[3], 32)
		cera, ce := unsigned(r[5], 31)
		if e != nil || ie != nil || de != nil || ce != nil || sku == 0 || sku == math.MaxUint32 || seen[uint32(sku)] {
			return fmt.Errorf("name tag commodity is invalid or duplicated")
		}
		seen[uint32(sku)] = true
		row := cashRow{sku: uint32(sku), item: uint32(id), count: 1, cera: uint32(cera)}
		if r[2] != "-1" || r[4] != "0" || r[6] != "0" || days != 7 && days != 30 || cera == 0 || !s.ordinarySupplemental(row, false) {
			continue
		}
		src, err := s.items.Source(uint32(id))
		if err != nil || !src.Equipment {
			continue
		}
		tag, err := parseNameTag(src)
		if err != nil {
			continue
		}
		tag.Commodity, tag.Cera, tag.Days = uint32(sku), uint32(cera), uint32(days)
		q := row.quote()
		q.NameTag = tag
		if err := s.add(q, nil); err != nil {
			return err
		}
	}
	if strings.TrimSpace(text[at:]) != "" {
		return fmt.Errorf("name tag visual catalog is not complete eight-field rows")
	}
	return nil
}

func parseNameTag(src ItemSource) (*storage.CashNameTag, error) {
	fields, err := gm.ItemFields(src.Text)
	if err != nil {
		return nil, err
	}
	invalid := fmt.Errorf("name tag source %d has unsupported visual or permission metadata", src.Id)
	scalars := []string{"name", "name2", "explain", "flavor text", "attach type", "rarity", "weight", "minimum level", "icon", "equipment type", "sub type", "move wav", "physical attack", "physical defense", "magical attack", "magical defense", "attack speed", "cast speed", "move speed", "name tag bar height rate", "name tag bar alpha"}
	blocks := []string{"usable job", "name tag img", "name tag offset", "name tag bar offset", "impossible equip module"}
	values := map[string]string{}
	for i, f := range fields {
		if _, exists := values[f.Name]; exists {
			return nil, invalid
		}
		if !slices.Contains(scalars, f.Name) && !slices.Contains(blocks, strings.TrimPrefix(f.Name, "/")) {
			return nil, invalid
		}
		if slices.Contains(blocks, f.Name) {
			if i+1 >= len(fields) || fields[i+1].Name != "/"+f.Name || fields[i+1].Value != "" {
				return nil, invalid
			}
		}
		values[f.Name] = f.Value
	}
	for _, name := range append(scalars, blocks...) {
		if _, exists := values[name]; !exists && name != "name2" && name != "flavor text" {
			return nil, invalid
		}
	}
	for _, name := range []string{"name", "name2", "explain", "flavor text"} {
		if v, ok := values[name]; ok && !singleLiteral.MatchString(v) {
			return nil, invalid
		}
	}
	expected := map[string]string{"equipment type": "`[name tag]` 1", "attach type": "`[sealing]`", "usable job": "`[all]`", "minimum level": "1",
		"rarity": "2", "weight": "0", "sub type": "0", "move wav": "`NAME_TOUCH`", "impossible equip module": "`[pvp type]` `[pvp]` `[fair pvp]`",
		"name tag bar height rate": "1", "physical attack": "3", "physical defense": "3", "magical attack": "3", "magical defense": "3",
		"attack speed": "10", "cast speed": "10", "move speed": "10"}
	for key, want := range expected {
		if strings.Join(strings.Fields(values[key]), " ") != want {
			return nil, invalid
		}
	}
	icon, image := nameTagIcon.FindStringSubmatch(values["icon"]), nameTagImage.FindStringSubmatch(values["name tag img"])
	if icon == nil || image == nil || len(image[1]) > 512 {
		return nil, invalid
	}
	if _, err := unsigned(icon[1], 31); err != nil {
		return nil, invalid
	}
	read := func(values []string) ([3]int, bool) {
		var out [3]int
		if len(values) != 3 {
			return out, false
		}
		for i, s := range values {
			n, err := strconv.ParseInt(s, 10, 32)
			if err != nil {
				return out, false
			}
			out[i] = int(n)
		}
		return out, true
	}
	indices, ok := read(image[2:5])
	offset, oo := read(strings.Fields(values["name tag offset"]))
	bar, bo := read(strings.Fields(values["name tag bar offset"]))
	alpha, ae := unsigned(values["name tag bar alpha"], 8)
	if !ok || !oo || !bo || ae != nil || indices[0] < 0 || indices[1] < 0 || indices[2] < 0 {
		return nil, invalid
	}
	tag := &storage.CashNameTag{}
	tag.Template.TemplateID, tag.Template.ResourceHash, tag.Template.MinimumLevel, tag.Template.UsableJobs = src.Id, src.Hash, 1, math.MaxUint16
	tag.Template.Visual = &storage.NameTagVisual{Image: image[1], ImageIndices: indices, Offset: offset, BarHeightRate: 1, BarAlpha: int(alpha), BarOffset: bar}
	tag.Template.Effects = &storage.NameTagEffects{PhysicalAttack: 3, PhysicalDefense: 3, MagicalAttack: 3, MagicalDefense: 3, AttackSpeed: 10, CastSpeed: 10, MoveSpeed: 10}
	tag.Template.UnavailableModules = []string{"[pvp type]", "[pvp]", "[fair pvp]"}
	return tag, nil
}
