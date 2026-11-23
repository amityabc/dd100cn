package host

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) prepareSelectedGameplay(ctx context.Context, c storage.Character, now time.Time) (storage.Character, error) {
	p, err := readGameplay(c.Gameplay)
	if err != nil {
		return c, err
	}
	var update json.RawMessage
	if !p.Initialized {
		base, ok := g.catalog().Template(c.Job, 0)
		if !ok {
			return c, storage.ErrInvalid
		}
		update, err = initializeSelectedGameplay(c, base.Gameplay, now)
		if err != nil {
			return c, err
		}
		c.Gameplay = update
	}
	questUpdate, err := g.catalog().Quests.RefreshLoginGameplay(c, now)
	if err != nil {
		return c, err
	}
	if questUpdate != nil {
		update = questUpdate
	}
	if update == nil {
		return c, nil
	}
	c, err = g.server.DB.SaveSelectedGameplay(ctx, g.lease, c, update)
	if err == nil {
		g.trackPetCare(c)
	}
	return c, err
}

func initializeSelectedGameplay(c storage.Character, template json.RawMessage, now time.Time) (json.RawMessage, error) {
	var stats struct{ Values map[string]int64 }
	var doc map[string]json.RawMessage
	var base struct {
		Initialized bool
		Progression struct{ Sp uint16 }
		Skills      json.RawMessage
	}
	var skills []protocol.ActorSkill
	if json.Unmarshal(c.Stats, &stats) != nil || json.Unmarshal(c.Gameplay, &doc) != nil || doc == nil ||
		json.Unmarshal(template, &base) != nil || !base.Initialized {
		return nil, storage.ErrInvalid
	}
	if raw := doc["Skills"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &skills); err != nil {
			return nil, err
		}
	}
	if len(skills) == 0 {
		if err := json.Unmarshal(base.Skills, &skills); err != nil {
			return nil, err
		}
		doc["Skills"] = base.Skills
	}
	if skills == nil || len(skills) > 255 {
		return nil, storage.ErrInvalid
	}
	seenIDs, slots := map[uint16]bool{}, map[uint16]bool{}
	for _, v := range skills {
		if v.ID == 0 || v.Level == 0 || v.Slot > 254 || seenIDs[v.ID] || slots[v.Slot] {
			return nil, storage.ErrInvalid
		}
		seenIDs[v.ID], slots[v.Slot] = true, true
	}
	read := func(key string, fallback int64) int64 {
		if v, ok := stats.Values[key]; ok {
			return v
		}
		return fallback
	}
	hpMax, hpFound := stats.Values["hp_max"]
	mpMax, mpFound := stats.Values["mp_max"]
	day := now.UTC().Add(2 * time.Hour)
	fallbackDay := int64(day.Year()*10000 + int(day.Month())*100 + day.Day())
	exp := read("exp", 0)
	sp, tp, grow := read("sp", int64(base.Progression.Sp)), read("tp", 0), read("grow_type", 0)
	fatigue, limit, savedDay := read("fatigue", 156), read("fatigue_limit", 156), read("fatigue_day", fallbackDay)
	hp, mp := read("hp", hpMax), read("mp", mpMax)
	if !hpFound || !mpFound || hpMax < 0 || hpMax > math.MaxUint32 || mpMax < 0 || mpMax > math.MaxUint32 ||
		exp < 0 || sp < 0 || sp > math.MaxUint16 || tp < 0 || tp > math.MaxUint16 || grow < 0 || grow > 0x3f || grow&15 > 5 ||
		fatigue < 0 || fatigue > limit || limit > math.MaxUint16 || savedDay < 0 || hp < 0 || hp > hpMax || mp < 0 || mp > mpMax {
		return nil, storage.ErrInvalid
	}
	doc["Progression"], _ = json.Marshal(struct {
		Experience            uint64
		Sp, Tp                uint16
		GrowType              byte
		Fatigue, FatigueLimit uint16
		FatigueDay            int64
		Hp, Mp                uint32
	}{uint64(exp), uint16(sp), uint16(tp), byte(grow), uint16(fatigue), uint16(limit), savedDay, uint32(hp), uint32(mp)})
	doc["Initialized"] = json.RawMessage("true")
	if len(doc["Quests"]) == 0 {
		doc["Quests"] = json.RawMessage(`{"Active":{},"Cleared":{}}`)
	}
	return json.Marshal(doc)
}
