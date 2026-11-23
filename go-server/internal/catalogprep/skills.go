package catalogprep

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/opening"
)

type LearnedSkill = opening.LearnedSkill
type AdvancementSkill = opening.AdvancementSkill
type PreparedSkills struct {
	SkillDefinitions []SkillRow
	InitialSkills    map[byte][]LearnedSkill
	Grants           map[byte]map[byte][]AdvancementSkill
}
type SkillRow struct {
	Job        byte
	Definition SkillDefinition
}

var initialSkillsHeader = regexp.MustCompile(`(?im)^[ \t]*\[initial value\][ \t]*\r?$`)
var branchSkillsHeader = regexp.MustCompile(`(?im)^[ \t]*\[growtype\s+(\d+)\][ \t]*\r?$`)
var awakeningSkillsHeader = regexp.MustCompile(`(?im)^[ \t]*\[awakening\s+(\d+)\][ \t]*\r?$`)
var quotedValue = regexp.MustCompile("`([^`]*)`")

func Skills(src Source) (PreparedSkills, error) {
	z := PreparedSkills{SkillDefinitions: []SkillRow{}, InitialSkills: map[byte][]LearnedSkill{}, Grants: map[byte]map[byte][]AdvancementSkill{}}
	text, err := src.ReadText("skill/skilllist.lst", scriptLimit)
	if err != nil {
		return z, err
	}
	jobLists, err := pvftext.List(text, "skill")
	if err != nil {
		return z, err
	}
	characters, err := src.ReadIndex("character", scriptLimit)
	if err != nil {
		return z, err
	}
	for job := byte(0); job < 16; job++ {
		path, ok := characters[int64(job)]
		if !ok {
			continue
		}
		character, err := src.ReadText(path, scriptLimit)
		if err != nil {
			return z, err
		}
		initial, grants, named, err := skillGrants(character)
		if err != nil {
			return z, fmt.Errorf("%s: %w", path, err)
		}
		list, err := src.ReadText(jobLists[int(job)], scriptLimit)
		if err != nil {
			return z, err
		}
		paths, err := pvftext.List(list, "skill")
		if err != nil {
			return z, err
		}
		characterGrant := map[uint16]bool{}
		for _, rows := range grants {
			for _, row := range rows {
				characterGrant[row.ID] = true
			}
		}
		definitions := map[uint16]SkillDefinition{}
		for _, code := range slices.Sorted(maps.Keys(paths)) {
			if code <= 0 || code > 65535 {
				continue
			}
			id := uint16(code)
			p := paths[code]
			data, err := src.ReadText(p, scriptLimit)
			if err != nil {
				return z, fmt.Errorf("%s: %w", p, err)
			}
			raw, err := src.Read(p, scriptLimit)
			if err != nil {
				return z, err
			}
			d, err := parseSkill(id, data, fmt.Sprintf("%X", sha256.Sum256(raw)), initial[id], characterGrant[id])
			if err != nil {
				continue
			}
			definitions[id] = d
			z.SkillDefinitions = append(z.SkillDefinitions, SkillRow{job, d})
		}
		shortcuts := [...]uint16{0, 1, 2, 6, 7, 8, 9, 10, 11, 3, 4, 5}
		active := 0
		unbound := uint16(102)
		z.InitialSkills[job] = []LearnedSkill{}
		for _, id := range slices.Sorted(maps.Keys(initial)) {
			d, ok := definitions[id]
			if !ok {
				return z, fmt.Errorf("job %d initial skill %d unavailable", job, id)
			}
			slot := unbound
			if d.Active && active < len(shortcuts) {
				slot = shortcuts[active]
				active++
			} else {
				unbound++
			}
			z.InitialSkills[job] = append(z.InitialSkills[job], LearnedSkill{Id: id, Level: initial[id], Slot: slot})
		}
		z.Grants[job] = map[byte][]AdvancementSkill{}
		for branch, rows := range grants {
			if branch != 0 && !named[branch] {
				continue
			}
			ready := true
			for i, row := range rows {
				def, ok := definitions[row.ID]
				if !ok {
					ready = false
					break
				}
				rows[i].Active = def.Active
			}
			if ready {
				z.Grants[job][branch] = rows
			}
		}
	}
	return z, nil
}

