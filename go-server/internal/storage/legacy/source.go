// Package legacy inspects a consistent, read-only snapshot of an old save.
// It is used by the offline importer, never by the game process.
package legacy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

type Object struct{ Kind, Name, Table, SQL string }
type Column struct {
	Name, Type string
	Primary    int
}
type Table struct {
	Name    string
	Columns []Column
	Rows    int64
	SHA256  string
}
type Report struct {
	ModuleVersion     int
	Tables            []Table
	SHA256            string
	Characters, Gold  int64
	ActivationIssues  []string
	Staged, Activated bool
	ActivationVersion int    `json:",omitempty"`
	ActivationSHA256  string `json:",omitempty"`
	VersionInferred   bool   `json:",omitempty"`
}
type source struct {
	db      *sql.DB
	conn    *sql.Conn
	tx      *sql.Tx
	objects []Object
	report  Report
}

func openSource(ctx context.Context, path string) (*source, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("source must be an existing SQLite file")
	}
	db, err := sql.Open("sqlite", sqliteURI(abs, "mode=ro&_pragma=query_only(1)"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		conn.Close()
		db.Close()
		return nil, err
	}
	s := &source{db: db, conn: conn, tx: tx}
	if err = s.inspect(ctx); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func sqliteURI(abs, query string) string {
	p := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: query}).String()
}

func (s *source) close() { s.tx.Rollback(); s.conn.Close(); s.db.Close() }

func Inspect(ctx context.Context, path string) (Report, error) {
	s, err := openSource(ctx, path)
	if err != nil {
		return Report{}, err
	}
	defer s.close()
	return s.report, nil
}

func (s *source) inspect(ctx context.Context) error {
	if err := s.inspectVersion(ctx); err != nil {
		return err
	}
	var err error
	var count int
	if err = s.tx.QueryRowContext(ctx, `SELECT count(*) FROM local_entry_accounts WHERE account='local'`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("expected exactly one local account")
	}
	rows, err := s.tx.QueryContext(ctx, `SELECT type,name,tbl_name,coalesce(sql,'') FROM main.sqlite_schema ORDER BY type,name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var obj Object
		if err = rows.Scan(&obj.Kind, &obj.Name, &obj.Table, &obj.SQL); err != nil {
			rows.Close()
			return err
		}
		s.objects = append(s.objects, obj)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return s.inspectTables(ctx)
}

func (s *source) inspectVersion(ctx context.Context) error {
	var marked int
	if err := s.tx.QueryRowContext(ctx, `SELECT count(*) FROM main.sqlite_schema
		WHERE type='table' AND name IN ('next_schema_metadata','next_schema_modules')`).Scan(&marked); err != nil {
		return err
	}
	if marked == 0 {
		return s.inspectUnmarkedVersion(ctx)
	}
	var meta int
	err := s.tx.QueryRowContext(ctx, `SELECT version FROM next_schema_metadata WHERE baseline='serve100-next'`).Scan(&meta)
	if err != nil || meta != 1 {
		return fmt.Errorf("unrecognized S9A1 save metadata (version %d, read %v)", meta, err)
	}
	err = s.tx.QueryRowContext(ctx, `SELECT version FROM next_schema_modules WHERE module='local-entry'`).Scan(&s.report.ModuleVersion)
	if err != nil {
		return err
	}
	if s.report.ModuleVersion < 1 || s.report.ModuleVersion > latestLocalEntry {
		return fmt.Errorf("unsupported local-entry version %d", s.report.ModuleVersion)
	}
	return nil
}

func (s *source) inspectUnmarkedVersion(ctx context.Context) error {
	var userVersion, base, extra, weapons, returns int
	if err := s.tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
		return err
	}
	q := `SELECT
		count(*) FILTER(WHERE type='table' AND name IN ('local_entry_accounts','local_entry_characters')),
		count(*) FILTER(WHERE name NOT GLOB 'sqlite_*' AND type IN ('view','trigger') OR name='local_weapon_assets'),
		count(*) FILTER(WHERE type='table' AND name='local_equipped_weapons')
		FROM main.sqlite_schema`
	if err := s.tx.QueryRowContext(ctx, q).Scan(&base, &extra, &weapons); err != nil {
		return err
	}
	if err := s.tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_xinfo('local_entry_characters')
		WHERE name IN ('return_town','return_area','return_x','return_y','return_direction')`).Scan(&returns); err != nil {
		return err
	}
	if userVersion != 0 || base != 2 || extra != 0 || (returns != 0 && returns != 5) || weapons != 0 && returns != 5 {
		return errors.New("unrecognized unversioned S9A1 local-entry schema")
	}
	version := 1
	if returns == 5 {
		version++
	}
	if weapons == 1 {
		version++
	}
	s.report.ModuleVersion, s.report.VersionInferred = version, true
	return nil
}

