package dungeons

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/savejson"
)

const groundPolicy = "PVFMonsterDrops/v33;solo-market-uninitialized-neutral-reference;floating-market-thresholds;solo-gold-bonuses-on-chance-reference;zero-gold-control-preserves-artifacts;native-mon-named-classification;character-difficulty-first-hunt;monster-resource-item-and-gold-restrictions;initial-durability-extra-and-independent-callers-reference;initial-equipment-durability-reference;object-equipment-generation-reference;pvf-generated-equipment-upgrade;creature-channel-and-special-pickup-reference;extra-selection-suppresses-main-items-reference;extra-has-no-gold;dungeon-basis-probability-reference;recipe-channel-pool-reference;solo-boss-gold-tier-reference;ordinary-actor-extra-source;hell-capability-not-run-mode;gold-type-on-chance;gold-percent-variance-reference;combined-rate-before-clamp;zero-gold-suppressed;native-recipe-throw-acquisition;secondary-complete-source-windows;native-secondary-source-kind-routing;native-rarity-control-frozen;native-secondary-monster-object-hell;itemdropinfo-control-level-gold;itemdropinfo-togclearreward-settlement-source;itemdropinfo-monseter-independent-channels;pvf-10000-drop-scale;generic-independent-production;legacy-farming-parse-only;aic-independent-owner;native-only-production;difficulty-party-monster-bonuses;four-rarity-profiles;grade-upper-exclusive-maximum200-reference;ordinary-sealed-and-unopened-magic-acquisition;scoped-equipment-effects-and-native-qualification;static-evolution-recipe-acquisition;legacy-skill-and-scoped-avatar-condition;monster-drop-scope-separated-from-clear-rewards"
const firstHuntPolicy = "OrdinarySoloFirstHunt/v1;native-mon-classification;character-difficulty-history;unknown-history-no-extra"
const groundLuckPolicy = "OrdinarySoloLuck/v1;wide-integer-percent;character-owner;generation-debit"

type groundGold struct{ Amount, Variance int }
type groundRate struct {
	MinimumLevel, MaximumLevel byte
	Per10000                   []int
}
type groundAux struct{ FirstBossNamedMobHunting, ConditionRate, GoldQuantity, GoldVolume, RarityControl []int }
type GroundRules struct {
	Probabilities                             []groundRate
	DifficultyBonus, PartyBonus, MonsterBonus []json.Number
	RarityThresholds                          []int
	Auxiliary                                 groundAux
	Hash                                      string
}
type mainDropPool struct {
	Level           byte
	Rarity, Channel int
	Ids             []uint32
}
type mainDropData struct {
	Hash      string
	Native    GroundRules
	Gold      map[byte]groundGold
	MainPools []mainDropPool
	levels    []byte
	layouts   []Definition
	sources   []Definition
	codes     map[uint32]bool
}
type firstHuntData struct {
	Hash                            string
	BossMultiplier, NamedMultiplier int
}

