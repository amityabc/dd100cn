package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"
)

type MailClaim struct {
	MailID, ClaimID int64
}

type MailboxOutcome struct {
	Character Character
	Inventory Inventory
	Page      MailboxPage
	Claims    []MailClaim
}

// MailboxAction reads or modifies the selected character's mailbox under the
// account lock, including any transfer back to the live asset and gold tables.
func (s *Store) MailboxAction(ctx context.Context, lease Lease, op uint16, ids []int64, state uint16, limits map[byte]int64, weights map[uint32]int64) (MailboxOutcome, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (MailboxOutcome, error) {
		var out MailboxOutcome
		r, err := lockAccount(ctx, tx, lease, op != 96)
		if err != nil {
			return out, err
		}
		if r.Phase != TownReady || r.SelectedID == 0 {
			return out, ErrPhase
		}
		c, err := mailCharacter(ctx, tx, lease.Account, r.SelectedID)
		if err != nil {
			return out, err
		}
		box, err := ReadMailbox(c)
		if err != nil {
			return out, err
		}
		page := box.Page()
		if op == 96 {
			return MailboxOutcome{Character: c, Page: page}, nil
		}
		if len(ids) < 1 || len(ids) > 100 {
			return out, ErrInvalid
		}
		seen := map[int64]bool{}
		for _, id := range ids {
			if id <= 0 || seen[id] {
				return out, ErrInvalid
			}
			seen[id] = true
		}
		changed := true
		switch op {
		case 95:
			in, err := readInventory(ctx, tx, lease.Account, r)
			if err != nil {
				return out, err
			}
			claims, shared, err := claimMail(ctx, tx, lease.Account, &c, in, box, page, ids, limits, weights)
			if err != nil {
				return out, err
			}
			out.Claims = claims
			if shared {
				if _, err = tx.ExecContext(ctx, "UPDATE s9a1.accounts SET asset_revision=asset_revision+1 WHERE account=$1", lease.Account); err != nil {
					return out, err
				}
				r.AssetRevision++
			}
		case 134:
			changed, err = changeMail(box, page, ids, state)
			if err != nil {
				return out, err
			}
		default:
			return out, ErrInvalid
		}
		if changed {
			if err = box.Validate(); err != nil {
				return out, err
			}
			if err = replaceMailFields(&c, map[string]any{"Mailbox": box}); err != nil {
				return out, err
			}
			c, err = saveMailCharacter(ctx, tx, lease.Account, c)
			if err != nil {
				return out, err
			}
		}
		out.Character, out.Page = c, box.Page()
		if op == 95 {
			out.Inventory, err = readInventory(ctx, tx, lease.Account, r)
		}
		return out, err
	})
}

func changeMail(box Mailbox, page MailboxPage, ids []int64, state uint16) (bool, error) {
	if state != 0 && state != 2 && state != 3 {
		return false, ErrInvalid
	}
	letters := map[int64]*MailLetter{}
	for _, l := range page.Letters {
		letters[l.MailID] = l
	}
	saved := 0
	for _, l := range box.Mails {
		if l.Saved && !l.Deleted {
			saved++
		}
	}
	changed := false
	for _, id := range ids {
		l := letters[id]
		if l == nil {
			return false, ErrInvalid
		}
		switch state {
		case 0:
			if !l.FullyClaimed() {
				return false, ErrInvalid
			}
			l.Deleted, changed = true, true
		case 2:
			changed = changed || !l.Read
			l.Read = true
		case 3:
			if l.Saved {
				continue
			}
			if saved >= 10 {
				return false, ErrCapacity
			}
			l.Saved, l.Read, changed = true, true, true
			saved++
		}
	}
	return changed, nil
}

func claimMail(ctx context.Context, tx *sql.Tx, account string, c *Character, in Inventory, box Mailbox, page MailboxPage, ids []int64, limits map[byte]int64, weights map[uint32]int64) ([]MailClaim, bool, error) {
	type target struct {
		mail  *MailLetter
		index int
	}
	allowed := map[int64]target{}
	for _, l := range page.Letters {
		pending := 0
		for i, a := range l.Attachments {
			if !a.Claimed {
				allowed[a.ClaimID] = target{l, i}
				pending++
			}
		}
		if pending == 0 && l.Gold != 0 && !l.GoldClaimed {
			allowed[l.MailID] = target{l, -1}
		}
	}
	rows := slices.Clone(in.Items)
	deltas := map[uint32]int64{}
	var claims []MailClaim
	var gold int64
	var shared bool
	now := time.Now().Unix()
	for _, id := range ids {
		t, ok := allowed[id]
		if !ok {
			return nil, false, ErrInvalid
		}
		l := t.mail
		first := slices.IndexFunc(l.Attachments, func(a MailAttachment) bool { return !a.Claimed })
		if !l.GoldClaimed && (t.index < 0 || t.index == first) {
			gold += int64(l.Gold)
			l.GoldClaimed = true
		}
		if t.index >= 0 {
			a := &l.Attachments[t.index]
			source, template, err := claimSource(a.Asset, c.ID, now)
			if err != nil {
				return nil, false, err
			}
			tgts, err := returnMailAttachment(ctx, tx, account, in.BagExpansion, &rows, source, template)
			if err != nil {
				return nil, false, err
			}
			a.Claimed = true
			box.ClaimTransfers[id] = MailTransfer{id, a.Asset, tgts}
			if source.Source == "shared" {
				shared = true
			} else if source.Location == 0 {
				deltas[source.TemplateID] += int64(source.Count)
			}
		}
		l.Read = true
		claims = append(claims, MailClaim{l.MailID, id})
	}
	if gold != 0 {
		limit, err := MailGoldLimit(*c, limits)
		if err != nil {
			return nil, false, err
		}
		if c.Gold > limit-gold {
			return nil, false, ErrFunds
		}
		c.Gold += gold
	}
	if err := checkBagWeight(*c, in, deltas, weights); err != nil {
		return nil, false, err
	}
	return claims, shared, nil
}

