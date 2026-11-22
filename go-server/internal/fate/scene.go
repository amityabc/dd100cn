package fate

import (
	"encoding/json"
	"math"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
)

func TemplateID(raw json.RawMessage) (uint32, error) {
	var t struct{ TemplateID uint32 }
	if err := json.Unmarshal(raw, &t); err != nil {
		return 0, err
	}
	if t.TemplateID == 0 || t.TemplateID > math.MaxInt32 {
		return 0, ErrInvalid
	}
	return t.TemplateID, nil
}
func (d *Definition) EntryPackets(a protocol.Actor, seed uint32, first uint16) ([]opening.Packet, error) {
	v := d.LoadingDungeon
	r := v.Start
	if v.ID != d.DungeonId || r.MapID == 0 || len(r.Monsters) > 255 || len(r.Monsters) > 0 && (first < 402 || int(first)+len(r.Monsters) > math.MaxUint16) {
		return nil, ErrInvalid
	}
	info := make([]byte, 36)
	le.PutUint32(info, v.ID)
	info[7], info[8], info[9], info[18] = v.MazeIndex, v.BossX, v.BossY, 11
	le.PutUint32(info[20:], math.MaxUint32)
	state := append(le.AppendUint16([]byte{1}, a.ID), 1)
	p := []opening.Packet{{Class: 0, Opcode: 27, Body: make([]byte, 47)}, {Class: 1, Opcode: 16, Body: []byte{1}}, {Class: 0, Opcode: 3, Body: state}, {Class: 0, Opcode: 28, Body: info}, {Class: 0, Opcode: 1774, Body: []byte{0, 0}}}
	if d.InstanceBuff > 0 && a.Level >= d.BuffMinimumLevel && a.Level <= d.BuffMaximumLevel {
		add := append(le.AppendUint32([]byte{1}, uint32(d.InstanceBuff)), make([]byte, 12)...)
		p = append(p, opening.Packet{Class: 0, Opcode: 473, Body: add}, opening.Packet{Class: 0, Opcode: 475, Body: le.AppendUint32([]byte{1, 0}, uint32(d.InstanceBuff))})
	}
	b := le.AppendUint32([]byte{r.X, r.Y, 1}, seed)
	b = append(b, 0, 0, 0, 0, 0, 0, 1)
	b = le.AppendUint32(b, r.MapID)
	b = append(b, byte(len(r.Monsters)))
	for i, m := range r.Monsters {
		b = le.AppendUint16(b, 0)
		b = le.AppendUint32(b, uint32(i))
		b = le.AppendUint16(b, first+uint16(i))
		b = le.AppendUint32(b, m.Code)
		b = append(b, m.Level, m.Type, 0, 0)
		b = le.AppendUint32(b, 100)
		b = append(b, 0)
	}
	return append(p, opening.Packet{Class: 0, Opcode: 29, Body: append(b, 0, 0, 0, 255)}), nil
}
func LoadPackets(a protocol.Actor, sp, tp uint16) []opening.Packet {
	b := make([]byte, 91)
	b[0] = a.Level
	le.PutUint64(b[1:], a.Experience)
	for _, i := range []int{13, 15} {
		le.PutUint16(b[i:], sp)
	}
	for _, i := range []int{17, 19} {
		le.PutUint16(b[i:], tp)
	}
	return []opening.Packet{{Class: 1, Opcode: 37, Body: []byte{1}}, {Class: 0, Opcode: 37, Body: b}, {Class: 0, Opcode: 30, Body: make([]byte, 5)}, {Class: 0, Opcode: 3, Body: append(le.AppendUint16([]byte{1}, a.ID), 1)}}
}
func GroundPacket(actor uint16, d Drop) ([]byte, error) {
	id, err := TemplateID(d.Template)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 166)
	le.PutUint16(b, actor)
	le.PutUint16(b[2:], d.X)
	le.PutUint16(b[4:], d.Y)
	le.PutUint32(b[6:], d.ObjectKey)
	le.PutUint32(b[12:], id)
	le.PutUint32(b[16:], 1)
	le.PutUint16(b[164:], actor)
	return b, nil
}
func PickedPacket(scene uint32, actor, slot uint16) []byte {
	b := make([]byte, 19)
	le.PutUint32(b, scene)
	le.PutUint16(b[4:], actor)
	le.PutUint16(b[14:], actor)
	le.PutUint16(b[16:], slot)
	return b
}
func ReadPickup(b []byte) (uint32, uint16, uint16, error) {
	if len(b) != 20 && len(b) != 24 || !zeros(b[20:]) || b[4] != 0 || le.Uint16(b[10:])%7 != 0 || le.Uint16(b[16:])%7 != 0 {
		return 0, 0, 0, ErrPacket
	}
	return le.Uint32(b), le.Uint16(b[12:]), le.Uint16(b[14:]), nil
}
