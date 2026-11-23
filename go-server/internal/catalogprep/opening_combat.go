package catalogprep

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/dungeons"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
)

type OpeningCombat struct {
	ExperienceHash string
	Levels         []Level
	Jobs           map[byte]OpeningJob
	ClassTickets   map[uint32]ClassTicket
	Replacement    ClassReplacement
}

type OpeningJob struct {
	Initial              map[string]json.Number
	Growth               []GrowthCurve
	Grants               map[byte][]AdvancementSkill
	Awards               []dungeons.Award
	ExperienceMultiplier json.Number
	DisableExperience    bool
}

type ClassTicket struct {
	Template     *gm.ItemTemplate
	MaximumLevel byte
}

func OpeningCombatTables(items *commerce.Items, levels []Level, replacement ClassReplacement, jobs map[byte]CharacterGrowth, skills PreparedSkills, tutorials []dungeons.Opening, experience *dungeons.Experience) (*OpeningCombat, error) {
	data := &OpeningCombat{ExperienceHash: experience.Hash, Levels: levels, Jobs: map[byte]OpeningJob{}, Replacement: replacement, ClassTickets: map[uint32]ClassTicket{}}
	for _, o := range tutorials {
		awards, err := experience.OpeningAwards(o)
		if err != nil {
			return nil, err
		}
		j := jobs[o.Job]
		data.Jobs[o.Job] = OpeningJob{j.Initial, j.Growth, skills.Grants[o.Job], awards, o.ExperienceMultiplier, o.DisableExperience}
	}
	stock := items.StackableIds()
	for _, id := range []uint32{2660702, 2660779} {
		if _, found := slices.BinarySearch(stock, id); !found {
			continue
		}
		item, err := items.Source(id)
		if err != nil {
			return nil, err
		}
		row, err := classTicket(item)
		if err != nil {
			return nil, fmt.Errorf("class ticket %d: %w", id, err)
		}
		data.ClassTickets[id] = row
	}
	return data, nil
}
func classTicket(item commerce.ItemSource) (ClassTicket, error) {
	t, err := item.Gift()
	if err != nil {
		return ClassTicket{}, err
	}
	script, err := gm.ItemFields(item.Text)
	if err != nil {
		return ClassTicket{}, err
	}
	fields := map[string]string{}
	known := strings.Split("name|explain|explain alpha|flavor text|grade|rarity|usable job|attach type|minimum level|maximum level|icon|field image|stackable type|move wav|action type|action usable place|action usable server type|impossible contents|need material|price|value|weight|stack limit|usable period|expiration date", "|")
	for _, f := range script {
		if _, dup := fields[f.Name]; dup || !slices.Contains(known, strings.TrimPrefix(f.Name, "/")) {
			return ClassTicket{}, fmt.Errorf("unexpected ticket field %s", f.Name)
		}
		fields[f.Name] = f.Value
	}
	for _, tag := range []string{"usable job", "action type", "action usable place", "action usable server type", "impossible contents", "need material"} {
		_, open := fields[tag]
		v, close := fields["/"+tag]
		if open != close || v != "" {
			return ClassTicket{}, fmt.Errorf("incomplete %s", tag)
		}
	}
	n, err := strconv.ParseUint(fields["maximum level"], 10, 8)
	if err != nil || n > 100 || n < uint64(t.MinimumLevel) || t.Kind != 2 || t.FirstBagSlot != 65 || t.LastBagSlot != 120 || fields["stackable type"] != "`[waste]` 0" || fields["action type"] != "`[class change]`" || fields["action usable place"] != "`[seria room]` `[village]` `[dont use assult]`" || fields["action usable server type"] != "`[general server]` `[event server]`" || fields["impossible contents"] != "" && fields["impossible contents"] != "`gift`" {
		return ClassTicket{}, fmt.Errorf("unsupported class change policy")
	}
	if v, ok := fields["need material"]; ok {
		ids, err := pvftext.Integers(v)
		if err != nil || len(ids) == 0 || len(ids)%2 != 0 || slices.ContainsFunc(ids, func(n int) bool { return n <= 0 }) {
			return ClassTicket{}, fmt.Errorf("unsupported material cost")
		}
	}
	return ClassTicket{t, byte(n)}, nil
}
