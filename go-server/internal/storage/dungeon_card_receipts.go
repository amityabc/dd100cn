package storage

import (
	"encoding/json"
	"fmt"
	"strings"

	"s9a1/server/internal/savejson"
)

const dungeonCardLuckVersion = "SoloCardLuck/v1;preview-snapshot;successful-claim-debit;older-reference"

type frozenDungeonCard struct {
	Policy string
	Items  []dungeonCardItem
	Luck   *struct {
		Version, Hash string
		Items         []dungeonCardLuckItem
	}
	artifact *DungeonArtifact
	Artifact *struct {
		Template    json.RawMessage
		QualitySeed uint32
	}
}

type dungeonCardItem struct {
	Template          json.RawMessage
	StackCount, Count uint32
}

type dungeonCardLuckItem struct {
	Item   uint32
	Grade  int
	Rarity byte
}

type dungeonCardLuckProof struct {
	Version, Hash  string
	CharacterLevel byte
	Before, After  int64
	Debits         []struct {
		Item                   uint32
		Grade                  int
		Percent, Before, After int64
	}
}

func availableCardJSON(raw json.RawMessage) json.RawMessage {
	if string(raw) == "null" {
		return nil
	}
	return raw
}

func readFrozenDungeonCard(raw json.RawMessage, gold, cost uint32, paid bool) (frozenDungeonCard, error) {
	var card frozenDungeonCard
	if len(raw) == 0 {
		return card, nil
	}
	if json.Unmarshal(raw, &card) != nil || card.Policy == "" {
		return card, ErrConflict
	}
	if card.Artifact == nil {
		return card, nil
	}
	paidArtifact := card.Policy == "PVFNormalPaidArtifactCard/v1" ||
		strings.HasPrefix(card.Policy, "LocalFarmingChallengeResourcePremium/v1/") &&
			strings.HasSuffix(card.Policy, ";ordinary=PVFNormalPaidArtifactCard/v1")
	if len(card.Items) != 0 || paid && (!paidArtifact || cost == 0 || gold != 0) ||
		!paid && (card.Policy != "PVFFreeArtifactCard/v1" && card.Policy != "PVFFreeZeroGold/v1" || cost != 0) {
		return card, ErrConflict
	}
	var template specialPurchaseTemplate
	if err := json.Unmarshal(card.Artifact.Template, &template); err != nil {
		return card, ErrConflict
	}
	a := &DungeonArtifact{Template: card.Artifact.Template, QualitySeed: card.Artifact.QualitySeed, Durability: template.MaximumDurability}
	if _, err := a.definition(); err != nil {
		return card, err
	}
	card.artifact = a
	return card, nil
}

func (c frozenDungeonCard) itemCount() uint32 {
	if c.artifact != nil {
		return 1
	}
	return uint32(len(c.Items))
}

