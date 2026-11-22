package resources

import (
	"math"
	"slices"
	"strings"

	"s9a1/server/internal/storage"
)

func (c *CharacterCatalog) TownMerchant(ch storage.Character, npc, shop uint32) bool {
	cat := c.Commerce
	pi := slices.IndexFunc(cat.TownPeers, func(p storage.CommercePeer) bool { return p.NPC == npc && p.Shop == shop })
	if pi < 0 {
		return false
	}
	return c.townCommercePlacement(ch, cat.TownPeers[pi].Placements)
}

func (c *CharacterCatalog) TownDisassembler(ch storage.Character, npc uint32) bool {
	return slices.Contains(c.Commerce.Disassemblers, npc) && c.townCommercePlacement(ch, []uint32{npc})
}

func (c *CharacterCatalog) TownRepair(ch storage.Character) bool {
	return c.townCommercePlacement(ch, c.Commerce.RepairNPCs)
}

func (c *CharacterCatalog) TownCraft(ch storage.Character, method storage.CraftMethod, npc int16) bool {
	switch method {
	case storage.CraftRemodel, storage.CraftEnchant, storage.CraftTicket:
		return true
	}
	placements := c.Commerce.Craft.NPCs[method]
	if npc == -1 {
		return c.townCommercePlacement(ch, placements)
	}
	asked := uint32(uint16(npc))
	return slices.Contains(placements, asked) && c.townCommercePlacement(ch, []uint32{asked})
}

func (c *CharacterCatalog) townCommercePlacement(ch storage.Character, placements []uint32) bool {
	cat := c.Commerce
	i := slices.IndexFunc(cat.TownAreas, func(a storage.CommerceArea) bool { return a.Town == ch.Position.Town && a.Area == ch.Position.Area })
	if i < 0 {
		return false
	}
	q, err := ReadTownQuests(ch.Gameplay)
	if err != nil {
		return false
	}
	a := cat.TownAreas[i]
	if !permission(a.Permission).allows(ch.Level, q) {
		return false
	}
	paths := []string{a.Path}
	if !exempt(townArea{Town: a.Town, Area: a.Area}) {
		rules := cat.TownPhases[a.Town]
		variant := func(index int) string {
			if index < 0 || len(a.Phases) == 0 {
				return a.Path
			}
			if index >= len(a.Phases) {
				return ""
			}
			return a.Phases[index]
		}
		selected := false
		for _, r := range rules {
			switch r.Kind {
			case "completed quest":
				if r.Id > 0 && r.Id <= math.MaxUint16 && q.clear(uint16(r.Id)) {
					paths = []string{variant(r.Index)}
					selected = true
				}
			case "by event", "depend townarea event":
			default:
				return false
			}
		}
		if !selected {
			for _, r := range rules {
				if r.Kind == "by event" {
					paths = append(paths, variant(r.Index))
				}
			}
		}
	}
	return slices.ContainsFunc(placements, func(placed uint32) bool {
		if !merchantVisible(cat.Visibility, q, placed) {
			return false
		}
		for _, path := range paths {
			if !slices.Contains(cat.TownMaps[strings.ToLower(path)], placed) {
				return false
			}
		}
		return true
	})
}

func merchantVisible(v storage.CommerceVisibility, q TownQuests, id uint32) bool {
	if slices.Contains(v.Blocked, id) {
		return false
	}
	applied := []storage.CommerceVisibilityRule{}
	for _, r := range v.Rules[id] {
		_, active := q.Active[r.Quest]
		if r.Clear && q.clear(r.Quest) || !r.Clear && (active || !r.Show && q.clear(r.Quest)) {
			applied = append(applied, r)
		}
	}
	for _, hidden := range applied {
		if hidden.Show {
			continue
		}
		if _, active := q.Active[hidden.Quest]; active {
			return false
		}
		restored := slices.ContainsFunc(applied, func(r storage.CommerceVisibilityRule) bool {
			_, active := q.Active[r.Quest]
			return r.Show && r.Clear && !active && (r.Quest == hidden.Quest && !hidden.Clear ||
				r.Quest != hidden.Quest && slices.Contains(r.Earlier, hidden.Quest))
		})
		if !restored {
			return false
		}
	}
	return !slices.Contains(v.Hidden, id) || slices.ContainsFunc(applied, func(r storage.CommerceVisibilityRule) bool { return r.Show })
}

func (c *CharacterCatalog) ConsumableTown(ch storage.Character) (string, error) {
	q, err := ReadTownQuests(ch.Gameplay)
	if err != nil {
		return "", err
	}
	pos := ch.Position
	p := TownPoint{Town: pos.Town, Area: pos.Area, X: pos.X, Y: pos.Y, Direction: pos.Direction}
	if c.Towns == nil || !c.Towns.allows(ch.Level, q, p, false) {
		return "", storage.ErrPhase
	}
	if c.Towns.IsRoom(p) {
		return "seria room", nil
	}
	return "village", nil
}
