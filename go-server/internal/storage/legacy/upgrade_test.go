package legacy

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func historicalFixture(t *testing.T) (string, string, *sql.DB) {
	t.Helper()
	project, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := privateUpgradeDir(project)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "historical.sqlite3")
	db, err := sql.Open("sqlite", sqliteURI(path, "mode=rwc"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	rows, err := os.ReadFile(filepath.Join("testdata", "v3-upgrade-fixture.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; " + string(rows)); err != nil {
		t.Fatal(err)
	}
	// The WAL is still open. No file copy of the main database alone sees the schema.
	if _, err = db.Exec("UPDATE local_entry_accounts SET cera=500,epoch=7 WHERE account='local'"); err != nil {
		t.Fatal(err)
	}
	return project, path, db
}

func TestLegacySnapshotBackupSharesPinnedWALView(t *testing.T) {
	_, path, writer := historicalFixture(t)
	ctx := t.Context()
	s, err := openSource(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	if s.report.ModuleVersion != 3 || !s.report.VersionInferred || s.report.Characters != 1 {
		t.Fatalf("historical snapshot: %+v", s.report)
	}
	if _, err = writer.Exec("UPDATE local_entry_accounts SET cera=700 WHERE account='local'"); err != nil {
		t.Fatal(err)
	}
	newer, err := Inspect(ctx, path)
	if err != nil || newer.SHA256 == s.report.SHA256 {
		t.Fatalf("new WAL commit was not distinct: %+v %v", newer, err)
	}
	copyPath := filepath.Join(filepath.Dir(path), "copy.sqlite3")
	if err = s.copySnapshot(ctx, copyPath); err != nil {
		t.Fatal(err)
	}
	copied, err := Inspect(ctx, copyPath)
	if err != nil || copied.SHA256 != s.report.SHA256 {
		t.Fatalf("backup does not match the pinned original view: %+v %v", copied, err)
	}
}

func TestLegacyOfflineUpgradePreservesWornWeaponAndDeletedIdentity(t *testing.T) {
	project, path, writer := historicalFixture(t)
	dotnet := os.Getenv("S9A1_DOTNET")
	if dotnet == "" {
		t.Skip("set S9A1_DOTNET and build tools/build-save-upgrade.ps1 for the offline C# fixture")
	}
	assembly := filepath.Join(project, ".work", "save-upgrade-build", "bin", "LegacyUpgrade", "release", "LegacyUpgrade.dll")
	if _, err := os.Stat(assembly); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec("CREATE TABLE next_schema_metadata(baseline TEXT PRIMARY KEY,version INTEGER NOT NULL CHECK(version>=1)); " +
		"CREATE TABLE next_schema_modules(module TEXT PRIMARY KEY,version INTEGER NOT NULL CHECK(version>=1));" +
		"INSERT INTO next_schema_metadata VALUES('serve100-next',1); INSERT INTO next_schema_modules VALUES('local-entry',3); PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	s, err := openSource(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	oldDigest := s.report.SHA256
	install, cleanup, err := s.upgrade(ctx, UpgradeOptions{ProjectDir: project, Dotnet: dotnet})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if install.report.ModuleVersion != 35 || install.report.SHA256 == oldDigest || install.report.Characters != 1 ||
		len(install.report.ActivationIssues) != 0 {
		t.Fatalf("upgraded snapshot: %+v", install.report)
	}
	var uid string
	var raw []byte
	if err := install.tx.QueryRowContext(ctx,
		"SELECT instance_id,raw119 FROM local_weapon_assets WHERE character_id=200 AND location=3 AND slot=12").Scan(&uid, &raw); err != nil {
		t.Fatal(err)
	}
	if uid != "ffeeddccbbaa99887766554433221100" || len(raw) != 119 {
		t.Fatalf("upgraded weapon identity or core length: %s %d", uid, len(raw))
	}
	want := make([]byte, 119)
	binary.LittleEndian.PutUint16(want, 12)
	binary.LittleEndian.PutUint32(want[2:], 287454020)
	binary.LittleEndian.PutUint32(want[6:], 19088743)
	binary.LittleEndian.PutUint16(want[11:], 45)
	if !bytes.Equal(want, raw) {
		t.Fatalf("upgraded native core: got %x, want %x", raw[:15], want[:15])
	}
	var high, cera int
	if err := install.tx.QueryRowContext(ctx, "SELECT seq FROM sqlite_sequence WHERE name='local_entry_characters'").Scan(&high); err != nil {
		t.Fatal(err)
	}
	if err := install.tx.QueryRowContext(ctx, "SELECT cera FROM local_entry_accounts WHERE account='local'").Scan(&cera); err != nil {
		t.Fatal(err)
	}
	if high != 273 || cera != 500 {
		t.Fatalf("allocator or wallet changed: high %d cera %d", high, cera)
	}
	original, err := Inspect(ctx, path)
	if err != nil || original.ModuleVersion != 3 || original.SHA256 != oldDigest {
		t.Fatalf("original source was modified: %+v %v", original, err)
	}

	// Rebuild the exact v34 boundary by reversing its one-column successor.
	// This checks the v34 entry point with nonzero wallets and a populated
	// split receipt row, independently of the empty defaults from v3.
	t.Run("v34 populated receipt", func(t *testing.T) {
		v34 := filepath.Join(filepath.Dir(path), "v34.sqlite3")
		if err := install.copySnapshot(ctx, v34); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", sqliteURI(v34, "mode=rw"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		frozen := `{"fixture:1":{"Key":"fixture:1","Input":"old argument","Result":"old result","Revision":7,"Grants":[],"Future":18446744073709551615}}`
		if _, err = db.Exec("ALTER TABLE local_entry_accounts DROP COLUMN hotkeys; UPDATE next_schema_modules SET version=34 WHERE module='local-entry'; UPDATE local_entry_characters SET gold=917 WHERE id=200"); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("UPDATE local_character_receipts SET receipts_json=? WHERE character_id=200", frozen); err != nil {
			t.Fatal(err)
		}
		latest, err := openSource(ctx, v34)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(latest.close)
		if latest.report.ModuleVersion != 34 || latest.report.Gold != 917 {
			t.Fatalf("source v34: %+v", latest.report)
		}
		upgraded, cleanup, err := latest.upgrade(ctx, UpgradeOptions{ProjectDir: project, Dotnet: dotnet})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		if upgraded.report.Gold != 917 || upgraded.report.ModuleVersion != 35 || len(upgraded.report.ActivationIssues) > 0 {
			t.Fatalf("upgraded v34: %+v", upgraded.report)
		}
		var actual string
		if err := upgraded.tx.QueryRowContext(ctx, "SELECT receipts_json FROM local_character_receipts WHERE character_id=200").Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != frozen || !json.Valid([]byte(actual)) {
			t.Fatalf("upgraded v34 receipt changed: %q", actual)
		}
	})
}
