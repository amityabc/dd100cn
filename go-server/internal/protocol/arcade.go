package protocol

import "errors"

type ArcadeCreate struct {
	Kind, Difficulty byte
	Supported        bool
}

func ReadArcadeCreate(body []byte) (ArcadeCreate, error) {
	if len(body) < 10 || len(body) > 138 || little.Uint32(body[1:5]) != uint32(len(body)-10) {
		return ArcadeCreate{}, errors.New("invalid arcade room title boundary")
	}
	tail := body[len(body)-5:]
	kind, difficulty := tail[3], tail[4]
	supported := body[0] == 0 && little.Uint16(tail) == 0 && tail[2] == 0 &&
		(kind == 1 && difficulty == 0 || kind == 3 && difficulty >= 1 && difficulty <= 3)
	if kind == 3 && difficulty > 0 {
		difficulty--
	}
	return ArcadeCreate{kind, difficulty, supported}, nil
}

type ArcadeRound struct {
	Phase        byte
	Opponent     uint32
	Total, Round byte
	Elapsed      uint32
	Opaque       byte
	Won          bool
	Attack       uint16
	Metric       uint32
}

func ReadArcadeRound(body []byte) (ArcadeRound, error) {
	if len(body) != 19 || body[0] > 1 || body[12] > 1 {
		return ArcadeRound{}, errors.New("invalid arcade round report")
	}
	return ArcadeRound{
		Phase: body[0], Opponent: little.Uint32(body[1:]), Total: body[5], Round: body[6],
		Elapsed: little.Uint32(body[7:]), Opaque: body[11], Won: body[12] == 1,
		Attack: little.Uint16(body[13:]), Metric: little.Uint32(body[15:]),
	}, nil
}

const (
	ArcadeMap    uint16 = 50036
	ArcadeMode   byte   = 10
	PracticeMode byte   = 6
)

func ArcadeRoom(id, character uint16, mode byte) ([]byte, error) {
	if id == 0 || character == 0 || character == 65535 || mode != ArcadeMode && mode != PracticeMode {
		return nil, errors.New("invalid arcade room identity")
	}
	var w bodyWriter
	w.u16(1)
	w.u16(id)
	w.u8(0)
	w.u32(0)
	w.u8(1)
	w.u8(0)
	w.u16(ArcadeMap)
	w.u8(mode)
	w.u8(1)
	w.u16(character)
	for seat := 1; seat < 8; seat++ {
		w.u8(255)
		w.u16(65535)
	}
	w.u8(0)
	w.u32(0)
	return w, nil
}

func ArcadeRoomState(room uint16, mode, state byte) []byte {
	var w bodyWriter
	w.u16(room)
	w.u8(state)
	w.u8(0)
	w.u16(ArcadeMap)
	w.u8(mode)
	w.u32(0)
	return w
}

func ArcadeLeave(room uint16, mode byte) []byte {
	var w bodyWriter
	w.u16(room)
	w.u8(mode)
	w.u8(1)
	w.u8(0)
	w.u8(255)
	w.u16(65535)
	return w
}
