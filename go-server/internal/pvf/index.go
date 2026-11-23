package pvf

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var indexRow = regexp.MustCompile("(-?[0-9]+)[[:space:]]+\x60([^\x60]+)\x60")

// ReadIndex returns original signed numeric IDs and raw resource paths.
// The equipment list has its own 4 MiB allowance in the original catalog.
func (a *Archive) ReadIndex(category string, maximum ...int) (map[int64]string, error) {
	if category == "" || strings.IndexFunc(category, func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-'
	}) >= 0 {
		return nil, errors.New("PVF index category must be a single resource name")
	}
	capacity := regularBudget
	if strings.EqualFold(category, "equipment") {
		capacity = 4 << 20
	}
	limit := capacity
	if len(maximum) == 1 {
		limit = maximum[0]
	}
	if len(maximum) > 1 || limit < 0 || limit > capacity {
		return nil, errors.New("invalid PVF index read budget")
	}
	a.lifetime.RLock()
	defer a.lifetime.RUnlock()
	if err := a.checkSource(); err != nil {
		return nil, err
	}
	path := "list/" + category + ".lst"
	i := a.find(path)
	full := i >= 0
	if !full {
		path = category + "/" + category + ".lst"
		var err error
		i, err = a.entry(path)
		if err != nil {
			return nil, err
		}
	}
	raw, err := a.raw(i, limit)
	if err != nil {
		return nil, err
	}
	entries := make(map[int64]string)
	add := func(id int64, item string) {
		if id < 0 && strings.EqualFold(category, "monster") {
			id = int64(uint32(id))
		}
		if _, found := entries[id]; found {
			return
		}
		item = strings.ReplaceAll(item, "\\", "/")
		if !full && !hasCategoryPrefix(item, category) {
			item = category + "/" + item
		}
		entries[id] = item
	}
	if kind := a.items[i].kind; kind == 1 {
		if len(raw)%5 != 0 {
			return nil, errors.New("truncated native PVF list token")
		}
		for p := 0; p+10 <= len(raw); p += 5 {
			if raw[p] != 0 || (raw[p+5] != 6 && raw[p+5] != 8) {
				continue
			}
			item := a.resolve(i32(raw[p+6:]))
			if item == "" {
				return nil, errors.New("native PVF list contains an empty path")
			}
			add(int64(i32(raw[p+1:])), item)
			p += 5
		}
	} else {
		text, err := a.decode(raw, kind, decodedTextBudget, false, false, nil)
		if err != nil {
			return nil, err
		}
		for _, row := range indexRow.FindAllStringSubmatch(text, -1) {
			id, err := strconv.ParseInt(row[1], 10, 64)
			if err != nil {
				return nil, err
			}
			add(id, row[2])
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("PVF list has no index entries: %s", path)
	}
	return entries, a.checkSource()
}

func hasCategoryPrefix(path, category string) bool {
	before, _, ok := strings.Cut(path, "/")
	return ok && strings.EqualFold(before, category)
}

func (a *Archive) ReadExperienceThresholds(maximum int) ([]int64, error) {
	a.lifetime.RLock()
	defer a.lifetime.RUnlock()
	if maximum < 0 || maximum > regularBudget {
		return nil, errors.New("invalid PVF experience table read budget")
	}
	if err := a.checkSource(); err != nil {
		return nil, err
	}
	i, err := a.entry("character/exptable.tbl")
	if err != nil {
		return nil, err
	}
	raw, err := a.raw(i, maximum)
	if err != nil {
		return nil, err
	}
	if a.items[i].kind != 1 {
		text, err := a.decode(raw, a.items[i].kind, decodedTextBudget, false, false, nil)
		if err != nil {
			return nil, err
		}
		rows := strings.Fields(text)
		out := make([]int64, len(rows))
		if len(out) < 100 {
			return nil, errors.New("PVF experience table is incomplete")
		}
		for i, s := range rows {
			value, err := strconv.ParseInt(s, 10, 64)
			if err != nil || value < 0 || i > 0 && out[i-1] > value {
				return nil, errors.New("invalid PVF experience threshold")
			}
			out[i] = value
		}
		return out[:100], a.checkSource()
	}
	if len(raw) == 0 || len(raw)%5 != 0 {
		return nil, errors.New("expected the native PVF experience table tokens")
	}
	out := make([]int64, 0, len(raw)/5)
	for p := 0; p < len(raw); p += 5 {
		switch raw[p] {
		case 0:
			value := i32(raw[p+1:])
			if value < 0 {
				return nil, errors.New("negative PVF experience threshold")
			}
			out = append(out, int64(value))
		case 9:
			if p+10 > len(raw) || raw[p+5] != 9 {
				return nil, errors.New("incomplete PVF experience high/low word pair")
			}
			value := uint64(le.Uint32(raw[p+1:]))<<32 | uint64(le.Uint32(raw[p+6:]))
			if value > math.MaxInt64 {
				return nil, errors.New("PVF experience threshold exceeds Int64")
			}
			out = append(out, int64(value))
			p += 5
		default:
			return nil, errors.New("unexpected PVF experience table token")
		}
	}
	return out, a.checkSource()
}

func (a *Archive) ExperienceThresholds(maximum int) ([]int64, error) {
	return a.ReadExperienceThresholds(maximum)
}
