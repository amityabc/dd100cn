package gm

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type ItemSource struct {
	Id         uint32
	Path       string
	Equipment  bool
	Text, Hash string
	Raw        []byte
	resolve    func(int32) (string, error)
}

func ReadItemSource(a ItemArchive, id uint32, path string, equipment bool) (ItemSource, error) {
	text, err := a.ReadText(path, memberBudget)
	if err != nil {
		return ItemSource{}, err
	}
	raw, err := a.Read(path, memberBudget)
	if err != nil {
		return ItemSource{}, err
	}
	digest := sha256.Sum256(raw)
	return ItemSource{id, path, equipment, text, strings.ToUpper(hex.EncodeToString(digest[:])), raw, a.ResolveString}, nil
}

func (r ItemSource) Ordinary() (*ItemTemplate, error) {
	s, err := parseScript(r.Text)
	if err != nil {
		return nil, err
	}
	return parseOrdinary(r.Id, r.Equipment, s, r.Hash, r.Raw, r.resolve)
}

func (r ItemSource) Gift() (*ItemTemplate, error) {
	return r.GiftFor(GiftDefault)
}

type GiftMode byte

const (
	GiftDefault GiftMode = iota
	GiftShop
	GiftBattlePass
	GiftMonster
	// GiftQuestTicket is used after the native quest ticket policy validates
	// the action and its arguments.
	GiftQuestTicket
)

func (r ItemSource) GiftFor(mode GiftMode) (*ItemTemplate, error) {
	s, err := parseScript(r.Text)
	if err != nil {
		return nil, err
	}
	return parseGiftMode(r.Id, r.Equipment, s, r.Hash, r.Raw, r.resolve, mode)
}

func (r ItemSource) GM() (*ItemTemplate, bool, error) {
	s, err := parseScript(r.Text)
	if err != nil {
		return nil, false, err
	}
	return parseItem(r.Id, r.Equipment, s.normalized(), r.Hash, r.Raw, r.resolve)
}

func (r ItemSource) Weight() (int64, error) {
	s, err := parseScript(r.Text)
	if err != nil {
		return 0, err
	}
	return parseWeight(s)
}

func (r ItemSource) SpecialGM(cases map[uint32]map[byte]uint16) (*SpecialProduct, error) {
	s, err := parseScript(r.Text)
	if err != nil {
		return nil, err
	}
	return parseSpecial(r.Id, s.normalized(), r.Hash, cases)
}

// SpecialGift selects an explicit native creation model for the matching item.
func (r ItemSource) SpecialGift(cases map[uint32]map[byte]uint16, model uint32) (*SpecialProduct, error) {
	s, err := parseScript(r.Text)
	if err != nil {
		return nil, err
	}
	return parseSpecialModel(r.Id, s, r.Hash, cases, model)
}

func AbilityCases(text string) (map[uint32]map[byte]uint16, error) { return parseCases(text) }

func SelectedAbilities(text string, cases map[uint32]map[byte]uint16) (map[byte]uint16, error) {
	s, err := parseScript(text)
	if err != nil {
		return nil, err
	}
	return selectedAbilitiesMode(s, cases, false)
}

type Field struct{ Name, Value string }

func ItemFields(text string) ([]Field, error) {
	s, err := parseScript(text)
	if err != nil {
		return nil, err
	}
	rows := make([]Field, 0, len(s.fields))
	for _, f := range s.fields {
		rows = append(rows, Field{f.name, f.value})
	}
	return rows, nil
}

func ItemField(text, name string) (string, error) {
	s, err := parseScript(text)
	if err != nil {
		return "", err
	}
	return s.section(name)
}
