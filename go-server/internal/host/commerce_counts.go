package host

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/storage"
)

func npcPurchaseCounts(products []storage.CommerceProduct, c storage.Character, in storage.Inventory, npc, shop uint32, now time.Time) ([]byte, bool, error) {
	var profile struct {
		NpcPurchases map[uint32]storage.CommercePurchase
	}
	if err := json.Unmarshal(c.Gameplay, &profile); err != nil {
		return nil, false, err
	}
	rows := [4]map[uint32]uint32{{}, {}, {}, {}}
	count := 0
	for _, p := range products {
		if p.NPC != npc || p.Job != c.Job || shop != 0 && p.Shop != shop {
			continue
		}
		for _, limit := range p.Limits {
			group := 0
			old := profile.NpcPurchases[p.TemplateID]
			if limit.Account {
				group = 2
				old = in.AccountPurchases[p.TemplateID|0x80000000]
			}
			period, _ := storage.CommerceWindow(int(limit.Period), now, old)
			var used uint32
			if old.PeriodStart == period {
				used = min(old.Count, limit.Count)
			}
			if old.PeriodStart > period {
				used = limit.Count
			}
			if _, exists := rows[group][p.TemplateID]; !exists {
				count++
			}
			rows[group][p.TemplateID] = used
		}
	}
	if count > 4096 {
		return nil, false, storage.ErrInvalid
	}
	le := binary.LittleEndian
	b := []byte{1}
	for _, r := range rows {
		ids := make([]uint32, 0, len(r))
		for id := range r {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		b = le.AppendUint32(b, uint32(len(ids)))
		for _, id := range ids {
			b = le.AppendUint32(b, id)
			b = le.AppendUint32(b, r[id])
		}
	}
	return b, count != 0, nil
}

func (g *game) merchantCounts(ctx context.Context, body []byte) error {
	reject := func() error { return g.send(1, 707, mailwire.Rejected(0)) }
	if len(body) != 4 && (len(body) != 8 || binary.LittleEndian.Uint32(body[4:]) != 0) ||
		g.channel != 19 || g.ordinary.Returning() || g.opening != nil {
		return reject()
	}
	npc := binary.LittleEndian.Uint32(body)
	if npc == 0 {
		return reject()
	}
	if storage.IsFateExchangeNPC(npc) {
		return g.fateExchangeCounts(ctx, npc)
	}
	r, err := g.server.DB.Snapshot(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(r)
	if errors.Is(err, storage.ErrPhase) || r.Phase != storage.TownReady {
		return reject()
	}
	if err != nil {
		return err
	}
	catalog := g.server.Characters
	products, peers := catalog.Commerce.Products, catalog.Commerce.TownPeers
	run := g.ordinary.Run
	if run != nil {
		p, err := readGameplay(c.Gameplay)
		if err != nil {
			return err
		}
		if run.Character != c.ID || !p.Initialized || p.Progression.Hp == 0 || !run.ShopRoomReady(npc) {
			return reject()
		}
		products, peers = catalog.Commerce.DungeonProducts, catalog.Commerce.DungeonPeers
	}
	shops := map[uint32]bool{}
	for _, p := range peers {
		if p.NPC == npc && (run != nil || catalog.TownMerchant(c, npc, p.Shop)) {
			shops[p.Shop] = true
		}
	}
	if len(shops) == 0 {
		return reject()
	}
	matched := make([]storage.CommerceProduct, 0)
	for _, p := range products {
		if p.NPC == npc && p.Job == c.Job && shops[p.Shop] {
			matched = append(matched, p)
		}
	}
	in, err := g.server.DB.Inventory(ctx, g.lease)
	if err != nil {
		return err
	}
	b, _, err := npcPurchaseCounts(matched, c, in, npc, 0, time.Now())
	if err != nil {
		return err
	}
	return g.send(1, 707, b)
}
