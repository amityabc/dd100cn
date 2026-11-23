package storage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

type CraftMethod byte

const (
	CraftReinforce CraftMethod = iota
	CraftAmplify
	CraftForge
	CraftEnchant
	CraftTicket
	CraftRemodel
)

type CraftCatalog struct {
	Definitions map[uint32]CraftEntry
	Quotes      []CraftStages
	Tickets     map[uint32]CraftTicketRule
	Enchants    map[uint32]CraftBead
	Destruction map[uint32]CraftDestruction
	NPCs        map[CraftMethod][]uint32
	Templates   map[uint32]json.RawMessage
}

type CraftEntry struct {
	Equipment CraftEquipment
	Quote     int
}

type CraftEquipment struct {
	TemplateID                                     uint32 `json:"TemplateId"`
	ResourceHash, EquipmentType                    string
	TargetEquipmentSlot, MinimumLevel, Rarity      int
	Grade, RemodelGroup                            int
	CanReinforce, CanAmplify, CanForge, CanRemodel bool
}

// QuoteGroup identifies equipment that shares one upgrade price and reward table.
func (e CraftEquipment) QuoteGroup() [5]int {
	return [5]int{e.MinimumLevel, e.Grade, e.Rarity, e.TargetEquipmentSlot, e.RemodelGroup}
}

type CraftStages struct {
	Reinforce, Amplify, Forge, Remodel []*CraftQuote
}

type CraftQuote struct {
	Gold                                    int64
	MaterialID                              uint32 `json:"MaterialId"`
	MaterialCount                           uint32
	FailureWeight, PenaltyType, PenaltyLoss int
	DestroyRewards                          map[uint32]uint32
	RemodelPenalties                        []struct{ Loss, Weight int }
	ResourceHash                            string
}

type CraftBead struct {
	BeadTemplateID                 uint32 `json:"BeadTemplateId"`
	CardTemplateID                 uint32 `json:"CardTemplateId"`
	UpgradeLevels                  []uint16
	EquipmentTypes                 []string
	TargetTemplateIDs              []uint32 `json:"TargetTemplateIds"`
	LimitedTemplateIDs             []uint32 `json:"LimitedTemplateIds"`
	ResourceHash, CardResourceHash string
}

type CraftTicketRule struct {
	TemplateID                                               uint32 `json:"TemplateId"`
	Kind, Level                                              byte
	ResourceHash                                             string
	ChancePercent, BookAction, BookOption, BookMaterialCount int
	BookMaximumUpgradeLevel, BookMinimumLevel                int
	RandomValues, BookTypeWeights                            []struct{ Value, Weight int }
}

type CraftRequest struct {
	Sequence, Opcode, Slot, SourceSlot     uint16
	Method                                 CraftMethod
	List, SourceList, Mode, RawMethod      byte
	TemplateID, SourceTemplateID           uint32
	Material, Protection, NPC, NPCMaterial int16
	Selection, BookAction                  int
}

type CraftResult struct {
	PreviousLevel, NewLevel, ResultCode, AmplificationType byte
	MaterialSlot                                           int16
	MaterialRemaining, MaterialID, CardID                  uint32
	CardUpgrade                                            byte
	Succeeded                                              bool
	Changes                                                []Asset
	Cleared                                                []uint16
	BreakRewards                                           []CommercePayment
}

