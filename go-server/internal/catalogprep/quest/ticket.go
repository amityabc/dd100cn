package quest

import (
	"fmt"
	"slices"
	"strings"

	"s9a1/server/internal/questwire"
)

type giftField struct{ name, value string }

func giftFields(text string) ([]giftField, error) {
	text = strings.ReplaceAll(text, "\r", "")
	type marker struct {
		name       string
		start, end int
	}
	var markers []marker
	for at := 0; at < len(text); at++ {
		switch text[at] {
		case '\x60':
			end := strings.IndexByte(text[at+1:], '\x60')
			if end < 0 {
				return nil, fmt.Errorf("unterminated PVF resource string")
			}
			at += 1 + end
		case '[':
			if at > 0 && text[at-1] != '\n' {
				continue
			}
			line := strings.IndexByte(text[at:], '\n')
			if line < 0 {
				line = len(text) - at
			}
			close := strings.IndexByte(text[at:at+line], ']')
			if close < 0 || strings.TrimSpace(text[at+close+1:at+line]) != "" {
				continue
			}
			markers = append(markers, marker{text[at+1 : at+close], at, min(len(text), at+line+1)})
			at += line - 1
		}
	}
	fields := make([]giftField, len(markers))
	for i, m := range markers {
		end := len(text)
		if i+1 < len(markers) {
			end = markers[i+1].start
		}
		fields[i] = giftField{m.name, strings.TrimSpace(text[m.end:end])}
	}
	return fields, nil
}

func ticketName(fields []giftField) (string, error) {
	variant, seen := false, false
	name := ""
	for _, f := range fields {
		switch f.name {
		case "change depend on grow job", "/change depend on grow job":
			opening := f.name[0] != '/'
			if variant == opening {
				return "", fmt.Errorf("invalid quest class display variant")
			}
			variant = opening
		case "name":
			if !variant {
				if seen {
					return "", fmt.Errorf("ambiguous top-level quest name")
				}
				seen, name = true, f.value
			}
		}
	}
	if variant {
		return "", fmt.Errorf("unclosed quest class display variant")
	}
	return strings.Trim(name, "\x60"), nil
}

var ticketFields = strings.Split("grade|difficulty|gold multiple|npc index|complete npc index|job|grow type|level|type|sub type|int data|exception quest|reward type|reward int data|reward selection int data|name|depend message|condition message|solve message|pre required quest|exception filter|ignore quest level 4 exp|job change quest|grow number|alternative npc index|collision quest|target character|relation quest|dungeon info|enemy reward item|multi condition min clear|multi condition direct check|attribute|reward item trade instance|creature kind|creature level|myname|mail time|mail title|mail contents|go guide|substitutive names|sub reward|pvp private expose|visible npc|monster reward item|name depend on job|depend message depend on job|condition message depend on job|solve message depend on job|account quest|imagine level|main quest|starting epic quest to new job|quest item noti|display send item|display recv item|noti message|dialog|dialog depend on job|npc visibility|npc|condition|visibility|go area guide|auto accept|get quest item noti|item index|change depend on grow job|character|male dialog|add episode check quest|reset config value on change module|non target character|question info|pre required quest answer|male depend message|male solve message|exposed by npc|isnotclear levelup system|condition quest hide|show quest on event|depend give item|quest clear noti|image info|dialog show time|daily random set|next relation quest|episode town use|quest title noti|accept ani path|accept ani fade option|solve video|clear ani path|clear ani fade option|casecade clear|synchronization account quest|cant giveup|exp random set|need to reset quest|npc visivility not revert|check skill on clear|check count|limit showing msg|condition data|limit dungeon index|string data|hardcore mode disable|send postal|item count|sender|message|hardcore mode only|impossible achievement quest|event|schedule|change dungeon info message|not use guide tag|first exposed by npc|appear map|searching information|favor quest open condition|npc favor|show click on complete npc|clear reward item|mail enclose|quest point|disable quest clear|is account quest|account collision quest|event quest|expertjob level|is event|is account|expert job type", "|")

