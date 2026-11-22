package inventorywire

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"s9a1/server/internal/storage"
)

func TestImportedOrdinaryWeapon(t *testing.T) {
	file := os.Getenv("S9A1_INVENTORY_FIXTURE_DSN_FILE")
	if file == "" {
		t.Skip("requires the isolated ordinary-v35 PostgreSQL fixture")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	dsn := strings.TrimSpace(string(raw))
	if !strings.Contains(dsn, "/s9a1_equipped_fixture") {
		t.Fatal("this test requires the dedicated equipped fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	db, err := storage.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	lease, err := db.Acquire(ctx, "ordinary-v35")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Release(ctx, lease)
	if _, err = db.List(ctx, lease); err != nil {
		t.Fatal(err)
	}
	character, err := db.Select(ctx, lease, 0)
	if err != nil {
		t.Fatal(err)
	}
	in, err := db.Inventory(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	regular, _, err := Town(in, character, Options{})
	if err != nil {
		t.Fatal(err)
	}
	core := make([]byte, 119)
	le.PutUint16(core, 12)
	le.PutUint32(core[2:], 0x11223344)
	le.PutUint32(core[6:], 0x01234567)
	le.PutUint16(core[11:], 45)
	if in.Gold != 19 || in.Coins != 10 || in.Cera != 51 || len(regular) < 3 || regular[1].Opcode != 14 || !bytes.Equal(regular[1].Body[3:122], core) {
		t.Fatal("imported inventory or native worn core differs from its v35 source")
	}
	t.Log("v35 imported slot 12 equals all 119 original persisted bytes, with 19 gold, 10 coins and 51 Cera")
}
