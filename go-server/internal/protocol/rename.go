package protocol

import (
	"bytes"
	"errors"
)

type RenameRequest struct {
	Index            byte
	OldName, NewName string
	Reason           byte
}

func ReadRename(body []byte) (RenameRequest, error) {
	var r RenameRequest
	if len(body) == 0 {
		return r, errors.New("missing rename index")
	}
	r.Index, body = body[0], body[1:]
	read := func(limit uint32) (string, error) {
		if len(body) < 4 {
			return "", errors.New("truncated rename length")
		}
		n := little.Uint32(body)
		body = body[4:]
		if n == 0 || n > limit || int(n) > len(body) || bytes.IndexByte(body[:n], 0) >= 0 {
			return "", errors.New("invalid rename name block")
		}
		name, err := decodeName(body[:n])
		body = body[n:]
		return name, err
	}
	var err error
	if r.OldName, err = read(30); err != nil {
		return r, err
	}
	if r.NewName, err = read(12); err != nil {
		return r, err
	}
	if len(body) != 1 || body[0] != 1 && body[0] != 2 {
		return r, errors.New("invalid rename reason")
	}
	r.Reason = body[0]
	return r, nil
}
