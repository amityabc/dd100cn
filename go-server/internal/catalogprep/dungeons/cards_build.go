package dungeons

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/savejson"
)

type cardPaidPool struct {
	Type, Level, Rarity byte
	Items               []uint32
}
type cardPaid struct {
	Hash, KindHash               string
	Costs                        map[byte]uint32
	Profiles                     map[string][]cardProbabilityProfile
	DropKinds, ItemTypes, Rarity []uint32
	DifficultyBonuses            []json.Number
	CreateRateParts              uint32
	Pools                        []cardPaidPool
	Equipment                    map[uint32]NativeDropCandidate
	Specific                     *cardSpecific
	Groups                       *cardGroups
	Generation                   *EquipmentGeneration
}
type Cards struct {
	Hash, GuideHash      string
	DifficultyRates      []json.Number
	SoloPartyRate        json.Number
	Rank                 cardRank
	Probabilities        []freeCardProbability
	Equipment            []NativeDropCandidate
	DropKinds, ItemTypes []uint32
	DifficultyBonuses    []json.Number
	Rarity               []uint32
	Windows              map[byte]cardWindow
	MapRates             map[byte]uint32
	Specific             *cardSpecific
	Generation           *EquipmentGeneration
	Challenge            []challengeCards
	Paid                 cardPaid
}

const freeCardPolicy = "LocalInheritedGold/v1"
const freeCardRules = "solo-clear-gold-reference/v2;base=trunc(amount*175/1000);half=trunc(base*weighted-kills/2);group=trunc(float32(half)*weight);piece=trunc(float32(group)/2*weight);weight=solo-party*difficulty*(1+pvf-rank-bonus);variance-inclusive-integer-percent;no-blank-bonus;sha256-run-seed-level-weight-rule;reject-zero-overflow"
const paidCardPolicy = "LocalNormalPaid/v1"
const paidCardRules = "normal-prepared-runs;normal-paid-profile1-event-reference/v1;pvf-level-domain;dungeon-basis-level/v1;zero-basis-legacy-input;pvf-grade-window-upper-exclusive-max200-reference;no-wear-level-filter;" +
	"creation-rate-parts10000;rarity-domain1000000;retain-unsupported-weights;no-reroll-no-blank-bonus;" +
	"unavailable-no-charge;ordinary-free-no-random-detail;one-item;paid-gold-zero-reference/v2;" +
	"item-type-fixed10000/v2;one-based;type2-conventional;other-types-unavailable;uncovered-type1;exclusive-upper-local;" +
	"difficulty-item-bonus/v2;truncate-creation-before-add;integer-threshold;clamp10000;pvf-five-slots;" +
	"drop-kind-first-fixed10000/v1;specific-dungeon-item-fixed100-reference;separate-kind-domain;sha256-run-seed-level-weight-rule"

func CardCompatibilityIDs() []uint32 { return slices.Clone(cardCompatibilityIDs[:]) }

