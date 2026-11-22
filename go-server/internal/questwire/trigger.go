package questwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"s9a1/server/internal/storage"
)

type TriggerRequest struct {
	Id                 uint16
	Trigger, Increment byte
	HasExplicitTrigger bool
}

// Confirmation is tied to the saved character and both revisions.
type Confirmation struct {
	CharacterID             uint16
	Revision, AssetRevision int64
	Trigger                 byte
	Remaining               uint32
	ObjectiveHash           string
	DungeonRun              string
	RoomX, RoomY            byte
}

func (catalog *Catalog) ReadTrigger(body []byte) (TriggerRequest, bool, error) {
	var req TriggerRequest
	if len(body) < 4 || little.Uint16(body) != 33 {
		return req, false, errors.New("quest callback identity boundary mismatch")
	}
	req.Id = little.Uint16(body[2:])
	if q := catalog.ordinary[req.Id]; q != nil && q.Kind == question {
		if len(body) != 6 || body[4] != 0 {
			return req, true, errors.New("question answer boundary mismatch")
		}
		req.Trigger = body[5]
		return req, true, nil
	}
	if (len(body) != 6 && len(body) != 7) || body[len(body)-1] != 0 {
		return req, false, errors.New("quest trigger boundary mismatch")
	}
	req.Increment = body[len(body)-2]
	if len(body) == 7 {
		req.HasExplicitTrigger = true
		req.Trigger = body[4]
	}
	return req, false, nil
}

func questBook(c storage.Character) (map[string]json.RawMessage, map[uint16]json.RawMessage, error) {
	var doc, book map[string]json.RawMessage
	var rows map[uint16]json.RawMessage
	if json.Unmarshal(c.Gameplay, &doc) != nil || json.Unmarshal(doc["Quests"], &book) != nil || json.Unmarshal(book["Active"], &rows) != nil || rows == nil || len(rows) > 20 {
		return nil, nil, storage.ErrInvalid
	}
	return book, rows, nil
}

func projectionView(catalog *Catalog, p player, in storage.Inventory, receipts map[string]json.RawMessage) view {
	v := view{catalog: catalog, p: p, in: in, held: map[uint32]uint64{}, receipts: receipts}
	for _, a := range in.Items {
		if a.Location == 0 {
			v.held[a.TemplateID] += uint64(a.Count)
		}
	}
	return v
}

func (catalog *Catalog) answer(c storage.Character, p player, in storage.Inventory, q *Quest, receipts map[string]json.RawMessage, a *active, answer byte, opts ChangeContext) (bool, error) {
	if !q.AcceptanceSupported || answer >= q.QuestionChoices {
		return false, storage.ErrInvalid
	}
	if p.hist.Cleared[q.Id] != 0 {
		out, ok := completionReplay(p, in, q, receipts, CompleteRequest{q.Id, 65535, 1, 65535}, 0)
		if !ok || out.QuestionAnswer == nil || *out.QuestionAnswer != answer {
			return false, storage.ErrInvalid
		}
		return false, nil
	}
	if !catalog.ordinaryAvailable(p, q) || !validActive(q, *a) {
		return false, storage.ErrInvalid
	}
	if err := requireQuestionScene(c, q, opts); err != nil {
		return false, err
	}
	if a.Answer != nil {
		if *a.Answer != uint16(answer) {
			return false, storage.ErrInvalid
		}
		return false, nil
	}
	chosen := uint16(answer)
	a.Answer, a.Remaining, a.Receipts = &chosen, []uint32{0}, []string{fmt.Sprintf("question-answer:%d:%s:%d", q.Id, q.Hash, answer)}
	return true, nil
}

func requireQuestionScene(c storage.Character, q *Quest, opts ChangeContext) error {
	if !q.AcceptanceSupported || q.NpcRangeTarget == nil || opts.HasNPC == nil {
		return storage.ErrInvalid
	}
	ok, err := opts.HasNPC(c, *q.NpcRangeTarget)
	if err != nil {
		return err
	}
	if !ok {
		return storage.ErrInvalid
	}
	return nil
}

