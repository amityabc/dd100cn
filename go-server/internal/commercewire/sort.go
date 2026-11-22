package commercewire

import (
	"bytes"
	"encoding/binary"
	"math"

	"s9a1/server/internal/storage"
)

func ReadSort(body []byte) (storage.SortRequest, error) {
	var q storage.SortRequest
	if len(body) < 10 || len(body) > 22 || int(le.Uint32(body)) != len(body)-4 {
		return q, storage.ErrInvalid
	}
	r := bytes.NewReader(body[4:])
	for i, p := range []*uint32{&q.List, &q.Category, &q.Order} {
		b, err := r.ReadByte()
		if err != nil || b != byte(0x10+i*8) {
			return q, storage.ErrInvalid
		}
		before := r.Len()
		v, err := binary.ReadUvarint(r)
		if err != nil || v > math.MaxUint32 || before-r.Len() > 5 {
			return q, storage.ErrInvalid
		}
		*p = uint32(v)
	}
	if r.Len() != 0 || q.List != 0 && q.List != 1 && q.List != 2 && q.List != 7 && q.List != 46 {
		return q, storage.ErrInvalid
	}
	return q, nil
}

func Sorted(list uint32, success bool) []byte {
	// Every accepted native list ID fits the one-byte protobuf varint.
	b := []byte{1}
	if !success {
		b = []byte{0, 0, 0}
	}
	return append(le.AppendUint32(b, 4), 8, byte(list), 16, 0)
}
