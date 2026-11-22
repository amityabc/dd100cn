package questwire

import (
	"errors"
	"math"
	"s9a1/server/internal/storage"
	"slices"
)

func ReadEmblem(op uint16, body []byte) (storage.EmblemRequest, int, error) {
	r := storage.EmblemRequest{Opcode: op}
	bad := errors.New("invalid native emblem request")
	i, maxRows := 0, 2
	if op == 201 {
		if len(body) == 0 || body[0] != 1 && body[0] != 3 {
			return r, 0, bad
		}
		r.List, i, maxRows = body[0], 1, 5
	}
	if len(body) < i+7 {
		return r, 0, bad
	}
	r.Target, r.Reference = little.Uint16(body[i:]), little.Uint32(body[i+2:])
	i += 6
	if r.Target > 479 || r.Reference == 0 || r.Reference == math.MaxUint32 {
		return r, 0, bad
	}
	switch op {
	case 908:
		if len(body) < i+2 {
			return r, 0, bad
		}
		r.Material = little.Uint16(body[i:])
		i += 2
		if r.Material > 360 {
			return r, 0, bad
		}
	case 201, 907:
		n := int(body[i])
		i++
		if n < 1 || n > maxRows || len(body) < i+n*7 {
			return r, 0, bad
		}
		for range n {
			r.Rows = append(r.Rows, storage.EmblemRow{Slot: little.Uint16(body[i:]), Reference: little.Uint32(body[i+2:]), Socket: body[i+6]})
			i += 7
		}
	default:
		return r, 0, bad
	}
	if len(body)-i > 16 || slices.ContainsFunc(body[i:], func(b byte) bool { return b != 0 }) {
		return r, 0, bad
	}
	return r, i, nil
}

func (c *Catalog) Emblems() storage.EmblemCatalog { return c.growth.Emblems }

func EmblemAccepted(remaining []storage.EmblemRemaining) []byte {
	b := []byte{1, byte(len(remaining))}
	for _, row := range remaining {
		b = little.AppendUint16(b, row.Slot)
		b = little.AppendUint32(b, row.Count)
	}
	return b
}

func ReadEmblemCompound(body []byte) ([]storage.EmblemCompoundInput, error) {
	bad := errors.New("invalid native emblem compound request")
	if len(body) < 1 || body[0] < 2 || body[0] > 5 || len(body) != 1+int(body[0])*6 {
		return nil, bad
	}
	rows := make([]storage.EmblemCompoundInput, 0, body[0])
	for i := 1; i < len(body); i += 6 {
		r := storage.EmblemCompoundInput{Slot: little.Uint16(body[i+4:]), Template: little.Uint32(body[i:])}
		if r.Template == 0 || r.Slot < 289 || r.Slot > 360 {
			return nil, bad
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func EmblemCompoundAccepted(rewards []storage.GiftReward) []byte {
	b := []byte{1, byte(len(rewards))}
	for _, r := range rewards {
		b = little.AppendUint32(b, r.TemplateID)
		b = little.AppendUint32(b, r.Count)
	}
	return b
}
