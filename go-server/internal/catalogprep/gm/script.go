package gm

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type field struct {
	name, value string
	end         int
}
type script struct {
	fields []field
	length int
}

func parseScript(text string) (script, error) {
	text = strings.ReplaceAll(text, "\r", "")
	type marker struct {
		name       string
		start, end int
	}
	var tags []marker
	for at := 0; at < len(text); at++ {
		if text[at] == '`' {
			next := strings.IndexByte(text[at+1:], '`')
			if next < 0 {
				return script{}, errors.New("Gift resource has an unterminated string")
			}
			at += next + 1
			continue
		}
		if text[at] != '[' || at > 0 && text[at-1] != '\n' {
			continue
		}
		line := strings.IndexByte(text[at:], '\n')
		if line < 0 {
			line = len(text) - at
		}
		row := text[at : at+line]
		close := strings.IndexByte(row, ']')
		if close < 0 || strings.TrimSpace(row[close+1:]) != "" {
			continue
		}
		end := at + line
		if end < len(text) {
			end++
		}
		tags = append(tags, marker{row[1:close], at, end})
		at = end - 2
		if end == len(text) {
			break
		}
	}
	out := script{fields: make([]field, 0, len(tags)), length: len(text)}
	for i, tag := range tags {
		end := len(text)
		if i+1 < len(tags) {
			end = tags[i+1].start
		}
		out.fields = append(out.fields, field{tag.name, strings.TrimSpace(text[tag.end:end]), end})
	}
	return out, nil
}

func (s script) section(name string) (string, error) {
	found := false
	var value string
	for _, f := range s.fields {
		if f.name != name {
			continue
		}
		if found && (name != "attach type" || value != f.value) {
			return "", fmt.Errorf("Ambiguous gift resource field: %s", name)
		}
		found, value = true, f.value
	}
	return value, nil
}

func (s script) values(name string) []string {
	var out []string
	for _, f := range s.fields {
		if f.name == name {
			out = append(out, f.value)
		}
	}
	return out
}

func (s script) get(name string) string {
	for _, f := range s.fields {
		if f.name == name {
			return f.value
		}
	}
	return ""
}

func (s script) has(name string) bool {
	for _, f := range s.fields {
		if f.name == name {
			return true
		}
	}
	return false
}

func (s script) normalized() script {
	var values = make(map[string]string)
	ambiguous := make(map[string]bool)
	for _, f := range s.fields {
		switch f.name {
		case "equipment type", "stackable type", "minimum level", "usable job", "durability", "stack limit", "attach type",
			"trade limit", "trade limit max", "usable period", "expiration date", "daily delete item", "weekly delete item":
			if prev, exists := values[f.name]; exists && prev != f.value {
				ambiguous[f.name] = true
			}
			values[f.name] = f.value
		}
	}
	out := script{fields: make([]field, 0, len(s.fields)), length: s.length}
	for i := len(s.fields) - 1; i >= 0; i-- {
		f := s.fields[i]
		if !ambiguous[f.name] {
			if v, ok := values[f.name]; ok {
				if v == "\x00" {
					continue
				}
				values[f.name] = "\x00"
			}
		}
		out.fields = append(out.fields, f)
	}
	for i, j := 0, len(out.fields)-1; i < j; i, j = i+1, j-1 {
		out.fields[i], out.fields[j] = out.fields[j], out.fields[i]
	}
	return out
}

func parseWeight(s script) (int64, error) {
	var last uint64
	for _, f := range s.fields {
		if f.name != "weight" {
			continue
		}
		grams, err := exactUint(f.value, 31)
		if err != nil {
			return 0, errors.New("Ordinary item weight metadata is invalid")
		}
		last = grams
	}
	return int64(last), nil
}

