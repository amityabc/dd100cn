package quest

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/questwire"
)

const (
	boardHash   = "7EDF83117E63E07B22C7BE24A34D96E0A0F9602509A9697C8484370538ED996E"
	mintaiHash  = "82FFA8FFB03B05323AEE3C0804EFC030CEC4D51A3BCB5521DEABBDD59F9E850B"
	minetHash   = "029A698EA133C1A2328B894A9D7053383EAF72966FAD42730DE73970B4E2E933"
	ronHash     = "9F156DC4A87870DCEA801570E50BE7FF4513514EFD667E561D0CC7123410A6DF"
	grandisHash = "ACAEFB908BE7D1984E79F4CC8113629809C3829158E27903CC5315B4C52337A3"
)

var (
	ronSources = map[uint16]string{
		6300: "A81FA241B1C5C5E571CAB49A72D68CCAC5CC55A5BF20C8496E62D39B3D707D46",
		6302: "71103C9BF362E867CBDBC619CE5FEFD23A841E800524DFE6ADCF770FE0958B88",
	}
	grandisSources = map[uint16]string{
		12160: "817FF039E6EEAA0A6ABAF897C199E2A6374C457102852563B3B8E21BE8A9A448",
		12166: "F706780FE81BBB5A740B47B53388029187E6E254DC2ED915BC2F73A5D261DE75",
		12303: "CA3219208C27EECFA95BFCA20AE84B9B56AF3E6DD57C62A7AB60540477105316",
		12311: "9F7B9EE364294B9D8B6C26336CB7C0EB86EEE9F63DED4511174BA7555A386F3B",
		12312: "752DD7BE8076E06FF798CADF7B1E5015D47317E0FAD62EBAFA6A816D43C0BAC7",
		12315: "287878AC4493FF718E7DEEFFD3927292FA0EABEEB9956BAABC64858EF0B10980",
		12323: "DF4BCECC7418E4BB027E455D865EA9FAA1FAAF2A3B6B45DF6CAB53BA6FE36931",
		12384: "11031D74B61D9C1C4282D79E29FD05B990E8F8052C888E4E81987FB7B829410A",
		12401: "8916F91F41592D182B0746DE0C0C5583E93B79544CA6316B4672389E2CF06470",
	}
)

func (s *questSource) prepareNPCRange(q *questwire.Quest) error {
	if anyOf(q.Id, 3759, 3832) {
		return s.prepareChildRange(q)
	}
	npc := uint32(q.Data[0])
	town, area := uint16(40), uint16(3)
	expected, npcPath := minetHash, "npc/rothon.npc"
	var hiddenSources map[uint16]string
	switch npc {
	case 15:
		town, area, expected, npcPath = 39, 0, boardHash, ""
	case 39:
		town, area, expected, npcPath = 43, 0, mintaiHash, ""
	case 100000091:
		town, expected, npcPath, hiddenSources = 54, ronHash, "", ronSources
	case 100000374:
		expected, npcPath, hiddenSources = grandisHash, "", grandisSources
	}
	hiddenHash, npcHash, err := s.checkVisibility(npc, expected, npcPath, hiddenSources)
	if err != nil {
		return err
	}
	var target *questwire.NpcTarget
	switch npc {
	case 100000374:
		target, err = s.freezeGrandis()
	default:
		target, err = s.freezeTarget(npc, town, area)
	}
	if err != nil {
		return err
	}
	if npc == 100000091 && (target.X != 573 || target.Y != 88) {
		return fmt.Errorf("Ron moved outside the confirmed scene")
	}
	switch {
	case q.Id == 3162:
		q.Hash = hash(strings.Join([]string{q.Hash, target.ResourceHash, hiddenHash, npcHash}, "\n"))
	case hiddenSources == nil:
		target.ResourceHash = hash(strings.Join([]string{target.ResourceHash, hiddenHash, npcHash}, "\n"))
		q.Hash = hash(q.Hash + "\n" + target.ResourceHash)
	default:
		visibilityHash, err := s.visibilityHash(npc, hiddenSources)
		if err != nil {
			return err
		}
		target.Visibility = &questwire.NpcVisibility{Quest: q.Id, ResourceHash: visibilityHash}
		target.ResourceHash = hash(strings.Join([]string{target.ResourceHash, hiddenHash, npcHash, visibilityHash}, "\n"))
		q.Hash = hash(q.Hash + "\n" + target.ResourceHash)
	}
	q.NpcRangeTarget = target
	return nil
}

var (
	visibleBlock = regexp.MustCompile("(?is)\\[npc visibility\\](.*?)\\[/npc visibility\\]")
	visibleNPCs  = regexp.MustCompile("(?is)\\[npc\\](.*?)\\[/npc\\]")
)

func (s *questSource) visibilityHash(npc uint32, sources map[uint16]string) (string, error) {
	number := strconv.FormatUint(uint64(npc), 10)
	digits := regexp.MustCompile("(?:^|[^0-9])" + number + "(?:[^0-9]|$)")
	found := make(map[uint16]string, len(sources))
	for id, path := range s.paths {
		text, err := s.text(path)
		if err != nil {
			return "", err
		}
		if !strings.Contains(text, number) {
			continue
		}
		value := script{text: text}
		if value.section("visible npc") == number || value.err != nil {
			return "", fmt.Errorf("unknown direct visibility source for NPC %d", npc)
		}
		relevant := slices.ContainsFunc(visibleBlock.FindAllStringSubmatch(text, -1), func(block []string) bool {
			return slices.ContainsFunc(visibleNPCs.FindAllStringSubmatch(block[1], -1), func(row []string) bool {
				return digits.MatchString(row[1])
			})
		})
		if !relevant {
			continue
		}
		expected := sources[uint16(id)]
		if id <= 0 || id >= 30000 || expected == "" {
			return "", fmt.Errorf("unconfirmed visibility source quest %d for NPC %d", id, npc)
		}
		sum, err := s.rawHash(path)
		if err != nil {
			return "", err
		}
		if sum != expected {
			return "", fmt.Errorf("changed visibility source quest %d for NPC %d", id, npc)
		}
		found[uint16(id)] = expected
	}
	if len(found) != len(sources) {
		return "", fmt.Errorf("incomplete visibility sources for NPC %d", npc)
	}
	var rows []string
	for _, id := range slices.Sorted(maps.Keys(found)) {
		rows = append(rows, fmt.Sprintf("%d:%s", id, found[id]))
	}
	return hash(strings.Join(rows, "\n")), nil
}

