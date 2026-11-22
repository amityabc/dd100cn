package questwire

import (
	"encoding/json"
	"fmt"
	"math"
	"s9a1/server/internal/storage"
	"slices"
	"time"
)

const CommunicatorQuest = uint16(12122)
const communicatorItem = uint32(10312154)
const communicatorResult = "communicator-use:v1"

type questPoint struct {
	Town, Area, X, Y uint16
	Direction        byte
}

func questPosition(p storage.Position) questPoint {
	return questPoint{p.Town, p.Area, p.X, p.Y, p.Direction}
}
func (p questPoint) position() storage.Position {
	return storage.Position{Town: p.Town, Area: p.Area, X: p.X, Y: p.Y, Direction: p.Direction}
}

type communicatorFact struct {
	Character, Quest                         uint16
	QuestHash, ItemHash, SceneHash, Instance string
	Slot                                     uint16
	Origin, Destination                      questPoint
	UsedAt                                   int64
}

type CommunicatorReplay struct {
	Character, Slot uint16
	Instance        string
}

func communicatorKey(q *Quest) string { return fmt.Sprintf("communicator:%d:%s", q.Id, q.Hash) }

func communicatorUsed(c storage.Character, v view, q *Quest, claimed bool) bool {
	if q == nil || q.CommunicatorTarget == nil || !q.AcceptanceSupported || q.Id != CommunicatorQuest || v.in.CharacterID != c.ID || c.Revision != v.in.Revision {
		return false
	}
	t := q.CommunicatorTarget
	key := communicatorKey(q)
	var saved receipt
	var fact communicatorFact
	var g struct{ CommunicatorUsedAt int64 }
	if json.Unmarshal(v.receipts[key], &saved) != nil || saved.Key != key || saved.Result != communicatorResult || saved.Revision <= 0 || saved.Revision > v.in.Revision || string(saved.Grants) != "[]" ||
		json.Unmarshal([]byte(saved.Input), &fact) != nil || json.Unmarshal(c.Gameplay, &g) != nil || fact.Character != c.ID || fact.Quest != q.Id || fact.QuestHash != q.Hash || fact.ItemHash != t.ItemHash || fact.SceneHash != t.SceneHash ||
		guidKey(fact.Instance) == "" || !(fact.Slot >= 3 && fact.Slot <= 8 || fact.Slot >= 65 && fact.Slot <= 120) || fact.Origin.Town == 76 || fact.Origin.Town > 255 || fact.Origin.Area > 255 ||
		fact.Origin.X > math.MaxInt16 || fact.Origin.Y > math.MaxInt16 || fact.Origin.Direction > 7 || fact.Destination != t.Arrival || fact.UsedAt <= 0 || fact.UsedAt > g.CommunicatorUsedAt {
		return false
	}
	if claimed {
		f, ok := completionReplay(v.p, v.in, q, v.receipts, CompleteRequest{q.Id, 65535, 1, 65535}, 0)
		var claim receipt
		_, stillActive := v.p.hist.Active[q.Id]
		return ok && !stillActive && v.p.hist.Cleared[q.Id] == 1 && guidKey(f.DungeonRunId) == "" && f.ParentQuestId == 0 && f.TerminalQuestId == 0 && json.Unmarshal(v.receipts[completionKey(q, 0)], &claim) == nil && claim.Revision >= saved.Revision
	}
	a := v.p.hist.Active[q.Id]
	return validActive(q, a) && a.Remaining[0] == 0
}

func communicatorScene(c storage.Character, v view, q *Quest, opts ChangeContext) error {
	if c.Level < 96 || c.Position.Town != 76 || c.Position.Area != 0 || v.p.hist.Cleared[12121] == 0 {
		return storage.ErrInvalid
	}
	return requireQuestionScene(c, q, opts)
}

func communicatorAsset(a storage.Asset, q *Quest, slot uint16, now time.Time) bool {
	var t grantTemplate
	var attrs struct {
		Locked    bool
		ExpiresAt *int64
	}
	return a.Location == 0 && a.Slot == slot && a.TemplateID == communicatorItem && a.Count == 1 && guidKey(a.UID) != "" && json.Unmarshal(a.Template, &t) == nil && t.Kind == 2 &&
		t.ResourceHash == q.CommunicatorTarget.ItemHash && t.MinimumLevel == 96 && t.StackLimit == 1 && t.FirstBagSlot == 65 && t.LastBagSlot == 120 && t.UsableJobs == math.MaxUint16 &&
		json.Unmarshal(a.Attributes, &attrs) == nil && !attrs.Locked && (attrs.ExpiresAt == nil || *attrs.ExpiresAt > now.Unix())
}

