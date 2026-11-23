package gm

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

type AvatarClone struct {
	Id      uint32
	Hash    string
	Slot    uint16
	IsClone bool
}
type socketLayout struct{ First, Second, Third, Fourth, Fifth uint16 }

type SpecialTemplate struct {
	TemplateId                  uint32
	Kind                        byte
	WornSlot, MaximumDurability uint16
	MinimumLevel                byte
	UsableJobs                  uint16
	ResourceHash                string
	InitialAvatarSocketCount    byte
	InitialAvatarSocketType     uint16
	InitialAvatarSocketLayout   *socketLayout
	ResourceExpiration          *int64
	RelativeLifetimeSeconds     uint32
	DailyDeletion               bool
	WeeklyDeletionDay           *byte
	InitialSealed               bool
}

type SpecialProduct struct {
	Template       SpecialTemplate
	Abilities      map[byte]uint16
	Model          uint32
	ResourcePeriod int64
	AttachType     byte
	InitialPetName string
}

var (
	socketTypes        = map[string]uint16{"A socket": 1, "B socket": 2, "C socket": 4, "D socket": 8, "S socket": 16, "M socket": 0xFFEF}
	avatarParts        = []string{"hat", "hair", "face", "coat", "pants", "shoes", "breast", "waist", "skin", "aurora", "weapon", "aura skin"}
	cloneType          = regexp.MustCompile("^\x60\\[([^\\]]+) avatar\\]\\x60(?:\\s+0)?$")
	cloneCategory      = regexp.MustCompile("(?:^|\\s)\x60clear avatar\x60(?:\\s|$)")
	specialUnsupported = regexp.MustCompile("(?i)expiration|expiry|usable period|required skill|required job skill|package|contract")
	abilityNumber      = regexp.MustCompile("^-?[0-9]+(?:\\.[0-9]+)?$")
)

func cloneTemplate(id uint32, s script, hash string) (AvatarClone, bool) {
	typ, err := s.section("equipment type")
	match := cloneType.FindStringSubmatch(typ)
	if err != nil || match == nil || id == 0 || id == math.MaxUint32 {
		return AvatarClone{}, false
	}
	slot := slices.Index(avatarParts[:8], match[1])
	category, err := s.section("item category")
	if slot < 0 || err != nil {
		return AvatarClone{}, false
	}
	return AvatarClone{id, hash, uint16(slot), cloneCategory.MatchString(category)}, true
}

func parseSpecial(id uint32, s script, hash string, cases map[uint32]map[byte]uint16) (*SpecialProduct, error) {
	return parseSpecialModel(id, s, hash, cases, 0)
}

