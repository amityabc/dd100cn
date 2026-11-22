package legacy_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"s9a1/server/internal/storage"
)

// S9A1_IMPORTED_TEST_DATABASE_URL is the isolated copy activated from
// testdata/v35-ordinary-fixture.sql under its documented fixture account.
func TestImportedOrdinaryAssets(t *testing.T) {
	dsn := os.Getenv("S9A1_IMPORTED_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set S9A1_IMPORTED_TEST_DATABASE_URL for the activated original-weapon fixture")
	}
	ctx := t.Context()
	s, err := storage.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	l, err := s.Acquire(ctx, "ordinary-v35")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Release(ctx, l)
	r, err := s.List(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Characters) != 1 || r.Cera != 51 || r.AssetRevision != 3 || r.ConquerorExpiresAt != 2000000001 || r.SkillMasterExpiresAt != 2000000002 ||
		r.CrystalExpiresAt != 2000000003 || r.GrowthExpiresAt != 2000000004 {
		t.Fatal("wrong restored account")
	}
	c, err := s.Select(ctx, l, 0)
	if err != nil || c.ID != 1 || len(c.Worn) != 1 || len(r.Characters[0].Worn) != 1 {
		t.Fatalf("worn roster and selection: %v %v", c.Worn, err)
	}
	w := c.Worn[0]
	if w.Slot != 12 || w.TemplateID != 0x11223344 || w.QualitySeed != 0x01234567 || w.Durability != 45 || len(w.Core) != 119 {
		t.Fatalf("wrong native weapon: %+v", w)
	}
	i, err := s.Inventory(ctx, l)
	if err != nil || len(i.Items) != 1 || i.Gold != 19 || i.Coins != 10 || i.CargoCapacity != 8 || i.PersonalCapacity != 152 {
		t.Fatalf("inventory: %+v %v", i, err)
	}
	a := i.Items[0]
	if a.UID != "112233445566778899aabbccddeeff00" || a.CharacterID != c.ID || a.Weapon == nil || a.Weapon.QualitySeed != w.QualitySeed || !bytes.Equal(a.Core, w.Core) {
		t.Fatal("inventory identity, frozen facts or native bytes differ")
	}
	p, ok, err := s.Receipt(ctx, l, "storage-fixture:1")
	var receipt struct {
		Input, Result string
		Future        struct{ Raw uint64 }
	}
	if err != nil || !ok || json.Unmarshal(p, &receipt) != nil || receipt.Input != "old argument" || receipt.Result != "old result" || receipt.Future.Raw != ^uint64(0) {
		t.Fatalf("old receipt: %s %v", p, err)
	}
}
