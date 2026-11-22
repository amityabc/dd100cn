package storage

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
)

type RepairDefinition struct {
	TemplateID         uint32
	ResourceHash       string
	Grade, RepairPrice int
	MaximumDurability  uint16
	CostRate           float32
	SupportsCrafted    bool
}

type RepairRequest struct {
	Seq             uint16
	List            byte
	Slot, NPC, Mode uint16
	Quick           byte
}

func (x *CommerceTx) Repair(q RepairRequest, catalog map[uint32]RepairDefinition, source *OculusRepairSource) error {
	var p useState
	if err := json.Unmarshal(x.Character.Gameplay, &p); err != nil {
		return err
	}
	if !p.Initialized || p.Progression.HP == 0 {
		return ErrPhase
	}
	type repaired struct {
		InstanceId         string
		PreviousDurability uint16
		Definition         RepairDefinition
	}
	var repairedRows []repaired
	var goldCost int64
	matched := q.Slot == math.MaxUint16
	for _, a := range x.Inventory.Items {
		if a.CharacterID != x.Character.ID || a.Location != 0 && a.Location != 3 ||
			q.Slot != math.MaxUint16 && (q.List != a.Location || q.Slot != a.Slot) {
			continue
		}
		if a.Source != "item" && a.Source != "weapon" {
			continue
		}
		t, err := moveTemplate(&a)
		if err != nil {
			return err
		}
		if t.Kind == 2 {
			continue
		}
		matched = true
		var attrs map[string]json.RawMessage
		var v materialAttributes
		if err = json.Unmarshal(a.Attributes, &v); err != nil {
			return err
		}
		if a.Weapon != nil && (v.Durability != a.Weapon.Durability || v.QualitySeed != a.Weapon.QualitySeed) {
			return ErrInvalid
		}
		if v.Durability > t.MaximumDurability {
			return ErrInvalid
		}
		if v.Durability == t.MaximumDurability {
			continue
		}
		d, ok := catalog[a.TemplateID]
		if !ok || d.ResourceHash != t.ResourceHash || d.TemplateID != a.TemplateID || d.MaximumDurability != t.MaximumDurability ||
			a.Location == 0 && a.Slot > t.bagEnd(x.Inventory.BagExpansion) || !repairable(a, v) {
			return ErrInvalid
		}
		cost, err := repairCost(d, v.Durability)
		if err != nil {
			return err
		}
		goldCost += int64(cost)
		if goldCost > x.Inventory.Gold {
			return ErrFunds
		}
		if err = json.Unmarshal(a.Attributes, &attrs); err != nil || attrs == nil {
			return ErrInvalid
		}
		attrs["Durability"] = json.RawMessage(fmt.Sprint(t.MaximumDurability))
		updated, err := json.Marshal(attrs)
		if err != nil {
			return err
		}
		if a.Weapon != nil {
			weapon := *a.Weapon
			weapon.Durability = t.MaximumDurability
			a.Weapon = &weapon
		}
		w, err := json.Marshal(a.Weapon)
		if err != nil {
			return err
		}
		if _, err = x.tx.ExecContext(x.ctx, "UPDATE s9a1.assets SET attributes_json=$2::json,weapon_facts=$3::json WHERE instance_id=$1", a.UID, string(updated), string(w)); err != nil {
			return err
		}
		repairedRows = append(repairedRows, repaired{materialGUID(a.UID), v.Durability, d})
	}
	if !matched {
		return ErrInvalid
	}
	if len(repairedRows) == 0 {
		x.noChange = true
		return nil
	}
	x.Inventory.Gold -= goldCost
	key := fmt.Sprintf("equipment-repair:%d:%d", x.Character.ID, x.Character.Revision+1)
	input, err := json.Marshal(struct {
		Request       RepairRequest
		Items         []repaired
		DungeonSource *OculusRepairSource `json:",omitempty"`
	}{q, repairedRows, source})
	if err != nil {
		return err
	}
	result, err := json.Marshal(struct{ GoldCost int64 }{goldCost})
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct{ Key, Input, Result string }{key, string(input), string(result)})
	if err != nil {
		return err
	}
	_, err = x.tx.ExecContext(x.ctx, `INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)`, x.Character.ID, key, string(payload))
	return err
}

func repairable(a Asset, v materialAttributes) bool {
	if len(a.Core) != 119 || len(a.Detail) != 0 || a.Count != 1 || v.Locked || v.ExpiresAt != nil || v.Mythology != nil ||
		v.Reinforcement > 31 || v.Forge > 8 || v.AmplificationType > 4 || v.AmplificationType == 0 && v.AmplificationValue != 0 ||
		v.Enchantment == 0 && v.EnchantUpgradeCount != 0 ||
		a.Core[10] != v.Reinforcement || a.Core[0x57] != v.Forge || a.Core[19] != v.AmplificationType ||
		binary.LittleEndian.Uint16(a.Core[20:]) != v.AmplificationValue ||
		binary.LittleEndian.Uint32(a.Core[14:]) != v.Enchantment || a.Core[18] != v.EnchantUpgradeCount {
		return false
	}
	var d materialDefinition
	if json.Unmarshal(a.Template, &d) != nil {
		return false
	}
	for i, v := range a.Core[10:] {
		offset := i + 10
		if v == 0 || offset >= 10 && offset <= 21 || offset >= 60 && offset <= 68 || offset == 0x57 {
			continue
		}
		if !(d.CashPackage || d.CashPackageReward || d.PvfGiftItem) || offset < 56 || offset > 59 {
			return false
		}
	}
	if d.InitialSealed {
		return v.Binding == 1 && a.Core[13] == 1
	}
	return v.Binding == 0 && a.Core[13] == 0
}

func repairCost(d RepairDefinition, current uint16) (uint32, error) {
	if d.Grade < 0 || d.Grade > 255 || d.RepairPrice < 0 || int64(d.RepairPrice)*int64(d.Grade+5) > math.MaxInt32 ||
		d.MaximumDurability == 0 || current > d.MaximumDurability || d.CostRate <= 0 || d.CostRate > 1 {
		return 0, ErrInvalid
	}
	value := d.RepairPrice * (d.Grade + 5) / 10
	cost := float32(value) * d.CostRate
	cost /= float32(d.MaximumDurability)
	cost *= float32(d.MaximumDurability - current)
	cost += 0.5
	if math.IsNaN(float64(cost)) || cost >= 2147483648 {
		return 0, ErrInvalid
	}
	return uint32(cost), nil
}