func joinDropInts[T ~int | ~byte | ~uint32](numbers []T, sep string) string {
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = strconv.FormatInt(int64(n), 10)
	}
	return strings.Join(parts, sep)
}
func groundNative(a Archive, ground string) (GroundRules, error) {
	var r GroundRules
	const path = "etc/itemdropinfo_monseter.etc"
	hash, err := rawHash(a, path)
	if err != nil {
		return r, err
	}
	values, err := pvftext.Numbers(ground, "drop prob")
	if err != nil {
		return r, err
	}
	count, err := pvftext.Numbers(ground, "drop prob count")
	if err != nil || len(count) != 1 || count[0] <= 0 || len(values) != count[0]*7 {
		return r, errors.New("invalid ordinary drop probability count")
	}
	last := 0
	for v := range slices.Chunk(values, 7) {
		if v[0] <= last || v[1] < v[0] || v[1] >= 255 || slices.Min(v[2:]) < 0 || slices.Max(v[2:]) > 10000 {
			return r, errors.New("invalid native drop level range")
		}
		r.Probabilities = append(r.Probabilities, groundRate{byte(v[0]), byte(v[1]), v[2:]})
		last = v[1]
	}
	dec := cardInput{}
	var bonuses []string
	for _, v := range []struct {
		tag  string
		n    int
		dest *[]json.Number
	}{{"dungeon difficulty drop bonusrate", 25, &r.DifficultyBonus}, {"party member drop bonusrate", 20, &r.PartyBonus}, {"monster type drop bonusrate", 20, &r.MonsterBonus}} {
		body, err := pvftext.Section(ground, v.tag)
		if err != nil {
			return r, err
		}
		*v.dest = dec.decimals(body, v.n, flexibleDecimal, 0, false)
		ss := make([]string, len(*v.dest))
		for i, n := range *v.dest {
			ss[i] = n.String()
		}
		bonuses = append(bonuses, strings.Join(ss, " "))
	}
	if dec.err != nil {
		return r, dec.err
	}
	r.RarityThresholds, err = pvftext.Numbers(ground, "basis of rarity dicision")
	if err != nil || len(r.RarityThresholds) != 32 {
		return r, errors.New("invalid ordinary rarity profiles")
	}
	for c := range slices.Chunk(r.RarityThresholds, 8) {
		if !slices.IsSorted(c) || c[0] < 0 || c[7] < 1_000_000 || c[7] > 1_000_004 {
			return r, errors.New("invalid rarity thresholds")
		}
	}
	aux := &r.Auxiliary
	var auxText []string
	for _, row := range []struct {
		tag  string
		max  int
		dest *[]int
	}{{"first boss/named mob hunting", 64, &aux.FirstBossNamedMobHunting}, {"condition rate", 128, &aux.ConditionRate}, {"gold quantity", 64, &aux.GoldQuantity}, {"gold volume", 64, &aux.GoldVolume}, {"item drop rarity control", 256, &aux.RarityControl}} {
		v, err := pvftext.Numbers(ground, row.tag)
		if err != nil || len(v) > row.max || len(v) > 0 && row.tag != "item drop rarity control" && slices.Min(v) < 0 {
			return r, fmt.Errorf("invalid drop field %s", row.tag)
		}
		*row.dest = v
		auxText = append(auxText, joinDropInts(v, " "))
	}
	if len(pvftext.Blocks(ground, "first boss/named mob hunting")) > 0 && len(aux.FirstBossNamedMobHunting) != 2 {
		return r, errors.New("incomplete first hunt weights")
	}
	if len(aux.ConditionRate)+len(aux.GoldQuantity)+len(aux.GoldVolume) > 0 {
		if len(aux.ConditionRate) != 8 || len(aux.GoldQuantity) != 4 || len(aux.GoldVolume) != 2 || slices.Max(aux.ConditionRate) > 65535 || slices.Max(aux.GoldVolume) > 65535 || slices.Max(aux.GoldQuantity) > 16 || aux.GoldQuantity[0] > aux.GoldQuantity[1] || aux.GoldQuantity[2] > aux.GoldQuantity[3] {
			return r, errors.New("invalid boss gold configuration")
		}
	}
	r.Hash = hashText(strings.Join([]string{path, hash, joinDropInts(values, " "), bonuses[0], bonuses[1], bonuses[2], joinDropInts(r.RarityThresholds, " "), "aux=" + strings.Join(auxText, ";")}, "|"))
	return r, nil
}

func dungeonDropRooms(d Definition) []opening.Room {
	rooms := make([]opening.Room, 0, len(d.Rooms)+1)
	for _, room := range d.Rooms {
		if room.X != d.Start.X || room.Y != d.Start.Y {
			rooms = append(rooms, room)
		}
	}
	slices.SortFunc(rooms, func(a, b opening.Room) int { return cmp.Or(cmp.Compare(a.Y, b.Y), cmp.Compare(a.X, b.X)) })
	return append([]opening.Room{d.Start}, rooms...)
}
func ordinaryDropsAllowed(d Definition) bool {
	return !d.meta.disableDrops && d.UsesOrdinaryMonsterDrops && (d.UsesOrdinaryRewards || d.Difficulty != 0)
}