func claimSource(a MailAsset, character uint16, now int64) (Asset, ordinaryTemplate, error) {
	var t ordinaryTemplate
	var attr materialAttributes
	var src Asset
	if !mailUID(a.InstanceID) || len(a.Raw119) != 119 || len(a.Detail) > 65536 || json.Unmarshal(a.Template, &t) != nil ||
		json.Unmarshal(a.Attributes, &attr) != nil || attr.Locked || attr.ExpiresAt != nil && *attr.ExpiresAt <= now ||
		t.TemplateID == 0 || t.TemplateID == math.MaxUint32 || t.Kind < 0 || t.Kind > 2 || t.StackLimit == 0 ||
		a.Count == 0 || a.Count > t.StackLimit || t.Kind != 2 && (a.Count != 1 || attr.QualitySeed == 0 || attr.QualitySeed >= 999999999) ||
		t.FateCollection || t.AccountCommonInventory {
		return src, t, ErrInvalid
	}
	src = Asset{UID: strings.ToLower(strings.ReplaceAll(a.InstanceID, "-", "")), Source: "item", CharacterID: character,
		TemplateID: t.TemplateID, Count: a.Count, Template: a.Template, Attributes: a.Attributes, Core: a.Raw119, Detail: a.Detail}
	if err := attachCommerceWeapon(&src, t); err != nil {
		return src, t, err
	}
	switch {
	case t.PetConsumable:
		src.Location = 7
		t.FirstBagSlot, t.LastBagSlot = 216, 271
	case t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32:
		src.Location = 38
		t.FirstBagSlot, t.LastBagSlot = 0, 55
	default:
		if slot, ok := SharedMaterialSlot(t.TemplateID); ok {
			var def materialDefinition
			if err := json.Unmarshal(a.Template, &def); err != nil {
				return src, t, err
			}
			plain, err := plainMaterial(src, def)
			if err != nil {
				return src, t, err
			}
			if !plain {
				return src, t, ErrInvalid
			}
			src.Source, src.CharacterID = "shared", 0
			t.FirstBagSlot, t.LastBagSlot = slot, slot
		} else if t.FirstBagSlot < 3 || t.LastBagSlot > 360 || t.FirstBagSlot > t.LastBagSlot {
			return src, t, ErrInvalid
		}
	}
	return src, t, nil
}

func returnMailAttachment(ctx context.Context, tx *sql.Tx, account string, expansion uint16, rows *[]Asset, src Asset, t ordinaryTemplate) ([]MailTarget, error) {
	var targets []MailTarget
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM s9a1.assets WHERE lower(instance_id)=$1)", src.UID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrInvalid
	}
	left := src.Count
	end := t.LastBagSlot
	if src.Location == 0 && src.Source != "shared" {
		end = t.bagEnd(expansion)
	}
	slots := map[uint16]*Asset{}
	for i := range *rows {
		a := &(*rows)[i]
		if a.Location == src.Location && a.CharacterID == src.CharacterID {
			slots[a.Slot] = a
		}
	}
	if t.Kind == 2 {
		// Quickslots can be existing merge targets but new mail never fills them.
		for slot := uint16(0); slot <= end && left > 0; slot++ {
			a := slots[slot]
			if a == nil || a.Count >= t.StackLimit || a.TemplateID != src.TemplateID {
				continue
			}
			same := stacks(a, &src)
			if src.Source == "shared" {
				var err error
				same, err = compatibleMaterial(*a, src)
				if err != nil {
					return nil, err
				}
			}
			if !same {
				continue
			}
			take := min(left, t.StackLimit-a.Count)
			if _, err := tx.ExecContext(ctx, "UPDATE s9a1.assets SET count=count+$2 WHERE instance_id=$1", a.UID, take); err != nil {
				return nil, err
			}
			a.Count += take
			left -= take
			targets = append(targets, MailTarget{materialGUID(a.UID), slot, take})
		}
	}
	if left == 0 {
		return targets, nil
	}
	for slot := t.FirstBagSlot; slot <= end; slot++ {
		if slots[slot] != nil {
			continue
		}
		src.Slot, src.Count = slot, left
		err := insertCommerceAsset(ctx, tx, account, src)
		if err != nil {
			return nil, err
		}
		*rows = append(*rows, src)
		return append(targets, MailTarget{materialGUID(src.UID), slot, left}), nil
	}
	return nil, ErrCapacity
}
