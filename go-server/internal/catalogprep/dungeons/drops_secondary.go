package dungeons

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
)

const (
	SecondaryMonsterExtra byte = iota
	SecondaryMonsterHell
	SecondaryObject
	SecondaryTowerClear
	SecondaryControl
)

var secondaryPaths = []string{
	"etc/itemdropinfo_monseter_extra.etc",
	"etc/itemdropinfo_monster_hell.etc",
	"etc/itemdropinfo_object.etc",
	"etc/itemdropinfo_togclearreward.etc",
	"etc/itemdropinfo_control.etc",
}

type SecondaryDropSource struct {
	Path, Hash                                string
	Kind                                      byte
	ProbabilityTag                            string
	DeclaredCount                             int
	Profiles                                  []SecondaryDropProfile
	Sections                                  []string
	ProbabilityRows                           []SecondaryDropProbability
	RarityThresholds                          []uint32
	DifficultyBonus, PartyBonus, MonsterBonus []json.Number
	ItemDropRefTable                          []uint32
	RarityControl                             []int
	ItemDropWindows                           []SecondaryDropWindow
	Control                                   *SecondaryDropControl
	TowerClearReward                          *SecondaryTowerRewards
}

type SecondaryDropProfile struct {
	Name   string
	Values []json.Number
}
type SecondaryDropProbability struct {
	MinimumLevel, MaximumLevel byte
	Per10000                   []uint32
}
type SecondaryDropWindow struct{ Level, Down, Up byte }
type SecondaryDropExport struct {
	Kind                                      byte
	Hash                                      string
	DifficultyBonus, PartyBonus, MonsterBonus []json.Number
	RarityThresholds                          []uint32
	Probabilities                             []SecondaryDropProbability
}

func BuildSecondarySources(a Archive) ([]SecondaryDropSource, error) {
	out := make([]SecondaryDropSource, 0, len(secondaryPaths))
	for i, path := range secondaryPaths {
		text, err := a.ReadText(path, budget)
		if err != nil {
			return nil, err
		}
		hash, err := rawHash(a, path)
		if err != nil {
			return nil, err
		}
		source, err := parseSecondary(path, text, hash, byte(i))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, source)
	}
	return out, nil
}

func SecondaryExport(sources []SecondaryDropSource) []SecondaryDropExport {
	rows := []SecondaryDropExport{}
	for _, s := range sources {
		if s.Kind != SecondaryMonsterExtra && s.Kind != SecondaryObject {
			continue
		}
		rows = append(rows, SecondaryDropExport{Kind: s.Kind, Hash: s.Hash, DifficultyBonus: s.DifficultyBonus, PartyBonus: s.PartyBonus,
			MonsterBonus: s.MonsterBonus, RarityThresholds: s.RarityThresholds, Probabilities: s.ProbabilityRows})
	}
	return rows
}

var (
	secondaryTag          = regexp.MustCompile(`(?m)^\[([^/\r\n][^\r\n]*)\]`)
	secondaryNamedProfile = regexp.MustCompile("`([^`]*)`([^`]*)")
)

func parseSecondary(path, text, sourceHash string, kind byte) (SecondaryDropSource, error) {
	result := SecondaryDropSource{Path: path, Hash: hashText(path + "|" + sourceHash + "\n" + text), Kind: kind,
		Profiles: []SecondaryDropProfile{}, Sections: []string{}, ProbabilityRows: []SecondaryDropProbability{},
		RarityThresholds: []uint32{}, DifficultyBonus: []json.Number{}, PartyBonus: []json.Number{}, MonsterBonus: []json.Number{},
		ItemDropRefTable: []uint32{}, RarityControl: []int{}, ItemDropWindows: []SecondaryDropWindow{}}
	for _, m := range secondaryTag.FindAllStringSubmatch(text, -1) {
		result.Sections = append(result.Sections, strings.TrimSpace(m[1]))
	}
	slices.Sort(result.Sections)
	result.Sections = slices.Compact(result.Sections)
	if kind == SecondaryControl {
		result.ProbabilityTag = "control"
		control, err := parseSecondaryControl(text)
		result.Control = control
		return result, err
	}
	p := &input{}
	switch {
	case slices.Contains(result.Sections, "drop prob"):
		result.ProbabilityTag = "drop prob"
	case slices.Contains(result.Sections, "dungeon difficulty drop prob"):
		result.ProbabilityTag = "dungeon difficulty drop prob"
	default:
		return result, fmt.Errorf("PVF secondary source has no probability section")
	}
	count := p.secondaryUnsigned(text, "drop prob count", true)
	if !p.require(len(count) == 1 && count[0] > 0 && count[0] <= 64, "PVF secondary probability count invalid") {
		return result, p.err
	}
	result.DeclaredCount = int(count[0])
	body := p.secondaryBody(text, result.ProbabilityTag, true)
	pieces := secondaryNamedProfile.FindAllStringSubmatch(body, -1)
	if len(pieces) == 0 {
		result.Profiles = append(result.Profiles, SecondaryDropProfile{Values: p.secondaryDecimals(body, "secondary drop probability")})
	} else {
		seen := map[string]bool{}
		for _, m := range pieces {
			if !p.require(m[1] != "" && !seen[m[1]], "PVF secondary drop profile unnamed or repeated") {
				return result, p.err
			}
			seen[m[1]] = true
			result.Profiles = append(result.Profiles, SecondaryDropProfile{m[1], p.secondaryDecimals(m[2], "secondary drop probability")})
		}
	}
	result.ProbabilityRows = secondaryProbabilityRows(result.Profiles, result.DeclaredCount)
	result.RarityThresholds = p.secondaryUnsigned(text, "basis of rarity dicision", false)
	if r := result.RarityThresholds; result.ProbabilityTag == "dungeon difficulty drop prob" && len(r) == result.DeclaredCount*8+1 && int(r[0]) == result.DeclaredCount {
		result.RarityThresholds = r[1:]
	}
	result.DifficultyBonus = p.secondaryOptionalDecimals(text, "dungeon difficulty drop bonusrate", 25)
	if kind != SecondaryTowerClear {
		result.PartyBonus = p.secondaryOptionalDecimals(text, "party member drop bonusrate", 20)
	}
	result.MonsterBonus = p.secondaryOptionalDecimals(text, "monster type drop bonusrate", 20)
	result.ItemDropRefTable = p.secondaryUnsigned(text, "item drop ref table", false)
	if v := p.secondaryBody(text, "item drop rarity control", false); tagPresent(text, "item drop rarity control") {
		rows, err := pvftext.Integers(v)
		p.require(err == nil && len(rows) > 0 && len(rows)%5 == 0, "PVF secondary rarity control has incomplete signed rows")
		result.RarityControl = list(rows)
	}
	refs := result.ItemDropRefTable
	if !p.require(len(refs)%3 == 0 && len(refs)/3 <= 255, "PVF secondary item drop window incomplete") {
		return result, p.err
	}
	for i := 0; i < len(refs); i += 3 {
		level, down, up := refs[i], refs[i+1], refs[i+2]
		p.require(level == uint32(i/3+1) && level <= 255 && down <= 255 && up <= 255, "PVF secondary item window not contiguous")
		result.ItemDropWindows = append(result.ItemDropWindows, SecondaryDropWindow{byte(level), byte(down), byte(up)})
	}
	if p.err != nil {
		return result, p.err
	}
	if kind == SecondaryTowerClear {
		tower, err := parseClearRewardSource(text, sourceHash, true)
		result.TowerClearReward = tower
		return result, err
	}
	return result, nil
}

