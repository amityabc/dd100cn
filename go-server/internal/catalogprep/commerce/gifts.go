package commerce

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

type Gifts struct {
	Definitions map[uint32]storage.GiftDefinition
	Templates   map[uint32]*ItemTemplate
	SpecialIDs  []uint32
	Unavailable map[uint32]string
}

type giftVariant struct {
	id, model uint32
	period    int64
}

type giftCompiler struct {
	Gifts
	items       *Items
	visited     map[uint32]bool
	active      map[uint32]bool
	attachments map[uint32]byte
	special     map[giftVariant]*storage.GiftSpecial
	cases       map[uint32]map[byte]uint16
	questTicket func(ItemSource) (*ItemTemplate, error)
	err         error
}

func BuildGifts(items *Items, roots []uint32, questTicket ...func(ItemSource) (*ItemTemplate, error)) (*Gifts, error) {
	g := &giftCompiler{
		Gifts: Gifts{Definitions: map[uint32]storage.GiftDefinition{}, Templates: map[uint32]*ItemTemplate{}, Unavailable: map[uint32]string{}},
		items: items, visited: map[uint32]bool{}, active: map[uint32]bool{}, attachments: map[uint32]byte{}, special: map[giftVariant]*storage.GiftSpecial{},
	}
	if len(questTicket) > 0 {
		g.questTicket = questTicket[0]
	}
	for _, id := range roots {
		g.visit(id)
		if g.err != nil {
			return nil, g.err
		}
	}
	special := map[uint32]bool{}
	for k := range g.special {
		special[k.id] = true
	}
	g.SpecialIDs = sortedIDs(special)
	return &g.Gifts, nil
}

func (g *giftCompiler) visit(id uint32) {
	if g.visited[id] || g.err != nil {
		return
	}
	if len(g.visited) >= 32768 {
		g.err = fmt.Errorf("gift dependency preparation exceeds its template budget")
		return
	}
	g.visited[id], g.active[id] = true, true
	defer delete(g.active, id)
	if g.items.stackable[int64(id)] == "" && g.items.equipment[int64(id)] == "" {
		g.Unavailable[id] = "Gift template is absent from the resource indices"
		return
	}
	r, err := g.items.Source(id)
	if err != nil {
		g.err = err
		return
	}
	t, err := r.Gift()
	if err != nil && g.questTicket != nil {
		if ticket, ticketError := g.questTicket(r); ticketError == nil && ticket != nil {
			t, err = ticket, nil
		}
	}
	if err != nil {
		g.Unavailable[id] = err.Error()
		return
	}
	g.Templates[id] = t
	attach := cashField(r, "attach type")
	if n, ok := giftAttachment(attach, r.Equipment); ok {
		g.attachments[id] = n
	} else {
		g.Unavailable[id] = "Gift reward requires an unsupported attachment or sealing state: " + attach
	}
	if r.Equipment {
		return
	}
	typ := cashField(r, "stackable type")
	if !giftSelectionType(typ) && !giftRandomType(typ) && typ != giftFixedType {
		return
	}
	resolve := func(id, count uint32, reason string) storage.GiftReward {
		return g.reward(id, count, reason, giftSelectionType(typ))
	}
	d, err := parseGiftBox(t, r.Text, typ, resolve, g.specialReward)
	if err != nil {
		g.Unavailable[id] = err.Error()
		return
	}
	g.Definitions[id] = d
}

func giftAttachment(raw string, equipment bool) (byte, bool) {
	switch raw {
	case "", "`[free]`":
		return 0, true
	case "`[trade]`":
		return 1, true
	case "`[avatar trade]`":
		return 1, equipment
	case "`[trade delete]`":
		return 2, true
	case "`[sealing]`":
		return 3, true
	case "`[sealing trade]`":
		return 4, equipment
	case "`[account]`":
		return 5, true
	case "`[trade limit]`":
		return 7, !equipment
	case "`[trade limit account]`":
		return 8, !equipment
	}
	return 0, false
}

type giftResolve func(id, count uint32, reason string) storage.GiftReward
type giftSpecialResolve func(id, count, model uint32, period int64) storage.GiftReward

func giftReward(id, count uint32) storage.GiftReward {
	return storage.GiftReward{TemplateID: id, Count: count, Upgrades: []storage.GiftUpgrade{}}
}

func rejectedReward(id, count uint32, reason string) storage.GiftReward {
	r := giftReward(id, count)
	r.UnavailableReason = &reason
	return r
}

