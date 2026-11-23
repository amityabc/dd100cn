package dungeons

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type SecondaryTowerRewards struct {
	Hash                                             string
	Profiles                                         []SecondaryTowerProfile
	DropKindProbabilities, DropItemTypeProbabilities []uint32
	DifficultyDropBonuses                            []json.Number
	RarityThresholds                                 []uint32
	PartyBonuses                                     []SecondaryTowerParty
	GoldDropBonuses                                  []json.Number
	MapCountRates                                    []SecondaryTowerMapRate
	clearRewardExtras
}
type SecondaryTowerProfile struct {
	Name   string
	Ranges []SecondaryTowerRange
}
type SecondaryTowerRange struct {
	MinimumLevel, MaximumLevel byte
	Probability, Auxiliary     uint32
}
type SecondaryTowerParty struct {
	DungeonType int
	Multipliers []json.Number
}
type SecondaryTowerMapRate struct{ MaximumCount, Rate uint32 }

var towerPartyBlock = regexp.MustCompile(`(?ms)^\[dungeon type\]\s*(.*?)^\[/dungeon type\]`)

func parseClearRewardSource(text, sourceHash string, allowZeroTail bool) (*SecondaryTowerRewards, error) {
	p := &input{}
	r := &SecondaryTowerRewards{Hash: hashText(completionClearPath + "|" + sourceHash + "\n" + text), Profiles: []SecondaryTowerProfile{},
		PartyBonuses: []SecondaryTowerParty{}, MapCountRates: []SecondaryTowerMapRate{}}
	count := p.secondaryUnsigned(text, "drop prob count", true)
	if !p.require(len(count) == 1 && count[0] > 0 && count[0] <= 64, "tower clear reward probability count invalid") {
		return nil, p.err
	}
	profile := p.secondaryBody(text, "drop prob", true)
	rows := secondaryNamedProfile.FindAllStringSubmatchIndex(profile, -1)
	p.completeRows(profile, rows, "tower clear reward profile table incomplete")
	p.require(len(rows) > 0, "tower clear reward requires named profiles")
	seen := map[string]bool{}
	for _, row := range rows {
		fields := matchFields(profile, row)
		if !p.require(fields[1] != "" && !seen[fields[1]], "tower clear reward profile repeats or unnamed") {
			return nil, p.err
		}
		seen[fields[1]] = true
		ranges := p.towerUnsigned(fields[2], int(count[0]*4), "drop prob")
		if !p.require(len(ranges)%4 == 0, "tower clear reward has incomplete ranges") {
			return nil, p.err
		}
		out := SecondaryTowerProfile{Name: fields[1], Ranges: []SecondaryTowerRange{}}
		next := uint32(1)
		for i := 0; i < len(ranges); i += 4 {
			minimum, maximum, chance := ranges[i], ranges[i+1], ranges[i+2]
			p.require(minimum == next && maximum >= minimum && maximum <= 255 && chance <= 10000, "tower clear reward range invalid or not contiguous")
			out.Ranges = append(out.Ranges, SecondaryTowerRange{byte(minimum), byte(maximum), chance, ranges[i+3]})
			next = maximum + 1
		}
		r.Profiles = append(r.Profiles, out)
	}
	r.DropKindProbabilities = p.towerUnsigned(p.secondaryBody(text, "drop kind prob", true), 64, "drop kind prob")
	r.DropItemTypeProbabilities = p.towerUnsigned(p.secondaryBody(text, "drop item type prob", true), 64, "drop item type prob")
	r.DifficultyDropBonuses = p.secondaryOptionalDecimals(text, "dungeon difficulty drop bonusrate", 5)
	p.require(len(r.DifficultyDropBonuses) == 5, "tower clear reward difficulty bonuses missing")
	r.RarityThresholds = p.towerUnsigned(p.secondaryBody(text, "basis of rarity dicision", true), 8, "rarity thresholds")
	prefix := []uint32{}
	zero := false
	for _, value := range r.RarityThresholds {
		if allowZeroTail && value == 0 {
			zero = true
			continue
		}
		p.require(!zero && value <= 1000004, "tower clear reward rarity thresholds outside zero tail")
		prefix = append(prefix, value)
	}
	p.require(slices.IsSorted(prefix), "tower clear reward rarity thresholds unordered")
	parties := p.field(text, "party member drop bonusrate", true)
	partyRows := towerPartyBlock.FindAllStringSubmatchIndex(parties, -1)
	p.completeRows(parties, partyRows, "tower clear reward party bonus incomplete")
	p.require(len(partyRows) > 0, "tower clear reward requires party bonuses")
	for _, row := range partyRows {
		fields := strings.Fields(parties[row[2]:row[3]])
		if !p.require(len(fields) == 5, "tower clear reward party bonus width invalid") {
			return nil, p.err
		}
		kind, err := strconv.ParseInt(fields[0], 10, 32)
		p.require(err == nil, "tower clear reward party dungeon type invalid")
		r.PartyBonuses = append(r.PartyBonuses, SecondaryTowerParty{int(kind), p.secondaryDecimals(strings.Join(fields[1:], " "), "tower party bonus")})
	}
	r.GoldDropBonuses = p.secondaryOptionalDecimals(text, "dungeon difficulty gold drop bonusrate", 5)
	p.require(len(r.GoldDropBonuses) == 5, "tower clear reward gold bonuses absent")
	counts := p.towerUnsigned(p.secondaryBody(text, "reward item rate per map max count", true), 64, "map count rates")
	if !p.require(len(counts) > 0 && len(counts)%2 == 0, "tower clear map count rates incomplete") {
		return nil, p.err
	}
	for i := 0; i < len(counts); i += 2 {
		p.require(counts[i] > 0 && counts[i+1] > 0, "tower clear map count rates must be positive")
		r.MapCountRates = append(r.MapCountRates, SecondaryTowerMapRate{counts[i], counts[i+1]})
	}
	r.clearRewardExtras = p.clearRewardExtras(text)
	return r, p.err
}

func (p *input) towerUnsigned(body string, max int, label string) []uint32 {
	fields := strings.Fields(body)
	if !p.require(len(fields) > 0 && len(fields) <= max, "tower clear reward "+label+" width invalid") {
		return nil
	}
	out := make([]uint32, 0, len(fields))
	for _, field := range fields {
		v, err := strconv.ParseUint(field, 10, 32)
		p.require(err == nil, "tower clear reward "+label+" numeric value invalid")
		out = append(out, uint32(v))
	}
	return out
}
