package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"s9a1/server/internal/operator"
	"s9a1/server/internal/savejson"
)

// The declaration order is part of the legacy delivery fingerprint.
type ordinaryMailAttributes struct {
	QualitySeed                             uint32
	Durability                              uint16
	Reinforcement, Forge, AmplificationType uint8
	AmplificationValue                      uint16
	Enchantment                             uint32
	Binding                                 uint8
	ExpiresAt                               *int64
	Locked                                  bool
	EnchantUpgradeCount                     uint8
}
type systemGrant struct {
	Template         json.RawMessage
	Count            uint32
	Attributes       ordinaryMailAttributes
	Raw119           [119]uint16
	Detail           []uint16
	PreserveIdentity uuid.UUID
}
type systemDraft struct {
	Sender, Title, Body savejson.String
	Gold                uint32
	Attachments         []systemGrant
}
type deliveryProof struct {
	Fingerprint string
	MailId      int64
}

func gmMailGrant(m operator.Mail, raw json.RawMessage, now time.Time) (systemGrant, ordinaryTemplate, error) {
	var t ordinaryTemplate
	var d materialDefinition
	if err := json.Unmarshal(raw, &t); err != nil {
		return systemGrant{}, t, err
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return systemGrant{}, t, err
	}
	if t.TemplateID != m.TemplateId || t.Kind < 0 || t.Kind > 2 || len(t.ResourceHash) != 64 || t.StackLimit == 0 {
		return systemGrant{}, t, errors.New("Prepared PVF item template is invalid")
	}
	if m.Count == 0 || m.Count > t.StackLimit || t.Kind != 2 && m.Count != 1 {
		return systemGrant{}, t, errors.New("Mail quantity exceeds the PVF template limit")
	}
	if d.PvfGiftItem || d.CashPackage || d.CashPackageReward {
		if d.ResourceExpiration != nil && *d.ResourceExpiration <= now.Unix() {
			return systemGrant{}, t, errors.New("Gift box reward template, quantity or expiry is unavailable")
		}
		if d.InitialSealed || d.ResourceExpiration != nil || d.RelativeLifetimeSeconds != 0 || d.DailyDeletion || d.WeeklyDeletionDay != nil || d.FatigueTimeMonthlyDeletion {
			return systemGrant{}, t, errors.New("System mail supports complete ordinary equipment and bounded stacks only")
		}
	} else if d.InitialSealed {
		return systemGrant{}, t, errors.New("System mail attachment core is invalid")
	}
	var q uint32
	if t.Kind != 2 {
		key := fmt.Sprintf("gm-mail:%s:%d:%s", strings.ReplaceAll(m.OperationId.String(), "-", ""), m.TemplateId, t.ResourceHash)
		h := sha256.Sum256([]byte(key))
		q = 1 + binary.LittleEndian.Uint32(h[:])%999_999_997
	}
	plain, err := ordinaryNewCore(Asset{Core: make([]byte, 119)}, d, materialAttributes{QualitySeed: q, Durability: t.MaximumDurability})
	if err != nil || !plain {
		return systemGrant{}, t, errors.New("System mail attachment core is invalid")
	}
	return systemGrant{Template: raw, Count: m.Count, Attributes: ordinaryMailAttributes{QualitySeed: q, Durability: t.MaximumDurability}, Detail: []uint16{}}, t, nil
}

func deliverGmMail(c Character, key string, m operator.Mail, grant systemGrant, t ordinaryTemplate, now time.Time) (operator.MailAudit, Mailbox, error) {
	return deliverOrdinaryMail(c, key, "S7 GM", "GM 发放", m.Reason, grant, t, now)
}

// ActivityRewardMail uses the same durable mailbox identity as other native system deliveries.
func ActivityRewardMail(c Character, key, sender, title, body string, raw json.RawMessage, count uint32, now time.Time) (Character, bool, error) {
	var t ordinaryTemplate
	if err := json.Unmarshal(raw, &t); err != nil {
		return c, false, err
	}
	if count == 0 || count > t.StackLimit || t.TemplateID == 0 || t.Kind != 2 && count != 1 {
		return c, false, ErrInvalid
	}
	grant := systemGrant{Template: raw, Count: count, Detail: []uint16{}}
	box, err := ReadMailbox(c)
	if err != nil {
		return c, false, err
	}
	_, exists := box.DeliveryReceipts[key]
	_, box, err = deliverOrdinaryMail(c, key, sender, title, body, grant, t, now)
	if err != nil || exists {
		return c, false, err
	}
	err = replaceMailFields(&c, map[string]any{"Mailbox": box})
	return c, err == nil, err
}

