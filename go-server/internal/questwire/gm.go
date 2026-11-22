package questwire

import (
	"encoding/hex"
	"encoding/json"
	"slices"

	"s9a1/server/internal/storage"
)

func (catalog *Catalog) GameMasterQuest(l storage.Lease, c storage.Character, text string, request storage.GameMasterRequest) (storage.TownQuestMutation, error) {
	var m storage.TownQuestMutation
	if request.ID == 0 || request.ID >= 30000 {
		return m, storage.ErrInvalid
	}
	id := uint16(request.ID)
	q := catalog.ordinary[id]
	if q == nil || !q.Supported || q.Kind == unsupported || len(q.Hash) != 64 {
		return m, storage.ErrInvalid
	}
	if _, err := hex.DecodeString(q.Hash); err != nil {
		return m, storage.ErrInvalid
	}
	p, err := playerOf(c)
	if err != nil {
		return m, err
	}
	if !p.initialized || q.Job >= 0 && q.Job != int(p.job) || q.Grow >= 0 && q.Grow != int(p.grow&15) ||
		len(q.Targets) > 0 && !slices.ContainsFunc(q.Targets, p.matches) || slices.ContainsFunc(q.NonTargets, p.matches) {
		return m, storage.ErrInvalid
	}
	var doc, book map[string]json.RawMessage
	var activeRows map[uint16]json.RawMessage
	if json.Unmarshal(c.Gameplay, &doc) != nil || json.Unmarshal(doc["Quests"], &book) != nil || json.Unmarshal(book["Active"], &activeRows) != nil ||
		book == nil || activeRows == nil || p.hist.Cleared == nil || len(activeRows) > 20 {
		return m, storage.ErrInvalid
	}
	result := struct {
		Status    byte
		Id, Count uint32
		Active    *active
		Cleared   byte
	}{Id: request.ID, Count: request.Count}
	switch request.Operation {
	case "quest":
		if p.hist.Cleared[id] != 0 {
			return m, storage.ErrInvalid
		}
		if a, ok := p.hist.Active[id]; ok {
			if !validActive(q, a) {
				return m, storage.ErrInvalid
			}
			return m, nil
		}
		if len(activeRows) == 20 {
			return m, storage.ErrCapacity
		}
		initial, err := initial(q)
		if err != nil {
			return m, err
		}
		activeRows[id], err = json.Marshal(initial)
		if err != nil {
			return m, err
		}
		result.Status, result.Active = 1, &initial
	case "questclear2":
		if p.hist.Cleared[id] != 0 {
			return m, nil
		}
		delete(activeRows, id)
		p.hist.Cleared[id] = 1
		book["Cleared"], err = json.Marshal(p.hist.Cleared)
		if err != nil {
			return m, err
		}
		result.Status, result.Cleared = 2, 1
	default:
		return m, storage.ErrInvalid
	}
	book["Active"], err = json.Marshal(activeRows)
	if err != nil {
		return m, err
	}
	m.Quests, err = json.Marshal(book)
	if err != nil {
		return m, err
	}
	key, body, err := storage.GameMasterReceipt(l, c, text, request, q.Hash, nil, result)
	if err != nil {
		return m, err
	}
	m.Receipts = map[string]json.RawMessage{key: body}
	return m, nil
}
