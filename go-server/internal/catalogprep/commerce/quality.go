package commerce

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
)

// The quality-box families in this profile are defined by the exact PVF scripts.
var qualitySourceHashes = map[uint32]string{
	15:        "66041BFEB6CF328C0DDA532CC1BCB37F20BD0424FF932592D9E4C1BD42F507C2",
	897:       "7C083AB545A220D1249C3B915C07B83128A7AE18B42DFCD92292EC9B9A0D57BF",
	10150860:  "CD2FB99FA3F3DC0BC150BCE509FE5CA3E577B2B966237612A80A86F26EBA5127",
	10150861:  "783D0DA28EC148F1402C5BCEE4258BA23C7B9CE989D8405B7C234C7D6EF78D7B",
	10150862:  "5B0B41C3F5FB95AF066E3FFBEE8EE719E300982CCA03344F483C75DCFFEB293C",
	10150863:  "6B302B8B11DE063263CB0ECA76DE767FEB228D7A5E0634FCC5163C2214AC5270",
	10150903:  "BDEF08AEC42A39948F8A9D7C702EC19465E8A1043075F49E511C15E0C54FA501",
	10302434:  "79EF00EFFBBAFA878FF2A2254595B744F94A332EC8E351EF6CE4AC87E752DABF",
	490000155: "C596EBAD08B4A69D39B805997BE12145223F2E463E4FA0CED7E49844F54968A8",
	2683895:   "3B79CF7F8EB288CBFAE383468DC581FAE1FAB013351B3BD8949BE21B61DFAAF8",
	490013009: "3B274B6ADFB81F20D9387986A5BC45E1048FADF389B2BDF808501B44BE033BC4",
	490013699: "86395279AFB26BB84E1E34EF74DDD44D4ADDE9428ADF61AB3A6F4223E649EFE9",
	490017880: "2E4B893E5C8CDB85E82894858B6B6A6CA5CA2745BF0212B99608790A00D03BC1",
}

type Quality struct {
	Sources   map[uint32]*ItemTemplate
	Targets   map[uint32]QualityTarget
	Mythology map[uint32]QualityMythology
}
type QualityTarget struct {
	Template *ItemTemplate
	Facts    QualityFacts
}
type QualityFacts struct {
	ResourceHash                                  string
	Rarity, CustomAbilityCount, CustomAbilityType byte
}
type QualityMythology struct {
	Hash    string
	Count   byte
	Options [][]int
}
type MythologyResolver func(ItemSource) (*QualityMythology, error)

func qualitySource(s ItemSource) (*ItemTemplate, error) {
	if s.Equipment || qualitySourceHashes[s.Id] != s.Hash {
		return nil, fmt.Errorf("quality box %d has an unknown resource identity", s.Id)
	}
	switch s.Id {
	case 2683895, 490013009, 490013699:
		t, _, err := s.GM()
		return t, err
	case 490000155:
		s.Text = strings.ReplaceAll(s.Text, "\x60[gold kaleido]\x60 0", "\x60[etc]\x60 0")
	}
	return s.Gift()
}

func qualityFacts(s ItemSource) (QualityFacts, error) {
	f := QualityFacts{ResourceHash: s.Hash}
	rows, err := gm.ItemFields(s.Text)
	if err != nil {
		return f, err
	}
	known := map[string]string{}
	for _, r := range rows {
		switch r.Name {
		case "rarity", "custom ability count", "custom ability type":
			if old, seen := known[r.Name]; seen && old != r.Value {
				return f, fmt.Errorf("conflicting %s in item %d", r.Name, s.Id)
			}
			known[r.Name] = r.Value
		}
	}
	rarity, err := strconv.ParseUint(known["rarity"], 10, 8)
	if err != nil || rarity > 8 {
		return f, fmt.Errorf("invalid rarity of quality target %d", s.Id)
	}
	f.Rarity = byte(rarity)
	if v := known["custom ability count"]; v != "" {
		n, err := strconv.ParseUint(v, 10, 8)
		if err != nil {
			return f, fmt.Errorf("invalid ability count of quality target %d", s.Id)
		}
		f.CustomAbilityCount = byte(n)
	}
	switch known["custom ability type"] {
	case "":
	case "\x60warriors dungeon\x60":
		f.CustomAbilityType = 1
	case "\x60epic production\x60":
		f.CustomAbilityType = 2
	case "\x60new warriors dungeon\x60":
		f.CustomAbilityType = 3
	default:
		f.CustomAbilityType = 255
	}
	return f, nil
}

func BuildQuality(items *Items, roots []uint32, prepared map[uint32]*ItemTemplate, resolve MythologyResolver) (*Quality, error) {
	out := &Quality{map[uint32]*ItemTemplate{}, map[uint32]QualityTarget{}, map[uint32]QualityMythology{}}
	for id := range qualitySourceHashes {
		s, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		t, err := qualitySource(s)
		if err != nil {
			return nil, fmt.Errorf("quality source %d: %w", id, err)
		}
		if t == nil || t.ResourceHash != s.Hash {
			return nil, fmt.Errorf("incomplete quality source %d", id)
		}
		out.Sources[id] = t
	}
	ids := slices.Concat(roots, slices.Collect(maps.Keys(prepared)))
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		if items.equipment[int64(id)] == "" {
			continue
		}
		s, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		t := prepared[id]
		if t == nil {
			t, _ = preparedTemplate(s)
		}
		if t == nil || t.Kind > 1 || t.WornSlot < 12 || t.WornSlot > 25 || t.Kind != 0 && !t.WornSlotIsNative {
			continue
		}
		f, err := qualityFacts(s)
		if err != nil {
			return nil, err
		}
		if f.ResourceHash != t.ResourceHash {
			return nil, fmt.Errorf("changed quality target %d", id)
		}
		if t.Kind == 1 && t.MinimumLevel == 100 && f.Rarity == 7 {
			if resolve == nil {
				return nil, fmt.Errorf("quality target %d requires the mythology option index", id)
			}
			options, err := resolve(s)
			if err != nil {
				return nil, err
			}
			if options != nil {
				copy := *t
				copy.MythologyHash, copy.MythologyOptions = options.Hash, options.Count
				t = &copy
				out.Mythology[id] = *options
			}
		}
		out.Targets[id] = QualityTarget{t, f}
	}
	return out, nil
}
