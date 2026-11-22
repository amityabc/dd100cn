package questwire

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"s9a1/server/internal/storage"
	"time"
)

type AvatarAction struct {
	Character    uint16
	Town, Area   uint16
	Action, Kind byte
	Proof        [32]byte
	Expiry       int64
}

func ReadAvatarAction(body []byte) (byte, error) {
	if len(body) != 20 {
		return 0, errors.New("unique avatar action requires a native twenty byte request")
	}
	action := little.Uint32(body[16:])
	if action < 1 || action > 4 {
		return 0, errors.New("unknown unique avatar action")
	}
	return byte(action), nil
}

func (s AvatarAction) Current(character uint16, town, area uint16, proof [32]byte, now time.Time) bool {
	return s.Action > 1 && s.Character == character && s.Town == town && s.Area == area && s.Proof == proof && (s.Expiry == 0 || s.Expiry > now.Unix())
}

func AvatarActionProof(raw json.RawMessage) [32]byte {
	rows, p := avatarActionRows(raw)
	if !p {
		return [32]byte{}
	}
	return avatarRowsProof(rows)
}

func avatarActionRows(raw json.RawMessage) ([8]json.RawMessage, bool) {
	var rows [8]json.RawMessage
	var p struct {
		Initialized   bool
		Progression   struct{ Hp uint32 }
		SpecialAssets struct{ Items []json.RawMessage }
	}
	if json.Unmarshal(raw, &p) != nil || !p.Initialized || p.Progression.Hp == 0 {
		return rows, false
	}
	mask := byte(0)
	for _, r := range p.SpecialAssets.Items {
		var item struct {
			Location byte
			Slot     uint16
		}
		if json.Unmarshal(r, &item) != nil {
			return rows, false
		}
		if item.Location != 3 || item.Slot >= 8 {
			continue
		}
		bit := byte(1 << item.Slot)
		if mask&bit != 0 {
			return rows, false
		}
		rows[item.Slot] = r
		mask |= bit
	}
	return rows, mask == 255
}

func avatarRowsProof(rows [8]json.RawMessage) (out [32]byte) {
	h := sha256.New()
	for _, r := range rows {
		h.Write(little.AppendUint32(nil, uint32(len(r))))
		h.Write(r)
	}
	h.Sum(out[:0])
	return out
}

func (c *Catalog) AvatarAction(c0 storage.Character, action byte, now time.Time) (AvatarAction, error) {
	s := AvatarAction{Character: c0.ID, Town: c0.Position.Town, Area: c0.Position.Area, Action: action}
	if action == 1 || action == 4 {
		var p struct {
			Initialized bool
			Progression struct{ Hp uint32 }
		}
		if json.Unmarshal(c0.Gameplay, &p) != nil || !p.Initialized || p.Progression.Hp == 0 {
			return s, storage.ErrInvalid
		}
		s.Action = 1
		return s, nil
	}
	rows, ok := avatarActionRows(c0.Gameplay)
	if !ok || c0.Job > 15 {
		return s, storage.ErrInvalid
	}
	var set int
	for slot, row := range rows {
		var a struct {
			InstanceID string
			Template   struct {
				Kind, MinimumLevel byte
				UsableJobs         uint16
				TemplateID         uint32
				ResourceHash       string
				ResourceExpiration *int64
			}
			Attributes struct{ ExpiresAt *int64 }
		}
		if json.Unmarshal(row, &a) != nil {
			return s, storage.ErrInvalid
		}
		p, ok := c.growth.AvatarEdits.ActionParts[a.Template.TemplateID]
		if !ok || p.ID != a.Template.TemplateID || a.Template.Kind != 0 || guidKey(a.InstanceID) == "" ||
			p.Hash != a.Template.ResourceHash || int(p.Slot) != slot || p.Kind > 31 || p.Set < 1000 || p.Set > 1014 ||
			c0.Level < a.Template.MinimumLevel || a.Template.UsableJobs&(1<<c0.Job) == 0 || slot > 0 && (set != p.Set || s.Kind != p.Kind) {
			return s, storage.ErrInvalid
		}
		s.Kind, set = p.Kind, p.Set
		for _, end := range []*int64{a.Template.ResourceExpiration, a.Attributes.ExpiresAt} {
			if end == nil {
				continue
			}
			if *end <= now.Unix() {
				return s, storage.ErrInvalid
			}
			if s.Expiry == 0 || s.Expiry > *end {
				s.Expiry = *end
			}
		}
	}
	s.Proof = avatarRowsProof(rows)
	return s, nil
}
