package pvf

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const fateAbilityPath = "live/event/chn/2021/0914_decisionsuntodestiny2/etc/decisionsuntodestinyability.co"

func (a *Archive) ReadFateAbilityText() (string, error) {
	return a.readText(fateAbilityPath, 2<<20, 2<<20, 4<<20, false, true)
}

func (a *Archive) ReadFateAbility() ([]byte, error) {
	a.lifetime.RLock()
	defer a.lifetime.RUnlock()
	if err := a.checkSource(); err != nil {
		return nil, err
	}
	i, err := a.entry(fateAbilityPath)
	if err != nil {
		return nil, err
	}
	return a.raw(i, 2<<20)
}

func (a *Archive) ReadFatePortalIndex() (map[int64]string, error) {
	a.lifetime.RLock()
	defer a.lifetime.RUnlock()
	if err := a.checkSource(); err != nil {
		return nil, err
	}
	i, err := a.entry("list/passiveobject.lst")
	if err != nil {
		return nil, err
	}
	raw, err := a.raw(i, regularBudget)
	if err != nil {
		return nil, err
	}
	if a.items[i].kind != 1 || len(raw)%5 != 0 {
		return nil, errors.New("activity portal index must use complete native path tokens")
	}
	out := make(map[int64]string, 3)
	for p := 0; p+10 <= len(raw); p += 5 {
		if raw[p] != 0 || (raw[p+5] != 6 && raw[p+5] != 8) {
			continue
		}
		id := int64(i32(raw[p+1:]))
		switch id {
		case 490020148, 109055638, 72231:
			s := a.resolve(i32(raw[p+6:]))
			if _, exists := out[id]; exists || s == "" {
				return nil, fmt.Errorf("ambiguous activity portal %d", id)
			}
			out[id] = strings.ReplaceAll(s, "\\", "/")
		}
		p += 5
	}
	if len(out) != 3 {
		return nil, errors.New("activity passive object is absent from native index")
	}
	return out, a.checkSource()
}

var independentDropRow = regexp.MustCompile("^[[:space:]]*([0-9]+)[[:space:]]+\x60([^\x60]+)\x60")

func (a *Archive) ReadIndependentDropIndex() (map[int64]string, error) {
	a.lifetime.RLock()
	defer a.lifetime.RUnlock()
	if err := a.checkSource(); err != nil {
		return nil, err
	}
	i, err := a.entry("etc/independentdrop.lst")
	if err != nil {
		return nil, err
	}
	raw, err := a.raw(i, regularBudget)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string)
	add := func(id int64, p string) error {
		if _, exists := out[id]; exists || len(out) >= 4096 {
			return errors.New("independent-drop index has duplicate or excessive entries")
		}
		p = strings.ReplaceAll(p, "\\", "/")
		if !hasCategoryPrefix(p, "etc") {
			p = "etc/" + p
		}
		if !strings.HasPrefix(strings.ToLower(p), "etc/independentdrop/") {
			return errors.New("independent-drop path is outside its resource directory")
		}
		for x := range strings.SplitSeq(p, "/") {
			if x == "" || x == "." || x == ".." || strings.ContainsRune(x, ':') {
				return errors.New("invalid independent-drop path")
			}
		}
		out[id] = p
		return nil
	}
	if a.items[i].kind == 1 {
		if len(raw)%10 != 0 || len(raw)/10 > 4096 {
			return nil, errors.New("invalid independent-drop index token count")
		}
		for p := 0; p < len(raw); p += 10 {
			id := int64(i32(raw[p+1:]))
			if raw[p] != 0 || (raw[p+5] != 6 && raw[p+5] != 8) || id < 0 {
				return nil, errors.New("invalid independent-drop index token")
			}
			if err = add(id, a.resolve(i32(raw[p+6:]))); err != nil {
				return nil, err
			}
		}
	} else {
		text, err := a.decode(raw, a.items[i].kind, decodedTextBudget, false, false, nil)
		if err != nil {
			return nil, err
		}
		for {
			row := independentDropRow.FindStringSubmatchIndex(text)
			if len(row) == 0 {
				if strings.TrimSpace(text) != "" {
					return nil, errors.New("independent-drop external index has trailing data")
				}
				break
			}
			id, err := strconv.ParseInt(text[row[2]:row[3]], 10, 32)
			if err != nil {
				return nil, err
			}
			if err = add(id, text[row[4]:row[5]]); err != nil {
				return nil, err
			}
			text = text[row[1]:]
		}
	}
	return out, a.checkSource()
}
