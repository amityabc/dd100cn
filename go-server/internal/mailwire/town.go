// Package mailwire projects an existing character mailbox on the first town load.
package mailwire

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
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

type writer []byte

func (w *writer) u8(n byte)      { *w = append(*w, n) }
func (w *writer) u16(n uint16)   { *w = le.AppendUint16(*w, n) }
func (w *writer) u32(n uint32)   { *w = le.AppendUint32(*w, n) }
func (w *writer) id(n int64)     { *w = le.AppendUint64(*w, uint64(n)) }
func (w *writer) block(b []byte) { w.u32(uint32(len(b))); *w = append(*w, b...) }

type summary struct {
	mail       *storage.MailLetter
	attachment *storage.MailAttachment
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
func (w *writer) letter(l *storage.MailLetter) error {
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
	m, err := storage.ReadMailbox(c)
	if err != nil {
		return nil, err
	}
	page := m.Page()
	out, err := Pages(page)
	if err != nil {
		return nil, err
	}
	return append(out, Packet{99, Alarm(len(page.Unread))}), nil
}

func Pages(page storage.MailboxPage) ([]Packet, error) {
	overflow := page.Overflow
	var out []Packet
	var summaries []summary
	var group []*storage.MailLetter
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
	for _, l := range page.Letters {
		var attachments []*storage.MailAttachment
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
	return out, nil
}
