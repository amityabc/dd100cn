package quest

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/pvf"
	"s9a1/server/internal/questwire"
)

var (
	passLevel = regexp.MustCompile("(?s)\\[level reward\\]\\s*(.*?)\\[/level reward\\]")
	passBlock = regexp.MustCompile("(?s)\\[mission\\]\\s*(.*?)\\[/mission\\]")
	passTuple = regexp.MustCompile("^\\s*(\\d+)\\s+(\\d+)\\s+(\\d+)\\s+(\\d+)\\s+\x60([^\x60]*)\x60\\s+(-?\\d+)\\s*$")
)

type ItemLookup interface {
	Source(uint32) (gm.ItemSource, error)
}

func (s *script) unsigned(text string) []uint32 {
	fields := strings.Fields(text)
	out := make([]uint32, len(fields))
	for i, f := range fields {
		n, err := strconv.ParseFloat(f, 64)
		if err != nil || n < 0 || n > math.MaxUint32 || n != math.Trunc(n) {
			s.fail("non-integral or invalid resource value")
			return nil
		}
		out[i] = uint32(n)
	}
	return out
}

func BuildBattlePass(a *pvf.Archive, items ItemLookup) (*questwire.BattlePassData, error) {
	const path = "etc/chn_battlepass.etc"
	text, err := a.ReadText(path, budget)
	if err != nil {
		return nil, err
	}
	raw, err := a.Read(path, budget)
	if err != nil {
		return nil, err
	}
	out := &questwire.BattlePassData{Templates: make(map[uint32]json.RawMessage)}
	def := &out.Definition
	def.ResourceHash = hash(string(raw))
	s := script{text: text}
	scalar := func(key string) uint32 {
		parts := s.unsigned(s.section(key))
		if len(parts) != 1 || parts[0] == 0 {
			s.fail("invalid battle pass field " + key)
			return 0
		}
		return parts[0]
	}
	def.Certificate, def.Package, def.BuyCode = scalar("certificate index"), scalar("package index"), scalar("buy restrict code")
	def.Rewards = make(map[int][]questwire.PassReward, 100)
	templateIDs := make(map[uint32]bool)
	for _, match := range passLevel.FindAllStringSubmatch(text, -1) {
		args := s.unsigned(match[1])
		if len(args) < 4 || (len(args)-1)%3 != 0 || args[0] < 1 || args[0] > 100 || def.Rewards[int(args[0])] != nil {
			return nil, fmt.Errorf("invalid battle pass reward declaration")
		}
		rewards := make([]questwire.PassReward, 0, len(args)/3)
		for row := range slices.Chunk(args[1:], 3) {
			if row[0] == 0 || row[1] == 0 || row[2] > 1 {
				return nil, fmt.Errorf("invalid battle pass reward grant")
			}
			rewards = append(rewards, questwire.PassReward{Template: row[0], Count: row[1], Tier: byte(row[2])})
			templateIDs[row[0]] = true
		}
		def.Rewards[int(args[0])] = rewards
	}
	exp := s.unsigned(s.section("exp table"))
	if len(exp) != 200 || exp[199] != 0 || len(def.Rewards) != 100 {
		return nil, fmt.Errorf("incomplete battle pass experience table")
	}
	for i := range 100 {
		if exp[i*2] != uint32(i+1) || i < 99 && (exp[i*2+1] == 0 || exp[i*2+1] > math.MaxInt32) {
			return nil, fmt.Errorf("invalid battle pass level progression")
		}
		def.Experience = append(def.Experience, int(exp[i*2+1]))
	}
	bonus := s.unsigned(s.section("max level bonus"))
	if len(bonus) != 5 || bonus[0] > math.MaxInt32 || slices.Contains(bonus, 0) {
		return nil, fmt.Errorf("incomplete maximum battle pass reward")
	}
	def.MaximumBonus = &struct {
		Experience                           int
		Template, Count, MailTitle, MailBody uint32
	}{int(bonus[0]), bonus[1], bonus[2], bonus[3], bonus[4]}
	templateIDs[bonus[1]] = true
	def.Missions = make(map[uint16]questwire.PassMission)
	for _, match := range passBlock.FindAllStringSubmatch(text, -1) {
		tuple := passTuple.FindStringSubmatch(match[1])
		if len(tuple) != 7 {
			return nil, fmt.Errorf("invalid battle pass mission tuple")
		}
		v := make([]int, 5)
		for i, field := range []int{1, 2, 3, 4, 6} {
			n, err := strconv.ParseInt(tuple[field], 10, 32)
			if err != nil {
				return nil, err
			}
			v[i] = int(n)
		}
		id := uint16(v[0])
		if id == 0 || v[0] > math.MaxUint16 || v[1] <= 0 || v[2] <= 0 || v[4] < -1 || strings.TrimSpace(tuple[5]) == "" || def.Missions[id].Id != 0 {
			return nil, fmt.Errorf("invalid battle pass mission row")
		}
		def.Missions[id] = questwire.PassMission{Id: id, Target: v[1], Experience: v[2], EventType: v[3], Parameter: v[4], Title: tuple[5]}
	}
	def.GoldenMissionExpRatio = int(scalar("golden mission exp ratio"))
	def.WeeklyMissionRewardLimit = int(scalar("weekly mission reward limit"))
	if def.GoldenMissionExpRatio > math.MaxInt32 || def.WeeklyMissionRewardLimit > 6 {
		return nil, fmt.Errorf("invalid battle pass mission policy")
	}
	ids := func(key string) []uint16 {
		values := s.unsigned(s.section(key))
		out := make([]uint16, len(values))
		for i, value := range values {
			if value == 0 || value > math.MaxUint16 {
				s.fail("invalid battle pass mission ID")
			}
			out[i] = uint16(value)
		}
		return out
	}
	groups := func(key string) [][]uint16 {
		values := ids(key)
		if len(values) == 0 || len(values)%6 != 0 {
			s.fail("incomplete battle pass mission group")
			return nil
		}
		return slices.Collect(slices.Chunk(values, 6))
	}
	def.DailyMissionRows, def.WeeklyMissionRows = groups("daily mission"), groups("weekly mission")
	def.SeasonMissionIds = ids("season mission")
	if s.err != nil {
		return nil, s.err
	}
	for _, id := range slices.Sorted(maps.Keys(templateIDs)) {
		src, err := items.Source(id)
		if err != nil || src.Equipment {
			continue
		}
		template, err := src.GiftFor(gm.GiftBattlePass)
		if err == nil {
			out.Templates[id], err = json.Marshal(template)
		}
		if err != nil {
			continue
		}
	}
	return out, nil
}
