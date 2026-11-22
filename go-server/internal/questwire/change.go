package questwire

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"s9a1/server/internal/storage"
)

type grant struct {
	Template, Attributes json.RawMessage
	Core, Detail         []byte
}
type operationRules struct {
	CanAbandon, Available, Daily, DuelTraining bool
	Grade                                      string
	AcceptItems                                []Material
	missing                                    string
}

func (o *operationRules) UnmarshalJSON(data []byte) error {
	type plain operationRules
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"CanAbandon", "Available", "Daily", "DuelTraining", "Grade", "AcceptItems"} {
		v := fields[name]
		if len(v) == 0 || string(v) == "null" {
			o.missing = name
			break
		}
	}
	return json.Unmarshal(data, (*plain)(o))
}

// RequireOperations validates the entire runtime catalog before accepting a connection.
// Standalone projection vectors may contain a deliberately smaller schema.
func (c *Catalog) RequireOperations() error {
	for i := range c.quests {
		q := &c.quests[i]
		if q.Operations == nil {
			return fmt.Errorf("quest %d missing Operations: %w", q.Id, ErrCatalogOperations)
		}
		if field := q.Operations.missing; field != "" {
			return fmt.Errorf("quest %d missing Operations.%s: %w", q.Id, field, ErrCatalogOperations)
		}
		for _, m := range q.Operations.AcceptItems {
			if _, ok := c.grants[m.Template]; !ok {
				return fmt.Errorf("quest %d missing AcceptanceGrants template %d: %w", q.Id, m.Template, ErrCatalogOperations)
			}
			if len(q.Operations.AcceptItems) != 1 || m.Count != 1 || q.Kind != seeking {
				return fmt.Errorf("quest %d uses an unsupported acquisition shape", q.Id)
			}
		}
	}
	return nil
}

type Action struct {
	Mutation              storage.TownQuestMutation
	Ack                   []byte
	Full, AssetProjection bool
}
type ChangeContext struct {
	Now        time.Time
	Delegated  bool
	Arrival    func(storage.Character, uint16) (json.RawMessage, error)
	HasNPC     func(storage.Character, NpcTarget) (bool, error)
	Training   func(storage.Character, storage.Inventory, *Quest, TriggerRequest) (json.RawMessage, error)
	Dungeon    *DungeonContext
	Lease      storage.Lease
	RankedDuel bool
}

var ErrCatalogOperations = errors.New("re-export the matched catalog with tools/export-characters.ps1")

func ReadID(op uint16, body []byte) (uint16, error) {
	if (op == 31 || op == 32) && (len(body) == 2 || len(body) == 4 && little.Uint16(body) == op) {
		return little.Uint16(body[len(body)-2:]), nil
	}
	return 0, errors.New("quest identity request boundary mismatch")
}

func initial(q *Quest) (active, error) {
	a := active{ObjectiveHash: q.Hash, Remaining: []uint32{1}, Receipts: []string{}}
	switch q.Kind {
	case huntEnemy, huntMonster:
		width := 4
		if q.Kind == huntEnemy {
			width = 5
		}
		if len(q.Data) == 0 || len(q.Data)%width != 0 {
			return a, storage.ErrInvalid
		}
		a.Remaining = nil
		for i := width - 1; i < len(q.Data); i += width {
			a.Remaining = append(a.Remaining, uint32(q.Data[i]))
		}
	case clearCondition:
		if q.Subtype == 11 {
			if len(q.Data) < 3 {
				return a, storage.ErrInvalid
			}
			a.Remaining[0] = uint32(max(1, q.Data[2]))
		}
	case duel:
		if len(q.Data) != 4 {
			return a, storage.ErrInvalid
		}
		a.Remaining = []uint32{uint32(q.Data[0]), uint32(q.Data[1]), uint32(q.Data[2])}
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return a, err
		}
		token[6] = token[6]&0x0f | 0x40
		token[8] = token[8]&0x3f | 0x80
		a.Receipts = []string{"duel-accept:" + hex.EncodeToString(token[:])}
	}
	if !validActive(q, a) {
		return a, storage.ErrInvalid
	}
	return a, nil
}

