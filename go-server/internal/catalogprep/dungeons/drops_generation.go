package dungeons

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/dungeon"
)

const generationPolicy = "PVFOrdinaryEquipmentUpgrade/v4;initial-durability-reference;older-five-quality-seeds-and-main-monster-object-callers;first-weight-f32-gap-factor;inclusive-weighted-reference;no-extra-hell-or-card-upgrade"

func BuildEquipmentGeneration(a Archive) (*dungeon.EquipmentGeneration, error) {
	const path = "etc/(r)serverparameter.etc"
	text, err := a.ReadText(path, budget)
	if err != nil {
		return nil, err
	}
	hash, err := rawHash(a, path)
	if err != nil {
		return nil, err
	}
	out := &dungeon.EquipmentGeneration{}
	type weight = struct {
		Level  byte
		Weight uint64
	}
	type rate = struct {
		Gap    int
		Factor float32
	}
	out.Rarities = map[byte][]weight{}
	out.Rates = []rate{}
	damage, err := pvftext.Section(text, "damaged equipment ratio")
	if err != nil {
		return nil, err
	}
	ratio := "absent"
	if damage != "" {
		n, err := strconv.ParseUint(damage, 10, 8)
		if err != nil || n > 100 {
			return nil, errors.New("invalid initial equipment durability ratio")
		}
		v := byte(n)
		out.DamagedEquipmentRatio = &v
		ratio = strconv.Itoa(int(v))
	} else if len(pvftext.Blocks(text, "damaged equipment ratio")) != 0 {
		return nil, errors.New("empty durability ratio")
	}
	body, err := pvftext.Section(text, "drop upgrade rarity")
	if err != nil {
		return nil, err
	}
	tokens := strings.Fields(body)
	if len(tokens) > 4096 {
		return nil, errors.New("too many equipment generation weights")
	}
	labels := []string{"common", "uncommon", "rare", "unique", "epic", "chronicle", "legendary", "mythology"}
	for at := 0; at < len(tokens); {
		label := strings.TrimSuffix(strings.TrimPrefix(tokens[at], "\x60["), "]\x60")
		n := slices.Index(labels, label)
		if n < 0 || tokens[at] != "\x60["+label+"]\x60" {
			return nil, errors.New("invalid equipment rarity tag")
		}
		key := byte(n)
		if _, seen := out.Rarities[key]; seen {
			return nil, errors.New("duplicate equipment rarity")
		}
		at++
		rows := []weight{}
		sum := uint64(0)
		close := "\x60[/" + label + "]\x60"
		for at < len(tokens) && tokens[at] != close {
			if at+1 >= len(tokens) {
				return nil, errors.New("incomplete equipment weight")
			}
			l, e1 := strconv.ParseUint(tokens[at], 10, 8)
			w, e2 := strconv.ParseUint(tokens[at+1], 10, 31)
			sum += w
			if e1 != nil || e2 != nil || l > 31 || sum > math.MaxInt32 {
				return nil, errors.New("invalid equipment upgrade weights")
			}
			rows = append(rows, weight{byte(l), w})
			at += 2
		}
		if at == len(tokens) {
			return nil, errors.New("unclosed equipment rarity")
		}
		at++
		out.Rarities[key] = rows
	}
	body, err = pvftext.Section(text, "drop bonus of dungeon level increase")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(body)
	if len(fields) > 4096 || len(fields)%2 != 0 {
		return nil, errors.New("invalid level gap factors")
	}
	var rates []string
	for i := 0; i < len(fields); i += 2 {
		g, e1 := strconv.ParseInt(fields[i], 10, 32)
		f, e2 := strconv.ParseFloat(fields[i+1], 32)
		factor := float32(f)
		if e1 != nil || e2 != nil || math.IsInf(f, 0) || math.IsNaN(f) || f < 0 || len(out.Rates) > 0 && int(g) >= out.Rates[len(out.Rates)-1].Gap || 1000*factor >= 1<<31 || int64(1000*factor)+4000 > math.MaxInt32 {
			return nil, errors.New("invalid equipment generation gap")
		}
		rates = append(rates, fmt.Sprintf("%d:%s", g, strconv.FormatFloat(f, 'g', -1, 32)))
		out.Rates = append(out.Rates, rate{int(g), factor})
	}
	var rows []string
	for _, k := range slices.Sorted(maps.Keys(out.Rarities)) {
		var values []string
		var sum uint64
		w := out.Rarities[k]
		for _, v := range w {
			values = append(values, fmt.Sprintf("%d:%d", v.Level, v.Weight))
			sum += v.Weight
		}
		if len(w) > 0 {
			for _, r := range out.Rates {
				scaled := float32(w[0].Weight) * r.Factor
				if scaled >= 1<<31 || sum-w[0].Weight+uint64(scaled) > math.MaxInt32 {
					return nil, errors.New("scaled upgrade pool exceeds capacity")
				}
			}
		}
		rows = append(rows, fmt.Sprintf("%d:%s", k, strings.Join(values, " ")))
	}
	out.Hash = hashText(fmt.Sprintf("%s\n%s\n%s\n%d %d %d %d %d\n%s\n%s", generationPolicy, hash, ratio, 0x5aa7, 0xfa6, 0x6a23, 0x5df0, 0x7270, strings.Join(rows, "\n"), strings.Join(rates, " ")))
	out.InitialDurabilityHash = hashText(out.Hash + "\ninitial-durability-v1")
	return out, nil
}
