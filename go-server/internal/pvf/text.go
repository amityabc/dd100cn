package pvf

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const decodedTextBudget = 16 << 20

func byteText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b)
	if err == nil {
		return string(decoded)
	}
	fallback := make([]rune, len(b))
	for i, v := range b {
		fallback[i] = rune(v)
	}
	return string(fallback)
}

func wideText(b []byte) string {
	u := make([]uint16, (len(b)+1)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u[i/2] = le.Uint16(b[i:])
	}
	if len(b)%2 != 0 {
		u[len(u)-1] = unicodeReplacement
	}
	return string(utf16.Decode(u))
}

const unicodeReplacement = 0xfffd

func (a *Archive) resolve(off int32) string {
	if off < 0 {
		return ""
	}
	if off&1 == 0 {
		begin := int(off >> 1)
		if begin >= len(a.strA) {
			return ""
		}
		b := a.strA[begin:]
		if end := bytes.IndexByte(b, 0); end >= 0 {
			return byteText(b[:end])
		}
		return ""
	}
	begin := int(off &^ 1)
	if begin >= len(a.strW) {
		return ""
	}
	end := begin
	for end+1 < len(a.strW) && (a.strW[end] != 0 || a.strW[end+1] != 0) {
		end += 2
	}
	return wideText(a.strW[begin:end])
}

// ResolveString returns a raw string-pool lookup without localization.
func (a *Archive) ResolveString(off int32) (string, error) {
	a.lifetime.RLock()
	defer a.lifetime.RUnlock()
	if err := a.checkSource(); err != nil {
		return "", err
	}
	return a.resolve(off), nil
}

// ReadText decodes the native text, numeric, tag and string token formats.
func (a *Archive) ReadText(path string, maximum int) (string, error) {
	return a.readText(path, maximum, regularBudget, decodedTextBudget, false, false)
}

// ReadDungeonMapText preserves raw map display-name references, as the
// dungeon resource catalog does when reading rules that do not need them.
func (a *Archive) ReadDungeonMapText(path string, maximum int) (string, error) {
	return a.readText(path, maximum, regularBudget, regularBudget, true, true)
}

func (a *Archive) ReadMapText(path string, maximum int) (string, error) {
	return a.ReadDungeonMapText(path, maximum)
}

func (a *Archive) ReadCommerceText(path string) (string, error) {
	if !strings.EqualFold(NormalizePath(path), "etc/(r)cerashop.etc") {
		return a.ReadText(path, regularBudget)
	}
	return a.readText(path, 2<<20, 2<<20, 2<<20, false, true)
}

func (a *Archive) readText(path string, maximum, allowed, maxText int, mapNames, strict bool) (string, error) {
	a.lifetime.RLock()
	defer a.lifetime.RUnlock()
	if maximum < 0 || maximum > allowed {
		return "", errors.New("invalid PVF text read budget")
	}
	if err := a.checkSource(); err != nil {
		return "", err
	}
	i, err := a.entry(path)
	if err != nil {
		return "", err
	}
	raw, err := a.raw(i, maximum)
	if err != nil {
		return "", err
	}
	text, err := a.decode(raw, a.items[i].kind, maxText, mapNames, strict, nil)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return text, a.checkSource()
}

