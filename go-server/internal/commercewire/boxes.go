package commercewire

import (
	"math"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/storage"
)

func ReadLottery(b []byte) (slot uint16, double bool, err error) {
	if len(b) != 4 || le.Uint16(b) > 1 {
		return 0, false, storage.ErrInvalid
	}
	slot = le.Uint16(b[2:])
	if (slot < 3 || slot > 8) && (slot < 65 || slot > 120) {
		return 0, false, storage.ErrInvalid
	}
	return slot, le.Uint16(b) == 1, nil
}

func ReadKeyedBox(b []byte) (storage.KeyedBoxRequest, error) {
	var q storage.KeyedBoxRequest
	if len(b) != 15 || b[0] != 0 {
		return q, storage.ErrInvalid
	}
	q = storage.KeyedBoxRequest{SourceSlot: le.Uint16(b[1:]), SourceID: le.Uint32(b[3:]),
		KeySlot: le.Uint16(b[7:]), KeyID: le.Uint32(b[9:]), Count: le.Uint16(b[13:])}
	if q.SourceSlot == 0 || q.SourceSlot == math.MaxUint16 || q.KeySlot == 0 || q.KeySlot == math.MaxUint16 ||
		q.SourceSlot == q.KeySlot || q.SourceID == 0 || q.SourceID == math.MaxUint32 ||
		q.KeyID == 0 || q.KeyID == math.MaxUint32 || q.SourceID == q.KeyID || q.Count < 1 || q.Count > 100 {
		return q, storage.ErrInvalid
	}
	return q, nil
}

func LotteryOpened(slot uint16, a storage.Asset) ([]byte, error) {
	core, err := inventorywire.MailItem119(a)
	if err != nil {
		return nil, err
	}
	return append(append(le.AppendUint16([]byte{1}, slot), core...), make([]byte, 38)...), nil
}

func KeyedBoxOpened(q storage.KeyedBoxRequest, rewards []storage.Asset) ([]byte, error) {
	if len(rewards) == 0 || len(rewards) > 100 {
		return nil, storage.ErrInvalid
	}
	b := make([]byte, 3, 15+153*len(rewards))
	b[0] = 1
	b = le.AppendUint16(b, q.Count)
	b = le.AppendUint16(b, q.SourceSlot)
	b = le.AppendUint16(b, q.KeySlot)
	b = le.AppendUint16(b, uint16(len(rewards)))
	for _, a := range rewards {
		core, err := inventorywire.MailItem119(a)
		if err != nil {
			return nil, err
		}
		b = append(append(b, core...), make([]byte, 34)...)
	}
	return append(b, 0, 0, 0, 0), nil
}
