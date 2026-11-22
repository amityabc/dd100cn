package operator

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestOperatorFrameAndOfflineMailReplay(t *testing.T) {
	m := Mail{Account: "local", Character: 3, TemplateId: 3033, Count: 2, OperationId: uuid.MustParse("31570003-5555-4444-bbbb-444444444444"), Reason: "恢复装备"}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if err = WriteFrame(&wire, body); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(wire.Bytes()) != uint32(len(body)) {
		t.Fatal("length prefix")
	}
	raw, err := ReadFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	i, err := Parse(raw)
	if err != nil || i.Command != m || !Targets(m.OperationId, 3) || Targets(m.OperationId, 4) {
		t.Fatalf("intent: %v %v", i, err)
	}
	b := Outbox{Root: t.TempDir()}
	hash := strings.Repeat("A", 64)
	first, err := b.Queue(m, hash)
	if err != nil || first.Status != "Queued" || !first.ReadBack {
		t.Fatalf("queued: %+v %v", first, err)
	}
	saved, err := b.Read(m.OperationId)
	if err != nil || saved == nil || saved.Intent != m {
		t.Fatalf("readback: %v", err)
	}
	conflict := m
	conflict.Count++
	if _, err = b.Queue(conflict, hash); err == nil {
		t.Fatal("changed operation was accepted")
	}
	result := Reply{Status: "Granted", OperationId: m.OperationId, ReadBack: true, MailGrant: &MailAudit{MailId: 45, TemplateId: m.TemplateId, Count: m.Count, ResourceHash: hash}}
	if err = b.Complete(m, "Delivered", result); err != nil {
		t.Fatal(err)
	}
	replay, err := b.Queue(m, hash)
	if err != nil || replay.MailGrant == nil || *replay.MailGrant != *result.MailGrant || replay.Status != "Granted" {
		t.Fatalf("completed replay: %+v %v", replay, err)
	}
	if err = b.Pending(func(MailEntry) bool { t.Fatal("delivered mail is still queued"); return false }); err != nil {
		t.Fatal(err)
	}
}
