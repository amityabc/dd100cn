package legacy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"s9a1/server/internal/storage"
)

const stagingDDL = `
	CREATE SCHEMA IF NOT EXISTS s9a1_import;
	CREATE TABLE IF NOT EXISTS s9a1_import.snapshots(
		digest text PRIMARY KEY, target text NOT NULL, module_version integer NOT NULL,
		schema_objects json NOT NULL, table_manifest json NOT NULL, active boolean NOT NULL DEFAULT false);
	CREATE TABLE IF NOT EXISTS s9a1_import.saved_rows(
		digest text NOT NULL REFERENCES s9a1_import.snapshots(digest), table_name text NOT NULL,
		row_no bigint NOT NULL, cells text NOT NULL, PRIMARY KEY(digest,table_name,row_no));`

// Import preserves each original row in PostgreSQL staging. Activation is a
// separate atomic transaction and never erases or merges an existing account.
func Import(ctx context.Context, path, dsn, target string, activate bool) (Report, error) {
	return ImportWithUpgrade(ctx, path, dsn, target, activate, UpgradeOptions{})
}

func ImportWithUpgrade(ctx context.Context, path, dsn, target string, activate bool, options UpgradeOptions) (Report, error) {
	if strings.TrimSpace(target) == "" || dsn == "" {
		return Report{}, errors.New("an explicit target account and a PostgreSQL DSN are required")
	}
	s, err := openSource(ctx, path)
	if err != nil {
		return Report{}, err
	}
	defer s.close()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return s.report, err
	}
	defer db.Close()
	if err = s.stage(ctx, db, target); err != nil {
		return s.report, err
	}
	if activate && !s.report.Activated {
		install := s
		if s.report.ModuleVersion < latestLocalEntry {
			frozen, cleanup, err := s.upgrade(ctx, options)
			if err != nil {
				s.report.ActivationIssues = []string{err.Error()}
				return s.report, fmt.Errorf("snapshot was staged; private SQLite upgrade failed: %w", err)
			}
			defer cleanup()
			install = frozen
			s.report.ActivationVersion = frozen.report.ModuleVersion
			s.report.ActivationSHA256 = frozen.report.SHA256
			s.report.ActivationIssues = frozen.report.ActivationIssues
		}
		if len(install.report.ActivationIssues) > 0 {
			return s.report, errors.New("snapshot was staged; live activation is blocked by the listed unported state")
		}
		service, err := storage.Open(ctx, dsn)
		if err != nil {
			return s.report, err
		}
		err = service.Initialize(ctx)
		service.Close()
		if err != nil {
			return s.report, err
		}
		if err = install.activate(ctx, db, target, s.report.SHA256); err != nil {
			return s.report, err
		}
		s.report.Activated = install.report.Activated
	}
	return s.report, err
}

func (s *source) stage(ctx context.Context, db *sql.DB, target string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(1395736881,2)"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, stagingDDL); err != nil {
		return err
	}
	objects, err := json.Marshal(s.objects)
	if err != nil {
		return err
	}
	manifest, err := json.Marshal(s.report.Tables)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO s9a1_import.snapshots(digest,target,module_version,schema_objects,table_manifest)
		VALUES ($1,$2,$3,$4::json,$5::json) ON CONFLICT(digest) DO NOTHING`, s.report.SHA256, target, s.report.ModuleVersion, string(objects), string(manifest))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var previous string
		var active bool
		if err = tx.QueryRowContext(ctx, "SELECT target,active FROM s9a1_import.snapshots WHERE digest=$1", s.report.SHA256).Scan(&previous, &active); err != nil {
			return err
		}
		if previous != target {
			return errors.New("this exact snapshot is already mapped to a different account")
		}
		s.report.Activated = active
	} else {
		for _, t := range s.report.Tables {
			if err = s.stageTable(ctx, tx, t); err != nil {
				return fmt.Errorf("stage %s: %w", t.Name, err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("staging commit: %w; retry with the same account and source", err)
	}
	s.report.Staged = true
	return nil
}

func (s *source) stageTable(ctx context.Context, tx *sql.Tx, t Table) error {
	const batchRows = 128
	var done int64
	batch := make([]string, 0, batchRows)
	bytesInBatch := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO s9a1_import.saved_rows
			SELECT $1,$2,$3::bigint+r.n,r.v FROM unnest($4::text[]) WITH ORDINALITY AS r(v,n)`,
			s.report.SHA256, t.Name, done, batch)
		if err == nil {
			done += int64(len(batch))
			batch = batch[:0]
			bytesInBatch = 0
		}
		return err
	}
	err := s.walk(ctx, t, func(payload []byte) error {
		batch = append(batch, string(payload))
		bytesInBatch += len(payload)
		if len(batch) >= batchRows || bytesInBatch >= 1<<20 {
			return flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err = flush(); err != nil {
		return err
	}
	if done != t.Rows {
		return errors.New("SQLite snapshot row count changed")
	}
	rows, err := tx.QueryContext(ctx, `SELECT cells FROM s9a1_import.saved_rows WHERE digest=$1 AND table_name=$2 ORDER BY row_no`, s.report.SHA256, t.Name)
	if err != nil {
		return err
	}
	defer rows.Close()
	h := sha256.New()
	var n int64
	for rows.Next() {
		var payload []byte
		if err = rows.Scan(&payload); err != nil {
			return err
		}
		hashRow(h, payload)
		n++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if n != t.Rows || hex.EncodeToString(h.Sum(nil)) != t.SHA256 {
		return errors.New("PostgreSQL read-back differs from the source")
	}
	return nil
}
