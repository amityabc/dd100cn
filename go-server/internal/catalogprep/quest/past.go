package quest

import "s9a1/server/internal/questwire"

func parsePast(id uint16, text, rawHash string) *questwire.PastQuest {
	s := script{text: text}
	grade := s.tag("grade")
	if !anyOf(grade, "epic", "sub") {
		return nil
	}
	for _, tag := range []string{"account quest", "is account quest", "account collision quest", "event quest", "check skill on clear"} {
		if len(s.blocks(tag)) != 0 {
			return nil
		}
	}
	if s.scalar("event", 0) != 0 || s.scalar("is event", 0) != 0 || s.scalar("is account", 0) != 0 ||
		s.scalar("creature kind", -1) >= 0 || s.scalar("expert job type", -1) >= 0 ||
		s.scalar("exposed by npc", 1) == 0 || s.scalar("job change quest", 0) != 0 ||
		!anyOf(s.tag("reward type"), "", "item") || len(s.data("level")) == 0 {
		return nil
	}
	c := s.eligibility(id, rawHash, true)
	if grade == "sub" && c.Parent == 0 || c.Grow < -1 || c.Grow > 5 || c.GrowNumber < -1 || c.GrowNumber > 3 {
		return nil
	}
	for _, group := range c.Required {
		if len(group) == 0 {
			return nil
		}
	}
	choices := byte(0)
	objective := s.tag("type")
	if objective == "quest clear" {
		c.Children = s.children(s.scalar("sub type", -1), s.data("int data"))
	} else if objective == "question" {
		choices = s.question(id, grade, s.scalar("sub type", -1), s.data("int data"))
	}
	if s.err != nil {
		return nil
	}
	return &questwire.PastQuest{Criterion: c, Level: c.MinimumLevel, Awakening: c.GrowNumber, Epic: grade == "epic", Hash: rawHash, QuestionChoices: choices}
}