func deliverOrdinaryMail(c Character, key, sender, title, body string, grant systemGrant, t ordinaryTemplate, now time.Time) (operator.MailAudit, Mailbox, error) {
	box, err := ReadMailbox(c)
	audit := operator.MailAudit{MailId: mailIdentity(fmt.Sprintf("mail:%d:%s", c.ID, key)), TemplateId: t.TemplateID, Count: grant.Count, ResourceHash: t.ResourceHash}
	if err != nil {
		return audit, box, err
	}
	if err = validateMailText(body, 512); err != nil {
		return audit, box, errors.New("System mail text is outside the native limit")
	}
	draft := systemDraft{Sender: savejson.String(sender), Title: savejson.String(title), Body: savejson.String(body), Attachments: []systemGrant{grant}}
	frozen, err := json.Marshal(draft)
	if err != nil {
		return audit, box, err
	}
	sum := sha256.Sum256(frozen)
	proof := deliveryProof{strings.ToUpper(hex.EncodeToString(sum[:])), audit.MailId}
	if raw, exists := box.DeliveryReceipts[key]; exists {
		var previous deliveryProof
		if json.Unmarshal(raw, &previous) != nil || proof != previous {
			return audit, box, errors.New("Delivery key already identifies a different payload")
		}
		return audit, box, nil
	}
	visible := 0
	for _, letter := range box.Mails {
		if letter.MailID == audit.MailId || letter.DeliveryKey == key {
			return audit, box, errors.New("System mail identity collision")
		}
		if !letter.Deleted {
			visible++
		}
	}
	for _, a := range box.DeliveryReceipts {
		var p deliveryProof
		if err = json.Unmarshal(a, &p); err != nil {
			return audit, box, err
		}
		if p.MailId == audit.MailId {
			return audit, box, errors.New("System mail identity collision")
		}
	}
	if visible >= 100 {
		return audit, box, errors.New("Visible mailbox is full")
	}
	if now.Unix() < 0 || now.Unix() > math.MaxUint32 {
		return audit, box, ErrInvalid
	}
	uid, err := newMailUUID()
	if err != nil {
		return audit, box, err
	}
	attr, err := json.Marshal(grant.Attributes)
	if err != nil {
		return audit, box, err
	}
	var location byte
	if t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32 {
		location = 38
	} else if t.PetConsumable {
		location = 7
	}
	asset := MailAsset{InstanceID: materialGUID(uid), Template: grant.Template, Count: grant.Count, Location: location, Slot: t.FirstBagSlot, Attributes: attr, Raw119: make([]byte, 119), Detail: []byte{}}
	claim := MailAttachment{ClaimID: mailIdentity(fmt.Sprintf("mail:%d:%s:attachment:0", c.ID, key)), Asset: asset}
	box.Mails = append(box.Mails, MailLetter{MailID: audit.MailId, DeliveryKey: key, Fingerprint: proof.Fingerprint, Sender: sender, Title: title, Body: body, Created: uint32(now.Unix()), Attachments: []MailAttachment{claim}})
	box.DeliveryReceipts[key], err = json.Marshal(proof)
	if err != nil {
		return audit, box, err
	}
	return audit, box, box.Validate()
}

