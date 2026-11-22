package questwire

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"

	"s9a1/server/internal/storage"
)

func (catalog *Catalog) PlanSkill(c storage.Character, req SkillRequest, opts SkillContext) (SkillResult, error) {
	out := SkillResult{Session: opts.Session}
	var doc, progress, active skillPage
	var pstate skillProgress
	var b skillBuild
	p, err := playerOf(c)
	if err != nil || !p.initialized || c.Level == 0 || c.Job > 15 || p.grow > 0x3f || p.grow&15 > 5 ||
		json.Unmarshal(c.Gameplay, &doc) != nil || json.Unmarshal(doc["Progression"], &progress) != nil || json.Unmarshal(doc["Progression"], &pstate) != nil || progress == nil {
		return out, storage.ErrInvalid
	}
	if req.Overlay() {
		return catalog.planOverlay(c, p, doc, req, opts)
	}
	active = progress
	rows := doc["Skills"]
	locks := doc["CommandLocks"]
	if opts.Duel {
		if req.Opcode == 331 || req.Opcode == 260 {
			return out, skillError(19)
		}
		if json.Unmarshal(doc["DuelSkills"], &active) != nil || active == nil {
			return out, storage.ErrInvalid
		}
		rows, locks = active["Skills"], active["CommandLocks"]
	}
	if json.Unmarshal(rows, &b.Rows) != nil || !validSkills(b.Rows) || json.Unmarshal(active["Sp"], &b.Sp) != nil || json.Unmarshal(active["Tp"], &b.Tp) != nil {
		return out, storage.ErrInvalid
	}
	var style skillPage
	var selected byte
	if !opts.Duel && doc["SkillStyles"] != nil && string(doc["SkillStyles"]) != "null" {
		if json.Unmarshal(doc["SkillStyles"], &style) != nil {
			return out, storage.ErrInvalid
		}
		if _, selected, err = validateSkillStyle(style, locks); err != nil {
			return out, err
		}
	}
	if style != nil && (req.Opcode == 28 || req.Opcode == 29 || req.Opcode == 483) && req.Tree != 255 && req.Tree != selected {
		return out, skillError(19)
	}
	if req.Opcode == 439 {
		var old []byte
		if len(locks) > 0 && (json.Unmarshal(locks, &old) != nil || old != nil && len(old) != 512) {
			return out, storage.ErrInvalid
		}
		if bytes.Equal(old, req.Locks) {
			return out, nil
		}
		if opts.Duel {
			active["CommandLocks"], _ = json.Marshal(encodeCodes(req.Locks))
			doc["DuelSkills"], _ = json.Marshal(active)
		} else {
			doc["CommandLocks"], _ = json.Marshal(encodeCodes(req.Locks))
		}
		out.Mutation, _ = json.Marshal(doc)
		return out, nil
	}
	if req.Opcode == 331 {
		var old map[uint16][]byte
		if len(doc["SkillCommands"]) > 0 && json.Unmarshal(doc["SkillCommands"], &old) != nil || len(old) > 64 {
			return out, storage.ErrInvalid
		}
		for id, keys := range old {
			if id == 0 || keys == nil || len(keys) > 8 {
				return out, storage.ErrInvalid
			}
		}
		seen := map[uint16]bool{}
		commands := map[uint16][]byte{}
		for _, cmd := range req.Commands {
			if seen[cmd.ID] || !slices.ContainsFunc(b.Rows, func(s questSkill) bool { return s.Id == cmd.ID }) {
				return out, storage.ErrInvalid
			}
			seen[cmd.ID] = true
			if len(cmd.Keys) > 0 {
				commands[cmd.ID] = cmd.Keys
			}
		}
		out.Ack = commandBody(commands)
		if maps.EqualFunc(old, commands, bytes.Equal) {
			return out, nil
		}
		encoded := map[uint16][]uint16{}
		for id, keys := range commands {
			encoded[id] = encodeCodes(keys)
		}
		doc["SkillCommands"], _ = json.Marshal(encoded)
		out.Mutation, _ = json.Marshal(doc)
		return out, nil
	}
	out.Session = SkillSession{}
	before := b
	var changed []questSkill
	var free map[uint16]byte
	if opts.FreeSkills != nil {
		free = opts.FreeSkills(c.Job, c.Level, p.grow)
	}
	switch req.Opcode {
	case 260:
		if style == nil {
			return out, skillError(1)
		}
		if req.Tree != selected {
			return out, skillError(19)
		}
		if json.Unmarshal(style["Other"], &b) != nil {
			return out, storage.ErrInvalid
		}
		target := selected ^ 1
		style["Active"], _ = json.Marshal(target)
		style["Other"], _ = json.Marshal(before)
		doc["SkillStyles"], _ = json.Marshal(style)
		out.Ack = []byte{1, target}
		out.ProjectionBefore = true
	case 483:
		if req.Tree > 1 || req.Mode > 1 {
			return out, skillError(19)
		}
		b, err = catalog.reset(c, p, b, req, free)
		if err != nil {
			return out, err
		}
		out.Ack = []byte{1, req.Tree, req.Mode}
		out.ProjectionBefore = true
	case 28:
		if req.Mode != 0 {
			return out, storage.ErrInvalid
		}
		b, err = catalog.swap(c, b, req)
		if err != nil {
			return out, err
		}
		out.Ack = []byte{1, req.Tree, req.Source, req.Destination}
	case 29:
		if req.Mode > 1 || len(req.Slots) == 0 {
			return out, storage.ErrInvalid
		}
		b, changed, err = catalog.train(c, p, b, req.Slots, free, opts.SkillMaster && !opts.Duel)
		if err != nil {
			return out, err
		}
		out.Ack = skillTrainAck(req, b, changed)
		out.ProjectionAfter = true
	}
	dirty := req.Opcode != 483 || b.Sp != before.Sp || b.Tp != before.Tp || !slices.Equal(b.Rows, before.Rows)
	if dirty {
		applyBuild(doc, active, b, opts.Duel)
	}
	if opts.TrainingLobby && req.Mode == 1 {
		training, err := catalog.DuelObjective(c, 0)
		if err == nil && (training.ID == 2672 || training.ID == 2674) && training.Remaining[0] != 0 {
			stamp := skillAuto{training.Acceptance, training.ID, c.ID, c.Revision, req.Tree}
			if req.Opcode == 483 {
				if dirty {
					stamp.Revision++
				}
				out.Session.auto = stamp
			} else if req.Opcode == 29 && stamp == opts.Session.auto {
				out.TrainingID, out.Remaining, err = catalog.skillStage(c, doc, training)
				if err != nil {
					return out, err
				}
			}
		}
	}
	if dirty {
		out.Mutation, _ = json.Marshal(doc)
	}
	return out, nil
}

