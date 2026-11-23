package towns

import (
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

type Warp struct {
	Town, Area          uint16
	Kind, MinimumLevel  byte
	RequiredQuest       *uint16
	X, Y, Width, Height uint16
}

var buttonName = regexp.MustCompile("^STR:TeleportBtn[0-9]+$")

func warps(xui, room string) ([]Warp, error) {
	container, ok := single(room, "town movable area with animation")
	if !ok {
		return nil, fmt.Errorf("Seria warp animation boundary")
	}
	areas := pattern("movable area")
	blocks, closed := areas.blocks(container)
	if !closed || strings.TrimSpace(areas.block.ReplaceAllString(container, "")) != "" || len(blocks) != strings.Count(strings.ToLower(container), "[movable area]") || len(blocks) != strings.Count(strings.ToLower(container), "[/movable area]") {
		return nil, fmt.Errorf("invalid Seria movable areas")
	}
	enabled := fields(container)["is seria room warp"]
	if len(enabled) != 1 || enabled[0] != "1" || strings.Count(strings.ToLower(container), "[is seria room warp]") != 1 {
		return nil, fmt.Errorf("Seria warp marker missing")
	}
	dec := xml.NewDecoder(strings.NewReader(strings.TrimRight(strings.TrimPrefix(xui, "\uFEFF"), "\x00")))
	// ReadText already decoded the member indicated by the original XML declaration.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	buttons := map[string]map[string]string{}
	rootSeen := false
	for {
		t, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if d, ok := t.(xml.Directive); ok && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(string(d))), "DOCTYPE") {
			return nil, fmt.Errorf("unsupported Seria menu XML declaration")
		}
		e, ok := t.(xml.StartElement)
		if !ok {
			continue
		}
		if !rootSeen {
			if e.Name.Space != "" || e.Name.Local != "ROOT" {
				return nil, fmt.Errorf("invalid Seria warp XML root")
			}
			rootSeen = true
		}
		attr := map[string]string{}
		for _, v := range e.Attr {
			if v.Name.Space == "" {
				attr[v.Name.Local] = v.Value
			}
		}
		id := attr["ID"]
		if !strings.HasPrefix(id, "STR:TeleportBtn") {
			continue
		}
		if e.Name.Local != "CNButton" || e.Name.Space != "" || !buttonName.MatchString(id) || buttons[id] != nil {
			return nil, fmt.Errorf("invalid warp control %s", id)
		}
		buttons[id] = attr
	}
	out := []Warp{}
	for _, r := range []struct{ button, town, area, kind int }{{2, 39, 0, 5}, {3, 39, 2, 5}, {4, 38, 0, 6}, {6, 117, 1, 5}, {0, 38, 5, 5}, {1, 40, 4, 5}, {5, 76, 0, 6}, {7, 125, 0, 5}} {
		id := "STR:TeleportBtn" + strconv.Itoa(r.button)
		b := buttons[id]
		if b == nil {
			return nil, fmt.Errorf("missing %s", id)
		}
		var fieldErr error
		number := func(k string, limit int) int {
			v := b[k]
			s, ok := strings.CutPrefix(v, "NUM:")
			n, err := strconv.ParseUint(s, 10, 32)
			if !ok || len(s) == 0 || strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) >= 0 || err != nil || n > uint64(limit) {
				fieldErr = fmt.Errorf("invalid %s %s", id, k)
			}
			return int(n)
		}
		town, area := number("townIndex", 255), number("areaIndex", 255)
		locale, hasLocale := b["localizeType"]
		event, hasEvent := b["connectionEventIndex"]
		if town != r.town || area != r.area || (hasLocale || r.button == 6 || r.button == 7) && locale != "STR:chn" || hasEvent && event != "NUM:0" {
			return nil, fmt.Errorf("unsupported Seria warp target %s", id)
		}
		quest := uint16(number("clearQuest", 29999))
		x, y := number("townXPos", 32767), number("townYPos", 32767)
		w, h := number("townAreaWidth", 32767), number("townAreaHeight", 32767)
		level := number("requireLevel", 255)
		if fieldErr != nil {
			return nil, fieldErr
		}
		if w == 0 || h == 0 || x+w > 32768 || y+h > 32768 {
			return nil, fmt.Errorf("invalid Seria warp rectangle %s", id)
		}
		entry := Warp{Town: uint16(town), Area: uint16(area), Kind: byte(r.kind), MinimumLevel: byte(level), X: uint16(x), Y: uint16(y), Width: uint16(w), Height: uint16(h)}
		if quest != 0 {
			entry.RequiredQuest = &quest
		}
		out = append(out, entry)
	}
	return out, nil
}
