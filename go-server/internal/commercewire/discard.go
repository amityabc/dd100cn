package commercewire

import (
	"bytes"
	"encoding/binary"
	"math"

	"s9a1/server/internal/storage"
)

type Discard struct {
	List, Context uint32
	State         byte
	Rows          []storage.DiscardLine
}

func ReadDiscard(body []byte) (Discard, error) {
	var q Discard
	if len(body) < 5 || len(body) > 4096 || int(le.Uint32(body)) != len(body)-5 {
		return q, storage.ErrInvalid
	}
	q.State = body[len(body)-1]
	b := bytes.NewReader(body[4 : len(body)-1])
	var hasList, hasContext bool
	for b.Len() != 0 {
		tag, err := readVarint(b)
		if err != nil {
			return q, err
		}
		value, err := readVarint(b)
		if err != nil {
			return q, err
		}
		switch {
		case tag == 16 && !hasList:
			q.List, hasList = value, true
		case tag == 32 && !hasContext:
			q.Context, hasContext = value, true
		case tag == 26 && len(q.Rows) < 64:
			if value > uint32(b.Len()) {
				return q, storage.ErrInvalid
			}
			entry := make([]byte, value)
			if _, err := b.Read(entry); err != nil && len(entry) != 0 {
				return q, err
			}
			row, err := discardEntry(entry)
			if err != nil {
				return q, err
			}
			q.Rows = append(q.Rows, row)
		default:
			return q, storage.ErrInvalid
		}
	}
	if !hasList || !hasContext || len(q.Rows) == 0 {
		return q, storage.ErrInvalid
	}
	return q, nil
}

func discardEntry(entry []byte) (storage.DiscardLine, error) {
	b := bytes.NewReader(entry)
	var fields [4]uint32
	var seen byte
	for b.Len() != 0 {
		tag, err := readVarint(b)
		if err != nil || tag < 8 || tag > 32 || tag%8 != 0 {
			return storage.DiscardLine{}, storage.ErrInvalid
		}
		index := tag/8 - 1
		if seen&(1<<index) != 0 {
			return storage.DiscardLine{}, storage.ErrInvalid
		}
		seen |= 1 << index
		fields[index], err = readVarint(b)
		if err != nil {
			return storage.DiscardLine{}, err
		}
	}
	if seen != 15 || fields[1] > math.MaxUint16 {
		return storage.DiscardLine{}, storage.ErrInvalid
	}
	return storage.DiscardLine{Action: fields[0], Slot: uint16(fields[1]), TemplateID: fields[2], Count: fields[3]}, nil
}

func readVarint(r *bytes.Reader) (uint32, error) {
	before := r.Len()
	n, err := binary.ReadUvarint(r)
	if err != nil || n > math.MaxUint32 || before-r.Len() != len(binary.AppendUvarint(nil, n)) {
		return 0, storage.ErrInvalid
	}
	return uint32(n), nil
}

func DiscardAccepted(rows []storage.DiscardLine) []byte {
	b := []byte{8, 0}
	for _, row := range rows {
		e := binary.AppendUvarint([]byte{8}, uint64(row.Slot))
		e = binary.AppendUvarint(append(e, 16), uint64(row.Count))
		e = binary.AppendUvarint(append(e, 24), uint64(row.Action))
		b = append(binary.AppendUvarint(append(b, 18), uint64(len(e))), e...)
	}
	b = append(b, 24, 0)
	return append(le.AppendUint32([]byte{1}, uint32(len(b))), b...)
}

func DiscardRejected() []byte { return []byte{0, 0, 0, 4, 0, 0, 0, 8, 0, 24, 0} }

type Disassembly struct {
	NPC   uint32
	Slots []uint16
}

func ReadDisassembly(b []byte) (Disassembly, error) {
	var q Disassembly
	if len(b) < 8 || b[0] != 0 || le.Uint16(b[1:]) != math.MaxUint16 || b[7] < 1 || b[7] > 56 || len(b) != 8+2*int(b[7]) {
		return q, storage.ErrInvalid
	}
	q.NPC = le.Uint32(b[3:])
	var seen [65]bool
	for i := 8; i < len(b); i += 2 {
		slot := le.Uint16(b[i:])
		if slot < 9 || slot > 64 || seen[slot] {
			return q, storage.ErrInvalid
		}
		seen[slot] = true
		q.Slots = append(q.Slots, slot)
	}
	return q, nil
}

func Disassembled(q Disassembly, grants []storage.Asset) []byte {
	b := []byte{1, byte(len(q.Slots))}
	for _, slot := range q.Slots {
		b = le.AppendUint16(b, slot)
	}
	b = append(b, 0, 255, 255, byte(len(grants)))
	for _, a := range grants {
		b = le.AppendUint16(b, a.Slot)
		b = le.AppendUint32(b, a.TemplateID)
		b = le.AppendUint32(b, a.Count)
	}
	return b
}
