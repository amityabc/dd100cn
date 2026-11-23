package questwire

import (
	"encoding/json"
	"time"

	"s9a1/server/internal/storage"
)

// RefreshLoginGameplay reuses the quest action's daily cycle for first entry.
// A nil profile means that the saved quest book already represents this day.
func (c *Catalog) RefreshLoginGameplay(character storage.Character, now time.Time) (json.RawMessage, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(character.Gameplay, &document); err != nil {
		return nil, err
	}
	if len(document["Quests"]) == 0 {
		return nil, nil
	}
	var book map[string]json.RawMessage
	if err := json.Unmarshal(document["Quests"], &book); err != nil || book == nil {
		return nil, storage.ErrInvalid
	}
	p, err := playerOf(character)
	if err != nil {
		return nil, err
	}
	changed, err := c.refresh(book, &p, false, now)
	if err != nil || !changed {
		return nil, err
	}
	document["Quests"], err = json.Marshal(book)
	if err != nil {
		return nil, err
	}
	return json.Marshal(document)
}