func (catalog *Catalog) UseCommunicator(c storage.Character, in storage.Inventory, rs map[string]json.RawMessage, slot uint16, now time.Time, recovering bool, replay CommunicatorReplay,
	allows func(storage.Character, storage.Position) bool, isRoom func(storage.Position) bool) (storage.TownQuestMutation, CommunicatorReplay, error) {
	var out storage.TownQuestMutation
	var next CommunicatorReplay
	q := catalog.ordinary[CommunicatorQuest]
	if q == nil || !q.AcceptanceSupported || q.CommunicatorTarget == nil || q.NpcRangeTarget == nil || !hash(q.CommunicatorTarget.ItemHash) || !hash(q.CommunicatorTarget.SceneHash) || q.CommunicatorTarget.Arrival != (questPoint{76, 0, 1783, 300, 5}) ||
		q.Operations == nil || !q.Operations.Available {
		return out, next, storage.ErrInvalid
	}
	p, err := playerOf(c)
	if err != nil || !p.initialized || c.Level < 96 || p.hist.Cleared[12121] == 0 {
		return out, next, storage.ErrInvalid
	}
	a := p.hist.Active[q.Id]
	claim := p.hist.Cleared[q.Id] != 0
	v := projectionView(catalog, p, in, rs)
	if claim && !communicatorUsed(c, v, q, true) || !claim && !validActive(q, a) {
		return out, next, storage.ErrInvalid
	}
	index := slices.IndexFunc(in.Items, func(a storage.Asset) bool { return communicatorAsset(a, q, slot, now) })
	if index < 0 {
		return out, next, storage.ErrInvalid
	}
	asset := in.Items[index]
	next = CommunicatorReplay{c.ID, slot, asset.UID}
	if recovering {
		if replay != next || c.Position.Town != 76 || c.Position.Area != 0 || !communicatorUsed(c, v, q, claim) {
			return out, next, storage.ErrInvalid
		}
		return out, next, nil
	}
	arrival := q.CommunicatorTarget.Arrival.position()
	if c.Position.Town == 76 || isRoom(c.Position) || !allows(c, c.Position) || !allows(c, arrival) {
		return out, next, storage.ErrInvalid
	}
	var doc skillPage
	var old int64
	if json.Unmarshal(c.Gameplay, &doc) != nil || doc == nil || doc["CommunicatorUsedAt"] != nil && json.Unmarshal(doc["CommunicatorUsedAt"], &old) != nil {
		return out, next, storage.ErrInvalid
	}
	stamp := now.UnixMilli()
	if old < 0 || stamp <= 0 || old != 0 && (stamp < old || stamp-old < 3000) {
		return out, next, storage.ErrInvalid
	}
	key := communicatorKey(q)
	if rs[key] != nil {
		if !communicatorUsed(c, v, q, claim) {
			return out, next, storage.ErrInvalid
		}
	} else {
		if claim || a.Remaining[0] == 0 {
			return out, next, storage.ErrInvalid
		}
		input, _ := json.Marshal(communicatorFact{Character: c.ID, Quest: q.Id, QuestHash: q.Hash, ItemHash: q.CommunicatorTarget.ItemHash, SceneHash: q.CommunicatorTarget.SceneHash,
			Instance: wireGUID(asset.UID), Slot: slot, Origin: questPosition(c.Position), Destination: q.CommunicatorTarget.Arrival, UsedAt: stamp})
		saved, _ := json.Marshal(receipt{Key: key, Input: string(input), Result: communicatorResult, Revision: c.Revision + 1, Grants: json.RawMessage("[]")})
		out.Receipts = map[string]json.RawMessage{key: saved}
	}
	if !claim {
		book, rows, err := questBook(c)
		if err != nil {
			return out, next, err
		}
		var raw skillPage
		if json.Unmarshal(rows[q.Id], &raw) != nil || raw == nil {
			return out, next, storage.ErrInvalid
		}
		raw["Remaining"], raw["UsedItemCount"] = json.RawMessage("[0]"), json.RawMessage("1")
		raw["Receipts"], _ = json.Marshal([]string{key})
		rows[q.Id], _ = json.Marshal(raw)
		book["Active"], _ = json.Marshal(rows)
		doc["Quests"], _ = json.Marshal(book)
	}
	doc["CommunicatorUsedAt"], _ = json.Marshal(stamp)
	gameplay, _ := json.Marshal(doc)
	out.Teleport = &storage.QuestTeleport{Position: arrival, Gameplay: gameplay}
	return out, next, nil
}
