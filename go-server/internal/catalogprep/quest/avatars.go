package quest

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/pvf"
	"s9a1/server/internal/storage"
)

var (
	avatarParts             = []string{"hat", "hair", "face", "coat", "pants", "shoes", "breast", "waist", "skin", "aurora", "weapon", "aura skin"}
	avatarType              = regexp.MustCompile("^\x60\\[([^\\]]+) avatar\\]\x60(?:\\s+0)?$")
	generalAvatarType       = regexp.MustCompile("(?i)^\x60\\[([^\\]]+) avatar\\]\x60(?:\\s+\\d+)?$")
	avatarAbilitySourceRE   = regexp.MustCompile("^\x60\\[all\\]\x60\\s+\x60\\[all\\]\x60\\s+([0-4])\\s+([01])$")
	avatarAbilityCaseRE     = regexp.MustCompile("(?ims)^\\[ability case\\]\\s*(\\d+)\\s+(.*?)^\\[/ability case\\]")
	avatarSkillRE           = regexp.MustCompile("(\\d+)\\s+\x60\\[SKILL_LEVEL\\]\x60\\s+\x60\\[[^\\]]+\\]\x60\\s+(-?\\d+(?:\\.\\d+)?)\\s+\\d+")
	avatarCloneRE           = regexp.MustCompile("(?:^|\\s)\x60clear avatar\x60(?:\\s|$)")
	avatarPlainForbidden    = regexp.MustCompile("(?i)expiration|expiry|usable period|required skill|required job skill|package|contract")
	avatarCompoundForbidden = regexp.MustCompile("(?i)compound|combin|synth|impossible|expiration|delete item|usable period")
)

type abilitySource struct {
	Source    *gm.ItemTemplate
	Grade     uint32
	Inclusive bool
}
type dyeSource struct {
	Source *gm.ItemTemplate
	Grade  uint32
	Color  uint32
}
type avatarAbilityTarget struct {
	TemplateId   uint32
	ResourceHash string
	Grade        byte
	Abilities    []int
	Jobs, Skills map[byte]uint16
}
type avatarDyeTarget struct {
	TemplateId   uint32
	ResourceHash string
	Grade        byte
	Enabled      bool
}
type avatarDrillTarget struct {
	Template gm.SpecialTemplate
	Grade    byte
}
type avatarDrillSource struct {
	Source            *gm.ItemTemplate
	NativeRestriction byte
}
type avatarActionPart struct {
	Id   uint32
	Hash string
	Kind byte
	Set  int
	Slot byte
}
type avatarInput struct {
	ResourceHash string
	WornSlot     uint16
	Grade        byte
	Clone        bool
}

type avatarEdits struct {
	AbilitySources map[uint32]abilitySource
	AbilityTargets map[uint32]avatarAbilityTarget
	DyeSources     map[uint32]dyeSource
	DyeTargets     map[uint32]avatarDyeTarget
	DrillSource    *avatarDrillSource
	DrillTargets   map[uint32]avatarDrillTarget
	ActionParts    map[uint32]avatarActionPart
	Disassembly    avatarDisassembly
	Compound       avatarCompound
	SkinTickets    map[uint32]*gm.ItemTemplate
	Conversions    storage.AvatarConversionCatalog
}