func (catalog *Catalog) Trigger(c storage.Character, in storage.Inventory, receipts map[string]json.RawMessage, req TriggerRequest, opts ChangeContext) (Action, *Confirmation, error) {
	var out Action
	q := catalog.ordinary[req.Id]
	if q == nil || !q.Supported || q.Operations == nil || !q.Operations.Available || q.Kind == duel && opts.Training == nil {
		return out, nil, storage.ErrInvalid
	}
	p, err := playerOf(c)
	if err != nil || !p.initialized || p.hist.Cleared == nil {
		return out, nil, storage.ErrInvalid
	}
	book, rows, err := questBook(c)
	if err != nil {
		return out, nil, err
	}
	a := p.hist.Active[q.Id]
	d := opts.Dungeon
	if d != nil && (!d.living(c) || !catalog.dungeonObjective(q, d)) {
		return out, nil, storage.ErrInvalid
	}
	var dirtyBook, dirtyActive bool
	var raw json.RawMessage
	var remaining uint32
	if q.Kind == question {
		dirtyActive, err = catalog.answer(c, p, in, q, receipts, &a, req.Trigger, opts)
	} else {
		dirtyBook, err = catalog.refresh(book, &p, q.Operations.Daily, opts.Now)
		if err != nil || req.Increment != 0 || !validActive(q, a) {
			return out, nil, storage.ErrInvalid
		}
		v := projectionView(catalog, p, in, receipts)
		v.inDungeon = d != nil
		switch {
		case q.Kind == duel:
			raw, err = opts.Training(c, in, q, req)
			if len(raw) > 0 && err == nil {
				before := a
				err = json.Unmarshal(raw, &a)
				dirtyActive = err == nil && !reflect.DeepEqual(before, a)
			}
		case q.Kind == communicator:
			if !req.HasExplicitTrigger || (req.Trigger != 0x10 && req.Trigger != 0x20) || !communicatorUsed(c, v, q, false) {
				err = storage.ErrInvalid
				break
			}
			if req.Trigger == 0x10 {
				remaining, err = v.remaining(q, map[uint16]bool{})
				out.Ack = little.AppendUint32(little.AppendUint16([]byte{1}, q.Id), remaining)
				return out, nil, err
			}
			if err = communicatorScene(c, v, q, opts); err == nil {
				dirtyActive = !a.NpcMet
				a.NpcMet = true
			}
		case q.DungeonCinematicTarget != nil:
			m := q.DungeonCinematicTarget
			if d == nil || d.Map != m.Map || req.Trigger != 0 || req.HasExplicitTrigger {
				err = storage.ErrInvalid
				break
			}
			if a.Remaining[0] == 0 {
				if !movieReceipt(v, q, d, c, a, false) {
					err = storage.ErrInvalid
				}
				break
			}
			key := movieKey(q, d.RunID)
			if receipts[key] != nil {
				err = storage.ErrInvalid
				break
			}
			input, _ := json.Marshal(cinematicFact{Owner: wireGUID(d.Lease.Owner), Epoch: d.Lease.Epoch, Character: c.ID, Run: wireGUID(d.RunID), Quest: q.Id, ObjectiveHash: q.Hash, Dungeon: d.Dungeon, Maze: d.Maze, Map: d.Map, X: d.X, Y: d.Y, DungeonHash: d.Hash, Movie: m.Movie, MovieHash: m.ResourceHash})
			saved, _ := json.Marshal(receipt{Key: key, Input: string(input), Result: cinematicResult, Revision: c.Revision + 1, Grants: json.RawMessage("[]")})
			out.Mutation.Receipts = map[string]json.RawMessage{key: saved}
			a.Remaining, a.Receipts = []uint32{0}, []string{key}
			dirtyActive = true
		case req.Trigger == 0x10 && q.Kind != seekMeet && q.Kind != meet:
			if d == nil && !q.TownPassive || v.passiveProof(q, a, opts.Lease, d, d != nil) == "" {
				err = storage.ErrInvalid
			}
		case q.TownHandIn && d == nil && req.Trigger == 0:
			if !v.handInReady(q, map[uint16]bool{}) {
				err = storage.ErrInvalid
			}
		case q.OrderedDungeonParent || (q.Kind == questClear && d != nil):
			if _, _, ok := v.orderedTerminal(q, d, opts.Lease, c); req.Trigger != 0 || !ok {
				err = storage.ErrInvalid
			}
		case req.Trigger == 0x40 && d != nil && q.DungeonPassive:
			// This acknowledgement has no new progress fact.
		case q.Kind == clearMap || q.Kind == clearCondition || q.Kind == huntMonster || q.Kind == huntEnemy:
			if req.Trigger != 0 {
				err = storage.ErrInvalid
			} else if d != nil {
				if !v.dungeonCompleted(q, d, opts.Lease, c) {
					err = storage.ErrInvalid
				}
			} else {
				left, e := v.remaining(q, map[uint16]bool{})
				if e != nil || left != 0 || len(a.Receipts) == 0 {
					err = storage.ErrInvalid
				}
			}
		case q.Kind == cinematic && q.TownCinematicTarget != nil && req.Trigger == 0 && !req.HasExplicitTrigger:
			target := q.TownCinematicTarget
			if c.Position.Town != target.Town || c.Position.Area != target.Area {
				err = storage.ErrInvalid
				break
			}
			if a.Remaining[0] != 0 {
				a.Remaining = []uint32{0}
				a.Receipts = append(a.Receipts, fmt.Sprintf("cinematic:%d", target.Movie))
				dirtyActive = true
			}
		case q.Kind == meet && req.Trigger == 0:
			if q.FateIntroduction && !(q.Id == 8884 && c.Position.Town == 38 && c.Position.Area == 1 || q.Id == 8885 && c.Position.Town == 135 && c.Position.Area == 0) {
				err = storage.ErrInvalid
				break
			}
			dirtyActive = !a.NpcMet || a.Remaining[0] != 0
			a.NpcMet, a.Remaining = true, []uint32{0}
		case q.Kind == seekMeet && (req.Trigger == 0 || req.Trigger == 0x10 || req.Trigger == 0x20):
			left, e := v.remaining(q, map[uint16]bool{})
			if e != nil || left&511 != 0 {
				err = storage.ErrInvalid
				break
			}
			dirtyActive = !a.NpcMet
			a.NpcMet = true
		case q.Kind == reach && req.Trigger == 0:
			if opts.Arrival != nil {
				raw, err = opts.Arrival(c, q.Id)
				if len(raw) > 0 && err == nil {
					err = json.Unmarshal(raw, &a)
					dirtyActive = err == nil
				}
			}
		case q.Kind == seeking && req.Trigger <= 1, q.Kind == questClear && req.Trigger == 0:
			// The callback only confirms authoritative inventory or child flags.
		default:
			err = storage.ErrInvalid
		}
		if err == nil {
			v.p.hist.Active[q.Id] = a
			remaining, err = v.remaining(q, map[uint16]bool{})
		}
	}
	if err != nil {
		return out, nil, err
	}
	if dirtyActive {
		if len(raw) == 0 {
			var fields map[string]json.RawMessage
			if json.Unmarshal(rows[q.Id], &fields) != nil || fields == nil {
				return out, nil, storage.ErrInvalid
			}
			fields["Remaining"], _ = json.Marshal(a.Remaining)
			fields["NpcMet"], _ = json.Marshal(a.NpcMet)
			fields["Receipts"], _ = json.Marshal(a.Receipts)
			if a.Answer != nil {
				fields["Answer"], _ = json.Marshal(a.Answer)
			}
			raw, _ = json.Marshal(fields)
		}
		rows[q.Id] = raw
		book["Active"], _ = json.Marshal(rows)
	}
	revision := c.Revision
	if dirtyActive || dirtyBook {
		out.Mutation.Quests, _ = json.Marshal(book)
		revision++
	}
	out.Ack = little.AppendUint32(little.AppendUint16([]byte{1}, q.Id), remaining)
	out.Full = (dirtyActive || dirtyBook) && q.Kind != question && q.Kind != reach
	if q.Kind == question && p.hist.Cleared[q.Id] != 0 {
		return out, nil, nil
	}
	trigger := req.Trigger
	if q.Kind == question {
		trigger = 0
	}
	fact := &Confirmation{CharacterID: c.ID, Revision: revision, AssetRevision: in.AssetRevision, Trigger: trigger, Remaining: remaining, ObjectiveHash: q.Hash}
	if d != nil {
		fact.DungeonRun, fact.RoomX, fact.RoomY = d.RunID, d.X, d.Y
	}
	return out, fact, nil
}
