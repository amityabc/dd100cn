package storage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"time"
)

// DungeonArtifact retains the complete pet equipment selected before pickup or card claim.
type DungeonArtifact struct {
	Template    json.RawMessage
	QualitySeed uint32
	Durability  uint16
}

func (a *DungeonArtifact) definition() (specialPurchaseTemplate, error) {
	var t specialPurchaseTemplate
	var life materialDefinition
	if a == nil || a.QualitySeed == 0 || a.QualitySeed >= 999999999 || json.Unmarshal(a.Template, &t) != nil ||
		json.Unmarshal(a.Template, &life) != nil || t.Kind != 2 || t.WornSlot < 27 || t.WornSlot > 29 ||
		t.MaximumDurability != a.Durability || !matchesSHA(t.ResourceHash) || t.MinimumLevel == 0 || t.UsableJobs == 0 ||
		life.ResourceExpiration != nil || life.RelativeLifetimeSeconds != 0 || life.DailyDeletion || life.WeeklyDeletionDay != nil {
		return t, ErrInvalid
	}
	return t, nil
}

func (a *DungeonArtifact) grant(c Character, now time.Time) (Character, json.RawMessage, uint16, error) {
	if _, err := a.definition(); err != nil {
		return c, nil, 0, err
	}
	x := CommerceTx{Character: c}
	raw, err := x.grantSpecialWithQuality(a.Template, a.Durability, "", now, false, a.QualitySeed)
	if err != nil {
		return c, nil, 0, err
	}
	var destination struct{ Slot uint16 }
	if json.Unmarshal(raw, &destination) != nil || a.verifyGrant(raw, destination.Slot) != nil {
		return c, nil, 0, ErrConflict
	}
	return x.Character, raw, destination.Slot, nil
}

func (a *DungeonArtifact) verifyGrant(raw json.RawMessage, slot uint16) error {
	t, err := a.definition()
	if err != nil {
		return err
	}
	var g struct {
		InstanceId           string
		Template, Attributes json.RawMessage
		Location             byte
		Slot                 uint16
		Core153              []byte
		Pet, Avatar          *json.RawMessage
		Artifact             *struct {
			DetailId string
			Color    uint16
		}
	}
	if json.Unmarshal(raw, &g) != nil || !matchesUUID(g.InstanceId) || g.Artifact == nil ||
		!matchesUUID(g.Artifact.DetailId) || g.Artifact.DetailId == g.InstanceId || g.Artifact.Color != t.WornSlot ||
		g.Location != 7 || g.Slot != slot || slot < 160 || slot > 215 || g.Pet != nil || g.Avatar != nil || !equalJSON(g.Template, a.Template) {
		return ErrConflict
	}
	var life materialDefinition
	if err := json.Unmarshal(a.Template, &life); err != nil {
		return err
	}
	attributes, _, err := commerceItemCore(ordinaryTemplate{Kind: 2, MaximumDurability: a.Durability}, life, time.Unix(0, 0))
	if err != nil {
		return err
	}
	var expected map[string]json.RawMessage
	var saved map[string]json.RawMessage
	if json.Unmarshal(attributes, &expected) != nil || json.Unmarshal(g.Attributes, &saved) != nil {
		return ErrConflict
	}
	expected["QualitySeed"], _ = json.Marshal(a.QualitySeed)
	for key, value := range expected {
		if !equalJSON(saved[key], value) {
			return ErrConflict
		}
	}
	core := make([]byte, 153)
	binary.LittleEndian.PutUint32(core[2:], t.TemplateID)
	binary.LittleEndian.PutUint32(core[6:], a.QualitySeed)
	binary.LittleEndian.PutUint16(core[11:], a.Durability)
	if life.InitialSealed {
		core[13] = 1
	}
	if !bytes.Equal(core, g.Core153) {
		return ErrConflict
	}
	return nil
}