func buildAvatars(a *pvf.Archive, items ItemLookup, sourceIDs growthSources, emblems storage.EmblemCatalog) (json.RawMessage, error) {
	out := avatarEdits{
		AbilitySources: make(map[uint32]abilitySource), AbilityTargets: make(map[uint32]avatarAbilityTarget),
		DyeSources: make(map[uint32]dyeSource), DyeTargets: make(map[uint32]avatarDyeTarget),
		DrillTargets: make(map[uint32]avatarDrillTarget), ActionParts: make(map[uint32]avatarActionPart),
		SkinTickets: make(map[uint32]*gm.ItemTemplate),
	}
	abilityData, err := a.ReadText("skill/abilitydatas.dat", budget)
	if err != nil {
		return nil, err
	}
	cases, err := gm.AbilityCases(abilityData)
	if err != nil {
		return nil, err
	}
	caseText := make(map[uint32]string)
	for _, row := range avatarAbilityCaseRE.FindAllStringSubmatch(abilityData, -1) {
		id, err := strconv.ParseUint(row[1], 10, 32)
		if err != nil {
			return nil, err
		}
		caseText[uint32(id)] = row[2]
	}
	out.prepareSources(items, sourceIDs)
	paths, err := a.ReadIndex("equipment")
	if err != nil {
		return nil, err
	}
	compound := loadAvatarCompound(a, items, cases, sourceIDs.absolute)
	disassemble, err := loadAvatarDisassembly(a, items, emblems)
	if err != nil {
		return nil, err
	}
	conversions := loadAvatarConversionSources(items, sourceIDs.conversions)
	type entry struct {
		id            uint32
		path          string
		chunk, offset int
	}
	entries := make([]entry, 0, len(paths))
	for id, path := range paths {
		if id <= 0 || id >= math.MaxUint32 || !strings.Contains(strings.ToLower(path), "/avatar/") {
			continue
		}
		if chunk, offset, ok := a.PhysicalIndex(path); ok {
			entries = append(entries, entry{uint32(id), path, chunk, offset})
		}
	}
	slices.SortFunc(entries, func(a, b entry) int {
		if delta := a.chunk - b.chunk; delta != 0 {
			return delta
		}
		return a.offset - b.offset
	})
	qualified := make(map[uint32]*avatarInput)
	for _, row := range entries {
		src, err := gm.ReadItemSource(a, row.id, row.path, true)
		if err != nil {
			continue
		}
		script := readItemScript(src.Text)
		kind := script.get("equipment type")
		grade := script.number("grade", 255, 255, true)
		if script.err != nil {
			continue
		}
		match := avatarType.FindStringSubmatch(kind)
		avatar := match != nil && slices.Contains(avatarParts, match[1])
		if avatar && grade <= 4 {
			copyScript := *script
			if target, ok := abilityTarget(src, &copyScript, byte(grade), cases, caseText); ok {
				out.AbilityTargets[row.id] = target
			}
			copyScript = *script
			enabled := false
			if copyScript.count("enable dye") != 0 {
				flags := copyScript.numbers(copyScript.get("enable dye"))
				if len(flags) != 2 || flags[0] > 2 || flags[1] != 0 {
					copyScript.fail("unsupported dye flag")
				} else {
					enabled = flags[0] != 0
				}
			}
			if copyScript.err == nil {
				out.DyeTargets[row.id] = avatarDyeTarget{row.id, src.Hash, byte(grade), enabled}
			}
		}
		if grade == 4 && avatar && kind == "\x60["+match[1]+" avatar]\x60 0" {
			s := *script
			unique := s.number("unique avatar type", 255, 31, true)
			set := s.number("effect part set index", 0, 1014, true)
			part := slices.Index(avatarParts[:8], match[1])
			if s.err == nil && set >= 1000 && part >= 0 {
				out.ActionParts[row.id] = avatarActionPart{row.id, src.Hash, byte(unique), int(set), byte(part)}
			}
		}
		if candidate, ok := compoundInput(src, script, byte(grade), len(compound.Unique) > 0); ok {
			qualified[row.id] = candidate
		}
		if !avatar {
			continue
		}
		product, err := src.SpecialGift(cases, 4)
		if err != nil {
			continue
		}
		if grade == 3 && product.Template.WornSlot <= 7 && product.Template.InitialAvatarSocketCount > 0 {
			out.DrillTargets[row.id] = avatarDrillTarget{product.Template, byte(grade)}
		}
		disassemble.add(src, product, grade, script)
	}
	if len(compound.NormalPools) != 0 {
		ids := sortedTemplateIDs(qualified)
		for _, id := range ids[:min(32768, len(ids))] {
			if rule := qualified[id]; rule != nil {
				compound.Inputs[id] = *rule
			}
		}
	}
	disassemble.trim()
	out.Disassembly, out.Compound = disassemble.avatarDisassembly, compound
	out.Conversions = completeAvatarConversions(items, cases, conversions)
	return json.Marshal(out)
}

