package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"
)

var sharedMaterialIDs = [...]uint32{3033, 3034, 3035, 3036, 3037, 3262, 10100115, 10100116, 10099773, 10099774, 10099775, 10158124}

func SharedMaterialSlot(template uint32) (uint16, bool) {
	i := slices.Index(sharedMaterialIDs[:], template)
	if i < 0 {
		return 0, false
	}
	return uint16(363 + i), true
}

type materialDefinition struct {
	Kind                                                       int
	TemplateID, StackLimit                                     uint32
	CashPackage, CashPackageReward, PvfGiftItem, InitialSealed bool
	RelativeLifetimeSeconds                                    uint32
	DailyDeletion, FatigueTimeMonthlyDeletion                  bool
	WeeklyDeletionDay                                          *uint8
	ResourceExpiration                                         *int64
}

type materialAttributes struct {
	QualitySeed, Enchantment                                              uint32
	Durability, AmplificationValue                                        uint16
	Reinforcement, Forge, AmplificationType, EnchantUpgradeCount, Binding uint8
	Locked, InitialAmplification                                          bool
	MagicSeal, Mythology                                                  *json.RawMessage
	ExpiresAt                                                             *int64
}

// LocalSharedMaterialRules.Plain uses the ordinary-new core policy, including
// the saved deadline for verified PVF gift and cash templates.
func plainMaterial(a Asset, t materialDefinition) (bool, error) {
	var v materialAttributes
	if err := json.Unmarshal(a.Attributes, &v); err != nil {
		return false, err
	}
	if t.Kind != 2 || v.QualitySeed != 0 || v.Durability != 0 {
		return false, nil
	}
	return ordinaryNewCore(a, t, v)
}

func ordinaryNewCore(a Asset, t materialDefinition, v materialAttributes) (bool, error) {
	var binding uint8
	if t.InitialSealed {
		binding = 1
	}
	if len(a.Detail) != 0 || len(a.Core) != 119 || a.Core[13] != v.Binding || v.Binding != binding ||
		v.Reinforcement != 0 || v.Forge != 0 || v.AmplificationType != 0 || v.AmplificationValue != 0 || v.Enchantment != 0 || v.EnchantUpgradeCount != 0 ||
		v.InitialAmplification || v.Locked || v.MagicSeal != nil || v.Mythology != nil {
		return false, nil
	}
	gift := t.CashPackage || t.CashPackageReward || t.PvfGiftItem
	if gift {
		if t.ResourceExpiration == nil && !t.PvfGiftItem || t.ResourceExpiration != nil && (*t.ResourceExpiration <= 0 || *t.ResourceExpiration > math.MaxInt32) {
			return false, nil
		}
		dated := t.RelativeLifetimeSeconds > 0 || t.DailyDeletion || t.WeeklyDeletionDay != nil || t.FatigueTimeMonthlyDeletion
		var expires int64
		if v.ExpiresAt != nil {
			expires = *v.ExpiresAt
		}
		if dated {
			if expires <= 0 || expires > math.MaxInt32 || t.ResourceExpiration != nil && expires > *t.ResourceExpiration {
				return false, nil
			}
		} else if (v.ExpiresAt == nil) != (t.ResourceExpiration == nil) || t.ResourceExpiration != nil && expires != *t.ResourceExpiration {
			return false, nil
		}
		if int64(int32(binary.LittleEndian.Uint32(a.Core[56:60]))) != expires {
			return false, nil
		}
	} else if v.ExpiresAt != nil {
		return false, nil
	}
	for i, b := range a.Core[10:] {
		offset := i + 10
		if b != 0 && offset != 11 && offset != 12 && !(gift && (offset == 13 || offset >= 56 && offset <= 59)) {
			return false, nil
		}
	}
	return true, nil
}

// Records in older saves can omit fields added by System.Text.Json over time.
// Reproduce their C# defaults in the generated history, keep future fields and
// keep the original target template and core in the asset table.
const materialTemplateDefaults = `{"WornSlotIsNative":false,"CashPackage":false,"CashPackageReward":false,"PvfGiftItem":false,"AccountAfterFirstTrade":false,"MonsterCard":false,"InitialSealed":false,"ResourceExpiration":null,"FateCollection":false,"AccountCommonInventory":false,"PetConsumable":false,"CharmEnergy":false,"WeaponQualification":{"Verified":false,"Jobs":[],"Valid":true},"RelativeLifetimeSeconds":0,"DailyDeletion":false,"WeeklyDeletionDay":null,"FatigueTimeMonthlyDeletion":false}`
const materialAttributeDefaults = `{"QualitySeed":0,"Durability":0,"Reinforcement":0,"Forge":0,"AmplificationType":0,"AmplificationValue":0,"Enchantment":0,"Binding":0,"ExpiresAt":null,"Locked":false,"EnchantUpgradeCount":0}`

