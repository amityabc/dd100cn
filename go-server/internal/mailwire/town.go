// Package mailwire projects an existing character mailbox on the first town load.
package mailwire

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

type Packet struct {
	Opcode uint16
	Body   []byte
}

var le = binary.LittleEndian
var invalid = errors.New("invalid saved mailbox")

type asset struct {
	InstanceID           string
	Template, Attributes json.RawMessage
	Count                uint32
	Location             byte
	Slot                 uint16
	Raw119, Detail       []byte
}
type attachment struct {
	ClaimID int64
	Asset   asset
	Claimed bool
}
type letter struct {
	MailID                            int64
	Sender, Title, Body               string
	Gold, Created                     uint32
	Attachments                       []attachment
	Read, Saved, Deleted, GoldClaimed bool
}
type transfer struct {
	ClaimID int64
	Source  asset
	Targets []struct {
		InstanceID string
		Count      uint32
	}
}
type mailbox struct {
	Mails          []letter
	ClaimTransfers map[int64]transfer
}

func text(s string, stored, wire int) ([]byte, error) {
	if strings.ContainsRune(s, 0) {
		return nil, invalid
	}
	raw, err := protocol.EncodeText(s, stored)
	if err != nil {
		return nil, err
	}
	for len(raw) > wire {
		r, size := utf8.DecodeLastRuneInString(s)
		part, err := protocol.EncodeText(string(r), 4)
		if err != nil {
			return nil, err
		}
		raw = raw[:len(raw)-len(part)]
		s = s[:len(s)-size]
	}
	return raw, nil
}
func sameJSON(a, b json.RawMessage) bool {
	parse := func(raw json.RawMessage) (any, error) {
		var v any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		err := d.Decode(&v)
		return v, err
	}
	x, e := parse(a)
	y, f := parse(b)
	return e == nil && f == nil && reflect.DeepEqual(x, y)
}
func sameAsset(a, b asset) bool {
	return strings.EqualFold(a.InstanceID, b.InstanceID) && a.Count == b.Count && a.Location == b.Location && a.Slot == b.Slot && bytes.Equal(a.Raw119, b.Raw119) && bytes.Equal(a.Detail, b.Detail) && sameJSON(a.Template, b.Template) && sameJSON(a.Attributes, b.Attributes)
}
func notEmptyID(s string) bool {
	id := strings.ReplaceAll(s, "-", "")
	if len(id) != 32 || id == strings.Repeat("0", 32) {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
func (m mailbox) validate() error {
	if m.Mails == nil {
		return invalid
	}
	seen := map[int64]bool{}
	claims := map[int64]attachment{}
	visible, summaries := 0, 0
	for _, l := range m.Mails {
		if l.MailID <= 0 || seen[l.MailID] || l.Attachments == nil {
			return invalid
		}
		seen[l.MailID] = true
		if _, err := text(l.Sender, 30, 30); err != nil {
			return err
		}
		if _, err := text(l.Title, 512, 512); err != nil {
			return err
		}
		if _, err := text(l.Body, 512, 512); err != nil {
			return err
		}
		pending := 0
		for _, a := range l.Attachments {
			if a.ClaimID <= 0 || seen[a.ClaimID] {
				return invalid
			}
			seen[a.ClaimID] = true
			claims[a.ClaimID] = a
			if !a.Claimed {
				pending++
			}
		}
		if l.Deleted {
			if pending > 0 || l.Gold > 0 && !l.GoldClaimed {
				return invalid
			}
		} else {
			visible++
			summaries += max(1, pending)
		}
	}
	if visible > 100 || summaries > 255 {
		return invalid
	}
	for id, r := range m.ClaimTransfers {
		a, ok := claims[id]
		if !ok || r.ClaimID != id || !a.Claimed || !notEmptyID(r.Source.InstanceID) || !sameAsset(a.Asset, r.Source) || len(r.Targets) == 0 {
			return invalid
		}
		var total uint64
		used := map[string]bool{}
		for _, x := range r.Targets {
			key := strings.ToLower(x.InstanceID)
			if !notEmptyID(key) || used[key] || x.Count == 0 {
				return invalid
			}
			used[key] = true
			total += uint64(x.Count)
		}
		if total != uint64(r.Source.Count) {
			return invalid
		}
	}
	return nil
}

type writer []byte

func (w *writer) u8(n byte)      { *w = append(*w, n) }
func (w *writer) u16(n uint16)   { *w = le.AppendUint16(*w, n) }
func (w *writer) u32(n uint32)   { *w = le.AppendUint32(*w, n) }
func (w *writer) id(n int64)     { *w = le.AppendUint64(*w, uint64(n)) }
func (w *writer) block(b []byte) { w.u32(uint32(len(b))); *w = append(*w, b...) }

type summary struct {
	mail       *letter
	attachment *attachment
	gold       bool
}

func (w *writer) summary(s summary) error {
	l := s.mail
	id := l.MailID
	var tid uint32
	var kind byte
	core := make([]byte, 119)
	core[93] = 255
	if a := s.attachment; a != nil {
		id = a.ClaimID
		kind = 1
		var template struct{ TemplateID uint32 }
		if err := json.Unmarshal(a.Asset.Template, &template); err != nil {
			return err
		}
		tid = template.TemplateID
		var err error
		core, err = inventorywire.MailItem119(storage.Asset{Source: "item", TemplateID: tid, Template: a.Asset.Template, Attributes: a.Asset.Attributes, Count: a.Asset.Count, Slot: a.Asset.Slot, Core: a.Asset.Raw119})
		if err != nil {
			return err
		}
	}
	sender, err := text(l.Sender, 30, 29)
	if err != nil {
		return err
	}
	w.id(id)
	w.u32(0)
	w.block(sender)
	gold := uint32(0)
	if s.gold {
		gold = min(l.Gold, math.MaxInt32)
	}
	w.u32(gold)
	w.u32(tid)
	w.u8(kind)
	w.u32(le.Uint32(core[6:]))
	w.u16(le.Uint16(core[11:]))
	w.u8(core[10])
	w.u32(le.Uint32(core[14:]))
	w.u8(core[18])
	w.u8(core[19])
	w.u16(le.Uint16(core[20:]))
	*w = append(*w, core...)
	*w = append(*w, make([]byte, 34)...)
	w.u32(0)
	w.id(l.MailID)
	w.u8(0)
	return nil
}
func (w *writer) letter(l *letter) error {
	sender, err := text(l.Sender, 30, 29)
	if err != nil {
		return err
	}
	body, err := text(l.Body, 512, 511)
	if err != nil {
		return err
	}
	w.id(l.MailID)
	w.u32(0)
	w.block(sender)
	w.block(body)
	w.u32(l.Created)
	status := uint16(1)
	if l.Saved {
		status = 3
	} else if l.Read {
		status = 2
	}
	w.u16(status)
	w.u8(0)
	return nil
}

// Town emits the original first N97 page(s), followed by the reset N99 unread count.
func Town(c storage.Character) ([]Packet, error) {
	var profile struct{ Mailbox json.RawMessage }
	if err := json.Unmarshal(c.Gameplay, &profile); err != nil {
		return nil, err
	}
	m := mailbox{Mails: []letter{}}
	if len(profile.Mailbox) > 0 {
		if string(profile.Mailbox) == "null" {
			return nil, invalid
		}
		if err := json.Unmarshal(profile.Mailbox, &m); err != nil {
			return nil, err
		}
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	ordinary, saved := []*letter{}, []*letter{}
	unread := 0
	for i := range m.Mails {
		l := &m.Mails[i]
		if l.Deleted {
			continue
		}
		if !l.Read {
			unread++
		}
		if l.Saved {
			saved = append(saved, l)
		} else {
			ordinary = append(ordinary, l)
		}
	}
	order := func(a, b *letter) int {
		if a.Created < b.Created {
			return -1
		}
		if a.Created > b.Created {
			return 1
		}
		if a.MailID < b.MailID {
			return -1
		}
		if a.MailID > b.MailID {
			return 1
		}
		return 0
	}
	slices.SortFunc(ordinary, order)
	overflow := max(0, len(ordinary)-20)
	visible := append(ordinary[:len(ordinary)-overflow:len(ordinary)-overflow], saved...)
	slices.SortFunc(visible, func(a, b *letter) int { return -order(a, b) })
	var out []Packet
	var summaries []summary
	var group []*letter
	flush := func() error {
		w := writer{byte(len(summaries)), 0}
		if len(out) > 0 {
			w[1] = 1
		}
		for _, s := range summaries {
			if err := w.summary(s); err != nil {
				return err
			}
		}
		w.u16(uint16(overflow))
		w.u16(uint16(len(group)))
		for _, l := range group {
			if err := w.letter(l); err != nil {
				return err
			}
		}
		out = append(out, Packet{97, w})
		summaries = nil
		group = nil
		return nil
	}
	for _, l := range visible {
		var attachments []*attachment
		for i := range l.Attachments {
			if !l.Attachments[i].Claimed {
				attachments = append(attachments, &l.Attachments[i])
			}
		}
		count := len(attachments)
		if count == 0 && l.Gold > 0 && !l.GoldClaimed {
			count = 1
		}
		if len(summaries)+count > 255 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		for i, a := range attachments {
			summaries = append(summaries, summary{l, a, i == 0 && !l.GoldClaimed})
		}
		if len(attachments) == 0 && count != 0 {
			summaries = append(summaries, summary{l, nil, true})
		}
		group = append(group, l)
	}
	if len(group) > 0 || len(out) == 0 {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	return append(out, Packet{99, le.AppendUint16(nil, uint16(unread))}), nil
}
