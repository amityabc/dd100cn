package resources

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestGMIndexCacheRejectsMissingDisplayField(t *testing.T) {
	dir := t.TempDir()
	pvf, sidecar := []byte("test pvf"), []byte("test sidecar")
	pvfPath := filepath.Join(dir, "Script.pvf")
	cache := filepath.Join(dir, "item-index.json")
	if err := os.WriteFile(pvfPath, pvf, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sk.dat"), sidecar, 0600); err != nil {
		t.Fatal(err)
	}
	const parser = "3d737f2f-1db2-455f-9bb0-577a721e6f50"
	const profile = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
	const pack = "characters.gm.zip"
	source := fmt.Sprintf("%X", sha256.Sum256(pvf))
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	write := func(name string, body any) {
		t.Helper()
		w, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.NewEncoder(w).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", map[string]any{
		"Schema": 1, "PvfSha256": source, "ProfileSha256": profile, "SidecarSha256": fmt.Sprintf("%X", sha256.Sum256(sidecar)),
		"Parser": parser, "Unprofiled": true,
	})
	write("item-index.json", gmIndexSnapshot{1, source, profile, parser, []GmCatalogItem{{10312874, "stackable", "道具 / 材料", "stackable/wisdom.stk", "时间引导石"}}})
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	packPath := filepath.Join(dir, pack)
	if err := os.WriteFile(packPath, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	load := func(hit bool) {
		t.Helper()
		got, err := LoadGMIndex(packPath, pvfPath, "", cache)
		if err != nil || got.CacheHit != hit || len(got.Rows) != 1 || got.Rows[0].Name != "时间引导石" || got.Hash != source {
			t.Fatalf("index = %+v, %v", got, err)
		}
	}
	load(false)
	load(true)
	raw, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"Name":"时间引导石"`), []byte(`"Name":null`), 1)
	if err := os.WriteFile(cache, raw, 0600); err != nil {
		t.Fatal(err)
	}
	load(false)
	load(true)
}
