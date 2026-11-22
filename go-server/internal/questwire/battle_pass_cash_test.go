package questwire

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"s9a1/server/internal/storage"
)

func TestBattlePassCashPurchaseSeasonAndAsymmetricUnlock(t *testing.T) {
	now := time.Unix(1000, 0)
	d := BattlePassData{}
	d.Definition.Schedule.StartsAt, d.Definition.Schedule.EndsAt = 900, 1100
	d.Definition.ResourceHash = strings.Repeat("0", 64)
	d.Definition.Experience = make([]int, 100)
	d.Definition.Certificate, d.Definition.Package = 501, 502
	offer := storage.CashOffer{TemplateID: 501, Count: 1, Cera: 200, BattlePassHash: d.Definition.ResourceHash}
	cert, err := d.BuyCash(nil, offer, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.BuyCash(cert, offer, now); !errors.Is(err, ErrPassQualification) {
		t.Fatalf("certificate replay qualification = %v", err)
	}
	pack := offer
	pack.TemplateID, pack.BattlePassDeluxe = 502, true
	raw, err := d.BuyCash(cert, pack, now)
	if err != nil {
		t.Fatal(err)
	}
	var state passState
	if json.Unmarshal(raw, &state) != nil || !state.Certificate || !state.Deluxe || state.Level != 50 || state.Experience != 0 {
		t.Fatalf("upgraded account = %s", raw)
	}
	if _, err := d.BuyCash(raw, pack, now); !errors.Is(err, ErrPassQualification) {
		t.Fatalf("deluxe replay qualification = %v", err)
	}
	if _, err := d.BuyCash(nil, offer, time.Unix(1100, 0)); !errors.Is(err, ErrPassClosed) {
		t.Fatalf("closed season qualification = %v", err)
	}
}
