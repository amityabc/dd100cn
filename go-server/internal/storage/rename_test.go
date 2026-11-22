package storage

import (
	"encoding/json"
	"testing"

	"s9a1/server/internal/protocol"
)

func TestRenameCardAndPaidCooldown(t *testing.T) {
	const now int64 = 1760000000
	req := protocol.RenameRequest{OldName: "旧名字", NewName: "新名字", Reason: 2}
	for _, source := range []uint32{RenameBag, RenameCash} {
		p, err := json.Marshal(struct{ CharacterRename RenameState }{RenameState{PendingSource: source, GrantedAt: now - 1, NextAllowedAt: 12}})
		if err != nil {
			t.Fatal(err)
		}
		c := Character{Name: req.OldName, Gameplay: p}
		s, paid, err := completeRename(c, req, now)
		if err != nil || s.Pending || s.GrantedAt != 0 || s.LastChangedAt != now || paid != (source == RenameCash) {
			t.Fatal(s, paid, err)
		}
		want := int64(12)
		if paid {
			want = now + 90*86400
		}
		if s.NextAllowedAt != want {
			t.Fatal(s.NextAllowedAt)
		}
		c.Name = "Original"
		if _, _, err := completeRename(c, protocol.RenameRequest{OldName: "Original", NewName: "ORIGINAL", Reason: 1}, now); err == nil {
			t.Fatal("ASCII case-only rename admitted")
		}
	}
}