func parseSpecialModel(id uint32, s script, hash string, cases map[uint32]map[byte]uint16, model uint32) (*SpecialProduct, error) {
	typeName, err := s.section("equipment type")
	if err != nil {
		return nil, err
	}
	match := equipmentLabel.FindStringSubmatch(typeName)
	if match == nil {
		return nil, errors.New("Special equipment type grammar is unsupported")
	}
	label := strings.ToLower(match[1])
	t := SpecialTemplate{TemplateId: id, ResourceHash: hash, UsableJobs: math.MaxUint16}
	if avatar := slices.Index(avatarParts, strings.TrimSuffix(label, " avatar")); avatar >= 0 && strings.HasSuffix(label, " avatar") {
		t.WornSlot = uint16(avatar)
	} else {
		switch label {
		case "creature":
			t.Kind, t.WornSlot = 1, 26
		case "artifact red":
			t.Kind, t.WornSlot = 2, 27
		case "artifact blue":
			t.Kind, t.WornSlot = 2, 28
		case "artifact green":
			t.Kind, t.WornSlot = 2, 29
		default:
			return nil, errors.New("Unknown special detail owner")
		}
	}
	period, err := s.section("usable period")
	if err != nil {
		return nil, err
	}
	if model == 0 {
		t.RelativeLifetimeSeconds, err = parseLifetime(period)
		if err != nil {
			return nil, err
		}
	}
	p := &SpecialProduct{Template: t, Abilities: map[byte]uint16{}}
	if period != "" {
		p.ResourcePeriod, err = strconv.ParseInt(period, 10, 32)
		if err != nil {
			return nil, errors.New("Gift special relative period grammar is unsupported")
		}
	}
	expiration, err := s.section("expiration date")
	if err != nil {
		return nil, err
	}
	p.Template.ResourceExpiration, err = parseExpiration(expiration, true)
	if err != nil {
		return nil, err
	}
	p.Template.DailyDeletion, p.Template.WeeklyDeletionDay, _, err = parseDeletion(s, true)
	if err != nil {
		return nil, err
	}
	for _, f := range s.fields {
		switch f.name {
		case "usable period", "expiration date", "daily delete item", "weekly delete item":
			continue
		}
		if !strings.HasPrefix(f.name, "/") && specialUnsupported.MatchString(f.name) {
			return nil, errors.New("Special template requires unsupported time or permission rules")
		}
	}
	level := uint64(1)
	for i, v := range s.values("minimum level") {
		next, err := exactUint(v, 8)
		if err != nil || next == 0 || i > 0 && next != level {
			return nil, errors.New("Special minimum level is invalid or conflicting")
		}
		level = next
	}
	p.Template.MinimumLevel = byte(level)
	jobs, err := s.section("usable job")
	if err != nil {
		return nil, err
	}
	if jobs != "" {
		p.Template.UsableJobs, err = parseJobs(jobs)
		if err != nil {
			return nil, err
		}
	}
	if t.Kind != 1 {
		d, err := s.section("durability")
		if err != nil {
			return nil, err
		}
		n, err := parseInteger(d, 1, math.MaxUint16, 45)
		if err != nil {
			return nil, errors.New("Special template integer is invalid")
		}
		p.Template.MaximumDurability = uint16(n)
	}
	switch t.Kind {
	case 0:
		if err := selectAvatarModel(p, s, model); err != nil {
			return nil, err
		}
		p.Abilities, err = selectedAbilitiesMode(s, cases, model == 0)
		if err != nil {
			return nil, err
		}
	case 1:
		if model != 0 {
			return nil, errors.New("Gift pet cannot use an avatar creation model")
		}
		name, err := s.section("name")
		if err != nil {
			return nil, err
		}
		p.InitialPetName = strings.Trim(name, "`")
		for {
			gb, err := simplifiedchinese.GB18030.NewEncoder().String(p.InitialPetName)
			if err != nil {
				return nil, err
			}
			if len(gb) <= 29 {
				break
			}
			_, n := utf8.DecodeLastRuneInString(p.InitialPetName)
			p.InitialPetName = p.InitialPetName[:len(p.InitialPetName)-n]
		}
	}
	if t.Kind == 2 && model != 0 {
		return nil, errors.New("Gift artifact cannot use an avatar creation model")
	}
	attach, err := s.section("attach type")
	if err != nil {
		return nil, err
	}
	switch attach {
	case "", "`[free]`":
		p.AttachType = 0
	case "`[trade]`", "`[avatar trade]`":
		p.AttachType = 1
	case "`[trade delete]`":
		p.AttachType = 2
	case "`[sealing]`":
		p.AttachType, p.Template.InitialSealed = 3, true
	case "`[sealing trade]`":
		p.AttachType, p.Template.InitialSealed = 4, true
	case "`[account]`":
		p.AttachType = 5
	default:
		return nil, errors.New("Gift special attachment requires its verified native mapping")
	}
	if id == 0 || p.Template.Kind > 2 || p.Template.WornSlot > 37 {
		return nil, errors.New("Special template is invalid")
	}
	return p, nil
}

type avatarModel struct {
	values [7]string
	labels []string
}

