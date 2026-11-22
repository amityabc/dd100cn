package storage

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
)

type EmblemCatalog struct {
	Colors      map[uint32]uint16
	SocketTypes map[uint16][]uint16
	Compound    EmblemCompoundCatalog
}

type EmblemRequest struct {
	Opcode, Target, Material uint16
	Reference                uint32
	List                     byte
	Rows                     []EmblemRow
}

type EmblemRow struct {
	Slot      uint16
	Reference uint32
	Socket    byte
}

type EmblemRemaining struct {
	Slot  uint16
	Count uint32
}

type emblemTemplate struct {
	Kind                                byte
	TemplateID                          uint32
	WornSlot, FirstBagSlot, LastBagSlot uint16
}

func accessibleEmblem(attrs json.RawMessage, now int64) bool {
	var a materialAttributes
	return json.Unmarshal(attrs, &a) == nil && !a.Locked && (a.ExpiresAt == nil || *a.ExpiresAt > now)
}

func (x *CommerceTx) livingEmblemUser(now time.Time) bool {
	var profile struct {
		Initialized bool
		Progression struct{ Hp uint32 }
	}
	return now.Unix() > 0 && json.Unmarshal(x.Character.Gameplay, &profile) == nil && profile.Initialized && profile.Progression.Hp > 0
}

func (x *CommerceTx) emblemBag(slot uint16, now time.Time) (int, emblemTemplate, error) {
	var t emblemTemplate
	i := slices.IndexFunc(x.Inventory.Items, func(a Asset) bool { return a.Location == 0 && a.Slot == slot && a.CharacterID == x.Character.ID })
	if i < 0 {
		return i, t, ErrInvalid
	}
	a := x.Inventory.Items[i]
	if a.Source != "item" || a.Count == 0 || !validEmblemID(a.UID) || !accessibleEmblem(a.Attributes, now.Unix()) ||
		json.Unmarshal(a.Template, &t) != nil || t.TemplateID != a.TemplateID {
		return i, t, ErrInvalid
	}
	return i, t, nil
}

func validEmblemID(id string) bool {
	uid, err := uuid.Parse(id)
	return err == nil && uid != uuid.Nil
}

type emblemSocket struct {
	color uint16
	start int
}

func (x *CommerceTx) ApplyEmblem(r EmblemRequest, cat EmblemCatalog, now time.Time) ([]EmblemRemaining, error) {
	if !x.livingEmblemUser(now) {
		return nil, ErrInvalid
	}
	if r.Opcode == 201 {
		return x.embedAvatarEmblem(r, cat, now)
	}
	if r.Opcode != 907 && r.Opcode != 908 {
		return nil, ErrInvalid
	}
	i, t, err := x.emblemBag(r.Target, now)
	if err != nil {
		return nil, err
	}
	target := x.Inventory.Items[i]
	if t.Kind != 1 || len(target.Core) != 119 {
		return nil, ErrInvalid
	}
	raw := slices.Clone(target.Core)
	types := cat.SocketTypes[t.WornSlot]
	if len(types) < 1 || len(types) > 2 {
		return nil, ErrInvalid
	}
	var remaining []EmblemRemaining
	if r.Opcode == 908 {
		if raw[60] != 0 {
			return nil, ErrInvalid
		}
		m, mt, err := x.emblemBag(r.Material, now)
		if err != nil || mt.Kind != 2 || mt.TemplateID != 2749124 {
			return nil, ErrInvalid
		}
		raw[60] = byte(len(types))
		binary.LittleEndian.PutUint32(raw[61:65], math.MaxUint32)
		binary.LittleEndian.PutUint32(raw[65:69], math.MaxUint32)
		if err = x.debitAsset(m, 1); err != nil {
			return nil, err
		}
		remaining = []EmblemRemaining{{r.Material, x.Inventory.Items[m].Count}}
	} else {
		if int(raw[60]) != len(types) {
			return nil, ErrInvalid
		}
		sockets := make([]emblemSocket, len(types))
		for j, color := range types {
			sockets[j] = emblemSocket{color, 61 + j*4}
		}
		remaining, err = x.embedEmblems(raw, sockets, r.Rows, cat, false, now)
		if err != nil {
			return nil, err
		}
	}
	_, err = x.tx.ExecContext(x.ctx, "UPDATE s9a1.assets SET core=$1 WHERE account=$2 AND instance_id=$3", raw, x.account, target.UID)
	return remaining, err
}

