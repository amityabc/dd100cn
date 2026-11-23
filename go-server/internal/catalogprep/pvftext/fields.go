// Package pvftext reads fields using the S9A1 LocalGameplayResourceText grammar.
package pvftext

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

var nextField = regexp.MustCompile(`(?m)^[ \t]*\[/?[^\]\r\n]+\]`)

type patterns struct{ open, close *regexp.Regexp }

var blockPatterns sync.Map

func Blocks(text, tag string) []string {
	entry, ok := blockPatterns.Load(tag)
	if !ok {
		t := regexp.QuoteMeta(tag)
		entry = patterns{regexp.MustCompile(`(?im)^[ \t]*\[` + t + `\][ \t]*`), regexp.MustCompile(`(?im)^[ \t]*\[/` + t + `\][ \t]*`)}
		blockPatterns.Store(tag, entry)
	}
	p := entry.(patterns)
	var result []string
	for _, at := range p.open.FindAllStringIndex(text, -1) {
		tail := text[at[1]:]
		end := p.close.FindStringIndex(tail)
		if end == nil {
			end = nextField.FindStringIndex(tail)
		}
		if end != nil {
			tail = tail[:end[0]]
		}
		result = append(result, strings.TrimSpace(tail))
	}
	return result
}

func Section(text, tag string) (string, error) {
	sections := Blocks(text, tag)
	switch len(sections) {
	case 0:
		return "", nil
	case 1:
		return sections[0], nil
	default:
		return "", fmt.Errorf("repeated PVF field %q", tag)
	}
}

func Integers(value string) ([]int, error) {
	fields := strings.Fields(value)
	result := make([]int, len(fields))
	for i, field := range fields {
		n, err := strconv.ParseInt(field, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("PVF integer %q: %w", field, err)
		}
		result[i] = int(n)
	}
	return result, nil
}

func Numbers(text, tag string) ([]int, error) {
	s, err := Section(text, tag)
	if err != nil {
		return nil, err
	}
	return Integers(s)
}

var listRow = regexp.MustCompile("(-?\\d+)\\s+`([^`]+)`")

func List(text, category string) (map[int]string, error) {
	out := map[int]string{}
	for _, row := range listRow.FindAllStringSubmatch(text, -1) {
		n, err := strconv.ParseInt(row[1], 10, 32)
		if err != nil {
			return nil, err
		}
		p := strings.ReplaceAll(row[2], "\\", "/")
		if !strings.HasPrefix(strings.ToLower(p), strings.ToLower(category)+"/") {
			p = category + "/" + p
		}
		if strings.Contains(p, ":") || strings.IndexFunc(p, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("invalid PVF %s path %q", category, p)
		}
		for _, part := range strings.Split(p, "/") {
			if part == "" || part == "." || part == ".." {
				return nil, fmt.Errorf("invalid PVF %s path %q", category, p)
			}
		}
		if _, exists := out[int(n)]; exists {
			return nil, fmt.Errorf("duplicate %s list entry %d", category, n)
		}
		out[int(n)] = p
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty PVF list %s", category)
	}
	return out, nil
}
