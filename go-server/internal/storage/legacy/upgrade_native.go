package legacy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

func upgradeNative(ctx context.Context, path string, report Report) error {
	var book schemaBook
	if err := json.Unmarshal(publishedSchemas, &book); err != nil {
		return err
	}
	if len(book.Versions) != latestLocalEntry || report.ModuleVersion < 1 || report.ModuleVersion >= latestLocalEntry {
		return fmt.Errorf("invalid historical schema version %d", report.ModuleVersion)
	}
	db, err := sql.Open("sqlite", sqliteURI(path, "mode=rw&_txlock=immediate&_pragma=foreign_keys(0)"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actual, err := databaseObjects(ctx, tx)
	if err != nil {
		return err
	}
	marked := !report.VersionInferred
	modules, err := moduleVersions(ctx, tx, book, actual, report.ModuleVersion, marked)
	if err != nil {
		return err
	}
	branch, base := startupBranch(book, actual, report.ModuleVersion)
	start := book.snapshot(base, modules, marked, branch)
	protected := maps.Clone(book.Metadata)
	for _, d := range book.Versions {
		maps.Copy(protected, d.Set)
	}
	for _, m := range book.Modules {
		maps.Copy(protected, m.Set)
	}
	if err := verifySchema(ctx, tx, actual, start, protected, marked); err != nil {
		return err
	}
	indexNames, err := historicalIndexes(ctx, tx, actual, start)
	if err != nil {
		return err
	}
	if err := checkUpgradeExtensions(ctx, tx, actual, start, base); err != nil {
		return err
	}
	if strings.HasPrefix(branch, "oculus") {
		// The published branch drops its premature column before reappending it at v24.
		// Let SQLite itself check dependent external views, indexes and triggers.
		if _, err = tx.ExecContext(ctx, "SAVEPOINT validate_oculus_column; ALTER TABLE local_entry_accounts DROP COLUMN oculus_guide_bindings_json; ROLLBACK TO validate_oculus_column; RELEASE validate_oculus_column"); err != nil {
			return err
		}
	}
	// Preserve explicit extension definitions. Rebuilding owner tables must not run user triggers.
	extras := schemaMap{}
	for n, o := range actual {
		if _, standard := start[n]; !standard && o.Type != "table" {
			extras[n] = o
		}
	}
	for _, n := range slices.Sorted(maps.Keys(actual)) {
		o := actual[n]
		if o.Type == "view" || o.Type == "trigger" || o.Type == "index" {
			if _, err = tx.ExecContext(ctx, "DROP "+o.Type+" IF EXISTS "+quote(n)); err != nil {
				return err
			}
		}
	}
	current := maps.Clone(start)
	if !marked {
		for _, n := range slices.Sorted(maps.Keys(book.Metadata)) {
			o := book.Metadata[n]
			if _, err = tx.ExecContext(ctx, o.SQL); err != nil {
				return err
			}
			current[n] = o
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO next_schema_metadata VALUES('serve100-next',1); PRAGMA user_version=1"); err != nil {
			return err
		}
		for name, v := range modules {
			if _, err = tx.ExecContext(ctx, "INSERT INTO next_schema_modules VALUES(?,?)", name, v); err != nil {
				return err
			}
		}
	}
	var savedBindings *string
	if strings.HasPrefix(branch, "oculus") {
		var saved string
		if err = tx.QueryRowContext(ctx, "SELECT oculus_guide_bindings_json FROM local_entry_accounts WHERE account='local'").Scan(&saved); err != nil {
			return err
		}
		if err = validateOldBindings(saved); err != nil {
			return err
		}
		savedBindings = &saved
		target := book.snapshot(base, modules, true, "")["local_entry_accounts"]
		if err = rebuildTable(ctx, tx, "local_entry_accounts", target.SQL, true); err != nil {
			return err
		}
		current["local_entry_accounts"] = target
	}
	var sequence int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce((SELECT seq FROM sqlite_sequence WHERE name='local_entry_characters'),0)").Scan(&sequence); err != nil {
		return err
	}
	for version := base + 1; version <= latestLocalEntry; version++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		delta := book.Versions[version-1]
		if err = applyTables(ctx, tx, current, delta, version); err != nil {
			return fmt.Errorf("old save v%d tables: %w", version, err)
		}
		if err = upgradeRows(ctx, tx, version, savedBindings, sequence); err != nil {
			return fmt.Errorf("old save v%d records: %w", version, err)
		}
		delta.apply(current)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE next_schema_modules SET version=? WHERE module='local-entry'", latestLocalEntry); err != nil {
		return err
	}
	final := book.snapshot(latestLocalEntry, modules, true, "")
	objects := maps.Clone(extras)
	maps.Copy(objects, final)
	for published, original := range indexNames {
		delete(objects, original)
		if final[published] == start[published] {
			delete(objects, published)
			objects[original] = actual[original]
		}
	}
	for _, kind := range []string{"index", "view", "trigger"} {
		for _, n := range slices.Sorted(maps.Keys(objects)) {
			o := objects[n]
			if o.Type == kind {
				if _, err = tx.ExecContext(ctx, o.SQL); err != nil {
					return fmt.Errorf("restore %s: %w", n, err)
				}
			}
		}
	}
	got, err := databaseObjects(ctx, tx)
	if err != nil {
		return err
	}
	if err = verifySchema(ctx, tx, got, final, protected, true); err != nil {
		return err
	}
	if _, err = historicalIndexes(ctx, tx, got, final); err != nil {
		return err
	}
	return tx.Commit()
}

