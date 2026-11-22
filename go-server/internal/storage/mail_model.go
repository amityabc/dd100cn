package storage

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	"s9a1/server/internal/protocol"
)

type MailAsset struct {
	InstanceID           string `json:"InstanceId"`
	Template, Attributes json.RawMessage
	Count                uint32
	Location             byte
	Slot                 uint16
	Raw119, Detail       []byte
}
type MailAttachment struct {
	ClaimID int64 `json:"ClaimId"`
	Asset   MailAsset
	Claimed bool
}
type MailLetter struct {
	MailID                                        int64 `json:"MailId"`
	DeliveryKey, Fingerprint, Sender, Title, Body string
	Gold, Created                                 uint32
	Attachments                                   []MailAttachment
	Read, Saved, Deleted, GoldClaimed             bool
}
type MailTransfer struct {
	ClaimID int64 `json:"ClaimId"`
	Source  MailAsset
	Targets []MailTarget
}
type Mailbox struct {
	Mails            []MailLetter
	DeliveryReceipts map[string]json.RawMessage
	ClaimTransfers   map[int64]MailTransfer
}

type MailTarget struct {
	InstanceID string `json:"InstanceId"`
	Slot       uint16
	Count      uint32
}

func (a MailAsset) MarshalJSON() ([]byte, error) {
	type alias MailAsset
	numbers := func(b []byte) []uint16 {
		out := make([]uint16, len(b))
		for i, v := range b {
			out[i] = uint16(v)
		}
		return out
	}
	return json.Marshal(struct {
		alias
		Raw119, Detail []uint16
	}{alias(a), numbers(a.Raw119), numbers(a.Detail)})
}

func validateMailText(text string, size int) error {
	if strings.ContainsRune(text, 0) {
		return ErrInvalid
	}
	_, err := protocol.EncodeText(text, size)
	return err
}

func ReadMailbox(c Character) (Mailbox, error) {
	m := Mailbox{Mails: []MailLetter{}, DeliveryReceipts: map[string]json.RawMessage{}, ClaimTransfers: map[int64]MailTransfer{}}
	var profile struct{ Mailbox json.RawMessage }
	if err := json.Unmarshal(c.Gameplay, &profile); err != nil {
		return m, err
	}
	if len(profile.Mailbox) != 0 {
		if bytes.Equal(bytes.TrimSpace(profile.Mailbox), []byte("null")) {
			return m, ErrInvalid
		}
		if err := json.Unmarshal(profile.Mailbox, &m); err != nil {
			return m, err
		}
	}
	if m.ClaimTransfers == nil {
		m.ClaimTransfers = map[int64]MailTransfer{}
	}
	if m.DeliveryReceipts == nil {
		m.DeliveryReceipts = map[string]json.RawMessage{}
	}
	return m, m.Validate()
}

type MailboxPage struct {
	Letters  []*MailLetter
	Overflow int
	Unread   map[int64]struct{}
}

func (m Mailbox) Page() MailboxPage {
	var ordinary, saved []*MailLetter
	p := MailboxPage{Unread: map[int64]struct{}{}}
	for i := range m.Mails {
		l := &m.Mails[i]
		if l.Deleted {
			continue
		}
		if !l.Read {
			p.Unread[l.MailID] = struct{}{}
		}
		if l.Saved {
			saved = append(saved, l)
		} else {
			ordinary = append(ordinary, l)
		}
	}
	order := func(a, b *MailLetter) int {
		if a.Created < b.Created || a.Created == b.Created && a.MailID < b.MailID {
			return -1
		}
		if a.Created > b.Created || a.Created == b.Created && a.MailID > b.MailID {
			return 1
		}
		return 0
	}
	slices.SortFunc(ordinary, order)
	p.Overflow = max(0, len(ordinary)-20)
	p.Letters = append(ordinary[:len(ordinary)-p.Overflow:len(ordinary)-p.Overflow], saved...)
	slices.SortFunc(p.Letters, func(a, b *MailLetter) int { return -order(a, b) })
	return p
}

func (l MailLetter) FullyClaimed() bool {
	if l.Gold != 0 && !l.GoldClaimed {
		return false
	}
	for _, a := range l.Attachments {
		if !a.Claimed {
			return false
		}
	}
	return true
}

func sameAsset(a, b MailAsset) bool {
	return strings.EqualFold(a.InstanceID, b.InstanceID) && a.Count == b.Count && a.Location == b.Location && a.Slot == b.Slot && bytes.Equal(a.Raw119, b.Raw119) && bytes.Equal(a.Detail, b.Detail) && equalJSON(a.Template, b.Template) && equalJSON(a.Attributes, b.Attributes)
}
func mailUID(s string) bool {
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
func (m Mailbox) Validate() error {
	if m.Mails == nil {
		return ErrInvalid
	}
	seen := map[int64]bool{}
	claims := map[int64]MailAttachment{}
	visible, summaries := 0, 0
	for _, l := range m.Mails {
		if l.MailID <= 0 || seen[l.MailID] || l.Attachments == nil {
			return ErrInvalid
		}
		seen[l.MailID] = true
		if err := validateMailText(l.Sender, 30); err != nil {
			return err
		}
		if err := validateMailText(l.Title, 512); err != nil {
			return err
		}
		if err := validateMailText(l.Body, 512); err != nil {
			return err
		}
		pending := 0
		for _, a := range l.Attachments {
			if a.ClaimID <= 0 || seen[a.ClaimID] {
				return ErrInvalid
			}
			seen[a.ClaimID] = true
			claims[a.ClaimID] = a
			if !a.Claimed {
				pending++
			}
		}
		if l.Deleted {
			if pending > 0 || l.Gold > 0 && !l.GoldClaimed {
				return ErrInvalid
			}
		} else {
			visible++
			summaries += max(1, pending)
		}
	}
	if visible > 100 || summaries > 255 {
		return ErrInvalid
	}
	for id, r := range m.ClaimTransfers {
		a, ok := claims[id]
		if !ok || r.ClaimID != id || !a.Claimed || !mailUID(r.Source.InstanceID) || !sameAsset(a.Asset, r.Source) || len(r.Targets) == 0 {
			return ErrInvalid
		}
		var total uint64
		used := map[string]bool{}
		for _, x := range r.Targets {
			key := strings.ToLower(x.InstanceID)
			if !mailUID(key) || used[key] || x.Count == 0 {
				return ErrInvalid
			}
			used[key] = true
			total += uint64(x.Count)
		}
		if total != uint64(r.Source.Count) {
			return ErrInvalid
		}
	}
	return nil
}
