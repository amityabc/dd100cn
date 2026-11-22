package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
)

// TownChange is the native C36 body. The final six bytes of ordinary requests
// are opaque except for the two canonical menu encodings.
type TownChange struct {
	Town, PreviousTown, TargetNPC       uint32
	Area, PreviousArea, Direction, Kind byte
	X, Y                                uint16
	Tail                                [6]byte
	NativeNPC, ReturnSentinel           bool
}

func ReadTownChange(b []byte, fate bool, camp, campNPC uint32) (TownChange, error) {
	var r TownChange
	if len(b) != 21 && len(b) != 24 {
		return r, errors.New("invalid town change width")
	}
	u := binary.LittleEndian
	r.Town, r.Area, r.X, r.Y, r.Direction = u.Uint32(b), b[4], u.Uint16(b[5:]), u.Uint16(b[7:]), b[9]
	r.PreviousTown, r.PreviousArea = u.Uint32(b[10:]), b[14]
	copy(r.Tail[:], b[15:21])
	npc := u.Uint32(b[16:])
	r.NativeNPC = fate && (r.Town == camp || r.PreviousTown == camp || npc == campNPC)
	if r.NativeNPC {
		if b[15] != 0 || b[20] != 0 || len(b) == 24 && !bytes.Equal(b[21:], []byte{0, 0, 0}) {
			return r, errors.New("invalid native NPC travel fields")
		}
		r.TargetNPC = npc
		r.ReturnSentinel = r.Town == math.MaxUint32 && r.Area == 255 && r.X == 0 && r.Y == 0 && r.Direction == 5 && npc == 0
		return r, nil
	}
	if len(b) != 21 || r.Town > math.MaxInt32 || r.PreviousTown > math.MaxInt32 {
		return r, errors.New("invalid ordinary town change")
	}
	if b[15] == 0 {
		if b[20] == 0 {
			r.TargetNPC = npc
		}
		if npc == 0 && (b[20] == 5 || b[20] == 6) {
			r.Kind = b[20]
		}
	}
	return r, nil
}

func TownChangeRejected(town uint32, area byte) []byte {
	return append(binary.LittleEndian.AppendUint32([]byte{0, 0, 0}, town), area)
}
