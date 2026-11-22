package host

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestSeasonalEventVisibility108(t *testing.T) {
	for _, end := range []uint32{0, 1800000000} {
		ids := []uint16{4171, 2554}
		if end != 0 {
			ids = []uint16{4171, 2520, 2554}
		}
		w := binary.LittleEndian.AppendUint16(nil, uint16(len(ids)))
		for _, id := range ids {
			w = binary.LittleEndian.AppendUint16(w, id)
			w = append(w, make([]byte, 13)...)
		}
		if end == 0 {
			w = append(w, 0)
		} else {
			w = append(w, 1)
			w = binary.LittleEndian.AppendUint16(w, 2520)
			w = binary.LittleEndian.AppendUint32(w, end)
			w = append(w, make([]byte, 44)...)
		}
		if !bytes.Equal(eventVisibility(end), w) {
			t.Fatalf("N108 season end=%d", end)
		}
	}
}
