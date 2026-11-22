package storage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"s9a1/server/internal/operator"
)

func TestOperatorMailKeepsIdentityOnReplay(t *testing.T) {
	now := time.Unix(1800000000, 0)
	hash := strings.Repeat("C", 64)
	m := operator.Mail{Account: "local", Character: 8, TemplateId: 7301, Count: 1, OperationId: uuid.MustParse("43365095-2938-4d66-b795-bff0b5762384"), Reason: "补发+物品"}
	raw := json.RawMessage(`{"TemplateId":7301,"Kind":1,"StackLimit":1,"FirstBagSlot":9,"MaximumDurability":34,"ResourceHash":"` + hash + `"}`)
	g, def, err := gmMailGrant(m, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if g.Attributes.QualitySeed == 0 || g.Attributes.Durability != 34 {
		t.Fatal(g.Attributes)
	}
	key := "gm-mail:4336509529384d66b795bff0b5762384"
	c := Character{ID: 8, Gameplay: json.RawMessage("{}")}
	a, box, err := deliverGmMail(c, key, m, g, def, now)
	if err != nil {
		t.Fatal(err)
	}
	asset := box.Mails[0].Attachments[0].Asset
	if !mailUID(asset.InstanceID) || len(asset.Raw119) != 119 || a.MailId != box.Mails[0].MailID || box.Mails[0].FullyClaimed() {
		t.Fatal("invalid delivered asset")
	}
	if err = replaceMailFields(&c, map[string]any{"Mailbox": box}); err != nil {
		t.Fatal(err)
	}
	a2, box2, err := deliverGmMail(c, key, m, g, def, now.Add(24*time.Hour))
	if err != nil || a != a2 || len(box2.Mails) != 1 || box2.Mails[0].Attachments[0].Asset.InstanceID != asset.InstanceID {
		t.Fatalf("replay: %v", err)
	}
	m.Reason = "不同的请求"
	if _, _, err = deliverGmMail(c, key, m, g, def, now); err == nil {
		t.Fatal("different draft under same key")
	}
}
