package commercewire

import (
	"math"

	"s9a1/server/internal/storage"
)

func ReadGift(op uint16, b []byte) (storage.GiftRequest, error) {
	q := storage.GiftRequest{Opcode: op}
	if len(b) < 3 {
		return q, storage.ErrInvalid
	}
	q.Slot = le.Uint16(b)
	if (q.Slot < 3 || q.Slot > 8) && (q.Slot < 65 || q.Slot > 120) {
		return q, storage.ErrInvalid
	}
	if op == 510 {
		count := int(b[2])
		if len(b) != 3+5*count {
			return q, storage.ErrInvalid
		}
		choice := storage.GiftChoice{Abilities: map[uint32]byte{}}
		for i := 0; i < count; i++ {
			row := b[3+5*i:]
			id := le.Uint32(row)
			if id == 0 || id == math.MaxUint32 {
				return q, storage.ErrInvalid
			}
			if _, ok := choice.Abilities[id]; ok {
				return q, storage.ErrInvalid
			}
			choice.Items = append(choice.Items, id)
			choice.Abilities[id] = row[4]
		}
		if count != 0 {
			q.Choices = append(q.Choices, choice)
		}
		return q, nil
	}
	if op != 160 || len(b) < 6 || len(b) > 2305 || le.Uint32(b[2:]) != 1 {
		return q, storage.ErrInvalid
	}
	if len(b) == 6 {
		q.Random = true
		return q, nil
	}
	if len(b) < 14 || b[len(b)-1] != 0 {
		return q, storage.ErrInvalid
	}
	q.Category, q.Subcategory = b[6], b[7]
	for n := 1; n <= 255 && 10+4*n <= len(b); n++ {
		at := 8 + 4*n
		attrs := int(b[at])
		if attrs > n || len(b) != 10+4*n+5*attrs {
			continue
		}
		ids := map[uint32]bool{}
		choice := storage.GiftChoice{Abilities: map[uint32]byte{}}
		valid := true
		for i := 0; i < n; i++ {
			id := le.Uint32(b[8+4*i:])
			if id == math.MaxUint32 || ids[id] {
				valid = false
				break
			}
			ids[id] = true
			choice.Items = append(choice.Items, id)
		}
		if !valid {
			continue
		}
		for i := 0; i < attrs; i++ {
			o := b[at+1+5*i:]
			id := le.Uint32(o)
			_, exists := choice.Abilities[id]
			if !ids[id] || exists || o[4] == 255 {
				valid = false
				break
			}
			choice.Abilities[id] = o[4]
		}
		if valid {
			q.Choices = append(q.Choices, choice)
		}
	}
	if len(q.Choices) == 0 {
		return q, storage.ErrInvalid
	}
	return q, nil
}

func GiftOpened(op, slot uint16, o storage.GiftResult) []byte {
	if op == 510 {
		return le.AppendUint16([]byte{1}, slot)
	}
	b := le.AppendUint32([]byte{1, 0}, o.TemplateID)
	b = le.AppendUint16(b, slot)
	b = le.AppendUint32(b, max(1, o.Consumed))
	b = le.AppendUint32(b, 0)
	b = le.AppendUint16(b, uint16(len(o.Rewards)))
	for _, row := range o.Rewards {
		b = le.AppendUint32(b, row.TemplateID)
		b = le.AppendUint32(b, row.CountOrSeed)
		b = append(b, 0, row.AttachType)
	}
	return b
}