func BuildCards(a Archive, layouts []Definition, items *commerce.Items, candidates []NativeDropCandidate, generation *EquipmentGeneration) (Cards, error) {
	var out Cards
	if items == nil || generation == nil || len(generation.InitialDurabilityHash) != 64 {
		return out, fmt.Errorf("card preparation requires matching item and equipment generation sources")
	}
	t, err := readCardTables(a)
	if err != nil {
		return out, err
	}
	lanes, err := qualifiedCardCandidates(candidates, cardNeedsStackable(t.itemTypes))
	if err != nil {
		return out, err
	}
	dungeons, err := cardDefinitions(layouts, false)
	if err != nil {
		return out, err
	}
	paid, err := cardDefinitions(dungeons, true)
	if err != nil {
		return out, err
	}
	var specific *cardSpecific
	if len(t.kinds) != 0 && t.kinds[0] < 10000 {
		specific, err = loadCardSpecific(a, items)
		if err != nil {
			return out, err
		}
	}
	groups, err := loadCardGroups(a, items, t, paid)
	if err != nil {
		return out, err
	}
	challenge, err := loadCardChallenges(a, items, dungeons)
	if err != nil {
		return out, err
	}
	out = Cards{
		DifficultyRates: t.difficultyRates, SoloPartyRate: t.soloPartyRate, Rank: t.rank,
		DropKinds: t.kinds, ItemTypes: t.itemTypes, DifficultyBonuses: t.difficultyBonuses, Rarity: t.rarity,
		Windows: t.windows, MapRates: t.mapRates, Specific: specific, Generation: generation, Challenge: challenge,
	}
	for _, v := range t.profiles["default"] {
		out.Probabilities = append(out.Probabilities, freeCardProbability{v.Minimum, v.Maximum, v.Probability})
	}
	for _, v := range lanes {
		if v.CreationWeight != 0 {
			out.Equipment = append(out.Equipment, v)
		}
	}
	out.Hash, err = freeCardHash(out, t, generation)
	if err != nil {
		return out, err
	}
	out.GuideHash = hashText("LocalGuideGold/v1\nresource-enabled-difficulty;" + freeCardRules + "\n" + out.Hash)
	out.Paid = buildPaidCards(t, lanes, specific, groups, generation)
	out.Paid.Hash, err = paidCardHash(t, lanes, paid, specific, groups, generation)
	out.Paid.KindHash = hashText("paid-drop-kind/v1|" + out.Paid.Hash)
	return out, err
}

var conventionalCardEquipment = []string{"weapon", "coat", "shoulder", "pants", "waist", "shoes", "amulet", "wrist", "ring", "support", "magic stone", "earring"}

func qualifiedCardCandidates(candidates []NativeDropCandidate, stackable bool) ([]NativeDropCandidate, error) {
	seen := make(map[uint32]NativeDropCandidate)
	for _, c := range candidates {
		if prior := seen[c.Id]; prior.Id != 0 {
			return nil, fmt.Errorf("duplicate shared card candidate %d", c.Id)
		}
		seen[c.Id] = c
	}
	out := make([]NativeDropCandidate, 0, len(cardCompatibilityIDs))
	added := make(map[uint32]bool, len(cardCompatibilityIDs))
	for _, id := range cardCompatibilityIDs {
		c, ok := seen[id]
		if !ok || !c.Equipment || c.Creature || c.EquipmentType != "" && !slices.Contains(conventionalCardEquipment, c.EquipmentType) {
			return nil, fmt.Errorf("paid card compatibility equipment %d is absent or no longer conventional", id)
		}
		out = append(out, c)
		added[id] = true
	}
	for _, c := range candidates {
		if !added[c.Id] && (c.Creature || stackable && !c.Equipment) && c.CreationWeight != 0 {
			out = append(out, c)
		}
	}
	if len(out) == 0 || len(out) > 16384 {
		return nil, fmt.Errorf("paid card candidates exceed native bounds")
	}
	for _, c := range out {
		if c.Id == 0 || c.Id == math.MaxUint32 || c.Grade < 1 || c.Grade > 255 || c.MinimumLevel < 1 || c.MinimumLevel > 255 || c.Rarity < 0 || c.Rarity > 7 ||
			c.Equipment && (c.Grade > 102 || c.MinimumLevel > 102 || c.Recipe) || c.Creature && (!c.Equipment || c.Template != nil) ||
			c.Artifact != nil && (!c.Creature || c.Artifact.Kind != 2 || c.Artifact.TemplateId != c.Id || c.Artifact.ResourceHash != c.DefinitionHash) ||
			len(c.DefinitionHash) != 64 || c.Template != nil && (c.Template.TemplateId != c.Id || c.Equipment == (c.Template.Kind == 2) ||
			int(c.Template.MinimumLevel) != c.MinimumLevel || c.Template.ResourceHash != c.DefinitionHash) {
			return nil, fmt.Errorf("illegal card candidate %d", c.Id)
		}
	}
	slices.SortFunc(out, func(a, b NativeDropCandidate) int { return int(a.Id) - int(b.Id) })
	return out, nil
}

