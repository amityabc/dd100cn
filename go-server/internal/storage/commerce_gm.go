package storage

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"
)

type GameMasterRequest struct {
	Operation string
	ID, Count uint32
	Level     int
	Faction   byte
}

func GameMasterReceipt(l Lease, c Character, text string, request GameMasterRequest, hash string, grant any, result any) (string, json.RawMessage, error) {
	uid, err := newMailUUID()
	if err != nil {
		return "", nil, err
	}
	return gameMasterReceipt(l, c, uid, text, request, hash, grant, result, json.RawMessage("[]"))
}

func gameMasterReceipt(l Lease, c Character, uid, text string, request GameMasterRequest, hash string, grant, result any,
	grants json.RawMessage) (string, json.RawMessage, error) {
	key := "gm:" + uid
	requestType := map[string]byte{"item": 0, "quest": 1, "questclear2": 2}
	operation, ok := requestType[request.Operation]
	if !ok {
		return "", nil, ErrInvalid
	}
	input, err := json.Marshal(struct {
		Source, Version, Account, Owner string
		Epoch                           int64
		Character                       uint16
		OperationId, Text               string
		Request                         any
		ResourceHash                    string
		Grant                           any
	}{"GameMaster", "AuthorizedLocalGm/v1", l.Account, l.Owner, l.Epoch, c.ID, materialGUID(uid), text,
		struct {
			Operation byte
			Id, Count uint32
			Level     int
			Faction   byte
		}{operation, request.ID, request.Count, request.Level, request.Faction}, hash, grant})
	if err != nil {
		return "", nil, err
	}
	payload, err := encodeGameMasterReceipt(c, key, input, result, grants)
	return key, payload, err
}

func encodeGameMasterReceipt(c Character, key string, input json.RawMessage, result any, grants json.RawMessage) (json.RawMessage, error) {
	outcome, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Key, Input, Result string
		Revision           int64
		Grants             json.RawMessage
	}{key, string(input), string(outcome), c.Revision + 1, grants})
}

func gmQuality(source string) uint32 {
	seed := sha256.Sum256([]byte(source))
	return 1 + binary.LittleEndian.Uint32(seed[:])%999999997
}

func (x *CommerceTx) GrantGameMasterItem(text string, request GameMasterRequest, template json.RawMessage, now time.Time) error {
	var p useState
	var t ordinaryTemplate
	if json.Unmarshal(x.Character.Gameplay, &p) != nil || !p.Initialized || request.Count == 0 || json.Unmarshal(template, &t) != nil || t.TemplateID != request.ID {
		return ErrInvalid
	}
	uid, err := newMailUUID()
	if err != nil {
		return err
	}
	var d materialDefinition
	if json.Unmarshal(template, &d) != nil {
		return ErrInvalid
	}
	flags := giftEquipment{plainGM: !d.PvfGiftItem && !d.CashPackage && !d.CashPackageReward}
	if t.Kind != 2 {
		flags.qualitySeed = gmQuality(fmt.Sprintf("gm:%s:%d:%s", uid, request.ID, t.ResourceHash))
	}
	if err := x.grantGiftItem(template, request.Count, now, true, flags); err != nil {
		return err
	}
	if len(x.Grants) == 0 {
		return ErrConflict
	}
	intent := struct {
		Template   json.RawMessage
		Count      uint32
		Attributes json.RawMessage
	}{template, request.Count, x.Grants[0].Attributes}
	result := struct {
		Status    byte
		Id, Count uint32
		Active    *struct{}
		Cleared   byte
	}{Id: request.ID, Count: request.Count}
	key, payload, err := gameMasterReceipt(x.lease, x.Character, uid, text, request, t.ResourceHash, intent, result, QuestGranted(x.before, x.Inventory))
	if err != nil {
		return err
	}
	_, err = x.tx.ExecContext(x.ctx, `INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)`, x.Character.ID, key, string(payload))
	return err
}

func (x *CommerceTx) GrantGameMasterSpecial(text string, req GameMasterRequest, product GiftSpecial, now time.Time) error {
	var p useState
	var t specialPurchaseTemplate
	if json.Unmarshal(x.Character.Gameplay, &p) != nil || !p.Initialized || req.Count == 0 ||
		json.Unmarshal(product.Template, &t) != nil || t.TemplateID != req.ID || t.Kind > 2 || product.ResourcePeriod != 0 && t.RelativeLifetimeSeconds == 0 {
		return ErrInvalid
	}
	_, items, expansion, err := x.loadSpecial()
	if err != nil {
		return err
	}
	var ability byte
	value := t.MaximumDurability
	switch t.Kind {
	case 0:
		limit := uint32(120) + uint32(expansion)
		if req.Count > limit {
			return ErrCapacity
		}
		ability = 255
		for id, jobs := range product.Abilities {
			if jobs&t.UsableJobs != 0 && id < ability {
				ability = id
			}
		}
		if ability == 255 {
			return ErrInvalid
		}
		value = uint16(ability)
	case 1:
		if req.Count > 160 {
			return ErrCapacity
		}
		value = 0
	case 2:
		if req.Count > 56 {
			return ErrCapacity
		}
	}
	if uint32(len(items))+req.Count > 713 {
		return ErrCapacity
	}
	uid, err := newMailUUID()
	if err != nil {
		return err
	}
	key := "gm:" + uid
	for ordinal := range req.Count {
		quality := gmQuality(fmt.Sprintf("gm-special:%s:%s:%d", uid, t.ResourceHash, ordinal))
		if _, err = x.grantSpecialWithQuality(product.Template, value, product.InitialPetName, now, false, quality); err != nil {
			return err
		}
	}
	result := struct {
		Id, Count     uint32
		Kind, Ability byte
	}{req.ID, req.Count, t.Kind, ability}
	input, err := json.Marshal(struct {
		Source, Version, Account, Owner string
		Epoch                           int64
		Character                       uint16
		OperationId, Text               string
		Product                         GiftSpecial
		Count                           uint32
		Ability                         byte
	}{"GameMaster", "AuthorizedLocalGmSpecial/v1", x.lease.Account, x.lease.Owner, x.lease.Epoch, x.Character.ID, materialGUID(uid), text, product, req.Count, ability})
	if err != nil {
		return err
	}
	payload, err := encodeGameMasterReceipt(x.Character, key, input, result, json.RawMessage("[]"))
	if err != nil {
		return err
	}
	_, err = x.tx.ExecContext(x.ctx, `INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)`, x.Character.ID, key, string(payload))
	return err
}
