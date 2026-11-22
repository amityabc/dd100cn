package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"s9a1/server/internal/operator"
	"s9a1/server/internal/savejson"
)

const WisdomStone uint32 = 10312874

type wisdomAudit struct {
	FatigueBefore, FatigueAfter uint16
	StoneCount                  uint32
	ResourceHash                string
	Owner                       uuid.UUID
	Epoch, Revision             int64
	ExecutorBuild               uuid.UUID
}

func (s *Store) OperatorWisdom(ctx context.Context, l Lease, m operator.Wisdom, template json.RawMessage, sceneReady bool, build uuid.UUID) (operator.Reply, error) {
	msg := func(status, why string) operator.Reply { return operator.Message(status, m.OperationId, why) }
	if m.Account != "local" || m.Account != l.Account || m.Character == 0 || m.OperationId[0] != 0x31 || m.OperationId[1] != 0x53 || binary.BigEndian.Uint16(m.OperationId[2:4]) != m.Character || m.StoneCount == 0 || m.StoneCount > 10000 || !operator.ValidReason(m.Reason) {
		return msg("Rejected", "Invalid character-bound Wisdom supply intent"), nil
	}
	if len(template) == 0 {
		return msg("Rejected", "Time Guide Stone template is not prepared"), nil
	}
	var t ordinaryTemplate
	if json.Unmarshal(template, &t) != nil || t.TemplateID != WisdomStone || t.Kind != 2 || t.StackLimit == 0 || !matchesSHA(t.ResourceHash) {
		return msg("Rejected", "Time Guide Stone template is invalid"), nil
	}
	type intent struct {
		Account     savejson.String
		Character   uint16
		StoneCount  uint32
		OperationId uuid.UUID
		Reason      savejson.String
	}
	input, err := json.Marshal(struct {
		Version  string
		Intent   intent
		Template json.RawMessage
	}{"AuthorizedWisdomSupply/v1", intent{savejson.String(m.Account), m.Character, m.StoneCount, m.OperationId, savejson.String(m.Reason)}, template})
	if err != nil {
		return operator.Reply{}, err
	}
	owner, err := uuid.Parse(l.Owner)
	if err != nil {
		return operator.Reply{}, err
	}
	now := time.Now().UTC()
	day := now.Add(2 * time.Hour)
	dayKey := int64(day.Year()*10000 + int(day.Month())*100 + day.Day())
	key := "gm-wisdom-supply:" + strings.ReplaceAll(m.OperationId.String(), "-", "")
	readback := false
	apply := func(tx *sql.Tx) (operator.Reply, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return operator.Reply{}, err
		}
		if r.SelectedID != m.Character {
			return msg("Rejected", "Exactly one current selected owner is required"), nil
		}
		if r.Phase != TownReady || !sceneReady {
			return msg("Rejected", "Wisdom supplies require a ready town owner"), nil
		}
		c, err := mailCharacter(ctx, tx, l.Account, r.SelectedID)
		if err != nil {
			return operator.Reply{}, err
		}
		var profile struct {
			Initialized bool
			Progression struct {
				Fatigue, FatigueLimit uint16
				FatigueDay            int64
			}
		}
		if err = json.Unmarshal(c.Gameplay, &profile); err != nil {
			return operator.Reply{}, err
		}
		if !profile.Initialized {
			return msg("Rejected", "Invalid character-bound Wisdom supply intent"), nil
		}
		p := profile.Progression
		var payload []byte
		err = tx.QueryRowContext(ctx, "SELECT payload::text FROM s9a1.reward_receipts WHERE character_id=$1 AND event_key=$2", c.ID, key).Scan(&payload)
		if err == nil {
			var receipt deathReceipt
			if json.Unmarshal(payload, &receipt) != nil || receipt.Key != key || receipt.Input != string(input) || receipt.Revision <= 0 || receipt.Revision > c.Revision || len(receipt.Grants) == 0 {
				return msg("Rejected", "Wisdom supply conflicts with its committed receipt"), nil
			}
			var a wisdomAudit
			if json.Unmarshal([]byte(receipt.Result), &a) != nil || a.FatigueBefore > a.FatigueAfter || a.FatigueAfter == 0 || a.StoneCount != m.StoneCount || a.ResourceHash != t.ResourceHash || a.Owner == uuid.Nil || a.Epoch <= 0 || a.Revision < 0 || a.Revision != receipt.Revision-1 || a.ExecutorBuild == uuid.Nil {
				return msg("Rejected", "Invalid committed Wisdom supply result"), nil
			}
			b, err := json.Marshal(a)
			if err != nil || string(b) != receipt.Result {
				return msg("Rejected", "Invalid committed Wisdom supply result"), nil
			}
			var total uint64
			seen := map[string]bool{}
			for _, raw := range receipt.Grants {
				var g questGrantResult
				if json.Unmarshal(raw, &g) != nil || !matchesUUID(g.InstanceId) || seen[g.InstanceId] || g.TemplateId != WisdomStone || g.Location != 0 || g.Slot < 121 || g.Slot > 176 || g.Granted == 0 {
					return msg("Rejected", "Invalid committed Wisdom supply result"), nil
				}
				seen[g.InstanceId] = true
				total += uint64(g.Granted)
			}
			if total != uint64(m.StoneCount) {
				return msg("Rejected", "Invalid committed Wisdom supply result"), nil
			}
			return operator.Reply{Status: "Replay", OperationId: m.OperationId, CharacterRevision: c.Revision, AccountRevision: r.AssetRevision, WisdomSupply: b, CurrentFatigue: p.Fatigue}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return operator.Reply{}, err
		}
		if readback {
			return msg("Unconfirmed", "Committed supply receipt is absent"), nil
		}
		if p.Fatigue > p.FatigueLimit || p.FatigueDay < 0 {
			return msg("Rejected", "Invalid saved fatigue"), nil
		}
		if p.FatigueLimit == 0 {
			return msg("Rejected", "Character has no fatigue limit"), nil
		}
		in, err := readInventory(ctx, tx, l.Account, r)
		if err != nil {
			return operator.Reply{}, err
		}
		x := CommerceTx{ctx: ctx, tx: tx, account: l.Account, Character: c, Inventory: in}
		if err = x.grantCommerceItem(template, m.StoneCount, now, false); err != nil {
			return operator.Reply{}, err
		}
		result, err := json.Marshal(wisdomAudit{p.Fatigue, p.FatigueLimit, m.StoneCount, t.ResourceHash, owner, l.Epoch, c.Revision, build})
		if err != nil {
			return operator.Reply{}, err
		}
		receipt := deathReceipt{Key: key, Input: string(input), Result: string(result), Revision: c.Revision + 1}
		for _, a := range x.Grants {
			if a.Location != 0 || a.Slot < 121 || a.Slot > 176 {
				return operator.Reply{}, ErrCapacity
			}
			row, err := json.Marshal(questGrantResult{InstanceId: materialGUID(a.UID), TemplateId: a.TemplateID, Location: a.Location, Slot: a.Slot, Granted: a.Count})
			if err != nil {
				return operator.Reply{}, err
			}
			receipt.Grants = append(receipt.Grants, row)
		}
		payload, err = json.Marshal(receipt)
		if err != nil {
			return operator.Reply{}, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)", c.ID, key, string(payload))
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE s9a1.characters SET gameplay_json=jsonb_set(gameplay_json::jsonb,'{Progression}',(gameplay_json::jsonb->'Progression') || jsonb_build_object('Fatigue',$3::int,'FatigueDay',$4::bigint))::json, revision=revision+1 WHERE account=$1 AND wire_id=$2`, l.Account, c.ID, p.FatigueLimit, max(dayKey, p.FatigueDay))
		}
		return operator.Reply{Status: "Granted", OperationId: m.OperationId}, err
	}
	first, err := writeTx(ctx, s.db, apply)
	if errors.Is(err, ErrCapacity) || errors.Is(err, ErrInvalid) {
		return msg("Rejected", err.Error()), nil
	}
	if err != nil || first.Status != "Granted" && first.Status != "Replay" {
		return first, err
	}
	readback = true
	read, err := writeTx(ctx, s.db, apply)
	if err != nil {
		return first, err
	}
	if read.Status != "Replay" {
		return msg(first.Status, "Supply completed but readback was not confirmed"), nil
	}
	read.Status, read.ReadBack = first.Status, true
	return read, nil
}
