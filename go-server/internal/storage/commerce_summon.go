package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

const SummonAuthorizationResult = "NativeApcSummonAuthorized/v1"

type PotionSummonAuthorization struct {
	Owner, RunID, SourceInstance string
	Epoch                        int64
	Character                    uint16
	Template                     uint32
	ResourceHash                 string
	Request                      PotionSummonRequest
	UsedAt, ExpiresAt            int64
	Definition                   PotionSummon
	Key                          string `json:"-"`
}

type PotionSummonRequest struct {
	Sequence, Slot                        uint16
	List                                  byte
	NativeReference, TemplateID, Reserved uint32
}

type PotionSummonKill struct {
	AuthorizationKey string
	Actor            uint16
	ReportedAt       int64
	RunID            string `json:"-"`
	Body             []byte `json:"-"`
}

func (x *CommerceTx) AuthorizePotionSummon(slot uint16, seq uint16, body []byte, run string, d PotionDefinition, now time.Time) error {
	if d.TemplateID != 8008 || !d.NativeApcSummon.Supported() || !validAssetUUID(run) || len(body) != 15 || body[2] != 0 ||
		binary.LittleEndian.Uint16(body) != slot || binary.LittleEndian.Uint32(body[7:]) != 8008 ||
		binary.LittleEndian.Uint32(body[11:]) != 0 || binary.LittleEndian.Uint32(body[3:]) == 0 || now.UnixMilli() <= 0 ||
		!fateExchangeHash(d.ResourceHash) {
		return ErrInvalid
	}
	pos := slices.IndexFunc(x.before.Items, func(a Asset) bool {
		return a.Location == 0 && a.CharacterID == x.Character.ID && a.Slot == slot && a.TemplateID == d.TemplateID
	})
	if pos < 0 {
		return ErrInvalid
	}
	uid := materialGUID(x.before.Items[pos].UID)
	if !validAssetUUID(uid) {
		return ErrInvalid
	}
	key := fmt.Sprintf("dungeon-summon:%s:%d", strings.ReplaceAll(run, "-", ""), x.Character.Revision)
	a := PotionSummonAuthorization{
		Owner: x.lease.Owner, RunID: run, SourceInstance: uid, Epoch: x.lease.Epoch, Character: x.Character.ID, Template: d.TemplateID,
		ResourceHash: d.ResourceHash, UsedAt: now.UnixMilli(), ExpiresAt: now.UnixMilli() + int64(d.NativeApcSummon.DurationMilliseconds),
		Request: PotionSummonRequest{Sequence: seq, Slot: slot, List: body[2], NativeReference: binary.LittleEndian.Uint32(body[3:]),
			TemplateID: binary.LittleEndian.Uint32(body[7:]), Reserved: binary.LittleEndian.Uint32(body[11:])},
		Definition: *d.NativeApcSummon, Key: key,
	}
	input, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err = putDungeonReceiptWithGrants(x.ctx, x.tx, x.Character, key, string(input), SummonAuthorizationResult, []json.RawMessage{}); err != nil {
		return err
	}
	x.summon = &a
	return nil
}

func SummonReportedActor(body []byte) uint16 {
	le := binary.LittleEndian
	if len(body) < 74 || len(body) != 74+14*int(body[30]) || le.Uint16(body[4:]) != math.MaxUint16 || body[len(body)-6] != 0 {
		return 0
	}
	var found uint16
	for i := range int(body[30]) {
		r := body[31+14*i:]
		if le.Uint16(r) != 0xf111 {
			continue
		}
		candidate := le.Uint16(r[2:])
		if candidate == 0 || candidate >= 402 || le.Uint64(r[4:]) == 0 || le.Uint16(r[12:]) == 0 || found != 0 && found != candidate {
			return 0
		}
		found = candidate
	}
	return found
}

func validateSummonedDeath(ctx context.Context, tx *sql.Tx, l Lease, c Character, k *PotionSummonKill) error {
	if k == nil {
		return nil
	}
	saved, err := readDungeonReceipt(ctx, tx, c, k.AuthorizationKey)
	if err != nil {
		return err
	}
	var a PotionSummonAuthorization
	if saved.Result != SummonAuthorizationResult || json.Unmarshal([]byte(saved.Input), &a) != nil || a.Owner != l.Owner || a.Epoch != l.Epoch ||
		a.Character != c.ID || a.RunID != k.RunID || !validAssetUUID(a.RunID) || !validAssetUUID(a.SourceInstance) ||
		a.Template != 8008 || a.Request.TemplateID != 8008 || a.Request.List != 0 || a.Request.Reserved != 0 || a.Request.NativeReference == 0 ||
		!fateExchangeHash(a.ResourceHash) || !a.Definition.Supported() || a.UsedAt <= 0 || a.ExpiresAt-a.UsedAt != 900000 ||
		k.ReportedAt < a.UsedAt || k.ReportedAt >= a.ExpiresAt || k.Actor == c.ID || SummonReportedActor(k.Body) != k.Actor || k.Actor == 0 ||
		saved.Key != fmt.Sprintf("dungeon-summon:%s:%d", strings.ReplaceAll(a.RunID, "-", ""), saved.Revision-1) {
		return ErrConflict
	}
	return nil
}
