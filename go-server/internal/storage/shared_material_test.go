package storage

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPostgreSQLSharedMaterialImport(t *testing.T) {
	ctx, s, account := testStore(t)
	l, err := s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.List(ctx, l); err != nil {
		t.Fatal(err)
	}
	create := CreateCharacter{Name: account, Stats: []byte(`{}`), Gameplay: []byte(`{"SharedMaterialImports":{},"Next":18446744073709551615}`), ResourceHash: strings.Repeat("A", 64)}
	c, err := s.Create(ctx, l, create)
	if err != nil {
		t.Fatal(err)
	}
	create.Name += "b"
	other, err := s.Create(ctx, l, create)
	if err != nil {
		t.Fatal(err)
	}
	if c, err = s.Select(ctx, l, 0); err != nil {
		t.Fatal(err)
	}
	var salt [4]byte
	if _, err = rand.Read(salt[:]); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/legacy-shared-material.json")
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte("30000000-0000-"), []byte(hex.EncodeToString(salt[:])+"-0000-"))
	type legacyItem struct {
		InstanceId           string
		Template, Attributes json.RawMessage
		Count                uint32
		Location             uint8
		Slot                 uint16
		Raw119, Detail       []byte
	}
	var gold struct {
		Items, After []legacyItem
		History      json.RawMessage
	}
	if err = json.Unmarshal(data, &gold); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE s9a1.accounts SET cargo_capacity=2,asset_revision=7 WHERE account=$1", account); err != nil {
		t.Fatal(err)
	}
	seed := func(a legacyItem) {
		t.Helper()
		var owner any = c.ID
		if a.Location == 12 {
			owner = nil
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO s9a1.assets(instance_id,account,character_id,source,template_id,template_json,count,location,slot,attributes_json,core,detail)
			VALUES ($1,$2,$3,'item',3033,$4::json,$5,$6,$7,$8::json,$9,$10)`, strings.ReplaceAll(a.InstanceId, "-", ""), account, owner, string(a.Template), a.Count, a.Location, a.Slot, string(a.Attributes), a.Raw119, a.Detail)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range gold.Items {
		seed(a)
	}
	before := c
	c, in, err := s.PrepareTownInventory(ctx, l, c.ID, c.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if c.Revision != before.Revision+1 || in.AssetRevision != 8 || in.Revision != c.Revision || len(in.Items) != len(gold.After) {
		t.Fatalf("wrong import result: rev %d shared revision %d items %d", c.Revision, in.AssetRevision, len(in.Items))
	}
	for _, want := range gold.After {
		a := item(in, strings.ReplaceAll(want.InstanceId, "-", ""))
		if a == nil || a.Count != want.Count || a.Slot != want.Slot || a.Location != want.Location || !bytes.Equal(a.Core, want.Raw119) || !bytes.Equal(a.Detail, want.Detail) || !equalJSON(a.Template, want.Template) || !equalJSON(a.Attributes, want.Attributes) {
			t.Fatalf("asset differs from old C# import for %s", want.InstanceId)
		}
	}
	var profile struct{ SharedMaterialImports, Next json.RawMessage }
	if json.Unmarshal(c.Gameplay, &profile) != nil || !equalJSON(gold.History, profile.SharedMaterialImports) || string(profile.Next) != "18446744073709551615" {
		t.Fatal("retirement history differs from C# or unrelated gameplay changed")
	}
	if v, i, err := s.PrepareTownInventory(ctx, l, c.ID, before.Revision); err != nil || v.Revision != c.Revision || i.AssetRevision != 8 {
		t.Fatalf("replay: %v", err)
	}
	newLease, err := s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.PrepareTownInventory(ctx, l, c.ID, c.Revision); !errors.Is(err, ErrLease) {
		t.Fatalf("old lease: %v", err)
	}
	other, err = s.Select(ctx, newLease, 1)
	if err != nil {
		t.Fatal(err)
	}
	v, otherItems, err := s.PrepareTownInventory(ctx, newLease, other.ID, other.Revision)
	if err != nil || len(otherItems.Items) != 1 || otherItems.Items[0].Source != "shared" || otherItems.Items[0].Slot != 363 || otherItems.Items[0].Count != 15 || otherItems.AssetRevision != 8 || v.Revision != other.Revision {
		t.Fatalf("other character shared view: %v", err)
	}
	if err = s.ReturnToList(ctx, newLease); err != nil {
		t.Fatal(err)
	}
	c, err = s.Select(ctx, newLease, 0)
	if err != nil {
		t.Fatal(err)
	}
	seed(gold.Items[2]) // A retired UID reappears in the former personal cargo.
	if _, _, err = s.PrepareTownInventory(ctx, newLease, c.ID, c.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused retired UID: %v", err)
	}
	r, err := s.Snapshot(ctx, newLease)
	if err != nil || r.Phase != Selected || r.AssetRevision != 8 || r.Characters[0].Revision != c.Revision {
		t.Fatalf("failed import committed: %v", err)
	}
}
