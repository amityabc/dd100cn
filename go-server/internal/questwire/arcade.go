package questwire

import (
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/storage"
)

type DuelObjective struct {
	ID               uint16
	Hash, Acceptance string
	Remaining        [3]uint32
}

func (d DuelObjective) StageReady(stage byte) bool {
	return stage < 3 && d.Remaining[stage] != 0 && !slices.Contains(d.Remaining[:stage], uint32(1))
}

func trainingFact(stage int) string { return "duel-training:" + strconv.Itoa(stage) }

func validateTraining(q *Quest, a active) (DuelObjective, error) {
	var d DuelObjective
	if q == nil || q.Operations == nil || !q.Operations.DuelTraining || len(q.Data) != 4 || a.ObjectiveHash != q.Hash || a.Receipts == nil || len(a.Remaining) != 3 {
		return d, storage.ErrInvalid
	}
	for _, receipt := range a.Receipts {
		key, ok := strings.CutPrefix(receipt, "duel-accept:")
		if !ok {
			continue
		}
		if _, err := hex.DecodeString(key); d.Acceptance != "" || len(key) != 32 || err != nil {
			return d, storage.ErrInvalid
		}
		d.Acceptance = receipt
	}
	if d.Acceptance == "" {
		return d, storage.ErrInvalid
	}
	for i, remaining := range a.Remaining {
		fact := slices.Contains(a.Receipts, trainingFact(i))
		if remaining > uint32(q.Data[i]) || fact != (q.Data[i] != 0 && remaining == 0) {
			return d, storage.ErrInvalid
		}
		d.Remaining[i] = remaining
	}
	d.ID, d.Hash = q.Id, q.Hash
	return d, nil
}

// With no requested identity the dojo may bind exactly one active training objective.
func (cat *Catalog) DuelObjective(c storage.Character, id uint16) (DuelObjective, error) {
	var d DuelObjective
	p, err := playerOf(c)
	if err != nil {
		return d, err
	}
	for _, known := range [...]uint16{2672, 2673, 2674} {
		a, found := p.hist.Active[known]
		if !found || id != 0 && id != known {
			continue
		}
		if d.ID != 0 || !p.initialized {
			return d, storage.ErrInvalid
		}
		d, err = validateTraining(cat.ordinary[known], a)
		if err != nil {
			return d, err
		}
	}
	return d, nil
}

func (cat *Catalog) PracticeAdvanced(c storage.Character, id uint16, hash, acceptance string) bool {
	p, err := playerOf(c)
	a, ok := p.hist.Active[id]
	return err == nil && ok && a.ObjectiveHash == hash && len(a.Remaining) == 3 && a.Remaining[1] == 0 && slices.Contains(a.Receipts, acceptance)
}

// AdvanceDuelStage updates only the two fields written by LocalQuestDuelTraining.Advance.
func AdvanceDuelStage(c storage.Character, in storage.Inventory, q *Quest, trigger, increment byte, explicit bool,
	qualifies func(DuelObjective, byte) bool) (json.RawMessage, error) {
	var gameplay struct {
		Quests struct {
			Active map[uint16]map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(c.Gameplay, &gameplay); err != nil {
		return nil, err
	}
	row := gameplay.Quests.Active[q.Id]
	data, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	var a active
	if err = json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	d, err := validateTraining(q, a)
	if err != nil || !explicit || increment != 0 {
		return nil, storage.ErrInvalid
	}
	var stage byte
	switch trigger {
	case 0x10:
		stage = 0
	case 0x20:
		stage = 1
	case 0x40:
		stage = 2
	default:
		return nil, storage.ErrInvalid
	}
	if q.Data[stage] == 0 {
		return nil, storage.ErrInvalid
	}
	if d.Remaining[stage] == 0 {
		return data, nil
	}
	if stage == 0 && q.Id != 2673 || !d.StageReady(stage) || !qualifies(d, stage) {
		return nil, storage.ErrInvalid
	}
	if q.Id == 2674 && stage == 2 && !slices.ContainsFunc(in.Items, func(item storage.Asset) bool {
		return item.Location == 0 && item.TemplateID == 10158452 && item.Count > 0
	}) {
		return nil, storage.ErrInvalid
	}
	a.Remaining[stage] = 0
	a.Receipts = append(a.Receipts, trainingFact(int(stage)))
	row["Remaining"], err = json.Marshal(a.Remaining)
	if err != nil {
		return nil, err
	}
	row["Receipts"], err = json.Marshal(a.Receipts)
	if err != nil {
		return nil, err
	}
	return json.Marshal(row)
}