func (x *CommerceTx) Craft(q CraftRequest, c CraftCatalog, now time.Time, access func(CraftMethod, int16) bool,
	limits map[byte]int64, weights map[uint32]int64) error {
	if q.List != 0 && q.List != 3 || q.Mode != 0 || q.Method > CraftRemodel || q.Material < -1 {
		return ErrInvalid
	}
	i := slices.IndexFunc(x.Inventory.Items, func(a Asset) bool {
		return a.Count == 1 && a.CharacterID == x.Character.ID && a.Location == q.List && a.Slot == q.Slot
	})
	if i < 0 {
		return ErrInvalid
	}
	a := x.Inventory.Items[i]
	t, err := moveTemplate(&a)
	var attrs materialAttributes
	if err != nil || json.Unmarshal(a.Attributes, &attrs) != nil || t.Kind == 2 ||
		q.List == 0 && q.Slot > t.bagEnd(x.Inventory.BagExpansion) || q.TemplateID != 0 && q.TemplateID != a.TemplateID ||
		attrs.Durability == 0 && t.MaximumDurability != 0 || !craftableAsset(a, attrs, q.Method == CraftRemodel) {
		return ErrInvalid
	}
	d, ok := c.Definitions[a.TemplateID]
	if !ok || d.Equipment.TemplateID != a.TemplateID || d.Equipment.ResourceHash != t.ResourceHash || d.Quote < 0 || d.Quote >= len(c.Quotes) {
		return ErrInvalid
	}
	material := -1
	if q.Material >= 0 {
		material = x.bagSource(uint16(q.Material))
	}
	if material >= 0 {
		if ticket, exists := c.Tickets[x.Inventory.Items[material].TemplateID]; exists {
			return x.craftTicket(q, i, material, a, t, attrs, ticket, d.Equipment, now)
		}
	}
	if q.Method == CraftTicket || q.Protection != -1 || q.NPCMaterial != -1 || q.Selection != -1 && q.Selection != 0 ||
		!access(q.Method, q.NPC) {
		return ErrInvalid
	}
	if q.Method == CraftEnchant {
		return x.craftBead(q, a, attrs, d.Equipment, c.Enchants, now)
	}
	stages := c.Quotes[d.Quote]
	old := attrs.Reinforcement
	var rows []*CraftQuote
	switch q.Method {
	case CraftReinforce:
		if !d.Equipment.CanReinforce || attrs.AmplificationType != 0 {
			return ErrInvalid
		}
		rows = stages.Reinforce
	case CraftAmplify:
		if !d.Equipment.CanAmplify || attrs.AmplificationType < 1 || attrs.AmplificationType > 4 || attrs.AmplificationValue == 0 {
			return ErrInvalid
		}
		rows = stages.Amplify
	case CraftForge:
		if !d.Equipment.CanForge || t.Kind != 0 {
			return ErrInvalid
		}
		old, rows = attrs.Forge, stages.Forge
	case CraftRemodel:
		if !d.Equipment.CanRemodel || attrs.AmplificationType != 0 {
			return ErrInvalid
		}
		rows = stages.Remodel
	}
	if attrs.Durability != t.MaximumDurability || int(old) >= len(rows) || rows[old] == nil {
		return ErrInvalid
	}
	quote := *rows[old]
	if quote.Gold < 0 || quote.MaterialID == 0 || quote.MaterialCount == 0 || quote.FailureWeight < 0 || quote.FailureWeight > 100000 ||
		quote.PenaltyType < 1 || quote.PenaltyType > 3 || quote.PenaltyLoss < 0 || len(quote.ResourceHash) != 64 {
		return ErrInvalid
	}
	if x.Inventory.Gold < quote.Gold {
		return ErrFunds
	}
	first, err := x.craftMaterials(q.Material, quote.MaterialID, quote.MaterialCount)
	if err != nil {
		return err
	}
	x.Inventory.Gold -= quote.Gold
	result := CraftResult{PreviousLevel: old, NewLevel: old + 1, MaterialSlot: int16(x.Inventory.Items[first].Slot),
		MaterialRemaining: x.Inventory.Items[first].Count, MaterialID: quote.MaterialID, Succeeded: true}
	breaks := quote.PenaltyType == 3
	if q.Method == CraftRemodel {
		breaks = slices.ContainsFunc(quote.RemodelPenalties, func(p struct{ Loss, Weight int }) bool { return p.Loss < 0 })
	}
	if breaks {
		if err = x.preflightCraftBreak(i, d.Equipment, quote, c, q.Method, now, limits, weights); err != nil {
			return err
		}
	}
	draw, err := giftRandom(100000)
	if err != nil {
		return err
	}
	rewardSeed, err := giftRandom(math.MaxInt32)
	if err != nil {
		return err
	}
	if draw < uint64(quote.FailureWeight) {
		result.Succeeded = false
		result.ResultCode = byte(quote.PenaltyType)
		result.NewLevel = old
		switch {
		case q.Method == CraftRemodel:
			loss, err := craftRemodelLoss(quote, rewardSeed)
			if err != nil {
				return err
			}
			result.ResultCode = 1
			if loss < 0 {
				result.ResultCode, result.NewLevel = 3, 0
			} else if loss > 0 {
				result.ResultCode, result.NewLevel = 2, byte(max(int(old)-loss, 0))
			}
		case quote.PenaltyType == 2:
			result.NewLevel = byte(max(0, int(old)-quote.PenaltyLoss))
		case quote.PenaltyType == 3:
			result.NewLevel = 0
		}
	}
	if result.ResultCode == 3 {
		if err = x.debitAsset(i, 1); err != nil {
			return err
		}
		if err = x.grantCraftBreak(d.Equipment, quote, c, q.Method, now); err != nil {
			return err
		}
		for _, grant := range x.Grants {
			result.BreakRewards = append(result.BreakRewards, CommercePayment{TemplateID: grant.TemplateID, Count: grant.Count})
		}
	} else {
		if q.Method == CraftForge {
			attrs.Forge = result.NewLevel
		} else {
			attrs.Reinforcement = result.NewLevel
		}
		if err = x.persistCraftAttrs(a, attrs); err != nil {
			return err
		}
	}
	x.craft = &result
	return x.recordCraft(q, a, result, quote, draw, rewardSeed, "")
}

