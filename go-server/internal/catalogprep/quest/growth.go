package quest

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/pvf"
	"s9a1/server/internal/questwire"
)

type growthSources struct {
	ability, dyes, absolute, conversions []uint32
}

// BuildGrowth combines native stackable effects with avatar, emblem and
// creature records. A script with no confirmed use owner stays out of its map.
func BuildGrowth(a *pvf.Archive, items ItemLookup) (questwire.GrowthItems, error) {
	growth, sources, err := prepareGrowthItems(a, items)
	if err == nil {
		growth.AvatarProjection, err = buildAvatars(a, items, sources, growth.Emblems)
	}
	return growth, err
}

func prepareGrowthItems(a *pvf.Archive, items ItemLookup) (questwire.GrowthItems, growthSources, error) {
	out := questwire.GrowthItems{
		Experience: make(map[uint32]questwire.ExperienceItem), LevelTickets: make(map[uint32]questwire.LevelTicket),
		Capsules: make(map[uint32]questwire.CapsuleItem), QuestTickets: make(map[uint32]questwire.QuestTicketItem),
		Teleports: make(map[uint32]questwire.TeleportDefinition),
	}
	var sources growthSources
	out.Emblems.Colors = make(map[uint32]uint16)
	out.Emblems.Compound.Sources = make(map[uint32]emblemSource)
	var err error
	out.Emblems.SocketTypes, err = loadEmblemSockets(a)
	if err != nil {
		return out, sources, err
	}
	paths, err := a.ReadIndex("stackable")
	if err != nil {
		return out, sources, err
	}
	type indexed struct {
		id            uint32
		path          string
		chunk, offset int
	}
	entries := make([]indexed, 0, len(paths))
	for id, path := range paths {
		if id <= 0 || id > math.MaxInt32 {
			continue
		}
		chunk, offset, ok := a.PhysicalIndex(path)
		if ok {
			entries = append(entries, indexed{uint32(id), path, chunk, offset})
		}
	}
	slices.SortFunc(entries, func(a, b indexed) int {
		if delta := a.chunk - b.chunk; delta != 0 {
			return delta
		}
		return a.offset - b.offset
	})
	destinations, destinationHash, err := teleportDestinations(a)
	if err != nil {
		return out, sources, err
	}
	for _, row := range entries {
		id := row.id
		text, err := a.ReadText(row.path, budget)
		if err != nil {
			continue
		}
		if strings.Contains(text, "[avatar select ability change]") {
			sources.ability = append(sources.ability, id)
		}
		if strings.Contains(text, "\x60[dye]\x60") {
			sources.dyes = append(sources.dyes, id)
		}
		if strings.Contains(text, "\x60[absolute bind cube]\x60") {
			sources.absolute = append(sources.absolute, id)
		}
		if strings.Contains(text, "\x60[change avatar]\x60") {
			sources.conversions = append(sources.conversions, id)
		}
		if strings.Contains(strings.ToLower(text), "[avatar emblem]") {
			addEmblem(&out.Emblems, items, id, text)
		}
		experience := experienceBooks[id] != 0 || strings.Contains(text, "\x60[expUp")
		level := strings.Contains(text, "\x60[level up ticket]\x60")
		capsuleItem := strings.Contains(text, "\x60[jumping lv]\x60")
		ticket := strings.Contains(text, " clear]\x60")
		teleport := strings.Contains(text, "\x60[teleport potion]\x60")
		if teleport || strings.Contains(text, "\x60[add fatigue]\x60") {
			out.PreparedTemplateIDs = append(out.PreparedTemplateIDs, id)
		}
		if !experience && !level && !capsuleItem && !ticket && !teleport {
			continue
		}
		src, err := items.Source(id)
		if err != nil || src.Equipment {
			continue
		}
		if teleport {
			if definition, err := parseTeleport(src, destinations, destinationHash); err == nil {
				out.Teleports[id] = definition
			}
		}
		if !experience && !level && !capsuleItem && !ticket {
			continue
		}
		var parsedTicket questwire.QuestTicketItem
		mode := gm.GiftDefault
		if !experience && !level && !capsuleItem && ticket {
			parsedTicket, err = parseTicketItem(id, text, src.Hash, nil)
			if err != nil {
				continue
			}
			mode = gm.GiftQuestTicket
		}
		template, err := src.GiftFor(mode)
		if err != nil {
			continue
		}
		raw, err := json.Marshal(template)
		if err != nil {
			return out, sources, err
		}
		switch {
		case experience:
			if def, err := experienceItem(id, text, template, raw); err == nil {
				out.Experience[id] = def
			}
		case level:
			if def, err := levelItem(text, raw); err == nil {
				out.LevelTickets[id] = def
			}
		case capsuleItem:
			if def, err := capsule(text, template, raw); err == nil {
				out.Capsules[id] = def
			}
		case ticket:
			parsedTicket.Template = raw
			out.QuestTickets[id] = parsedTicket
		}
	}
	for _, list := range [][]uint32{sources.ability, sources.dyes, sources.absolute, sources.conversions} {
		slices.Sort(list)
	}
	out.PetGrowth, err = petGrowth(a, items)
	if err == nil {
		err = loadEmblemGraph(a, &out.Emblems, items)
	}
	return out, sources, err
}

func sortedTemplateIDs[T any](m map[uint32]T) []uint32 { return slices.Sorted(maps.Keys(m)) }
