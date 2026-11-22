package legacy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"s9a1/server/internal/storage"
)

func (s *source) installAssets(ctx context.Context, pg *sql.Tx, account string) error {
	var cargo int
	var conqueror, skillMaster, crystal, growth int64
	if err := s.tx.QueryRowContext(ctx, `SELECT cargo_capacity,
		coalesce(max(expires_at) FILTER(WHERE premium_type=22),0),coalesce(max(expires_at) FILTER(WHERE premium_type=27),0),
		coalesce(max(expires_at) FILTER(WHERE premium_type=97),0),coalesce(max(expires_at) FILTER(WHERE premium_type=84),0)
		FROM local_entry_accounts a LEFT JOIN local_account_premium p ON p.account=a.account WHERE a.account='local' GROUP BY a.account`).
		Scan(&cargo, &conqueror, &skillMaster, &crystal, &growth); err != nil {
		return err
	}
	if _, err := pg.ExecContext(ctx, `UPDATE s9a1.accounts SET cargo_capacity=$2,conqueror_expires_at=$3,skill_master_expires_at=$4,
		crystal_expires_at=$5,growth_expires_at=$6 WHERE account=$1`, account, cargo, conqueror, skillMaster, crystal, growth); err != nil {
		return err
	}
	for _, tbl := range s.report.Tables {
		if tbl.Name != "local_account_battle_pass" || tbl.Rows == 0 {
			continue
		}
		var state string
		err := s.tx.QueryRowContext(ctx, "SELECT state_json FROM local_account_battle_pass WHERE account='local'").Scan(&state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if _, err = pg.ExecContext(ctx, "UPDATE s9a1.accounts SET battle_pass=$2::json WHERE account=$1", account, state); err != nil {
				return err
			}
		}
	}
	owners := map[uint16]storage.Character{}
	rows, err := s.tx.QueryContext(ctx, "SELECT id,job,gameplay_json,bag_expansion,personal_capacity,personal_capacity_100 FROM local_entry_characters WHERE account='local'")
	if err != nil {
		return err
	}
	for rows.Next() {
		var c storage.Character
		var profile string
		var bag, oldCap, cap int
		if err = rows.Scan(&c.ID, &c.Job, &profile, &bag, &oldCap, &cap); err != nil {
			rows.Close()
			return err
		}
		c.Gameplay = json.RawMessage(profile)
		owners[c.ID] = c
		if _, err = pg.ExecContext(ctx, `UPDATE s9a1.characters SET bag_expansion=$3,legacy_personal_capacity=$4,personal_capacity=$5 WHERE account=$1 AND wire_id=$2`, account, c.ID, bag, oldCap, cap); err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, kind := range []string{"weapon", "item", "shared"} {
		if err = s.copyAssetKind(ctx, pg, account, owners, kind); err != nil {
			return fmt.Errorf("restore %s assets: %w", kind, err)
		}
	}
	if err = s.copyCommerceHistory(ctx, pg, account); err != nil {
		return err
	}
	return s.copyReceipts(ctx, pg)
}

func (s *source) copyAssetKind(ctx context.Context, pg *sql.Tx, account string, owners map[uint16]storage.Character, kind string) error {
	queries := map[string]string{
		"weapon": `SELECT coalesce(character_id,0),instance_id,template_id,quality_seed,durability,maximum_durability,minimum_level,usable_jobs,resource_hash,weapon_qualification_json,location,slot,attributes_json,raw119,detail FROM local_weapon_assets WHERE account='local'`,
		"item":   `SELECT coalesce(character_id,0),instance_id,template_json,count,location,slot,attributes_json,raw119,detail FROM local_item_assets WHERE account='local'`,
		"shared": `SELECT 0,instance_id,template_json,count,0,slot,attributes_json,raw119,detail FROM local_shared_material_assets WHERE account='local'`,
	}
	rows, err := s.tx.QueryContext(ctx, queries[kind])
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		a := storage.Asset{Source: kind}
		var template, attr string
		if kind == "weapon" {
			w := storage.WeaponFacts{}
			var qual string
			if err = rows.Scan(&a.CharacterID, &a.UID, &a.TemplateID, &w.QualitySeed, &w.Durability, &w.MaximumDurability, &w.MinimumLevel, &w.UsableJobs, &w.ResourceHash, &qual, &a.Location, &a.Slot, &attr, &a.Core, &a.Detail); err != nil {
				return err
			}
			w.Qualification = json.RawMessage(qual)
			a.Weapon = &w
			a.Count = 1
			var fields struct{ FrozenWeaponTemplate json.RawMessage }
			if err = json.Unmarshal([]byte(attr), &fields); err != nil {
				return err
			}
			template = "null"
			if len(fields.FrozenWeaponTemplate) > 0 {
				template = string(fields.FrozenWeaponTemplate)
			}
		} else {
			if err = rows.Scan(&a.CharacterID, &a.UID, &template, &a.Count, &a.Location, &a.Slot, &attr, &a.Core, &a.Detail); err != nil {
				return err
			}
			var key struct{ TemplateID uint32 }
			if err = json.Unmarshal([]byte(template), &key); err != nil {
				return err
			}
			a.TemplateID = key.TemplateID
		}
		a.Attributes, a.Template = json.RawMessage(attr), json.RawMessage(template)
		if a.CharacterID != 0 {
			if _, ok := owners[a.CharacterID]; !ok {
				return errors.New("asset references a different account")
			}
		}
		if a.Location == 3 {
			if _, err = a.WornEquipment(owners[a.CharacterID]); err != nil {
				return fmt.Errorf("unsupported native worn state for %s: %w", a.UID, err)
			}
		}
		weapon, err := json.Marshal(a.Weapon)
		if err != nil {
			return err
		}
		var char any
		if a.CharacterID != 0 {
			char = a.CharacterID
		}
		if a.Detail == nil {
			a.Detail = []byte{}
		}
		var core, detail []byte
		var savedAttr, savedTemplate string
		err = pg.QueryRowContext(ctx, `INSERT INTO s9a1.assets(instance_id,account,character_id,source,template_id,template_json,weapon_facts,count,location,slot,attributes_json,core,detail)
			VALUES($1,$2,$3,$4,$5,$6::json,$7::json,$8,$9,$10,$11::json,$12,$13) RETURNING core,detail,attributes_json::text,template_json::text`,
			a.UID, account, char, a.Source, a.TemplateID, template, string(weapon), a.Count, a.Location, a.Slot, attr, a.Core, a.Detail).Scan(&core, &detail, &savedAttr, &savedTemplate)
		if err != nil {
			return err
		}
		if !bytes.Equal(core, a.Core) || !bytes.Equal(detail, a.Detail) || savedAttr != attr || savedTemplate != template {
			return fmt.Errorf("frozen asset %s differs after insert", a.UID)
		}
	}
	return rows.Err()
}