func cardDefinitions(layouts []Definition, paid bool) ([]Definition, error) {
	seen := make(map[[2]uint32]bool)
	var out []Definition
	for _, d := range layouts {
		if !d.UsesOrdinaryRewards || paid && d.ID == dungeon.WisdomDungeon {
			continue
		}
		key := [2]uint32{d.ID, uint32(d.MazeIndex)}
		if seen[key] {
			continue
		}
		if d.ID == 0 || len(d.ResourceHash) != 64 {
			return nil, fmt.Errorf("invalid ordinary card dungeon definition %d/%d", d.ID, d.MazeIndex)
		}
		seen[key] = true
		out = append(out, d)
	}
	if len(out) == 0 || len(out) > 4096 {
		return nil, fmt.Errorf("paid card definitions absent or exceed native bounds")
	}
	slices.SortFunc(out, func(a, b Definition) int {
		if a.ID != b.ID {
			return int(a.ID) - int(b.ID)
		}
		return int(a.MazeIndex) - int(b.MazeIndex)
	})
	return out, nil
}

func freeCardHash(out Cards, t cardTables, generation *EquipmentGeneration) (string, error) {
	var text strings.Builder
	fmt.Fprintf(&text, "%s\n%s\n%s|%s\n%s|%s\n", freeCardPolicy, freeCardRules, cardCommonPath, t.commonHash, cardClearPath, t.clearHash)
	for _, row := range orderedPairs(t.gold) {
		fmt.Fprintf(&text, "G|%d|%d|%d\n", row.Key, row.Value.Amount, row.Value.Variance)
	}
	for i, d := range out.DifficultyRates {
		fmt.Fprintf(&text, "D|%d|%s\n", i, canonicalCardDecimal(d))
	}
	fmt.Fprintf(&text, "S|%s\nGR|%s\n", canonicalCardDecimal(out.SoloPartyRate), out.Rank.Hash)
	for _, v := range out.DropKinds {
		fmt.Fprintf(&text, "K|%d\n", v)
	}
	if len(out.DropKinds) != 0 {
		text.WriteString("free-drop-kind-reference/v1;first-threshold-fixed10000;random-or-dungeon-item;separate-domains7-8-9\n")
	}
	if out.Specific != nil {
		fmt.Fprintf(&text, "specific|%s\n", out.Specific.hash)
	}
	for _, v := range out.ItemTypes {
		fmt.Fprintf(&text, "T|%d\n", v)
	}
	text.WriteString("free-item-type-fixed10000/v2;one-based;type2-conventional;other-types-empty-no-reroll;uncovered-type1;exclusive-upper-local;draw-domain5\n" +
		"normal-free-no-event-goldcard-blank/v1;source-rows-frozen-only\n" +
		"ordinary-clear-dungeon-basis-level/v1;zero-basis-legacy-input;gold-and-equipment\n" +
		"free-difficulty-item-bonus/v2;truncate-exploration-before-add;integer-threshold;clamp10000;pvf-five-slots\n" +
		"free-map-rate-zero-bypass/v1;preserve-exploration;missing-unchanged\n")
	for _, v := range out.DifficultyBonuses {
		fmt.Fprintf(&text, "DI|%s\n", canonicalCardDecimal(v))
	}
	for _, v := range out.Probabilities {
		fmt.Fprintf(&text, "P|%d|%d|%d\n", v.Minimum, v.Maximum, v.Probability)
	}
	for _, v := range out.Rarity {
		fmt.Fprintf(&text, "R|%d\n", v)
	}
	if out.Rarity[3] < 1_000_000 {
		text.WriteString("free-rarity-domain/v1;all-eight-pvf-columns;retain-unavailable-outcomes\n")
	}
	for _, w := range orderedPairs(out.Windows) {
		fmt.Fprintf(&text, "W|%d|%d|%d\n", w.Key, w.Value.Down, w.Value.Up)
	}
	for _, v := range orderedPairs(out.MapRates) {
		fmt.Fprintf(&text, "M|%d|%d\n", v.Key, v.Value)
	}
	for _, v := range t.blank {
		fmt.Fprintf(&text, "B|%d|%d|%d\n", v.Weight, v.Item, v.Quantity)
	}
	if len(out.Equipment) != 0 {
		text.WriteString("free-equipment-retain-unsupported-no-reroll/v3;pvf-grade-window-upper-exclusive-max200-reference;no-wear-level-filter;no-window-fallback\n")
	}
	for _, v := range out.Equipment {
		source := "unsupported"
		if v.Template != nil {
			source = v.Template.ResourceHash
		}
		fmt.Fprintf(&text, "E|%d|%d|%d|%d|%d|%s|%s\n", v.Id, v.Grade, v.MinimumLevel, v.Rarity, v.CreationWeight, v.DefinitionHash, source)
	}
	for _, p := range []struct {
		prefix  string
		matches func(NativeDropCandidate) bool
	}{
		{"stackable-card/v1;type1-all-stackables;type3-recipes;one-item;no-equipment-luck|", func(c NativeDropCandidate) bool { return !c.Equipment }},
		{"creature-card/v1;type2-and-type4;type4-no-equipment-luck|", func(c NativeDropCandidate) bool { return c.Creature }},
	} {
		for _, c := range out.Equipment {
			if p.matches(c) {
				b, err := savejson.Marshal(c)
				if err != nil {
					return "", err
				}
				text.WriteString(p.prefix)
				text.Write(b)
				text.WriteByte('\n')
			}
		}
	}
	boolean := func(v bool) string {
		if v {
			return "True"
		}
		return "False"
	}
	for _, c := range out.Equipment {
		sealed := c.Template != nil && c.Template.InitialSealed
		if sealed || c.RequiresMagicSeal {
			fmt.Fprintf(&text, "acquisition|%d|%s|%s\n", c.Id, boolean(sealed), boolean(c.RequiresMagicSeal))
		}
	}
	return bindCardGeneration(hashText(text.String()), generation), nil
}