func (x *CommerceTx) embedAvatarEmblem(r EmblemRequest, cat EmblemCatalog, now time.Time) ([]EmblemRemaining, error) {
	state, items, _, err := x.loadSpecial()
	if err != nil {
		return nil, err
	}
	if r.List != 1 && r.List != 3 {
		return nil, ErrInvalid
	}
	target := -1
	for i, item := range items {
		var f struct {
			Location byte
			Slot     uint16
		}
		if json.Unmarshal(item, &f) != nil {
			return nil, ErrInvalid
		}
		if f.Location == r.List && f.Slot == r.Target {
			if target >= 0 {
				return nil, ErrInvalid
			}
			target = i
		}
	}
	if target < 0 {
		return nil, ErrInvalid
	}
	var saved struct {
		InstanceID string
		Template   emblemTemplate
		Attributes json.RawMessage
	}
	var row, avatar map[string]json.RawMessage
	var raw []byte
	if json.Unmarshal(items[target], &saved) != nil || !validEmblemID(saved.InstanceID) || saved.Template.Kind != 0 ||
		!accessibleEmblem(saved.Attributes, now.Unix()) || json.Unmarshal(items[target], &row) != nil ||
		json.Unmarshal(row["Avatar"], &avatar) != nil || len(avatar) == 0 || json.Unmarshal(avatar["Sockets"], &raw) != nil ||
		len(raw)%6 != 0 || len(raw) > 30 {
		return nil, ErrInvalid
	}
	sockets := make([]emblemSocket, len(raw)/6)
	for j := range sockets {
		sockets[j] = emblemSocket{binary.LittleEndian.Uint16(raw[j*6:]), j*6 + 2}
	}
	remaining, err := x.embedEmblems(raw, sockets, r.Rows, cat, true, now)
	if err != nil {
		return nil, err
	}
	avatar["Sockets"], err = nativeByteList(raw)
	if err != nil {
		return nil, err
	}
	row["Avatar"], err = json.Marshal(avatar)
	if err != nil {
		return nil, err
	}
	items[target], err = json.Marshal(row)
	if err != nil {
		return nil, err
	}
	state["Items"], err = json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return remaining, replaceMailFields(&x.Character, map[string]any{"SpecialAssets": state})
}

func (x *CommerceTx) embedEmblems(raw []byte, sockets []emblemSocket, rows []EmblemRow, cat EmblemCatalog, sharedStack bool, now time.Time) ([]EmblemRemaining, error) {
	if len(rows) == 0 || len(rows) > len(sockets) {
		return nil, ErrInvalid
	}
	used := make(map[int]uint32, len(rows))
	seen := [5]bool{}
	for _, r := range rows {
		if int(r.Socket) >= len(sockets) || seen[r.Socket] {
			return nil, ErrInvalid
		}
		seen[r.Socket] = true
		i, t, err := x.emblemBag(r.Slot, now)
		if err != nil {
			return nil, err
		}
		s := sockets[r.Socket]
		color := cat.Colors[t.TemplateID]
		if t.Kind != 2 || t.FirstBagSlot != 289 || t.LastBagSlot != 360 || t.TemplateID > math.MaxInt32 ||
			color == 0 || s.color&color != color || int32(binary.LittleEndian.Uint32(raw[s.start:])) >= 0 ||
			!sharedStack && used[i] != 0 {
			return nil, ErrInvalid
		}
		binary.LittleEndian.PutUint32(raw[s.start:], t.TemplateID)
		used[i]++
	}
	remaining := make([]EmblemRemaining, 0, len(used))
	for i, count := range used {
		if err := x.debitAsset(i, count); err != nil {
			return nil, err
		}
		a := x.Inventory.Items[i]
		remaining = append(remaining, EmblemRemaining{a.Slot, a.Count})
	}
	slices.SortFunc(remaining, func(a, b EmblemRemaining) int { return int(a.Slot) - int(b.Slot) })
	return remaining, nil
}
