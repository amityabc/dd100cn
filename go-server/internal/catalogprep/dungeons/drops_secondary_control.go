package dungeons

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

type SecondaryDropControl struct {
	GoldPartyKeys                                 []uint32
	GoldPartyRates                                []json.Number
	GoldLevelRates                                []uint32
	EquipmentPartyKeys                            []uint32
	EquipmentPartyRates                           []json.Number
	EquipmentLevelRates                           []uint32
	EquipmentLevelControls                        []SecondaryEquipmentLevel
	DurabilityKeys                                []uint32
	DurabilityRates                               []json.Number
	GoldMarket, EquipmentMarket, DurabilityMarket SecondaryMarket
	NativeGoldLevels                              []SecondaryGoldLevel
}
type SecondaryEquipmentLevel struct {
	MinimumLevel, MaximumLevel uint32
	Rates                      []uint32
}
type SecondaryMarket struct {
	Thresholds, Rates []float64
}
type SecondaryGoldLevel struct {
	Level       byte
	Coefficient int
}

func parseSecondaryControl(text string) (*SecondaryDropControl, error) {
	p := &input{}
	r := &SecondaryDropControl{GoldLevelRates: []uint32{}, EquipmentLevelControls: []SecondaryEquipmentLevel{}, NativeGoldLevels: []SecondaryGoldLevel{}}
	r.GoldPartyKeys, r.GoldPartyRates, r.GoldMarket = p.marketPair(text, "control of gold drop amount rate")
	r.EquipmentPartyKeys, r.EquipmentPartyRates, r.EquipmentMarket = p.marketPair(text, "control of equipment drop prob")
	r.DurabilityKeys, r.DurabilityRates, r.DurabilityMarket = p.marketPair(text, "control of durability decrease rate")
	r.EquipmentLevelRates = p.secondaryUnsigned(text, "control of equipment drop prob per level range", false)
	gold := strings.Fields(p.secondaryBody(text, "control of gold drop amount rate per level", false))
	if !p.require(len(gold)%2 == 0 && len(gold) <= 8192, "secondary gold level coefficients incomplete or oversized") {
		return nil, p.err
	}
	nonnegative := true
	for i := 0; i < len(gold); i += 2 {
		level, le := strconv.ParseUint(gold[i], 10, 8)
		coefficient, ce := strconv.ParseInt(gold[i+1], 10, 32)
		p.require(le == nil && ce == nil && level >= 1 && level <= 200, "secondary gold level coefficient invalid")
		r.NativeGoldLevels = append(r.NativeGoldLevels, SecondaryGoldLevel{byte(level), int(coefficient)})
		nonnegative = nonnegative && coefficient >= 0
	}
	if nonnegative {
		for _, row := range r.NativeGoldLevels {
			r.GoldLevelRates = append(r.GoldLevelRates, uint32(row.Level), uint32(row.Coefficient))
		}
	}
	ranges := r.EquipmentLevelRates
	if !p.require(len(ranges)%7 == 0 && len(ranges)/7 <= 256, "secondary equipment level control incomplete") {
		return nil, p.err
	}
	if len(ranges) > 0 {
		next := ranges[0]
		for i := 0; i < len(ranges); i += 7 {
			minimum, maximum := ranges[i], ranges[i+1]
			rate := ranges[i+2 : i+7]
			for _, v := range rate {
				p.require(v <= 10000, "secondary equipment level rate exceeds native scale")
			}
			p.require(minimum <= maximum && minimum == next, "secondary equipment level controls not contiguous")
			r.EquipmentLevelControls = append(r.EquipmentLevelControls, SecondaryEquipmentLevel{minimum, maximum, rate})
			if maximum == math.MaxUint32 {
				break
			}
			next = maximum + 1
		}
	}
	return r, p.err
}

func (p *input) marketPair(text, tag string) ([]uint32, []json.Number, SecondaryMarket) {
	fields := strings.Fields(p.secondaryBody(text, tag, true))
	out := SecondaryMarket{Thresholds: []float64{}, Rates: []float64{}}
	if !p.require(len(fields) == 10, "secondary market requires five thresholds and rates: "+tag) {
		return nil, nil, out
	}
	values := make([]float64, len(fields))
	for i, field := range fields {
		n, err := strconv.ParseFloat(field, 64)
		p.require(err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0, "secondary market has invalid numeric value: "+tag)
		values[i] = n
	}
	out.Thresholds, out.Rates = values[:5], values[5:]
	keys := []uint32{}
	for _, v := range out.Thresholds {
		if v > math.MaxUint32 || math.Trunc(v) != v {
			keys = []uint32{}
			break
		}
		keys = append(keys, uint32(v))
	}
	rates := []json.Number{}
	for i, token := range fields[5:] {
		v, r, ok := secondaryDecimalsValue(token)
		if !ok {
			rates = []json.Number{}
			break
		}
		n, _ := r.Float64()
		if n != out.Rates[i] {
			rates = []json.Number{}
			break
		}
		rates = append(rates, v)
	}
	return keys, rates, out
}
