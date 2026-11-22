package legacy

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"s9a1/server/internal/savejson"
	"s9a1/server/internal/storage"
)

func validateOldBindings(s string) error { return storage.ValidateOculusBindings([]byte(s)) }
func upgradeRows(ctx context.Context, tx *sql.Tx, v int, bindings *string, sequence int64) error {
	var statement string
	switch v {
	case 4:
		return convertWorn(ctx, tx)
	case 6:
		statement = "INSERT INTO local_asset_sequences(name,next) VALUES('pet_serial',1)"
	case 14:
		statement = "UPDATE local_entry_characters SET personal_capacity_100=personal_capacity"
	case 15, 16:
		return convertProfiles(ctx, tx, v)
	case 17, 33:
		return convertInventory(ctx, tx, v)
	case 24:
		if bindings != nil {
			_, err := tx.ExecContext(ctx, "UPDATE local_entry_accounts SET oculus_guide_bindings_json=? WHERE account='local'", *bindings)
			return err
		}
		return historicalBindings(ctx, tx)
	case 29:
		if _, err := tx.ExecContext(ctx, "UPDATE sqlite_sequence SET seq=max(seq,?) WHERE name='local_entry_characters'", sequence); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO sqlite_sequence(name,seq) SELECT 'local_entry_characters',? WHERE NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='local_entry_characters')", sequence)
		return err
	case 32:
		var bad int
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM local_entry_characters WHERE json_type(receipts_json)<>'object')").Scan(&bad); err != nil {
			return err
		}
		if bad != 0 {
			return errors.New("legacy receipt history must be an object")
		}
		statement = "INSERT INTO local_character_receipts(character_id,receipts_json) SELECT id,receipts_json FROM local_entry_characters"
	default:
		return nil
	}
	_, err := tx.ExecContext(ctx, statement)
	return err
}
func convertWorn(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT character_id,template_id,quality_seed,durability FROM temp._s9a1_worn ORDER BY character_id")
	if err != nil {
		return err
	}
	type item struct {
		char     uint16
		id, seed uint32
		dur      uint16
	}
	var old []item
	for rows.Next() {
		var x item
		if err = rows.Scan(&x.char, &x.id, &x.seed, &x.dur); err != nil {
			break
		}
		old = append(old, x)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	const keys = "character_id,instance_id,template_id,quality_seed,durability,maximum_durability,minimum_level,usable_jobs,resource_hash"
	for _, x := range old {
		raw := make([]byte, 119)
		binary.LittleEndian.PutUint16(raw, 12)
		binary.LittleEndian.PutUint32(raw[2:], x.id)
		binary.LittleEndian.PutUint32(raw[6:], x.seed)
		binary.LittleEndian.PutUint16(raw[11:], x.dur)
		if _, err = tx.ExecContext(ctx, "INSERT INTO local_weapon_assets("+keys+",location,slot,raw119) SELECT "+keys+",3,12,? FROM temp._s9a1_worn WHERE character_id=?", raw, x.char); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "DROP TABLE temp._s9a1_worn")
	return err
}

type oldChar struct {
	id       uint16
	revision int64
	value    string
}

func characterJSON(ctx context.Context, tx *sql.Tx, column string) ([]oldChar, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,revision,"+quote(column)+" FROM local_entry_characters ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []oldChar
	for rows.Next() {
		var row oldChar
		if err = rows.Scan(&row.id, &row.revision, &row.value); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func convertProfiles(ctx context.Context, tx *sql.Tx, version int) error {
	chars, err := characterJSON(ctx, tx, "gameplay_json")
	if err != nil {
		return err
	}
	for _, c := range chars {
		next, err := convertProfile(ctx, tx, c.value, version)
		if err != nil {
			return fmt.Errorf("character %d: %w", c.id, err)
		}
		if next == c.value {
			continue
		}
		raw, err := savejson.Marshal(json.RawMessage(next))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE local_entry_characters SET gameplay_json=? WHERE id=?", string(raw), c.id); err != nil {
			return err
		}
	}
	return nil
}

func convertProfile(ctx context.Context, tx *sql.Tx, before string, version int) (string, error) {
	root := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(before), &root); err != nil || root == nil {
		return "", errors.New("invalid saved gameplay object")
	}
	next := before
	raw, present := root["SpecialAssets"]
	if !present && version == 15 {
		obj := `{"Items":[],"TownDisplay":false}`
		if err := tx.QueryRowContext(ctx, "SELECT json_set(?,'$.SpecialAssets',json(?))", next, obj).Scan(&next); err != nil {
			return "", err
		}
		raw = json.RawMessage(obj)
	}
	var special map[string]json.RawMessage
	if json.Unmarshal(raw, &special) != nil || special == nil {
		return "", errors.New("invalid special asset owner")
	}
	var items []struct {
		Location *byte
		Slot     *uint16
		Template struct{ Kind *int }
	}
	if err := json.Unmarshal(special["Items"], &items); err != nil || items == nil {
		return "", errors.New("invalid special item positions")
	}
	var expansion uint16
	if version == 15 {
		value, exists := special["AvatarExpansion"]
		if !exists {
			expansion = 96
			if err := tx.QueryRowContext(ctx, "SELECT json_set(?,'$.SpecialAssets.AvatarExpansion',96)", next).Scan(&next); err != nil {
				return "", err
			}
		} else if string(value) == "null" || json.Unmarshal(value, &expansion) != nil || expansion > 360 || expansion%8 != 0 {
			return "", errors.New("invalid historical avatar expansion")
		}
	}
	positions := map[[2]int]bool{}
	for i, item := range items {
		if item.Location == nil || item.Slot == nil {
			return "", errors.New("missing special item location")
		}
		loc, slot := *item.Location, *item.Slot
		if version == 15 {
			if loc == 1 && slot >= 120+expansion {
				return "", errors.New("saved avatar lies beyond expanded capacity")
			}
			continue
		}
		if loc == 7 {
			switch {
			case item.Template.Kind != nil && *item.Template.Kind == 1 && slot >= 100 && slot <= 139:
			case item.Template.Kind != nil && *item.Template.Kind == 2 && slot >= 140 && slot <= 188:
				slot += 20
				path := fmt.Sprintf("$.SpecialAssets.Items[%d].Slot", i)
				if err := tx.QueryRowContext(ctx, "SELECT json_set(?,?,?)", next, path, slot).Scan(&next); err != nil {
					return "", err
				}
			default:
				return "", errors.New("unsupported historical pet bag position")
			}
		}
		key := [2]int{int(loc), int(slot)}
		if positions[key] {
			return "", errors.New("overlapping historical special item slots")
		}
		positions[key] = true
	}
	return next, nil
}

func historicalBindings(ctx context.Context, tx *sql.Tx) error {
	chars, err := characterJSON(ctx, tx, "receipts_json")
	if err != nil {
		return err
	}
	type binding struct {
		Week        int64
		CharacterId uint16
		EntryInput  string
		Legacy      bool
	}
	type saved struct {
		revision int64
		b        binding
	}
	var entries []saved
	for _, c := range chars {
		var receipts map[string]struct {
			Input    string
			Revision int64
		}
		if err = json.Unmarshal([]byte(c.value), &receipts); err != nil || receipts == nil {
			return errors.New("invalid historical receipt aggregate")
		}
		rows, err := tx.QueryContext(ctx, `SELECT key,value FROM json_each(?) WHERE key GLOB 'oculus-entry:run:*' OR key GLOB 'oculus-entry:end:*' OR key GLOB 'oculus-entry:credit:*' ORDER BY json_extract(value,'$.Revision')`, c.value)
		if err != nil {
			return err
		}
		if _, _, _, err = storage.OculusReceiptCounts(rows, storage.Character{ID: c.id, Revision: c.revision}, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
			return err
		}
		for k, r := range receipts {
			if !strings.HasPrefix(k, "oculus-entry:run:") {
				continue
			}
			var e storage.OculusEntry
			if err = json.Unmarshal([]byte(r.Input), &e); err != nil {
				return err
			}
			if e.Mode == 2 {
				entries = append(entries, saved{r.Revision, binding{e.Week, e.CharacterId, r.Input, true}})
			}
		}
	}
	slices.SortStableFunc(entries, func(x, y saved) int {
		if x.revision != y.revision {
			if x.revision < y.revision {
				return -1
			}
			return 1
		}
		return int(x.b.CharacterId) - int(y.b.CharacterId)
	})
	seen := map[[2]int64]bool{}
	out := []binding{}
	for _, s := range entries {
		k := [2]int64{s.b.Week, int64(s.b.CharacterId)}
		if !seen[k] {
			out = append(out, s.b)
			seen[k] = true
		}
	}
	raw, err := savejson.Marshal(out)
	if err != nil {
		return err
	}
	if err = storage.ValidateOculusBindings(raw); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE local_entry_accounts SET oculus_guide_bindings_json=? WHERE account='local'", string(raw))
	return err
}