func abilityTarget(src gm.ItemSource, s *itemScript, grade byte, cases map[uint32]map[byte]uint16, caseText map[uint32]string) (avatarAbilityTarget, bool) {
	var out avatarAbilityTarget
	if s.count("avatar select ability") == 0 && s.count("ability case index") == 0 {
		return out, false
	}
	choices, err := gm.SelectedAbilities(src.Text, cases)
	if err != nil {
		return out, false
	}
	var selected string
	for _, f := range s.fields {
		switch f.name {
		case "avatar select ability":
			selected = f.value
		case "ability case index":
			id, err := strconv.ParseUint(f.value, 10, 32)
			if err != nil || caseText[uint32(id)] == "" {
				return out, false
			}
			selected = caseText[uint32(id)]
		}
	}
	skills := make(map[byte]uint16)
	for _, row := range avatarSkillRE.FindAllStringSubmatch(selected, -1) {
		ability, abilityErr := strconv.ParseUint(row[1], 10, 8)
		skill, skillErr := strconv.ParseUint(row[2], 10, 9)
		if abilityErr != nil || skillErr != nil || skill == 0 || choices[byte(ability)] == 0 || skills[byte(ability)] != 0 {
			return out, false
		}
		skills[byte(ability)] = uint16(skill)
	}
	ids := make([]int, 0, len(choices))
	for k := range choices {
		ids = append(ids, int(k))
	}
	slices.Sort(ids)
	return avatarAbilityTarget{src.Id, src.Hash, grade, ids, choices, skills}, true
}

func compoundInput(src gm.ItemSource, base *itemScript, grade byte, clones bool) (*avatarInput, bool) {
	s := *base
	label := generalAvatarType.FindStringSubmatch(s.get("equipment type"))
	if len(label) != 2 {
		return nil, false
	}
	slot := slices.Index(avatarParts, strings.ToLower(label[1]))
	if slot < 0 {
		return nil, false
	}
	for _, f := range s.fields {
		if !strings.HasPrefix(f.name, "/") && avatarPlainForbidden.MatchString(f.name) {
			return nil, false
		}
	}
	var level uint64
	for _, f := range s.fields {
		if f.name == "minimum level" {
			n, err := strconv.ParseUint(f.value, 10, 8)
			if err != nil || n == 0 || level > 0 && n != level {
				return nil, false
			}
			level = n
		}
	}
	if s.get("usable job") != "" {
		s.jobs("usable job", false)
	}
	durability := s.number("durability", 45, math.MaxUint16, false)
	if durability == 0 || s.err != nil {
		return nil, false
	}
	attach := s.get("attach type")
	forbidden := slices.ContainsFunc(s.fields, func(f giftField) bool { return avatarCompoundForbidden.MatchString(f.name) })
	if s.err != nil {
		return nil, false
	}
	if grade < 1 || grade > 3 || !anyOf(attach, "\x60[trade]\x60", "\x60[avatar trade]\x60") || forbidden {
		return nil, true
	}
	return &avatarInput{src.Hash, uint16(slot), grade, clones && grade == 3 && strings.Contains(s.get("item category"), "\x60clear avatar\x60")}, true
}

func permanentSource(t *gm.ItemTemplate) bool {
	return t != nil && t.ResourceExpiration == nil && t.RelativeLifetimeSeconds == 0 && !t.DailyDeletion && t.WeeklyDeletionDay == nil
}

