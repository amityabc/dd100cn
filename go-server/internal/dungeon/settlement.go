package dungeon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/storage"
)

type Settlement struct {
	EntryInput, EntryResult string
	Boss                    uint16
	Confirmation            []byte
	Pending                 []byte
	pendingByActor          map[uint16][]byte
	Result                  *ClearResult
	LayoutSent              bool
	LayoutOpenedAt          time.Time
	FreeClaimed             bool
	PaidClaimed             bool
	FreeTimeoutBlocked      bool
}

func (r *Run) CardDeadline() time.Time {
	if r == nil || !r.Ready || !r.Cleared || r.Settlement.Result == nil || !r.Settlement.LayoutSent ||
		r.Settlement.FreeClaimed || r.Settlement.FreeTimeoutBlocked || r.Settlement.LayoutOpenedAt.IsZero() {
		return time.Time{}
	}
	return r.Settlement.LayoutOpenedAt.Add(4 * time.Second)
}

type ClearResult struct {
	Body                    []byte `json:"-"`
	ElapsedMilliseconds     uint32
	RankPoint               byte
	MonsterExperienceTotals [4]uint32
	ClearExperienceAward    uint32
	FreeCardReward          *CardReward                 `json:",omitempty"`
	PaidCardReward          *CardReward                 `json:",omitempty"`
	RankExperienceAward     uint32                      `json:",omitempty"`
	NamedExperience         []NamedExperience           `json:",omitempty"`
	SpecialReward           *SpecialCardPlan            `json:",omitempty"`
	OculusReward            *OculusCardPlan             `json:",omitempty"`
	CompletionReward        *CardReward                 `json:",omitempty"`
	ClearExperience         *ClearExperienceProof       `json:",omitempty"`
	AbaddonReward           *storage.AbaddonFixedReward `json:",omitempty"`
	AbaddonMail             *storage.AbaddonMail        `json:",omitempty"`
	AbaddonTimes            *storage.AbaddonClearTimes  `json:",omitempty"`
}

type ClearExperienceProof struct {
	Policy        string
	Rate          ClearExperienceData
	Before, After uint64
	Rank          *RankExperienceData `json:",omitempty"`
}

type CardReward struct {
	Policy, ResourceHash string
	Gold                 uint32
	Cost                 uint32     `json:",omitempty"`
	UnavailableReason    string     `json:",omitempty"`
	Surprise             bool       `json:",omitempty"`
	Items                []CardItem `json:",omitempty"`
	luckSnapshot         *CardLuckSnapshot
	completionSnapshot   *CardCounterCompletion
	legacyPlan           json.RawMessage
}

type NamedExperience struct{ Code, Experience uint32 }

func ReadConfirmation(b []byte) (actor, owner uint16, err error) {
	if len(b) != 39 || le.Uint32(b[4:]) != 0 || b[12] != 0 {
		return 0, 0, errors.New("invalid C117 object confirmation")
	}
	return le.Uint16(b[2:]), le.Uint16(b), nil
}

func ReadResult(b []byte) (byte, error) {
	if len(b) > 46+7*255 || !(len(b) >= 42 && (len(b)-42)%7 == 0 || len(b) >= 46 && (len(b)-46)%7 == 0) {
		return 0, errors.New("invalid native play result body")
	}
	return b[10], nil
}

func (r *Run) CurrentBoss(actor uint16) (opening.Monster, bool, bool) {
	room := r.CombatRoom()
	if !r.Ready || r.PlayerDeath != nil {
		return opening.Monster{}, false, false
	}
	index := int(actor) - int(r.combatActorBase())
	monsters := room.Monsters
	if index < 0 || index >= len(monsters) {
		return opening.Monster{}, false, false
	}
	m := monsters[index]
	def := r.Definition.Dungeon
	rec, dead := r.Dead[actor]
	forest, forestCleared := r.forgottenForestClearVictim()
	isBoss := room.X == def.BossX && room.Y == def.BossY && (!m.Dummy && (m.Type == 3 || m.Code == 75100) ||
		room != r.Room && m.Code == 107002770 ||
		rec.Cinematic || forestCleared && forest == actor || dead && (m.Dummy && r.placeholderBossRoom() || r.markerRoom()))
	return m, isBoss, dead
}

