// Package ruby replays the native weekly Ruby treasury from permanent receipts.
package ruby

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"s9a1/server/internal/savejson"
)

const Gate uint16 = 180
const Policy = "LocalRubySequentialFloors/v1"
const RoutePolicy = "LocalRubyResourceRoutes/v1"
const ClaimPolicy = "LocalRubyTreasureSelection/v3"
const CompoundPolicy = "LocalRubyCompound/v1"

var ErrInvalid = errors.New("invalid ruby treasury history or selection")
var le = binary.LittleEndian
var boxes = []uint32{10312271, 10312272, 10312273, 10312274, 10319996}

type Entry struct {
	Policy             string
	Character          uint16
	Run                string
	Requested, Dungeon uint32
	Floor              byte
	Week               int64
	Seed               uint32
	Hash               string
	FromClear          *string `json:",omitempty"`
}
type Advance struct {
	Policy      string
	First, Last byte
	Next        uint32
	Boxes       []uint32
}
type Clear struct {
	Policy      string
	Character   uint16
	Run         string
	Dungeon     uint32
	Floor       byte
	Week        int64
	Hash, Death string
	Advance     *Advance `json:",omitempty"`
}
type Grant struct {
	InstanceId string
	TemplateId uint32
	Location   byte
	Slot       uint16
	Granted    uint32
}
type Receipt struct {
	Key, Input, Result string
	Revision           int64
	Grants             []Grant
}
type View struct {
	Week    int64
	Boxes   []uint32
	Claimed byte
}
type Claim struct {
	Policy string
	Week   int64
	Body   savejson.String
	Slots  []int
	Items  []uint32
	Hash   string
	View   *View `json:",omitempty"`
}
type ClaimPlan struct {
	Key    string
	Proof  Claim
	Replay bool
}
type Ledger struct {
	View     View
	Cleared  byte
	Entries  map[string]Entry
	Clears   map[string]Clear
	receipts map[string]Receipt
}

func Hidden(id uint32) bool    { return id >= 100000640 && id <= 100000650 }
func Supported(id uint32) bool { return id >= 9700 && id <= 9749 || id >= 100000630 && id <= 100000650 }
func Floor(id uint32) byte {
	switch {
	case id >= 9700 && id <= 9749:
		return byte(id - 9699)
	case id >= 100000630 && id <= 100000639:
		return byte(id - 100000579)
	}
	return 0
}
func FloorID(floor byte) uint32 {
	if floor >= 1 && floor <= 50 {
		return 9699 + uint32(floor)
	}
	if floor >= 51 && floor <= 60 {
		return 100000579 + uint32(floor)
	}
	return 0
}
func EntryKey(id string) string { return "ruby-entry:" + strings.ReplaceAll(id, "-", "") }
func ClearKey(e Entry) string {
	prefix := "ruby-clear"
	if Hidden(e.Dungeon) {
		prefix = "ruby-hidden-clear"
	}
	return fmt.Sprintf("%s:%d:%d", prefix, e.Week, e.Floor)
}
func Week(now time.Time) int64 {
	local := now.UTC().Add(2 * time.Hour)
	local = local.AddDate(0, 0, -((int(local.Weekday()) - int(time.Thursday) + 7) % 7))
	v, _ := strconv.ParseInt(local.Format("20060102"), 10, 64)
	return v
}
func hash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func validID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}
func exact(s string, v any) bool { b, err := json.Marshal(v); return err == nil && string(b) == s }
func (v View) Same(other View) bool {
	return v.Week == other.Week && v.Claimed == other.Claimed && v.Boxes != nil && other.Boxes != nil && slices.Equal(v.Boxes, other.Boxes)
}
func ReadSlots(b []byte, compound bool) ([]int, error) {
	n := 5
	if compound {
		if len(b) != 24 || le.Uint32(b) > 5 {
			return nil, ErrInvalid
		}
		n = int(le.Uint32(b))
		b = b[4:]
	} else if len(b) != 20 {
		return nil, ErrInvalid
	}
	slots := []int{}
	for i := 0; i < 5; i++ {
		id := int(int32(le.Uint32(b[4*i:])))
		if id < -1 || id >= 100 || compound && (i >= n && id != -1 || i < n && id < 0) || id >= 0 && slices.Contains(slots, id) {
			return nil, ErrInvalid
		}
		if id >= 0 {
			slots = append(slots, id)
		}
	}
	return slots, nil
}
func (v View) Indices(slots []int, absolute bool) ([]int, error) {
	visible := make([]int, 0, len(v.Boxes))
	for i, id := range v.Boxes {
		if id != math.MaxUint32 {
			visible = append(visible, i)
		}
	}
	selected := make([]int, 0, len(slots))
	for _, i := range slots {
		if !absolute {
			if i < 0 || i >= len(visible) {
				return nil, ErrInvalid
			}
			i = visible[i]
		}
		if i < 0 || i >= len(v.Boxes) || v.Boxes[i] == math.MaxUint32 || slices.Contains(selected, i) {
			return nil, ErrInvalid
		}
		selected = append(selected, i)
	}
	return selected, nil
}
func (v View) Wire() ([]byte, error) {
	if len(v.Boxes) > 100 || v.Claimed > 5 {
		return nil, ErrInvalid
	}
	b := make([]byte, 4)
	for _, id := range v.Boxes {
		if id == 0 {
			return nil, ErrInvalid
		}
		if id != math.MaxUint32 {
			b = le.AppendUint32(b, id)
		}
	}
	le.PutUint32(b, uint32(len(b)/4-1))
	return append(b, 0, 0, 0, 0, 0, v.Claimed, 0, 0, 0, 0), nil
}

