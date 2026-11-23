package questwire

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"s9a1/server/internal/storage"
	"slices"
	"time"
)

type teleportDefinition struct {
	Item                 json.RawMessage
	CooldownMilliseconds uint32
	Destinations         []storage.Position
	DestinationHash      string
}

func (d teleportDefinition) MarshalJSON() ([]byte, error) {
	points := make([]questPoint, 0, len(d.Destinations))
	for _, position := range d.Destinations {
		points = append(points, questPosition(position))
	}
	return json.Marshal(struct {
		Item                 json.RawMessage
		CooldownMilliseconds uint32
		Destinations         []questPoint
		DestinationHash      string
	}{d.Item, d.CooldownMilliseconds, points, d.DestinationHash})
}

type TeleportRequest struct {
	Sequence, Slot uint16
	TemplateID     uint32
	List           byte
	Town           uint32
	Area           byte
	X, Y           uint16
	HasMode        bool
	Mode           byte
}

type TeleportReplay struct {
	Request     TeleportRequest
	Character   uint16
	Revision    int64
	Destination storage.Position
}

func ReadTeleport(seq uint16, b []byte) (TeleportRequest, error) {
	q := TeleportRequest{Sequence: seq}
	bad := errors.New("invalid native teleport request")
	if len(b) != 16 && len(b) != 17 {
		return q, bad
	}
	q.Slot, q.TemplateID, q.List = little.Uint16(b), little.Uint32(b[2:]), b[6]
	q.Town, q.Area, q.X, q.Y = little.Uint32(b[7:]), b[11], little.Uint16(b[12:]), little.Uint16(b[14:])
	if len(b) == 17 {
		q.HasMode, q.Mode = true, b[16]
	}
	if q.List != 0 || q.TemplateID == 0 || q.TemplateID > math.MaxInt32 || q.Town > 255 || q.X > math.MaxInt16 || q.Y > math.MaxInt16 {
		return q, bad
	}
	return q, nil
}

func (cat *Catalog) UseTeleport(c storage.Character, in storage.Inventory, q TeleportRequest, now time.Time, returning bool, prior TeleportReplay,
	allows, isRoom, recordsOrigin func(storage.Position) bool) (storage.TownQuestMutation, bool, error) {
	var out storage.TownQuestMutation
	if c.ID == prior.Character && c.Revision == prior.Revision && prior.Request == q && prior.Destination == c.Position {
		return out, true, nil
	}
	d, ok := cat.growth.Teleports[q.TemplateID]
	if returning || !ok || q.HasMode && q.Mode != 1 || c.ID != in.CharacterID || len(d.Destinations) == 0 || len(d.Destinations) > 256 ||
		len(d.DestinationHash) != 64 || d.CooldownMilliseconds > math.MaxInt32 || !allows(c.Position) {
		return out, false, storage.ErrInvalid
	}
	if _, err := hex.DecodeString(d.DestinationHash); err != nil {
		return out, false, storage.ErrInvalid
	}
	dest := storage.Position{Town: uint16(q.Town), Area: uint16(q.Area), X: q.X, Y: q.Y, Direction: 5}
	if q.X == math.MaxInt16 || q.Y == math.MaxInt16 {
		i := slices.IndexFunc(d.Destinations, func(p storage.Position) bool { return p.Town == dest.Town && p.Area == dest.Area })
		if q.X != q.Y || i < 0 {
			return out, false, storage.ErrInvalid
		}
		dest = d.Destinations[i]
	}
	if !allows(dest) {
		return out, false, storage.ErrInvalid
	}
	var p struct {
		Initialized          bool
		Progression          struct{ Hp uint32 }
		TeleportPotionUsedAt int64
	}
	at := now.UnixMilli()
	if json.Unmarshal(c.Gameplay, &p) != nil || !p.Initialized || p.Progression.Hp == 0 || at <= 0 ||
		p.TeleportPotionUsedAt < 0 || p.TeleportPotionUsedAt != 0 && (at < p.TeleportPotionUsedAt || at-p.TeleportPotionUsedAt < int64(d.CooldownMilliseconds)) {
		return out, false, storage.ErrInvalid
	}
	i := slices.IndexFunc(in.Items, func(a storage.Asset) bool {
		return a.Slot == q.Slot && a.TemplateID == q.TemplateID && storage.QuestTeleportSource(a, d.Item, in, c, at/1000)
	})
	if i < 0 {
		return out, false, storage.ErrInvalid
	}
	var doc skillPage
	if json.Unmarshal(c.Gameplay, &doc) != nil || doc == nil {
		return out, false, storage.ErrInvalid
	}
	last, err := json.Marshal(at)
	if err != nil {
		return out, false, err
	}
	doc["TeleportPotionUsedAt"] = last
	raw, err := json.Marshal(doc)
	if err != nil {
		return out, false, err
	}
	out.Teleport = &storage.QuestTeleport{Position: dest, Gameplay: raw, ConsumeUID: in.Items[i].UID,
		RecordOrigin: isRoom(dest) && !isRoom(c.Position) && recordsOrigin(c.Position)}
	return out, false, nil
}
