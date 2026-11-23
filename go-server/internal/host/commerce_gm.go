package host

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/pvf"
	"s9a1/server/internal/resources"
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
		return uint32(n), err == nil && n > 0
	}
	id, ok := u32(w[1])
	if !ok || id == math.MaxUint32 {
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
		words := utf16.Encode([]rune(strings.ReplaceAll(message, "\x00", " ")))
		payload := make([]byte, 0, 2*(min(256, len(words))+1))
		for _, ch := range words[:min(256, len(words))] {
			payload = binary.LittleEndian.AppendUint16(payload, ch)
		}
		return g.send(0, 0x7f43, append(payload, 0, 0))
	}
	apcFeedback := func(message string) error {
		if !isAPC {
			return nil
		}
		return feedback(message)
	}
	if err = g.server.DB.ValidateLease(ctx, g.lease); err != nil {
		return err
	}
	if !slices.Contains(g.server.gmAccounts, g.lease.Account) {
		return apcFeedback("当前账号没有GM命令权限")
	}
	q, ok := gameMasterRequest(text)
	catalog := g.catalog()
	if isAPC || ok && q.Operation == "apc" {
		var hash string
		if ok {
			hash, err = g.prepareGameMasterAPC(catalog, q.ID)
			if err != nil {
				return feedback("APC召唤失败：" + err.Error())
			}
		}
		return g.gameMasterAPC(ctx, q, ok, hash, apcFeedback)
	}
	if !ok {
		return nil
	}
	if q.Operation == "item" {
		prepared, err := g.server.prepareGMItem(ctx, q.ID)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		}
		catalog, g.frameCatalog = prepared, prepared
	}
	if q.Operation == "kill" {
		run := g.ordinary.Run
		if g.channel != 19 && (g.channel != 63 || run == nil || run.OculusEntry == nil) ||
			g.opening != nil || !run.GameMasterReady() || g.ordinary.Returning() {
			return nil
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
			return nil
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

func (g *game) gameMasterAPC(ctx context.Context, q storage.GameMasterRequest, parsed bool, hash string, feedback func(string) error) error {
	r, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(r)
	if errors.Is(err, storage.ErrPhase) {
		return feedback("No initialized selected character")
	}
	if err != nil {
		return err
	}
	p, err := readGameplay(c.Gameplay)
	if err != nil {
		return err
	}
	if !p.Initialized {
		return feedback("No initialized selected character")
	}
	if !parsed {
		return feedback("用法：//apc 编号 [等级(-1跟随角色)] [友方/中立/敌方或0/1/2]")
	}
	run := g.ordinary.Run
	if g.channel != 19 && (g.channel != 63 || run == nil || run.OculusEntry == nil) || g.opening != nil ||
		!run.GameMasterReady() || g.ordinary.Returning() || r.Phase != storage.TownReady || c.ID != run.Character || p.Progression.Hp == 0 {
		return feedback("请在副本房间加载完成且角色存活时使用此命令")
	}
	if !g.server.delegated || run.InLayer {
		return feedback("APC召唤需要开启本地战斗委托，且不能在剧情层使用")
	}
	level := byte(q.Level)
	if q.Level == -1 {
		level = c.Level
	}
	apc, next, err := run.AddGameMasterAPC(g.nextActor, q.ID, level, q.Faction, hash)
	if err != nil {
		return feedback(err.Error())
	}
	g.nextActor = next
	return g.sendGameMasterAPC(apc)
}

func (g *game) prepareGameMasterAPC(catalog *resources.CharacterCatalog, id uint32) (string, error) {
	if hash := catalog.Commerce.GmApcs[id]; len(hash) == 64 {
		if _, err := hex.DecodeString(hash); err == nil {
			return hash, nil
		}
	}
	a, err := pvf.Open(g.server.pvfPath, g.server.profilePath)
	if err != nil {
		return "", err
	}
	defer a.Close()
	hash, err := gameMasterAPCSource(a, id)
	if err != nil {
		return "", err
	}
	inputs, err := gm.FileMetadata(g.server.pvfPath, g.server.profilePath, false)
	if err != nil {
		return "", err
	}
	if inputs.PvfSha256 != catalog.GM.Pvf || inputs.SidecarSha256 != catalog.GM.Sidecar || inputs.ProfileSha256 != catalog.GM.Profile {
		return "", errors.New("APC资源与当前目录不一致")
	}
	return hash, nil
}

func gameMasterAPCSource(a gm.ItemArchive, id uint32) (string, error) {
	const budget = 1 << 20
	index, err := a.ReadIndex("aicharacter", budget)
	if err != nil {
		return "", err
	}
	path, found := index[int64(id)]
	if !found {
		return "", errors.New("APC编号不存在，请使用aicharacter资源编号")
	}
	text, err := a.ReadText(path, budget)
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("PVF resource not found")
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("APC资源为空")
	}
	raw, err := a.Read(path, budget)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%X", sha256.Sum256(raw)), nil
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
