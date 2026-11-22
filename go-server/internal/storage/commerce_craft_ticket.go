package storage

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"time"
)

func (x *CommerceTx) craftTicket(q CraftRequest, i, j int, target Asset, t ordinaryTemplate, attrs materialAttributes, ticket CraftTicketRule, d CraftEquipment, now time.Time) error {
	if q.Selection < -1 || q.Selection > 6 || i == j || ticket.BookMaterialCount < 1 || ticket.Level > 31 || ticket.ChancePercent < 0 || ticket.ChancePercent > 100 {
		return ErrInvalid
	}
	material := x.Inventory.Items[j]
	var mt ordinaryTemplate
	var v materialAttributes
	count := uint32(ticket.BookMaterialCount)
	if json.Unmarshal(material.Template, &mt) != nil || json.Unmarshal(material.Attributes, &v) != nil ||
		mt.Kind != 2 || mt.TemplateID != ticket.TemplateID || mt.ResourceHash != ticket.ResourceHash || material.Count < count ||
		q.SourceTemplateID != 0 && q.SourceTemplateID != material.TemplateID ||
		len(material.Core) != 119 || len(material.Detail) != 0 || v.Locked || v.ExpiresAt != nil && *v.ExpiresAt <= now.Unix() ||
		material.Core[10] != v.Reinforcement || material.Core[19] != v.AmplificationType ||
		binary.LittleEndian.Uint16(material.Core[20:]) != v.AmplificationValue ||
		ticket.Kind != 2 && attrs.Durability != t.MaximumDurability || q.Opcode == 205 && ticket.Kind != 2 {
		return ErrInvalid
	}
	draw, err := giftRandom(100000)
	if err != nil {
		return err
	}
	seed, err := giftRandom(math.MaxInt32)
	if err != nil {
		return err
	}
	old := attrs.Reinforcement
	success := ticket.Kind == 2 || draw < uint64(ticket.ChancePercent)*1000
	switch ticket.Kind {
	case 0:
		if q.Method == CraftAmplify || !d.CanReinforce || attrs.AmplificationType != 0 {
			return ErrInvalid
		}
		if success {
			attrs.Reinforcement = ticket.Level
		}
	case 1:
		if q.Method == CraftReinforce || !d.CanAmplify || attrs.AmplificationType < 1 || attrs.AmplificationType > 4 {
			return ErrInvalid
		}
		if success {
			attrs.Reinforcement = ticket.Level
		}
	case 2:
		action := ticket.BookAction
		if q.BookAction >= 0 {
			action = q.BookAction
		}
		if !d.CanAmplify || d.MinimumLevel < ticket.BookMinimumLevel || d.Rarity < 2 || d.Rarity > 7 ||
			d.TargetEquipmentSlot < 12 || d.TargetEquipmentSlot > 25 || d.TargetEquipmentSlot == 13 ||
			action != ticket.BookAction || action < 0 || action > 2 ||
			action == 0 && (attrs.AmplificationType != 0 || attrs.Reinforcement != 0) ||
			action == 1 && (attrs.AmplificationType < 1 || attrs.AmplificationType > 4 || int(attrs.Reinforcement) > ticket.BookMaximumUpgradeLevel) {
			return ErrInvalid
		}
		typ, err := craftBookType(ticket, q.Selection, attrs.AmplificationType, seed)
		if err != nil {
			return err
		}
		attrs.AmplificationType, attrs.AmplificationValue = typ, 0
		if action == 2 {
			level, err := craftWeighted(ticket.RandomValues, draw, true, 0, 31)
			if err != nil {
				return err
			}
			attrs.Reinforcement = level
		}
	default:
		return ErrInvalid
	}
	if err = x.debitAsset(j, count); err != nil {
		return err
	}
	if err = x.persistCraftAttrs(target, attrs); err != nil {
		return err
	}
	result := CraftResult{PreviousLevel: old, NewLevel: attrs.Reinforcement, AmplificationType: attrs.AmplificationType,
		MaterialSlot: int16(material.Slot), MaterialRemaining: material.Count - count, MaterialID: material.TemplateID, Succeeded: success}
	if !success {
		result.ResultCode = 1
	}
	x.craft = &result
	quote := CraftQuote{MaterialID: material.TemplateID, MaterialCount: count, PenaltyType: 1, ResourceHash: ticket.ResourceHash}
	return x.recordCraft(q, target, result, quote, draw, seed, "")
}

func craftBookType(t CraftTicketRule, selected int, old byte, seed uint64) (byte, error) {
	if t.BookOption == 6 {
		if selected != 6 {
			return 0, ErrInvalid
		}
		return craftWeighted(t.BookTypeWeights, seed, false, 1, 4)
	}
	typ := t.BookOption
	if typ == 5 {
		typ = selected
	}
	if typ < 1 || typ > 4 {
		if t.BookOption != 5 || selected != -1 && selected != 0 {
			return 0, ErrInvalid
		}
		var candidates []byte
		for i := byte(1); i <= 4; i++ {
			if i != old {
				candidates = append(candidates, i)
			}
		}
		return candidates[seed%uint64(len(candidates))], nil
	}
	if t.BookOption != 5 && selected != 0 && selected != typ || byte(typ) == old {
		return 0, ErrInvalid
	}
	return byte(typ), nil
}

func craftWeighted(rows []struct{ Value, Weight int }, seed uint64, fraction bool, low, high int) (byte, error) {
	var total uint64
	for _, r := range rows {
		if r.Weight < 0 || r.Value < low || r.Value > high {
			return 0, ErrInvalid
		}
		total += uint64(r.Weight)
	}
	if total == 0 || total > math.MaxInt32 {
		return 0, ErrInvalid
	}
	draw := seed % total
	if fraction {
		draw = seed * total / 100000
	}
	for _, r := range rows {
		if draw < uint64(r.Weight) {
			return byte(r.Value), nil
		}
		draw -= uint64(r.Weight)
	}
	return 0, ErrInvalid
}