func (r *Run) SetPendingConfirmation(actor uint16, body []byte) bool {
	s := &r.Settlement
	monster, _, _ := r.CurrentBoss(actor)
	if monster.Type != 3 {
		return false
	}
	if old := s.pendingByActor[actor]; old != nil {
		return bytes.Equal(old, body)
	}
	if s.pendingByActor == nil {
		s.pendingByActor = make(map[uint16][]byte)
	}
	s.pendingByActor[actor] = slices.Clone(body)
	if s.Pending == nil {
		s.Pending = s.pendingByActor[actor]
	}
	return true
}

func (r *Run) ClearingConfirmationMatches(actor uint16, body []byte) bool {
	pending := r.Settlement.pendingByActor[actor]
	return pending == nil || bytes.Equal(pending, body)
}

func (r *Run) TempleConfirmationBody(actor uint16) ([]byte, bool) {
	d, room := r.Definition.Dungeon, r.Room
	if d.ID != 24 || d.MazeIndex != 2 || !slices.Equal(d.QuestConnections, []uint16{3204}) ||
		room.MapID != 53486 || len(room.Monsters) != 1 || len(room.CinematicDestroys) != 1 {
		return nil, false
	}
	m, script := room.Monsters[0], room.CinematicDestroys[0]
	death, ok := r.Dead[actor]
	if actor != r.bases[cell{room.X, room.Y}] || m.Code != 260 || m.Type != 3 || !m.Dummy ||
		script.Index != 0 || script.Movie != 227 || script.TriggerIndex != nil || !ok ||
		!death.Cinematic || !death.Removal || !death.Result.DungeonCleared || death.Result.ExperienceAward != 0 {
		return nil, false
	}
	return []byte("LocalTemplePeakCinematicClear/v1"), true
}

func (r *Run) MovieSettlementActor() (uint16, bool) {
	if r == nil || !r.Ready || !r.Cleared || r.PlayerDeath != nil || r.Room.CinematicLayer == nil ||
		r.Settlement.Confirmation != nil || r.Settlement.Pending != nil {
		return 0, false
	}
	def := r.Definition.Dungeon
	if r.Room.X != def.BossX || r.Room.Y != def.BossY {
		return 0, false
	}
	base := r.bases[cell{r.Room.X, r.Room.Y}]
	if !r.seen[scene{cell{r.Room.X, r.Room.Y}, false}] {
		return 0, false
	}
	var actor uint16
	for i, m := range r.Room.Monsters {
		candidate := base + uint16(i)
		if _, dead := r.Dead[candidate]; !dead || m.Type != 3 || m.Dummy || !m.BlocksRoom {
			continue
		}
		if actor != 0 {
			return 0, false
		}
		actor = candidate
	}
	return actor, actor != 0
}

func (r *Run) ClearProofKeys(actor uint16) []string {
	keys := make([]string, 0, len(r.Dead)+1)
	for _, death := range r.Dead {
		keys = append(keys, death.Key)
	}
	if actor == 0 {
		keys = append(keys, r.RoomKey())
	}
	sort.Strings(keys)
	return keys
}

func (r *Run) ScriptedConfirmation() ([]byte, bool) {
	if !r.Cleared || r.Settlement.Confirmation != nil || !r.Ready {
		return nil, false
	}
	if actor, ok := r.forgottenForestClearVictim(); ok {
		body := make([]byte, 39)
		le.PutUint16(body, r.Character)
		le.PutUint16(body[2:], actor)
		return body, true
	}
	if r.InLayer && !r.wisdomStoryLayerLoaded() {
		return nil, false
	}
	if r.Settlement.Pending != nil {
		return r.Settlement.Pending, true
	}
	if r.wisdomStoryRoom() {
		return nil, false
	}
	actor := uint16(0)
	base := r.actorBase()
	for i, mon := range r.Room.Monsters {
		death, dead := r.Dead[base+uint16(i)]
		if mon.Type == 3 && dead && !death.Cinematic && !mon.Dummy {
			return nil, false
		}
		if dead && (mon.Code == 75100 || death.Cinematic && death.Result.DungeonCleared || mon.Dummy && mon.BlocksRoom && r.placeholderBossRoom() ||
			r.markerRoom() && mon.Code == 74669) {
			actor = base + uint16(i)
		}
	}
	if actor == 0 && !r.EmptyBossRoom() {
		return nil, false
	}
	body := make([]byte, 39)
	le.PutUint16(body, r.Character)
	le.PutUint16(body[2:], actor)
	return body, true
}

