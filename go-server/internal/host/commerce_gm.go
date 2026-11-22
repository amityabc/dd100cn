package host

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/storage"
)

func gameMasterText(b []byte) (string, error) {
	if len(b) < 5 || len(b) > 260 || binary.LittleEndian.Uint32(b) != uint32(len(b)-4) {
		return "", storage.ErrInvalid
	}
	for _, ch := range b[4:] {
		if ch < 32 || ch > 126 {
			return "", storage.ErrInvalid
		}
	}
	return string(b[4:]), nil
}

func gameMasterRequest(text string) (storage.GameMasterRequest, bool) {
	q := storage.GameMasterRequest{Count: 1, Level: -1}
	w := strings.Fields(text)
	if len(w) == 0 || !strings.HasPrefix(w[0], "//") {
		return q, false
	}
	q.Operation = w[0][2:]
	if q.Operation == "kill" {
		return q, len(w) == 1
	}
	if len(w) < 2 {
		return q, false
	}
	u32 := func(s string) (uint32, bool) {
		n, err := strconv.ParseUint(s, 10, 32)
		return uint32(n), err == nil && n > 0 && n < math.MaxUint32
	}
	id, ok := u32(w[1])
	if !ok {
		return q, false
	}
	q.ID = id
	switch q.Operation {
	case "quest", "questclear2":
		return q, len(w) == 2 && id < 30000
	case "item":
		if len(w) == 3 {
			q.Count, ok = u32(w[2])
		}
		return q, len(w) <= 3 && ok
	case "apc":
		if len(w) > 4 || id > 80000 {
			return q, false
		}
		if len(w) >= 3 {
			lev, err := strconv.Atoi(w[2])
			if err != nil || lev != -1 && (lev < 1 || lev > 100) {
				return q, false
			}
			q.Level = lev
		}
		if len(w) == 4 {
			switch strings.ToLower(w[3]) {
			case "0", "friendly", "friend":
			case "1", "neutral":
				q.Faction = 1
			case "2", "enemy":
				q.Faction = 2
			default:
				return q, false
			}
		}
		return q, true
	}
	return q, false
}