// OperatorMail writes the mailbox, attachment identity and audit receipt in one
// account transaction. A second transaction independently reloads the receipt.
func (s *Store) OperatorMail(ctx context.Context, l Lease, m operator.Mail, raw json.RawMessage, inTown bool) (operator.Reply, error) {
	msg := func(status, reason string) operator.Reply { return operator.Message(status, m.OperationId, reason) }
	if m.Account != "local" || m.Account != l.Account || m.Character == 0 {
		return msg("Rejected", "Operator target is not the currently selected local account and character"), nil
	}
	if m.OperationId == uuid.Nil || m.TemplateId == 0 || m.Count == 0 || m.Count > 1_000_000 || !operator.ValidReason(m.Reason) {
		return msg("Rejected", "Invalid bounded GM mail intent"), nil
	}
	now := time.Now().UTC()
	grant, t, err := gmMailGrant(m, raw, now)
	if err != nil {
		return msg("Rejected", err.Error()), nil
	}
	key := "gm-mail:" + strings.ReplaceAll(m.OperationId.String(), "-", "")
	input, err := json.Marshal(struct {
		Source, Version   string
		Account           savejson.String
		Character         uint16
		TemplateId, Count uint32
		OperationId       uuid.UUID
		Reason            savejson.String
		ResourceHash      string
	}{
		"LocalOperator", "AuthorizedLocalGmMail/v1", savejson.String(m.Account), m.Character, m.TemplateId, m.Count, m.OperationId, savejson.String(m.Reason), t.ResourceHash})
	if err != nil {
		return operator.Reply{}, err
	}
	readback := false
	apply := func(tx *sql.Tx) (operator.Reply, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return operator.Reply{}, err
		}
		if r.Phase != TownReady || r.SelectedID != m.Character {
			return operator.Reply{}, ErrPhase
		}
		c, err := mailCharacter(ctx, tx, l.Account, r.SelectedID)
		if err != nil {
			return operator.Reply{}, err
		}
		var p struct{ Initialized bool }
		if err = json.Unmarshal(c.Gameplay, &p); err != nil {
			return operator.Reply{}, err
		}
		if !p.Initialized {
			return msg("Rejected", "Operator target is not the currently selected local account and character"), nil
		}
		if !inTown {
			return msg("Rejected", "GM mail grants require a ready town owner"), nil
		}
		var payload []byte
		err = tx.QueryRowContext(ctx, "SELECT payload::text FROM s9a1.reward_receipts WHERE character_id=$1 AND event_key=$2", c.ID, key).Scan(&payload)
		if err == nil {
			var receipt deathReceipt
			var a operator.MailAudit
			if json.Unmarshal(payload, &receipt) != nil || receipt.Key != key || receipt.Input != string(input) || receipt.Revision <= 0 || receipt.Revision > c.Revision || json.Unmarshal([]byte(receipt.Result), &a) != nil || a.MailId != mailIdentity(fmt.Sprintf("mail:%d:%s", c.ID, key)) || a.TemplateId != m.TemplateId || a.Count != m.Count || a.ResourceHash != t.ResourceHash {
				return msg("Rejected", "GM mail operation conflicts with its committed intent"), nil
			}
			return operator.Reply{Status: "Replay", OperationId: m.OperationId, CurrentCera: r.Cera, CurrentGold: c.Gold, CharacterRevision: c.Revision, AccountRevision: r.AssetRevision, MailGrant: &a}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return operator.Reply{}, err
		}
		if readback {
			return msg("Unconfirmed", "Committed mail receipt is absent"), nil
		}
		a, box, err := deliverGmMail(c, key, m, grant, t, now)
		if err != nil {
			return msg("Rejected", err.Error()), nil
		}
		if err = replaceMailFields(&c, map[string]any{"Mailbox": box}); err != nil {
			return operator.Reply{}, err
		}
		c, err = saveMailCharacter(ctx, tx, l.Account, c)
		if err != nil {
			return operator.Reply{}, err
		}
		result, err := json.Marshal(a)
		if err != nil {
			return operator.Reply{}, err
		}
		payload, err = json.Marshal(deathReceipt{Key: key, Input: string(input), Result: string(result), Revision: c.Revision, Grants: []json.RawMessage{}})
		if err != nil {
			return operator.Reply{}, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)", c.ID, key, string(payload))
		return operator.Reply{Status: "Granted", OperationId: m.OperationId}, err
	}
	first, err := writeTx(ctx, s.db, apply)
	if err != nil || first.Status != "Granted" && first.Status != "Replay" {
		return first, err
	}
	readback = true
	read, err := writeTx(ctx, s.db, apply)
	if err != nil {
		return first, err
	}
	if read.Status != "Replay" {
		return msg(first.Status, "Mail grant completed but cold readback was not confirmed"), nil
	}
	read.Status, read.ReadBack = first.Status, true
	return read, nil
}