func selectAvatarModel(p *SpecialProduct, s script, requested uint32) error {
	plans, err := s.section("avatar type select")
	if err != nil {
		return err
	}
	models, err := avatarModels(plans)
	if err != nil {
		return err
	}
	var explicit *avatarModel
	if requested != 0 {
		if requested <= uint32(len(models)) {
			model := models[requested-1]
			if model.values[1] != "0" {
				return errors.New("Gift avatar creation model carries a gold price")
			}
			explicit = &model
		} else if requested == 4 && (p.Template.WornSlot == 9 || p.Template.WornSlot == 10) ||
			requested == 3 && p.Template.WornSlot == 9 && len(models) == 0 && !s.has("avatar type select") {
			if len(models) > 0 {
				if len(models) < 3 {
					return errors.New("Gift avatar lacks its permanent creation model")
				}
				for _, field := range []int{0, 1, 2, 4, 5, 6} {
					if models[2].values[field] != "0" {
						return errors.New("Gift avatar permanent model flags are unsupported")
					}
				}
			}
			block, err := s.section("emblem socket default")
			if err != nil {
				return err
			}
			labels, err := quotedLabels(block)
			if err != nil {
				return err
			}
			explicit = &avatarModel{[7]string{"0", "0", "0", "0", "0", "0", strconv.Itoa(len(labels))}, labels}
		} else {
			return errors.New("Gift avatar creation model is absent")
		}
	}
	if len(models) == 0 && explicit == nil {
		block, err := s.section("emblem socket default")
		if err != nil {
			return err
		}
		labels, err := quotedLabels(block)
		if err != nil {
			return errors.New("Gift avatar initial socket layout is malformed")
		}
		models = []avatarModel{{[7]string{"0", "0", "0", "0", "0", "0", strconv.Itoa(len(labels))}, labels}}
	}
	declared, err := s.section("avatar emblem socket num")
	if err != nil {
		return err
	}
	var model avatarModel
	if explicit != nil {
		model, p.Model = *explicit, requested
	} else {
		selected := 0
		matches := func(i int) bool {
			return models[i].values[0] == "0" && (declared == "" || models[i].values[6] == declared)
		}
		if len(models) > 2 && matches(2) {
			selected = 2
		} else {
			for i := range models {
				if matches(i) {
					selected = i
					break
				}
			}
		}
		model, p.Model = models[selected], uint32(selected)+1
	}
	for _, index := range []int{2, 4, 5} {
		if model.values[index] != "0" {
			return errors.New("Gift avatar creation model flags are unsupported")
		}
	}
	period, err := exactUint(model.values[0], 31)
	if err != nil {
		return errors.New("Gift avatar creation period is invalid")
	}
	if period > 0 {
		if p.ResourcePeriod != 0 && p.ResourcePeriod != int64(period) {
			return errors.New("Gift avatar period differs from its creation model")
		}
		p.ResourcePeriod = int64(period)
		if period > math.MaxInt32/86400 {
			return errors.New("Gift avatar model lifetime exceeds the native seconds range")
		}
		lifetime := uint32(period * 86400)
		if p.Template.RelativeLifetimeSeconds != 0 && p.Template.RelativeLifetimeSeconds != lifetime {
			return errors.New("Gift avatar model conflicts with its own acquisition lifetime")
		}
		p.Template.RelativeLifetimeSeconds = lifetime
	}
	count, err := exactUint(model.values[6], 32)
	if err != nil {
		return errors.New("Gift avatar socket count is invalid")
	}
	if count > 5 || int(count) != len(model.labels) {
		return errors.New("Gift avatar initial socket layout is malformed")
	}
	if declared != "" {
		expected, err := exactUint(declared, 32)
		if err != nil || expected != count {
			return errors.New("Gift avatar socket count differs from its explicit declaration")
		}
	}
	var types [5]uint16
	var mixed bool
	for i, label := range model.labels {
		socket, ok := socketTypes[label]
		if !ok {
			return errors.New("Gift avatar initial socket type is unknown")
		}
		types[i] = socket
		mixed = mixed || socket != types[0]
	}
	p.Template.InitialAvatarSocketCount = byte(count)
	if mixed {
		p.Template.InitialAvatarSocketLayout = &socketLayout{types[0], types[1], types[2], types[3], types[4]}
	} else {
		p.Template.InitialAvatarSocketType = types[0]
	}
	return nil
}

func avatarModels(raw string) ([]avatarModel, error) {
	tokens, err := scriptTokens(raw)
	if err != nil {
		return nil, errors.New("Gift avatar creation models are malformed")
	}
	var models []avatarModel
	for len(tokens) > 0 {
		var model avatarModel
		if len(tokens) < 7 {
			return nil, errors.New("Gift avatar creation models are malformed")
		}
		for i := range 7 {
			if !tradeMaximum.MatchString(tokens[i]) {
				return nil, errors.New("Gift avatar creation models are malformed")
			}
			model.values[i] = tokens[i]
		}
		tokens = tokens[7:]
		for len(tokens) > 0 && strings.HasPrefix(tokens[0], "`") {
			label, ok := quotedLabel(tokens[0])
			if !ok {
				return nil, errors.New("Gift avatar creation models are malformed")
			}
			model.labels = append(model.labels, label)
			tokens = tokens[1:]
		}
		models = append(models, model)
	}
	return models, nil
}

func quotedLabels(src string) ([]string, error) {
	tokens, err := scriptTokens(src)
	if err != nil {
		return nil, err
	}
	for i, t := range tokens {
		label, ok := quotedLabel(t)
		if !ok {
			return nil, errors.New("PVF field does not contain a complete label list")
		}
		tokens[i] = label
	}
	return tokens, nil
}

func quotedLabel(s string) (string, bool) {
	match := quotedTags.FindStringSubmatch(s)
	if match == nil || match[0] != s {
		return "", false
	}
	return match[1], true
}

func scriptTokens(src string) ([]string, error) {
	var tokens []string
	for src = strings.TrimSpace(src); src != ""; src = strings.TrimSpace(src) {
		if src[0] == '`' {
			i := strings.IndexByte(src[1:], '`')
			if i < 0 {
				return nil, errors.New("unterminated PVF token")
			}
			tokens = append(tokens, src[:i+2])
			src = src[i+2:]
			continue
		}
		end := strings.IndexAny(src, " \t\r\n\v\f")
		if end < 0 {
			end = len(src)
		}
		tokens = append(tokens, src[:end])
		src = src[end:]
	}
	return tokens, nil
}

