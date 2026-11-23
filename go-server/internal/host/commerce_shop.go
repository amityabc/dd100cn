package host

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"s9a1/server/internal/commercewire"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) commerceInTown() bool {
	return g.channel == 19 && g.opening == nil && !g.ordinary.Active() && !g.ordinary.Returning()
}

func (g *game) npcCommerce(ctx context.Context, op uint16, body []byte) error {
	reject := func() error { return g.send(1, op, mailwire.Rejected(4)) }
	var buy commercewire.Buy
	var sale commercewire.Sale
	var npc, shop uint32
	var err error
	switch op {
	case 21:
		buy, err = commercewire.ReadBuy(body)
		npc, shop = buy.NPC, buy.Shop
	case 22:
		sale, err = commercewire.ReadSale(body)
		npc, shop = sale.NPC, sale.Shop
	default:
		return reject()
	}
	oculus := g.channel == 63
	if err != nil {
		return reject()
	}
	merchantRejected := func() error {
		if err := reject(); err != nil || op != 22 {
			return err
		}
		return g.commerceAnnouncement("出售失败：当前商店不可用，请重新打开可交易的NPC商店。")
	}
	if g.channel != 19 && !oculus || g.ordinary.Returning() || g.opening != nil {
		return merchantRejected()
	}
	if !oculus && op == 21 && storage.IsFateExchangeNPC(npc) {
		return g.buyFateExchange(ctx, buy, body)
	}
	c := g.catalog()
	catalog := c.Commerce
	products := catalog.Products
	run := g.ordinary.Run
	if oculus && run == nil {
		return merchantRejected()
	}
	if run != nil {
		products = catalog.DungeonProducts
		if !oculus && (!run.ShopRoomReady(npc) || !slices.ContainsFunc(catalog.DungeonPeers, func(p storage.CommercePeer) bool { return p.NPC == npc && p.Shop == shop })) {
			return merchantRejected()
		}
	}
	now := time.Now()
	verify := func(x *storage.CommerceTx) error {
		if oculus {
			_, err := g.oculusMerchant(x, npc, shop)
			return err
		}
		if run == nil {
			if !c.TownMerchant(x.Character, npc, shop) {
				return storage.ErrPhase
			}
		} else {
			p, err := readGameplay(x.Character.Gameplay)
			if err != nil {
				return err
			}
			if run.Character != x.Character.ID || !p.Initialized || p.Progression.Hp == 0 {
				return storage.ErrPhase
			}
		}
		return nil
	}
	out, err := g.server.DB.CommerceScene(ctx, g.lease, op, g.seq, body, g.commerceState, g.dungeonMerchantScene(), verify, func(x *storage.CommerceTx) error {
		if op == 22 {
			if err := x.Sell(sale.Rows, catalog.SalePrices); err != nil {
				return err
			}
		} else {
			i := slices.IndexFunc(products, func(p storage.CommerceProduct) bool {
				return p.NPC == npc && p.Shop == shop && p.Job == x.Character.Job && p.TemplateID == buy.TemplateID
			})
			if i < 0 {
				return storage.ErrInvalid
			}
			if buy.TemplateID == 490709686 && (x.Character.Position.Town != 38 || x.Character.Position.Area != 1 || run != nil) {
				return storage.ErrPhase
			}
			if (buy.TemplateID == 490016725 || buy.TemplateID == 490016727) && (run != nil || !products[i].DPLLifetime()) {
				return storage.ErrInvalid
			}
			if err := x.BuyNPC(products[i], buy.Count, catalog, now); err != nil {
				return err
			}
		}
		return x.CheckCarry(c.GoldCarryLimits, c.Weights)
	})
	if op == 22 && (expectedCommerceError(err) || errors.Is(err, storage.ErrSaleMissing) ||
		errors.Is(err, storage.ErrSaleUnavailable) || errors.Is(err, storage.ErrSaleAssetChanged)) {
		if errors.Is(err, storage.ErrPhase) {
			return merchantRejected()
		}
		if sendErr := reject(); sendErr != nil {
			return sendErr
		}
		reason := "当前物品状态不满足操作条件，请重新确认物品数量及所在位置。"
		switch {
		case errors.Is(err, storage.ErrFunds):
			reason = "角色携带金币已达到上限，请先存入金库或减少携带金币。"
		case errors.Is(err, storage.ErrSaleMissing):
			reason = "该位置已没有物品，请重新打开背包。"
		case errors.Is(err, storage.ErrSaleUnavailable):
			reason = "该物品不允许出售或出售规则尚未接入。"
		case errors.Is(err, storage.ErrSaleAssetChanged):
			reason = "物品已锁定、已到期或数量发生变化，请检查后重试。"
		}
		return g.commerceAnnouncement("出售失败：" + reason)
	}
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, true)
	if err != nil {
		return err
	}
	g.commerceState = out.Replay
	if op == 22 {
		if err = g.send(1, op, commercewire.Sold(uint32(out.Inventory.Gold), sale.Rows)); err != nil {
			return err
		}
		return g.projected(bags)
	}
	core, err := inventorywire.MailItem119(out.Granted)
	if err != nil {
		return err
	}
	var medals uint32
	for _, a := range out.Inventory.Items {
		if a.Location == 0 && a.Slot == 2 && a.TemplateID == 2 {
			medals += a.Count
		}
	}
	if err = g.send(1, 21, commercewire.Bought(uint32(out.Inventory.Gold), medals, uint32(out.Inventory.Cera), core)); err != nil {
		return err
	}
	for _, a := range out.Grants[1:] {
		for _, full := range out.Inventory.Items {
			if full.UID == a.UID {
				a = full
				break
			}
		}
		core, err = inventorywire.MailItem119(a)
		if err != nil {
			return err
		}
		if err = g.send(0, 14, append([]byte{a.Location, 1, 0}, core...)); err != nil {
			return err
		}
	}
	counts, hasCounts, err := npcPurchaseCounts(products, out.Character, out.Inventory, npc, shop, now)
	if err != nil {
		return err
	}
	if hasCounts {
		if err = g.send(1, 707, counts); err != nil {
			return err
		}
		common, err := inventorywire.CommerceCommon(out.Inventory, out.Character)
		if err != nil {
			return err
		}
		if err = g.send(0, common.Opcode, common.Body); err != nil {
			return err
		}
	} else if out.Granted.Location == 45 {
		common, err := inventorywire.CommerceCommon(out.Inventory, out.Character)
		if err != nil {
			return err
		}
		if err = g.send(0, common.Opcode, common.Body); err != nil {
			return err
		}
	}
	return g.projected(bags)
}

