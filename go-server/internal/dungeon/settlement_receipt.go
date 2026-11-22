package dungeon

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"

	"s9a1/server/internal/savejson"
	"s9a1/server/internal/storage"
)

func (r *Run) ReadSettlementResult(raw, body []byte) (ClearResult, error) {
	var z ClearResult
	type plain ClearResult
	data := struct {
		*plain
		FreeCardReward, PaidCardReward, CompletionReward json.RawMessage
		Body                                             []byte
	}{plain: (*plain)(&z)}
	rank, err := ReadResult(body)
	if err != nil || json.Unmarshal(raw, &data) != nil || rank > 100 || z.RankPoint != rank ||
		z.ElapsedMilliseconds != r.Elapsed || z.MonsterExperienceTotals != r.Totals || data.Body != nil && !bytes.Equal(data.Body, body) {
		return ClearResult{}, storage.ErrConflict
	}
	z.Body = bytes.Clone(body)
	if z.FreeCardReward, err = DecodeFreeReceiptPlan(data.FreeCardReward); err != nil {
		return ClearResult{}, err
	}
	if z.PaidCardReward, err = DecodePaidReceiptPlan(data.PaidCardReward); err != nil {
		return ClearResult{}, err
	}
	if z.CompletionReward, err = DecodeCompletionReceiptPlan(data.CompletionReward); err != nil {
		return ClearResult{}, err
	}
	def := r.Definition.Dungeon
	var names []NamedExperience
	if def.ID < 100000000 || r.OculusEntry != nil {
		_, names = r.RewardProof()
	}
	if !slices.Equal(names, z.NamedExperience) || len(names) > 256 {
		return ClearResult{}, storage.ErrConflict
	}
	if z.SpecialReward != nil {
		id := z.SpecialReward.DungeonId
		if id == 0 {
			id = 100000002
		}
		if id != def.ID || z.FreeCardReward == nil {
			return ClearResult{}, storage.ErrConflict
		}
	}
	if z.CompletionReward != nil && z.CompletionReward.completionSnapshot != nil {
		p := z.CompletionReward.completionSnapshot
		if p.DungeonId != def.ID || p.Maze != def.MazeIndex {
			return ClearResult{}, storage.ErrConflict
		}
	}
	if (z.OculusReward != nil) != (r.OculusEntry != nil) || z.OculusReward != nil && z.OculusReward.DungeonId != def.ID ||
		(def.DisableClearReward || !def.UsesOrdinaryRewards) && (z.FreeCardReward != nil || z.PaidCardReward != nil || z.SpecialReward != nil || z.CompletionReward != nil) ||
		(z.AbaddonMail != nil || z.AbaddonTimes != nil || z.AbaddonReward != nil) && r.AbaddonEntry == nil {
		return ClearResult{}, storage.ErrConflict
	}
	if z.ClearExperience == nil {
		if data.Body == nil && (z.ClearExperienceAward != 0 || z.RankExperienceAward != 0) {
			return ClearResult{}, storage.ErrConflict
		}
		return z, nil
	}
	p := z.ClearExperience
	if !def.UsesOrdinaryRewards || def.DisableClearReward || def.DisableExperience || p.Policy != "CommittedMonsterTotalClear/v1" || !isHash(p.Rate.ResourceHash) || p.After < p.Before || p.After-p.Before != uint64(z.ClearExperienceAward)+uint64(z.RankExperienceAward) {
		return ClearResult{}, storage.ErrConflict
	}
	var total uint64
	for _, n := range r.Totals {
		total += uint64(n)
	}
	basis, err := scaledFloor(total, min(math.MaxUint32, math.MaxUint64-p.Before), p.Rate.Multiplier)
	if err != nil || basis != uint64(z.ClearExperienceAward) {
		return ClearResult{}, storage.ErrConflict
	}
	var bonus uint64
	if rates := p.Rank; rates != nil {
		if !isHash(rates.ResourceHash) || len(rates.Grades) != 8 || len(rates.Rates) != 5 {
			return ClearResult{}, storage.ErrConflict
		}
		for i := range 5 {
			if rank >= rates.Grades[i] {
				bonus, err = scaledFloor(basis, min(math.MaxUint32-basis, math.MaxUint64-p.Before-basis), rates.Rates[4-i])
				break
			}
		}
	}
	if err != nil || bonus != uint64(z.RankExperienceAward) {
		return ClearResult{}, storage.ErrConflict
	}
	return z, nil
}

// SettlementResultJSON writes LocalDungeonSettlementReceiptResult. The client
// report body is already frozen in the input. The card shapes depend on the
// award slot and cannot be recovered from CardReward alone.
func (r *Run) SettlementResultJSON(z ClearResult) ([]byte, error) {
	free, err := z.FreeCardReward.FreeReceiptPlan()
	if err != nil {
		return nil, err
	}
	paid, err := z.PaidCardReward.PaidReceiptPlan()
	if err != nil {
		return nil, err
	}
	completion, err := r.CounterCompletion(z.CompletionReward)
	if err != nil {
		return nil, err
	}
	return savejson.Marshal(struct {
		ElapsedMilliseconds     uint32
		RankPoint               byte
		MonsterExperienceTotals [4]uint32
		ClearExperienceAward    uint32
		FreeCardReward          *CardCounterReward          `json:",omitempty"`
		PaidCardReward          *PaidReceiptCardPlan        `json:",omitempty"`
		RankExperienceAward     uint32                      `json:",omitempty"`
		NamedExperience         []NamedExperience           `json:",omitempty"`
		SpecialReward           *SpecialCardPlan            `json:",omitempty"`
		OculusReward            *OculusCardPlan             `json:",omitempty"`
		CompletionReward        *CardCounterCompletion      `json:",omitempty"`
		ClearExperience         *ClearExperienceProof       `json:",omitempty"`
		AbaddonReward           *storage.AbaddonFixedReward `json:",omitempty"`
		AbaddonMail             *storage.AbaddonMail        `json:",omitempty"`
		AbaddonTimes            *storage.AbaddonClearTimes  `json:",omitempty"`
	}{z.ElapsedMilliseconds, z.RankPoint, z.MonsterExperienceTotals, z.ClearExperienceAward, free, paid, z.RankExperienceAward,
		z.NamedExperience, z.SpecialReward, z.OculusReward, completion, z.ClearExperience, z.AbaddonReward, z.AbaddonMail, z.AbaddonTimes})
}