func makeFirstHunt(defs []Definition, monHash string, weights []int) *firstHuntData {
	if len(weights) != 2 {
		return nil
	}
	defs = slices.Clone(defs)
	slices.SortStableFunc(defs, func(a, b Definition) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.MazeIndex, b.MazeIndex), cmp.Compare(a.ResourceVariant, b.ResourceVariant))
	})
	rows := make([]string, len(defs))
	boolean := func(b bool) string {
		if b {
			return "True"
		}
		return "False"
	}
	for i, d := range defs {
		rows[i] = fmt.Sprintf("%d:%d:%d:%d:%s:%s:%s:%s:%s", d.ID, d.MazeIndex, d.ResourceVariant, d.Difficulty, joinDropInts(d.meta.selectable, ","), boolean(d.UsesOrdinaryRewards), boolean(d.UsesOrdinaryMonsterDrops), boolean(d.meta.disableDrops), d.ResourceHash)
	}
	hash := hashText(fmt.Sprintf("%s\n%d/%d\n%s\n%s", firstHuntPolicy, weights[0], weights[1], monHash, strings.Join(rows, "\n")))
	return &firstHuntData{hash, weights[0], weights[1]}
}

func buildMainGround(a Archive, candidates []NativeDropCandidate, definitions []Definition) (mainDropData, error) {
	ground, err := a.ReadText("etc/itemdropinfo_monseter.etc", budget)
	if err != nil {
		return mainDropData{}, err
	}
	common, err := a.ReadText("etc/itemdropinfo_common.etc", budget)
	if err != nil {
		return mainDropData{}, err
	}
	x := mainDropData{Gold: map[byte]groundGold{}, MainPools: []mainDropPool{}, codes: map[uint32]bool{}}
	x.Native, err = groundNative(a, ground)
	if err != nil {
		return x, err
	}
	gold, e := pvftext.Numbers(common, "gold drop ref table")
	if e != nil || len(gold) == 0 || len(gold) > 768 || len(gold)%3 != 0 {
		return x, errors.New("invalid gold table")
	}
	for v := range slices.Chunk(gold, 3) {
		k := byte(v[0])
		if v[0] < 1 || v[0] > 255 || v[1] <= 0 || v[2] < 0 || v[2] > 99 || x.Gold[k].Amount != 0 {
			return x, errors.New("invalid gold reference row")
		}
		x.Gold[k] = groundGold{v[1], v[2]}
	}
	refs, e := pvftext.Numbers(ground, "item drop ref table")
	if e != nil || len(refs) == 0 || len(refs) > 768 || len(refs)%3 != 0 {
		return x, errors.New("invalid drop windows")
	}
	windows := map[byte][2]int{}
	for v := range slices.Chunk(refs, 3) {
		k := byte(v[0])
		_, dup := windows[k]
		if v[0] < 1 || v[0] > 255 || slices.Min(v[1:]) < 0 || slices.Max(v[1:]) > 255 || dup {
			return x, errors.New("invalid drop window row")
		}
		windows[k] = [2]int{v[1], v[2]}
	}
	seen := map[[3]uint32]string{}
	for _, d := range definitions {
		key := [3]uint32{d.ID, uint32(d.MazeIndex), d.ResourceVariant}
		if hash, ok := seen[key]; ok {
			if hash != d.ResourceHash {
				return x, errors.New("conflicting drop source for maze")
			}
			continue
		}
		seen[key] = d.ResourceHash
		x.sources = append(x.sources, d)
		for _, r := range dungeonDropRooms(d) {
			for _, m := range r.Monsters {
				if !m.Dummy {
					x.codes[m.Code] = true
				}
			}
		}
		if ordinaryDropsAllowed(d) {
			x.layouts = append(x.layouts, d)
		}
	}
	if len(x.layouts) == 0 || len(x.sources) > 4096 || len(candidates) == 0 || len(candidates) > 65536 {
		return x, errors.New("invalid ordinary drop source count")
	}
	slices.SortStableFunc(x.layouts, func(a, b Definition) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.MazeIndex, b.MazeIndex))
	})
	h := sha256.New()
	addLengthText(h, groundPolicy)
	addLengthText(h, common)
	addLengthText(h, ground)
	var groups [8][5][]NativeDropCandidate
	for _, c := range candidates {
		raw, err := json.Marshal(c)
		if err != nil {
			return x, err
		}
		addLengthText(h, string(raw))
		if c.CreationWeight == 0 {
			continue
		}
		channel := 1
		if c.Equipment {
			channel = 2
		}
		groups[c.Rarity][channel] = append(groups[c.Rarity][channel], c)
		if c.Recipe {
			groups[c.Rarity][3] = append(groups[c.Rarity][3], c)
		}
		if c.Creature {
			groups[c.Rarity][4] = append(groups[c.Rarity][4], c)
		}
	}
	levels := map[byte]bool{}
	type roomIdentity struct {
		X, Y       byte
		MapId      uint32
		Gates      byte
		MapAliases []uint32
		Monsters   []nativeMonster
	}
	for _, d := range x.layouts {
		rooms := []roomIdentity{}
		for _, r := range dungeonDropRooms(d) {
			wr := wireRoom(r)
			aliases := slices.Clone(wr.MapAliases)
			slices.Sort(aliases)
			rooms = append(rooms, roomIdentity{r.X, r.Y, r.MapID, r.Gates, aliases, wr.Monsters})
			for _, m := range r.Monsters {
				levels[m.Level] = true
			}
		}
		quests := list(slices.Clone(d.meta.quests))
		slices.Sort(quests)
		row := struct {
			Id                                                                                            uint32
			MinimumLevel, BasisLevel, MazeIndex, BossX, BossY, StartX, StartY                             byte
			ExperienceMultiplier                                                                          json.Number
			ResourceHash                                                                                  string
			DisableDrops, UsesOrdinaryMonsterDrops, AllowsOrdinaryMonsterDrops, HellDungeon, TowerOfGrief bool
			QuestConnections                                                                              []uint16
			Rooms                                                                                         []roomIdentity
		}{d.ID, d.MinimumLevel, d.meta.basis, d.MazeIndex, d.BossX, d.BossY, d.Start.X, d.Start.Y, d.meta.multiplier, d.ResourceHash, d.meta.disableDrops, d.UsesOrdinaryMonsterDrops, true, d.meta.hell, d.meta.tower, quests, rooms}
		raw, err := savejson.Marshal(row)
		if err != nil {
			return x, err
		}
		addLengthText(h, string(raw))
	}
	x.Hash = fmt.Sprintf("%X", h.Sum(nil))
	x.levels = slices.Sorted(maps.Keys(levels))
	for _, level := range x.levels {
		w, ok := windows[level]
		if !ok || x.Gold[level].Amount == 0 || level < 3 || level > 100 && level != 134 && level != 135 {
			return x, fmt.Errorf("unsupported drop window for monster level %d", level)
		}
		from, to := max(1, int(level)-w[0]), min(201, int(level)+w[1])
		for rarity, cols := range groups {
			for i := 1; i <= 4; i++ {
				var ids []uint32
				for _, c := range cols[i] {
					if c.Grade >= from && c.Grade < to {
						ids = append(ids, c.Id)
					}
				}
				if len(ids) > 0 {
					x.MainPools = append(x.MainPools, mainDropPool{level, rarity, i, ids})
				}
			}
		}
	}
	return x, nil
}