func (g *game) cashBuy(ctx context.Context, body []byte) error {
	rows, err := commercewire.ReadCash(body)
	if err != nil {
		var id uint32
		if len(body) >= 11 {
			id = binary.LittleEndian.Uint32(body[7:])
		}
		return g.send(1, 64, commercewire.CashRejected(id, 20))
	}
	reject := func(code uint16) error { return g.send(1, 64, commercewire.CashRejected(rows[0].Commodity, code)) }
	run := g.ordinary.Run
	death := !g.commerceInTown()
	scene := "town"
	if death {
		row := rows[0]
		if g.channel != 19 || run == nil || run.Room == nil || !run.ReviveAllowed(run.Character) ||
			run.Settlement.Confirmation != nil || len(rows) != 1 || row.Commodity != 100004 || !row.Ordinary() || row.CouponTemplate != 0 || row.CouponSlot != 0 {
			return reject(20)
		}
		scene = fmt.Sprintf("death-coin:%s:%d", run.ID, run.DeathCycle)
	}
	lines := make([]storage.CashLine, len(rows))
	for i, row := range rows {
		if row.Commodity == storage.RenameCommodity {
			if len(rows) != 1 {
				return reject(20)
			}
			return g.buyRename(ctx, row, body)
		}
		lines[i] = storage.CashLine{Commodity: row.Commodity, CouponTemplate: row.CouponTemplate, CouponSlot: row.CouponSlot,
			AvatarFlag: row.AvatarFlag, AbilityID: row.AbilityID, PurchaseModel: row.PurchaseModel, Avatar: !row.Ordinary()}
	}
	c := g.catalog()
	now := time.Now()
	if slices.ContainsFunc(lines, func(line storage.CashLine) bool {
		return !line.Avatar && c.Commerce.Cash[line.Commodity].BattlePassHash != ""
	}) && (!slices.Contains(c.Commerce.Activities, uint16(2520)) || !c.Quests.BattlePass.Active(now)) {
		return reject(20)
	}
	day, _ := storage.CommerceWindow(1, now, storage.CommercePurchase{})
	scene = fmt.Sprintf("%s:%d", scene, day)
	replay := g.commerceState
	if replay.Scene != scene {
		replay = storage.CommerceReplay{}
	}
	out, err := g.server.DB.Commerce(ctx, g.lease, 64, g.seq, body, replay, func(x *storage.CommerceTx) error {
		if death {
			p, err := readGameplay(x.Character.Gameplay)
			if err != nil {
				return err
			}
			offer := c.Commerce.Cash[100004]
			if run.Character != x.Character.ID || !p.Initialized || p.Progression.Hp != 0 ||
				!offer.RevivalCoins || offer.TemplateID != 1 || offer.Count != 1 {
				return storage.ErrPhase
			}
		}
		if err := x.BuyCash(lines, c.Commerce, now, func(ch storage.Character) (json.RawMessage, error) {
			p, err := c.Quests.GrantSkillStyle(ch, c.OpeningCombat.QuestFreeSkills)
			if err != nil {
				return nil, storage.ErrInvalid
			}
			return p, nil
		}, func(raw json.RawMessage, offer storage.CashOffer) (json.RawMessage, error) {
			p, err := c.Quests.BattlePass.BuyCash(raw, offer, now)
			if err != nil {
				return nil, storage.ErrInvalid
			}
			return p, nil
		}); err != nil {
			return err
		}
		return x.CheckCarry(c.GoldCarryLimits, c.Weights)
	})
	var code storage.CommerceRejection
	if errors.As(err, &code) {
		return reject(uint16(code))
	}
	if errors.Is(err, storage.ErrCapacity) {
		return reject(4)
	}
	if expectedCommerceError(err) {
		return reject(20)
	}
	if err != nil {
		return err
	}
	bags, err := inventorywire.CommerceBags(out.Inventory, out.Character, true)
	if err != nil {
		return err
	}
	offer := c.Commerce.Cash[rows[0].Commodity]
	var list byte
	if offer.TargetPersonalCargoCapacity != 0 {
		list = 2
	}
	if offer.TargetAccountCargoCapacity != 0 {
		list = 12
	}
	if list != 0 {
		cargo, err := inventorywire.CommerceCargo(out.Inventory, out.Character, list)
		if err != nil {
			return err
		}
		bags = append(bags, cargo)
	}
	limits, err := inventorywire.CommerceCashLimits(out.Inventory, out.Character, c.CashPolicies, now)
	if err != nil {
		return err
	}
	var account storage.Roster
	var slots, premium, avatar, pet, nameTag, service, skillStyle bool
	for _, row := range rows {
		o := c.Commerce.Cash[row.Commodity]
		slots = slots || o.CharacterSlot
		premium = premium || o.PremiumType != 0 && o.PremiumType < 100
		service = service || o.PremiumType >= 100
		avatar = avatar || !row.Ordinary() || o.TargetAvatarExpansion != 0
		pet = pet || len(o.PetTemplate) != 0 && string(o.PetTemplate) != "null"
		nameTag = nameTag || o.NameTag != nil
		skillStyle = skillStyle || o.SkillStyleHash != ""
	}
	if slots || premium {
		account, err = g.server.DB.Snapshot(ctx, g.lease)
		if err != nil {
			return err
		}
	}
	var special []inventorywire.Packet
	if avatar || pet {
		special, err = inventorywire.CommerceSpecial(out.Inventory, out.Character)
		if err != nil {
			return err
		}
	}
	g.commerceState = out.Replay
	g.commerceState.Scene = scene
	for i, row := range rows {
		category := int32(-1)
		if !row.Ordinary() {
			category = 0
		}
		if err = g.send(1, 64, commercewire.CashBought(row.Commodity, uint32(i), uint32(len(rows)-1), category)); err != nil {
			return err
		}
	}
	if err = g.wallet(out.Inventory.Cera); err != nil {
		return err
	}
	if err = g.projected(bags); err != nil {
		return err
	}
	if slots {
		if err = g.send(0, 66, []byte{8, 0, account.Capacity}); err != nil {
			return err
		}
	}
	if avatar {
		if err = g.projected(special[:1]); err != nil {
			return err
		}
	}
	if nameTag || skillStyle {
		a, _, err := readActor(out.Character, g.channel)
		if err != nil {
			return err
		}
		m1, err := protocol.Mode1(a, true)
		if err != nil {
			return err
		}
		m0, err := g.actorMode0(a)
		if err != nil {
			return err
		}
		if err = g.send(0, 2, m1); err != nil {
			return err
		}
		if err = g.send(0, 2, m0); err != nil {
			return err
		}
		if skillStyle {
			if err = g.sendSkillBuild(out.Character, false, nil); err != nil {
				return err
			}
		}
	}
	if pet {
		g.trackPetCare(out.Character)
		if err = g.projected(special[1:3]); err != nil {
			return err
		}
	}
	if len(limits) != 0 {
		if err = g.send(0, 851, limits); err != nil {
			return err
		}
	}
	if premium {
		g.contracts = [4]int64{account.ConquerorExpiresAt, account.SkillMasterExpiresAt, account.CrystalExpiresAt, account.GrowthExpiresAt}
		if err = g.showPremium(); err != nil {
			return err
		}
	}
	if service {
		if err = g.refreshCharacterServices(ctx, false); err != nil {
			return err
		}
	}
	return g.showPurchasedCodes(out.Inventory.AccountPurchases)
}
