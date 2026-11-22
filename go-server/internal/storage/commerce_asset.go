package storage

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Legacy and newly acquired weapon instances share the same worn-slot owner.
// A complete weapon cannot be written as a generic armor or stackable row.
func attachCommerceWeapon(a *Asset, t ordinaryTemplate) error {
	if t.Kind != 0 {
		return nil
	}
	var attrs struct {
		QualitySeed uint32
		Durability  uint16
	}
	var template struct{ WeaponQualification json.RawMessage }
	if json.Unmarshal(a.Attributes, &attrs) != nil || json.Unmarshal(a.Template, &template) != nil ||
		attrs.QualitySeed == 0 || attrs.QualitySeed >= 999999999 || attrs.Durability > t.MaximumDurability {
		return ErrInvalid
	}
	a.Source = "weapon"
	a.Weapon = &WeaponFacts{
		QualitySeed: attrs.QualitySeed, Durability: attrs.Durability, MaximumDurability: t.MaximumDurability,
		MinimumLevel: t.MinimumLevel, UsableJobs: t.UsableJobs, ResourceHash: t.ResourceHash, Qualification: template.WeaponQualification,
	}
	return nil
}

func insertCommerceAsset(ctx context.Context, tx *sql.Tx, account string, a Asset) error {
	var char any
	if a.CharacterID != 0 {
		char = a.CharacterID
	}
	weapon, err := json.Marshal(a.Weapon)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO s9a1.assets
		(instance_id,account,character_id,source,template_id,template_json,count,location,slot,attributes_json,core,detail,weapon_facts)
		VALUES($1,$2,$3,$4,$5,$6::json,$7,$8,$9,$10::json,$11,$12,$13::json)`,
		a.UID, account, char, a.Source, a.TemplateID, string(a.Template), a.Count, a.Location, a.Slot,
		string(a.Attributes), a.Core, append([]byte{}, a.Detail...), string(weapon))
	return err
}