func validateDungeonCardClaimInput(l Lease, id uint16, p DungeonCardClaim, f frozenDungeonCard) error {
	var i struct {
		Version, Owner                          string
		Epoch                                   int64
		CharacterId                             uint16
		RunId, ResultKey                        string
		Member                                  byte
		Body                                    string
		Reward, SpecialReward, CompletionReward json.RawMessage
	}
	if json.Unmarshal([]byte(p.Input), &i) != nil || i.Owner != l.Owner || i.Epoch != l.Epoch || i.CharacterId != id ||
		!validAssetUUID(i.RunId) || i.Member != 0 || i.ResultKey != p.ResultKey ||
		!equalJSON(availableCardJSON(i.Reward), availableCardJSON(p.Reward)) ||
		!equalJSON(availableCardJSON(i.SpecialReward), p.Special) || !equalJSON(availableCardJSON(i.CompletionReward), p.Completion) {
		return ErrConflict
	}
	suffix := fmt.Sprintf(":v1:%s:%d:%d:%s", strings.ReplaceAll(l.Owner, "-", ""), l.Epoch, id, strings.ReplaceAll(i.RunId, "-", ""))
	if p.ResultKey != "dungeon-settlement"+suffix+":result" {
		return ErrConflict
	}
	if p.Paid {
		expected := "local100-paid-card-v2"
		if f.Policy == "" {
			expected = "local100-zero-price-empty-paid-v1"
		} else if f.artifact != nil {
			expected = "local100-paid-artifact-v1"
		}
		if i.Version != expected || i.Body != "AQA=" || p.Key != "dungeon-empty-paid-card"+suffix {
			return ErrConflict
		}
		return nil
	}
	expected := "local100-free-gold-v1"
	switch {
	case f.artifact != nil:
		expected = "local100-free-artifact-v1"
	case len(p.Special) != 0:
		expected = "local100-free-special/v1"
	case len(p.Completion) != 0:
		expected = "pvf-native-completion/v1"
	case len(f.Items) != 0:
		expected = "local100-free-card-v2"
	}
	if i.Version != expected || i.Body != "" || p.Key != "dungeon-free-card"+suffix {
		return ErrConflict
	}
	return nil
}

func dungeonCardResult(paid bool, gold, cost, count uint32, luck json.RawMessage) (string, error) {
	if paid {
		raw, err := savejson.Marshal(struct {
			Cost, Gold, ItemCount uint32
			Luck                  json.RawMessage `json:",omitempty"`
		}{cost, gold, count, luck})
		return string(raw), err
	}
	raw, err := savejson.Marshal(struct {
		Gold uint32
		Luck json.RawMessage `json:",omitempty"`
	}{gold, luck})
	return string(raw), err
}

func (c frozenDungeonCard) validateResult(result string, paid bool, gold, cost uint32) error {
	var r struct {
		Gold, Cost, ItemCount uint32
		Luck                  json.RawMessage
	}
	if json.Unmarshal([]byte(result), &r) != nil || r.Gold != gold || r.Cost != cost ||
		paid && r.ItemCount != c.itemCount() || !paid && r.ItemCount != 0 {
		return ErrConflict
	}
	_, err := c.validateLuck(r.Luck)
	return err
}

func (c frozenDungeonCard) validateLuck(raw json.RawMessage) (*dungeonCardLuckProof, error) {
	if c.Luck == nil {
		if len(availableCardJSON(raw)) == 0 {
			return nil, nil
		}
		return nil, ErrConflict
	}
	var r dungeonCardLuckProof
	if json.Unmarshal(raw, &r) != nil || c.Luck.Version != dungeonCardLuckVersion || !matchesSHA(c.Luck.Hash) ||
		r.Version != c.Luck.Version || r.Hash != c.Luck.Hash || r.CharacterLevel == 0 || r.Debits == nil ||
		c.Luck.Items == nil || len(c.Luck.Items) > 126 ||
		r.Before < 0 || r.Before > 100000 || r.After < 0 || r.After > 100000 {
		return nil, ErrConflict
	}
	n, points := 0, r.Before
	for _, item := range c.Luck.Items {
		if item.Item == 0 || item.Grade < 1 || item.Grade > 255 || item.Rarity > 7 {
			return nil, ErrConflict
		}
		if item.Rarity != 2 {
			continue
		}
		if n == len(r.Debits) {
			return nil, ErrConflict
		}
		debit := r.Debits[n]
		if debit.Item != item.Item || debit.Grade != item.Grade || debit.Before != points ||
			debit.Percent < 0 || debit.Percent > 2147483647 || debit.After != min(99999, max(0, points-points*debit.Percent/10000)) {
			return nil, ErrConflict
		}
		n++
		points = debit.After
	}
	if points != r.After || n != len(r.Debits) {
		return nil, ErrConflict
	}
	return &r, nil
}

