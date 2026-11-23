package dungeons

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/savejson"
)

type weightedDrop struct{ Item, Weight uint32 }
type keyValue[K cmp.Ordered, V any] struct {
	Key   K
	Value V
}

func orderedPairs[K cmp.Ordered, V any](m map[K]V) []keyValue[K, V] {
	rows := make([]keyValue[K, V], 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		rows = append(rows, keyValue[K, V]{k, m[k]})
	}
	return rows
}

// PVF hashes combine raw resource text and the original ordered JSON representation.
func catalogHash(parts ...any) (string, error) {
	var b strings.Builder
	for _, v := range parts {
		switch s := v.(type) {
		case string:
			b.WriteString(s)
		case json.RawMessage:
			if !json.Valid(s) {
				return "", errors.New("invalid frozen JSON in resource identity")
			}
			b.Write(s)
		default:
			raw, err := savejson.Marshal(v)
			if err != nil {
				return "", err
			}
			b.Write(raw)
		}
	}
	return hashText(b.String()), nil
}

type dropTokens struct {
	values []string
	n      int
	err    error
}

var dropLexeme = regexp.MustCompile(`\[[^\]\r\n]+\]|-?\d+`)

func lexDrops(text string) *dropTokens {
	r := &dropTokens{}
	at := 0
	if len(text) > budget {
		r.err = errors.New("drop script exceeds size limit")
		return r
	}
	for _, m := range dropLexeme.FindAllStringIndex(text, -1) {
		if strings.TrimSpace(text[at:m[0]]) != "" {
			r.err = errors.New("unrecognized drop token")
			return r
		}
		r.values = append(r.values, text[m[0]:m[1]])
		at = m[1]
	}
	if strings.TrimSpace(text[at:]) != "" {
		r.err = errors.New("unexpected trailing drop script text")
	}
	return r
}
func (r *dropTokens) peek() string {
	if r.n >= len(r.values) {
		return ""
	}
	return r.values[r.n]
}
func (r *dropTokens) more(close string) bool {
	return r.err == nil && r.peek() != "" && r.peek() != close
}
func (r *dropTokens) require(ok bool, reason string) {
	if !ok && r.err == nil {
		r.err = fmt.Errorf("drop token %d: %s", r.n, reason)
	}
}
func (r *dropTokens) tag(want string) {
	r.require(r.peek() == want, "expected "+want)
	if r.n < len(r.values) {
		r.n++
	}
}
func (r *dropTokens) number() int {
	n, err := strconv.ParseInt(r.peek(), 10, 32)
	r.require(err == nil, "expected signed integer")
	if r.n < len(r.values) {
		r.n++
	}
	return int(n)
}

type dropGrammar byte

const (
	monsterIndependent dropGrammar = iota
	worldIndependent
	cardIndependent
)

func (r *dropTokens) pairs(close string, grammar dropGrammar) []weightedDrop {
	found := []weightedDrop{}
	for r.more(close) {
		id, weight := r.number(), r.number()
		switch grammar {
		case monsterIndependent:
			if id <= 0 || weight < 0 {
				continue
			}
		case worldIndependent:
			r.require(id >= 0 && weight >= 0 && id != 3051 && id != 3052, "unexpected item in world seal pool")
		case cardIndependent:
			r.require(id > 0 && weight >= 0, "invalid card pool")
		}
		r.require(len(found) < 4096, "too many candidate rows")
		found = append(found, weightedDrop{uint32(id), uint32(weight)})
	}
	r.tag(close)
	r.require(len(found) > 0, "empty item pool")
	return found
}

func hasDropLifetime(t *gm.ItemTemplate) bool {
	return t.ResourceExpiration != nil || t.RelativeLifetimeSeconds != 0 || t.DailyDeletion || t.WeeklyDeletionDay != nil || t.FatigueTimeMonthlyDeletion
}
func monsterDropTemplate(items *commerce.Items, id uint32) *gm.ItemTemplate {
	src, err := items.Source(id)
	if err != nil {
		return nil
	}
	t, err := items.Prepared(id)
	if err != nil || t == nil {
		if src.Equipment {
			return nil
		}
		t, err = src.GiftFor(gm.GiftMonster)
	}
	if err != nil || t == nil || t.Kind > 2 || t.AccountCommonInventory || t.CashPackage || t.CashPackageReward || t.FateCollection || t.PetConsumable || t.AccountAfterFirstTrade || t.MythologyHash != "" || hasDropLifetime(t) && !(t.Kind == 2 && t.PvfGiftItem) {
		return nil
	}
	return t
}
func eventOwned(path string, cash bool) bool {
	parts := strings.Split(strings.ToLower(strings.ReplaceAll(path, "\\", "/")), "/")
	return slices.Contains(parts, "event") || cash && slices.Contains(parts, "cash")
}