func validateSkillStyle(style skillPage, sharedLocks json.RawMessage) (skillPage, byte, error) {
	var index byte
	var page skillPage
	var rows []questSkill
	var locks []byte
	if json.Unmarshal(style["Active"], &index) != nil || index > 1 || json.Unmarshal(style["Other"], &page) != nil || page == nil ||
		page["CommandLocks"] != nil && string(page["CommandLocks"]) != "null" ||
		json.Unmarshal(page["Skills"], &rows) != nil || !validSkills(rows) ||
		sharedLocks != nil && (json.Unmarshal(sharedLocks, &locks) != nil || locks != nil && len(locks) != 512) {
		return nil, 0, storage.ErrInvalid
	}
	return page, index, nil
}

func applyBuild(doc, points skillPage, b skillBuild, duel bool) {
	points["Sp"], _ = json.Marshal(b.Sp)
	points["Tp"], _ = json.Marshal(b.Tp)
	if duel {
		points["Skills"], _ = json.Marshal(b.Rows)
		doc["DuelSkills"], _ = json.Marshal(points)
	} else {
		doc["Skills"], _ = json.Marshal(b.Rows)
		doc["Progression"], _ = json.Marshal(points)
	}
}

func (catalog *Catalog) skillStage(c storage.Character, doc skillPage, d DuelObjective) (uint16, uint32, error) {
	q := catalog.ordinary[d.ID]
	if q == nil || !d.StageReady(0) {
		return d.ID, d.Remaining[1]<<9 | d.Remaining[2]<<18, nil
	}
	book, rows, err := questBook(c)
	if err != nil {
		return 0, 0, err
	}
	var raw skillPage
	var a active
	if json.Unmarshal(rows[d.ID], &a) != nil || json.Unmarshal(rows[d.ID], &raw) != nil || raw == nil {
		return 0, 0, storage.ErrInvalid
	}
	a.Remaining[0] = 0
	a.Receipts = append(a.Receipts, trainingFact(0))
	raw["Remaining"], _ = json.Marshal(a.Remaining)
	raw["Receipts"], _ = json.Marshal(a.Receipts)
	rows[d.ID], _ = json.Marshal(raw)
	book["Active"], _ = json.Marshal(rows)
	doc["Quests"], _ = json.Marshal(book)
	return d.ID, d.Remaining[1]<<9 | d.Remaining[2]<<18, nil
}