func skillGrants(text string) (map[uint16]byte, map[byte][]AdvancementSkill, map[byte]bool, error) {
	begin := initialSkillsHeader.FindStringIndex(text)
	branches := branchSkillsHeader.FindAllStringSubmatchIndex(text, -1)
	fail := func(msg string) (map[uint16]byte, map[byte][]AdvancementSkill, map[byte]bool, error) {
		return nil, nil, nil, fmt.Errorf("%s", msg)
	}
	if begin == nil || len(branches) == 0 || begin[1] > branches[0][0] {
		return fail("missing initial skill block")
	}
	values, err := pvftext.Numbers(text[begin[1]:branches[0][0]], "skill")
	if err != nil || len(values)%3 != 0 {
		return fail("invalid initial skill triples")
	}
	initial := map[uint16]byte{}
	for i := 0; i < len(values); i += 3 {
		n, rank := values[i], values[i+1]
		if n <= 0 || n > 65535 || rank <= 0 || rank > 255 || values[i+2] != 1 {
			return fail("invalid initial skill grant")
		}
		id := uint16(n)
		if old := initial[id]; old != 0 && old != byte(rank) {
			return fail("conflicting initial rank")
		}
		initial[id] = byte(rank)
	}
	byBranch := map[byte][]AdvancementSkill{}
	for i, h := range branches {
		n, _ := strconv.Atoi(text[h[2]:h[3]])
		if n < 1 || n > 6 {
			return fail("unsupported character branch")
		}
		branch := byte(n - 1)
		if _, ok := byBranch[branch]; ok {
			return fail("repeated character branch")
		}
		end := len(text)
		if i+1 < len(branches) {
			end = branches[i+1][0]
		}
		block := text[h[1]:end]
		stages := awakeningSkillsHeader.FindAllStringSubmatchIndex(block, -1)
		ordinary := block
		if len(stages) > 0 {
			ordinary = block[:stages[0][0]]
		}
		values, err := pvftext.Numbers(ordinary, "skill")
		if err != nil || len(values)%3 != 0 {
			return fail("malformed branch skill triples")
		}
		rows := []AdvancementSkill{}
		seen := map[[2]int]bool{}
		add := func(id, rank, lv, aw int) bool {
			if id <= 0 || id > 65535 || rank < 0 || rank > 255 || lv <= 0 || lv > 255 {
				return false
			}
			if rank == 0 {
				return true
			}
			k := [2]int{id, aw}
			if seen[k] {
				return false
			}
			seen[k] = true
			rows = append(rows, AdvancementSkill{ID: uint16(id), Level: byte(rank), RequiredLevel: byte(lv), Awakening: byte(aw)})
			return true
		}
		for j := 0; j < len(values); j += 3 {
			if !add(values[j], values[j+1], values[j+2], 0) {
				return fail("invalid branch skill grant")
			}
		}
		seenStage := map[int]bool{}
		for j, s := range stages {
			stage, _ := strconv.Atoi(block[s[2]:s[3]])
			if stage < 1 || stage > 3 || seenStage[stage] {
				return fail("invalid awakening stage")
			}
			seenStage[stage] = true
			end := len(block)
			if j+1 < len(stages) {
				end = stages[j+1][0]
			}
			pairs, err := pvftext.Numbers(block[s[1]:end], "awakening skill")
			if err != nil || len(pairs)%2 != 0 {
				return fail("invalid awakening pairs")
			}
			for k := 0; k < len(pairs); k += 2 {
				if !add(pairs[k], pairs[k+1], 1, stage) {
					return fail("invalid awakening grant")
				}
			}
		}
		byBranch[branch] = rows
	}
	section, err := pvftext.Section(text, "growtype name")
	if err != nil {
		return nil, nil, nil, err
	}
	names := quotedValue.FindAllStringSubmatch(section, -1)
	named := map[byte]bool{}
	for i := 1; i < len(names) && i <= 5; i++ {
		v := strings.TrimSpace(names[i][1])
		named[byte(i)] = v != "" && !strings.HasPrefix(v, "//") && v != strings.TrimSpace(names[0][1])
	}
	return initial, byBranch, named, nil
}