func frozenMaterialObject(raw json.RawMessage, defaults string) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(defaults), &out); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil, ErrInvalid
	}
	return out, nil
}

func compatibleMaterial(a, b Asset) (bool, error) {
	if a.TemplateID != b.TemplateID || len(a.Core) != 119 || len(b.Core) != 119 || !bytes.Equal(a.Core[10:], b.Core[10:]) || !bytes.Equal(a.Detail, b.Detail) {
		return false, nil
	}
	for i, defs := range []string{materialTemplateDefaults, materialAttributeDefaults} {
		x, y := a.Template, b.Template
		if i == 1 {
			x, y = a.Attributes, b.Attributes
		}
		if equalJSON(x, y) {
			continue
		}
		p, err := frozenMaterialObject(x, defs)
		if err != nil {
			return false, err
		}
		q, err := frozenMaterialObject(y, defs)
		if err != nil {
			return false, err
		}
		if i == 0 {
			// The sole compatibility exception is unverified-empty versus
			// verified-empty permission on a stackable template.
			var left, right qualification
			if json.Unmarshal(p["WeaponQualification"], &left) != nil || json.Unmarshal(q["WeaponQualification"], &right) != nil {
				return false, ErrInvalid
			}
			if left.Jobs != nil && right.Jobs != nil && len(left.Jobs) == 0 && len(right.Jobs) == 0 {
				delete(p, "WeaponQualification")
				delete(q, "WeaponQualification")
			}
		}
		px, _ := json.Marshal(p)
		qx, _ := json.Marshal(q)
		if !equalJSON(px, qx) {
			return false, nil
		}
	}
	return true, nil
}

func materialGUID(uid string) string {
	u := strings.ToLower(uid) // assets.instance_id is a checked N-format GUID.
	return u[:8] + "-" + u[8:12] + "-" + u[12:16] + "-" + u[16:20] + "-" + u[20:]
}

type materialSource struct {
	InstanceId     string
	Template       map[string]json.RawMessage
	Count          uint32
	Location       uint8
	Slot           uint16
	Attributes     map[string]json.RawMessage
	Raw119, Detail []uint16 // C# ImmutableArray<byte> serializes as JSON integers.
}

type materialFact struct {
	Source materialSource
	Target string
}
type materialMove struct {
	source, target string
	slot           uint16
	count          uint32
}

func materialImports(c Character, in Inventory) ([]materialMove, map[string]materialFact, error) {
	fixed := map[uint16]Asset{}
	for _, a := range in.Items {
		if a.Location == 0 && a.Slot >= 363 && a.Slot <= 374 {
			fixed[a.Slot] = a
		}
	}
	var profile struct{ SharedMaterialImports json.RawMessage }
	if err := json.Unmarshal(c.Gameplay, &profile); err != nil {
		return nil, nil, err
	}
	var previous map[string]json.RawMessage
	if len(profile.SharedMaterialImports) > 0 {
		if err := json.Unmarshal(profile.SharedMaterialImports, &previous); err != nil || previous == nil {
			return nil, nil, ErrInvalid
		}
	}
	var moves []materialMove
	var facts map[string]materialFact
	for _, a := range in.Items { // readInventory uses the old location,slot order.
		slot, ok := SharedMaterialSlot(a.TemplateID)
		if !ok || a.Source != "item" || a.Location != 0 && a.Location != 2 && a.Location != 12 || a.Location == 0 && a.Slot == slot {
			continue
		}
		var t materialDefinition
		if err := json.Unmarshal(a.Template, &t); err != nil {
			return nil, nil, err
		}
		if t.TemplateID != a.TemplateID || a.Count == 0 || a.Count > t.StackLimit {
			return nil, nil, ErrInvalid
		}
		plain, err := plainMaterial(a, t)
		if err != nil {
			return nil, nil, err
		}
		if !plain {
			continue
		}
		guid := materialGUID(a.UID)
		if _, used := previous[guid]; used {
			return nil, nil, ErrConflict
		}
		move := materialMove{source: a.UID, slot: slot, count: a.Count}
		target, exists := fixed[slot]
		if exists {
			same, err := compatibleMaterial(target, a)
			if err != nil {
				return nil, nil, err
			}
			if !same || uint64(target.Count)+uint64(a.Count) > uint64(t.StackLimit) {
				continue
			}
			move.target = target.UID
			target.Count += a.Count
		} else {
			target = a
			target.Location, target.Slot, target.Source, target.CharacterID = 0, slot, "shared", 0
		}
		fixed[slot] = target
		frozen, err := frozenMaterialObject(a.Template, materialTemplateDefaults)
		if err != nil {
			return nil, nil, err
		}
		attrs, err := frozenMaterialObject(a.Attributes, materialAttributeDefaults)
		if err != nil {
			return nil, nil, err
		}
		core := make([]uint16, len(a.Core))
		for i, b := range a.Core {
			core[i] = uint16(b)
		}
		if facts == nil {
			facts = make(map[string]materialFact)
		}
		facts[guid] = materialFact{materialSource{guid, frozen, a.Count, a.Location, a.Slot, attrs, core, []uint16{}}, materialGUID(target.UID)}
		moves = append(moves, move)
	}
	return moves, facts, nil
}

