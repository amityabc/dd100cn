package catalogprep

import (
	"bytes"
	"os"
	"testing"

	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

func TestOwnedEquipmentReusesAndRebasesQuotes(t *testing.T) {
	equipment := func(id uint32, grade, index int) storage.CraftEntry {
		return storage.CraftEntry{Equipment: storage.CraftEquipment{TemplateID: id, Grade: grade}, Quote: index}
	}
	stages := func(gold int64) storage.CraftStages {
		return storage.CraftStages{Reinforce: []*storage.CraftQuote{{Gold: gold}}}
	}
	base := storage.CraftCatalog{Definitions: map[uint32]storage.CraftEntry{1: equipment(1, 72, 0)}, Quotes: []storage.CraftStages{stages(54)}}
	extra := storage.CraftCatalog{Definitions: map[uint32]storage.CraftEntry{2: equipment(2, 72, 1), 3: equipment(3, 85, 0)}, Quotes: []storage.CraftStages{stages(80), stages(54)}}
	next, err := mergeCraft(base, extra)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Quotes) != 2 || next.Definitions[2].Quote != 0 || next.Definitions[3].Quote != 1 || next.Quotes[1].Reinforce[0].Gold != 80 {
		t.Fatalf("owned equipment prices lost their original groups: %+v", next.Definitions)
	}
	if len(base.Definitions) != 1 || len(base.Quotes) != 1 || extra.Definitions[2].Quote != 1 {
		t.Fatal("preparing equipment changed a previous snapshot or the input delta")
	}
	extra.Definitions[4] = equipment(4, 1, 2)
	if _, err = mergeCraft(base, extra); err == nil || len(base.Definitions) != 1 {
		t.Fatal("invalid quote was published")
	}
}

func TestOwnedAndGMItemsFromOriginalPVF(t *testing.T) {
	ref, source, profile := os.Getenv("S9A1_CREATION_CATALOG"), os.Getenv("S9A1_TEST_PVF"), os.Getenv("S9A1_TEST_PROFILE")
	if ref == "" || source == "" || profile == "" {
		t.Skip("set the catalog, PVF and profile for original item vectors")
	}
	base, err := resources.LoadCharacters(ref, source, profile, 1)
	if err != nil {
		t.Fatal(err)
	}
	const coat, stackable = 100050017, 1285
	online, offline := *base, *base
	queued, err := PrepareGMMail(t.Context(), &offline, source, profile, stackable, false)
	if err != nil || len(queued) == 0 || len(offline.Commerce.Templates[stackable]) != 0 {
		t.Fatalf("offline mail did not keep its acquisition private: %v", err)
	}
	mailed, err := PrepareGMMail(t.Context(), &online, source, profile, stackable, true)
	if err != nil || !bytes.Equal(mailed, queued) || !bytes.Equal(online.Commerce.Templates[stackable], queued) || online.Commerce.Discard[stackable].Template != stackable {
		t.Fatalf("online mail did not register the ordinary item's inventory rules: %v", err)
	}
	if _, exists := online.MailAttachments[stackable]; exists || len(base.Commerce.Templates[stackable]) != 0 {
		t.Fatal("operator mail unexpectedly granted player-mail transfer or modified a previous snapshot")
	}
	if _, err = PrepareGMMail(t.Context(), &online, "", "", stackable, true); err != nil {
		t.Fatalf("already registered mail reopened its PVF: %v", err)
	}
	next := *base
	if err = PrepareOwned(t.Context(), &next, source, profile, []uint32{coat}); err != nil {
		t.Fatal(err)
	}
	r, ok := next.Commerce.Craft.Definitions[coat]
	if !ok || r.Quote < 0 || r.Quote >= len(next.Commerce.Craft.Quotes) {
		t.Fatal("saved coat has no crafting quote")
	}
	prices := next.Commerce.Craft.Quotes[r.Quote]
	if prices.Reinforce[0].Gold != 54128 || prices.Amplify[0].Gold != 49140 || next.Weights[coat] != 1400 {
		t.Fatal("saved coat differs from its original quote or weight")
	}
	if err = PrepareGMItem(t.Context(), &next, source, profile, stackable); err != nil {
		t.Fatal(err)
	}
	if next.MailAttachments[stackable].Rule != 1 || next.Commerce.Discard[stackable].Template != stackable || next.Weights[stackable] != 1 {
		t.Fatal("ordinary GM fallback lost its transfer or discard rule")
	}
	if _, existed := base.Commerce.Craft.Definitions[coat]; existed || base.MailAttachments[stackable].TemplateID != 0 || len(next.Commerce.Products) != len(base.Commerce.Products) {
		t.Fatal("preparing items changed the old view or merchant catalog")
	}
}
