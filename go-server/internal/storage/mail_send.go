package storage

import (
	"context"
	"crypto/rand"
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
)

type MailRejection uint16

func (code MailRejection) Error() string { return fmt.Sprintf("player mail rejected (%d)", code) }

type MailAttachmentPolicy struct {
	TemplateID         uint32 `json:"TemplateId"`
	ResourceHash       string
	Rule               int
	LifetimeRestricted bool
}

type MailSendAttachment struct {
	Space             byte
	Slot              uint16
	TemplateID, Count uint32
}

type MailSend struct {
	Recipient, Body        string
	Gold, SpecialRecipient uint32
	Mode                   byte
	Attachments            []MailSendAttachment
}

type MailSendReplay struct {
	SenderID, RecipientID, Sequence                  uint16
	SenderRevision, RecipientRevision, AssetRevision int64
	Request                                          string
}

type MailSendOutcome struct {
	Character Character
	Inventory Inventory
	Replay    MailSendReplay
}

func MailGoldLimit(c Character, levels map[byte]int64) (int64, error) {
	var profile struct{ GoldExpansion byte }
	if err := json.Unmarshal(c.Gameplay, &profile); err != nil {
		return 0, err
	}
	if profile.GoldExpansion > 8 {
		return 0, ErrInvalid
	}
	if profile.GoldExpansion != 0 {
		return (4 + int64(profile.GoldExpansion)) * 100_000_000, nil
	}
	limit, ok := levels[c.Level]
	if !ok || limit < 0 || limit > math.MaxUint32 {
		return 0, ErrInvalid
	}
	return limit, nil
}

func mailCharacter(ctx context.Context, tx *sql.Tx, account string, id uint16) (Character, error) {
	return scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", account, id))
}

func replaceMailFields(c *Character, fields map[string]any) error {
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(c.Gameplay, &profile); err != nil {
		return err
	}
	if profile == nil {
		return ErrInvalid
	}
	for name, value := range fields {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		profile[name] = raw
	}
	data, err := json.Marshal(profile)
	if err == nil {
		c.Gameplay = data
	}
	return err
}

func saveMailCharacter(ctx context.Context, tx *sql.Tx, account string, c Character) (Character, error) {
	updated, err := scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters
		SET gameplay_json=$4::json, gold=$5, revision=revision+1
		WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns,
		account, c.ID, c.Revision, string(c.Gameplay), c.Gold))
	if errors.Is(err, sql.ErrNoRows) {
		return updated, ErrConflict
	}
	return updated, err
}

func mailIdentity(s string) int64 {
	h := sha256.Sum256([]byte(s))
	return max(1, int64(binary.LittleEndian.Uint64(h[:])&math.MaxInt64))
}

func newMailUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return hex.EncodeToString(b[:]), nil
}

