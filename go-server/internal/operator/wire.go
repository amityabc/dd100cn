// Package operator preserves the original current-user GM pipe and CLI contracts.
package operator

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/google/uuid"
)

var ErrNotConnected = errors.New("未连接到服务端；请求尚未发送。")

func PipeName(root string) (string, error) {
	path, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	n := sha256.Sum256([]byte(strings.ToUpper(strings.TrimRight(path, "/\\"))))
	return "Serve100.CeraOps." + strings.ToUpper(hex.EncodeToString(n[:12])), nil
}

func ReadFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint32(header[:])
	if length == 0 || length > 4096 {
		return nil, errors.New("Operator IPC frame exceeds its bounded size")
	}
	b := make([]byte, length)
	_, err := io.ReadFull(r, b)
	return b, err
}

func WriteFrame(w io.Writer, b []byte) error {
	if len(b) == 0 || len(b) > 4096 {
		return errors.New("Operator IPC result exceeds its bounded size")
	}
	frame := binary.LittleEndian.AppendUint32(nil, uint32(len(b)))
	frame = append(frame, b...)
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

type Cera struct {
	Account     string
	Character   uint16
	Amount      int64
	OperationId uuid.UUID
	Reason      string
}
type Wallet struct {
	Account                string
	Character              uint16
	CeraAmount, GoldAmount int64
	OperationId            uuid.UUID
	Reason                 string
}
type Mail struct {
	Account           string
	Character         uint16
	TemplateId, Count uint32
	OperationId       uuid.UUID
	Reason            string
}
type Wisdom struct {
	Account     string
	Character   uint16
	StoneCount  uint32
	OperationId uuid.UUID
	Reason      string
}

type Intent struct {
	Account     string
	Character   uint16
	OperationID uuid.UUID
	Reason      string
	Command     any
}

func Parse(b []byte) (Intent, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		return Intent{}, err
	}
	var cmd any
	var extra []string
	switch {
	case keys["StoneCount"] != nil:
		cmd = &Wisdom{}
		extra = []string{"StoneCount"}
	case keys["CeraAmount"] != nil:
		cmd = &Wallet{}
		extra = []string{"CeraAmount", "GoldAmount"}
	case keys["TemplateId"] != nil:
		cmd = &Mail{}
		extra = []string{"TemplateId", "Count"}
	default:
		cmd = &Cera{}
		extra = []string{"Amount"}
	}
	allowed := map[string]bool{"Account": true, "Character": true, "OperationId": true, "Reason": true}
	for _, k := range extra {
		allowed[k] = true
	}
	for k := range keys {
		if !allowed[k] {
			return Intent{}, errors.New("Unknown operator request field: " + k)
		}
	}
	if keys == nil {
		return Intent{}, errors.New("Operator request is absent")
	}
	if err := json.Unmarshal(b, cmd); err != nil {
		return Intent{}, err
	}
	switch c := cmd.(type) {
	case *Wallet:
		return Intent{c.Account, c.Character, c.OperationId, c.Reason, *c}, nil
	case *Mail:
		return Intent{c.Account, c.Character, c.OperationId, c.Reason, *c}, nil
	case *Wisdom:
		return Intent{c.Account, c.Character, c.OperationId, c.Reason, *c}, nil
	case *Cera:
		return Intent{c.Account, c.Character, c.OperationId, c.Reason, *c}, nil
	}
	return Intent{}, errors.New("Unsupported operator command")
}

func ValidReason(s string) bool {
	return strings.TrimSpace(s) != "" && len(utf16.Encode([]rune(s))) <= 256 && strings.IndexFunc(s, unicode.IsControl) < 0
}

// Guid.TryWriteBytes uses Windows little-endian fields for the character-bound wallet opcode.
func Targets(id uuid.UUID, character uint16) bool {
	return character != 0 && id[0] == 0x31 && id[1] == 0x57 && binary.BigEndian.Uint16(id[2:4]) == character
}

type CeraAudit struct {
	Before, After, Amount                     int64
	Owner                                     uuid.UUID
	Epoch, CharacterRevision, AccountRevision int64
	ExecutorBuild                             uuid.UUID
}
type WalletAudit struct {
	CeraBefore, CeraAfter, GoldBefore, GoldAfter int64
	Owner                                        uuid.UUID
	Epoch, CharacterRevision, AccountRevision    int64
	ExecutorBuild                                uuid.UUID
}
type MailAudit struct {
	MailId            int64
	TemplateId, Count uint32
	ResourceHash      string
}
type Reply struct {
	Status                             string
	OperationId                        uuid.UUID
	Reason                             *string
	CurrentCera                        int64
	Grant                              *CeraAudit
	CharacterRevision, AccountRevision int64
	ReadBack                           bool
	CurrentGold                        int64
	WalletGrant                        *WalletAudit
	WisdomSupply                       json.RawMessage
	CurrentFatigue                     uint16
	MailGrant                          *MailAudit
}

func Message(status string, id uuid.UUID, reason string) Reply {
	return Reply{Status: status, OperationId: id, Reason: &reason}
}

func Exchange(rw io.ReadWriter, body []byte) (Reply, error) {
	if err := WriteFrame(rw, body); err != nil {
		return Reply{}, err
	}
	b, err := ReadFrame(rw)
	if err != nil {
		return Reply{}, err
	}
	var out Reply
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	err = d.Decode(&out)
	return out, err
}
