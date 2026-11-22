package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/google/uuid"
	"s9a1/server/internal/operator"
	"s9a1/server/internal/savejson"
)

type walletIntent struct {
	Account                savejson.String
	Character              uint16
	CeraAmount, GoldAmount int64
	OperationId            uuid.UUID
	Reason                 savejson.String
}

func walletInput(i operator.Intent) (key, input string, cera, gold int64, err error) {
	id := strings.ReplaceAll(i.OperationID.String(), "-", "")
	var value any
	switch c := i.Command.(type) {
	case operator.Wallet:
		key, cera, gold = "gm-wallet:"+id, c.CeraAmount, c.GoldAmount
		if !operator.Targets(i.OperationID, i.Character) || cera < 0 || cera > math.MaxInt32 || gold < 0 || gold > math.MaxInt32 || cera+gold == 0 || !operator.ValidReason(i.Reason) {
			return "", "", 0, 0, errors.New("Invalid bounded wallet intent or character-bound operation identity")
		}
		value = struct {
			Source, Version string
			Intent          walletIntent
		}{"LocalOperator", "AuthorizedWalletGrant/v1",
			walletIntent{savejson.String(i.Account), i.Character, cera, gold, i.OperationID, savejson.String(i.Reason)}}
	case operator.Cera:
		key, cera = "gm-cera:"+id, c.Amount
		if i.Character != 1 || i.Account != "local" {
			return "", "", 0, 0, errors.New("This operator grant is limited to the authorized local account and audit character1")
		}
		if i.OperationID == uuid.Nil || cera <= 0 || cera > math.MaxInt32 || !operator.ValidReason(i.Reason) {
			return "", "", 0, 0, errors.New("Invalid bounded CERA grant intent")
		}
		value = struct {
			Source, Version string
			Account         savejson.String
			Character       uint16
			OperationId     uuid.UUID
			Amount          int64
			Reason          savejson.String
		}{
			"LocalOperator", "AuthorizedCeraGrant/v1", savejson.String(i.Account), i.Character, i.OperationID, cera, savejson.String(i.Reason)}
	default:
		return "", "", 0, 0, ErrInvalid
	}
	raw, err := json.Marshal(value)
	return key, string(raw), cera, gold, err
}

func walletReceipt(saved deathReceipt, account Roster, c Character, i operator.Intent, key, input string, cera, gold int64) (operator.Reply, bool) {
	out := operator.Reply{Status: "Replay", OperationId: i.OperationID, CurrentCera: account.Cera, CharacterRevision: c.Revision, AccountRevision: account.AssetRevision}
	if saved.Key != key || saved.Input != input || saved.Grants == nil || len(saved.Grants) != 0 || saved.Revision <= 0 || saved.Revision > c.Revision {
		return out, false
	}
	valid := func(owner, build uuid.UUID, epoch, charRev, accountRev int64) bool {
		return owner != uuid.Nil && build != uuid.Nil && epoch > 0 && charRev >= 0 && accountRev >= 0 && saved.Revision == charRev+1
	}
	exact := func(v any) bool { b, err := json.Marshal(v); return err == nil && string(b) == saved.Result }
	if _, cashOnly := i.Command.(operator.Cera); cashOnly {
		var a operator.CeraAudit
		if json.Unmarshal([]byte(saved.Result), &a) != nil || a.Before < 0 || a.Before > math.MaxInt32 || a.After > math.MaxInt32 ||
			a.Amount != cera || a.Before+cera != a.After || !valid(a.Owner, a.ExecutorBuild, a.Epoch, a.CharacterRevision, a.AccountRevision) || !exact(a) {
			return out, false
		}
		out.Grant = &a
	} else {
		var a operator.WalletAudit
		if json.Unmarshal([]byte(saved.Result), &a) != nil || a.CeraBefore < 0 || a.CeraBefore > math.MaxInt32 || a.CeraAfter > math.MaxInt32 ||
			a.GoldBefore < 0 || a.GoldBefore > math.MaxInt32 || a.GoldAfter > math.MaxInt32 || a.CeraBefore+cera != a.CeraAfter || a.GoldBefore+gold != a.GoldAfter ||
			!valid(a.Owner, a.ExecutorBuild, a.Epoch, a.CharacterRevision, a.AccountRevision) || !exact(a) {
			return out, false
		}
		out.WalletGrant, out.CurrentGold = &a, c.Gold
	}
	return out, true
}

