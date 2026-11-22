package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"s9a1/server/internal/config"
)

func TestGMIndexUsesMatchedSourceOrInstalledCatalog(t *testing.T) {
	t.Setenv("S9A1_CREATION_CATALOG", "")
	base := t.TempDir()
	project := filepath.Join(base, "go-server")
	root := filepath.Join(base, "s9a1", "Server")
	catalog := filepath.Join(project, ".work", "catalog", "characters.json")
	put := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(project, "go.mod"))
	put(gmIndexPackPath(catalog))
	c := config.Config{Root: root}
	if got := runtimeCatalogPath(c, true); got != catalog {
		t.Fatalf("source GM resource = %s", got)
	}
	put(filepath.Join(filepath.Dir(root), "release-manifest.json"))
	if got := runtimeCatalogPath(c, true); got != c.CatalogPath() {
		t.Fatalf("release used an adjacent source resource: %s", got)
	}
	c.Storage.Database = "runtime/data/next-local.db"
	if got, err := gmIndexDatabasePath(c); err != nil || got != c.Path(c.Storage.Database) {
		t.Fatalf("valid database path %s: %v", got, err)
	}
	c.Storage.Database = "runtime/../other.db"
	if _, err := gmIndexDatabasePath(c); err == nil {
		t.Fatal("legacy statistics accepted a database path outside this runtime")
	}
}

func TestGMIndexStillReportsResourcesWithMissingSaveConnection(t *testing.T) {
	t.Setenv("S9A1_POSTGRES_DSN", "")
	path := t.TempDir()
	client, root := filepath.Join(path, "Client"), filepath.Join(path, "Server")
	catalog := filepath.Join(path, "characters.json")
	t.Setenv("S9A1_CREATION_CATALOG", catalog)
	put := func(path, data string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%X", sha256.Sum256([]byte(data)))
	}
	pvf := put(filepath.Join(client, "Script.pvf"), "PVF fixture")
	sidecar := put(filepath.Join(client, "sk.dat"), "sidecar fixture")
	profile := put(filepath.Join(client, "Client100Profile.json"), `{}`)
	put(filepath.Join(client, "ImagePacks2", "a.npk"), "image fixture")
	put(filepath.Join(root, "config", "server.json"), `{"Storage":{"Database":"runtime/data/next-local.db"}}`)
	put(filepath.Join(root, "runtime", "postgres-dsn.txt"), "")
	archive, err := os.Create(gmIndexPackPath(catalog))
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(archive)
	parser := "e6e98e0d-26d4-4505-8bd7-30bb9fae4625"
	for name, data := range map[string]string{
		"manifest.json":   fmt.Sprintf(`{"Schema":1,"PvfSha256":%q,"ProfileSha256":%q,"SidecarSha256":%q,"Parser":%q}`, pvf, profile, sidecar, parser),
		"item-index.json": fmt.Sprintf(`{"Version":1,"Source":%q,"Profile":%q,"Parser":%q,"Items":[{"Id":17,"Kind":"stackable","Group":"etcs","Path":"etc.stk","Name":"示例"}]}`, pvf, profile, parser),
	} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(path, "GM", "index.json")
	if err := gmIndex([]string{"--gm-index", "--client", client, "--output", output, "--server-root", root}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Ready    bool
		Items    []indexItem
		Database struct{ Characters json.RawMessage }
	}
	if json.Unmarshal(data, &result) != nil || !result.Ready || len(result.Items) != 1 || result.Items[0].Id != 17 || string(result.Database.Characters) != "[]" {
		t.Fatalf("missing resource or no-save report: %s", data)
	}
}
