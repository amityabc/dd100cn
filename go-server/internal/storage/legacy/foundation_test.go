package legacy

import (
	"compress/gzip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"s9a1/server/internal/storage"
)

// v35-official-release is the unmodified output of ReleaseDatabaseSeed.CreateAsync.
func TestOfficialReleaseSave(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "v35-official-release.sqlite3.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	content, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	dsn := os.Getenv("S9A1_RELEASE_TEST_DATABASE_URL")
	for i, account := range []string{"release-original", "release-played"} {
		path := filepath.Join(t.TempDir(), "next-local.db")
		if err = os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		source, err := sql.Open("sqlite", sqliteURI(path, "mode=rw"))
		if err != nil {
			t.Fatal(err)
		}
		cera := int64(storage.ReleaseSeedCera)
		if i == 1 {
			cera = 830102
			if _, err = source.Exec(`UPDATE local_entry_accounts SET cera=? WHERE account='local'`, cera); err != nil {
				t.Fatal(err)
			}
			if _, err = source.Exec(`INSERT INTO foundation_state(key,value) VALUES (?,?)`, "已读", "A\x00B"); err != nil {
				t.Fatal(err)
			}
		}
		var receipt string
		if err = source.QueryRow(`SELECT value FROM foundation_state WHERE key=?`, storage.ReleaseSeedPolicy).Scan(&receipt); err != nil {
			t.Fatal(err)
		}
		if err = source.Close(); err != nil {
			t.Fatal(err)
		}
		report, err := Inspect(ctx, path)
		if err != nil || len(report.ActivationIssues) != 0 || report.ModuleVersion != 35 || report.Characters != 0 {
			t.Fatalf("%s inspection: %+v, %v", account, report.ActivationIssues, err)
		}
		if dsn == "" {
			continue
		}
		for attempt := 0; attempt < 2; attempt++ {
			result, err := Import(ctx, path, dsn, account, true)
			if err != nil || !result.Staged || !result.Activated {
				t.Fatalf("%s import %d: %v", account, attempt+1, err)
			}
		}
		db, err := storage.Open(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		status, ok, err := db.ReleaseSeedStatus(ctx, account)
		if err != nil || !ok || status.Cera != storage.ReleaseSeedCera || status.Characters != 0 || status.Value != receipt || status.SourceDigest != report.SHA256 {
			t.Fatalf("%s issue receipt differs: %+v, %v", account, status, err)
		}
		v, ok, err := db.FoundationState(ctx, account, "已读")
		if err != nil || ok != (i == 1) || ok && (v.Value != "A\x00B" || v.SourceDigest != report.SHA256) {
			t.Fatalf("%s foundation state differs: %+v, %v", account, v, err)
		}
		lease, err := db.Acquire(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		roster, err := db.List(ctx, lease)
		if err != nil || roster.Cera != cera || len(roster.Characters) != 0 {
			t.Fatalf("%s live account does not match its snapshot: %v", account, err)
		}
		if err = db.Release(ctx, lease); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
}
