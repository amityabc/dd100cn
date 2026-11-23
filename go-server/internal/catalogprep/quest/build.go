// Package quest prepares the native quest book from matching Client100 PVF data.
package quest

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/pvf"
	"s9a1/server/internal/questwire"
)

const budget = 1 << 20

type Result struct {
	Data                  questwire.Data
	AcceptanceTemplateIDs []uint32
	MaterialTemplateIDs   []uint32
	RewardTemplateIDs     []uint32
	OrdinaryIssues        map[uint16]string
	Ranges                []Range
}

type ChildRangeBinding struct {
	Parent        uint16
	ParentHash    string
	PreviousChild uint16
}

type Range struct {
	Id                uint16
	Hash              string
	Subtype           int
	Data              []int
	NpcRangeTarget    *questwire.NpcTarget
	ChildRangeBinding *ChildRangeBinding
}

type questSource struct {
	archive  *pvf.Archive
	index    map[string]map[int64]string
	paths    map[int64]string
	texts    map[string]string
	hashes   map[string]string
	bindings map[uint16]*ChildRangeBinding
}

func Build(a *pvf.Archive) (Result, error) {
	var result Result
	questPaths, err := a.ReadIndex("quest")
	if err != nil {
		return result, err
	}
	source := questSource{archive: a, paths: questPaths, index: make(map[string]map[int64]string), texts: make(map[string]string), hashes: make(map[string]string), bindings: make(map[uint16]*ChildRangeBinding)}
	policyPath := "etc/chn_questclearcoupon.etc"
	if data, err := source.text(policyPath); err == nil {
		if err := validateTicketPolicy(data); err != nil {
			return result, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	param, err := source.text("n_quest/questparameter.etc")
	if err != nil {
		return result, err
	}
	experience, err := parseExperienceTable(param)
	if err != nil {
		return result, err
	}
	result.OrdinaryIssues = make(map[uint16]string)
	for _, id := range slices.Sorted(maps.Keys(questPaths)) {
		if id <= 0 || id >= 30000 {
			continue
		}
		text, err := source.text(questPaths[id])
		if err != nil {
			return result, err
		}
		qid, raw := uint16(id), hash(text)
		ticket, err := parseTicket(qid, text, raw)
		if err != nil {
			ticket = questwire.Ticket{Criterion: disabledCriterion(qid), Hash: raw}
		}
		if past := parsePast(qid, text, raw); past != nil {
			freezeCriterion(&past.Criterion)
			result.Data.Past = append(result.Data.Past, *past)
		}
		ordinary, err := parseOrdinary(qid, text, raw)
		if err == nil {
			err = source.prepareObjective(&ordinary)
		}
		if err == nil {
			ordinary.ExperienceByLevel, err = experience.rewards(text, ordinary.FateIntroduction)
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && !anyOf(qid, 3162, 3868, 3869, 12122, 3759, 3832) && npcRangeObjectives[qid] == nil {
				return result, err
			}
			result.OrdinaryIssues[qid] = err.Error()
			ordinary = questwire.Quest{
				Criterion: disabledCriterion(qid), Hash: raw, Kind: unsupported, Subtype: -1,
				Operations: &questwire.OperationRules{CanAbandon: true, AcceptItems: []questwire.Material{}},
			}
		}
		freezeOrdinary(&ordinary)
		result.Data.Ordinary = append(result.Data.Ordinary, ordinary)
		result.Data.Tickets = append(result.Data.Tickets, ticket)
	}
	byID := make(map[uint16]*questwire.Quest, len(result.Data.Ordinary))
	for i := range result.Data.Ordinary {
		q := &result.Data.Ordinary[i]
		byID[q.Id] = q
	}
	for i := range result.Data.Ordinary {
		q := &result.Data.Ordinary[i]
		deriveFlags(q, byID)
		if townRange(q) {
			result.Ranges = append(result.Ranges, Range{q.Id, q.Hash, q.Subtype, q.Data, q.NpcRangeTarget, source.bindings[q.Id]})
		}
	}
	if err := source.ticketTitles(result.Data.Tickets); err != nil {
		return result, err
	}
	for i := range result.Data.Tickets {
		freezeTicket(&result.Data.Tickets[i])
	}
	result.AcceptanceTemplateIDs, result.MaterialTemplateIDs, result.RewardTemplateIDs = templateIDs(result.Data)
	return result, nil
}

func emptySlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func freezeCriterion(q *questwire.Criterion) {
	q.Required = emptySlice(q.Required)
	q.Answers = emptySlice(q.Answers)
	q.Collisions = emptySlice(q.Collisions)
	q.Children = emptySlice(q.Children)
	q.Targets = emptySlice(q.Targets)
	q.NonTargets = emptySlice(q.NonTargets)
}

func freezeOrdinary(q *questwire.Quest) {
	freezeCriterion(&q.Criterion)
	q.Data = emptySlice(q.Data)
	q.Materials = emptySlice(q.Materials)
	q.Rewards = emptySlice(q.Rewards)
	q.RewardRows = emptySlice(q.RewardRows)
	q.SelectionRewardRows = emptySlice(q.SelectionRewardRows)
	q.DestinationDungeons = emptySlice(q.DestinationDungeons)
	q.DestinationRequirements = emptySlice(q.DestinationRequirements)
	q.DropRules = emptySlice(q.DropRules)
	q.CascadeClear = emptySlice(q.CascadeClear)
	q.SkillEffects = emptySlice(q.SkillEffects)
}

func freezeTicket(q *questwire.Ticket) {
	freezeCriterion(&q.Criterion)
	q.ProhibitedFlags = emptySlice(q.ProhibitedFlags)
	q.CascadeClear = emptySlice(q.CascadeClear)
	q.Rewards = emptySlice(q.Rewards)
	q.RewardRows = emptySlice(q.RewardRows)
	q.TitleRewards = emptySlice(q.TitleRewards)
	q.SkillEffects = emptySlice(q.SkillEffects)
	q.ConditionQuestHide = emptySlice(q.ConditionQuestHide)
}

func townRange(q *questwire.Quest) bool {
	if !q.Supported || q.Kind != reachRange {
		return false
	}
	if q.Subtype == 0 {
		return q.NpcRangeTarget != nil
	}
	const maxPoint = 32767
	return q.Subtype == 1 && len(q.Data) == 6 && q.Data[0] > 0 && q.Data[0] <= 255 && q.Data[1] >= 0 && q.Data[1] <= 255 &&
		q.Data[2] >= 0 && q.Data[2] <= maxPoint && q.Data[3] >= 0 && q.Data[3] <= maxPoint && q.Data[4] > 0 && q.Data[5] > 0
}

func disabledCriterion(id uint16) questwire.Criterion {
	return questwire.Criterion{Id: id, MinimumLevel: 1, MaximumLevel: 255, Job: -1, Grow: -1, GrowNumber: -1}
}

func (s *questSource) text(path string) (string, error) {
	key := strings.ToLower(pvf.NormalizePath(path))
	if text, found := s.texts[key]; found {
		return text, nil
	}
	text, err := s.archive.ReadText(path, budget)
	if err == nil {
		s.texts[key] = text
	}
	return text, err
}

func (s *questSource) rawHash(path string) (string, error) {
	key := strings.ToLower(pvf.NormalizePath(path))
	if sum := s.hashes[key]; sum != "" {
		return sum, nil
	}
	raw, err := s.archive.Read(path, budget)
	if err != nil {
		return "", err
	}
	sum := hash(string(raw))
	s.hashes[key] = sum
	return sum, nil
}

func (s *questSource) indexed(category string, id int64) (string, error) {
	rows := s.index[category]
	if rows == nil {
		var err error
		rows, err = s.archive.ReadIndex(category)
		if err != nil {
			return "", err
		}
		s.index[category] = rows
	}
	path, ok := rows[id]
	if !ok {
		return "", fmt.Errorf("quest resource %s/%d missing from index", category, id)
	}
	return path, nil
}

var titleEquipment = regexp.MustCompile("(?i)^\x60\\[title name\\]\x60(?:\\s+\\d+)?$")

func (s *questSource) ticketTitles(tickets []questwire.Ticket) error {
	items := make(map[uint32]bool)
	for _, q := range tickets {
		if q.Supported {
			for _, row := range q.RewardRows {
				items[row.Item.Template] = false
			}
		}
	}
	if len(items) == 0 {
		return nil
	}
	equipment, err := s.archive.ReadIndex("equipment")
	if err != nil {
		return err
	}
	for id := range items {
		path := equipment[int64(id)]
		if path == "" {
			continue
		}
		text, err := s.text(path)
		if err != nil {
			return err
		}
		fields, err := giftFields(text)
		if err != nil {
			return err
		}
		var declaration *giftField
		for i := range fields {
			if fields[i].name == "equipment type" {
				if declaration != nil {
					return fmt.Errorf("ambiguous ticket reward equipment type for item %d", id)
				}
				declaration = &fields[i]
			}
		}
		items[id] = declaration != nil && titleEquipment.MatchString(declaration.value)
	}
	for i := range tickets {
		q := &tickets[i]
		for _, row := range q.RewardRows {
			if items[row.Item.Template] && !slices.Contains(q.TitleRewards, row.Item.Template) {
				q.TitleRewards = append(q.TitleRewards, row.Item.Template)
			}
		}
	}
	return nil
}

func templateIDs(data questwire.Data) ([]uint32, []uint32, []uint32) {
	acceptance, materials, rewards := make(map[uint32]bool), make(map[uint32]bool), make(map[uint32]bool)
	for _, q := range data.Ordinary {
		if !q.Supported {
			continue
		}
		for _, m := range q.Operations.AcceptItems {
			acceptance[m.Template] = true
		}
		for _, m := range q.Materials {
			materials[m.Template] = true
		}
		for _, r := range q.Rewards {
			rewards[r.Template] = true
		}
		for _, r := range q.RewardRows {
			rewards[r.Item.Template] = true
		}
		for _, r := range q.SelectionRewardRows {
			rewards[r.Item.Template] = true
		}
		for _, r := range q.DropRules {
			rewards[r.Template] = true
		}
		if q.Id == 2674 && q.Kind == duelTraining {
			rewards[trainingManual] = true
		}
	}
	for _, q := range data.Tickets {
		if q.Supported {
			for _, id := range q.TitleRewards {
				rewards[id] = true
			}
			if q.LegacyUpperAwakening {
				for _, item := range q.Rewards {
					rewards[item.Template] = true
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(acceptance)), slices.Sorted(maps.Keys(materials)), slices.Sorted(maps.Keys(rewards))
}

func validateTicketPolicy(text string) error {
	fields, err := giftFields(text)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, f := range fields {
		if !anyOf(f.name, "available quest grade", "except quest index", "coupon item index", "level section reward") {
			continue
		}
		if seen[f.name] {
			return fmt.Errorf("duplicate quest coupon policy field %q", f.name)
		}
		seen[f.name] = true
		for _, value := range strings.Fields(f.value) {
			n, err := strconv.ParseUint(value, 10, 32)
			if err != nil || f.name == "except quest index" && (n == 0 || n >= 30000) ||
				f.name == "coupon item index" && n == 0 || anyOf(f.name, "available quest grade", "level section reward") && n > 2147483647 {
				return fmt.Errorf("invalid native quest coupon policy field %q", f.name)
			}
		}
	}
	return nil
}
