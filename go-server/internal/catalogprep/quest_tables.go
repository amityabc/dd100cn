package catalogprep

import (
	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/quest"
	"s9a1/server/internal/pvf"
	"s9a1/server/internal/questwire"
)

// QuestTables retains the complete PVF skill definition. The runtime also
// accepts a reduced view of the same records for quest-side skill effects.
type QuestTables struct {
	questwire.Data
	SkillDefinitions []SkillRow
	InitialSkills    map[byte][]LearnedSkill
}

func PrepareQuestTables(a *pvf.Archive, items *commerce.Items, rows *quest.Result, skills PreparedSkills) (*QuestTables, error) {
	if err := QuestItems(items, rows); err != nil {
		return nil, err
	}
	growth, err := quest.BuildGrowth(a, items)
	if err != nil {
		return nil, err
	}
	pass, err := quest.BuildBattlePass(a, items)
	if err != nil {
		return nil, err
	}
	disguise, err := quest.BuildDisguise(items)
	if err != nil {
		return nil, err
	}
	rows.Data.GrowthItems, rows.Data.BattlePass, rows.Data.Disguise = growth, pass, disguise
	return &QuestTables{rows.Data, skills.SkillDefinitions, skills.InitialSkills}, nil
}