func (r *Run) EmptyBossRoom() bool {
	d := r.Definition.Dungeon
	if r.Room.X != d.BossX || r.Room.Y != d.BossY || !r.Cleared {
		return false
	}
	base := r.actorBase()
	for i, mon := range r.Room.Monsters {
		_, dead := r.Dead[base+uint16(i)]
		if mon.BlocksRoom || dead {
			return false
		}
	}
	return true
}

func (r *Run) ClearResult(cat *Catalog, body []byte, rewardLevel byte, exp uint64, luck *LuckRoll) (ClearResult, error) {
	rank, err := ReadResult(body)
	if err != nil || rank > 100 {
		return ClearResult{}, errors.New("unsupported native rank score")
	}
	z := ClearResult{Body: slices.Clone(body), ElapsedMilliseconds: r.Elapsed, RankPoint: rank, MonsterExperienceTotals: r.Totals}
	def := r.Definition.Dungeon
	if def.DisableClearReward || !def.UsesOrdinaryRewards {
		return z, nil
	}
	weight, named := r.RewardProof()
	if def.ID < 100000000 {
		z.NamedExperience = named
	}
	if !def.DisableExperience && cat.ClearExperience.ResourceHash != "" {
		sum := uint64(r.Totals[0]) + uint64(r.Totals[1]) + uint64(r.Totals[2]) + uint64(r.Totals[3])
		n, err := scaledFloor(sum, min(math.MaxUint32, math.MaxUint64-exp), cat.ClearExperience.Multiplier)
		if err != nil {
			return ClearResult{}, err
		}
		z.ClearExperienceAward = uint32(n)
		z.ClearExperience = &ClearExperienceProof{Policy: "CommittedMonsterTotalClear/v1", Rate: cat.ClearExperience, Before: exp, After: exp + n}
		if cat.RankExperience.ResourceHash != "" {
			if len(cat.RankExperience.Rates) != 5 || len(cat.RankExperience.Grades) != 8 {
				return ClearResult{}, errors.New("invalid rank experience table")
			}
			z.ClearExperience.Rank = &cat.RankExperience
			for i := range 5 {
				if rank >= cat.RankExperience.Grades[i] {
					bonus, err := scaledFloor(n, min(math.MaxUint32-n, math.MaxUint64-exp-n), cat.RankExperience.Rates[4-i])
					if err != nil {
						return ClearResult{}, err
					}
					z.RankExperienceAward = uint32(bonus)
					z.ClearExperience.After += bonus
					break
				}
			}
		}
	}
	if weight != 0 {
		if def.BasisLevel > 0 {
			rewardLevel = def.BasisLevel
		}
		card, err := cat.cardGold(r, weight, rewardLevel, rank)
		if err != nil {
			return ClearResult{}, err
		}
		z.FreeCardReward = card
		if card != nil {
			card.Items, err = cat.Cards.freeItem(r, weight, rewardLevel, card.ResourceHash, luck)
			if err != nil {
				return ClearResult{}, err
			}
			if len(card.Items) > 0 && card.Gold != 0 {
				card.Policy = "PVFFreeCard/v2"
				if card.Artifact() != nil {
					card.Policy = "PVFFreeArtifactCard/v1"
				}
			}
			if err := card.freezeCardLuck(luck); err != nil {
				return ClearResult{}, err
			}
		}
		z.PaidCardReward, err = cat.Cards.paidPlan(r, weight, rewardLevel, luck)
		if err != nil {
			return ClearResult{}, err
		}
		if z.PaidCardReward != nil && !z.PaidCardReward.Surprise {
			if err := z.PaidCardReward.freezeCardLuck(luck); err != nil {
				return ClearResult{}, err
			}
		}
	}
	z.CompletionReward, err = cat.Drops.PvfRewards.completion(r)
	return z, err
}

