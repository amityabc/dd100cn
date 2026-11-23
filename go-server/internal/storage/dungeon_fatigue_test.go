package storage

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAvailableDungeonFatigueAtDailyCutoff(t *testing.T) {
	cutoff := time.Date(2026, time.November, 22, 22, 0, 0, 0, time.UTC)
	c := Character{Gameplay: json.RawMessage(`{"Progression":{"Fatigue":0,"FatigueLimit":156,"FatigueDay":20261122}}`)}
	for _, test := range []struct {
		at   time.Time
		want uint16
	}{
		{cutoff.Add(-time.Second), 0},
		{cutoff, 156},
	} {
		got, err := AvailableDungeonFatigue(c, test.at)
		if err != nil || got.Remaining != test.want || got.Limit != 156 {
			t.Fatalf("at=%s fatigue=%+v err=%v, want=%d", test.at, got, err, test.want)
		}
	}
}
