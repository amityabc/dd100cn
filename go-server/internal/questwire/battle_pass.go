package questwire

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"s9a1/server/internal/storage"
	"slices"
	"strings"
	"time"
)

type BattlePassData struct {
	Definition passDefinition
	Templates  map[uint32]json.RawMessage
}

type passDefinition struct {
	ResourceHash                  string
	Certificate, Package, BuyCode uint32
	Rewards                       map[int][]passReward
	Experience                    []int
	Schedule                      struct{ StartsAt, EndsAt int64 }
	MaximumBonus                  *struct {
		Experience                           int
		Template, Count, MailTitle, MailBody uint32
	}
	Missions                                        map[uint16]passMission
	GoldenMissionExpRatio, WeeklyMissionRewardLimit int
	DailyMissionRows, WeeklyMissionRows             [][]uint16
	SeasonMissionIds                                []uint16
}

type passReward struct {
	Template, Count uint32
	Tier            byte
}
type passMission struct {
	Id                                       uint16
	Target, Experience, EventType, Parameter int
	Title                                    string
}
type passProgress struct {
	Id       uint16
	Progress int
	Claimed  bool
}
type passMissions struct {
	DailyPeriod, WeeklyPeriod                                        int64
	Daily, Weekly, Season                                            []passProgress
	LastLoginDay                                                     int64
	DailyClaims, WeeklyClaims, SeasonDailyClaims, SeasonWeeklyClaims int
}
type passState struct {
	Season              string
	Certificate, Deluxe bool
	Level, Experience   int
	Claims              string
	MissionState        passMissions
}

func (s passState) advanced() bool { return s.Certificate || s.Deluxe }

var (
	ErrPassClosed        = errors.New("Battle Pass season is outside its configured opening period")
	ErrPassUnavailable   = errors.New("Battle Pass requires active resources and a ready town")
	ErrPassSeason        = errors.New("Battle Pass resource season changed; explicit season migration required")
	ErrPassQualification = errors.New("Battle Pass reward qualification is not met")
	ErrPassClaimed       = errors.New("Battle Pass reward already claimed or absent")
	ErrPassMax           = errors.New("Battle Pass maximum-level experience is insufficient")
)

func (d *BattlePassData) Configure(start, end *time.Time) error {
	if start == nil && end == nil {
		return nil
	}
	if d == nil || start == nil || end == nil || start.Unix() <= 0 || end.Unix() <= start.Unix() || end.Unix() > math.MaxInt32 {
		return ErrPassUnavailable
	}
	d.Definition.Schedule.StartsAt, d.Definition.Schedule.EndsAt = start.Unix(), end.Unix()
	return nil
}

func (d *BattlePassData) Active(t time.Time) bool {
	return d != nil && d.Definition.Schedule.StartsAt > 0 && t.Unix() >= d.Definition.Schedule.StartsAt && t.Unix() < d.Definition.Schedule.EndsAt
}

func (d *BattlePassData) BuyCash(raw json.RawMessage, offer storage.CashOffer, now time.Time) (json.RawMessage, error) {
	if !d.Active(now) {
		return nil, ErrPassClosed
	}
	template := d.Definition.Certificate
	if offer.BattlePassDeluxe {
		template = d.Definition.Package
	}
	if template == 0 || offer.TemplateID != template || offer.BattlePassHash != d.Definition.ResourceHash ||
		offer.Count != 1 || offer.Cera == 0 || offer.Gold != 0 || offer.PremiumType != 0 || offer.SkillStyleHash != "" ||
		offer.CharacterSlot || offer.NameTag != nil || offer.RevivalCoins || offer.NativeFreePointOnly ||
		len(offer.PetTemplate) > 0 && string(offer.PetTemplate) != "null" ||
		len(offer.BonusItems) != 0 || len(offer.RequiredCouponTemplates) != 0 || len(offer.OwnedItemExclusions) != 0 ||
		offer.AccountRestriction != 0 || offer.CharacterRestriction != 0 || offer.TargetBagExpansion != 0 ||
		offer.TargetPersonalCargoCapacity != 0 || offer.TargetAccountCargoCapacity != 0 || offer.TargetAvatarExpansion != 0 {
		return nil, storage.ErrInvalid
	}
	state, err := d.bind(raw)
	if err != nil {
		return nil, err
	}
	if offer.BattlePassDeluxe {
		if state.Deluxe || state.Level >= 50 {
			return nil, ErrPassQualification
		}
		state.Deluxe, state.Level, state.Experience = true, 50, 0
	} else {
		if state.advanced() {
			return nil, ErrPassQualification
		}
		state.Certificate = true
	}
	return json.Marshal(state)
}

