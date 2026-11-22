package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
)

type bodyWriter []byte

func (w *bodyWriter) u8(v byte)      { *w = append(*w, v) }
func (w *bodyWriter) u16(v uint16)   { *w = little.AppendUint16(*w, v) }
func (w *bodyWriter) u32(v uint32)   { *w = little.AppendUint32(*w, v) }
func (w *bodyWriter) u64(v uint64)   { *w = little.AppendUint64(*w, v) }
func (w *bodyWriter) zeros(n int)    { *w = append(*w, make([]byte, n)...) }
func (w *bodyWriter) raw(b []byte)   { *w = append(*w, b...) }
func (w *bodyWriter) block(b []byte) { w.u32(uint32(len(b))); w.raw(b) }

func Login(port uint16, channelType byte) ([]byte, error) {
	if port == 0 || (channelType != 1 && channelType != 8 && channelType != 13 && channelType != 63) {
		return nil, errors.New("invalid login endpoint or channel type")
	}
	w := bodyWriter{1, 1, 1, channelType, 1}
	w.u32(0xde509f65)
	w.block([]byte("127.0.0.1"))
	w.u32(uint32(port))
	w.zeros(6)
	w.raw([]byte{1, 1})
	w.zeros(20)
	return w, nil
}

// AccountWallet is N53's signed balance followed by the other two original
// balance categories, which the original Host initializes to zero.
func AccountWallet(cera int64) []byte {
	b := make([]byte, 13)
	b[0] = 1
	little.PutUint32(b[1:], uint32(min(max(cera, 0), int64(math.MaxInt32))))
	return b
}

const CodecMaterialLength = 334

func ChannelInformation(channelID byte, unixSeconds int64, material []byte) ([]byte, error) {
	if channelID == 0 || unixSeconds <= 0 || unixSeconds > math.MaxInt32 || len(material) != CodecMaterialLength {
		return nil, errors.New("invalid native channel information")
	}
	var proto []byte
	number := func(field, value uint64) {
		proto = binary.AppendUvarint(proto, field<<3)
		proto = binary.AppendUvarint(proto, value)
	}
	block := func(field uint64, b []byte) {
		proto = binary.AppendUvarint(proto, field<<3|2)
		proto = binary.AppendUvarint(proto, uint64(len(b)))
		proto = append(proto, b...)
	}
	number(2, 1)
	block(3, material)
	block(4, []byte("ch."+strconv.Itoa(int(channelID))))
	number(7, 0)
	number(8, uint64(channelID))
	number(9, 0)
	number(11, uint64(unixSeconds))
	number(30002, 0)
	number(30004, 1931)
	number(30005, 2120)
	var body bodyWriter
	body.block(proto)
	return body, nil
}

func CheckListRequest(body []byte) error {
	if !bytes.Equal(body, []byte{255, 255, 2}) {
		return errors.New("invalid local roster request")
	}
	return nil
}

func CheckEmptyRosterProbe(body []byte) error {
	if !bytes.Equal(body, []byte{32, 0}) {
		return errors.New("invalid empty roster probe")
	}
	return nil
}

func Ping(body []byte) ([]byte, error) {
	if len(body) != 17 {
		return nil, errors.New("ping must contain 13 opaque bytes and a tick")
	}
	return append([]byte{1}, body[13:]...), nil
}

func Ack() []byte              { return []byte{1} }
func Failure(code byte) []byte { return []byte{0, code} }
func ExitAccepted() []byte     { return []byte{1, 0, 0, 0, 0} }
func Level(level byte) []byte  { return little.AppendUint32(nil, uint32(level)) }

type CreateRequest struct {
	Job       byte
	Name      string
	Options   []byte
	Supported bool
	GrowType  byte
}