type dungeonCardGrant struct {
	count                                                 uint64
	TemplateId                                            uint32
	Kind, WornSlot                                        byte
	WornSlotIsNative                                      bool
	FirstBagSlot, LastBagSlot                             uint16
	FateCollection, AccountCommonInventory, PetConsumable bool
}

func (c frozenDungeonCard) expectedGrants(completion, special json.RawMessage) (map[uint32]dungeonCardGrant, error) {
	out := make(map[uint32]dungeonCardGrant)
	add := func(row dungeonCardItem, count uint32) error {
		var t dungeonCardGrant
		if count == 0 || json.Unmarshal(row.Template, &t) != nil || t.TemplateId == 0 || t.Kind > 2 || t.FirstBagSlot > t.LastBagSlot {
			return ErrConflict
		}
		t.count = out[t.TemplateId].count + uint64(count)
		out[t.TemplateId] = t
		return nil
	}
	for _, row := range c.Items {
		if err := add(row, max(1, row.StackCount)); err != nil {
			return nil, err
		}
	}
	for _, raw := range []json.RawMessage{special, completion} {
		if len(raw) == 0 {
			continue
		}
		var plan struct{ Items []dungeonCardItem }
		if json.Unmarshal(raw, &plan) != nil || plan.Items == nil {
			return nil, ErrConflict
		}
		for _, row := range plan.Items {
			if err := add(row, row.Count); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func validateDungeonCardGrants(grants []questGrantResult, expected map[uint32]dungeonCardGrant) error {
	totals := make(map[uint32]uint64, len(expected))
	ids := make(map[string]bool, len(grants))
	for _, g := range grants {
		want, ok := expected[g.TemplateId]
		uid := strings.ToLower(g.InstanceId)
		if !ok || g.Granted == 0 || !validAssetUUID(uid) || ids[uid] || !want.validSlot(g) {
			return ErrConflict
		}
		ids[uid] = true
		totals[g.TemplateId] += uint64(g.Granted)
	}
	for id, want := range expected {
		if totals[id] != want.count {
			return ErrConflict
		}
	}
	return nil
}

func (t dungeonCardGrant) validSlot(g questGrantResult) bool {
	if t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32 {
		return g.Location == 38 && g.Slot < 56
	}
	if g.Location != 0 {
		return false
	}
	if g.Slot >= t.FirstBagSlot && g.Slot <= t.LastBagSlot {
		return true
	}
	if g.Slot >= 3 && g.Slot <= 8 && !t.FateCollection && !t.AccountCommonInventory && !t.PetConsumable {
		return true
	}
	if t.Kind == 2 {
		shared, ok := SharedMaterialSlot(t.TemplateId)
		return ok && g.Slot == shared
	}
	return false
}

func matchesEarlierDungeonCardInput(p DungeonCardClaim, previous string) bool {
	var old struct {
		Version, Owner           string
		Epoch                    int64
		CharacterId              uint16
		RunId, ResultKey         string
		Member                   byte
		Paid                     *bool
		Reward, CompletionReward json.RawMessage
	}
	var current struct {
		Owner       string
		Epoch       int64
		CharacterId uint16
		RunId       string
	}
	if json.Unmarshal([]byte(previous), &old) != nil || json.Unmarshal([]byte(p.Input), &current) != nil ||
		old.Version != "local100-card/v1" || old.Paid == nil || *old.Paid != p.Paid || len(p.Special) != 0 ||
		old.Owner != current.Owner || old.Epoch != current.Epoch || old.CharacterId != current.CharacterId ||
		old.RunId != current.RunId || old.ResultKey != p.ResultKey || old.Member != 0 {
		return false
	}
	reward := p.Reward
	if len(p.PreviousReward) != 0 {
		reward = p.PreviousReward
	}
	completion := p.Completion
	if len(p.PreviousCompletion) != 0 {
		completion = p.PreviousCompletion
	}
	return equalJSON(availableCardJSON(old.Reward), reward) && equalJSON(availableCardJSON(old.CompletionReward), completion)
}
