package mailwire

import (
	"math"
	"unicode/utf8"

	"s9a1/server/internal/storage"
)

func ReadSend(op uint16, b []byte) (storage.MailSend, error) {
	req := storage.MailSend{}
	block := func(maximum int) (string, error) {
		if len(b) < 4 {
			return "", invalidRecipient
		}
		size := int(le.Uint32(b))
		if size < 0 || size > maximum || size > len(b)-4 {
			return "", invalidRecipient
		}
		field := b[4 : 4+size]
		b = b[4+size:]
		return decode936(field)
	}
	var err error
	req.Recipient, err = block(19)
	if err != nil || len(b) < 4 || op != 94 && op != 315 {
		return req, invalidRecipient
	}
	req.Gold, b = le.Uint32(b), b[4:]
	if req.Gold > math.MaxInt32 {
		return req, invalidRecipient
	}
	count := 1
	if op == 315 {
		if len(b) < 1 || b[0] < 1 || b[0] > 10 {
			return req, invalidRecipient
		}
		count, b = int(b[0]), b[1:]
	}
	if len(b) < 11*count {
		return req, invalidRecipient
	}
	seen := map[[2]uint16]bool{}
	for range count {
		a := storage.MailSendAttachment{Space: b[0], Slot: le.Uint16(b[1:]), TemplateID: le.Uint32(b[3:]), Count: le.Uint32(b[7:])}
		b = b[11:]
		if op == 94 && a == (storage.MailSendAttachment{}) {
			continue
		}
		key := [2]uint16{uint16(a.Space), a.Slot}
		if a.TemplateID == 0 || a.Count == 0 || a.TemplateID > math.MaxInt32 || a.Count > math.MaxInt32 || seen[key] {
			return req, invalidRecipient
		}
		seen[key] = true
		req.Attachments = append(req.Attachments, a)
	}
	req.Body, err = block(510)
	// CP936 has only BMP scalars, so the rune and native UTF-16 counts coincide.
	if err != nil || utf8.RuneCountInString(req.Body) > 255 || len(b) != 5 {
		return req, invalidRecipient
	}
	req.SpecialRecipient, req.Mode = le.Uint32(b), b[4]
	return req, nil
}

func ReadBatch(b []byte, withState bool) ([]int64, uint16, error) {
	if len(b) < 4 {
		return nil, 0, invalid
	}
	n := int(le.Uint32(b))
	extra := 0
	if withState {
		extra = 2
	}
	if n < 1 || n > 100 || len(b) != 4+8*n+extra {
		return nil, 0, invalid
	}
	ids := make([]int64, n)
	unique := make(map[int64]bool, n)
	for i := range ids {
		id := int64(le.Uint64(b[4+8*i:]))
		if id <= 0 || unique[id] {
			return nil, 0, invalid
		}
		unique[id], ids[i] = true, id
	}
	if withState {
		return ids, le.Uint16(b[len(b)-2:]), nil
	}
	return ids, 0, nil
}

func Opened(overflow int) []byte  { return le.AppendUint16([]byte{1}, uint16(overflow)) }
func Alarm(unread int) []byte     { return le.AppendUint16(nil, uint16(unread)) }
func Rejected(code uint16) []byte { return le.AppendUint16([]byte{0}, code) }

func Claimed(rows []storage.MailClaim) []byte {
	w := writer{1}
	w.u32(uint32(len(rows)))
	for _, row := range rows {
		w.id(row.MailID)
		w.id(row.ClaimID)
		w.u32(0)
	}
	return w
}

func Changed(ids []int64, state uint16) []byte {
	w := writer{1}
	w.u32(uint32(len(ids)))
	for _, id := range ids {
		w.id(id)
		w.u16(state)
	}
	return w
}