func ReadCreateRequest(body []byte) (CreateRequest, error) {
	if len(body) == 0 || body[0] > 15 {
		return CreateRequest{}, errors.New("invalid creation job")
	}
	name, options, err := readNameBlock(body[1:])
	if err != nil {
		return CreateRequest{}, err
	}
	if len(options) > 64 {
		return CreateRequest{}, errors.New("creation options exceed 64 bytes")
	}
	result := CreateRequest{Job: body[0], Name: name, Options: bytes.Clone(options)}
	switch {
	case len(options) == 0:
		result.Supported = true
	case len(options) == 12 && bytes.Equal(options[:8], []byte{0, 0, 0, 0, 0, 0, 255, 0}) && options[8] <= 5 && bytes.Equal(options[9:], []byte{0, 0, 0}):
		result.Supported = true
		result.GrowType = options[8]
	}
	return result, nil
}

func ReadSelectRequest(body []byte) (uint16, error) {
	if len(body) != 2 {
		return 0, errors.New("select request must contain a roster index")
	}
	return little.Uint16(body), nil
}

func CreateAccepted(id uint16, name string) ([]byte, error) {
	if id == 0 {
		return nil, errors.New("invalid created character ID")
	}
	encoded, err := encodeName(name)
	if err != nil {
		return nil, err
	}
	w := bodyWriter{1}
	w.u16(id)
	w.block(encoded)
	return w, nil
}

type Equipment struct {
	Slot                                    byte
	TemplateID, QualitySeed                 uint32
	Durability                              uint16
	Raw119                                  []byte
	Reinforcement, Forge, AmplificationType byte
	AmplificationValue                      uint16
	LinkedTemplateID                        uint32
	LinkedRaw, AvatarSockets, AvatarColors  []byte
	AvatarLifetimeSeconds                   int32
}

func (e Equipment) validate() error {
	if e.TemplateID == 0 || e.TemplateID == math.MaxUint32 || e.QualitySeed == 0 || e.QualitySeed >= 999999999 || e.Slot > 32 || e.Slot == 26 || e.Slot == 30 || e.Slot == 31 || len(e.Raw119) != 119 ||
		e.LinkedTemplateID == math.MaxUint32 || len(e.LinkedRaw) > 30 || len(e.AvatarSockets) > 30 || len(e.AvatarColors) > 4 ||
		(e.Slot > 11 && (len(e.AvatarSockets) != 0 || len(e.AvatarColors) != 0)) || ((e.LinkedTemplateID == 0 || e.Slot != 9) && len(e.LinkedRaw) != 0) ||
		e.Raw119[0x27] > 2 || e.Raw119[0x3c] > 2 || e.Raw119[0x47] > 3 {
		return errors.New("invalid equipment wire projection")
	}
	return nil
}

func (e Equipment) summary(w *bodyWriter) error {
	if err := e.validate(); err != nil {
		return err
	}
	w.u8(e.Slot)
	w.u32(e.TemplateID)
	w.block(e.AvatarColors)
	var reinforcement byte
	if e.Slot == 12 {
		reinforcement = e.Reinforcement << 1
	}
	w.u8(reinforcement)
	w.u32(e.LinkedTemplateID)
	if e.LinkedTemplateID > 0 && e.Slot == 9 {
		w.block(e.LinkedRaw)
	}
	w.zeros(5)
	return nil
}

type RosterCharacter struct {
	ID                             uint16
	Name                           string
	Job, GrowType, Level           byte
	RenamePending, PurchasedRename bool
	Worn                           []Equipment
}

func flag(value bool) byte {
	if value {
		return 1
	}
	return 0
}

