package storage

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
)

const (
	OculusClearPolicy       = "OculusCommittedClear/v1"
	oculusRewardPolicy      = "OfficialSharedOculusSpecialCnSchedule/v1"
	oculusResourcePolicy    = "LocalOculusResourceReward/v2"
	oculusClaimVersion      = "local100-oculus-free-special/v1"
	oculusCounterPrefix     = "oculus-reward:week:"
	settlementReceiptPrefix = "dungeon-settlement:v1:"
	freeCardReceiptPrefix   = "dungeon-free-card:v1:"
)

type OculusClearCoins struct{ Total, Used byte }

type OculusClearQualification struct {
	Policy, RunId       string
	Mode                uint16
	Dungeon             uint32
	ModeHash, SceneHash string
	SceneGeneration     uint32
	ActorFirst          uint16
	AdmissionInput      string
	Ritual              OculusRitualProof
	Coins               OculusClearCoins
}

type oculusSettlementIdentity struct {
	Owner       string
	Epoch       int64
	CharacterId uint16
	RunId       string
}

func (s oculusSettlementIdentity) valid(character uint16) bool {
	return s.CharacterId == character && matchesUUID(s.Owner) && s.Epoch > 0 && matchesUUID(s.RunId)
}

func (s oculusSettlementIdentity) key(prefix string) string {
	return fmt.Sprintf("%s%s:%d:%d:%s", prefix, strings.ReplaceAll(s.Owner, "-", ""), s.Epoch, s.CharacterId, strings.ReplaceAll(s.RunId, "-", ""))
}

type oculusSettlementInput struct {
	oculusSettlementIdentity
	Version                 string
	Dungeon, BossMap        uint32
	Maze                    byte
	BossActor               uint16
	ResourceHash            string
	ElapsedMilliseconds     uint32
	MonsterExperienceTotals []uint32
	Body                    string
	Oculus                  *OculusClearQualification
}

type oculusSettlementResult struct {
	ElapsedMilliseconds                                                           uint32
	RankPoint                                                                     byte
	MonsterExperienceTotals                                                       []uint32
	ClearExperienceAward, RankExperienceAward                                     uint32
	FreeCardReward, PaidCardReward, SpecialReward, CompletionReward, OculusReward json.RawMessage
}

type oculusRewardPlan struct {
	Policy         string
	Week           int64
	Limit, Ordinal byte
	DungeonId      uint32
	Reward         *oculusRewardRounds
}

type oculusRewardRounds struct {
	Policy, ResourceHash        string
	DungeonId, CounterDungeonId uint32
	Limit, RoundCount           byte
	CycleRaw                    int
	Pool                        struct {
		Kind, Difficulty byte
		PartyKindRaw     int
	}
	Rounds []struct {
		SelectedRow int
		Container   uint32
		Items       []struct {
			Template, Attributes json.RawMessage
			Count                uint32
			Raw119, Detail       []byte
		}
	}
}

type oculusFrozenResult struct {
	identity oculusSettlementIdentity
	receipt  deathReceipt
	plan     oculusRewardPlan
	raw      json.RawMessage
}

type oculusClaimInput struct {
	oculusSettlementIdentity
	Version, ResultKey string
	Member             byte
	Reward             json.RawMessage
}

type oculusRewardLedger struct {
	results        map[string]oculusFrozenResult
	claims         map[string]bool
	claimRevisions map[int64]int64
	latestWeek     int64
}

func (s *Store) OculusCountsView(ctx context.Context, l Lease, c Character, now time.Time) (OculusCounts, int64, bool, error) {
	type view struct {
		entry OculusCounts
		week  int64
		used  bool
	}
	out, err := writeTx(ctx, s.db, func(tx *sql.Tx) (view, error) {
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil {
			return view{}, err
		}
		if c.ID == 0 || r.SelectedID != c.ID || r.Phase != Selected && r.Phase != TownLoading && r.Phase != TownReady {
			return view{}, ErrPhase
		}
		current, err := mailCharacter(ctx, tx, l.Account, r.SelectedID)
		if err != nil {
			return view{}, err
		}
		count, ledger, err := readOculusRewardLedger(ctx, tx, current, now)
		return view{count, ledger.latestWeek, ledger.claimRevisions[ledger.latestWeek] != 0}, err
	})
	return out.entry, out.week, out.used, err
}