func (g *giftCompiler) reward(id, count uint32, reason string, selection bool) storage.GiftReward {
	if reason != "" {
		return rejectedReward(id, count, reason)
	}
	r := giftReward(id, count)
	switch id {
	case 0, 1, 42:
		r.Wallet = &struct{ Gold, Coin, Cera int64 }{}
		if id == 0 {
			r.Wallet.Gold = int64(count)
		} else {
			r.TemplateID, r.Wallet.Coin = 1, int64(count)
		}
		return r
	case 2:
		return rejectedReward(id, count, "Gift virtual reward requires its verified wallet policy")
	}
	attach, found := g.attachments[id]
	if !found {
		if g.active[id] {
			return rejectedReward(id, count, "Gift dependency cycle cannot create an ordinary reward")
		}
		if len(g.active) >= 64 {
			return rejectedReward(id, count, "Gift dependency chain exceeds its preparation depth budget")
		}
		g.visit(id)
		attach, found = g.attachments[id]
	}
	if found {
		var err error
		r.Template, err = json.Marshal(g.Templates[id])
		if err != nil {
			g.err = err
		}
		r.AttachType = attach
		return r
	}
	r = g.specialReward(id, count, 0, 0)
	if !selection && r.SpecialProduct != nil {
		var t gm.SpecialTemplate
		if err := json.Unmarshal(r.SpecialProduct.Template, &t); err != nil {
			g.err = err
		}
		if t.Kind == 0 {
			if r.SpecialProduct.Abilities[0]&t.UsableJobs == 0 {
				return rejectedReward(id, count, "Direct avatar reward has no legal native default ability zero")
			}
			var ability byte
			r.FixedAbility = &ability
		}
	}
	return r
}

var giftAvatarType = regexp.MustCompile("^`\\[[^\\]]+ avatar\\]`(?:\\s+[0-9]+)?$")
var giftAvatarModel = regexp.MustCompile("\\s*([0-9]+)\\s+[0-9]+\\s+[0-9]+\\s+[0-9]+\\s+[0-9]+\\s+[0-9]+\\s+[0-9]+(?:\\s+`\\[[^\\]]+\\]`)*")

func permanentThirdAvatar(text string) bool {
	rows := giftAvatarModel.FindAllStringSubmatchIndex(text, -1)
	if len(rows) < 3 || text[rows[2][2]:rows[2][3]] != "0" {
		return false
	}
	previous := 0
	for _, row := range rows {
		if row[0] != previous {
			return false
		}
		previous = row[1]
	}
	return strings.TrimSpace(text[previous:]) == ""
}

func (g *giftCompiler) specialReward(id, count, model uint32, period int64) storage.GiftReward {
	if g.active[id] {
		return rejectedReward(id, count, "Gift dependency cycle cannot create a special reward")
	}
	if len(g.active) >= 64 {
		return rejectedReward(id, count, "Gift special dependency chain exceeds its preparation depth budget")
	}
	g.visit(id)
	key := giftVariant{id, model, period}
	r := giftReward(id, count)
	if p := g.special[key]; p != nil {
		r.AttachType, r.SpecialProduct = p.AttachType, p
		return r
	}
	if len(g.special) >= 32768 {
		g.err = fmt.Errorf("gift special product preparation exceeds its variant budget")
		return r
	}
	if g.items.stackable[int64(id)] == "" && g.items.equipment[int64(id)] == "" {
		return rejectedReward(id, count, "Gift special reward is absent from the resource indices")
	}
	src, err := g.items.Source(id)
	if err != nil {
		g.err = err
		return r
	}
	if model == 0 {
		typ := cashField(src, "equipment type")
		if giftAvatarType.MatchString(typ) {
			table := cashField(src, "avatar type select")
			defaultAura := (typ == "`[aurora avatar]`" || typ == "`[aurora avatar]` 0") && !giftHasField(src.Text, "avatar type select")
			if !permanentThirdAvatar(table) && !defaultAura {
				return rejectedReward(id, count, "Direct avatar reward lacks an explicitly permanent third creation model")
			}
			model = 3
		}
	}
	if g.cases == nil && cashField(src, "ability case index") != "" {
		ability, err := g.items.source.ReadText("skill/abilitydatas.dat", scriptLimit)
		if err != nil {
			g.err = err
			return r
		}
		g.cases, err = gm.AbilityCases(ability)
		if err != nil {
			g.err = err
			return r
		}
	}
	p, err := src.SpecialGift(g.cases, model)
	if err == nil && (period < math.MinInt32 || period > math.MaxInt32 || period != 0 && p.ResourcePeriod != 0 && p.ResourcePeriod != period) {
		err = fmt.Errorf("Gift special reward has conflicting relative period declarations")
	}
	if err != nil {
		reason := err.Error()
		if ordinary := g.Unavailable[id]; ordinary != "" {
			reason = ordinary + "; " + reason
		}
		return rejectedReward(id, count, reason)
	}
	if period != 0 {
		p.ResourcePeriod = period
	}
	raw, err := json.Marshal(p.Template)
	if err != nil {
		g.err = err
		return r
	}
	p.Abilities = nonNilAbilities(p.Abilities)
	r.AttachType = p.AttachType
	r.SpecialProduct = &storage.GiftSpecial{Template: raw, Abilities: p.Abilities, Model: p.Model, ResourcePeriod: p.ResourcePeriod, AttachType: p.AttachType, InitialPetName: p.InitialPetName}
	g.special[key] = r.SpecialProduct
	return r
}

func nonNilAbilities(values map[byte]uint16) map[byte]uint16 {
	if values == nil {
		return map[byte]uint16{}
	}
	return values
}

func giftHasField(text, name string) bool {
	fields, _ := gm.ItemFields(text)
	for _, f := range fields {
		if f.Name == name {
			return true
		}
	}
	return false
}
