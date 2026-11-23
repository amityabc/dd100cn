package gm

import (
	"errors"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var cashAvatarPriceRow = regexp.MustCompile("(?m)^[ \\t]*(\\d+)[ \\t]+(\\d+)[ \\t]+(\\d+)[ \\t]+(\\d+)[ \\t]+(\\d+)[ \\t]+(\\d+)[ \\t]+(\\d+)(?:[ \\t]+\x60\\[[^\\]]+\\]\x60)*")

func (r ItemSource) Clone() (AvatarClone, bool) {
	s, err := parseScript(r.Text)
	if err != nil {
		return AvatarClone{}, false
	}
	return cloneTemplate(r.Id, s, r.Hash)
}

// Cash SKUs select a priced equipment model, while gift boxes select their
// own socket model. Prices do not participate in the gift acquisition parser.
func (r ItemSource) CashAvatar(model byte, cases map[uint32]map[byte]uint16) (SpecialTemplate, uint32, map[byte]uint16, error) {
	var t SpecialTemplate
	bad := errors.New("unsupported cash avatar SKU")
	if model < 1 || model > 3 || !r.Equipment {
		return t, 0, nil, bad
	}
	s, err := parseScript(r.Text)
	if err != nil {
		return t, 0, nil, err
	}
	attach, err := s.section("attach type")
	if err != nil || attach != "\x60[trade]\x60" && attach != "\x60[avatar trade]\x60" {
		return t, 0, nil, bad
	}
	for _, f := range s.fields {
		if strings.HasPrefix(f.name, "/") {
			continue
		}
		if f.name == "daily delete item" || f.name == "weekly delete item" || f.name == "monthly delete item" || specialUnsupported.MatchString(f.name) || strings.Contains(f.name, "usable period") || strings.Contains(f.name, "expiration") {
			return t, 0, nil, bad
		}
	}
	typ, err := s.section("equipment type")
	if err != nil {
		return t, 0, nil, err
	}
	m := equipmentLabel.FindStringSubmatch(typ)
	if m == nil || !strings.HasSuffix(m[1], " avatar") {
		return t, 0, nil, bad
	}
	slot := slices.Index(avatarParts, strings.TrimSuffix(strings.ToLower(m[1]), " avatar"))
	if slot < 0 {
		return t, 0, nil, bad
	}
	prices, err := s.section("avatar type select")
	if err != nil {
		return t, 0, nil, err
	}
	// Cash prices consume one physical row per model. Gift socket models
	// use a different grammar and may be packed onto a single line.
	rows := cashAvatarPriceRow.FindAllStringSubmatchIndex(prices, -1)
	if len(rows) < int(model) || strings.TrimSpace(prices[rows[len(rows)-1][1]:]) != "" {
		return t, 0, nil, bad
	}
	row := rows[int(model)-1]
	var v [7]string
	for i := range v {
		v[i] = prices[row[2*i+2]:row[2*i+3]]
	}
	days := []uint32{7, 30, 0}[model-1]
	if v[0] != strconv.Itoa(int(days)) || v[1] != "0" || v[2] != "0" || v[4] != "0" || v[5] != "0" || v[6] != "0" {
		return t, 0, nil, bad
	}
	price, err := exactUint(v[3], 31)
	if err != nil || price == 0 {
		return t, 0, nil, bad
	}
	t = SpecialTemplate{TemplateId: r.Id, WornSlot: uint16(slot), MinimumLevel: 1, MaximumDurability: 45, UsableJobs: math.MaxUint16, RelativeLifetimeSeconds: days * 86400, ResourceHash: r.Hash}
	for i, level := range s.values("minimum level") {
		n, e := exactUint(level, 8)
		if e != nil || n == 0 || i > 0 && uint64(t.MinimumLevel) != n {
			return t, 0, nil, bad
		}
		t.MinimumLevel = byte(n)
	}
	du, err := s.section("durability")
	if err != nil {
		return t, 0, nil, err
	}
	d, err := parseInteger(du, 1, math.MaxUint16, 45)
	if err != nil {
		return t, 0, nil, err
	}
	t.MaximumDurability = uint16(d)
	jobs, err := s.section("usable job")
	if err != nil {
		return t, 0, nil, err
	}
	if jobs != "" {
		t.UsableJobs, err = parseJobs(jobs)
		if err != nil {
			return t, 0, nil, err
		}
	}
	abilities, err := selectedAbilitiesMode(s, cases, false)
	return t, uint32(price), abilities, err
}