func bindCardGeneration(hash string, generation *EquipmentGeneration) string {
	return hashText(hash + "\ninitial-durability-callers-reference/v1\n" + generation.InitialDurabilityHash)
}

func buildPaidCards(t cardTables, rows []NativeDropCandidate, specific *cardSpecific, groups *cardGroups, generation *EquipmentGeneration) cardPaid {
	out := cardPaid{Costs: t.costs, Profiles: t.profiles, DropKinds: t.kinds, ItemTypes: t.itemTypes, Rarity: t.rarity, DifficultyBonuses: t.difficultyBonuses,
		CreateRateParts: t.createRateParts, Pools: []cardPaidPool{}, Equipment: make(map[uint32]NativeDropCandidate), Specific: specific, Groups: groups, Generation: generation}
	var rarity [8][]NativeDropCandidate
	for _, r := range rows {
		if r.CreationWeight != 0 {
			rarity[r.Rarity] = append(rarity[r.Rarity], r)
		}
	}
	for typ := byte(1); typ <= 4; typ++ {
		for _, w := range orderedPairs(t.windows) {
			level := int(w.Key)
			lo, hi := max(1, level-int(w.Value.Down)), min(201, level+int(w.Value.Up))
			for grade, set := range rarity {
				var ids []uint32
				for _, c := range set {
					if c.Grade < lo || c.Grade >= hi {
						continue
					}
					match := typ == 1 && !c.Equipment || typ == 2 && c.Equipment || typ == 3 && !c.Equipment && c.Recipe || typ == 4 && c.Equipment && c.Creature
					if match {
						ids = append(ids, c.Id)
						out.Equipment[c.Id] = c
					}
				}
				if len(ids) > 0 {
					out.Pools = append(out.Pools, cardPaidPool{typ, w.Key, byte(grade), ids})
				}
			}
		}
	}
	return out
}

type paidHashTemplate struct {
	TemplateId         uint32
	ResourceHash       string
	Kind, MinimumLevel byte
	StackLimit         uint32
	MaximumDurability  uint16
}