func (l Ledger) ProgressWire(actual uint32) (progress, floor, treasury []byte, err error) {
	if actual == 0 || l.Cleared > 60 {
		return nil, nil, nil, ErrInvalid
	}
	progress = make([]byte, 12)
	le.PutUint32(progress, uint32(l.Cleared))
	kind := byte(0)
	if Hidden(actual) {
		kind = 1
	}
	floor = append(le.AppendUint32([]byte{kind}, actual), l.Cleared)
	treasury, err = l.View.Wire()
	return
}

func (l Ledger) PlanClaim(v View, b []byte, rulesHash string) (ClaimPlan, error) {
	p := ClaimPlan{}
	slots, err := ReadSlots(b, false)
	if err != nil || len(slots) == 0 || v.Week != l.View.Week || !hash(rulesHash) {
		return p, ErrInvalid
	}
	p.Key = ClaimKey(v, b)
	body := savejson.String(base64.StdEncoding.EncodeToString(b))
	if prev, ok := l.Receipt(p.Key); ok {
		var saved Claim
		if json.Unmarshal([]byte(prev.Input), &saved) != nil || saved.Policy != ClaimPolicy || saved.Body != body ||
			!slices.Equal(saved.Slots, slots) || saved.View == nil || !saved.View.Same(v) {
			return p, ErrInvalid
		}
		p.Proof, p.Replay = saved, true
		return p, nil
	}
	if !v.Same(l.View) || int(v.Claimed)+len(slots) > 5 {
		return p, ErrInvalid
	}
	indices, err := v.Indices(slots, false)
	if err != nil {
		return p, err
	}
	items := make([]uint32, 0, len(indices))
	for _, i := range indices {
		items = append(items, v.Boxes[i])
	}
	v.Boxes = slices.Clone(v.Boxes)
	p.Proof = Claim{Policy: ClaimPolicy, Week: v.Week, Body: body, Slots: slots, Items: items, Hash: rulesHash, View: &v}
	return p, nil
}
func ClaimKey(v View, b []byte) string {
	raw, _ := json.Marshal(struct {
		View View
		Body savejson.String
	}{v, savejson.String(base64.StdEncoding.EncodeToString(b))})
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("ruby-treasure-v3:%d:%X", v.Week, sum)
}
func (l *Ledger) Receipt(key string) (Receipt, bool) { r, ok := l.receipts[key]; return r, ok }
func (l *Ledger) replayClaim(r Receipt) error {
	v := &l.View
	var p Claim
	if json.Unmarshal([]byte(r.Input), &p) != nil || !exact(r.Input, p) || p.Week != v.Week || !hash(p.Hash) || r.Result != "claimed" || len(p.Slots) == 0 || len(p.Items) != len(p.Slots) || int(v.Claimed)+len(p.Slots) > 5 {
		return ErrInvalid
	}
	body, err := base64.StdEncoding.DecodeString(string(p.Body))
	if err != nil {
		return err
	}
	legacy := p.Policy == "LocalRubyTreasureSelection/v1"
	var key string
	switch p.Policy {
	case ClaimPolicy:
		if p.View == nil || !p.View.Same(*v) {
			return ErrInvalid
		}
		key = ClaimKey(*p.View, body)
	case "LocalRubyTreasureSelection/v1", "LocalRubyTreasureSelection/v2":
		if p.View != nil {
			return ErrInvalid
		}
		prefix := "ruby-treasure-v2"
		if legacy {
			prefix = "ruby-treasure"
		}
		key = fmt.Sprintf("%s:%d:%X", prefix, v.Week, sha256.Sum256(body))
	default:
		return ErrInvalid
	}
	slots, err := ReadSlots(body, legacy)
	if err != nil || key != r.Key || !slices.Equal(slots, p.Slots) {
		return ErrInvalid
	}
	selected, err := v.Indices(slots, legacy)
	if err != nil {
		return err
	}
	want := map[uint32]int64{}
	for i, pos := range selected {
		if p.Items[i] != v.Boxes[pos] {
			return ErrInvalid
		}
		want[p.Items[i]]++
	}
	for _, g := range r.Grants {
		n, ok := want[g.TemplateId]
		if !validID(g.InstanceId) || g.Location != 0 || !ok {
			return ErrInvalid
		}
		want[g.TemplateId] = n - int64(g.Granted)
	}
	for _, n := range want {
		if n != 0 {
			return ErrInvalid
		}
	}
	for _, pos := range selected {
		v.Boxes[pos] = math.MaxUint32
	}
	v.Claimed += byte(len(slots))
	return nil
}
func Read(raw map[string]json.RawMessage, char uint16, revision int64, now time.Time) (Ledger, error) {
	l := Ledger{View: View{Week: Week(now), Boxes: []uint32{}}, Entries: map[string]Entry{}, Clears: map[string]Clear{}, receipts: map[string]Receipt{}}
	get := func(key string) (Receipt, error) {
		var r Receipt
		b, ok := raw[key]
		if !ok || json.Unmarshal(b, &r) != nil || r.Key != key || r.Revision <= 0 || r.Revision > revision {
			return r, ErrInvalid
		}
		return r, nil
	}
	for key := range raw {
		if !strings.HasPrefix(key, "ruby-") {
			continue
		}
		r, err := get(key)
		if err != nil {
			return l, err
		}
		l.receipts[key] = r
		if !strings.HasPrefix(key, "ruby-entry:") {
			continue
		}
		var e Entry
		if json.Unmarshal([]byte(r.Input), &e) != nil || !exact(r.Input, e) || e.Policy != Policy || !validID(e.Run) || key != EntryKey(e.Run) || e.Character != char || !Supported(e.Requested) || !Supported(e.Dungeon) || e.Floor < 1 || e.Floor > 60 || !Hidden(e.Dungeon) && e.Floor != Floor(e.Dungeon) || !hash(e.Hash) || e.Week < 20000101 || r.Result != "admitted" || len(r.Grants) != 0 {
			return l, ErrInvalid
		}
		l.Entries[e.Run] = e
		l.View.Week = max(l.View.Week, e.Week)
	}
	floors := map[int64]map[byte]bool{}
	for k, r := range l.receipts {
		if !strings.HasPrefix(k, "ruby-clear:") && !strings.HasPrefix(k, "ruby-hidden-clear:") {
			continue
		}
		var c Clear
		if json.Unmarshal([]byte(r.Input), &c) != nil || !exact(r.Input, c) || c.Policy != Policy {
			return l, ErrInvalid
		}
		e, ok := l.Entries[c.Run]
		if !ok || c.Character != char || c.Dungeon != e.Dungeon || c.Floor != e.Floor || c.Week != e.Week || c.Hash != e.Hash || k != ClearKey(e) || r.Result != "cleared;special-rewards-disabled" || len(r.Grants) != 0 || r.Revision <= l.receipts[EntryKey(c.Run)].Revision {
			return l, ErrInvalid
		}
		death, err := get(c.Death)
		if err != nil || death.Revision != r.Revision || !terminal(death, c) {
			return l, ErrInvalid
		}
		last := c.Floor
		if a := c.Advance; a != nil {
			if a.Policy != RoutePolicy || a.First != c.Floor || a.Last < c.Floor || a.Last > 60 || a.Last-c.Floor > 3 || a.Boxes == nil || len(a.Boxes) > 2 || a.Next != 0 && !Supported(a.Next) {
				return l, ErrInvalid
			}
			last = a.Last
		}
		if Hidden(c.Dungeon) {
			if e.FromClear == nil {
				return l, ErrInvalid
			}
			prior, ok := l.receipts[*e.FromClear]
			var origin Clear
			if !ok || json.Unmarshal([]byte(prior.Input), &origin) != nil || origin.Week != c.Week || origin.Advance == nil || origin.Advance.Next != c.Dungeon || c.Floor != origin.Advance.Last || c.Advance == nil || last != c.Floor {
				return l, ErrInvalid
			}
		} else {
			if floors[c.Week] == nil {
				floors[c.Week] = map[byte]bool{}
			}
			for i := c.Floor; i <= last; i++ {
				if floors[c.Week][i] {
					return l, ErrInvalid
				}
				floors[c.Week][i] = true
			}
		}
		l.Clears[k] = c
	}
	for week, set := range floors {
		for i := 1; i <= len(set); i++ {
			if !set[byte(i)] {
				return l, ErrInvalid
			}
		}
		if week == l.View.Week {
			l.Cleared = byte(len(set))
		}
	}
	type event struct {
		revision int64
		key      string
		order    int
	}
	var events []event
	for k, c := range l.Clears {
		if c.Week == l.View.Week && c.Advance != nil {
			events = append(events, event{l.receipts[k].Revision, k, 0})
		}
	}
	for k, r := range l.receipts {
		for _, prefix := range []string{"ruby-treasure", "ruby-treasure-v2", "ruby-treasure-v3", "ruby-compound"} {
			if strings.HasPrefix(k, fmt.Sprintf("%s:%d:", prefix, l.View.Week)) {
				order := 1
				if prefix == "ruby-compound" {
					order = 2
				}
				events = append(events, event{r.Revision, k, order})
				break
			}
		}
	}
	slices.SortFunc(events, func(a, b event) int {
		if a.revision < b.revision {
			return -1
		}
		if a.revision > b.revision {
			return 1
		}
		if a.order != b.order {
			return a.order - b.order
		}
		return strings.Compare(a.key, b.key)
	})
	for _, ev := range events {
		switch ev.order {
		case 0:
			award := l.Clears[ev.key].Advance.Boxes
			if len(l.View.Boxes)+len(award) > 100 {
				return l, ErrInvalid
			}
			for _, id := range award {
				if !slices.Contains(boxes, id) {
					return l, ErrInvalid
				}
			}
			l.View.Boxes = append(l.View.Boxes, award...)
		case 1:
			if err := l.replayClaim(l.receipts[ev.key]); err != nil {
				return l, err
			}
		case 2:
			if err := l.replayCompound(l.receipts[ev.key]); err != nil {
				return l, err
			}
		}
	}
	return l, nil
}