func (s *source) copyReceipts(ctx context.Context, pg *sql.Tx) error {
	rows, err := s.tx.QueryContext(ctx, "SELECT character_id,receipts_json FROM local_character_receipts ORDER BY character_id")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uint16
		var raw string
		if err = rows.Scan(&id, &raw); err != nil {
			return err
		}
		d := json.NewDecoder(strings.NewReader(raw))
		t, err := d.Token()
		if err != nil || t != json.Delim('{') {
			return errors.New("legacy receipt history must be an object")
		}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || key == "" {
				return errors.New("invalid receipt key")
			}
			var p json.RawMessage
			if err = d.Decode(&p); err != nil {
				return err
			}
			var check struct {
				Key      string
				Revision int64
			}
			if err = json.Unmarshal(p, &check); err != nil || check.Key != key || check.Revision < 0 {
				return fmt.Errorf("invalid legacy receipt %q on character %d", key, id)
			}
			var saved string
			err = pg.QueryRowContext(ctx, "INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json) RETURNING payload::text", id, key, string(p)).Scan(&saved)
			if err != nil {
				return err
			}
			if saved != string(p) {
				return errors.New("receipt value changed during import")
			}
		}
		if _, err = d.Token(); err != nil {
			return err
		}
	}
	return rows.Err()
}