func (r *Run) RewardProof() (uint32, []NamedExperience) {
	var weight uint32
	named := map[uint32]uint32{}
	for k, room := range r.rooms {
		base := r.bases[k]
		for i, mon := range room.Monsters {
			d, ok := r.Dead[base+uint16(i)]
			if !ok || d.Removal {
				continue
			}
			weight += [...]uint32{1, 2, 2, 4}[mon.Type]
			if d.Result.ExperienceAward != 0 && r.named(mon) {
				named[mon.Code] = uint32(min(math.MaxUint32, uint64(named[mon.Code])+uint64(d.Result.ExperienceAward)))
			}
		}
	}
	names := make([]NamedExperience, 0, len(named))
	for code, amount := range named {
		names = append(names, NamedExperience{code, amount})
	}
	slices.SortFunc(names, func(a, b NamedExperience) int { return int(a.Code) - int(b.Code) })
	return weight, names
}

func (r *Run) named(m opening.Monster) bool {
	return r.Definition.Dungeon.UsesOrdinaryRewards && m.Type < 3 && !m.Dummy && m.AiScriptIndex == nil &&
		(m.Code >= 50000 && m.Code <= 51000 || m.Code >= 56000 && m.Code <= 60000 || slices.Contains(r.Definition.Dungeon.NamedMonsterCodes, m.Code))
}

func (cat *Catalog) cardGold(r *Run, weight uint32, level, rank byte) (*CardReward, error) {
	c := cat.Cards
	if c.Hash == "" {
		return nil, nil
	}
	basis, ok := cat.Drops.Gold[level]
	difficulty := r.Definition.Dungeon.Difficulty
	if !ok || int(difficulty) >= len(c.DifficultyRates) {
		return nil, errors.New("missing frozen clear reward gold or difficulty")
	}
	party, err := c.SoloPartyRate.Float64()
	if err != nil {
		return nil, err
	}
	diff, err := c.DifficultyRates[difficulty].Float64()
	if err != nil {
		return nil, err
	}
	var bonus float64
	if slices.Contains(c.Rank.Grades, int(rank)) && len(c.Rank.Grades) >= 5 && len(c.Rank.Bonuses) == 5 {
		for i := range 5 {
			if int(rank) >= c.Rank.Grades[i] {
				bonus, err = c.Rank.Bonuses[4-i].Float64()
				if err != nil {
					return nil, err
				}
				break
			}
		}
	}
	rate := float32(party) * float32(diff) * (1 + float32(bonus))
	half := uint64(basis.Amount) * 175 / 1000 * uint64(weight) / 2
	group := float32(half) * rate
	if group < 0 || float64(group) > math.MaxUint32 {
		return nil, errors.New("unsupported clear group gold")
	}
	piece := float32(uint32(group)) / 2 * rate
	if piece < 0 || float64(piece) > math.MaxUint32 {
		return nil, errors.New("unsupported clear share gold")
	}
	policy, hash := "LocalInheritedGold/v1", c.Hash
	if difficulty != 0 {
		policy, hash = "LocalGuideGold/v1", c.GuideHash
	}
	variance, err := cardVariance(r, hash, weight, level, basis.Variance)
	if err != nil {
		return nil, err
	}
	amount := uint64(uint32(piece)) * uint64(100+variance) / 100
	if amount > math.MaxUint32 {
		return nil, errors.New("clear gold exceeds wallet width")
	}
	if amount == 0 {
		policy = "PVFFreeZeroGold/v1"
		h := sha256.Sum256([]byte(hash + "\n" + policy))
		hash = strings.ToUpper(hex.EncodeToString(h[:]))
	}
	return &CardReward{Policy: policy, ResourceHash: hash, Gold: uint32(amount)}, nil
}

func cardVariance(r *Run, hash string, weight uint32, level byte, maxVariance int64) (int64, error) {
	if maxVariance == 0 {
		return 0, nil
	}
	id, err := guidWire(r.ID)
	if err != nil || !isHash(hash) || maxVariance < 0 || maxVariance > 99 {
		return 0, errors.New("invalid clear gold random source")
	}
	var input [61]byte
	copy(input[:], id)
	le.PutUint32(input[16:], r.Seed)
	input[20] = level
	le.PutUint32(input[21:], weight)
	_, _ = hex.Decode(input[25:57], []byte(hash))
	bound := uint64(2*maxVariance + 1)
	for i := range uint32(16) {
		le.PutUint32(input[57:], i)
		hash := sha256.Sum256(input[:])
		v := le.Uint64(hash[:])
		if v >= -bound%bound {
			return int64(v%bound) - maxVariance, nil
		}
	}
	return 0, errors.New("clear gold sample exhausted")
}