func exactUint(s string, bits int) (uint64, error) {
	for _, b := range s {
		if b < '0' || b > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	return strconv.ParseUint(s, 10, bits)
}

func parseInteger(s string, min, max, fallback uint64) (uint64, error) {
	if s == "" {
		return fallback, nil
	}
	v, err := exactUint(s, 32)
	if err != nil || v < min || v > max {
		return 0, errors.New("Ordinary item integer is invalid")
	}
	return v, nil
}

var (
	quotedTags      = regexp.MustCompile("`\\[([^\\]]+)\\]`")
	absoluteGrammar = regexp.MustCompile("^`[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}`$")
	utcPlus8        = time.FixedZone("UTC+8", 8*3600)
)

func parseExpiration(s string, special bool) (*int64, error) {
	if s == "" {
		return nil, nil
	}
	where := "Gift"
	if special {
		where += " special"
	}
	if !absoluteGrammar.MatchString(s) {
		return nil, fmt.Errorf("%s absolute expiration grammar is unsupported", where)
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05", s[1:len(s)-1], utcPlus8)
	if err != nil {
		return nil, fmt.Errorf("%s absolute expiration grammar is unsupported", where)
	}
	seconds := at.Unix()
	if seconds <= 0 || seconds > math.MaxInt32 {
		return nil, fmt.Errorf("%s absolute expiration is outside the native core range", where)
	}
	return &seconds, nil
}

func parseLifetime(s string) (uint32, error) {
	if s == "" {
		return 0, nil
	}
	days, err := exactUint(s, 32)
	if err != nil || days > math.MaxInt32/86400 {
		return 0, errors.New("Gift relative lifetime days exceed the native expiration range")
	}
	return uint32(days * 86400), nil
}

func parseDeletion(s script, special bool) (bool, *byte, bool, error) {
	daily := s.has("daily delete item")
	if daily {
		value, err := s.section("daily delete item")
		if err != nil {
			return false, nil, false, err
		}
		if value != "1" {
			kind := "Gift"
			if special {
				kind += " special"
			}
			return false, nil, false, fmt.Errorf("%s daily deletion mode is unsupported", kind)
		}
	}
	var weekly *byte
	if s.has("weekly delete item") {
		value, err := s.section("weekly delete item")
		if err != nil {
			return false, nil, false, err
		}
		day, err := exactUint(value, 8)
		if err != nil || day > 6 {
			return false, nil, false, errors.New("Gift weekly deletion day must be a native weekday0..6")
		}
		typed := byte(day)
		weekly = &typed
	}
	monthly := s.has("fatiguetime monthly delete")
	if special && s.has("monthly delete item") {
		return false, nil, false, errors.New("Gift special monthly deletion calendar is unverified")
	}
	if daily && weekly != nil {
		return false, nil, false, errors.New("Gift combines daily and weekly deletion calendars")
	}
	if monthly && (daily || weekly != nil) {
		return false, nil, false, errors.New("Gift combines incompatible monthly deletion calendars")
	}
	return daily, weekly, monthly, nil
}

var jobLabels = []string{"swordman", "fighter", "gunner", "mage", "priest", "at gunner", "thief", "at fighter", "at mage",
	"demonic swordman", "creator mage", "at swordman", "knight", "demonic lancer", "at priest", "gun blader"}

func parseJobs(s string) (uint16, error) {
	if s == "" {
		return 0, errors.New("Item usable-job policy absent")
	}
	var jobs uint16
	for s != "" {
		m := quotedTags.FindStringSubmatchIndex(s)
		if m == nil || m[0] != 0 {
			return 0, errors.New("Item usable-job grammar unsupported")
		}
		name := strings.ToLower(s[m[2]:m[3]])
		if name == "all" {
			jobs = math.MaxUint16
		} else {
			ordinal := jobOrdinal(name)
			if ordinal < 0 {
				return 0, errors.New("Item usable-job label unknown")
			}
			jobs |= 1 << ordinal
		}
		s = strings.TrimSpace(s[m[1]:])
	}
	return jobs, nil
}

func jobOrdinal(name string) int {
	for i, label := range jobLabels {
		if name == label {
			return i
		}
	}
	return -1
}