func readPass(raw json.RawMessage) (passState, error) {
	s := passState{Level: 1, Claims: strings.Repeat("0", 56), MissionState: passMissions{Daily: []passProgress{}, Weekly: []passProgress{}, Season: []passProgress{}}}
	if len(raw) != 0 && json.Unmarshal(raw, &s) != nil {
		return s, storage.ErrInvalid
	}
	if s.Level < 1 || s.Level > 100 || s.Experience < 0 || s.Experience > math.MaxInt32 || len(s.Claims) != 56 || s.Season != "" && !hash(s.Season) {
		return s, storage.ErrInvalid
	}
	if _, err := hex.DecodeString(s.Claims); err != nil {
		return s, storage.ErrInvalid
	}
	if s.Season == "" && (s.advanced() || s.Level != 1 || s.Experience != 0 || s.Claims != strings.Repeat("0", 56) || s.MissionState.DailyPeriod != 0) {
		return s, storage.ErrInvalid
	}
	return s, nil
}

func (d *BattlePassData) bind(raw json.RawMessage) (passState, error) {
	s, err := readPass(raw)
	if err != nil {
		return s, err
	}
	if d == nil || !hash(d.Definition.ResourceHash) || len(d.Definition.Experience) != 100 {
		return s, ErrPassUnavailable
	}
	if s.Season != "" && s.Season != d.Definition.ResourceHash {
		return s, ErrPassSeason
	}
	s.Season = d.Definition.ResourceHash
	return s, d.validateMissions(s.MissionState)
}

func passPeriods(t time.Time) (int64, int64) {
	local := t.In(time.FixedZone("UTC+8", 8*3600)).Add(-6 * time.Hour)
	d := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, local.Location())
	w := d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
	return d.Unix(), w.Unix()
}

func (d *BattlePassData) validateMissions(p passMissions) error {
	empty := p.DailyPeriod == 0 && p.WeeklyPeriod == 0
	if p.Daily == nil || p.Weekly == nil || p.Season == nil || p.DailyPeriod < 0 || p.DailyPeriod > math.MaxInt32 || p.WeeklyPeriod < 0 || p.WeeklyPeriod > math.MaxInt32 ||
		p.LastLoginDay < 0 || p.LastLoginDay > p.DailyPeriod || p.DailyClaims < 0 || p.WeeklyClaims < 0 || p.SeasonDailyClaims < p.DailyClaims || p.SeasonWeeklyClaims < p.WeeklyClaims {
		return storage.ErrInvalid
	}
	if empty {
		if len(p.Daily)+len(p.Weekly)+len(p.Season)+p.DailyClaims+p.WeeklyClaims+p.SeasonDailyClaims+p.SeasonWeeklyClaims != 0 {
			return storage.ErrInvalid
		}
		return nil
	}
	day, week := passPeriods(time.Unix(p.DailyPeriod, 0))
	if day != p.DailyPeriod || week != p.WeeklyPeriod || len(p.Daily) != 6 || len(p.Weekly) != 6 || len(p.Season) != 30 || p.WeeklyClaims > d.Definition.WeeklyMissionRewardLimit {
		return storage.ErrInvalid
	}
	for n, rows := range [][]passProgress{p.Daily, p.Weekly, p.Season} {
		ids := make([]uint16, 0, len(rows))
		claimed := 0
		for _, r := range rows {
			m, ok := d.Definition.Missions[r.Id]
			if !ok || slices.Contains(ids, r.Id) || r.Progress < 0 || r.Progress > m.Target || r.Claimed && r.Progress != m.Target {
				return storage.ErrInvalid
			}
			ids = append(ids, r.Id)
			if r.Claimed {
				claimed++
			}
		}
		match := func(cases [][]uint16) bool {
			return slices.ContainsFunc(cases, func(row []uint16) bool { return slices.Equal(row, ids) })
		}
		if n == 0 && (claimed != p.DailyClaims || !match(d.Definition.DailyMissionRows)) || n == 1 && (claimed != p.WeeklyClaims || !match(d.Definition.WeeklyMissionRows)) || n == 2 && !slices.Equal(ids, d.Definition.SeasonMissionIds) {
			return storage.ErrInvalid
		}
	}
	return nil
}

