package quest

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type itemScript struct {
	fields []giftField
	err    error
}

func readItemScript(text string) *itemScript {
	fields, err := giftFields(text)
	return &itemScript{fields: fields, err: err}
}

func (s *itemScript) fail(reason string) {
	if s.err == nil {
		s.err = fmt.Errorf("%s", reason)
	}
}

func (s *itemScript) count(name string) int {
	n := 0
	for _, f := range s.fields {
		if f.name == name {
			n++
		}
	}
	return n
}

func (s *itemScript) get(name string) string {
	value, found := "", false
	for _, f := range s.fields {
		if f.name == name {
			if found && (name != "attach type" || f.value != value) {
				s.fail("repeated item field " + name)
			}
			value, found = f.value, true
		}
	}
	return value
}

func (s *itemScript) allowed(names []string, trimSlash, unique bool) {
	seen := make(map[string]bool)
	for _, f := range s.fields {
		name := f.name
		if trimSlash {
			name = strings.TrimLeft(name, "/")
		}
		if !slices.Contains(names, name) || unique && seen[f.name] {
			s.fail("unsupported or repeated item field " + f.name)
		}
		seen[f.name] = true
	}
}

func (s *itemScript) balanced(fields ...string) {
	for _, f := range fields {
		open, close := s.count(f), s.count("/"+f)
		if open != close || open > 1 || s.get("/"+f) != "" {
			s.fail("incomplete item field block " + f)
		}
	}
}

func (s *itemScript) number(name string, fallback, maximum uint32, required bool) uint32 {
	value := s.get(name)
	if s.count(name) == 0 && !required {
		return fallback
	}
	n, err := strconv.ParseUint(value, 10, 32)
	if err != nil || n > uint64(maximum) {
		s.fail("invalid item scalar " + name)
	}
	return uint32(n)
}

func (s *itemScript) numbers(value string) []uint32 {
	fields := strings.Fields(value)
	out := make([]uint32, len(fields))
	for i, f := range fields {
		n, err := strconv.ParseUint(f, 10, 32)
		if err != nil {
			s.fail("invalid unsigned item field")
		}
		out[i] = uint32(n)
	}
	return out
}

var bracketedTag = regexp.MustCompile("\\s*\x60\\[([^\\]]+)\\]\x60\\s*")

func (s *itemScript) tags(name string, bracketed bool, allowed ...string) []string {
	pattern := stringRE
	if bracketed {
		pattern = bracketedTag
	}
	rows, complete := matchedRows(s.get(name), pattern)
	out := make([]string, 0, len(rows))
	if !complete {
		s.fail("invalid item tags " + name)
	}
	for _, row := range rows {
		if slices.Contains(out, row[0]) || len(allowed) > 0 && !slices.Contains(allowed, row[0]) {
			s.fail("unsupported or repeated item tag " + name)
		}
		out = append(out, row[0])
	}
	return out
}

func (s *itemScript) jobs(name string, optional bool) uint16 {
	tags := s.tags(name, !optional)
	var out uint16
	for _, tag := range tags {
		if tag == "all" && !optional {
			out = math.MaxUint16
			continue
		}
		job := slices.Index(jobNames, tag)
		if job < 0 {
			s.fail("unknown item job " + tag)
			continue
		}
		out |= 1 << job
	}
	if out == 0 && !optional {
		s.fail("empty usable jobs")
	}
	return out
}

func (s *itemScript) purchaseMaterial(max uint32, allowEmpty bool) {
	if s.count("need material") == 0 {
		return
	}
	values := s.numbers(s.get("need material"))
	if !allowEmpty && len(values) == 0 || len(values)%2 != 0 ||
		slices.ContainsFunc(values, func(n uint32) bool { return n == 0 || n > max }) {
		s.fail("incomplete item purchase material metadata")
	}
}
