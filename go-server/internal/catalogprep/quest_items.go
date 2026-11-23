package catalogprep

import (
	"encoding/json"
	"fmt"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/quest"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
)

// QuestItems binds only fully prepared resources to quest acceptance or completion.
func QuestItems(items *commerce.Items, result *quest.Result) error {
	pinned, err := items.Rename()
	if err != nil {
		return err
	}
	read := func(id uint32) (json.RawMessage, error) {
		t := pinned[id]
		if t == nil {
			// Absent source members cancel preparation; unsupported inventory shapes
			// are valid quest references with no materializable asset.
			if _, err := items.Source(id); err != nil {
				return nil, err
			}
			var parseErr error
			t, parseErr = items.Prepared(id)
			if parseErr != nil || t == nil {
				return nil, nil
			}
		}
		return json.Marshal(t)
	}
	attrs, err := storage.InitialItemAttributes(0, 0)
	if err != nil {
		return err
	}
	result.Data.AcceptanceGrants = []questwire.AcceptanceGrant{}
	result.Data.RewardTemplates = []json.RawMessage{}
	for _, id := range result.AcceptanceTemplateIDs {
		t, err := read(id)
		if err != nil {
			return fmt.Errorf("quest acceptance %d: %w", id, err)
		}
		if t != nil {
			result.Data.AcceptanceGrants = append(result.Data.AcceptanceGrants, questwire.AcceptanceGrant{Template: t, Attributes: attrs, Core: make([]byte, 119), Detail: []byte{}})
		}
	}
	for _, id := range result.RewardTemplateIDs {
		t, err := read(id)
		if err != nil {
			return fmt.Errorf("quest reward %d: %w", id, err)
		}
		if t != nil {
			result.Data.RewardTemplates = append(result.Data.RewardTemplates, t)
		}
	}
	return nil
}
