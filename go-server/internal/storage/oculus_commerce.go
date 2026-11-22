package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
)

type OculusMerchantPeer struct{ Dungeon, NPC, Shop uint32 }

type OculusRepairSource struct {
	RunId                    string
	Dungeon                  uint32
	Mode                     uint16
	AdmissionHash, SceneHash string
	Generation               uint32
	X, Y                     byte
	Merchant                 uint32
	RescueDeathKey           string
}

func (x *CommerceTx) RequireOculusRescue(entry OculusEntry, p OculusRepairSource, rescue uint32, rescueX, rescueY byte, rescueScene string, clock OculusRitualProof) error {
	c := x.Character
	if c.ID != entry.CharacterId || x.lease.Owner != entry.Owner || x.lease.Epoch != entry.Epoch ||
		p.RunId != entry.RunId || p.Dungeon != entry.Dungeon || p.Mode != entry.Mode || p.AdmissionHash != entry.ModeHash || p.Generation == 0 ||
		p.Merchant == 0 || rescue == 0 || !matchesSHA(rescueScene) || !matchesSHA(p.SceneHash) ||
		p.Generation == 1 && (p.SceneHash != entry.DungeonHash || rescueScene != entry.DungeonHash) {
		return ErrPhase
	}
	// The entry and the exact named monster death must both have committed
	// under this lease. A visible NPC or pending room change is insufficient.
	admitted, err := readDungeonReceipt(x.ctx, x.tx, c, entry.Key())
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPhase
	}
	if err != nil {
		return err
	}
	frozen, err := json.Marshal(entry)
	if err != nil || admitted.Result != "admitted" || admitted.Input != string(frozen) {
		return ErrPhase
	}
	expired, err := verifyOculusRitual(x.ctx, x.tx, c, entry, clock)
	if expired || errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrConflict) {
		return ErrPhase
	}
	if err != nil {
		return err
	}
	d, err := readDungeonReceipt(x.ctx, x.tx, c, p.RescueDeathKey)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPhase
	}
	if err != nil {
		return err
	}
	var in struct {
		dungeonDeathReceiptIdentity
		Code uint32
	}
	var result struct{ Actor uint16 }
	if json.Unmarshal([]byte(d.Input), &in) != nil || json.Unmarshal([]byte(d.Result), &result) != nil ||
		in.RunId != entry.RunId || in.Dungeon != entry.Dungeon || in.OwnerGuid != x.lease.Owner || in.Account != x.account ||
		in.Character != c.ID || in.Epoch != x.lease.Epoch || in.ResourceHash != rescueScene || in.MazeIndex != entry.Maze ||
		in.RoomX != rescueX || in.RoomY != rescueY || in.Code != rescue || in.Actor < 402 || result.Actor != in.Actor ||
		d.Key != dungeonDeathReceiptKey(in.dungeonDeathReceiptIdentity) {
		return ErrPhase
	}
	return nil
}