func readOculusRewardLedger(ctx context.Context, tx *sql.Tx, c Character, now time.Time) (OculusCounts, oculusRewardLedger, error) {
	count, _, _, err := readOculusLedger(ctx, tx, c, now)
	if err != nil {
		return count, oculusRewardLedger{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_key,payload::text FROM s9a1.reward_receipts WHERE character_id=$1 AND (
		starts_with(event_key,'oculus-entry:run:') OR starts_with(event_key,'oculus-reward:week:') OR
		starts_with(event_key,'dungeon-settlement:v1:') OR starts_with(event_key,'dungeon-free-card:v1:'))`, c.ID)
	if err != nil {
		return count, oculusRewardLedger{}, err
	}
	defer rows.Close()
	history := map[string]deathReceipt{}
	for rows.Next() {
		var key, data string
		var record deathReceipt
		if err := rows.Scan(&key, &data); err != nil {
			return count, oculusRewardLedger{}, err
		}
		if json.Unmarshal([]byte(data), &record) != nil || record.Key != key || record.Grants == nil || record.Revision <= 0 ||
			record.Revision > c.Revision || record.Input == "" || record.Result == "" {
			return count, oculusRewardLedger{}, ErrConflict
		}
		history[key] = record
	}
	if err := rows.Err(); err != nil {
		return count, oculusRewardLedger{}, err
	}
	ledger, err := validateOculusRewardLedger(c.ID, count.Week, history)
	return count, ledger, err
}

func validateOculusRewardLedger(character uint16, entryWeek int64, history map[string]deathReceipt) (oculusRewardLedger, error) {
	z := oculusRewardLedger{
		results: map[string]oculusFrozenResult{}, claims: map[string]bool{}, claimRevisions: map[int64]int64{}, latestWeek: entryWeek,
	}
	var results []deathReceipt
	for key, row := range history {
		if strings.HasPrefix(key, settlementReceiptPrefix) && strings.HasSuffix(key, ":result") {
			results = append(results, row)
		}
	}
	slices.SortFunc(results, func(a, b deathReceipt) int {
		return cmpReceiptRevision(a, b)
	})
	var previousWeek int64
	for _, record := range results {
		var input oculusSettlementInput
		var result oculusSettlementResult
		if json.Unmarshal([]byte(record.Input), &input) != nil || json.Unmarshal([]byte(record.Result), &result) != nil ||
			!jsonObjectBody(record.Input) || !jsonObjectBody(record.Result) {
			return z, ErrConflict
		}
		if !hasEffect(result.OculusReward) {
			if input.Version == OculusClearPolicy || input.Oculus != nil {
				return z, ErrConflict
			}
			continue
		}
		frozen, err := readOculusFrozenResult(character, input, result, record, history)
		if err != nil || frozen.plan.Week < previousWeek {
			return z, ErrConflict
		}
		z.results[record.Key] = frozen
		z.latestWeek = max(z.latestWeek, frozen.plan.Week)
		previousWeek = frozen.plan.Week
	}
	for key, mark := range history {
		if !strings.HasPrefix(key, oculusCounterPrefix) {
			continue
		}
		claim, frozen, err := oculusClaimFromInput(character, mark.Input, z)
		if err != nil || key != oculusRewardCounter(frozen.plan.Week) || z.claimRevisions[frozen.plan.Week] != 0 ||
			frozen.receipt.Revision >= mark.Revision || !equalJSON([]byte(mark.Result), oculusClaimResult()) ||
			hasEffect(mark.SpecialGrants) {
			return z, ErrConflict
		}
		freeKey := claim.key(freeCardReceiptPrefix)
		free, ok := history[freeKey]
		if !ok || free.Input != mark.Input || free.Result != mark.Result || free.Revision != mark.Revision ||
			hasEffect(free.SpecialGrants) || !equalJSON(mustDungeonJSON(free.Grants), mustDungeonJSON(mark.Grants)) || z.claims[freeKey] {
			return z, ErrConflict
		}
		if err := validateOculusRewardGrants(frozen.plan, mark.Grants); err != nil {
			return z, err
		}
		z.claims[freeKey] = true
		z.claimRevisions[frozen.plan.Week] = mark.Revision
		z.latestWeek = max(z.latestWeek, frozen.plan.Week)
	}
	for _, frozen := range z.results {
		claim := z.claimRevisions[frozen.plan.Week]
		used := claim != 0 && claim < frozen.receipt.Revision
		if used != (frozen.plan.Ordinal == 0) {
			return z, ErrConflict
		}
	}
	for key, receipt := range history {
		if !strings.HasPrefix(key, freeCardReceiptPrefix) {
			continue
		}
		var input struct{ Version string }
		if !jsonObjectBody(receipt.Input) || json.Unmarshal([]byte(receipt.Input), &input) != nil ||
			input.Version == oculusClaimVersion && !z.claims[key] {
			return z, ErrConflict
		}
	}
	return z, nil
}

func cmpReceiptRevision(a, b deathReceipt) int {
	if a.Revision < b.Revision {
		return -1
	}
	if a.Revision > b.Revision {
		return 1
	}
	return strings.Compare(a.Key, b.Key)
}

func jsonObjectBody(raw string) bool { return strings.HasPrefix(strings.TrimSpace(raw), "{") }

func oculusRewardCounter(week int64) string {
	return fmt.Sprintf("%s100000007:%d:1", oculusCounterPrefix, week)
}

func oculusClaimResult() json.RawMessage {
	return json.RawMessage(`{"Version":"local100-oculus-free-special/v1","Status":"granted"}`)
}

func validOculusWeek(week int64) bool {
	t, err := time.Parse("20060102", fmt.Sprint(week))
	return err == nil && week >= 19991230 && t.Year() <= 9998 && t.Weekday() == time.Thursday
}

func readOculusFrozenResult(character uint16, input oculusSettlementInput, result oculusSettlementResult,
	receipt deathReceipt, history map[string]deathReceipt) (oculusFrozenResult, error) {
	var frozen oculusFrozenResult
	var plan oculusRewardPlan
	if !input.valid(character) || input.Version != OculusClearPolicy || input.Oculus == nil ||
		!matchesSHA(input.ResourceHash) || input.BossMap == 0 || input.BossActor < 402 ||
		receipt.Key != input.key(settlementReceiptPrefix)+":result" ||
		receipt.Grants == nil || len(receipt.Grants) != 0 || hasEffect(receipt.SpecialGrants) ||
		json.Unmarshal(result.OculusReward, &plan) != nil || !plan.valid() || plan.DungeonId != input.Dungeon ||
		result.ClearExperienceAward != 0 || result.RankExperienceAward != 0 || hasEffect(result.FreeCardReward) ||
		hasEffect(result.PaidCardReward) || hasEffect(result.SpecialReward) || hasEffect(result.CompletionReward) ||
		len(input.MonsterExperienceTotals) != 4 || !slices.Equal(input.MonsterExperienceTotals, result.MonsterExperienceTotals) ||
		result.ElapsedMilliseconds != input.ElapsedMilliseconds {
		return frozen, ErrConflict
	}
	body, err := base64.StdEncoding.DecodeString(input.Body)
	if err != nil || base64.StdEncoding.EncodeToString(body) != input.Body || len(body) > 46+7*255 ||
		!(len(body) >= 42 && (len(body)-42)%7 == 0 || len(body) >= 46 && (len(body)-46)%7 == 0) ||
		result.RankPoint != body[10] {
		return frozen, ErrConflict
	}
	clear := input.Oculus
	var admitted OculusEntry
	admissionKey := "oculus-entry:run:" + strings.ReplaceAll(input.RunId, "-", "")
	admission, ok := history[admissionKey]
	if !ok || admission.Key != admissionKey || admission.Revision <= 0 || admission.Revision >= receipt.Revision ||
		admission.Result != "admitted" || admission.Input != clear.AdmissionInput || json.Unmarshal([]byte(admission.Input), &admitted) != nil ||
		admitted.Owner != input.Owner || admitted.Epoch != input.Epoch || admitted.CharacterId != input.CharacterId ||
		admitted.RunId != input.RunId || admitted.Dungeon != input.Dungeon || !validOculusWeek(admitted.Week) || plan.Week < admitted.Week ||
		admitted.Mode != clear.Mode || admitted.ModeHash != clear.ModeHash || clear.SceneGeneration == 1 &&
		(admitted.ActorFirst != clear.ActorFirst || admitted.DungeonHash != clear.SceneHash) ||
		!validOculusClearInput(input, receipt.Input) {
		return frozen, ErrConflict
	}
	entryKey := input.key(settlementReceiptPrefix)
	entry, ok := history[entryKey]
	if !ok || entry.Key != entryKey || entry.Revision <= admission.Revision || entry.Revision >= receipt.Revision ||
		entry.Grants == nil || len(entry.Grants) != 0 || hasEffect(entry.SpecialGrants) {
		return frozen, ErrConflict
	}
	var entered map[string]json.RawMessage
	var entryResult oculusSettlementResult
	if json.Unmarshal([]byte(entry.Input), &entered) != nil || !hasEffect(entered["Body"]) ||
		json.Unmarshal([]byte(entry.Result), &entryResult) != nil ||
		entryResult.RankPoint != 0 || entryResult.ClearExperienceAward != 0 || entryResult.RankExperienceAward != 0 ||
		entryResult.ElapsedMilliseconds != input.ElapsedMilliseconds ||
		!slices.Equal(entryResult.MonsterExperienceTotals, input.MonsterExperienceTotals) ||
		hasEffect(entryResult.FreeCardReward) || hasEffect(entryResult.PaidCardReward) || hasEffect(entryResult.SpecialReward) ||
		hasEffect(entryResult.CompletionReward) || hasEffect(entryResult.OculusReward) {
		return frozen, ErrConflict
	}
	entered["Body"] = mustDungeonJSON(input.Body)
	if !equalJSON(mustDungeonJSON(entered), []byte(receipt.Input)) {
		return frozen, ErrConflict
	}
	return oculusFrozenResult{input.oculusSettlementIdentity, receipt, plan, result.OculusReward}, nil
}

func validOculusClearInput(input oculusSettlementInput, raw string) bool {
	q := input.Oculus
	ritual := q.Ritual
	if q.Policy != OculusClearPolicy || q.RunId != input.RunId || q.Dungeon != input.Dungeon ||
		q.Dungeon != oculusDungeon(q.Mode) || q.SceneHash != input.ResourceHash || !matchesSHA(q.ModeHash) ||
		q.SceneGeneration < 1 || q.SceneGeneration > 129 || q.ActorFirst < 402 || q.Coins.Total == 0 || q.Coins.Used > q.Coins.Total ||
		ritual.Policy != "OculusCommittedRitualClock/v1" || ritual.RunId != q.RunId || ritual.Dungeon != q.Dungeon ||
		ritual.Mode != q.Mode || ritual.ResourceHash != q.ModeHash || ritual.RemainingMilliseconds <= 0 {
		return false
	}
	var document struct {
		Oculus struct{ Ritual struct{ Expired *bool } }
	}
	return json.Unmarshal([]byte(raw), &document) == nil &&
		(document.Oculus.Ritual.Expired == nil || !*document.Oculus.Ritual.Expired) && validOculusRitualClock(ritual)
}

func validOculusRitualClock(p OculusRitualProof) bool {
	if p.StartedAt < 0 || p.ObservedAt < p.StartedAt || p.Frequency <= 0 || p.LimitSeconds < 1 || p.LimitSeconds > 86400 ||
		p.Reductions == nil || len(p.Reductions) > 128 || p.RemainingMilliseconds < 0 {
		return false
	}
	// Keep hundredths of a stopwatch tick so the .NET and Go frequencies
	// both retain their exact percentage reductions before the millisecond ceiling.
	frequency := big.NewInt(p.Frequency)
	scale := new(big.Int).Mul(frequency, big.NewInt(100))
	full := new(big.Int).Mul(scale, big.NewInt(int64(p.LimitSeconds)))
	left := new(big.Int).Set(full)
	seenActors, seenDeaths := map[uint16]bool{}, map[string]bool{}
	at := p.StartedAt
	for _, reduction := range p.Reductions {
		if reduction.Actor < 402 || reduction.Monster == 0 || reduction.Percent < 1 || reduction.Percent > 100 ||
			strings.TrimSpace(reduction.DeathKey) == "" || len(reduction.DeathKey) > 256 || seenActors[reduction.Actor] ||
			seenDeaths[reduction.DeathKey] || reduction.AtTimestamp < at || reduction.AtTimestamp > p.ObservedAt {
			return false
		}
		seenActors[reduction.Actor], seenDeaths[reduction.DeathKey] = true, true
		elapsed := new(big.Int).Mul(big.NewInt(reduction.AtTimestamp-at), big.NewInt(100))
		left.Sub(left, elapsed)
		if left.Sign() <= 0 {
			return false
		}
		gain := new(big.Int).Mul(frequency, big.NewInt(int64(p.LimitSeconds)*int64(reduction.Percent)))
		left.Add(left, gain)
		if left.Cmp(full) > 0 {
			left.Set(full)
		}
		at = reduction.AtTimestamp
	}
	left.Sub(left, new(big.Int).Mul(big.NewInt(p.ObservedAt-at), big.NewInt(100)))
	if left.Sign() <= 0 {
		return p.RemainingMilliseconds == 0
	}
	left.Mul(left, big.NewInt(1000))
	left.Add(left, new(big.Int).Sub(scale, big.NewInt(1)))
	left.Quo(left, scale)
	return left.IsInt64() && left.Int64() == p.RemainingMilliseconds
}

func (p oculusRewardPlan) valid() bool {
	if p.Policy != oculusRewardPolicy || !validOculusWeek(p.Week) || p.Limit != 1 || p.Ordinal > 1 ||
		p.DungeonId != oculusDungeon(0) && p.DungeonId != oculusDungeon(2) && p.DungeonId != oculusDungeon(6) ||
		(p.Ordinal == 0) != (p.Reward == nil) {
		return false
	}
	if p.Reward == nil {
		return true
	}
	r := p.Reward
	if r.Policy != oculusResourcePolicy || !matchesSHA(r.ResourceHash) || r.DungeonId != p.DungeonId ||
		r.CounterDungeonId != oculusDungeon(0) || r.CycleRaw != 1 || r.Limit != 1 || r.Pool.Kind != 0 || r.Pool.Difficulty != 4 ||
		r.Pool.PartyKindRaw != 0 || r.RoundCount < 1 || r.RoundCount > 2 || len(r.Rounds) != int(r.RoundCount) {
		return false
	}
	for _, round := range r.Rounds {
		if round.SelectedRow < 0 || round.SelectedRow >= 128 || round.Container == 0 || len(round.Items) < 1 || len(round.Items) > 256 {
			return false
		}
		for _, item := range round.Items {
			var t ordinaryTemplate
			var lifetime materialDefinition
			var attr materialAttributes
			if item.Count == 0 || item.Count > 1000000 || len(item.Raw119) != 119 || item.Detail == nil ||
				json.Unmarshal(item.Template, &t) != nil || json.Unmarshal(item.Template, &lifetime) != nil ||
				json.Unmarshal(item.Attributes, &attr) != nil || !jsonObjectBody(string(item.Attributes)) ||
				t.TemplateID == 0 || t.TemplateID == ^uint32(0) || t.Kind < 0 || t.Kind > 2 || t.StackLimit == 0 ||
				t.MinimumLevel == 0 || t.UsableJobs == 0 || t.FirstBagSlot < 3 || t.LastBagSlot < t.FirstBagSlot || t.LastBagSlot > 360 ||
				!matchesSHA(t.ResourceHash) || t.FateCollection || t.AccountCommonInventory ||
				t.Kind == 2 && (attr.QualitySeed != 0 || attr.Durability != 0) ||
				t.Kind != 2 && (attr.QualitySeed == 0 || attr.QualitySeed >= 999999999 || attr.Durability > t.MaximumDurability) {
				return false
			}
			asset := Asset{Core: item.Raw119, Detail: item.Detail}
			plain, err := ordinaryNewCore(asset, lifetime, attr)
			if err != nil || !plain {
				return false
			}
		}
	}
	return true
}

func oculusClaimFromInput(character uint16, raw string, ledger oculusRewardLedger) (oculusClaimInput, oculusFrozenResult, error) {
	var claim oculusClaimInput
	if json.Unmarshal([]byte(raw), &claim) != nil || claim.Version != oculusClaimVersion ||
		!claim.valid(character) || claim.Member != 0 || claim.ResultKey != claim.key(settlementReceiptPrefix)+":result" {
		return claim, oculusFrozenResult{}, ErrConflict
	}
	frozen, ok := ledger.results[claim.ResultKey]
	if !ok || frozen.identity != claim.oculusSettlementIdentity || frozen.plan.Ordinal != 1 || !equalJSON(frozen.raw, claim.Reward) {
		return claim, oculusFrozenResult{}, ErrConflict
	}
	return claim, frozen, nil
}

func validateOculusRewardGrants(plan oculusRewardPlan, raw []json.RawMessage) error {
	if plan.Reward == nil || len(raw) == 0 {
		return ErrConflict
	}
	type expectedReward struct {
		Template ordinaryTemplate
		Count    uint64
	}
	expected := map[uint32]expectedReward{}
	for _, round := range plan.Reward.Rounds {
		for _, item := range round.Items {
			var t ordinaryTemplate
			if json.Unmarshal(item.Template, &t) != nil {
				return ErrConflict
			}
			expected[t.TemplateID] = expectedReward{t, expected[t.TemplateID].Count + uint64(item.Count)}
		}
	}
	uids, slots := map[string]bool{}, map[uint16]bool{}
	for _, b := range raw {
		var grant questGrantResult
		if json.Unmarshal(b, &grant) != nil || !matchesUUID(grant.InstanceId) || uids[grant.InstanceId] ||
			grant.Granted == 0 || grant.Location != 0 || slots[grant.Slot] {
			return ErrConflict
		}
		want, ok := expected[grant.TemplateId]
		t := want.Template
		sharedSlot, shared := SharedMaterialSlot(t.TemplateID)
		ordinarySlot := grant.Slot >= t.FirstBagSlot && grant.Slot <= t.LastBagSlot
		quickSlot := grant.Slot >= 3 && grant.Slot <= 8 && !t.FateCollection && !t.AccountCommonInventory && !t.PetConsumable
		if !ok || uint64(grant.Granted) > want.Count || !ordinarySlot && !quickSlot && !(shared && t.Kind == 2 && grant.Slot == sharedSlot) {
			return ErrConflict
		}
		want.Count -= uint64(grant.Granted)
		expected[grant.TemplateId] = want
		uids[grant.InstanceId], slots[grant.Slot] = true, true
	}
	for _, item := range expected {
		if item.Count != 0 {
			return ErrConflict
		}
	}
	return nil
}
