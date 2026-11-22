package commercewire

import "s9a1/server/internal/storage"

func ReadQuality(op uint16, b []byte) (storage.QualityRequest, error) {
	q := storage.QualityRequest{Opcode: op}
	switch {
	case op == 81 && len(b) == 10:
		q.TargetSlot, q.Target = le.Uint16(b), le.Uint32(b[2:])
		q.SourceSlot, q.Mode = le.Uint16(b[6:]), le.Uint16(b[8:])
	case op == 1733 && len(b) == 4:
		q.TargetSlot, q.SourceSlot = le.Uint16(b), le.Uint16(b[2:])
	case op == 1826 && len(b) == 5:
		q.TargetSlot, q.SourceSlot, q.Mode = le.Uint16(b), le.Uint16(b[2:]), uint16(b[4])
	default:
		return q, storage.ErrInvalid
	}
	return q, nil
}

func QualityAccepted(op, slot uint16, count uint32) []byte {
	if op != 81 {
		return []byte{1}
	}
	return le.AppendUint16(le.AppendUint32(le.AppendUint16([]byte{1}, slot), count), 2)
}
