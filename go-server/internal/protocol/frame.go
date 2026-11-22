// Package protocol implements the existing S9A1 wire formats.
package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

type Transport uint8
type Direction uint8

const (
	Channel Transport = iota
	Game
)

const (
	ClientToServer Direction = iota
	ServerToClient
)

const (
	MaximumFrameBytes          = 1 << 20
	ChannelMaximumRequestBytes = 4096
)

type Frame struct {
	Transport  Transport
	Direction  Direction
	Class      byte
	Opcode     uint16
	Sequence   uint16
	Compressed bool
	Body       []byte // Original on-wire body. Expansion is explicit.
	Raw        []byte
}

var (
	little  = binary.LittleEndian
	gameCRC = crc32.MakeTable(0x4DB89129)
)

func Checksum(data []byte) uint32 { return crc32.Checksum(data, gameCRC) }

func frameLimits(t Transport, d Direction) (header, limit int, err error) {
	if d != ClientToServer && d != ServerToClient {
		return 0, 0, errors.New("unknown packet direction")
	}
	switch t {
	case Channel:
		if d == ClientToServer {
			return 11, ChannelMaximumRequestBytes, nil
		}
		return 11, MaximumFrameBytes, nil
	case Game:
		if d == ClientToServer {
			return 13, MaximumFrameBytes, nil
		}
		return 16, MaximumFrameBytes, nil
	default:
		return 0, 0, errors.New("unknown packet transport")
	}
}

func declaredLength(header []byte, t Transport, minimum, maximum int) (int, error) {
	offset := 3
	if t == Channel {
		offset = 2
	}
	n := little.Uint32(header[offset:])
	if n < uint32(minimum) || n > uint32(maximum) {
		return 0, fmt.Errorf("declared frame length %d outside %d..%d", n, minimum, maximum)
	}
	return int(n), nil
}

// ReadFrame consumes exactly one frame, irrespective of TCP chunk boundaries.
func ReadFrame(r io.Reader, t Transport, d Direction) (Frame, error) {
	size, maximum, err := frameLimits(t, d)
	if err != nil {
		return Frame{}, err
	}
	var header [16]byte
	if _, err := io.ReadFull(r, header[:size]); err != nil {
		return Frame{}, err
	}
	n, err := declaredLength(header[:size], t, size, maximum)
	if err != nil {
		return Frame{}, err
	}
	raw := make([]byte, n)
	copy(raw, header[:size])
	if _, err := io.ReadFull(r, raw[size:]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	return decodeOwned(raw, t, d, size)
}

func DecodeFrame(raw []byte, t Transport, d Direction) (Frame, error) {
	size, maximum, err := frameLimits(t, d)
	if err != nil {
		return Frame{}, err
	}
	if len(raw) < size {
		return Frame{}, io.ErrUnexpectedEOF
	}
	n, err := declaredLength(raw, t, size, maximum)
	if err != nil {
		return Frame{}, err
	}
	if len(raw) != n {
		return Frame{}, errors.New("frame length differs from header")
	}
	return decodeOwned(bytes.Clone(raw), t, d, size)
}

func decodeOwned(raw []byte, t Transport, d Direction, size int) (Frame, error) {
	frame := Frame{Transport: t, Direction: d, Class: raw[0], Raw: raw}
	if t == Channel {
		var prefix byte
		if d == ServerToClient {
			prefix = 0x7c
		}
		if raw[0] != prefix || little.Uint32(raw[6:]) != 0 || raw[10] != 1 {
			return Frame{}, errors.New("invalid channel header")
		}
		frame.Opcode = uint16(raw[1])
	} else {
		frame.Opcode = little.Uint16(raw[1:])
		frame.Sequence = little.Uint16(raw[11:])
		frame.Compressed = d == ServerToClient && raw[15] == 1
		if frame.Compressed {
			if frame.Class != 0 || little.Uint32(raw[7:]) != 1 || little.Uint32(raw[11:]) != 0 {
				return Frame{}, errors.New("invalid marker16 header")
			}
		} else if Checksum(raw[11:]) != little.Uint32(raw[7:]) {
			return Frame{}, errors.New("game frame checksum mismatch")
		}
	}
	frame.Body = bytes.Clone(raw[size:])
	return frame, nil
}

// EncodeGame accepts the wire body; a marker16 body is already compressed.
func EncodeGame(d Direction, class byte, opcode, sequence uint16, body []byte, marker bool) ([]byte, error) {
	size, maximum, err := frameLimits(Game, d)
	if err != nil {
		return nil, err
	}
	if len(body) > maximum-size {
		return nil, errors.New("outgoing game frame too large")
	}
	if marker && (d != ServerToClient || class != 0) {
		return nil, errors.New("marker16 requires S2C class 0")
	}
	raw := make([]byte, size+len(body))
	raw[0] = class
	little.PutUint16(raw[1:], opcode)
	little.PutUint32(raw[3:], uint32(len(raw)))
	copy(raw[size:], body)
	if marker {
		little.PutUint32(raw[7:], 1)
		raw[15] = 1
	} else {
		little.PutUint16(raw[11:], sequence)
		little.PutUint32(raw[7:], Checksum(raw[11:]))
	}
	return raw, nil
}

func EncodeChannel(d Direction, opcode byte, body []byte) ([]byte, error) {
	size, maximum, err := frameLimits(Channel, d)
	if err != nil {
		return nil, err
	}
	if len(body) > maximum-size {
		return nil, errors.New("outgoing channel frame too large")
	}
	raw := make([]byte, size+len(body))
	if d == ServerToClient {
		raw[0] = 0x7c
	}
	raw[1], raw[10] = opcode, 1
	little.PutUint32(raw[2:], uint32(len(raw)))
	copy(raw[size:], body)
	return raw, nil
}
