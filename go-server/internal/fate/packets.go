package fate

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
)

var le = binary.LittleEndian
var ErrPacket = errors.New("invalid native activity report")

type Control struct{ Action, ID, Slot, Price, Remaining int32 }

func ReadControl(b []byte) (Control, error) {
	var q Control
	if len(b) < 8 || le.Uint32(b) != Event {
		return q, ErrPacket
	}
	q.Action = int32(le.Uint32(b[4:]))
	switch q.Action {
	case 7:
		if len(b) == 8 {
			return q, nil
		}
	case 8:
		if (len(b) == 24 || len(b) == 32) && zeros(b[24:]) {
			q.Slot, q.ID, q.Price, q.Remaining = int32(le.Uint32(b[8:])), int32(le.Uint32(b[12:])), int32(le.Uint32(b[16:])), int32(le.Uint32(b[20:]))
			if q.Slot >= 0 && q.Slot <= 2 && q.ID >= 0 && q.Price >= 0 && q.Remaining >= 0 && int64(q.Price)+int64(q.Remaining) <= math.MaxInt32 {
				return q, nil
			}
		}
	case 9:
		if len(b) == 12 {
			q.ID = int32(le.Uint32(b[8:]))
			return q, nil
		}
	default:
		switch len(b) {
		case 8:
			if q.Action == 3 || q.Action == 6 {
				return q, nil
			}
		case 12:
			if q.Action != 3 && q.Action != 6 {
				q.ID = int32(le.Uint32(b[8:]))
				return q, nil
			}
		case 16:
			if zeros(b[12:]) {
				q.ID = int32(le.Uint32(b[8:]))
				return q, nil
			}
		}
	}
	// Story and faction selectors also occur in the 16-byte rank envelope.
	if len(b) == 16 && q.Action != 9 && q.Action != 8 && zeros(b[12:]) {
		q.ID = int32(le.Uint32(b[8:]))
		return q, nil
	}
	if len(b) == 12 && q.Action == 7 {
		q.ID = int32(le.Uint32(b[8:]))
		return q, nil
	}
	return q, ErrPacket
}

func zeros(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
func ReadEquip(b []byte) (effect, slot int32, err error) {
	if len(b) != 8 {
		return 0, 0, ErrPacket
	}
	effect, slot = int32(le.Uint32(b)), int32(le.Uint32(b[4:]))
	if effect < 0 || slot < 0 || slot > 2 {
		return 0, 0, ErrPacket
	}
	return
}
func ReadResult(b []byte) error {
	if len(b) != 8 && len(b) != 16 || !zeros(b[4:]) {
		return ErrPacket
	}
	return nil
}
func ReadAbility(b []byte) error {
	if len(b) != 12 || !zeros(b[:4]) {
		return ErrPacket
	}
	return nil
}
func ReadConsumption(b []byte) error {
	if len(b) == 9 {
		if bytes.Equal(b[:8], []byte{4, 0, 0, 0, 16, 0, 32, 0}) && b[8] <= 1 {
			return nil
		}
		return ErrPacket
	}
	if len(b) != 16 {
		return ErrPacket
	}
	n := le.Uint32(b)
	if n > 4 || b[4+n] > 1 || !zeros(b[5+n:]) {
		return ErrPacket
	}
	input := b[4 : 4+n]
	var space, context bool
	for len(input) > 0 {
		tag, used := binary.Uvarint(input)
		if used <= 0 {
			return ErrPacket
		}
		input = input[used:]
		switch {
		case tag == 16 && !space:
			space = true
		case tag == 32 && !context:
			context = true
		default:
			return ErrPacket
		}
		v, used := binary.Uvarint(input)
		if used <= 0 || v != 0 {
			return ErrPacket
		}
		input = input[used:]
	}
	return nil
}
func ControlAck(ok, shop bool) []byte {
	var b []byte
	if ok {
		b = []byte{1}
	} else {
		b = []byte{0, 0, 0}
		if shop {
			b[1] = 10
		}
	}
	b = le.AppendUint32(b, Event)
	return append(b, make([]byte, 40)...)
}
func ResultPacket(phases, points uint32, rewards []struct{ Template, Count uint32 }) []byte {
	b := le.AppendUint32([]byte{1}, phases)
	b = le.AppendUint32(b, points)
	for n := 0; n < 5; n++ {
		id, count := uint32(math.MaxUint32), uint32(0)
		if n < len(rewards) {
			id, count = rewards[n].Template, rewards[n].Count
		}
		b = le.AppendUint32(b, id)
		b = le.AppendUint32(b, count)
	}
	return append(b, make([]byte, 8)...)
}
