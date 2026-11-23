package catalogprep

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"s9a1/server/internal/catalogprep/dungeons"
	"s9a1/server/internal/fate"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

// CharacterTemplates applies the same initial grants and quest eligibility used by the game host.
func CharacterTemplates(jobs map[byte]CharacterGrowth, skills PreparedSkills, levels []Level, tutorials []dungeons.Opening, quests *questwire.Catalog) ([]resources.CharacterTemplate, error) {
	if len(levels) == 0 || quests == nil {
		return nil, fmt.Errorf("character creation needs level-one progression and quest eligibility")
	}
	byJob := map[byte]opening.Definition{}
	for _, t := range tutorials {
		byJob[t.Job] = t.Definition
	}
	rows := []resources.CharacterTemplate{}
	for _, job := range slices.Sorted(maps.Keys(jobs)) {
		stats, err := json.Marshal(struct{ Values map[string]int64 }{jobs[job].Stats})
		if err != nil {
			return nil, err
		}
		for branch := byte(0); branch <= 5; branch++ {
			learned := skills.InitialSkills[job]
			if branch != 0 {
				table, ok := skills.Grants[job][branch]
				if !ok {
					continue
				}
				learned, err = opening.AdvanceLearnedSkills(learned, table, 1, branch)
				if err != nil {
					return nil, err
				}
			}
			game, err := newCharacterProfile(jobs[job], learned, levels[0].Sp, branch, byJob[job])
			if err != nil {
				return nil, err
			}
			c := storage.Character{ID: 1, Name: "资源样本", Job: job, Level: 1, Stats: stats, Gameplay: game, ResourceHash: jobs[job].SourceHash,
				Position: storage.Position{Town: 38, Area: 0, X: 1677, Y: 222, Direction: 5}}
			outdoor, err := quests.Available(c)
			if err != nil {
				return nil, err
			}
			c.Position.Area = 1
			room, err := quests.Available(c)
			if err != nil {
				return nil, err
			}
			rows = append(rows, resources.CharacterTemplate{Job: job, GrowType: branch, SourceHash: jobs[job].SourceHash,
				Stats: stats, Gameplay: game, OutdoorQuests: outdoor, RoomQuests: room})
		}
	}
	return rows, nil
}

func newCharacterProfile(job CharacterGrowth, learned []LearnedSkill, sp uint16, branch byte, tutorial opening.Definition) (json.RawMessage, error) {
	// Empty maps/lists and omitted optional properties mirror LocalGameplayProfile.Empty.
	empty := map[string]any{}
	mail := storage.Mailbox{Mails: []storage.MailLetter{}, DeliveryReceipts: map[string]json.RawMessage{}, ClaimTransfers: map[int64]storage.MailTransfer{}}
	profile := map[string]any{
		"AuraSkinUnlocked": false, "PetSkinUnlocked": false, "CharacterCashPurchases": empty,
		"CharacterServices": struct{ Day, Slot0, Slot1, Slot2, Slot3, Slot4, Slot5, Slot6, Slot7 int }{},
		"Mailbox":           mail, "CrystalContractSelection": 255, "Fate": fate.NewProfile(),
		"FatigueItemUses": empty, "Skills": learned, "Initialized": true, "ClassReplacementCount": 0,
		"Quests": map[string]any{"Active": empty, "Cleared": empty},
		"Progression": map[string]any{"Experience": 0, "Sp": sp, "Tp": 0, "GrowType": branch, "Fatigue": 156, "FatigueLimit": 156,
			"FatigueDay": 19700101, "Hp": job.Stats["hp_max"], "Mp": job.Stats["mp_max"]},
		"GiftBoxUses": empty, "GiftBoxCooldownGroups": empty, "GoldExpansion": 0, "Level100CapsuleCooldownUntilMilliseconds": 0,
		"NpcPurchases": empty, "MailSendQuota": struct{ DayId, SentGold int }{}, "PotionCooldowns": empty, "QuestTicketCooldowns": empty,
		"SharedMaterialImports": empty, "SpecialAssets": map[string]any{"Items": []any{}, "TownDisplay": false, "AvatarExpansion": 0},
	}
	if tutorial.Dungeon.ID != 0 {
		profile["Tutorial"] = map[string]any{"Dungeon": tutorial.Dungeon.ID, "ResourceHash": tutorial.Hash, "ReturnPoint": tutorial.ReturnPoint, "Completed": false}
	}
	return json.Marshal(profile)
}