func (g *game) gameMaster(ctx context.Context, body []byte) error {
	text, err := gameMasterText(body)
	if err != nil {
		return err
	}
	isAPC := text == "//apc" || strings.HasPrefix(text, "//apc ")
	feedback := func(message string) error {
		if !isAPC {
			return nil
		}
		words := utf16.Encode([]rune(strings.ReplaceAll(message, "\x00", " ")))
		payload := make([]byte, 0, 2*(min(256, len(words))+1))
		for _, ch := range words[:min(256, len(words))] {
			payload = binary.LittleEndian.AppendUint16(payload, ch)
		}
		return g.send(0, 0x7f43, append(payload, 0, 0))
	}
	if err = g.server.DB.ValidateLease(ctx, g.lease); err != nil {
		return err
	}
	if !slices.Contains(g.server.gmAccounts, g.lease.Account) {
		return feedback("当前账号没有GM命令权限")
	}
	q, ok := gameMasterRequest(text)
	if !ok {
		return feedback("用法：//apc 编号 [等级(-1跟随角色)] [友方/中立/敌方或0/1/2]")
	}
	catalog := g.server.Characters
	if q.Operation == "kill" || q.Operation == "apc" {
		run := g.ordinary.Run
		if g.channel != 19 && (g.channel != 63 || run == nil || run.OculusEntry == nil) ||
			g.opening != nil || !run.GameMasterReady() || g.ordinary.Returning() {
			return feedback("请在副本房间加载完成且角色存活时使用此命令")
		}
		r, err := g.server.DB.Current(ctx, g.lease)
		if err != nil {
			return err
		}
		c, err := selected(r)
		if err != nil {
			return err
		}
		actor, _, err := readActor(c, g.channel)
		if err != nil {
			return err
		}
		p, err := readGameplay(c.Gameplay)
		if err != nil {
			return err
		}
		if r.Phase != storage.TownReady || c.ID != run.Character || !p.Initialized || p.Progression.Hp == 0 {
			return feedback("请在副本房间加载完成且角色存活时使用此命令")
		}
		if q.Operation == "apc" {
			if !g.server.delegated || run.InLayer {
				return feedback("APC召唤需要开启本地战斗委托，且不能在剧情层使用")
			}
			hash := catalog.Commerce.GmApcs[q.ID]
			if _, err := hex.DecodeString(hash); len(hash) != 64 || err != nil {
				return feedback("APC编号不存在或资源未准备完成")
			}
			lv := q.Level
			if lv == -1 {
				lv = int(c.Level)
			}
			apc, ok := run.AddGameMasterAPC(g.nextActor, q.ID, byte(lv), q.Faction, hash)
			if !ok {
				return feedback("本次副本的 GM 召唤名额或对象编号不足")
			}
			g.nextActor++
			return g.sendGameMasterAPC(apc)
		}
		for _, id := range run.GameMasterTargets() {
			death := binary.LittleEndian.AppendUint32(nil, uint32(id))
			death = binary.LittleEndian.AppendUint16(death, c.ID)
			death = append(death, make([]byte, 68)...)
			if err = g.dungeonMonsterDeathAuthorized(ctx, run, c, actor, death, true); err != nil {
				return err
			}
			if !run.GameMasterDefeated(id) || g.ordinary.Run != run {
				break
			}
			r, err = g.server.DB.Current(ctx, g.lease)
			if err != nil {
				return err
			}
			c, err = selected(r)
			if err != nil {
				return err
			}
			actor, _, err = readActor(c, g.channel)
			if err != nil {
				return err
			}
		}
		return nil
	}
	if !g.commerceInTown() {
		return nil
	}
	switch q.Operation {
	case "item":
		intent, err := catalog.GM.Grant(q.ID)
		if err != nil {
			return nil
		}
		weights := catalog.Weights
		extra := map[uint32]int64{}
		if _, ok := weights[q.ID]; !ok && intent.Weight != nil {
			extra[q.ID] = *intent.Weight
		}
		if intent.Special == nil {
			in, err := g.server.DB.Inventory(ctx, g.lease)
			if err != nil {
				return err
			}
			for _, a := range in.Items {
				if a.Location != 0 || a.CharacterID != in.CharacterID {
					continue
				}
				_, known := weights[a.TemplateID]
				_, prepared := extra[a.TemplateID]
				if known || prepared {
					continue
				}
				row, err := catalog.GM.Grant(a.TemplateID)
				if err != nil || row.Weight == nil {
					continue
				}
				extra[a.TemplateID] = *row.Weight
			}
		}
		if len(extra) > 0 {
			weights = maps.Clone(weights)
			maps.Copy(weights, extra)
		}
		out, err := g.server.DB.Commerce(ctx, g.lease, 176, g.seq, body, storage.CommerceReplay{}, func(x *storage.CommerceTx) error {
			if intent.Special != nil {
				return x.GrantGameMasterSpecial(text, q, *intent.Special, time.Now())
			}
			if err := x.GrantGameMasterItem(text, q, intent.Template, time.Now()); err != nil {
				return err
			}
			return x.CheckCarry(catalog.GoldCarryLimits, weights)
		})
		if expectedCommerceError(err) {
			return nil
		}
		if err != nil {
			return err
		}
		packets, special, err := inventorywire.Town(out.Inventory, out.Character, inventorywire.Options{
			CashPolicies: catalog.CashPolicies, Now: time.Now(), PetDefaultName: catalog.Commerce.PetDefaultName,
		})
		if err != nil {
			return err
		}
		if intent.Special != nil {
			g.trackPetCare(out.Character)
			packets = append(packets, special...)
		}
		return g.projected(packets)
	case "quest", "questclear2":
		changed := false
		out, err := g.server.DB.ChangeTownQuest(ctx, g.lease, catalog.Weights, func(c storage.Character, _ storage.Inventory, _ map[string]json.RawMessage) (storage.TownQuestMutation, error) {
			m, err := catalog.Quests.GameMasterQuest(g.lease, c, text, q)
			changed = len(m.Quests) > 0
			return m, err
		})
		if expectedCommerceError(err) {
			return nil
		}
		if err != nil || !changed {
			return err
		}
		delete(g.questAcceptSeen, uint16(q.ID))
		delete(g.questConfirm, uint16(q.ID))
		return g.sendQuestBook(out)
	default:
		return nil
	}
}

func (g *game) sendGameMasterAPC(a dungeon.GameMasterAPC) error {
	wire := binary.LittleEndian.AppendUint32(nil, 0x31435041)
	wire = binary.LittleEndian.AppendUint16(wire, a.Actor)
	wire = binary.LittleEndian.AppendUint32(wire, a.Code)
	wire = append(wire, a.Level, a.Faction, a.X, a.Y)
	return g.send(0, 0x7f42, wire)
}

func (g *game) projectGameMasterFollowers(run *dungeon.Run) error {
	for _, a := range run.GameMasterFollowers() {
		if err := g.sendGameMasterAPC(a); err != nil {
			return err
		}
	}
	return nil
}