var nextSecondaryTag = regexp.MustCompile(`(?m)^\[`)

func (p *input) secondaryBody(text, tag string, must bool) string {
	re := regexp.MustCompile(`(?m)^\[` + regexp.QuoteMeta(tag) + `\]`)
	indices := re.FindStringIndex(text)
	if indices == nil {
		p.require(!must, "PVF secondary source section missing: "+tag)
		return ""
	}
	body := strings.TrimLeft(text[indices[1]:], " \t\r\n")
	if next := nextSecondaryTag.FindStringIndex(body); next != nil {
		body = body[:next[0]]
	}
	return strings.TrimSpace(body)
}

func secondaryDecimalsValue(token string) (json.Number, *big.Rat, bool) {
	if token == "" {
		return "", nil, false
	}
	decimal, ok := new(big.Rat).SetString(token)
	if !ok || decimal.Sign() < 0 || strings.ContainsAny(token, "/_xX") {
		return "", nil, false
	}
	clean := strings.TrimPrefix(token, "+")
	if strings.HasPrefix(clean, ".") {
		clean = "0" + clean
	}
	if strings.HasSuffix(clean, ".") {
		clean = strings.TrimSuffix(clean, ".")
	}
	if strings.HasPrefix(clean, "-") {
		clean = strings.TrimPrefix(clean, "-")
	}
	if !json.Valid([]byte(clean)) {
		return "", nil, false
	}
	return json.Number(clean), decimal, true
}

func (p *input) secondaryDecimals(text, tag string) []json.Number {
	fields := strings.Fields(text)
	p.require(len(fields) > 0, "empty secondary decimal section: "+tag)
	out := make([]json.Number, 0, len(fields))
	for _, field := range fields {
		value, _, ok := secondaryDecimalsValue(field)
		p.require(ok, "invalid secondary decimal section: "+tag)
		out = append(out, value)
	}
	return out
}

func (p *input) secondaryOptionalDecimals(text, tag string, expected int) []json.Number {
	if !tagPresent(text, tag) {
		return []json.Number{}
	}
	body := p.secondaryBody(text, tag, true)
	if strings.Contains(body, "[") {
		return []json.Number{}
	}
	d := p.secondaryDecimals(body, tag)
	p.require(len(d) == expected || expected == 25 && len(d) == 5, "incomplete secondary decimal section: "+tag)
	return d
}

func (p *input) secondaryUnsigned(text, tag string, must bool) []uint32 {
	body := p.secondaryBody(text, tag, must)
	out := []uint32{}
	for _, field := range strings.Fields(body) {
		u, err := strconv.ParseUint(field, 10, 32)
		p.require(err == nil, "invalid unsigned secondary section: "+tag)
		out = append(out, uint32(u))
	}
	return out
}

func secondaryProbabilityRows(profiles []SecondaryDropProfile, declared int) []SecondaryDropProbability {
	var values []json.Number
	for _, r := range profiles {
		values = append(values, r.Values...)
	}
	blank := []SecondaryDropProbability{}
	if len(values) != declared*7 {
		return blank
	}
	out := []SecondaryDropProbability{}
	expected := uint32(1)
	for i := 0; i < len(values); i += 7 {
		var row [7]uint32
		for j := range row {
			_, r, ok := secondaryDecimalsValue(string(values[i+j]))
			if !ok || !r.IsInt() || !r.Num().IsUint64() || r.Num().Uint64() > 10000 {
				return blank
			}
			row[j] = uint32(r.Num().Uint64())
		}
		if row[0] != expected || row[0] == 0 || row[0] > 255 || row[1] < row[0] || row[1] > 255 {
			return blank
		}
		out = append(out, SecondaryDropProbability{byte(row[0]), byte(row[1]), slices.Clone(row[2:])})
		expected = row[1] + 1
	}
	return out
}
