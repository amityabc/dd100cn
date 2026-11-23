package storage

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFreezePetClocksUsesClosingOwner(t *testing.T) {
	now := time.Unix(100, 0)
	lease := Lease{Owner: "a216e6df-fff4-4707-a4fe-8dba60c5d163", Epoch: 3}
	pet := func(owner string, epoch int, mode PetClockMode) string {
		return fmt.Sprintf(`{"Pet":{"Satiety":20,"SatietyClock":{"Owner":"%s","Epoch":%d,"SinceMilliseconds":92500,"Baseline":20,
			"Mode":%d,"SpendSeconds":5,"RecoverSeconds":5,"Maximum":100,"ResourceHash":"%s"},"ForeignPetField":9}}`,
			owner, epoch, mode, strings.Repeat("a", 64))
	}
	newer := pet(lease.Owner, 4, PetRecovering)
	paused := pet(lease.Owner, 3, PetPaused)
	exact := pet(strings.ReplaceAll(lease.Owner, "-", ""), 3, PetSpending)
	c := Character{Gameplay: json.RawMessage(`{"ForeignProfileField":18446744073709551615,"SpecialAssets":{"AvatarExpansion":0,"Items":[` +
		exact + "," + newer + "," + paused + `]}}`)}
	changed, err := freezePetClocks(&c, lease, now)
	if err != nil || !changed {
		t.Fatalf("freeze result changed=%v error=%v", changed, err)
	}
	var state struct {
		ForeignProfileField uint64
		SpecialAssets       struct{ Items []json.RawMessage }
	}
	if err := json.Unmarshal(c.Gameplay, &state); err != nil {
		t.Fatal(err)
	}
	var first struct {
		Pet struct {
			Satiety, ForeignPetField byte
			SatietyClock             savedPetClock
		}
	}
	if err := json.Unmarshal(state.SpecialAssets.Items[0], &first); err != nil {
		t.Fatal(err)
	}
	clock := first.Pet.SatietyClock
	if first.Pet.Satiety != 18 || first.Pet.ForeignPetField != 9 || clock.Baseline != 18.5 ||
		clock.SinceMilliseconds != now.UnixMilli() || clock.Mode != PetPaused || state.ForeignProfileField != ^uint64(0) {
		t.Fatalf("unexpected freeze %+v", first.Pet)
	}
	if !equalJSON(state.SpecialAssets.Items[1], []byte(newer)) || !equalJSON(state.SpecialAssets.Items[2], []byte(paused)) {
		t.Fatal("newer owner or already paused pet changed")
	}
	if changed, err = freezePetClocks(&c, lease, now.Add(time.Hour)); changed || err != nil {
		t.Fatalf("repeated release changed=%v err=%v", changed, err)
	}
}
