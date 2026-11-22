package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

type FateBoxesCatalog struct {
	Stages, Exchange                      map[uint32]FateBoxRule
	StageHash, ExchangeHash               string
	StageReactivated, ExchangeReactivated bool
}

type FateBoxRule struct {
	Source, Reward   json.RawMessage
	RewardCount      uint32
	RewardAttachType byte
}

var ErrFateBoxRejected = errors.New("activity box cannot be opened")

func (x *CommerceTx) OpenFateBox(slot uint16, count uint32, native bool, d FateBoxesCatalog, now time.Time) (bool, error) {
	j := x.bagSource(slot)
	if j < 0 {
		return false, nil
	}
	a := x.Inventory.Items[j]
	rule, isStage := d.Stages[a.TemplateID]
	other, isExchange := d.Exchange[a.TemplateID]
	if !isStage && !isExchange {
		return false, nil
	}
	reject := func() (bool, error) { return true, ErrFateBoxRejected }
	if !native || count == 0 || count > math.MaxInt32 || count > a.Count {
		return reject()
	}
	hash, active := d.StageHash, d.StageReactivated
	common := isStage && equalJSON(a.Template, rule.Source)
	if !common {
		if !isExchange {
			return reject()
		}
		rule, hash, active = other, d.ExchangeHash, d.ExchangeReactivated
	}
	i, _, _, _, err := x.giftSource(slot, now)
	if err != nil {
		if err == ErrInvalid {
			return reject()
		}
		return true, err
	}
	var source, reward ordinaryTemplate
	total := uint64(rule.RewardCount) * uint64(count)
	if i != j || !active || !fateExchangeHash(hash) || !equalJSON(a.Template, rule.Source) || json.Unmarshal(rule.Source, &source) != nil ||
		json.Unmarshal(rule.Reward, &reward) != nil || source.Kind != 2 || source.AccountCommonInventory || source.FateCollection ||
		reward.Kind != 2 || reward.FateCollection || reward.AccountCommonInventory != common || total == 0 || total > math.MaxInt32 ||
		rule.RewardAttachType != 1 && rule.RewardAttachType != 5 {
		return reject()
	}
	sourceID, err := uuid.Parse(a.UID)
	if err != nil || sourceID == uuid.Nil || len(a.Core) != 119 {
		return reject()
	}
	frozen := struct {
		InstanceId     string
		Template       json.RawMessage
		Count          uint32
		Location       byte
		Slot           uint16
		Attributes     json.RawMessage
		Raw119, Detail []uint16
	}{sourceID.String(), a.Template, a.Count, a.Location, a.Slot, a.Attributes, make([]uint16, len(a.Core)), make([]uint16, len(a.Detail))}
	for i, value := range a.Core {
		frozen.Raw119[i] = uint16(value)
	}
	for i, value := range a.Detail {
		frozen.Detail[i] = uint16(value)
	}
	input, err := json.Marshal(struct {
		ResourceHash string
		Source       any
		Count        uint32
	}{hash, frozen, count})
	if err != nil {
		return true, err
	}
	before := x.Inventory
	before.Items = append([]Asset(nil), before.Items...)
	if err = x.debitAsset(j, count); err != nil {
		return true, err
	}
	if err = x.grantCommerceItem(rule.Reward, uint32(total), now, true); err != nil {
		return true, err
	}
	x.gift = &GiftResult{TemplateID: source.TemplateID, Consumed: count, FateBox: true, Common: common,
		Rewards: []GiftResultRow{{reward.TemplateID, uint32(total), rule.RewardAttachType}}}
	key := fmt.Sprintf("fate-box:%d:%x:%d:%d:%d", x.Character.ID, sourceID[:], x.Character.Revision, a.Count, count)
	attach := rule.RewardAttachType
	if common {
		attach = 0
	}
	result, err := json.Marshal(struct {
		Template         json.RawMessage
		Count            uint32
		RewardAttachType byte `json:",omitempty"`
	}{rule.Reward, uint32(total), attach})
	if err != nil {
		return true, err
	}
	var grants []json.RawMessage
	if err := json.Unmarshal(QuestGranted(before, x.Inventory), &grants); err != nil {
		return true, err
	}
	body, err := json.Marshal(deathReceipt{Key: key, Input: string(input), Result: string(result),
		Revision: x.Character.Revision + 1, Grants: grants})
	if err != nil {
		return true, err
	}
	_, err = x.tx.ExecContext(x.ctx, `INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)`,
		x.Character.ID, key, string(body))
	return true, err
}