func (s *source) inspectTables(ctx context.Context) error {
	var err error
	for _, obj := range s.objects {
		if obj.Kind != "table" || (strings.HasPrefix(obj.Name, "sqlite_") && obj.Name != "sqlite_sequence") {
			continue
		}
		table := Table{Name: obj.Name}
		cols, err := s.tx.QueryContext(ctx, `SELECT name,type,pk FROM pragma_table_xinfo(?) ORDER BY cid`, obj.Name)
		if err != nil {
			return err
		}
		for cols.Next() {
			var c Column
			if err = cols.Scan(&c.Name, &c.Type, &c.Primary); err != nil {
				cols.Close()
				return err
			}
			table.Columns = append(table.Columns, c)
		}
		err = cols.Err()
		cols.Close()
		if err != nil {
			return err
		}
		digest := sha256.New()
		err = s.walk(ctx, table, func(payload []byte) error { hashRow(digest, payload); table.Rows++; return nil })
		if err != nil {
			return fmt.Errorf("reading %s: %w", obj.Name, err)
		}
		table.SHA256 = hex.EncodeToString(digest.Sum(nil))
		s.report.Tables = append(s.report.Tables, table)
	}
	identity, err := json.Marshal(struct {
		Objects []Object
		Tables  []Table
	}{s.objects, s.report.Tables})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(identity)
	s.report.SHA256 = hex.EncodeToString(digest[:])
	if s.report.ModuleVersion >= 5 {
		err = s.tx.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(gold),0) FROM local_entry_characters").Scan(&s.report.Characters, &s.report.Gold)
	} else {
		err = s.tx.QueryRowContext(ctx, "SELECT count(*) FROM local_entry_characters").Scan(&s.report.Characters)
	}
	if err != nil {
		return err
	}
	return s.activationIssues(ctx)
}

func hashRow(h hash.Hash, p []byte) {
	var size [8]byte
	binary.LittleEndian.PutUint64(size[:], uint64(len(p)))
	h.Write(size[:])
	h.Write(p)
}

func quote(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// Cells retain SQLite's actual storage class. Text with malformed UTF-8 and
// blobs retain their original bytes. Integers never pass through a JSON float.
type cell struct {
	Kind  string `json:"t"`
	Value string `json:"v,omitempty"`
}

func readCell(kind string, v any) (cell, error) {
	c := cell{Kind: kind}
	switch kind {
	case "null":
		return c, nil
	case "integer":
		if n, ok := v.(int64); ok {
			c.Value = strconv.FormatInt(n, 10)
			return c, nil
		}
	case "real":
		if n, ok := v.(float64); ok {
			c.Value = strconv.FormatUint(math.Float64bits(n), 16)
			return c, nil
		}
	case "text", "blob":
		buf, ok := v.([]byte)
		if !ok {
			break
		}
		if kind == "text" && utf8.Valid(buf) {
			c.Value = string(buf)
		} else {
			if kind == "text" {
				c.Kind = "text64"
			}
			c.Value = base64.StdEncoding.EncodeToString(buf)
		}
		return c, nil
	}
	return c, fmt.Errorf("unexpected SQLite %s value of type %T", kind, v)
}

func (s *source) walk(ctx context.Context, t Table, consume func([]byte) error) error {
	var fields, order []string
	pk := append([]Column(nil), t.Columns...)
	sort.SliceStable(pk, func(i, j int) bool { return pk[i].Primary < pk[j].Primary })
	for _, c := range pk {
		if c.Primary > 0 {
			order = append(order, quote(c.Name))
		}
	}
	if len(order) == 0 {
		order = []string{"rowid"}
	}
	for _, c := range t.Columns {
		n := quote(c.Name)
		fields = append(fields, "typeof("+n+")", "CASE WHEN typeof("+n+") IN ('text','blob') THEN CAST("+n+" AS BLOB) ELSE "+n+" END")
	}
	if len(fields) == 0 {
		return errors.New("table has no readable columns")
	}
	rows, err := s.tx.QueryContext(ctx, "SELECT "+strings.Join(fields, ",")+" FROM "+quote(t.Name)+" ORDER BY "+strings.Join(order, ","))
	if err != nil {
		return err
	}
	defer rows.Close()
	values := make([]any, len(t.Columns))
	types := make([]string, len(t.Columns))
	dest := make([]any, len(fields))
	cells := make([]cell, len(values))
	for i := range values {
		dest[2*i] = &types[i]
		dest[2*i+1] = &values[i]
	}
	for rows.Next() {
		if err = rows.Scan(dest...); err != nil {
			return err
		}
		for i, val := range values {
			cells[i], err = readCell(types[i], val)
			if err != nil {
				return fmt.Errorf("%s: %w", t.Columns[i].Name, err)
			}
		}
		payload, err := json.Marshal(cells)
		if err != nil {
			return err
		}
		if err = consume(payload); err != nil {
			return err
		}
	}
	return rows.Err()
}
