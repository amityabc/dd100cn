package commerce

import (
	"crypto/sha256"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
)

type craftParser struct {
	name string
	err  error
}

func (p *craftParser) check(ok bool, label string) bool {
	if !ok && p.err == nil {
		p.err = fmt.Errorf("PVF craft %s: %s", p.name, label)
	}
	return ok
}

func (p *craftParser) script(text string) []gm.Field {
	fields, err := gm.ItemFields(text)
	p.check(err == nil && len(text) <= 1<<20, "invalid script")
	return fields
}

func (p *craftParser) field(fields []gm.Field, tag string, optional bool) string {
	value, at, err := giftUnique(fields, tag)
	p.check(err == nil && (optional || at >= 0), "missing or duplicate "+tag)
	return value
}

var craftOperand = regexp.MustCompile("`[^`]*`|[+-]?(?:[0-9]+(?:\\.[0-9]*)?|\\.[0-9]+)(?:[eE][+-]?[0-9]+)?")

func (p *craftParser) tokens(value string) []string {
	spans := craftOperand.FindAllStringIndex(value, -1)
	out := make([]string, 0, len(spans))
	at := 0
	for i, span := range spans {
		p.check((i == 0 || span[0] > at) && strings.TrimSpace(value[at:span[0]]) == "", "malformed operand")
		out = append(out, value[span[0]:span[1]])
		at = span[1]
	}
	p.check(strings.TrimSpace(value[at:]) == "", "malformed operand tail")
	return out
}

func (p *craftParser) ints(s string, min, max int) int {
	n, err := strconv.ParseInt(s, 10, 32)
	if !p.check(err == nil && n >= int64(min) && n <= int64(max), "integer outside range") {
		return min
	}
	return int(n)
}

func (p *craftParser) float(s string) float32 {
	f, err := strconv.ParseFloat(s, 32)
	p.check(err == nil && f >= 0 && !math.IsInf(f, 0), "decimal outside range")
	return float32(f)
}

func (p *craftParser) array(fields []gm.Field, tag string, count int) []float32 {
	values := p.tokens(p.field(fields, tag, false))
	p.check(len(values) > 0 && len(values) <= 4096 && (count == 0 || len(values) == count), "invalid "+tag+" width")
	out := make([]float32, 0, len(values))
	for _, v := range values {
		out = append(out, p.float(v))
	}
	return out
}

func (p *craftParser) scalar(fields []gm.Field, tag string, max int) int {
	values := p.tokens(p.field(fields, tag, false))
	if !p.check(len(values) == 1, "invalid "+tag+" scalar") {
		return 0
	}
	return p.ints(values[0], 0, max)
}

func craftHash(hashes ...string) string {
	return fmt.Sprintf("%X", sha256.Sum256([]byte(strings.Join(hashes, "\n"))))
}
