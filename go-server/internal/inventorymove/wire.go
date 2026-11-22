// Package inventorymove owns the original ordinary equipment move body and
// ACK grammar. Frame encoding and actor/inventory notices belong to the host.
package inventorymove

import (
	"encoding/binary"
	"errors"

	"s9a1/server/internal/storage"
)

func Parse(sequence uint16, body []byte) (storage.MoveRequest, error) {
	if len(body) != 28 {
		return storage.MoveRequest{}, errors.New("C19 equipment move requires 28 bytes")
	}
	u := binary.LittleEndian
	return storage.MoveRequest{
		Sequence: sequence, SourceList: body[0], SourceSlot: u.Uint16(body[1:]), SourceTemplate: u.Uint32(body[3:]), Count: u.Uint32(body[7:]),
		DestinationList: body[11], DestinationSlot: u.Uint16(body[12:]), DestinationTemplate: u.Uint32(body[14:]), DestinationStack: u.Uint32(body[18:]),
		ActorIndex: u.Uint32(body[22:]), State0: body[26], State1: body[27],
	}, nil
}

func Accepted(r storage.MoveOutcome) []byte {
	b := make([]byte, 11)
	b[0], b[1], b[8] = 1, r.SourceList, r.DestinationList
	binary.LittleEndian.PutUint16(b[2:], r.SourceSlot)
	binary.LittleEndian.PutUint32(b[4:], r.Count)
	binary.LittleEndian.PutUint16(b[9:], r.DestinationSlot)
	return b
}

func Rejected(r storage.MoveRequest) []byte {
	return []byte{0, 4, 0, r.SourceList, r.DestinationList, 0, 0, 0, 0}
}
