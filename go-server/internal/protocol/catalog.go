package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type CatalogEntry struct {
	ID   uint16
	Type byte // Published script type. Channel 19's game ACK uses type 1.
}

type ChannelCatalog struct {
	Script  []byte
	Entries []CatalogEntry
}

var (
	serverSection = regexp.MustCompile(`(?im)^\s*\[server\]`)
	sourceID      = regexp.MustCompile(`^[\t ]*(19|501|251|252|63)(?:\s|$)`)
	sourceRow     = regexp.MustCompile("^(\\d+)(\\s+)(`[^`\\r\\n]*`)(\\s+)(\\d+)(\\s+`[^`\\r\\n]*`)([^\\r\\n]*)$")
	levels        = regexp.MustCompile(`^((?:\s+-?\d+){5}\s+)-?\d+(\s+)-?\d+((?:\s.*)?$)`)
)

// ParseChannelCatalog publishes the selected legacy rows without rewriting
// the supplied channel_info.etc or normalizing its untranslated columns.
func ParseChannelCatalog(source []byte, includeOculus bool) (ChannelCatalog, error) {
	if len(source) == 0 || len(source) > 65536 || !utf8.Valid(source) {
		return ChannelCatalog{}, errors.New("invalid channel script size or UTF-8")
	}
	text := string(bytes.TrimPrefix(source, []byte{0xef, 0xbb, 0xbf}))
	sections := serverSection.FindAllStringIndex(text, -1)
	if len(sections) != 1 {
		return ChannelCatalog{}, errors.New("channel script requires one server section")
	}
	start, after := sections[0][0], sections[0][1]
	end := endServerSection.FindStringIndex(text[after:])
	if end == nil {
		return ChannelCatalog{}, errors.New("unterminated channel server section")
	}
	entries := []CatalogEntry{{19, 22}, {251, 13}, {252, 8}}
	if includeOculus {
		entries = append(entries, CatalogEntry{63, 63})
	}
	rows := make(map[uint16][]string, len(entries))
	for _, candidate := range strings.Split(text[after:after+end[0]], "\n") {
		idMatch := sourceID.FindStringSubmatch(candidate)
		if idMatch == nil {
			continue
		}
		idValue, _ := strconv.ParseUint(idMatch[1], 10, 16)
		id := uint16(idValue)
		if id == 63 && !includeOculus {
			continue
		}
		row := sourceRow.FindStringSubmatch(strings.TrimSpace(candidate))
		if row == nil {
			return ChannelCatalog{}, fmt.Errorf("malformed channel %d", id)
		}
		typ, err := strconv.ParseUint(row[5], 10, 8)
		if err != nil {
			return ChannelCatalog{}, fmt.Errorf("invalid type for channel %d", id)
		}
		if id == 251 && typ != 13 {
			continue
		}
		if id == 501 {
			id = 251
		}
		if rows[id] != nil {
			return ChannelCatalog{}, fmt.Errorf("duplicate channel %d", id)
		}
		rows[id] = row
	}
	var out strings.Builder
	out.WriteString(text[:start])
	out.WriteString("[server]\n0\n")
	for _, entry := range entries {
		row := rows[entry.ID]
		if entry.ID == 63 && row == nil {
			row = rows[19]
		} else if row == nil {
			return ChannelCatalog{}, fmt.Errorf("missing channel %d", entry.ID)
		} else {
			t, _ := strconv.ParseUint(row[5], 10, 8)
			if byte(t) != entry.Type && !(entry.ID == 19 && t == 1) {
				return ChannelCatalog{}, fmt.Errorf("unexpected type for channel %d", entry.ID)
			}
		}
		leading := row[1] + row[2] + row[3] + row[4] + strconv.Itoa(int(entry.Type)) + row[6]
		tail := row[7]
		switch entry.ID {
		case 251:
			leading = "251 `#ch.251`" + row[4] + "13" + row[6]
		case 63, 252:
			m := levels.FindStringSubmatch(tail)
			if m == nil {
				return ChannelCatalog{}, fmt.Errorf("missing level columns for channel %d", entry.ID)
			}
			minimum := "50"
			if entry.ID == 63 {
				leading = "63 `#ch.63` 63 `[the_oculus]`"
				minimum = "100"
			}
			tail = m[1] + minimum + m[2] + "100" + m[3]
		}
		out.WriteString(leading + tail + "\n")
	}
	out.WriteString("[/server]\n")
	return ChannelCatalog{Script: []byte(out.String()), Entries: entries}, nil
}

var endServerSection = regexp.MustCompile(`(?i)\[/server\]`)