func craftBagChanges(before, after Inventory) ([]Asset, []uint16) {
	old := map[uint16]Asset{}
	for _, a := range before.Items {
		if a.Location == 0 {
			old[a.Slot] = a
		}
	}
	var updates []Asset
	for _, a := range after.Items {
		if a.Location != 0 {
			continue
		}
		b, ok := old[a.Slot]
		if !ok || a.Count != b.Count || a.TemplateID != b.TemplateID ||
			!bytes.Equal(a.Core, b.Core) || !bytes.Equal(a.Attributes, b.Attributes) {
			updates = append(updates, a)
		}
		delete(old, a.Slot)
	}
	var cleared []uint16
	for slot := range old {
		cleared = append(cleared, slot)
	}
	return updates, cleared
}

func craftableAsset(a Asset, attrs materialAttributes, remodel bool) bool {
	if remodel && len(a.Detail) == 5 {
		a.Detail = nil
	}
	return attrs.MagicSeal == nil && repairable(a, attrs)
}

func (x *CommerceTx) craftMaterials(preferred int16, id, count uint32) (int, error) {
	var positions []int
	for i, a := range x.Inventory.Items {
		if a.Location != 0 || a.TemplateID != id || a.Count == 0 {
			continue
		}
		var t ordinaryTemplate
		var v materialAttributes
		if json.Unmarshal(a.Template, &t) != nil || json.Unmarshal(a.Attributes, &v) != nil {
			return -1, ErrInvalid
		}
		if t.Kind == 2 && !v.Locked && v.ExpiresAt == nil {
			positions = append(positions, i)
		}
	}
	slices.SortFunc(positions, func(a, b int) int {
		left, right := x.Inventory.Items[a].Slot, x.Inventory.Items[b].Slot
		if left == right {
			return 0
		}
		if int16(left) == preferred {
			return -1
		}
		if int16(right) == preferred {
			return 1
		}
		return int(left) - int(right)
	})
	if len(positions) == 0 || preferred >= 0 && int16(x.Inventory.Items[positions[0]].Slot) != preferred {
		return -1, ErrFunds
	}
	for _, i := range positions {
		n := min(count, x.Inventory.Items[i].Count)
		if err := x.debitAsset(i, n); err != nil {
			return -1, err
		}
		count -= n
		if count == 0 {
			return positions[0], nil
		}
	}
	return -1, ErrFunds
}