func Roster(rows []RosterCharacter, capacity byte) ([]byte, error) {
	if capacity < 32 || capacity > 100 || len(rows) > int(capacity) {
		return nil, errors.New("invalid roster capacity")
	}
	w := bodyWriter{2, byte(len(rows)), 5}
	w.u16(uint16(capacity))
	w.u16(uint16(capacity))
	w.u16(uint16(len(rows)))
	w.u32(0)
	w.u16(uint16(len(rows)))
	seen := make(map[uint16]bool, len(rows))
	for index, row := range rows {
		if row.ID == 0 || seen[row.ID] || row.Job > 15 || row.Level == 0 || (row.PurchasedRename && !row.RenamePending) || len(row.Worn) > 32 {
			return nil, errors.New("invalid roster character")
		}
		seen[row.ID] = true
		name, err := encodeName(row.Name)
		if err != nil {
			return nil, err
		}
		w.u16(uint16(index))
		w.block(name)
		w.u8(flag(row.RenamePending))
		w.u8(flag(row.PurchasedRename))
		w.u8(row.Job)
		w.u8(row.GrowType)
		w.u8(row.Level)
		w.zeros(2)
		w.u8(byte(len(row.Worn)))
		worn := slices.Clone(row.Worn)
		sort.Slice(worn, func(i, j int) bool { return worn[i].Slot < worn[j].Slot })
		for i, e := range worn {
			if i > 0 && worn[i-1].Slot == e.Slot {
				return nil, errors.New("duplicate equipment slot")
			}
			if err := e.summary(&w); err != nil {
				return nil, err
			}
		}
		w.zeros(68)
	}
	w.u8(1)
	w.zeros(9)
	return w, nil
}

type SelectCharacter struct {
	ID                        uint16
	Created                   uint32
	FatigueUsed, FatigueLimit uint16
	TutorialDone              bool
}

func Select(c SelectCharacter, initialTown uint32, conqueror, skillMaster, crystal uint64, nativeOpening bool) ([]byte, error) {
	if c.ID == 0 || c.FatigueUsed > c.FatigueLimit || (nativeOpening && initialTown > 255) {
		return nil, errors.New("invalid selected character")
	}
	premiums := []struct {
		kind    byte
		seconds uint64
	}{{22, conqueror}, {27, skillMaster}, {97, crystal}}
	var count byte
	for _, p := range premiums {
		if p.seconds > 253402300799 {
			return nil, errors.New("invalid remaining premium duration")
		}
		if p.seconds > 0 {
			count++
		}
	}
	w := bodyWriter{1}
	w.u32(0)
	w.u32(c.Created)
	w.u16(c.ID)
	w.u16(c.FatigueUsed)
	w.u16(c.FatigueLimit)
	w.u16(0)
	w.u8(count)
	for _, p := range premiums {
		if p.seconds > 0 {
			w.u8(p.kind)
			w.u64(p.seconds)
		}
	}
	w.u32(0)
	for n := 0; n < 30; n++ {
		w.u16(0xffff)
		w.u32(0)
	}
	w.u32(0)
	if nativeOpening {
		w.u8(byte(initialTown))
		w.u8(0)
	} else {
		w.u32(initialTown)
		w.zeros(12)
		w.u8(38)
		w.u8(0)
	}
	w.u8(flag(c.TutorialDone))
	if c.TutorialDone {
		w.u8(31)
	}
	w.zeros(8)
	return w, nil
}

func ReadGameOptions(body []byte) ([]byte, error) {
	if len(body) != 376 || little.Uint32(body) != 372 {
		return nil, errors.New("invalid game options snapshot")
	}
	return bytes.Clone(body[4:]), nil
}

func RestoreGameOptions(data []byte) ([]byte, error) {
	if len(data) != 372 {
		return nil, errors.New("invalid saved game options")
	}
	var w bodyWriter
	w.block(data)
	w.zeros(8)
	return w, nil
}

func ReadHotkeys(body []byte) ([]byte, error) {
	if len(body) < 5 || len(body) > 1403 || little.Uint32(body) != uint32(len(body)-4) {
		return nil, errors.New("invalid hotkey snapshot")
	}
	return bytes.Clone(body[4:]), nil
}

func RestoreHotkeys(data []byte) ([]byte, error) {
	if len(data) < 1 || len(data) > 1399 {
		return nil, errors.New("invalid saved hotkeys")
	}
	w := bodyWriter{0}
	w.block(data)
	return w, nil
}