type townInventory struct {
	character Character
	inventory Inventory
}

// PrepareTownInventory locks the selection, promotes it to TownLoading and
// imports the twelve ordinary shared materials before projecting the first bag.
func (s *Store) PrepareTownInventory(ctx context.Context, l Lease, id uint16, rev int64) (Character, Inventory, error) {
	out, err := writeTx(ctx, s.db, func(tx *sql.Tx) (townInventory, error) {
		var out townInventory
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return out, err
		}
		if id == 0 || r.SelectedID != id || r.Phase != Selected && r.Phase != TownLoading {
			return out, ErrPhase
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, id))
		if err != nil {
			return out, err
		}
		in, err := readInventory(ctx, tx, l.Account, r)
		if err != nil {
			return out, err
		}
		if r.Phase == Selected {
			if c.Revision != rev {
				return out, ErrConflict
			}
			moves, facts, err := materialImports(c, in)
			if err != nil {
				return out, err
			}
			for _, m := range moves {
				if m.target == "" {
					_, err = tx.ExecContext(ctx, `UPDATE s9a1.assets SET character_id=NULL,source='shared',location=0,slot=$3 WHERE account=$1 AND instance_id=$2`, l.Account, m.source, m.slot)
				} else {
					_, err = tx.ExecContext(ctx, `UPDATE s9a1.assets SET count=count+$3 WHERE account=$1 AND instance_id=$2`, l.Account, m.target, m.count)
					if err == nil {
						_, err = tx.ExecContext(ctx, `DELETE FROM s9a1.assets WHERE account=$1 AND instance_id=$2`, l.Account, m.source)
					}
				}
				if err != nil {
					return out, err
				}
			}
			if len(moves) > 0 {
				history, err := json.Marshal(facts)
				if err != nil {
					return out, err
				}
				c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters SET revision=revision+1,
					gameplay_json=jsonb_set(gameplay_json::jsonb,'{SharedMaterialImports}',coalesce(gameplay_json::jsonb->'SharedMaterialImports','{}'::jsonb)||$4::jsonb)::json
					WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns, l.Account, id, rev, string(history)))
				if err != nil {
					return out, err
				}
				r.AssetRevision++
				in, err = readInventory(ctx, tx, l.Account, r)
				if err != nil {
					return out, err
				}
			}
			c, in, err = s.expireTownInventory(ctx, tx, l, c, in, &r, time.Now())
			if err != nil {
				return out, err
			}
			_, err = tx.ExecContext(ctx, "UPDATE s9a1.accounts SET phase=$2,asset_revision=$3 WHERE account=$1", l.Account, TownLoading, r.AssetRevision)
			if err != nil {
				return out, err
			}
		}
		characters := []Character{c}
		if err = loadWorn(ctx, tx, l.Account, characters); err != nil {
			return out, err
		}
		return townInventory{characters[0], in}, nil
	})
	return out.character, out.inventory, err
}
