package protocol

import (
	_ "embed"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// The strict .NET CP54936 used for names differs from the WHATWG GB18030
// decoder (e.g. single 0x80 is invalid). Generate-NameTable.ps1 enumerates
// that provider and verifies every mapping round-trips before writing it.
//
//go:embed name54936.bin
var nameMap string

const pairCount = 126 * 190

type nameRange struct{ index, code int }

var (
	namePairs  [65536]uint16
	nameRanges []nameRange
)

func nameWord(off int) int { return int(nameMap[off]) | int(nameMap[off+1])<<8 }

func init() {
	for i := 0; i < pairCount; i++ {
		trail := i%190 + 0x40
		if trail >= 0x7f {
			trail++
		}
		namePairs[nameWord(2*i)] = uint16((0x81+i/190)<<8 | trail)
	}
	for i := 2 * pairCount; i < len(nameMap); i += 4 {
		nameRanges = append(nameRanges, nameRange{nameWord(i), nameWord(i + 2)})
	}
}

func decodeName(raw []byte) (string, error) {
	var out strings.Builder
	for len(raw) > 0 {
		b := raw[0]
		if b < 0x80 {
			out.WriteByte(b)
			raw = raw[1:]
			continue
		}
		if b < 0x81 || b > 0xfe || len(raw) < 2 {
			return "", errors.New("invalid GB18030 lead byte")
		}
		trail := raw[1]
		if trail >= 0x40 && trail <= 0xfe && trail != 0x7f {
			column := int(trail) - 0x40
			if trail > 0x7f {
				column--
			}
			out.WriteRune(rune(nameWord(2 * (int(b-0x81)*190 + column))))
			raw = raw[2:]
			continue
		}
		if len(raw) < 4 || trail < '0' || trail > '9' || raw[2] < 0x81 || raw[2] > 0xfe || raw[3] < '0' || raw[3] > '9' {
			return "", errors.New("invalid GB18030 trail bytes")
		}
		n := ((int(b-0x81)*10+int(trail-'0'))*126+int(raw[2]-0x81))*10 + int(raw[3]-'0')
		switch {
		case n < 39420:
			i := sort.Search(len(nameRanges), func(i int) bool { return nameRanges[i].index > n }) - 1
			out.WriteRune(rune(nameRanges[i].code + n - nameRanges[i].index))
		case n >= 189000 && n < 189000+0x100000:
			out.WriteRune(rune(n - 189000 + 0x10000))
		default:
			return "", errors.New("unassigned GB18030 code")
		}
		raw = raw[4:]
	}
	return strings.TrimRight(out.String(), "\x00"), nil
}

func encodeName(s string) ([]byte, error) {
	units := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return nil, errors.New("invalid character in name")
		}
		units += utf16.RuneLen(r)
	}
	if units < 2 {
		return nil, errors.New("character name too short")
	}
	return encodeText(s, 30)
}

func encodeText(s string, limit int) ([]byte, error) {
	if limit < 0 || !utf8.ValidString(s) {
		return nil, errors.New("invalid Unicode text")
	}
	out := make([]byte, 0, min(len(s), limit))
	for _, r := range s {
		switch {
		case r < 0x80:
			out = append(out, byte(r))
		case r <= 0xffff && namePairs[r] != 0:
			pair := namePairs[r]
			out = append(out, byte(pair>>8), byte(pair))
		default:
			n := 189000 + int(r) - 0x10000
			if r <= 0xffff {
				i := sort.Search(len(nameRanges), func(i int) bool { return nameRanges[i].code > int(r) }) - 1
				n = nameRanges[i].index + int(r) - nameRanges[i].code
			}
			b3 := byte(0x30 + n%10)
			n /= 10
			b2 := byte(0x81 + n%126)
			n /= 126
			b1 := byte(0x30 + n%10)
			n /= 10
			out = append(out, byte(0x81+n), b1, b2, b3)
		}
		if len(out) > limit {
			return nil, errors.New("GB18030 text exceeds wire capacity")
		}
	}
	return out, nil
}

// EncodeText uses the original strict CP54936 provider and its byte capacity.
func EncodeText(s string, maxBytes int) ([]byte, error) { return encodeText(s, maxBytes) }

func readNameBlock(body []byte) (string, []byte, error) {
	if len(body) < 4 {
		return "", nil, errors.New("truncated name length")
	}
	n := little.Uint32(body)
	if n < 2 || n > 30 || int(n) > len(body)-4 {
		return "", nil, errors.New("invalid name block length")
	}
	name, err := decodeName(body[4 : 4+int(n)])
	return name, body[4+int(n):], err
}

func ReadNameRequest(body []byte) (string, error) {
	name, rest, err := readNameBlock(body)
	if err == nil && len(rest) > 0 {
		return "", errors.New("trailing name request data")
	}
	return name, err
}