func (s *Store) SendMail(ctx context.Context, lease Lease, sequence uint16, req MailSend, replay MailSendReplay, goldLimits map[byte]int64, policies map[uint32]MailAttachmentPolicy) (MailSendOutcome, error) {
	var quota struct {
		DayID    int `json:"DayId"`
		SentGold int64
	}
	request, err := json.Marshal(req)
	if err != nil {
		return MailSendOutcome{}, err
	}
	now := time.Now().UTC()
	day := now.Add(2 * time.Hour)
	dayID := day.Year()*10000 + int(day.Month())*100 + day.Day()
	return writeTx(ctx, s.db, func(tx *sql.Tx) (MailSendOutcome, error) {
		var out MailSendOutcome
		r, err := lockAccount(ctx, tx, lease, true)
		if err != nil {
			return out, err
		}
		if r.Phase != TownReady || r.SelectedID == 0 {
			return out, ErrPhase
		}
		if req.Mode != 0 || req.SpecialRecipient != 0 || strings.TrimSpace(req.Recipient) == "" || req.Gold > math.MaxInt32 || len(req.Attachments) > 10 {
			return out, MailRejection(17)
		}
		sender, err := mailCharacter(ctx, tx, lease.Account, r.SelectedID)
		if err != nil {
			return out, err
		}
		recipient, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND name_key=$2", lease.Account, nameKey(req.Recipient)))
		if errors.Is(err, sql.ErrNoRows) {
			return out, MailRejection(3)
		}
		if err != nil {
			return out, err
		}
		if recipient.ID == sender.ID {
			return out, MailRejection(7)
		}
		check := MailSendReplay{sender.ID, recipient.ID, sequence, sender.Revision, recipient.Revision, r.AssetRevision, string(request)}
		in, err := readInventory(ctx, tx, lease.Account, r)
		if err != nil {
			return out, err
		}
		if replay == check {
			return MailSendOutcome{sender, in, replay}, nil
		}
		if sender.Level < 5 {
			return out, MailRejection(227)
		}
		if req.Gold == 0 && len(req.Attachments) == 0 && req.Body == "" {
			return out, MailRejection(22)
		}
		fee := int64(100)
		if len(req.Attachments) != 0 {
			fee = 1000 * int64(len(req.Attachments))
		}
		debit := int64(req.Gold) + fee + min(10000, int64(req.Gold)*5/100)
		if debit > in.Gold {
			return out, MailRejection(10)
		}
		if req.Gold != 0 {
			limit, err := MailGoldLimit(recipient, goldLimits)
			if err != nil {
				return out, err
			}
			if int64(req.Gold) > limit {
				return out, MailRejection(14)
			}
		}
		var profile struct{ MailSendQuota json.RawMessage }
		if err = json.Unmarshal(sender.Gameplay, &profile); err != nil {
			return out, err
		}
		if len(profile.MailSendQuota) != 0 {
			if err = json.Unmarshal(profile.MailSendQuota, &quota); err != nil {
				return out, err
			}
		}
		if quota.DayID < 0 || quota.SentGold < 0 {
			return out, ErrInvalid
		}
		if quota.DayID != dayID {
			quota.DayID, quota.SentGold = dayID, 0
		}
		daily := 10000 * int64(sender.Level) * int64(sender.Level)
		if quota.SentGold > daily-int64(req.Gold) {
			return out, MailRejection(95)
		}
		quota.SentGold += int64(req.Gold)
		box, err := ReadMailbox(recipient)
		if err != nil {
			return out, err
		}
		operation, err := newMailUUID()
		if err != nil {
			return out, err
		}
		key := "player:" + operation
		identity := fmt.Sprintf("mail:%d:%s", recipient.ID, key)
		letter := MailLetter{MailID: mailIdentity(identity), DeliveryKey: key, Sender: sender.Name, Body: req.Body, Gold: req.Gold, Created: uint32(now.Unix()), Attachments: []MailAttachment{}}
		rows := map[uint16]Asset{}
		for _, a := range in.Items {
			if a.CharacterID == sender.ID && a.Location == 0 {
				rows[a.Slot] = a
			}
		}
		for _, a := range req.Attachments {
			row, ok := rows[a.Slot]
			delete(rows, a.Slot)
			if !ok || a.Space != 0 || row.TemplateID != a.TemplateID || a.Count == 0 || a.Count > row.Count {
				return out, MailRejection(17)
			}
			escrow, err := escrowMailAsset(row, a.Count, policies[row.TemplateID], now.Unix())
			if err != nil {
				return out, err
			}
			claim := mailIdentity(fmt.Sprintf("%s:attachment:%d", identity, len(letter.Attachments)))
			letter.Attachments = append(letter.Attachments, MailAttachment{ClaimID: claim, Asset: escrow})
			if a.Count == row.Count {
				_, err = tx.ExecContext(ctx, "DELETE FROM s9a1.assets WHERE instance_id=$1", row.UID)
			} else {
				_, err = tx.ExecContext(ctx, "UPDATE s9a1.assets SET count=count-$2 WHERE instance_id=$1", row.UID, a.Count)
			}
			if err != nil {
				return out, err
			}
		}
		// Keep a payload fingerprint separate from the short-lived client replay key.
		hashPayload, err := json.Marshal(struct {
			Sender, Recipient uint16
			Gold              uint32
			Body              string
			Attachments       []MailAttachment
		}{
			sender.ID, recipient.ID, req.Gold, req.Body, letter.Attachments,
		})
		if err != nil {
			return out, err
		}
		hash := sha256.Sum256(hashPayload)
		letter.Fingerprint = strings.ToUpper(hex.EncodeToString(hash[:]))
		box.Mails = append(box.Mails, letter)
		if err = box.Validate(); err != nil {
			return out, err
		}
		if err = replaceMailFields(&recipient, map[string]any{"Mailbox": box}); err != nil {
			return out, err
		}
		if err = replaceMailFields(&sender, map[string]any{"MailSendQuota": quota}); err != nil {
			return out, err
		}
		sender.Gold -= debit
		sender, err = saveMailCharacter(ctx, tx, lease.Account, sender)
		if err != nil {
			return out, err
		}
		recipient, err = saveMailCharacter(ctx, tx, lease.Account, recipient)
		if err != nil {
			return out, err
		}
		in, err = readInventory(ctx, tx, lease.Account, r)
		if err != nil {
			return out, err
		}
		check.SenderRevision, check.RecipientRevision = sender.Revision, recipient.Revision
		return MailSendOutcome{sender, in, check}, nil
	})
}

