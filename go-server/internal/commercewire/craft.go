package commercewire

import (
	"bytes"
	"math"
	"unicode"
	"unicode/utf16"

	"golang.org/x/text/encoding/simplifiedchinese"
	"s9a1/server/internal/storage"
)

func ReadCraft(op uint16, b []byte) (storage.CraftRequest, error) {
	q := storage.CraftRequest{Opcode: op, SourceSlot: math.MaxUint16,
		Material: -1, Protection: -1, NPC: -1, NPCMaterial: -1, Selection: -1, BookAction: -1}
	switch {
	case op == 80 && len(b) >= 20:
		q.RawMethod, q.List = b[0], b[1]
		q.Method = storage.CraftTicket
		if q.RawMethod <= 1 {
			q.Method = storage.CraftMethod(q.RawMethod)
		}
		q.Slot, q.TemplateID = le.Uint16(b[2:]), le.Uint32(b[4:])
		q.Material, q.Protection = int16(le.Uint16(b[8:])), int16(le.Uint16(b[10:]))
		tail, err := craftName(b[12:])
		if err != nil || len(tail) != 4 {
			return q, storage.ErrInvalid
		}
		q.NPC, q.NPCMaterial = int16(le.Uint16(tail)), int16(le.Uint16(tail[2:]))
	case op == 430 && len(b) >= 14:
		q.Method, q.List, q.Slot, q.TemplateID, q.Material = storage.CraftForge, b[0], le.Uint16(b[1:]), le.Uint32(b[3:]), int16(le.Uint16(b[7:]))
		tail, err := craftName(b[9:])
		if err != nil || len(tail) != 1 {
			return q, storage.ErrInvalid
		}
		q.Mode = tail[0]
	case op == 1734 && len(b) >= 9:
		q.Method, q.List, q.Slot, q.TemplateID, q.Material = storage.CraftRemodel, b[0], le.Uint16(b[1:]), le.Uint32(b[3:]), int16(le.Uint16(b[7:]))
		if q.TemplateID == 0 || !zeroCraftTail(b[9:]) {
			return q, storage.ErrInvalid
		}
	case op == 272 && len(b) == 10:
		q.Method, q.SourceList, q.SourceSlot = storage.CraftEnchant, b[0], le.Uint16(b[1:])
		q.List, q.Slot, q.Selection = b[3], le.Uint16(b[4:]), int(int32(le.Uint32(b[6:])))
	case op == 205 && len(b) >= 14:
		q.Method, q.RawMethod, q.Slot, q.TemplateID, q.Material = storage.CraftTicket, b[0], le.Uint16(b[1:]), le.Uint32(b[3:]), int16(le.Uint16(b[7:]))
		q.BookAction, q.SourceTemplateID, q.Selection = int(b[0]), le.Uint32(b[9:]), int(b[13])
		tail := b[14:]
		if q.RawMethod == 2 {
			var err error
			tail, err = craftName(tail)
			if err != nil {
				return q, err
			}
		}
		if q.RawMethod > 2 || int16(q.Slot) < 0 || q.TemplateID == 0 || q.Material < 0 || !zeroCraftTail(tail) {
			return q, storage.ErrInvalid
		}
	default:
		return q, storage.ErrInvalid
	}
	return q, nil
}

func craftName(b []byte) ([]byte, error) {
	if len(b) < 4 {
		return nil, storage.ErrInvalid
	}
	n := int(le.Uint32(b))
	if n < 0 || n > 512 || n > len(b)-4 || bytes.IndexByte(b[4:4+n], 0) >= 0 {
		return nil, storage.ErrInvalid
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().String(string(b[4 : 4+n]))
	if err != nil {
		return nil, storage.ErrInvalid
	}
	var units int
	for _, r := range decoded {
		if r == unicode.ReplacementChar {
			return nil, storage.ErrInvalid
		}
		units += utf16.RuneLen(r)
	}
	if units > 256 {
		return nil, storage.ErrInvalid
	}
	return b[4+n:], nil
}

func zeroCraftTail(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func CraftAccepted(q storage.CraftRequest, r storage.CraftResult, rewards []storage.Asset) ([]byte, error) {
	switch q.Opcode {
	case 80, 1734:
		out := le.AppendUint16([]byte{1, q.RawMethod}, uint16(r.MaterialSlot))
		out = le.AppendUint32(out, r.MaterialRemaining)
		out = le.AppendUint16(out, math.MaxUint16)
		out = append(out, 0, r.PreviousLevel, r.ResultCode, r.NewLevel, q.List)
		out = le.AppendUint16(out, q.Slot)
		out = le.AppendUint16(out, math.MaxUint16)
		if r.ResultCode == 3 {
			if len(rewards) > math.MaxUint8 {
				return nil, storage.ErrInvalid
			}
			out = append(out, byte(len(rewards)))
			for _, row := range rewards {
				out = le.AppendUint16(out, row.Slot)
				out = le.AppendUint32(out, row.TemplateID)
				out = le.AppendUint32(out, row.Count)
			}
		}
		out = le.AppendUint16(out, math.MaxUint16)
		return append(out, 0, 0, 0, 0, 0, 0), nil
	case 205:
		if r.AmplificationType < 1 || r.AmplificationType > 4 {
			return nil, storage.ErrInvalid
		}
		out := le.AppendUint16([]byte{1, q.RawMethod}, uint16(r.MaterialSlot))
		out = le.AppendUint32(out, r.MaterialRemaining)
		out = le.AppendUint16(out, q.Slot)
		out = append(out, r.AmplificationType, 0, 0)
		if q.RawMethod == 2 {
			out = append(out, r.NewLevel)
		}
		return out, nil
	case 430:
		out := le.AppendUint16([]byte{1}, uint16(r.MaterialSlot))
		out = le.AppendUint32(out, r.MaterialRemaining)
		out = append(out, r.PreviousLevel, r.ResultCode, r.NewLevel, q.List)
		return le.AppendUint16(out, q.Slot), nil
	case 272:
		return le.AppendUint16([]byte{1, q.List}, q.Slot), nil
	default:
		return nil, storage.ErrInvalid
	}
}
