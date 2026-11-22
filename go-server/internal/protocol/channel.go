package protocol

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

// Expand rejects truncated members, invalid Adler-32 values, trailing bytes
// and oversized output, matching the original wire decoder.
func Expand(compressed []byte, maximum int) ([]byte, error) {
	if maximum < 1 || maximum > MaximumFrameBytes || len(compressed) < 6 || len(compressed) > MaximumFrameBytes {
		return nil, errors.New("invalid zlib buffer size")
	}
	if compressed[1]&32 != 0 {
		return nil, errors.New("preset zlib dictionaries are unsupported")
	}
	// bytes.Reader also implements io.ByteReader, so flate will not read
	// past the end of this member into the next packet.
	in := bytes.NewReader(compressed)
	zr, err := zlib.NewReader(in)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(out) > maximum || in.Len() != 0 {
		return nil, errors.New("oversized or trailing zlib data")
	}
	return out, nil
}

func channelKey(key string) ([aes.BlockSize]byte, error) {
	var raw [aes.BlockSize]byte
	if len(key) > len(raw) {
		return raw, errors.New("channel key exceeds 16 bytes")
	}
	for i := 0; i < len(key); i++ {
		if key[i] == 0 || key[i] > 127 {
			return raw, errors.New("channel key must be ASCII")
		}
		raw[i] = key[i]
	}
	return raw, nil
}

func ProtectChannel(plain []byte, key string) ([]byte, error) {
	if len(plain) > MaximumFrameBytes-aes.BlockSize {
		return nil, errors.New("channel plaintext too large")
	}
	rawKey, err := channelKey(key)
	if err != nil {
		return nil, err
	}
	cipher, err := aes.NewCipher(rawKey[:])
	if err != nil {
		return nil, err
	}
	encrypted := make([]byte, (len(plain)+aes.BlockSize-1)/aes.BlockSize*aes.BlockSize)
	copy(encrypted, plain)
	for i := 0; i < len(encrypted); i += aes.BlockSize {
		cipher.Encrypt(encrypted[i:i+aes.BlockSize], encrypted[i:i+aes.BlockSize])
	}
	return compressWire(encrypted)
}

func UnprotectChannel(body []byte, key string) ([]byte, error) {
	plain, err := Expand(body, MaximumFrameBytes)
	if err != nil {
		return nil, err
	}
	if len(plain) == 0 || len(plain)%aes.BlockSize != 0 {
		return nil, errors.New("channel ciphertext is not a whole AES block")
	}
	rawKey, err := channelKey(key)
	if err != nil {
		return nil, err
	}
	cipher, err := aes.NewCipher(rawKey[:])
	if err != nil {
		return nil, err
	}
	for i := 0; i < len(plain); i += aes.BlockSize {
		cipher.Decrypt(plain[i:i+aes.BlockSize], plain[i:i+aes.BlockSize])
	}
	return plain, nil // Padding belongs to the consuming message format.
}

type ChannelEntry struct {
	ID, Port       uint16
	Capacity, Load uint32
}

func Directory(entries []ChannelEntry) ([]byte, error) {
	if len(entries) < 1 || len(entries) > 32 {
		return nil, errors.New("invalid local channel count")
	}
	out := make([]byte, 12+48*len(entries))
	little.PutUint32(out, 1)
	little.PutUint32(out[8:], uint32(len(entries)))
	seen := make(map[uint16]bool, len(entries))
	for i, entry := range entries {
		if entry.ID == 0 || seen[entry.ID] || entry.Port == 0 || entry.Load > entry.Capacity {
			return nil, fmt.Errorf("invalid directory entry for channel %d", entry.ID)
		}
		seen[entry.ID] = true
		row := out[12+i*48:]
		copy(row[:20], "#ch."+strconv.Itoa(int(entry.ID)))
		little.PutUint32(row[20:], entry.Capacity)
		little.PutUint32(row[24:], entry.Load)
		copy(row[28:44], "127.0.0.1")
		little.PutUint32(row[44:], uint32(entry.Port))
	}
	return out, nil
}

// ChannelSession is owned by one channel TCP connection.
type ChannelSession struct {
	stage byte
	key   string
}

func (s *ChannelSession) Reply(request Frame, now time.Time, publishedScript []byte, directory []ChannelEntry) ([]byte, error) {
	if request.Transport != Channel || request.Direction != ClientToServer || request.Class != 0 {
		return nil, errors.New("expected client channel request")
	}
	var body []byte
	var err error
	next, key := s.stage, s.key
	switch request.Opcode {
	case 11:
		if len(request.Body) != 32 || s.stage != 0 {
			return nil, errors.New("invalid channel hello")
		}
		key = now.UTC().Format("20060102") + "000006"
		body = make([]byte, 36)
		copy(body[4:], key)
		next = 1
	case 9:
		if len(request.Body) != 0 || s.stage != 1 {
			return nil, errors.New("invalid channel script request")
		}
		body, err = ProtectChannel(publishedScript, key)
		next = 2
	case 17:
		if len(request.Body) != 24 || (s.stage != 1 && s.stage != 2) {
			return nil, errors.New("invalid channel directory request")
		}
		body, err = Directory(directory)
		if err == nil {
			body, err = ProtectChannel(body, key)
		}
		// The legacy launcher may request the directory before the script.
		if s.stage == 2 {
			next = 3
		}
	default:
		return nil, fmt.Errorf("unknown channel opcode %d", request.Opcode)
	}
	if err != nil {
		return nil, err
	}
	raw, err := EncodeChannel(ServerToClient, byte(request.Opcode+1), body)
	if err == nil {
		s.stage, s.key = next, key
	}
	return raw, err
}