func (a *Archive) decode(raw []byte, kind int32, maximum int, mapNames, strict bool, visiting map[int]bool) (string, error) {
	if kind != 1 {
		text := ""
		if kind == 3 {
			text = wideText(raw)
		} else {
			text = byteText(raw)
		}
		if units(text) > maximum {
			return "", errors.New("PVF text exceeds decoded character budget")
		}
		return text, nil
	}
	if strict && len(raw)%5 != 0 {
		return "", errors.New("truncated PVF script token")
	}
	var out strings.Builder
	out.Grow(min(len(raw), maximum))
	length := 0
	appendText := func(text string) error {
		length += units(text)
		if length > maximum {
			return errors.New("PVF text exceeds decoded character budget")
		}
		out.WriteString(text)
		return nil
	}
	resolve := func(off int32, rawName bool) (string, error) {
		value := a.resolve(off)
		if !rawName && a.client100 && strings.HasPrefix(value, "<") {
			return a.localized(value, visiting)
		}
		return value, nil
	}
	tag := ""
	for off := 0; off+5 <= len(raw); off += 5 {
		value := i32(raw[off+1:])
		var text string
		var err error
		switch raw[off] {
		case 0:
			text = strconv.FormatInt(int64(value), 10) + " "
		case 2:
			text = floatText(math.Float32frombits(uint32(value)), a.client100) + " "
		case 3:
			tag, err = resolve(value, false)
			text = "\n" + tag + "\n"
		case 5:
			text = "\n{5=\x60\x60}"
		case 7:
			text = "\n{7=\x60\x60}"
		case 8:
			if !a.client100 {
				continue
			}
			fallthrough
		case 6:
			text, err = resolve(value, mapNames && tag == "[map name]")
			text = "\x60" + text + "\x60 "
		default:
			continue
		}
		if err != nil {
			return "", err
		}
		if err = appendText(text); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}

func units(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

func floatText(v float32, client100 bool) string {
	switch f := float64(v); {
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case math.IsNaN(f):
		return "NaN"
	case !client100:
		return strconv.FormatFloat(f, 'f', 2, 32)
	}
	e := strconv.FormatFloat(float64(v), 'E', -1, 32)
	pos := strings.IndexByte(e, 'E')
	exp, _ := strconv.Atoi(e[pos+1:])
	if exp >= -4 && exp < 9 {
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	}
	return e
}

var localizedRef = regexp.MustCompile("^<([0-9]+)::([^>]+)>$")

func (a *Archive) LocalizedText(value string) (string, error) {
	a.lifetime.RLock()
	defer a.lifetime.RUnlock()
	if err := a.checkSource(); err != nil {
		return "", err
	}
	return a.localized(value, nil)
}

func (a *Archive) localized(value string, visiting map[int]bool) (string, error) {
	m := localizedRef.FindStringSubmatch(strings.TrimSpace(value))
	if len(m) == 0 {
		return value, nil
	}
	id, err := strconv.ParseInt(m[1], 10, 32)
	if err != nil {
		return "", err
	}
	a.localeMu.Lock()
	table := a.locales[int(id)]
	a.localeMu.Unlock()
	if table == nil {
		if visiting[int(id)] {
			return "", fmt.Errorf("cyclic PVF localization table %d", id)
		}
		if visiting == nil {
			visiting = make(map[int]bool)
		}
		visiting[int(id)] = true
		defer delete(visiting, int(id))
		table, err = a.locale(int32(id), visiting)
		if err != nil {
			return "", err
		}
		a.localeMu.Lock()
		a.locales[int(id)] = table
		a.localeMu.Unlock()
	}
	if result, found := table[m[2]]; found {
		return result, nil
	}
	return value, nil
}

func (a *Archive) locale(id int32, visiting map[int]bool) (map[string]string, error) {
	registries := [...]string{"list/n_string.lst", "n_string.lst", "string/string.lst", "string.lst", "strings.lst"}
	for _, registry := range registries {
		index := a.find(registry)
		if index < 0 || a.items[index].kind != 1 {
			continue
		}
		raw, err := a.raw(index, metadataBudget)
		if err != nil {
			return nil, err
		}
		for p := 0; p+10 <= len(raw); p += 5 {
			if raw[p] != 0 || i32(raw[p+1:]) != id || (raw[p+5] != 6 && raw[p+5] != 8) {
				continue
			}
			path := strings.ReplaceAll(a.resolve(i32(raw[p+6:])), "\\", "/")
			table := a.find(path)
			if table < 0 {
				if slash := strings.LastIndexByte(registry, '/'); slash >= 0 {
					path = registry[:slash+1] + path
				}
				table = a.find(path)
			}
			if table >= 0 {
				return a.readLocale(table, path, visiting)
			}
		}
	}
	return nil, fmt.Errorf("missing PVF localization registry %d", id)
}

func (a *Archive) readLocale(index int, path string, visiting map[int]bool) (map[string]string, error) {
	raw, err := a.raw(index, metadataBudget)
	if err != nil {
		return nil, err
	}
	// The caller's text budget applies after resolving a value. The shared
	// table itself can be larger than even the named catalog script limits.
	text, err := a.decode(raw, a.items[index].kind, math.MaxInt, false, false, visiting)
	if err != nil {
		return nil, fmt.Errorf("decode localization table %s: %w", path, err)
	}
	if text == "" {
		return nil, fmt.Errorf("PVF localization table is unreadable: %s", path)
	}
	data := make(map[string]string)
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(line, "//") {
			continue
		}
		key, value, found := strings.Cut(line, ">")
		if !found || key == "" {
			continue
		}
		key = strings.Trim(strings.TrimSpace(key), "\ufeff")
		value = strings.ReplaceAll(strings.TrimRight(value, "\x00"), "\\n", "\n")
		if old, exists := data[key]; exists && old != value {
			return nil, fmt.Errorf("conflicting localization key %s: %s", key, path)
		}
		data[key] = value
	}
	return data, nil
}
