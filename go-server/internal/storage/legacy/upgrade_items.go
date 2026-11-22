package legacy

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"

	"s9a1/server/internal/itemdata"
	"s9a1/server/internal/savejson"
)

func oldPetSupply(t itemdata.ItemTemplate) (bool, error) {
	var hash string
	switch t.TemplateId {
	case 24:
		hash = "D97E7135ECC340125353D204211E7BEC3E77B7EE2ADA5FAE7F9751F34E477A8F"
	case 25:
		hash = "74210002B066B4B558DD392D7B7603B9FA82AC0BE5E6C1B6C68CC0FC6BE5381C"
	default:
		return false, nil
	}
	if t.ResourceHash != hash {
		return false, nil
	}
	want := itemdata.ItemTemplate{TemplateId: t.TemplateId, Kind: 2, FirstBagSlot: 65, LastBagSlot: 120, WornSlot: 65535, StackLimit: math.MaxUint32, MinimumLevel: 1, UsableJobs: 65535, ResourceHash: hash, PvfGiftItem: true, WeaponQualification: t.WeaponQualification}
	if !t.WeaponQualification.Verified || t.WeaponQualification.Jobs == nil || len(t.WeaponQualification.Jobs) != 0 || !reflect.DeepEqual(t, want) {
		return false, errors.New("legacy pet supplies have unexpected template metadata")
	}
	return true, nil
}

func historicalMedal(t itemdata.ItemTemplate) bool {
	validHash := func(s string) bool { _, e := hex.DecodeString(s); return len(s) == 64 && e == nil }
	return t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32 && t.TemplateId != 0 && t.TemplateId != math.MaxUint32 &&
		t.FirstBagSlot >= 3 && t.FirstBagSlot <= t.LastBagSlot && t.LastBagSlot <= 344 && t.StackLimit == 1 && t.UsableJobs != 0 && t.MinimumLevel != 0 && validHash(t.ResourceHash) &&
		!t.CashPackage && !t.CashPackageReward && !t.FateCollection && !t.AccountCommonInventory && !t.PetConsumable && !t.FatigueTimeMonthlyDeletion &&
		(t.ResourceExpiration == nil || t.PvfGiftItem && *t.ResourceExpiration > 0 && *t.ResourceExpiration <= math.MaxInt32) &&
		(t.PvfGiftItem || !t.AccountAfterFirstTrade && !t.InitialSealed && t.RelativeLifetimeSeconds == 0 && !t.DailyDeletion && t.WeeklyDeletionDay == nil) &&
		t.RelativeLifetimeSeconds <= math.MaxInt32 && (t.WeeklyDeletionDay == nil || *t.WeeklyDeletionDay <= 6) && len(t.WeaponQualification.Jobs) == 0 &&
		(t.MythologyHash == "" && t.MythologyOptions == 0 || validHash(t.MythologyHash) && t.MinimumLevel == 100 && (t.MythologyOptions == 3 || t.MythologyOptions == 4))
}

func checkPetMail(ctx context.Context, tx *sql.Tx) error {
	chars, err := characterJSON(ctx, tx, "gameplay_json")
	if err != nil {
		return err
	}
	for _, c := range chars {
		var p struct {
			Mailbox struct {
				Mails []struct {
					Attachments []struct {
						Claimed bool
						Asset   struct{ Template itemdata.ItemTemplate }
					}
				}
			}
		}
		if err = json.Unmarshal([]byte(c.value), &p); err != nil {
			return err
		}
		for _, mail := range p.Mailbox.Mails {
			for _, a := range mail.Attachments {
				if a.Claimed {
					continue
				}
				active, err := oldPetSupply(a.Asset.Template)
				if err != nil {
					return err
				}
				if active {
					return fmt.Errorf("character %d has historical pet supplies in mail escrow", c.id)
				}
			}
		}
	}
	return nil
}

type oldAssetChange struct {
	row, id            int64
	oldSlot, loc, slot int
	before, after      string
}

func convertInventory(ctx context.Context, tx *sql.Tx, version int) error {
	query := "SELECT rowid,character_id,template_json,location,slot,count FROM local_item_assets"
	if version == 17 {
		if err := checkPetMail(ctx, tx); err != nil {
			return err
		}
	} else {
		query += " WHERE location=0"
	}
	rows, err := tx.QueryContext(ctx, query+" ORDER BY character_id,slot,rowid")
	if err != nil {
		return err
	}
	defer rows.Close()
	var changes []oldAssetChange
	counts := map[int64]int{}
	for rows.Next() {
		var c oldAssetChange
		var id sql.NullInt64
		var loc int
		var count int64
		if err = rows.Scan(&c.row, &id, &c.before, &loc, &c.oldSlot, &count); err != nil {
			return err
		}
		var t *itemdata.ItemTemplate
		if err = json.Unmarshal([]byte(c.before), &t); err != nil || t == nil {
			return errors.New("invalid old inventory template")
		}
		c.id = id.Int64
		if version == 17 {
			yes, err := oldPetSupply(*t)
			if err != nil {
				return err
			}
			if !yes {
				continue
			}
			if !id.Valid || loc != 0 || !(c.oldSlot >= 3 && c.oldSlot <= 8 || c.oldSlot >= 65 && c.oldSlot <= 120) {
				return errors.New("old pet consumable outside original bag")
			}
			c.loc, c.slot = 7, 216+counts[c.id]
		} else {
			if t.Kind != 1 || !t.WornSlotIsNative || t.WornSlot != 32 {
				continue
			}
			if !id.Valid || !historicalMedal(*t) || count != 1 || c.oldSlot < 9 || c.oldSlot > 64 {
				return errors.New("old medal outside supported acquisition shape")
			}
			c.loc, c.slot = 38, counts[c.id]
		}
		if counts[c.id] >= 56 {
			return errors.New("historical items exceed their native inventory partition")
		}
		counts[c.id]++
		changes = append(changes, c)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, c := range changes {
		c.after = c.before
		if version == 17 {
			if err = tx.QueryRowContext(ctx, `SELECT json_set(?,'$.FirstBagSlot',216,'$.LastBagSlot',271,'$.PetConsumable',json('true'))`, c.before).Scan(&c.after); err != nil {
				return err
			}
			raw, err := savejson.Marshal(json.RawMessage(c.after))
			if err != nil {
				return err
			}
			c.after = string(raw)
		}
		changed, err := tx.ExecContext(ctx, "UPDATE local_item_assets SET template_json=?,location=?,slot=? WHERE rowid=? AND character_id=? AND template_json=? AND location=0 AND slot=?", c.after, c.loc, c.slot, c.row, c.id, c.before, c.oldSlot)
		if err != nil {
			return err
		}
		if n, e := changed.RowsAffected(); e != nil || n != 1 {
			return errors.New("historical inventory row changed during upgrade")
		}
	}
	return nil
}
