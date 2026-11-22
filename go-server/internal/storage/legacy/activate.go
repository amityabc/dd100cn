package legacy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Activation has a narrower contract than preserving a snapshot. A player
// whose inventory or entitlement cannot yet be projected must never appear
// online as an empty character.
func (s *source) activationIssues(ctx context.Context) error {
	r := &s.report
	if r.ModuleVersion != 35 {
		r.ActivationIssues = append(r.ActivationIssues, fmt.Sprintf("local-entry v%d requires the historical upgrade to v35 on a copy", r.ModuleVersion))
		return nil
	}
	accountCols := "account owner epoch phase selected_id cera asset_revision cargo_capacity game_options treasure_gauge oculus_guide_bindings_json unique_avatar_gauge character_slot_expansion cargo_gold hotkeys"
	charCols := "id account storage_slot name name_key job level created revision stats_json resource_hash town area x y direction state wire_tail return_town return_area return_x return_y return_direction gold coin personal_capacity gameplay_json receipts_json bag_expansion personal_capacity_100"
	for _, t := range r.Tables {
		var known string
		switch t.Name {
		case "local_entry_accounts":
			known = accountCols
		case "local_account_premium":
			known = "account premium_type expires_at"
		case "local_entry_characters":
			known = charCols
		case "local_character_receipts":
			known = "character_id receipts_json"
		case "local_weapon_assets":
			known = "account character_id instance_id template_id quality_seed durability maximum_durability minimum_level usable_jobs resource_hash location slot raw119 attributes_json detail weapon_qualification_json"
		case "local_item_assets":
			known = "account character_id instance_id template_json count location slot attributes_json raw119 detail"
		case "local_shared_material_assets":
			known = "account instance_id template_json count slot attributes_json raw119 detail"
		case "local_asset_sequences":
			known = "name next"
		case "local_name_reservations":
			known = "name_key character_id expires_at"
		case "next_schema_metadata", "next_schema_modules", "sqlite_sequence":
			continue
		default:
			if t.Rows > 0 {
				r.ActivationIssues = append(r.ActivationIssues, fmt.Sprintf("%s contains %d rows without a live Go projection", t.Name, t.Rows))
			}
			continue
		}
		for _, c := range t.Columns {
			if !strings.Contains(" "+known+" ", " "+c.Name+" ") {
				r.ActivationIssues = append(r.ActivationIssues, t.Name+"."+c.Name+" is an extension field")
			}
		}
	}
	checks := []struct{ message, query string }{
		{"account has non-default treasure, avatar or Oculus state", `SELECT EXISTS(SELECT 1 FROM local_entry_accounts WHERE treasure_gauge<>0 OR unique_avatar_gauge<>0 OR json_array_length(oculus_guide_bindings_json)<>0)`},
		{"account Cera exceeds the native signed 32-bit wallet", `SELECT EXISTS(SELECT 1 FROM local_entry_accounts WHERE cera<0 OR cera>2147483647)`},
		{"special avatar, pet or artifact needs its full native projection", `SELECT EXISTS(SELECT 1 FROM local_entry_characters WHERE coalesce(json_array_length(gameplay_json,'$.SpecialAssets.Items'),0)<>0)`},
		{"parked asset requires its original owning transaction", `SELECT EXISTS(SELECT 1 FROM local_weapon_assets WHERE location=255) OR EXISTS(SELECT 1 FROM local_item_assets WHERE location=255)`},
		{"character has a pending rename", `SELECT EXISTS(SELECT 1 FROM local_entry_characters WHERE coalesce(json_extract(gameplay_json,'$.CharacterRename.PendingSource'),0)<>0)`},
		{"pet serial allocator is already in use", `SELECT EXISTS(SELECT 1 FROM local_asset_sequences WHERE name<>'pet_serial' OR next<>1)`},
		{"character appears without its receipt row", `SELECT EXISTS(SELECT 1 FROM local_entry_characters c LEFT JOIN local_character_receipts r ON r.character_id=c.id WHERE r.character_id IS NULL)`},
	}
	for _, check := range checks {
		var found bool
		if err := s.tx.QueryRowContext(ctx, check.query).Scan(&found); err != nil {
			return err
		}
		if found {
			r.ActivationIssues = append(r.ActivationIssues, check.message)
		}
	}
	fk, err := s.tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer fk.Close()
	if fk.Next() {
		r.ActivationIssues = append(r.ActivationIssues, "source contains a foreign key violation")
	}
	return fk.Err()
}