func escrowMailAsset(row Asset, count uint32, p MailAttachmentPolicy, now int64) (MailAsset, error) {
	t, err := moveTemplate(&row)
	if err != nil || t.Kind != 2 && count != 1 || p.TemplateID != row.TemplateID || p.ResourceHash != t.ResourceHash {
		return MailAsset{}, MailRejection(17)
	}
	var attrs struct {
		Binding   byte
		Locked    bool
		ExpiresAt *int64
	}
	if err = json.Unmarshal(row.Attributes, &attrs); err != nil {
		return MailAsset{}, err
	}
	if attrs.Locked {
		return MailAsset{}, MailRejection(214)
	}
	if attrs.ExpiresAt != nil && *attrs.ExpiresAt <= now {
		return MailAsset{}, MailRejection(235)
	}
	if p.LifetimeRestricted || attrs.ExpiresAt != nil {
		return MailAsset{}, MailRejection(219)
	}
	if len(row.Detail) != 0 || attrs.Binding != 0 || p.Rule != 1 && p.Rule != 2 || len(row.Core) != 119 {
		return MailAsset{}, MailRejection(23)
	}
	uid := row.UID
	if row.Count != count {
		uid, err = newMailUUID()
		if err != nil {
			return MailAsset{}, err
		}
	}
	template := row.Template
	if row.Source == "weapon" && (len(template) == 0 || string(template) == "null") {
		raw, err := json.Marshal(t)
		if err != nil {
			return MailAsset{}, err
		}
		full, err := frozenMaterialObject(raw, materialTemplateDefaults)
		if err != nil {
			return MailAsset{}, err
		}
		full["TemplateId"] = full["TemplateID"]
		delete(full, "TemplateID")
		if hasEffect(row.Weapon.Qualification) {
			full["WeaponQualification"] = row.Weapon.Qualification
		}
		template, err = json.Marshal(full)
		if err != nil {
			return MailAsset{}, err
		}
	}
	attributes := row.Attributes
	if row.Source == "weapon" {
		if !hasEffect(attributes) {
			attributes = json.RawMessage("{}")
		}
		attrs, err := frozenMaterialObject(attributes, materialAttributeDefaults)
		if err != nil {
			return MailAsset{}, err
		}
		delete(attrs, "FrozenWeaponTemplate")
		attrs["QualitySeed"] = json.RawMessage(fmt.Sprint(row.Weapon.QualitySeed))
		attrs["Durability"] = json.RawMessage(fmt.Sprint(row.Weapon.Durability))
		attributes, err = json.Marshal(attrs)
		if err != nil {
			return MailAsset{}, err
		}
	}
	if !mailUID(uid) {
		return MailAsset{}, ErrInvalid
	}
	return MailAsset{materialGUID(uid), template, attributes, count, row.Location, row.Slot, row.Core, row.Detail}, nil
}
