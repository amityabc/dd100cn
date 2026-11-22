package questwire

import (
	"errors"
	"math"
	"s9a1/server/internal/storage"
)

func ReadAvatarCompound(op uint16, b []byte) (storage.AvatarCompoundRequest, []byte, error) {
	q := storage.AvatarCompoundRequest{Opcode: op}
	bad := errors.New("invalid native avatar compound request")
	switch op {
	case 99:
		if len(b) != 22 {
			return q, nil, bad
		}
		q.Source = little.Uint16(b)
		q.Inputs = []storage.AvatarCompoundInput{
			{Slot: little.Uint16(b[2:]), Template: little.Uint32(b[4:])},
			{Slot: little.Uint16(b[8:]), Template: little.Uint32(b[10:])},
		}
		q.Product, q.Ability, q.RandomStyle, q.Kind = little.Uint32(b[14:]), little.Uint16(b[18:]), b[20], b[21]
		if q.RandomStyle > 1 {
			return q, nil, bad
		}
	case 1113:
		if len(b) != 144 {
			return q, nil, bad
		}
		b = b[13:]
		q.Source, q.Product, q.Ability = little.Uint16(b), little.Uint32(b[3:]), little.Uint16(b[7:])
		n := int(little.Uint16(b[9:]))
		if b[2] != 0 || n < 1 || n > 8 {
			return q, nil, bad
		}
		b = b[:11+n*6]
		for i := 11; i < len(b); i += 6 {
			q.Inputs = append(q.Inputs, storage.AvatarCompoundInput{Slot: little.Uint16(b[i:]), Template: little.Uint32(b[i+2:])})
		}
	default:
		return q, nil, bad
	}
	if op == 1113 && (q.Product == 0 || q.Product == math.MaxUint32 || q.Ability >= 255) {
		return q, nil, bad
	}
	seen := map[uint16]bool{}
	for _, r := range q.Inputs {
		if r.Slot > 479 || seen[r.Slot] || op == 1113 && (r.Template == 0 || r.Template == math.MaxUint32) {
			return q, nil, bad
		}
		seen[r.Slot] = true
	}
	return q, b, nil
}

func AvatarCompoundAccepted(q storage.AvatarCompoundRequest, products []storage.AvatarCompoundProduct) []byte {
	if q.Opcode == 1113 {
		b := make([]byte, 5, 55)
		b[0] = 1
		b = little.AppendUint16(b, products[0].Slot)
		b = little.AppendUint32(b, q.Product)
		b = little.AppendUint16(b, q.Ability)
		b = little.AppendUint16(b, 0)
		for i := range 8 {
			n := uint16(math.MaxUint16)
			if i < len(q.Inputs) {
				n = q.Inputs[i].Slot
			}
			b = little.AppendUint16(b, n)
		}
		return append(b, make([]byte, 24)...)
	}
	b := []byte{1, 3, 0}
	b = little.AppendUint16(b, q.Source)
	b = little.AppendUint32(b, 1)
	for _, r := range q.Inputs {
		b = append(b, 1)
		b = little.AppendUint16(b, r.Slot)
		b = little.AppendUint32(b, 1)
	}
	for _, p := range products {
		b = little.AppendUint16(b, p.Slot)
		b = little.AppendUint32(b, p.Template)
		b = little.AppendUint32(b, uint32(p.Seconds))
		b = little.AppendUint16(b, p.Ability)
		b = little.AppendUint32(b, uint32(len(p.Sockets)))
		b = append(b, p.Sockets...)
		b = little.AppendUint32(b, uint32(len(p.Colors)))
		b = append(b, p.Colors...)
	}
	if len(products) == 1 {
		b = little.AppendUint16(b, math.MaxUint16)
		b = append(b, make([]byte, 10)...)
	}
	return b
}

func ReadUniqueAvatar(b []byte) (storage.UniqueAvatarRequest, []byte, error) {
	var q storage.UniqueAvatarRequest
	bad := errors.New("invalid native unique avatar request")
	if len(b) != 45 {
		return q, nil, bad
	}
	b = b[13:]
	i := func(offset int) int32 { return int32(little.Uint32(b[offset:])) }
	q = storage.UniqueAvatarRequest{Mode: i(0), Material: i(4), First: i(8), Second: i(12), Target: i(16), Product: little.Uint32(b[20:]), Ability: i(24), Season: i(28)}
	slot := func(n int32) bool { return n >= -1 && n <= 479 }
	if q.Mode < 0 || q.Mode > 2 || !slot(q.Material) || !slot(q.First) || !slot(q.Second) || !slot(q.Target) ||
		q.Product == 0 || q.Product == math.MaxUint32 || q.Ability < 0 || q.Ability >= 255 || q.Season < 1 || q.Season > 100 {
		return q, nil, bad
	}
	b = append([]byte(nil), b...)
	if q.Mode == 0 {
		q.First, q.Second = -1, -1
		little.PutUint64(b[8:], math.MaxUint64)
	}
	if q.Mode == 2 {
		q.Target = -1
		little.PutUint32(b[16:], math.MaxUint32)
	}
	return q, b, nil
}

func UniqueAvatarAccepted(r storage.UniqueAvatarResult) []byte {
	b := []byte{1, 0}
	if r.Upgraded {
		b[1] = 1
	}
	b = little.AppendUint32(b, r.Product)
	return little.AppendUint32(b, r.Value)
}