func BossNotice(actor uint16, complete bool) []byte {
	var done byte
	if complete {
		done = 1
	}
	return le.AppendUint16([]byte{1, done}, actor)
}

func (z ClearResult) PlayNotice(actor uint16) []byte {
	b := le.AppendUint32([]byte{0}, z.ElapsedMilliseconds)
	b = append(b, 0, z.RankPoint, 1, 1)
	b = le.AppendUint32(le.AppendUint16(b, actor), z.ElapsedMilliseconds)
	return append(b, 0)
}

func (z ClearResult) RewardNotice() []byte {
	return z.rewardNotice(z.AbaddonTimes == nil)
}

func NoCardRewardNotice() []byte {
	return (ClearResult{}).rewardNotice(false)
}

func (z ClearResult) rewardNotice(includeFreeCard bool) []byte {
	b := le.AppendUint32(le.AppendUint32(nil, z.ClearExperienceAward), z.RankExperienceAward)
	b = append(b, make([]byte, 8+1+22*4+2+8*4)...)
	b = le.AppendUint32(b, 1)
	for _, v := range z.MonsterExperienceTotals[1:] {
		b = le.AppendUint32(b, v)
	}
	b = le.AppendUint32(b, uint32(len(z.NamedExperience)))
	for _, v := range z.NamedExperience {
		b = le.AppendUint32(le.AppendUint32(b, v.Code), v.Experience)
	}
	var gold uint32
	if z.FreeCardReward != nil {
		gold = z.FreeCardReward.Gold
	}
	items := append(z.FreeCardReward.itemViews(), z.SpecialReward.itemViews()...)
	items = append(items, z.CompletionReward.itemViews()...)
	items = append(items, z.OculusReward.itemViews()...)
	ownCount := len(items)
	goldRow := includeFreeCard && (gold != 0 || len(items) == 0)
	if goldRow {
		ownCount++
	}
	b = append(b, byte(ownCount))
	if goldRow {
		b = le.AppendUint32(le.AppendUint32(b, 0), gold)
	}
	for _, item := range items {
		b = le.AppendUint32(le.AppendUint32(b, item[0]), item[1])
	}
	b = append(b, make([]byte, 7)...)
	var cost uint32
	if z.PaidCardReward != nil {
		cost = z.PaidCardReward.Cost
	}
	b = le.AppendUint32(b, cost)
	b = append(b, make([]byte, 16+8)...)
	b = append(b, 1, 0)
	var total uint64
	for _, v := range z.MonsterExperienceTotals {
		total += uint64(v)
	}
	b = le.AppendUint32(b, uint32(min(total, math.MaxUint32)))
	return append(b, make([]byte, 12+8+40)...)
}

func CardLayout(fatigue uint16) []byte {
	return append(le.AppendUint16([]byte{1}, fatigue), bytes.Repeat([]byte{255}, 14)...)
}

func CardOwners(accepted, free, paid bool) []byte {
	w := []byte{1}
	if !accepted {
		w = []byte{0, 0, 0}
	}
	for i := range 8 {
		var selected byte = 255
		if i == 0 && free {
			selected = 0
		}
		p := byte(255)
		if i == 0 && accepted && paid {
			p = 0
		}
		w = append(w, selected, p, 0, 0)
	}
	return w
}

func (r *CardReward) PaidNotice(free bool) []byte {
	items := r.itemViews()
	if len(items) == 0 {
		return CardOwners(true, free, true)
	}
	b := []byte{1, 255, 0, byte(len(items) + 1)}
	if free {
		b[1] = 0
	}
	var gold uint32
	if r != nil {
		gold = r.Gold
	}
	b = le.AppendUint32(le.AppendUint32(b, 0), gold)
	for _, row := range items {
		b = le.AppendUint32(le.AppendUint32(b, row[0]), row[1])
	}
	b = append(b, 0)
	for i := 1; i < 8; i++ {
		b = append(b, 255, 255, 0, 0)
	}
	return b
}
