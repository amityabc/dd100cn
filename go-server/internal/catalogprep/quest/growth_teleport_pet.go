package quest

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/pvf"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/storage"
)

var teleportFields = strings.Split("name|explain|flavor text|grade|rarity|usable job|attach type|minimum level|weight|cool time|icon|field image|stackable type|item group name|move wav|string data|int data|expert type|stack limit|expiration date|usable period|daily delete item|weekly delete item|price", "|")

func teleportDestinations(a *pvf.Archive) ([]storage.Position, string, error) {
	const path = "etc/seriaroom/seriaroomwarpinfo.etc"
	text, err := a.ReadText(path, budget)
	if err != nil {
		return nil, "", err
	}
	raw, err := a.Read(path, budget)
	if err != nil {
		return nil, "", err
	}
	s := readItemScript(text)
	var points []storage.Position
	keys := make(map[[2]uint16]bool)
	for _, f := range s.fields {
		if f.name != "warp town" {
			continue
		}
		row := s.numbers(f.value)
		if len(row) != 4 || row[0] > 255 || row[1] > 255 || row[2] > math.MaxInt16 || row[3] > math.MaxInt16 {
			return nil, "", fmt.Errorf("invalid teleport destination")
		}
		key := [2]uint16{uint16(row[0]), uint16(row[1])}
		if keys[key] {
			return nil, "", fmt.Errorf("duplicate teleport destination")
		}
		keys[key] = true
		points = append(points, storage.Position{Town: key[0], Area: key[1], X: uint16(row[2]), Y: uint16(row[3]), Direction: 5})
	}
	if len(points) == 0 || len(points) > 256 {
		return nil, "", fmt.Errorf("missing or excessive teleport destinations")
	}
	return points, hash(string(raw)), s.err
}

func parseTeleport(src gm.ItemSource, points []storage.Position, sum string) (questwire.TeleportDefinition, error) {
	var out questwire.TeleportDefinition
	s := readItemScript(src.Text)
	s.allowed(teleportFields, true, true)
	if s.get("stackable type") != "\x60[teleport potion]\x60 0" {
		return out, fmt.Errorf("invalid teleport item type")
	}
	if s.get("price") != "" {
		s.number("price", 0, math.MaxInt32, true)
	}
	cooldown := s.number("cool time", 0, math.MaxInt32, true)
	level, jobs := s.number("minimum level", 1, 100, false), s.jobs("usable job", false)
	template, _, err := src.GM()
	if err != nil || template == nil || template.Kind != 2 || template.FirstBagSlot != 65 ||
		uint32(template.MinimumLevel) != level || template.UsableJobs != jobs {
		return out, fmt.Errorf("teleport eligibility differs from native template")
	}
	if s.err != nil {
		return out, s.err
	}
	template.WeaponQualification.Verified = true
	out.Item, err = json.Marshal(template)
	out.CooldownMilliseconds, out.Destinations, out.DestinationHash = cooldown, points, sum
	return out, err
}

func petGrowth(a *pvf.Archive, items ItemLookup) (*storage.PetGrowthRule, error) {
	const bobi = 100330838
	equipment, err := items.Source(bobi)
	if err != nil {
		return nil, err
	}
	pet := readItemScript(equipment.Text)
	species := pet.number("creature species", 0, math.MaxUint32, true)
	if pet.err != nil || species != 258 || pet.get("equipment type") != "\x60[creature]\x60 0" || pet.get("sub type") != "0" {
		return nil, fmt.Errorf("golden Bobi equipment contract differs")
	}
	creatures, err := a.ReadIndex("creature")
	if err != nil {
		return nil, err
	}
	speciesPath := creatures[int64(species)]
	if speciesPath == "" {
		return nil, fmt.Errorf("golden Bobi creature source is unavailable")
	}
	speciesPath, err = resourcePath(speciesPath, "creature")
	if err != nil {
		return nil, err
	}
	source := questSource{archive: a, texts: make(map[string]string), hashes: make(map[string]string)}
	speciesText, err := source.text(speciesPath)
	if err != nil {
		return nil, err
	}
	creature := readItemScript(speciesText)
	start, maximum := creature.number("start level", 0, 255, true), creature.number("max level", 0, 255, true)
	permission, overskill := creature.number("permission level", 0, 255, true), creature.number("learn overskill level", 0, 255, true)
	if start != 1 || maximum != 20 || permission != 1 || overskill != 20 ||
		creature.number("evolution quest", 0, math.MaxUint32, true) != 0 ||
		creature.number("evolution creature id", 0, math.MaxUint32, true) != 0 ||
		creature.number("evolution level", 0, math.MaxUint32, true) != 0 || creature.err != nil {
		return nil, fmt.Errorf("unsupported golden Bobi growth or evolution")
	}
	const common = "creature/common/creaturecommon.cre"
	commonText, err := source.text(common)
	if err != nil {
		return nil, err
	}
	profile := readItemScript(commonText)
	spend := profile.number("second per fill point spend", 0, 2147483, true)
	recover := profile.number("second per fill point charge", 0, 2147483, true)
	limit := profile.number("MAX fill point", 0, 100, true)
	automatic := profile.number("fill point to eat feed", 0, limit, true)
	if profile.err != nil || spend == 0 || recover == 0 || limit == 0 {
		return nil, fmt.Errorf("invalid native creature clock definition")
	}
	const expPath = "creature/exptable.tbl"
	exp, err := source.text(expPath)
	if err != nil {
		return nil, err
	}
	parser := itemScript{}
	thresholds := parser.numbers(exp)
	if parser.err != nil || len(thresholds) != 55 || thresholds[0] == 0 {
		return nil, fmt.Errorf("invalid creature experience table")
	}
	for i, n := range thresholds {
		if n > math.MaxInt32 || i > 0 && thresholds[i-1] >= n {
			return nil, fmt.Errorf("non-increasing creature experience table")
		}
	}
	out := &storage.PetGrowthRule{Thresholds: thresholds}
	out.Pet.PetCareRule = storage.PetCareRule{TemplateID: bobi, EquipmentHash: equipment.Hash, SpendSeconds: spend, RecoverSeconds: recover, MaximumSatiety: byte(limit), MaximumLevel: byte(maximum)}
	out.Pet.SpeciesID, out.Pet.StartLevel, out.Pet.PermissionLevel, out.Pet.OverskillLevel = species, byte(start), byte(permission), byte(overskill)
	out.Pet.AutomaticFeedThreshold = byte(automatic)
	for _, entry := range []struct {
		path string
		dst  *string
	}{{speciesPath, &out.Pet.SpeciesHash}, {common, &out.Pet.CommonHash}, {expPath, &out.TableHash}} {
		*entry.dst, err = source.rawHash(entry.path)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