func (catalog *Catalog) planOverlay(c storage.Character, p player, doc skillPage, req SkillRequest, opts SkillContext) (SkillResult, error) {
	out := SkillResult{Session: opts.Session}
	d, err := catalog.DuelObjective(c, 0)
	if err != nil || !opts.Duel || (d.ID != 2672 && d.ID != 2674) || c.Level < 50 {
		return out, storage.ErrInvalid
	}
	boundary := 35
	if d.ID == 2674 {
		boundary = 50
	}
	admitted := func(id uint16) bool {
		def, ok := catalog.definition(c.Job, id)
		return ok && len(def.TpCosts) == 0 && def.RequiredLevel < boundary
	}
	free := opts.FreeSkills(c.Job, c.Level, p.grow)
	var b skillBuild
	var changed []questSkill
	if req.Opcode == 483 {
		if req.Tree > 1 {
			return out, skillError(19)
		}
		initial, ok := catalog.initialSkills[c.Job]
		if !ok {
			return out, storage.ErrInvalid
		}
		b = skillBuild{Rows: []questSkill{}, Sp: 10770}
		used := map[uint16]bool{}
		for _, s := range initial {
			if admitted(s.Id) {
				b.Rows = append(b.Rows, s)
				used[s.Slot] = true
			}
		}
		for _, id := range sortedKeys(free) {
			if !admitted(id) {
				continue
			}
			i := slices.IndexFunc(b.Rows, func(s questSkill) bool { return s.Id == id })
			if i >= 0 {
				b.Rows[i].Level = max(b.Rows[i].Level, free[id])
				continue
			}
			def, _ := catalog.definition(c.Job, id)
			slot := nextSkillSlot(used, def.Active)
			if slot > 254 {
				return out, storage.ErrInvalid
			}
			used[slot] = true
			b.Rows = append(b.Rows, questSkill{Id: id, Slot: slot, Level: free[id]})
		}
		slices.SortFunc(b.Rows, func(a, b questSkill) int { return int(a.Id) - int(b.Id) })
		out.Ack = []byte{1, req.Tree, req.Mode & 1}
		out.ProjectionBefore = true
	} else {
		prior := opts.Session.overlay
		if !prior.matches(c, d, p.grow) || req.Tree != prior.Tree {
			return out, storage.ErrInvalid
		}
		b = prior.Build
		if req.Opcode == 28 {
			b, err = catalog.swap(c, b, req)
			if err != nil {
				return out, err
			}
			out.Ack = []byte{1, req.Tree, req.Source, req.Destination}
		} else {
			for _, s := range req.Slots {
				if def, ok := catalog.definition(c.Job, s.ID); ok && (len(def.TpCosts) > 0 || def.RequiredLevel >= boundary) {
					return out, storage.ErrInvalid
				}
			}
			b, changed, err = catalog.train(c, p, b, req.Slots, free, false)
			if err != nil {
				return out, err
			}
			if b.Tp != 0 {
				return out, storage.ErrInvalid
			}
			out.Ack = skillTrainAck(req, b, changed)
			out.ProjectionAfter = true
			out.TrainingID, out.Remaining, err = catalog.skillStage(c, doc, d)
			if err != nil {
				return out, err
			}
			if d.Remaining[0] != 0 {
				out.Mutation, _ = json.Marshal(doc)
			}
		}
	}
	if !validSkills(b.Rows) {
		return out, storage.ErrInvalid
	}
	out.Session = SkillSession{overlay: skillOverlay{d.Acceptance, d.ID, c.ID, p.grow, req.Tree, b}}
	out.Display = overlayProfile(doc, b)
	return out, nil
}

func (o skillOverlay) matches(c storage.Character, d DuelObjective, grow byte) bool {
	return o.ID != 0 && o.Character == c.ID && o.ID == d.ID && o.Acceptance == d.Acceptance && o.Grow == grow && o.Tree <= 1
}

func overlayProfile(doc skillPage, b skillBuild) json.RawMessage {
	var p skillPage
	if json.Unmarshal(doc["Progression"], &p) != nil || p == nil {
		return nil
	}
	applyBuild(doc, p, b, false)
	delete(doc, "DuelSkills")
	delete(doc, "SkillStyles")
	data, _ := json.Marshal(doc)
	return data
}

func (catalog *Catalog) OverlayDisplay(c storage.Character, s SkillSession) json.RawMessage {
	d, err := catalog.DuelObjective(c, s.overlay.ID)
	if err != nil {
		return nil
	}
	p, err := playerOf(c)
	if err != nil || !s.overlay.matches(c, d, p.grow) {
		return nil
	}
	var doc skillPage
	if json.Unmarshal(c.Gameplay, &doc) != nil {
		return nil
	}
	return overlayProfile(doc, s.overlay.Build)
}
