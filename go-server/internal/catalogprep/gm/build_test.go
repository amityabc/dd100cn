package gm

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"s9a1/server/internal/resources"
)

type testArchive map[string]string

func (a testArchive) Read(path string, max int) ([]byte, error) {
	text, err := a.ReadText(path, max)
	return []byte(text), err
}

func (a testArchive) ReadText(path string, max int) (string, error) {
	text, found := a[path]
	if !found {
		return "", fs.ErrNotExist
	}
	return text, nil
}

func (a testArchive) ReadIndex(category string, _ ...int) (map[int64]string, error) {
	if category == "equipment" {
		return map[int64]string{1000: "equipment/hat.equ"}, nil
	}
	return map[int64]string{1001: "stackable/material.stk", 1002: "stackable/retired.stk"}, nil
}

func (a testArchive) LocalizedText(value string) (string, error) {
	if value == "<1000::hat_name>" {
		return "装扮名称", nil
	}
	return value, nil
}

func (a testArchive) ResolveString(id int32) (string, error) {
	return "", fmt.Errorf("unexpected weapon token %d", id)
}

func TestGMBuildKeepsDisplayAndGrantDecisions(t *testing.T) {
	const hat = "[name]\n`<1000::hat_name>`\n[equipment type]\n`[hat avatar]` 0\n[item category]\n`clear avatar`" +
		"\n[emblem socket default]\n`[A socket]` `[B socket]`\n[/emblem socket default]\n[attach type]\n`[avatar trade]`"
	const material = "[name]\n`材料\n[weight]\n显示文字`\n[stackable type]\n`[material]` 0\n[usable job]\n`[all]`\n[/usable job]\n[weight]\n5\n[weight]\n0"
	a := testArchive{"equipment/hat.equ": hat, "stackable/material.stk": material}
	hash := strings.Repeat("1", 64)
	path := filepath.Join(t.TempDir(), "items.zip")
	stats, err := BuildArchive(context.Background(), a, Metadata{hash, hash, hash, true}, path)
	if err != nil {
		t.Fatal(err)
	}
	if stats != (Stats{Items: 3, Definitions: 3, Ordinary: 1, Special: 1, Unavailable: 1, Named: 2, Weighed: 1, Clones: 1}) {
		t.Fatalf("unexpected build summary %+v", stats)
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	read := func(member string, into any) {
		t.Helper()
		f, err := z.Open(member)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := json.NewDecoder(f).Decode(into); err != nil {
			t.Fatal(err)
		}
	}
	var index struct {
		Items []struct {
			Id                      uint32
			Kind, Group, Path, Name string
		}
	}
	read("item-index.json", &index)
	if len(index.Items) != 3 || index.Items[0].Name != "装扮名称" || index.Items[0].Group != "装备" ||
		index.Items[1].Name != "材料\n[weight]\n显示文字" || index.Items[2].Name != "" {
		t.Fatalf("display rows %+v", index.Items)
	}
	var special map[uint32]itemRow
	read("e8.json", &special)
	product := special[1000].Special
	if product == nil || product.Template.InitialAvatarSocketCount != 2 || product.Template.InitialAvatarSocketLayout == nil ||
		*product.Template.InitialAvatarSocketLayout != (socketLayout{First: 1, Second: 2}) || product.AttachType != 1 {
		t.Fatalf("avatar creation row %+v", product)
	}
	var weights map[uint32]int64
	read("weights.json", &weights)
	if value, exists := weights[1001]; !exists || value != 0 {
		t.Fatalf("resource-defined zero weight not preserved: %+v", weights)
	}
	var clones map[uint32]AvatarClone
	read("avatar-clones.json", &clones)
	if clone := clones[1000]; clone.Id != 1000 || clone.Hash != product.Template.ResourceHash || !clone.IsClone {
		t.Fatalf("avatar clone %+v", clone)
	}
	base := resources.GmItems{}
	prepared := base
	for _, entry := range []struct {
		id        uint32
		path      string
		equipment bool
	}{{1000, "equipment/hat.equ", true}, {1001, "stackable/material.stk", false}} {
		source, err := ReadItemSource(a, entry.id, entry.path, entry.equipment)
		if err != nil {
			t.Fatal(err)
		}
		grant, err := PrepareGrant(a, source)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err = prepared.WithGrant(entry.id, grant)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := base.Grant(1000); err == nil {
		t.Fatal("publishing a grant mutated the earlier view")
	}
	if got, err := prepared.Grant(1000); err != nil || got.Special == nil || got.Special.AttachType != product.AttachType {
		t.Fatalf("live special grant %+v, %v", got, err)
	}
	if got, err := prepared.Grant(1001); err != nil || got.Weight == nil || *got.Weight != 0 || len(got.Template) == 0 {
		t.Fatalf("live ordinary grant %+v, %v", got, err)
	}
}