func parseTicket(id uint16, text, rawHash string) (questwire.Ticket, error) {
	s := script{text: text}
	q := questwire.Ticket{Hash: rawHash, Grade: s.tag("grade"), ObjectiveType: s.tag("type"), RewardType: s.tag("reward type")}
	fields, err := giftFields(text)
	if err != nil {
		return q, err
	}
	if !anyOf(q.Grade, "", "normal", "side", "sub", "epic", "achievement", "common unique", "realization", "system", "episode") {
		return q, fmt.Errorf("unsupported ticket quest grade")
	}
	q.Criterion = s.eligibility(id, rawHash, true)
	q.AdvancementChain = s.scalar("job change quest", 0)
	if q.Grow < -1 || q.Grow > 5 || q.GrowNumber < -1 || q.GrowNumber > 3 || q.AdvancementChain < 0 || q.AdvancementChain > 4 {
		s.fail("unsupported ticket quest character restriction")
	}
	for _, group := range q.Required {
		if len(group) == 0 {
			if id != 675 || len(q.Required) != 1 || rawHash != "B8728F958EF110C37B3D0E3E27DB32FD959CEE539A0FF78699A4DEF85E8799E6" {
				s.fail("empty ticket quest prerequisites")
			}
			q.Required = nil
			break
		}
	}
	if q.ObjectiveType == "quest clear" {
		q.Children = s.children(s.scalar("sub type", -1), s.data("int data"))
	}
	for _, tag := range []string{"disable quest clear", "impossible achievement quest", "isNotClear LevelUp System", "account quest", "is account quest", "account collision quest", "event quest", "depend give item", "clear reward item", "expertjob level", "synchronization account quest", "hardcore mode only", "favor quest open condition", "npc favor", "send postal", "sub reward"} {
		if len(s.blocks(tag)) != 0 {
			q.ProhibitedFlags = append(q.ProhibitedFlags, tag)
		}
	}
	q.ConditionQuestHide = s.ids(s.data("condition quest hide"))
	const knownHide4873 = "AEB2ED09305E19E245ABF88E533AFBC53F6D412FFDEDAAECE630385BBEB1AF03"
	const knownHide4875 = "81EF0DBCCDD14CA4626F3C21F78E6ADC775F919D6AB342DCF26FEB461C57E1E7"
	if len(s.blocks("condition quest hide")) != 0 &&
		(!slices.Equal(q.ConditionQuestHide, []uint16{3156}) || !(id == 4873 && rawHash == knownHide4873 || id == 4875 && rawHash == knownHide4875)) {
		q.ProhibitedFlags = append(q.ProhibitedFlags, "condition quest hide")
	}
	for _, tag := range []string{"event", "is event", "is account"} {
		if s.scalar(tag, 0) != 0 {
			q.ProhibitedFlags = append(q.ProhibitedFlags, tag)
		}
	}
	for _, tag := range []string{"creature kind", "expert job type"} {
		if s.scalar(tag, -1) >= 0 {
			q.ProhibitedFlags = append(q.ProhibitedFlags, tag)
		}
	}
	if s.scalar("exposed by npc", 1) == 0 {
		q.ProhibitedFlags = append(q.ProhibitedFlags, "exposed by npc")
	}
	if s.section("reward selection int data") != "" {
		q.ProhibitedFlags = append(q.ProhibitedFlags, "reward selection int data")
	}
	reward := s.section("reward int data")
	switch q.RewardType {
	case "item":
		q.Rewards, q.RewardRows, q.GoldAward = s.items(reward, false, true)
	case "awakening type":
		q.AwakeningAward = s.awakening(q.Criterion, q.Grade, 0, true)
	case "slot expansion":
		args := s.numbers(reward)
		if len(args) != 1 || args[0] < 0 || args[0] > 2 {
			s.fail("unsupported slot expansion ticket reward")
		}
	default:
		empty := q.RewardType == "" && slices.Equal(s.numbers(reward), []int{0, 0})
		if !empty && (q.RewardType != "" || reward != "") {
			q.ProhibitedFlags = append(q.ProhibitedFlags, "reward type: "+q.RewardType)
		}
	}
	if q.ObjectiveType == "question" {
		options, ok := matchedRows(s.section("question info"), stringRE)
		if !ok || len(options) < 3 || len(options) > 257 {
			s.fail("invalid ticket quest question")
		} else {
			q.QuestionChoices = byte(len(options) - 2)
		}
	}
	q.Name, err = ticketName(fields)
	if err != nil {
		return q, err
	}
	q.LegacyUpperAwakening = legacyUpper(id, rawHash)
	q.EquipmentSlotQuest = anyOf(s.tag("attribute"), "slot expansion", "magic stone")
	q.SkillEffects = s.skillEffects()
	q.CascadeClear = distinct(s.ids(s.data("casecade clear")))
	q.Supported = s.err == nil
	for _, f := range fields {
		if !strings.HasPrefix(f.name, "/") && !slices.Contains(ticketFields, strings.ToLower(f.name)) {
			q.Supported = false
		}
	}
	q.Equipment = q.EquipmentSlotQuest && q.Supported && len(q.ProhibitedFlags) == 0 && q.Grade == "side" &&
		q.Parent == 0 && len(q.Children) == 0 && len(q.ConditionQuestHide) == 0 && q.AdvancementChain == 0 &&
		q.AwakeningAward == nil && len(q.SkillEffects) == 0 && anyOf(q.RewardType, "item", "slot expansion")
	return q, s.err
}
