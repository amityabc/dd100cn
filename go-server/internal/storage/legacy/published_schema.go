package legacy

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
)

// The local-entry schema is append-only. The extraction utility records only changes
// between versions, including the published Oculus and Crystal branch definitions.
//
//go:embed schemas/published.json
var publishedSchemas []byte

type schemaObject struct{ Type, Table, SQL string }
type schemaMap map[string]schemaObject
type schemaDelta struct {
	Set    schemaMap
	Remove []string
}
type schemaBook struct {
	Metadata          schemaMap
	Versions          []schemaDelta
	Modules, Branches map[string]schemaDelta
}

func (d schemaDelta) apply(m schemaMap) {
	for _, n := range d.Remove {
		delete(m, n)
	}
	maps.Copy(m, d.Set)
}
func (b schemaBook) snapshot(version int, modules map[string]int, metadata bool, branch string) schemaMap {
	out := schemaMap{}
	if metadata {
		maps.Copy(out, b.Metadata)
	}
	for n := 0; n < version; n++ {
		b.Versions[n].apply(out)
	}
	for name := range modules {
		if d, ok := b.Modules[name]; ok {
			d.apply(out)
		}
	}
	if branch != "" {
		b.Branches[branch].apply(out)
	}
	return out
}

// SQL tokens follow the original schema validator, preserving the spelling of literals and quoted identifiers.
func sqlTokens(s string) ([]string, error) {
	r := []rune(s)
	tokens := []string{}
	for i := 0; i < len(r); {
		start := i
		c := r[i]
		i++
		switch {
		case unicode.IsSpace(c):
			continue
		case c == '-' && i < len(r) && r[i] == '-':
			for i < len(r) && r[i] != '\n' && r[i] != '\r' {
				i++
			}
			continue
		case c == '/' && i < len(r) && r[i] == '*':
			i++
			for i+1 < len(r) && (r[i] != '*' || r[i+1] != '/') {
				i++
			}
			if i+1 >= len(r) {
				return nil, errors.New("unterminated SQLite schema comment")
			}
			i += 2
			continue
		case c == '\'' || c == '"' || c == '`' || c == '[':
			end := c
			if c == '[' {
				end = ']'
			}
			closed := false
			for i < len(r) {
				v := r[i]
				i++
				if v == end {
					if end != ']' && i < len(r) && r[i] == end {
						i++
						continue
					}
					closed = true
					break
				}
			}
			if !closed {
				return nil, errors.New("unterminated SQLite schema quotation")
			}
		case unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_':
			for i < len(r) && (unicode.IsLetter(r[i]) || unicode.IsDigit(r[i]) || r[i] == '_') {
				i++
			}
			tokens = append(tokens, strings.ToLower(string(r[start:i])))
			continue
		}
		tokens = append(tokens, string(r[start:i]))
	}
	return tokens, nil
}
func sameSQL(a, b string) bool {
	if a == b {
		return true
	}
	left, e := sqlTokens(a)
	right, err := sqlTokens(b)
	return e == nil && err == nil && slices.Equal(left, right)
}
func databaseObjects(ctx context.Context, tx *sql.Tx) (schemaMap, error) {
	rows, err := tx.QueryContext(ctx, "SELECT name,type,tbl_name,sql FROM main.sqlite_schema WHERE name NOT GLOB 'sqlite_*' AND sql IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := schemaMap{}
	for rows.Next() {
		var o schemaObject
		var name string
		if err := rows.Scan(&name, &o.Type, &o.Table, &o.SQL); err != nil {
			return nil, err
		}
		m[name] = o
	}
	return m, rows.Err()
}
func verifySchema(ctx context.Context, tx *sql.Tx, actual, want, protected schemaMap, marked bool) error {
	for n, o := range want {
		if o.Type == "index" {
			continue
		}
		got, ok := actual[n]
		if !ok || got.Type != o.Type || !sameSQL(got.SQL, o.SQL) {
			return fmt.Errorf("historical %s %s differs from its published definition", o.Type, n)
		}
	}
	for n, o := range actual {
		if _, ok := want[n]; ok {
			continue
		}
		if p, reserved := protected[n]; reserved && p.Type != "index" && o.Type != "index" {
			return fmt.Errorf("unregistered historical schema object %s", n)
		}
		if !marked && o.Type == "table" {
			return fmt.Errorf("unregistered object in unversioned save: %s", n)
		}
	}
	return checkForeignKeys(ctx, tx)
}

// SQLite's published validator compares the partial flag and ordered index_xinfo rows.
// Explicit index names can differ in an otherwise compatible save. Table SQL already
// covers implicit primary and unique keys, including their collations and direction.
func historicalIndexes(ctx context.Context, src *sql.Tx, actual, want schemaMap) (map[string]string, error) {
	ref, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer ref.Close()
	target, err := ref.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer target.Rollback()
	for _, kind := range []string{"table", "index"} {
		for _, n := range slices.Sorted(maps.Keys(want)) {
			if o := want[n]; o.Type == kind {
				if _, err = target.ExecContext(ctx, o.SQL); err != nil {
					return nil, err
				}
			}
		}
	}
	type key struct{ name, table, shape string }
	read := func(tx *sql.Tx, objects schemaMap) ([]key, error) {
		var result []key
		for _, n := range slices.Sorted(maps.Keys(objects)) {
			o := objects[n]
			if o.Type != "index" || want[o.Table].Type != "table" {
				continue
			}
			var partial, unique int
			err := tx.QueryRowContext(ctx, `SELECT partial,"unique" FROM pragma_index_list(?) WHERE name=?`, o.Table, n).Scan(&partial, &unique)
			if err != nil {
				return nil, err
			}
			if unique == 0 {
				continue
			}
			var rows string
			err = tx.QueryRowContext(ctx, `SELECT json_group_array(json_array(seqno,cid,name,desc,coll,key) ORDER BY seqno) FROM pragma_index_xinfo(?)`, n).Scan(&rows)
			if err != nil {
				return nil, err
			}
			result = append(result, key{n, o.Table, fmt.Sprintf("%d:%s", partial, rows)})
		}
		return result, nil
	}
	old, err := read(src, actual)
	if err != nil {
		return nil, err
	}
	published, err := read(target, want)
	if err != nil {
		return nil, err
	}
	if len(old) != len(published) {
		return nil, errors.New("historical unique key count differs")
	}
	names := map[string]string{}
	// Match original names first, then equivalent keys under extension-provided names.
	for _, exact := range []bool{true, false} {
		for _, k := range published {
			if _, ok := names[k.name]; ok {
				continue
			}
			i := slices.IndexFunc(old, func(candidate key) bool {
				return candidate.table == k.table && candidate.shape == k.shape && (!exact || candidate.name == k.name)
			})
			if i >= 0 {
				names[k.name] = old[i].name
				old = slices.Delete(old, i, i+1)
			}
		}
	}
	if len(old) != 0 {
		return nil, fmt.Errorf("historical unique key differs on %s", old[0].table)
	}
	return names, nil
}
func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA main.foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("old save contains a foreign key violation")
	}
	return rows.Err()
}
func moduleVersions(ctx context.Context, tx *sql.Tx, b schemaBook, actual schemaMap, sourceVersion int, marked bool) (map[string]int, error) {
	modules := map[string]int{}
	if !marked {
		var marker int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&marker); err != nil || marker != 0 {
			return nil, errors.New("invalid unversioned save marker")
		}
		for name, d := range b.Modules {
			for n, table := range d.Set {
				if table.Type == "table" && actual[n].Type == "table" {
					modules[name] = 1
					break
				}
			}
		}
		modules["local-entry"] = sourceVersion
	} else {
		var marker, count int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&marker); err != nil || marker != 1 {
			return nil, errors.New("historical metadata version markers disagree")
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM next_schema_metadata").Scan(&count); err != nil || count != 1 {
			return nil, errors.New("invalid baseline metadata")
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM next_schema_metadata WHERE baseline='serve100-next' AND version=1").Scan(&count); err != nil || count != 1 {
			return nil, errors.New("unsupported historical baseline")
		}
		rows, err := tx.QueryContext(ctx, "SELECT module,version FROM next_schema_modules")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			var v int
			if err := rows.Scan(&name, &v); err != nil {
				return nil, err
			}
			if name == "local-entry" {
				if v != sourceVersion {
					return nil, errors.New("local-entry version changed")
				}
			} else if _, known := b.Modules[name]; !known || v != 1 {
				return nil, fmt.Errorf("unsupported historical module %s v%d", name, v)
			}
			if _, dup := modules[name]; dup {
				return nil, errors.New("duplicate historical module")
			}
			modules[name] = v
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if modules["local-entry"] != sourceVersion {
			return nil, errors.New("missing historical local-entry registration")
		}
	}
	if modules["adjustment"] == 1 && modules["counter"] == 0 {
		return nil, errors.New("historical adjustment requires counter")
	}
	return modules, nil
}
func startupBranch(b schemaBook, actual schemaMap, version int) (string, int) {
	for _, branch := range []struct {
		name         string
		marker, base int
		table        string
	}{
		{"oculus14", 14, 13, "local_entry_accounts"}, {"oculus23", 23, 22, "local_entry_accounts"},
		{"crystal26", 26, 25, "local_account_premium"}, {"crystal27", 27, 26, "local_account_premium"},
	} {
		if version == branch.marker && sameSQL(b.Branches[branch.name].Set[branch.table].SQL, actual[branch.table].SQL) {
			return branch.name, branch.base
		}
	}
	return "", version
}