func (x *CommerceTx) craftBead(q CraftRequest, target Asset, attrs materialAttributes, d CraftEquipment, rules map[uint32]CraftBead, now time.Time) error {
	i := x.bagSource(q.SourceSlot)
	if i < 0 || q.SourceList != 0 || q.Material != -1 {
		return ErrInvalid
	}
	bead := x.Inventory.Items[i]
	dye, ok := rules[bead.TemplateID]
	var t materialDefinition
	var resource struct{ ResourceHash string }
	var v materialAttributes
	if !ok || bead.CharacterID != x.Character.ID || bead.UID == target.UID || len(bead.Core) != 119 ||
		json.Unmarshal(bead.Template, &t) != nil || json.Unmarshal(bead.Template, &resource) != nil || json.Unmarshal(bead.Attributes, &v) != nil ||
		t.Kind != 2 || dye.ResourceHash != resource.ResourceHash || v.ExpiresAt != nil && *v.ExpiresAt <= now.Unix() ||
		v.EnchantUpgradeCount != bead.Core[18] || !slices.Contains(dye.UpgradeLevels, uint16(v.EnchantUpgradeCount)) ||
		!slices.ContainsFunc(dye.EquipmentTypes, func(s string) bool { return strings.EqualFold(d.EquipmentType, s) }) ||
		len(dye.TargetTemplateIDs) != 0 && !slices.Contains(dye.TargetTemplateIDs, d.TemplateID) ||
		len(dye.LimitedTemplateIDs) != 0 && !slices.Contains(dye.LimitedTemplateIDs, d.TemplateID) {
		return ErrInvalid
	}
	snapshot := bead
	snapshot.Core = bytes.Clone(bead.Core)
	upgrade := v.EnchantUpgradeCount
	snapshot.Core[18], v.EnchantUpgradeCount = 0, 0
	ok, err := ordinaryNewCore(snapshot, t, v)
	if err != nil || !ok {
		return ErrInvalid
	}
	attrs.Enchantment, attrs.EnchantUpgradeCount = dye.CardTemplateID, upgrade
	if err = x.debitAsset(i, 1); err != nil {
		return err
	}
	if err = x.persistCraftAttrs(target, attrs); err != nil {
		return err
	}
	result := CraftResult{PreviousLevel: attrs.Reinforcement, NewLevel: attrs.Reinforcement, Succeeded: true, MaterialID: bead.TemplateID,
		MaterialSlot: int16(bead.Slot), MaterialRemaining: bead.Count - 1, CardID: dye.CardTemplateID, CardUpgrade: upgrade}
	x.craft = &result
	quote := CraftQuote{MaterialID: bead.TemplateID, MaterialCount: 1, PenaltyType: 1, ResourceHash: dye.ResourceHash}
	return x.recordCraft(q, target, result, quote, 0, 0, dye.CardResourceHash)
}

func (x *CommerceTx) persistCraftAttrs(a Asset, v materialAttributes) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(a.Attributes, &fields) != nil || fields == nil {
		return ErrInvalid
	}
	values := map[string]any{"Reinforcement": v.Reinforcement, "Forge": v.Forge, "AmplificationType": v.AmplificationType,
		"AmplificationValue": v.AmplificationValue, "Enchantment": v.Enchantment, "EnchantUpgradeCount": v.EnchantUpgradeCount}
	for k, n := range values {
		var err error
		fields[k], err = json.Marshal(n)
		if err != nil {
			return err
		}
	}
	core := bytes.Clone(a.Core)
	core[10], core[0x57], core[19], core[18] = v.Reinforcement, v.Forge, v.AmplificationType, v.EnchantUpgradeCount
	binary.LittleEndian.PutUint16(core[20:], v.AmplificationValue)
	binary.LittleEndian.PutUint32(core[14:], v.Enchantment)
	return x.updateCraftAsset(a, fields, core)
}

func (x *CommerceTx) recordCraft(q CraftRequest, a Asset, result CraftResult, quote CraftQuote, draw, seed uint64, enchantHash string) error {
	key := fmt.Sprintf("equipment-craft:%d:%d", x.Character.ID, x.Character.Revision+1)
	d, err := moveTemplate(&a)
	if err != nil {
		return err
	}
	input, err := json.Marshal(struct {
		Command                 CraftRequest
		InstanceID              string
		Draw, RewardSeed        uint64
		Quote                   CraftQuote
		SourceHash, EnchantHash string
	}{q, materialGUID(a.UID), draw, seed, quote, d.ResourceHash, enchantHash})
	if err != nil {
		return err
	}
	out, err := json.Marshal(result)
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct{ Key, Input, Result string }{key, string(input), string(out)})
	if err != nil {
		return err
	}
	_, err = x.tx.ExecContext(x.ctx, `INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)`, x.Character.ID, key, string(body))
	return err
}
