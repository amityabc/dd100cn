package storage

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// ValidateOculusBindings accepts v1 and legacy rows for characters since deleted.
func ValidateOculusBindings(raw []byte) error {
	var bindings []oculusBinding
	if err := json.Unmarshal(raw, &bindings); err != nil || bindings == nil {
		return ErrInvalid
	}
	type bindingKey struct {
		week      int64
		character uint16
	}
	type weekCount struct{ legacy, current int }
	seen := make(map[bindingKey]bool, len(bindings))
	counts := make(map[int64]weekCount)
	for _, row := range bindings {
		var entry OculusEntry
		var compact bytes.Buffer
		decoder := json.NewDecoder(strings.NewReader(row.EntryInput))
		decoder.DisallowUnknownFields()
		if json.Compact(&compact, []byte(row.EntryInput)) != nil || compact.String() != row.EntryInput || decoder.Decode(&entry) != nil {
			return ErrInvalid
		}
		owner, errOwner := uuid.Parse(entry.Owner)
		run, errRun := uuid.Parse(entry.RunId)
		_, week := OculusPeriods(entry.EnteredAt)
		key := bindingKey{row.Week, row.CharacterId}
		if row.CharacterId == 0 || row.CharacterId != entry.CharacterId || row.Week != entry.Week || row.Week < week ||
			entry.Mode != 2 || entry.Dungeon != oculusDungeon(2) || entry.Epoch <= 0 || entry.EnteredAt.IsZero() ||
			errOwner != nil || owner == uuid.Nil || errRun != nil || run == uuid.Nil || seen[key] ||
			entry.Policy != OculusEntryPolicy && entry.Policy != "OfficialSharedOculusEntryCnSchedule/v1" {
			return ErrInvalid
		}
		seen[key] = true
		n := counts[row.Week]
		if row.Legacy {
			n.legacy++
		} else {
			n.current++
		}
		counts[row.Week] = n
	}
	for _, n := range counts {
		if n.current > max(0, 4-n.legacy) {
			return ErrInvalid
		}
	}
	return nil
}