func parseCases(text string) (map[uint32]map[byte]uint16, error) {
	s, err := parseScript(text)
	if err != nil {
		return nil, err
	}
	out := map[uint32]map[byte]uint16{}
	for i, f := range s.fields {
		if !strings.EqualFold(f.name, "ability case") || i+1 >= len(s.fields) || !strings.EqualFold(s.fields[i+1].name, "/ability case") {
			continue
		}
		tokens, err := scriptTokens(f.value)
		if err != nil || len(tokens) < 2 {
			return nil, errors.New("Cash avatar ability case header is invalid")
		}
		id, err := exactUint(tokens[0], 32)
		if err != nil {
			return nil, errors.New("Cash avatar ability case header is invalid")
		}
		if _, ok := out[uint32(id)]; ok {
			return nil, errors.New("Cash avatar ability case is duplicated")
		}
		out[uint32(id)], err = parseAbilities(tokens[1:], false, false)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func selectedAbilities(s script, cases map[uint32]map[byte]uint16) (map[byte]uint16, error) {
	return selectedAbilitiesMode(s, cases, true)
}

func selectedAbilitiesMode(s script, cases map[uint32]map[byte]uint16, allowDuplicate bool) (map[byte]uint16, error) {
	caseIDs, inline := s.values("ability case index"), s.values("avatar select ability")
	if len(caseIDs) > 0 {
		var id uint64
		for i, raw := range caseIDs {
			value, err := exactUint(raw, 32)
			if err != nil || i > 0 && value != id {
				return nil, errors.New("Cash avatar shared ability case is invalid or conflicting")
			}
			id = value
		}
		if len(inline) > 0 || cases[uint32(id)] == nil {
			return nil, errors.New("Cash avatar shared ability case is absent or ambiguous")
		}
		return cases[uint32(id)], nil
	}
	if len(inline) == 0 {
		return map[byte]uint16{0: math.MaxUint16}, nil
	}
	var abilities map[byte]uint16
	for _, field := range s.fields {
		if field.name != "avatar select ability" {
			continue
		}
		tokens, err := scriptTokens(field.value)
		if err != nil {
			return nil, err
		}
		abilities, err = parseAbilities(tokens, field.end == s.length, allowDuplicate)
		if err != nil {
			return nil, err
		}
	}
	return abilities, nil
}

func parseAbilities(tokens []string, allowTrailing, allowDuplicate bool) (map[byte]uint16, error) {
	result, err := completeAbilities(tokens, allowDuplicate)
	if err == nil || !allowTrailing || len(tokens) < 2 {
		return result, err
	}
	n, numberErr := exactUint(tokens[len(tokens)-1], 8)
	if numberErr != nil || n == 255 {
		return nil, err
	}
	result, err = completeAbilities(tokens[:len(tokens)-1], allowDuplicate)
	if err != nil {
		return nil, err
	}
	if _, exists := result[byte(n)]; exists {
		return nil, errors.New("Cash avatar default-none ability ID is duplicated")
	}
	result[byte(n)] = math.MaxUint16
	return result, nil
}

func completeAbilities(tokens []string, allowDuplicate bool) (map[byte]uint16, error) {
	out := map[byte]uint16{}
	grammar := errors.New("Cash avatar selectable abilities grammar is invalid")
	for len(tokens) > 0 {
		if len(tokens) < 4 {
			return nil, grammar
		}
		id, err := exactUint(tokens[0], 8)
		if err != nil || id == 255 {
			return nil, errors.New("Cash avatar ability ID is outside the request field")
		}
		kind, ok := quotedLabel(tokens[1])
		if !ok || len(tokens[2]) < 3 || tokens[2][0] != '`' || tokens[2][len(tokens[2])-1] != '`' || !abilityNumber.MatchString(tokens[3]) {
			return nil, grammar
		}
		op := tokens[2][1 : len(tokens[2])-1]
		tokens = tokens[4:]
		extra := len(tokens) > 0 && tradeMaximum.MatchString(tokens[0]) &&
			(len(tokens) == 1 || len(tokens) > 2 && tradeMaximum.MatchString(tokens[1]) && strings.HasPrefix(tokens[2], "`"))
		jobs := uint16(math.MaxUint16)
		if kind == "SKILL_LEVEL" {
			if !extra {
				return nil, errors.New("Cash avatar skill ability is incomplete")
			}
			if op != "[common]" {
				jobs, err = parseJobs("`" + op + "`")
				if err != nil {
					return nil, err
				}
			}
		} else if extra || op != "+" && op != "%" && op != "=" {
			return nil, errors.New("Cash avatar ability operator is unsupported")
		}
		if extra {
			tokens = tokens[1:]
		}
		if _, exists := out[byte(id)]; exists {
			if !allowDuplicate {
				return nil, errors.New("Cash avatar ability ID is duplicated")
			}
		} else {
			out[byte(id)] = jobs
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Cash avatar selectable abilities are absent")
	}
	return out, nil
}
