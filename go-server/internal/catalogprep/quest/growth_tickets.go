package quest

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/questwire"
)

type ticketAction struct {
	kind   byte
	action uint32
}

var ticketActions = map[string]ticketAction{
	"first awaken clear": {0, 119}, "second awaken clear": {1, 122}, "third awaken clear": {2, 241},
	"any quest clear": {3, 130}, "quest clear": {4, 77}, "achievement quest clear": {5, 99},
	"all quest clear": {6, 101}, "assist quest clear": {7, 137}, "epic quest clear": {8, 181},
	"side quest clear": {9, 183}, "gm quest clear": {10, 171},
}
var ticketActionRE = regexp.MustCompile("^\x60\\[([^\\]]+)\\]\x60((?:\\s+\\d+)*)$")
var ticketItemFields = strings.Split("name|explain|flavor text|grade|rarity|usable job|attach type|minimum level|maximum level|cool time|icon|icon mark|stackable type|move wav|expiration date|action type|stack limit|use action packet|impossible contents|action usable place|item category|price|usable period|impossible jobs|unable use grow type|need material|purchase limit|daily purchase limit|value", "|")

// GiftQuestTicket admits a native quest-ticket source only after verifying its
// complete action, arguments, ownership and use qualifiers.
func GiftQuestTicket(src gm.ItemSource) (*gm.ItemTemplate, error) {
	if _, err := parseTicketItem(src.Id, src.Text, src.Hash, nil); err != nil {
		return nil, err
	}
	return src.GiftFor(gm.GiftQuestTicket)
}

func parseTicketItem(id uint32, text, hash string, raw json.RawMessage) (questwire.QuestTicketItem, error) {
	s := readItemScript(text)
	r := questwire.QuestTicketItem{Template: raw}
	row := ticketActionRE.FindStringSubmatch(s.get("action type"))
	if len(row) != 3 || !anyOf(s.get("stackable type"), "\x60[etc]\x60 0", "\x60[etc]\x60 1") {
		s.fail("unknown quest ticket action")
		return r, s.err
	}
	action, known := ticketActions[row[1]]
	if !known {
		s.fail("unknown quest ticket action")
		return r, s.err
	}
	v := s.numbers(row[2])
	shape := false
	switch action.kind {
	case 0:
		shape = slices.Equal(v, []uint32{1}) || slices.Equal(v, []uint32{1, 1})
	case 1:
		shape = slices.Equal(v, []uint32{0}) || slices.Equal(v, []uint32{0, 1})
	case 2:
		shape = slices.Equal(v, []uint32{0})
	case 3:
		shape = len(v) > 0 && len(v) <= 511 && len(distinct(v)) == len(v) &&
			!slices.ContainsFunc(v, func(id uint32) bool { return id == 0 || id >= 30000 })
	case 8:
		shape = len(v) == 1 && v[0] > 0 && v[0] <= 255
	default:
		shape = len(v) == 0
	}
	if !shape {
		s.fail("invalid quest ticket action arguments")
	}
	number := func(key string, fallback, max uint32) uint32 {
		if s.get(key) == "" {
			return fallback
		}
		return s.number(key, fallback, max, true)
	}
	minimum, maximum := number("minimum level", 1, 255), number("maximum level", 255, 255)
	if maximum == 0 || maximum < minimum {
		s.fail("invalid quest ticket level restriction")
	}
	number("use action packet", 0, 1)
	s.tags("impossible contents", false, "hardcore mode", "ban redeemitem", "gift")
	blockedBranches := s.numbers(s.get("unable use grow type"))
	denied := make([]struct{ Job, Grow byte }, 0, len(blockedBranches)/2)
	if len(blockedBranches)%2 != 0 {
		s.fail("incomplete quest ticket class restriction")
	} else {
		for row := range slices.Chunk(blockedBranches, 2) {
			if row[0] > 15 || row[1] > 5 {
				s.fail("invalid quest ticket class restriction")
			}
			denied = append(denied, struct{ Job, Grow byte }{byte(row[0]), byte(row[1])})
		}
	}
	s.purchaseMaterial(math.MaxUint32, true)
	for _, f := range s.fields {
		if !strings.HasPrefix(f.name, "/") && !slices.Contains(ticketItemFields, f.name) {
			s.fail("unsupported quest ticket field " + f.name)
		}
	}
	r.Definition = questwire.QuestTicketDefinition{
		Kind: action.kind, NativeAction: action.action, TemplateId: id, ResourceHash: hash,
		MinimumLevel: byte(minimum), MaximumLevel: byte(maximum), CooldownMilliseconds: number("cool time", 0, math.MaxUint32),
		UsablePlaces: s.tags("action usable place", true, "village", "seria room", "dont use assult"),
		UsableJobs:   s.jobs("usable job", false), ImpossibleJobs: s.jobs("impossible jobs", true),
		Arguments: v, UnableUseGrowTypes: denied,
	}
	return r, s.err
}