func (d *BattlePassData) selectMissions(candidates [][]uint16, expected int) ([]passProgress, error) {
	valid := make([][]uint16, 0, len(candidates))
	for _, row := range candidates {
		if len(row) != expected {
			continue
		}
		seen := map[uint16]bool{}
		for _, id := range row {
			m, ok := d.Definition.Missions[id]
			if seen[id] || !ok || id == 0 || m.Id != id || m.Target <= 0 || m.Experience <= 0 || m.EventType < 0 || m.Parameter < -1 || strings.TrimSpace(m.Title) == "" {
				break
			}
			seen[id] = true
		}
		if len(seen) == expected {
			valid = append(valid, row)
		}
	}
	if len(valid) == 0 {
		return nil, storage.ErrInvalid
	}
	row := valid[rand.IntN(len(valid))]
	result := make([]passProgress, len(row))
	for i, id := range row {
		result[i].Id = id
	}
	return result, nil
}

func (d *BattlePassData) refresh(raw json.RawMessage, now time.Time) (passState, error) {
	s, err := d.bind(raw)
	if err != nil {
		return s, err
	}
	if !d.Active(now) {
		return s, ErrPassClosed
	}
	if d.Definition.GoldenMissionExpRatio < 1 || d.Definition.WeeklyMissionRewardLimit < 1 || d.Definition.WeeklyMissionRewardLimit > 6 {
		return s, storage.ErrInvalid
	}
	day, week := passPeriods(now)
	p := s.MissionState
	if day < p.DailyPeriod || week < p.WeeklyPeriod || day > math.MaxInt32 {
		return s, storage.ErrInvalid
	}
	if day != p.DailyPeriod {
		p.Daily, err = d.selectMissions(d.Definition.DailyMissionRows, 6)
		if err != nil {
			return s, err
		}
		p.DailyClaims = 0
	}
	if week != p.WeeklyPeriod {
		p.Weekly, err = d.selectMissions(d.Definition.WeeklyMissionRows, 6)
		if err != nil {
			return s, err
		}
		p.WeeklyClaims = 0
	}
	if len(p.Season) == 0 {
		p.Season, err = d.selectMissions([][]uint16{d.Definition.SeasonMissionIds}, 30)
		if err != nil {
			return s, err
		}
	}
	p.DailyPeriod, p.WeeklyPeriod = day, week
	s.MissionState = p
	return s, nil
}

func (d *BattlePassData) event(s *passState, event storage.BattlePassEvent) {
	p := &s.MissionState
	if event.Type == 30 {
		if event.Count != 1 || event.Parameter != -1 || p.LastLoginDay == p.DailyPeriod {
			return
		}
		p.LastLoginDay = p.DailyPeriod
	}
	for _, rows := range [][]passProgress{p.Daily, p.Weekly, p.Season} {
		for i := range rows {
			r := &rows[i]
			m := d.Definition.Missions[r.Id]
			if !r.Claimed && m.EventType == event.Type && m.Parameter == event.Parameter {
				r.Progress = int(min(int64(m.Target), int64(r.Progress)+int64(event.Count)))
			}
		}
	}
}

func (d *BattlePassData) Update(raw json.RawMessage, now time.Time, events []storage.BattlePassEvent) (json.RawMessage, error) {
	if !d.Active(now) {
		return nil, nil
	}
	s, err := d.refresh(raw, now)
	if err != nil {
		return nil, err
	}
	old, err := readPass(raw)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		if e.Count <= 0 || e.Parameter < -1 || e.Type < 0 || e.Type == 36 {
			return nil, storage.ErrInvalid
		}
		d.event(&s, e)
	}
	if reflect.DeepEqual(s, old) {
		return nil, nil
	}
	return json.Marshal(s)
}