// Readback after commit repeats exactly the same operation against a fresh SQL transaction.
func (s *Store) OperatorWallet(ctx context.Context, l Lease, i operator.Intent, sceneReady bool, build uuid.UUID) (operator.Reply, error) {
	msg := func(status, why string) operator.Reply { return operator.Message(status, i.OperationID, why) }
	_, ceraOnly := i.Command.(operator.Cera)
	readback := false
	apply := func(tx *sql.Tx) (operator.Reply, error) {
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return operator.Reply{}, err
		}
		if r.SelectedID != i.Character || i.Character == 0 {
			if ceraOnly {
				return msg("Rejected", "No current selected online owner for the requested account and character"), nil
			}
			return msg("Rejected", "Exactly one selected online owner is required for the target"), nil
		}
		c, err := scanCharacter(tx.QueryRowContext(ctx, "SELECT "+characterColumns+" FROM s9a1.characters WHERE account=$1 AND wire_id=$2", l.Account, r.SelectedID))
		if err != nil {
			return operator.Reply{}, err
		}
		var profile struct{ Initialized bool }
		if err = json.Unmarshal(c.Gameplay, &profile); err != nil {
			return operator.Reply{}, err
		}
		if i.Account != "local" || i.Account != l.Account || !profile.Initialized {
			return msg("Rejected", "Operator target is not the currently selected local account and character"), nil
		}
		key, input, cera, gold, err := walletInput(i)
		if err != nil {
			return msg("Rejected", err.Error()), nil
		}
		if r.Phase != TownReady || !sceneReady {
			if ceraOnly {
				return msg("Rejected", "Operator CERA grants require a ready town owner"), nil
			}
			return msg("Rejected", "Operator wallet grants require a ready town owner"), nil
		}
		var raw []byte
		err = tx.QueryRowContext(ctx, "SELECT payload::text FROM s9a1.reward_receipts WHERE character_id=$1 AND event_key=$2", c.ID, key).Scan(&raw)
		if err == nil {
			var saved deathReceipt
			if err = json.Unmarshal(raw, &saved); err != nil {
				return msg("Rejected", "Wallet receipt JSON is invalid"), nil
			}
			if out, ok := walletReceipt(saved, r, c, i, key, input, cera, gold); ok {
				return out, nil
			}
			if ceraOnly {
				return msg("Rejected", "CERA operation conflicts with its committed intent"), nil
			}
			return msg("Rejected", "Wallet operation conflicts with its committed intent"), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return operator.Reply{}, err
		}
		if readback {
			return msg("Unconfirmed", "Committed wallet receipt is absent"), nil
		}
		if r.Cera < 0 || c.Gold < 0 || r.Cera > math.MaxInt32-cera || c.Gold > math.MaxInt32-gold {
			if ceraOnly {
				return msg("Rejected", "CERA grant exceeds the native signed wallet limit"), nil
			}
			return msg("Rejected", "Wallet grant exceeds the native signed limit"), nil
		}
		owner, err := uuid.Parse(l.Owner)
		if err != nil {
			return operator.Reply{}, err
		}
		var audit any
		if ceraOnly {
			audit = operator.CeraAudit{Before: r.Cera, After: r.Cera + cera, Amount: cera, Owner: owner, Epoch: l.Epoch, CharacterRevision: c.Revision, AccountRevision: r.AssetRevision, ExecutorBuild: build}
		} else {
			audit = operator.WalletAudit{CeraBefore: r.Cera, CeraAfter: r.Cera + cera, GoldBefore: c.Gold, GoldAfter: c.Gold + gold, Owner: owner, Epoch: l.Epoch, CharacterRevision: c.Revision, AccountRevision: r.AssetRevision, ExecutorBuild: build}
		}
		result, err := json.Marshal(audit)
		if err != nil {
			return operator.Reply{}, err
		}
		receipt, err := json.Marshal(deathReceipt{Key: key, Input: input, Result: string(result), Revision: c.Revision + 1, Grants: []json.RawMessage{}})
		if err != nil {
			return operator.Reply{}, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO s9a1.reward_receipts(character_id,event_key,payload) VALUES($1,$2,$3::json)", c.ID, key, string(receipt))
		if err != nil {
			return operator.Reply{}, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE s9a1.characters SET revision=revision+1,gold=gold+$3 WHERE account=$1 AND wire_id=$2", l.Account, c.ID, gold)
		if err == nil && cera != 0 {
			_, err = tx.ExecContext(ctx, "UPDATE s9a1.accounts SET cera=cera+$2,asset_revision=asset_revision+1 WHERE account=$1", l.Account, cera)
		}
		return operator.Reply{Status: "Granted", OperationId: i.OperationID}, err
	}
	first, err := writeTx(ctx, s.db, apply)
	if err != nil || first.Status != "Granted" && first.Status != "Replay" {
		return first, err
	}
	readback = true
	read, err := writeTx(ctx, s.db, apply)
	if err != nil {
		return first, err
	}
	if read.Status != "Replay" {
		return msg(first.Status, "Wallet grant completed but cold readback was not confirmed"), nil
	}
	read.Status, read.ReadBack = first.Status, true
	return read, nil
}