func (s *source) activate(ctx context.Context, db *sql.DB, account string) error {
	if len(s.report.ActivationIssues) > 0 {
		return errors.New("staged only: " + strings.Join(s.report.ActivationIssues, "; "))
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active bool
	err = tx.QueryRowContext(ctx, "SELECT active FROM s9a1_import.snapshots WHERE digest=$1 AND target=$2 FOR UPDATE", s.report.SHA256, account).Scan(&active)
	if err != nil {
		return err
	}
	if active {
		s.report.Activated = true
		return nil
	}
	var epoch, cera, cargo, revision, expansion int64
	var opts, keys []byte
	err = s.tx.QueryRowContext(ctx, `SELECT epoch,cera,cargo_gold,asset_revision,character_slot_expansion,game_options,hotkeys
		FROM local_entry_accounts WHERE account='local'`).Scan(&epoch, &cera, &cargo, &revision, &expansion, &opts, &keys)
	if err != nil {
		return err
	}
	// SQLite distinguishes a zero-length BLOB from NULL. database/sql can
	// present the former as nil when scanning into a byte slice.
	if opts == nil {
		opts = []byte{}
	}
	if keys == nil {
		keys = []byte{}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO s9a1.accounts (account,epoch,cera,cargo_gold,asset_revision,capacity,game_options,hotkeys)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(account) DO NOTHING`, account, epoch, cera, cargo, revision, 32+expansion, opts, keys)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("target account already exists, activation would overwrite its state")
	}
	if _, err = tx.ExecContext(ctx, "SELECT next_id FROM s9a1.character_id_allocator WHERE singleton FOR UPDATE"); err != nil {
		return err
	}
	const columns = "id,storage_slot,name,name_key,job,level,created,revision,stats_json,gameplay_json,resource_hash,town,area,x,y,direction,state,wire_tail,return_town,return_area,return_x,return_y,return_direction,gold,coin"
	rows, err := s.tx.QueryContext(ctx, "SELECT "+columns+" FROM local_entry_characters WHERE account='local' ORDER BY id")
	if err != nil {
		return err
	}
	for rows.Next() {
		values := make([]any, 25)
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			rows.Close()
			return err
		}
		params := make([]string, 0, len(values)+1)
		for i := range values {
			params = append(params, fmt.Sprintf("$%d", i+1))
		}
		values = append(values, account)
		params = append(params, fmt.Sprintf("$%d", len(values)))
		q := "INSERT INTO s9a1.characters (" + strings.Replace(columns, "id,", "wire_id,", 1) + ",account) VALUES (" + strings.Join(params, ",") + ")"
		if _, err = tx.ExecContext(ctx, q, values...); err != nil {
			rows.Close()
			return fmt.Errorf("restoring old character %v with its reserved IDs: %w", values[0], err)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var high int64
	if err = s.tx.QueryRowContext(ctx, `SELECT max(coalesce((SELECT seq FROM sqlite_sequence WHERE name='local_entry_characters'),0),coalesce((SELECT max(id) FROM local_entry_characters),0))`).Scan(&high); err != nil {
		return err
	}
	if high < 0 || high > 65535 {
		return errors.New("legacy character ID allocator is outside the native range")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE s9a1.character_id_allocator SET next_id=greatest(next_id,$1) WHERE singleton", high+1); err != nil {
		return err
	}
	if err = s.installAssets(ctx, tx, account); err != nil {
		return err
	}
	rows, err = s.tx.QueryContext(ctx, "SELECT name_key,character_id,expires_at FROM local_name_reservations")
	if err != nil {
		return err
	}
	for rows.Next() {
		var key string
		var id, expires int64
		if err = rows.Scan(&key, &id, &expires); err != nil {
			rows.Close()
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO s9a1.name_reservations VALUES($1,$2,$3)", key, id, expires); err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var count, gold int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(gold),0) FROM s9a1.characters WHERE account=$1", account).Scan(&count, &gold); err != nil {
		return err
	}
	if count != s.report.Characters || gold != s.report.Gold {
		return errors.New("activated character count or gold total differs from the source")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE s9a1_import.snapshots SET active=true WHERE digest=$1", s.report.SHA256); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("activation commit: %w; check the recorded snapshot before retrying", err)
	}
	s.report.Activated = true
	return nil
}