func (c *Catalog) refresh(book map[string]json.RawMessage, p *player, force bool, now time.Time) (bool, error) {
	var prior int
	if b := book["DailyDay"]; len(b) > 0 {
		if json.Unmarshal(b, &prior) != nil {
			return false, storage.ErrInvalid
		}
		if prior != 0 {
			if _, err := time.Parse("20060102", strconv.Itoa(prior)); err != nil {
				return false, storage.ErrInvalid
			}
		}
	}
	daily := func(id uint16) bool {
		q := c.ordinary[id]
		return q != nil && q.Operations != nil && q.Operations.Daily
	}
	if prior == 0 && !force {
		found := false
		for id := range p.hist.Active {
			found = found || daily(id)
		}
		for id := range p.hist.Cleared {
			found = found || daily(id)
		}
		if !found {
			return false, nil
		}
	}
	day, _ := strconv.Atoi(now.UTC().Add(2 * time.Hour).Format("20060102"))
	if day <= prior {
		return false, nil
	}
	if prior != 0 {
		maps.DeleteFunc(p.hist.Cleared, func(id uint16, _ byte) bool { return daily(id) })
		book["Cleared"], _ = json.Marshal(p.hist.Cleared)
	}
	book["DailyDay"] = json.RawMessage(strconv.Itoa(day))
	return true, nil
}

func (c *Catalog) acceptItems(q *Quest, in storage.Inventory, fresh bool) (*storage.Asset, []byte, error) {
	items := q.Operations.AcceptItems
	if len(items) == 0 {
		return nil, []byte{0}, nil
	}
	// Only one nonzero grant exists in this PVF: one unit of quest material 4365.
	if len(items) != 1 || items[0].Count != 1 || q.Kind != seeking {
		return nil, nil, ErrCatalogOperations
	}
	m := items[0]
	if !slices.ContainsFunc(q.Materials, func(v Material) bool { return v.Template == m.Template && v.Count >= m.Count }) {
		return nil, nil, storage.ErrInvalid
	}
	f, found := c.grants[m.Template]
	if !found {
		return nil, nil, ErrCatalogOperations
	}
	used := map[uint16]bool{}
	var matched []storage.Asset
	for _, a := range in.Items {
		if a.Location != 0 {
			continue
		}
		used[a.Slot] = true
		if a.TemplateID == m.Template {
			var spec struct {
				Kind               byte
				ResourceExpiration *int64
			}
			var attrs struct{ ExpiresAt *int64 }
			if a.UID == "" || a.Count == 0 || a.Slot < 177 || a.Slot > 232 || json.Unmarshal(a.Template, &spec) != nil || spec.Kind != 2 || spec.ResourceExpiration != nil || json.Unmarshal(a.Attributes, &attrs) != nil || attrs.ExpiresAt != nil {
				return nil, nil, storage.ErrInvalid
			}
			matched = append(matched, a)
		}
	}
	var added *storage.Asset
	if fresh && len(matched) == 0 {
		slot := uint16(177)
		for ; slot <= min(uint16(232), 208+in.BagExpansion) && used[slot]; slot++ {
		}
		if slot > min(uint16(232), 208+in.BagExpansion) {
			return nil, nil, storage.ErrCapacity
		}
		added = &storage.Asset{CharacterID: in.CharacterID, Source: "item", TemplateID: m.Template, Count: 1, Slot: slot, Template: f.Template, Attributes: f.Attributes, Core: f.Core, Detail: f.Detail}
		matched = append(matched, *added)
	}
	slices.SortFunc(matched, func(a, b storage.Asset) int { return int(a.Slot) - int(b.Slot) })
	b := []byte{byte(len(matched))}
	for _, a := range matched {
		b = little.AppendUint16(b, a.Slot)
		b = little.AppendUint32(b, a.TemplateID)
		b = little.AppendUint32(b, a.Count)
	}
	return added, b, nil
}