var phaseMap = regexp.MustCompile("(?i)\x60([^\x60]+\\.map)\x60")
var grandisPhase = regexp.MustCompile("(?i)^\\s*\\[condition\\]\\s*\x60\\[completed quest\\]\x60\\s+12301\\s*\\[phase Index\\]\\s*2\\s*$")

func (s *questSource) freezeGrandis() (*questwire.NpcTarget, error) {
	text, body, townHash, err := s.townArea(40, 3)
	if err != nil {
		return nil, err
	}
	area, town := script{text: body}, script{text: text}
	phase := area.section("phase")
	paths := phaseMap.FindAllStringSubmatch(phase, -1)
	if area.err != nil || len(paths) < 3 || strings.TrimSpace(phaseMap.ReplaceAllString(phase, "")) != "" {
		return nil, fmt.Errorf("malformed Grandis phase map list")
	}
	matching := 0
	for _, shift := range town.blocks("phase shift") {
		if grandisPhase.MatchString(shift) {
			matching++
		}
	}
	if matching != 1 {
		return nil, fmt.Errorf("Grandis requires completed 12301 town phase 2")
	}
	target, err := s.freezeMapTarget(100000374, 40, 3, paths[2][1], townHash)
	if err != nil {
		return nil, err
	}
	if target.X != 515 || target.Y != 102 {
		return nil, fmt.Errorf("Grandis moved outside the confirmed scene")
	}
	return target, nil
}

func (s *questSource) questText(id uint16) (string, error) {
	path := s.paths[int64(id)]
	if path == "" {
		return "", fmt.Errorf("unknown child range dependency %d", id)
	}
	return s.text(path)
}

func (s *questSource) prepareChildRange(q *questwire.Quest) error {
	parent, previous := uint16(3830), uint16(3831)
	npc, town, x, y := uint32(344), uint16(22), uint16(681), uint16(241)
	expected := "9BE9300B3013AAFED4A87D2B1D5321D35F7EB3DE5515E8B20A0AA77154A8F209"
	visibility := []uint16{6359, 12119}
	if q.Id == 3759 {
		parent, previous = 3698, 3758
		npc, town, x, y = 78, 14, 1397, 210
		expected = "D4D3C08C919823F7B1293974F59C04FE0D34C9B43E1E2229A8B03D6910212A79"
		visibility = []uint16{12121, 12122}
	}
	parentText, err := s.questText(parent)
	if err != nil {
		return err
	}
	previousText, err := s.questText(previous)
	if err != nil {
		return err
	}
	p, pErr := parseOrdinary(parent, parentText, hash(parentText))
	before, beforeErr := parseOrdinary(previous, previousText, hash(previousText))
	if pErr != nil || beforeErr != nil || p.Kind != questClear || p.Subtype != 2 || p.Operations.Grade != "epic" ||
		!slices.Equal(p.Children, []uint16{previous, q.Id}) || before.Parent != parent || before.Kind != meetNPC ||
		q.Parent != parent || q.Operations.Grade != "sub" || len(q.DestinationDungeons) != 0 || len(q.Children) != 0 {
		return fmt.Errorf("NPC child range parent or sibling changed")
	}
	hiddenHash, npcHash, err := s.checkVisibility(npc, expected, "", nil)
	if err != nil {
		return err
	}
	target, err := s.freezeTarget(npc, town, 1)
	if err != nil {
		return err
	}
	if target.X != x || target.Y != y {
		return fmt.Errorf("NPC child range placement changed")
	}
	evidence := []string{target.ResourceHash, p.Hash, before.Hash, hiddenHash, npcHash}
	for _, id := range visibility {
		text, err := s.questText(id)
		if err != nil {
			return err
		}
		action := "hide"
		if id == 12119 {
			action = "delete"
		}
		declarations := regexp.MustCompile("(?ms)^\\[npc visibility\\]\\s*\\[npc\\]\\s*([0-9\\s]+)\\s*\\[/npc\\]\\s*\\[condition\\]\\s*\x60\\[clear\\]\x60\\s*\\[visibility\\]\\s*\x60\\[" + action + "\\]\x60\\s*\\[/npc visibility\\]")
		values := script{}
		if !slices.ContainsFunc(declarations.FindAllStringSubmatch(text, -1), func(row []string) bool {
			return slices.Contains(values.numbers(row[1]), int(npc))
		}) || values.err != nil {
			return fmt.Errorf("NPC child range visibility policy changed")
		}
		evidence = append(evidence, hash(text))
	}
	target.ResourceHash = hash(strings.Join(evidence, "\n"))
	q.Hash = hash(q.Hash + "\n" + target.ResourceHash)
	q.NpcRangeTarget = target
	s.bindings[q.Id] = &ChildRangeBinding{Parent: parent, ParentHash: p.Hash, PreviousChild: previous}
	return nil
}
