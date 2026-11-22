package inventorywire

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"github.com/google/uuid"
	"s9a1/server/internal/storage"
)

type SpecialIdentity struct {
	Instance, Detail uuid.UUID
	PetSerial        uint32
}

// SavedSpecial validates durable instances in addition to their native bag projection.
func SavedSpecial(c storage.Character) ([]SpecialIdentity, error) {
	var state struct {
		SpecialAssets *struct {
			Items           []json.RawMessage
			AvatarExpansion uint16
		}
		PetSkinUnlocked bool
	}
	if err := json.Unmarshal(c.Gameplay, &state); err != nil {
		return nil, err
	}
	if state.SpecialAssets == nil {
		return nil, nil
	}
	if state.SpecialAssets.Items == nil {
		return nil, errShape
	}
	project := specialState{AvatarExpansion: state.SpecialAssets.AvatarExpansion}
	ids := make([]SpecialIdentity, 0, len(state.SpecialAssets.Items))
	validHash := func(s string) bool {
		if len(s) != 64 {
			return false
		}
		_, err := hex.DecodeString(s)
		return err == nil
	}
	socket := func(t uint16) bool { return t == 1 || t == 2 || t == 4 || t == 8 || t == 16 || t == 0xffef }
	for _, raw := range state.SpecialAssets.Items {
		var row specialItem
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		var facts struct {
			InstanceId uuid.UUID
			Template   struct {
				MinimumLevel, InitialAvatarSocketCount                 byte
				UsableJobs, MaximumDurability, InitialAvatarSocketType uint16
				ResourceExpiration                                     *int64
				RelativeLifetimeSeconds                                uint32
				WeeklyDeletionDay                                      *byte
				DailyDeletion                                          bool
				InitialAvatarSocketLayout                              *struct{ First, Second, Third, Fourth, Fifth uint16 }
			}
			Avatar, Artifact *struct{ DetailId uuid.UUID }
			Pet              *struct {
				DetailId      uuid.UUID
				SatietyMicros int64
				Name          *string
				SatietyClock  *struct {
					Owner                        uuid.UUID
					Epoch, SinceMilliseconds     int64
					Baseline                     float64
					Mode, Maximum                byte
					SpendSeconds, RecoverSeconds uint32
					ResourceHash                 string
				}
			}
		}
		if err := json.Unmarshal(raw, &facts); err != nil {
			return nil, err
		}
		t := facts.Template
		kind := row.Template.Kind
		if !validHash(row.Template.ResourceHash) || facts.InstanceId == uuid.Nil || t.MinimumLevel == 0 || t.UsableJobs == 0 || t.RelativeLifetimeSeconds > math.MaxInt32 ||
			t.ResourceExpiration != nil && (*t.ResourceExpiration <= 0 || *t.ResourceExpiration > math.MaxInt32) || t.WeeklyDeletionDay != nil && (*t.WeeklyDeletionDay > 6 || t.DailyDeletion) ||
			kind == 1 && t.MaximumDurability != 0 || kind != 1 && t.MaximumDurability == 0 || kind == 0 && row.Attributes.Durability > 254 || kind != 0 && row.Attributes.Durability > t.MaximumDurability ||
			row.Location == 3 && (c.Job >= 16 || c.Level < t.MinimumLevel || t.UsableJobs&(1<<c.Job) == 0) {
			return nil, errShape
		}
		if s := t.InitialAvatarSocketLayout; s != nil {
			if kind != 0 || t.InitialAvatarSocketCount < 1 || t.InitialAvatarSocketCount > 5 || t.InitialAvatarSocketType != 0 {
				return nil, errShape
			}
			for i, k := range [...]uint16{s.First, s.Second, s.Third, s.Fourth, s.Fifth} {
				if i < int(t.InitialAvatarSocketCount) && !socket(k) || i >= int(t.InitialAvatarSocketCount) && k != 0 {
					return nil, errShape
				}
			}
		} else if (t.InitialAvatarSocketCount != 0 || t.InitialAvatarSocketType != 0) && (kind != 0 || t.InitialAvatarSocketCount < 1 || t.InitialAvatarSocketCount > 5 || !socket(t.InitialAvatarSocketType)) {
			return nil, errShape
		}
		id := SpecialIdentity{Instance: facts.InstanceId}
		switch kind {
		case 0:
			if facts.Avatar == nil || row.Avatar == nil || row.Avatar.Sockets == nil || row.Avatar.Colors == nil {
				return nil, errShape
			}
			id.Detail = facts.Avatar.DetailId
		case 1:
			if facts.Pet == nil || row.Pet == nil || facts.Pet.Name == nil || facts.Pet.SatietyMicros < 0 {
				return nil, errShape
			}
			if p := facts.Pet.SatietyClock; p != nil && (p.Owner == uuid.Nil || p.Epoch <= 0 || p.SinceMilliseconds <= 0 || p.Baseline < 0 || p.Baseline > float64(p.Maximum) || p.Maximum == 0 || p.Maximum > 100 || p.Mode > 2 || p.SpendSeconds == 0 || p.SpendSeconds > 2147483 || p.RecoverSeconds == 0 || p.RecoverSeconds > 2147483 || !validHash(p.ResourceHash)) {
				return nil, errShape
			}
			id.Detail, id.PetSerial = facts.Pet.DetailId, row.Pet.NativeSerial
		case 2:
			if facts.Artifact == nil {
				return nil, errShape
			}
			id.Detail = facts.Artifact.DetailId
		default:
			return nil, errShape
		}
		if id.Detail == uuid.Nil {
			return nil, errShape
		}
		project.Items = append(project.Items, row)
		ids = append(ids, id)
	}
	if _, err := specialPackets(project, state.PetSkinUnlocked, nil, Options{Now: time.Now()}); err != nil {
		return nil, err
	}
	return ids, nil
}
