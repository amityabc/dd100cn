package pvf

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestProjectClient100Archive(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	path := filepath.Join(root, "客户端", "Script.pvf")
	profile := filepath.Join(root, "s9a1", "Server", "Data", "Pvf", "Client100Profile.json")
	for _, filename := range []string{path, filepath.Join(filepath.Dir(path), "sk.dat"), profile} {
		if _, err := os.Stat(filename); errors.Is(err, os.ErrNotExist) {
			t.Skip("project Client100 resources are unavailable")
		} else if err != nil {
			t.Fatal(err)
		}
	}
	a, err := Open(path, profile)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Format() != "client100_sk_nkpi" {
		t.Fatalf("format = %q", a.Format())
	}
	equipment, err := a.ReadIndex("equipment")
	if err != nil {
		t.Fatal(err)
	}
	stackable, err := a.ReadIndex("stackable")
	if err != nil {
		t.Fatal(err)
	}
	item := stackable[4365]
	if len(equipment) != 212485 || len(stackable) != 80410 || item != "stackable/quest/quest_4365.stk" {
		t.Fatalf("incomplete indexes: equipment=%d stackable=%d item=%q", len(equipment), len(stackable), item)
	}
	content, err := a.ReadText(item, 1<<20)
	if err != nil {
		t.Fatalf("read indexed item: %v: %q", err, content)
	}
	checkTextHash(t, "localized item", content, "93E78D5475F9AAA2C7E30F26FB47868161435CAA94CD160DA3F63A1CB2C7FC13")
	exp, err := a.ExperienceThresholds(1 << 20)
	if err != nil || len(exp) != 150 || exp[99] <= exp[0] {
		t.Fatalf("experience table: len=%d err=%v", len(exp), err)
	}
	thresholds := make([]string, len(exp))
	for i, threshold := range exp {
		thresholds[i] = strconv.FormatInt(threshold, 10)
	}
	checkTextHash(t, "experience thresholds", strings.Join(thresholds, ","), "74AE5B5B0205220347534AAFC12EF2CED77EFD334AED9B761FCF485E76545751")
	portals, err := a.ReadFatePortalIndex()
	if err != nil || len(portals) != 3 {
		t.Fatalf("activity portal index: %v: %v", portals, err)
	}
	ability, err := a.ReadFateAbilityText()
	if err != nil || len(ability) == 0 {
		t.Fatalf("activity ability text: size=%d error=%v", len(ability), err)
	}
	checkTextHash(t, "Fate ability", ability, "E3459F609A0F8B90EE8FA67A59D7EB27A5178C403A9EF07E563C4EE631E6F53D")
	shop, err := a.ReadCommerceText("etc/(r)cerashop.etc")
	if err != nil {
		t.Fatal(err)
	}
	checkTextHash(t, "Cera shop", shop, "7D9E99B4857A27C68D734AADB61A32E627A9BDB6C1D43F60373C81E8B27FC81B")
	fateRaw, err := a.ReadFateAbility()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.Read(item, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			got, err := a.Read(`\\ETC\\../`+strings.ToUpper(item), 1<<20)
			if err != nil || !bytes.Equal(got, raw) {
				t.Errorf("concurrent equivalent archive path: %v", err)
			} else {
				got[0] ^= 255
			}
		})
	}
	readers.Wait()
	got, err := a.Read(item, 1<<20)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("Read returned shared data: %v", err)
	}
	t.Logf("equipment=%d stackable=%d item=%q portal=%d ability=%x exp[0]=%d exp[99]=%d", len(equipment), len(stackable), item, len(portals), sha256.Sum256(fateRaw), exp[0], exp[99])
}

// These expected identities are from the legacy PvfResourceProvider reading
// the same project archive independently.
func checkTextHash(t *testing.T, label, content, want string) {
	t.Helper()
	got := fmt.Sprintf("%X", sha256.Sum256([]byte(content)))
	if got != want {
		t.Errorf("%s: SHA-256 = %s, want %s", label, got, want)
	}
}