func paidCardHash(t cardTables, rows []NativeDropCandidate, definitions []Definition, specific *cardSpecific, groups *cardGroups,
	generation *EquipmentGeneration) (string, error) {
	h := sha256.New()
	write := func(text []byte) {
		var prefix [4]byte
		binary.LittleEndian.PutUint32(prefix[:], uint32(len(text)))
		h.Write(prefix[:])
		h.Write(text)
	}
	var err error
	encode := func(prefix string, value any) {
		if err != nil {
			return
		}
		var b []byte
		b, err = savejson.Marshal(value)
		if err == nil {
			write(append([]byte(prefix), b...))
		}
	}
	for _, v := range []string{paidCardPolicy, paidCardRules, cardCommonPath, t.commonHash, t.common, cardClearPath, t.clearHash, t.clear} {
		write([]byte(v))
	}
	if specific != nil {
		write([]byte(specific.hash))
	}
	if groups != nil {
		write([]byte(groups.hash))
	}
	for _, v := range rows {
		var template *paidHashTemplate
		if t := v.Template; t != nil {
			template = &paidHashTemplate{t.TemplateId, t.ResourceHash, t.Kind, t.MinimumLevel, t.StackLimit, t.MaximumDurability}
		}
		encode("", struct {
			Id                          uint32
			Grade, MinimumLevel, Rarity int
			CreationWeight              uint32
			DefinitionHash              string
			Template                    *paidHashTemplate
		}{v.Id, v.Grade, v.MinimumLevel, v.Rarity, v.CreationWeight, v.DefinitionHash, template})
	}
	for _, v := range rows {
		if !v.Equipment {
			encode("paid-stackable-card/v1;type1-all-stackables;type3-recipes;one-item;no-equipment-luck|", v)
		}
	}
	for _, v := range rows {
		if v.Creature {
			encode("paid-creature-card/v1;type2-and-type4;type4-no-equipment-luck;complete-special-claim|", v)
		}
	}
	for _, v := range rows {
		sealed := v.Template != nil && v.Template.InitialSealed
		if sealed || v.RequiresMagicSeal {
			encode("", struct {
				Acquisition               uint32
				Sealed, RequiresMagicSeal bool
			}{v.Id, sealed, v.RequiresMagicSeal})
		}
	}
	for _, d := range definitions {
		encode("", paidIdentity(d))
	}
	if err != nil {
		return "", err
	}
	return bindCardGeneration(fmt.Sprintf("%X", h.Sum(nil)), generation), nil
}

type paidRoomIdentity struct {
	X, Y       byte
	MapId      uint32
	Gates      byte
	MapAliases []uint32
	Monsters   []nativeMonster
}

type paidDungeonIdentity struct {
	Id                                                            uint32
	MinimumLevel, BasisLevel, MazeIndex, BossX, BossY, Difficulty byte
	SelectableDifficulties                                        []uint16
	ChallengeStage                                                uint16
	ChallengeStages                                               []uint16
	StartX, StartY                                                byte
	ExperienceMultiplier                                          json.Number
	ResourceHash                                                  string
	QuestConnections                                              []uint16
	Rooms                                                         []paidRoomIdentity
}

func paidIdentity(d Definition) paidDungeonIdentity {
	rooms := []opening.Room{}
	for _, r := range d.Rooms {
		if r.X != d.Start.X || r.Y != d.Start.Y {
			rooms = append(rooms, r)
		}
	}
	slices.SortFunc(rooms, func(a, b opening.Room) int {
		if a.Y != b.Y {
			return int(a.Y) - int(b.Y)
		}
		return int(a.X) - int(b.X)
	})
	entry := paidDungeonIdentity{d.ID, d.MinimumLevel, d.meta.basis, d.MazeIndex, d.BossX, d.BossY, d.Difficulty,
		byteValues(d.meta.selectable), d.ChallengeStage, list(d.ChallengeStages), d.Start.X, d.Start.Y, d.meta.multiplier, d.ResourceHash,
		list(slices.Clone(d.meta.quests)), []paidRoomIdentity{}}
	slices.Sort(entry.QuestConnections)
	for _, r := range append([]opening.Room{d.Start}, rooms...) {
		w := wireRoom(r)
		aliases := list(slices.Clone(w.MapAliases))
		slices.Sort(aliases)
		entry.Rooms = append(entry.Rooms, paidRoomIdentity{w.X, w.Y, w.MapId, w.Gates, aliases, w.Monsters})
	}
	return entry
}