func (catalog *Catalog) Change(c storage.Character, in storage.Inventory, receipts map[string]json.RawMessage, op, id uint16, opts ChangeContext) (Action, error) {
	var out Action
	q := catalog.ordinary[id]
	if q == nil {
		return out, storage.ErrInvalid
	}
	if q.Operations == nil {
		return out, ErrCatalogOperations
	}
	if !q.Operations.Available || op != 31 && op != 32 {
		return out, storage.ErrInvalid
	}
	p, err := playerOf(c)
	if err != nil {
		return out, err
	}
	if id >= 2672 && id <= 2674 && !((op == 31 || opts.RankedDuel) && opts.Delegated && q.Operations.DuelTraining && p.initialized && c.Level >= 50 && opts.Dungeon == nil) {
		return out, storage.ErrInvalid
	}
	d := opts.Dungeon
	mainline := q.Parent == 0 && q.Operations.Grade == "epic"
	if d != nil && (op != 31 || !d.living(c) || !mainline && !catalog.dungeonAcceptance(c, p, in, q, receipts, d)) {
		return out, storage.ErrInvalid
	}
	var doc, book map[string]json.RawMessage
	var rows map[uint16]json.RawMessage
	if err = json.Unmarshal(c.Gameplay, &doc); err != nil {
		return out, err
	}
	if err = json.Unmarshal(doc["Quests"], &book); err != nil {
		return out, err
	}
	if err = json.Unmarshal(book["Active"], &rows); err != nil {
		return out, err
	}
	if rows == nil || p.hist.Cleared == nil || len(rows) > 20 {
		return out, storage.ErrInvalid
	}
	changed, err := catalog.refresh(book, &p, q.Operations.Daily, opts.Now)
	if err != nil {
		return out, err
	}
	existing, wasActive := p.hist.Active[id]
	if op == 32 {
		if !wasActive || !q.Operations.CanAbandon {
			return out, storage.ErrInvalid
		}
		for child := range p.hist.Active {
			if def := catalog.ordinary[child]; def != nil && def.Parent == id {
				return out, storage.ErrInvalid
			}
		}
		delete(rows, id)
		book["Active"], _ = json.Marshal(rows)
		out.Mutation.Quests, err = json.Marshal(book)
		out.Ack = little.AppendUint16([]byte{1}, id)
		out.Full = true
		return out, err
	}
	if wasActive {
		if !validActive(q, existing) {
			return out, storage.ErrInvalid
		}
	} else {
		if len(rows) >= 20 || !catalog.ordinaryAvailable(p, q) {
			return out, storage.ErrInvalid
		}
		a, err := initial(q)
		if err != nil {
			return out, err
		}
		if d != nil && !mainline && q.Kind == clearMap {
			v := projectionView(catalog, p, in, receipts)
			if key := v.priorRoomEvent(q, d); key != "" {
				a.Remaining, a.Receipts = []uint32{0}, []string{key}
			}
		}
		rows[id], err = json.Marshal(a)
		if err != nil {
			return out, err
		}
		p.hist.Active[id] = a
		book["Active"], _ = json.Marshal(rows)
		changed = true
	}
	newItem, itemBytes, err := catalog.acceptItems(q, in, !wasActive)
	if err != nil {
		return out, err
	}
	out.Mutation.NewItem = newItem
	if changed {
		doc["Quests"], _ = json.Marshal(book)
		c.Gameplay, err = json.Marshal(doc)
		if err != nil {
			return out, err
		}
		if d == nil && !wasActive && q.Kind == reach && opts.Arrival != nil {
			arrived, err := opts.Arrival(c, id)
			if err != nil {
				return out, err
			}
			if len(arrived) > 0 {
				rows[id] = arrived
				book["Active"], _ = json.Marshal(rows)
				if err = json.Unmarshal(arrived, &existing); err != nil {
					return out, err
				}
				p.hist.Active[id] = existing
			}
		}
		out.Mutation.Quests, err = json.Marshal(book)
		if err != nil {
			return out, err
		}
		in.Revision++
	}
	if newItem != nil {
		in.Items = append(slices.Clone(in.Items), *newItem)
	}
	v := projectionView(catalog, p, in, receipts)
	v.inDungeon = d != nil
	left, err := v.remaining(q, map[uint16]bool{})
	if err != nil {
		return out, fmt.Errorf("%w: %v", storage.ErrInvalid, err)
	}
	out.Ack = little.AppendUint16([]byte{1}, id)
	out.Ack = little.AppendUint32(out.Ack, left)
	out.Ack = append(out.Ack, itemBytes...)
	out.Full = changed
	out.AssetProjection = changed && len(q.Operations.AcceptItems) > 0
	return out, nil
}