func (out *avatarEdits) prepareSources(items ItemLookup, ids growthSources) {
	for _, id := range ids.ability {
		src, err := items.Source(id)
		if err != nil {
			continue
		}
		s := readItemScript(src.Text)
		s.allowed(strings.Split("name|explain|grade|rarity|usable job|attach type|minimum level|icon|field image|stackable type|move wav|avatar select ability change|impossible contents|stack limit|need material", "|"), true, true)
		s.balanced("usable job", "impossible contents", "need material")
		tuple := avatarAbilitySourceRE.FindStringSubmatch(s.get("avatar select ability change"))
		t, err := src.Gift()
		if err != nil || !permanentSource(t) || s.err != nil || len(tuple) != 3 || s.get("usable job") != "\x60[all]\x60" ||
			s.get("minimum level") != "1" || s.get("stackable type") != "\x60[etc]\x60 0" || s.get("attach type") != "\x60[account]\x60" ||
			!anyOf(s.get("impossible contents"), "", "\x60gift\x60") || t.FirstBagSlot != 65 || t.LastBagSlot != 120 ||
			!t.PvfGiftItem || t.InitialSealed || t.AccountAfterFirstTrade {
			continue
		}
		s.purchaseMaterial(math.MaxInt32, false)
		if s.err == nil {
			out.AbilitySources[id] = abilitySource{t, uint32(tuple[1][0] - '0'), tuple[2] == "1"}
		}
	}
	for _, id := range ids.dyes {
		src, err := items.Source(id)
		if err != nil {
			continue
		}
		s := readItemScript(src.Text)
		s.allowed(strings.Split("name|explain|grade|rarity|usable job|attach type|minimum level|icon|stackable type|move wav|stack limit|dye type|dye info|impossible contents|item category|price", "|"), true, true)
		s.balanced("usable job", "dye info", "impossible contents", "item category")
		grade, rarity := s.number("grade", 0, 3, false), s.number("rarity", 0, 2, true)
		s.number("price", 0, math.MaxInt32, false)
		info := s.numbers(s.get("dye info"))
		t, err := src.Gift()
		if err != nil || !permanentSource(t) || s.err != nil || rarity == 0 || len(info) != 2 || info[0] > 47 || info[1] != 1000 ||
			s.get("stackable type") != "\x60[dye]\x60 0" || s.get("usable job") != "\x60[all]\x60" || s.get("minimum level") != "1" ||
			s.get("stack limit") != "1000" || !anyOf(s.get("attach type"), "\x60[free]\x60", "\x60[account]\x60") || s.get("dye type") != "1" ||
			s.get("item category") != "" || !anyOf(s.get("impossible contents"), "", "\x60gift\x60") ||
			t.Kind != 2 || t.FirstBagSlot != 65 || t.LastBagSlot != 120 || t.StackLimit != 1000 || t.MinimumLevel != 1 ||
			t.UsableJobs != math.MaxUint16 || !t.PvfGiftItem || t.InitialSealed || t.AccountAfterFirstTrade {
			continue
		}
		out.DyeSources[id] = dyeSource{t, grade, info[0]}
	}
	if src, err := items.Source(7421); err == nil {
		s := readItemScript(src.Text)
		if s.get("action type") == "\x60[add avatar socket]\x60 2" && s.get("stackable type") == "\x60[waste]\x60 0" &&
			s.get("stack limit") == "10" && s.get("minimum level") == "1" && s.get("usable job") == "\x60[all]\x60" && s.err == nil {
			if t, err := src.Gift(); err == nil && permanentSource(t) && t.Kind == 2 && t.FirstBagSlot == 65 && t.LastBagSlot == 120 {
				out.DrillSource = &avatarDrillSource{t, 2}
			}
		}
	}
	for _, id := range []uint32{490700411, 490020769, 490022060, 490703887} {
		src, err := items.Source(id)
		if err != nil {
			continue
		}
		s := readItemScript(src.Text)
		if id == 490700411 {
			if s.get("stackable type") != "\x60[material]\x60 0" || s.get("stack limit") != "1" ||
				s.count("action type") != 0 || s.count("use action packet") != 0 {
				continue
			}
		} else if s.get("stackable type") != "\x60[etc]\x60 1" || s.get("action type") != "\x60[open creature skin slot]\x60" || s.get("use action packet") != "0" {
			continue
		}
		if t, err := src.Gift(); err == nil && s.err == nil && (id != 490700411 || t.Kind == 2 && t.FirstBagSlot == 121 && t.LastBagSlot == 176 && t.StackLimit == 1) {
			out.SkinTickets[id] = t
		}
	}
}