func checkUpgradeExtensions(ctx context.Context, tx *sql.Tx, all, known schemaMap, base int) error {
	for n, o := range all {
		if _, ok := known[n]; ok {
			continue
		}
		lower := strings.ToLower(o.SQL)
		if base < 4 && o.Table == "local_equipped_weapons" && (o.Type == "index" || o.Type == "trigger") {
			return fmt.Errorf("%s requires a migration for the retired weapon table", n)
		}
		if base < 32 && (o.Type == "view" && strings.Contains(lower, "local_entry_characters") || o.Table == "local_entry_characters" && (o.Type == "index" || o.Type == "trigger") && strings.Contains(lower, "receipts_json")) {
			return fmt.Errorf("%s requires an explicit receipt migration", n)
		}
		if o.Type != "table" {
			continue
		}
		rows, err := tx.QueryContext(ctx, `SELECT "table",on_delete FROM pragma_foreign_key_list(?)`, n)
		if err != nil {
			return err
		}
		for rows.Next() {
			var parent, action string
			if err = rows.Scan(&parent, &action); err != nil {
				break
			}
			p := strings.ToLower(parent)
			if base < 30 && p == "local_weapon_assets" || base < 33 && p == "local_item_assets" || base < 34 && p == "local_account_premium" || base < 25 && p == "local_fate_exchange_purchases" || base < 29 && p == "local_entry_characters" && action != "NO ACTION" {
				err = fmt.Errorf("%s has an external foreign key requiring its own migration", n)
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func applyTables(ctx context.Context, tx *sql.Tx, current schemaMap, d schemaDelta, version int) error {
	if version == 4 {
		if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE _s9a1_worn AS SELECT * FROM local_equipped_weapons ORDER BY character_id; DROP TABLE local_equipped_weapons"); err != nil {
			return err
		}
	}
	for _, n := range slices.Sorted(maps.Keys(d.Set)) {
		next := d.Set[n]
		if next.Type != "table" {
			continue
		}
		prev, exists := current[n]
		if !exists || prev.Type != "table" {
			if _, err := tx.ExecContext(ctx, next.SQL); err != nil {
				return err
			}
			continue
		}
		if sameSQL(prev.SQL, next.SQL) {
			continue
		}
		done, err := addColumns(ctx, tx, n, next.SQL)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		keepRowID := n != "local_weapon_assets" || (version != 5 && version != 14)
		if err = rebuildTable(ctx, tx, n, next.SQL, keepRowID); err != nil {
			return err
		}
	}
	return nil
}

func tableColumns(ctx context.Context, tx *sql.Tx, name string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name FROM pragma_table_xinfo(?) WHERE hidden=0 ORDER BY cid", name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var col string
		if err = rows.Scan(&col); err != nil {
			return nil, err
		}
		names = append(names, col)
	}
	return names, rows.Err()
}

var ddlDelimiters = regexp.MustCompile("(?s)'(?:''|[^'])*'|\"(?:\"\"|[^\"])*\"|`(?:``|[^`])*`|\\[[^\\]]*\\]|/\\*.*?\\*/|--[^\\r\\n]*|[(),]")

func addColumns(ctx context.Context, tx *sql.Tx, name, ddl string) (bool, error) {
	old, err := tableColumns(ctx, tx, name)
	if err != nil {
		return false, err
	}
	var additions []string
	depth, start := 0, 0
	for _, m := range ddlDelimiters.FindAllStringIndex(ddl, -1) {
		t := ddl[m[0]:m[1]]
		if depth == 1 && (t == "," || t == ")") {
			part := strings.TrimSpace(ddl[start:m[0]])
			tokens, err := sqlTokens(part)
			if err != nil || len(tokens) == 0 {
				return false, fmt.Errorf("invalid published column in %s", name)
			}
			key := tokens[0]
			if !slices.Contains([]string{"unique", "check", "constraint", "foreign", "primary"}, key) && !slices.Contains(old, key) {
				additions = append(additions, part)
			}
			start = m[1]
		}
		if t == "(" {
			if depth == 0 {
				start = m[1]
			}
			depth++
		} else if t == ")" {
			depth--
		}
	}
	if len(additions) == 0 {
		return false, nil
	}
	for _, a := range additions {
		if _, err = tx.ExecContext(ctx, "ALTER TABLE "+quote(name)+" ADD COLUMN "+a); err != nil {
			return false, err
		}
	}
	var sqlText string
	err = tx.QueryRowContext(ctx, "SELECT sql FROM main.sqlite_schema WHERE type='table' AND name=?", name).Scan(&sqlText)
	return sameSQL(sqlText, ddl), err
}

func rebuildTable(ctx context.Context, tx *sql.Tx, name, ddl string, keepRowID bool) error {
	old, err := tableColumns(ctx, tx, name)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "CREATE TEMP TABLE _s9a1_table AS SELECT rowid AS _s9a1_id,* FROM main."+quote(name)+";DROP TABLE main."+quote(name)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	cols, err := tableColumns(ctx, tx, name)
	if err != nil {
		return err
	}
	var from, to []string
	if keepRowID {
		from, to = []string{"_s9a1_id"}, []string{"rowid"}
	}
	for _, col := range old {
		if slices.Contains(cols, col) {
			from = append(from, quote(col))
			to = append(to, quote(col))
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO main."+quote(name)+"("+strings.Join(to, ",")+") SELECT "+strings.Join(from, ",")+" FROM temp._s9a1_table ORDER BY _s9a1_id; DROP TABLE temp._s9a1_table")
	return err
}
