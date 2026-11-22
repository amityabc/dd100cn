package dungeon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"s9a1/server/internal/storage"
)

const oculusBossPolicy = "OculusBossTimeGuide/PVF-weekly-limit;one-total;ground-max-five;stable-sha256-rejection/v4"

type OculusBossProof struct {
	Policy                     string
	Week                       int64
	ObservedAt                 string
	EntryInput                 string
	BossMonster                uint32
	BaseHash                   string
	FirstDrop                  int
	Rounds                     []uint32
	WeeklyOrdinal, WeeklyLimit byte
	Equipment                  *BossEquipmentPlan `json:",omitempty"`
}

func (p *OculusBossProof) Key() string {
	return fmt.Sprintf("oculus-boss-reward:week:100000007:%d:%d", p.Week, p.WeeklyOrdinal)
}

func (c *Catalog) PlanOculusBoss(run *Run, mode OculusMode, d Death, z *DeathResult, used byte, entry string, now time.Time) error {
	if run.OculusEntry == nil || d.Removal || d.Monster.Type != 3 || d.Monster.Code != mode.Rules.BossMonsterId || d.Monster.Dummy || !d.Monster.BlocksRoom ||
		run.Room.MapID != mode.ClearMapId || run.Room.X != run.Definition.Dungeon.BossX || run.Room.Y != run.Definition.Dungeon.BossY {
		return storage.ErrInvalid
	}
	limit := mode.Rules.WeeklyBossRewardLimit
	if limit < 1 || limit > 127 {
		return storage.ErrInvalid
	}
	if int(used) >= limit {
		return nil
	}
	var t dropTemplate
	if json.Unmarshal(c.OculusStone, &t) != nil || t.TemplateID != 10312874 || t.Kind != 2 || t.FirstBagSlot != 121 || t.LastBagSlot != 176 ||
		t.ResourceExpiration != nil || t.StackLimit < 28 || !isHash(t.ResourceHash) {
		return storage.ErrInvalid
	}
	p := &OculusBossProof{Policy: oculusBossPolicy, Week: run.OculusEntry.Week, ObservedAt: now.UTC().Format("2006-01-02T15:04:05.9999999+00:00"),
		EntryInput: entry, BossMonster: d.Monster.Code, BaseHash: z.DropRuleHash, FirstDrop: len(z.Drops), WeeklyOrdinal: used + 1, WeeklyLimit: byte(limit)}
	seed := fmt.Sprintf("%s\n%s\n%d\n%s\n%s\n0", oculusBossPolicy, run.idKey(), d.Actor, run.OculusEntry.ModeHash, t.ResourceHash)
	var count uint32
	for block := 0; block < 16 && count == 0; block++ {
		for _, b := range sha256.Sum256([]byte(fmt.Sprintf("%s\n%d", seed, block))) {
			if b < 252 {
				count = 23 + uint32(b%6)
				break
			}
		}
	}
	if count == 0 {
		return storage.ErrInvalid
	}
	p.Rounds = []uint32{count}
	rewards := make([]GroundReward, 0, len(z.Drops)+7)
	for _, drop := range z.Drops {
		rewards = append(rewards, drop.Reward)
	}
	for left := count; left > 0; {
		pile := min(left, 5)
		rewards = append(rewards, GroundReward{Template: c.OculusStone, StackCount: pile})
		left -= pile
	}
	if c.BossEquipment != nil {
		var err error
		p.Equipment, err = c.BossEquipment.Plan(run.ID, d.Actor)
		if err != nil {
			return err
		}
		if p.Equipment != nil && p.Equipment.Reward != nil {
			rewards = append(rewards, *p.Equipment.Reward)
		}
	}
	drops, err := run.freezeDrops(d.Actor, d.Key, rewards)
	if err != nil {
		return err
	}
	hash := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return strings.ToUpper(hex.EncodeToString(h[:]))
	}
	base := hash("local100-combined-drop/v1\n" + p.BaseHash + "\n" + hash(oculusBossPolicy+"\n"+run.OculusEntry.ModeHash+"\n"+t.ResourceHash))
	if p.Equipment != nil {
		base = hash("local100-combined-drop/v1\n" + base + "\n" + p.Equipment.Hash)
	}
	z.Drops, z.DropRuleHash, z.OculusBossPolicy = drops, base, p
	return nil
}
