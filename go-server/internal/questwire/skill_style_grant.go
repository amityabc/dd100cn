package questwire

import (
	"encoding/json"
	"s9a1/server/internal/storage"
	"slices"
)

func (catalog *Catalog) GrantSkillStyle(c storage.Character, freeSkills func(byte, byte, byte) map[uint16]byte) (json.RawMessage, error) {
	p, err := playerOf(c)
	if err != nil || !p.initialized || c.Job > 15 || c.Level == 0 || p.grow > 0x35 || p.grow&15 > 5 || freeSkills == nil {
		return nil, storage.ErrInvalid
	}
	var doc, progress skillPage
	var b skillBuild
	var locks []byte
	if json.Unmarshal(c.Gameplay, &doc) != nil || doc == nil ||
		len(doc["SkillStyles"]) != 0 && string(doc["SkillStyles"]) != "null" ||
		json.Unmarshal(doc["Progression"], &progress) != nil || progress == nil ||
		json.Unmarshal(doc["Skills"], &b.Rows) != nil || !validSkills(b.Rows) ||
		json.Unmarshal(progress["Sp"], &b.Sp) != nil || json.Unmarshal(progress["Tp"], &b.Tp) != nil ||
		len(doc["CommandLocks"]) != 0 && (json.Unmarshal(doc["CommandLocks"], &locks) != nil || locks != nil && len(locks) != 512) {
		return nil, storage.ErrInvalid
	}
	for _, row := range b.Rows {
		if _, ok := catalog.definition(c.Job, row.Id); !ok {
			return nil, storage.ErrInvalid
		}
	}
	free := freeSkills(c.Job, c.Level, p.grow)
	b, err = catalog.reset(c, p, b, SkillRequest{Opcode: 483}, free)
	if err != nil {
		return nil, err
	}
	b.Rows = slices.Clone(catalog.initialSkills[c.Job])
	used := map[uint16]bool{}
	for _, row := range b.Rows {
		used[row.Slot] = true
	}
	for _, id := range sortedKeys(free) {
		level := free[id]
		d, ok := catalog.definition(c.Job, id)
		if !ok || level == 0 {
			return nil, storage.ErrInvalid
		}
		i := slices.IndexFunc(b.Rows, func(s questSkill) bool { return s.Id == id })
		if i >= 0 {
			b.Rows[i].Level = max(b.Rows[i].Level, level)
			continue
		}
		slot := nextSkillSlot(used, d.Active)
		if slot > 254 {
			return nil, storage.ErrInvalid
		}
		used[slot] = true
		b.Rows = append(b.Rows, questSkill{Id: id, Level: level, Slot: slot})
	}
	slices.SortFunc(b.Rows, func(a, b questSkill) int { return int(a.Id) - int(b.Id) })
	if !validSkills(b.Rows) {
		return nil, storage.ErrInvalid
	}
	doc["SkillStyles"], err = json.Marshal(struct {
		Active byte
		Other  skillBuild
	}{0, b})
	if err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}
