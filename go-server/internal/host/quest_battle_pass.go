package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
	"slices"
	"time"
)

func (g *game) refreshBattlePass(ctx context.Context, login bool) error {
	p := g.server.Characters.Quests.BattlePass
	now := time.Now()
	if !p.Active(now) {
		return nil
	}
	return g.server.DB.RefreshBattlePass(ctx, g.lease, now, login)
}

func (g *game) battlePassPackets(ctx context.Context, force bool) ([]opening.Packet, error) {
	raw, err := g.server.DB.BattlePass(ctx, g.lease)
	if err != nil {
		return nil, err
	}
	if !force && bytes.Equal(raw, g.battlePassStamp) {
		return nil, nil
	}
	projected, err := g.server.Characters.Quests.BattlePass.Projection(raw, g.battlePassStamp)
	if err != nil {
		return nil, err
	}
	packets := make([]opening.Packet, 0, len(projected))
	for _, n := range projected {
		packets = append(packets, opening.Packet{Class: n.Class, Opcode: n.Opcode, Body: n.Body})
	}
	g.battlePassStamp = raw
	return packets, nil
}

func (g *game) sendBattlePass(ctx context.Context, force bool) error {
	if !g.battlePassSent || !force && !g.server.Characters.Quests.BattlePass.Active(time.Now()) {
		return nil
	}
	packets, err := g.battlePassPackets(ctx, force)
	if err != nil {
		return err
	}
	return g.packets(packets)
}

func (g *game) battlePassRequest(ctx context.Context, f protocol.Frame) error {
	r, err := questwire.ReadBattlePass(f.Opcode, f.Body)
	if err != nil {
		if err = g.send(1, f.Opcode, []byte{0, 0, 0}); err != nil {
			return err
		}
		if f.Opcode == 1768 {
			return g.battlePassNotice("探险记领奖失败：领奖请求格式不正确。")
		}
		if f.Opcode == 1767 {
			return g.sendBattlePass(ctx, true)
		}
		return nil
	}
	now := time.Now()
	cat := g.server.Characters
	d := cat.Quests.BattlePass
	var rejected error
	var out storage.BattlePassResult
	if f.Opcode != 1772 {
		if err = g.refreshBattlePass(ctx, false); err != nil {
			return err
		}
	}
	if g.channel != 19 || g.opening != nil || g.ordinary.Active() || g.ordinary.Returning() {
		rejected = questwire.ErrPassUnavailable
	} else {
		out, err = g.server.DB.ChangeBattlePass(ctx, g.lease, cat.Weights, func(_ storage.Character, in storage.Inventory, raw json.RawMessage) (storage.BattlePassPlan, error) {
			plan, issue := d.Plan(r, in, raw, now)
			if issue != nil {
				rejected = issue
				return storage.BattlePassPlan{}, nil
			}
			return plan, nil
		})
		if errors.Is(err, storage.ErrPhase) {
			rejected = questwire.ErrPassUnavailable
		} else if errors.Is(err, storage.ErrCapacity) || errors.Is(err, storage.ErrInvalid) {
			rejected = err
		} else if err != nil {
			return err
		}
	}
	accepted := rejected == nil
	if f.Opcode == 1772 {
		if accepted {
			if err = g.send(0, 1863, make([]byte, 8)); err != nil {
				return err
			}
		}
		if err = g.send(1, f.Opcode, r.Reply(accepted && r.Action == 2, f.Body)); err != nil {
			return err
		}
		if accepted && r.Action != 2 {
			msg := "当前没有可助力的冒险团好友"
			if r.Action == 1 {
				msg = "收到的助力次数不足"
			}
			return g.battlePassNotice(msg)
		}
		return nil
	}
	if f.Opcode == 1768 && accepted {
		assets, _, err := inventorywire.Town(out.Inventory, out.Character, inventorywire.Options{CashPolicies: cat.CashPolicies, Now: now})
		if err != nil {
			return err
		}
		if err = g.send(0, assets[0].Opcode, assets[0].Body); err != nil {
			return err
		}
		if slices.ContainsFunc(out.Inventory.Items, func(a storage.Asset) bool { return a.Location == 7 }) {
			if err = g.send(0, assets[3].Opcode, assets[3].Body); err != nil {
				return err
			}
		}
	}
	if err = g.send(1, f.Opcode, r.Reply(accepted, f.Body)); err != nil {
		return err
	}
	if f.Opcode == 1768 && !accepted {
		if err = g.battlePassNotice(passFailure(rejected)); err != nil {
			return err
		}
	}
	return g.sendBattlePass(ctx, true)
}

func (g *game) battlePassNotice(msg string) error {
	b, err := protocol.Announcement(msg)
	if err != nil {
		return err
	}
	return g.send(0, 488, b)
}

func passFailure(err error) string {
	const prefix = "探险记领奖失败："
	switch {
	case errors.Is(err, storage.ErrCapacity):
		return prefix + "物品栏对应分类空间不足，请整理背包后再领取。"
	case errors.Is(err, questwire.ErrPassQualification):
		return prefix + "尚未达到奖励等级，或未获得对应的高级探险家资格。"
	case errors.Is(err, questwire.ErrPassClaimed):
		return prefix + "奖励已经领取，或当前没有可领取的奖励。"
	case errors.Is(err, questwire.ErrPassClosed):
		return prefix + "当前不在活动开放时间内。"
	case errors.Is(err, questwire.ErrPassMax):
		return prefix + "尚未满级，或满级奖励所需经验不足。"
	case errors.Is(err, questwire.ErrPassUnavailable):
		return prefix + "活动资源不可用，或角色尚未处于可操作的城镇状态。"
	case errors.Is(err, questwire.ErrPassSeason):
		return prefix + "活动配置与已保存的赛季不一致，需要服务端处理。"
	case errors.Is(err, questwire.ErrRewardResource):
		return prefix + "奖励物品的发放配置尚未接入。"
	default:
		return prefix + "奖励数值或当前资产超出允许范围。"
	}
}
