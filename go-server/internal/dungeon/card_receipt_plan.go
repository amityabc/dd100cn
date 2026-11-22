package dungeon

import (
	"encoding/json"

	"s9a1/server/internal/storage"
)

const (
	cardLuckVersion      = "SoloCardLuck/v1;preview-snapshot;successful-claim-debit;older-reference"
	completionPlanPolicy = "PVF-native-completion/v1;source-item-quantity;source-weight;no-fixed-total"
)

type CardLuckSnapshot struct {
	Version, Hash      string
	Points, Multiplier int64
	Items              []cardLuckItem
}

type PaidReceiptCardPlan struct {
	Policy, ResourceHash string
	Cost, Gold           uint32
	Items                []cardCounterItem
	UnavailableReason    *string
	Surprise             bool                 `json:",omitempty"`
	Luck                 *CardLuckSnapshot    `json:",omitempty"`
	Artifact             *CardCounterArtifact `json:",omitempty"`
}

func (r *CardReward) FreeReceiptPlan() (*CardCounterReward, error) {
	if r == nil {
		return nil, nil
	}
	return r.CounterReward()
}

func (r *CardReward) PaidReceiptPlan() (*PaidReceiptCardPlan, error) {
	if r == nil {
		return nil, nil
	}
	frozen, err := r.CounterReward()
	if err != nil {
		return nil, err
	}
	items := frozen.Items
	if items == nil {
		items = []cardCounterItem{}
	}
	var reason *string
	if r.UnavailableReason != "" {
		reason = &r.UnavailableReason
	}
	return &PaidReceiptCardPlan{
		Policy: frozen.Policy, ResourceHash: frozen.ResourceHash, Cost: r.Cost, Gold: frozen.Gold,
		Items: items, UnavailableReason: reason, Surprise: r.Surprise, Luck: frozen.Luck, Artifact: frozen.Artifact,
	}, nil
}

func (r *CardReward) freezeCardLuck(luck *LuckRoll) error {
	if r == nil || luck == nil || !isHash(luck.Hash) {
		return nil
	}
	frozen := &CardLuckSnapshot{Version: cardLuckVersion, Hash: luck.Hash, Points: luck.Before, Items: []cardLuckItem{}}
	for _, band := range luck.policy.Bands {
		if frozen.Points <= band.Maximum {
			frozen.Multiplier = band.PartsPer10000
			break
		}
	}
	for _, item := range r.Items {
		if item.Reward.Artifact != nil {
			continue
		}
		var template dropTemplate
		if err := json.Unmarshal(item.Reward.Template, &template); err != nil {
			return err
		}
		if template.Kind > 1 {
			continue
		}
		if item.Metadata.Item != template.TemplateID || item.Metadata.Grade < 1 || item.Metadata.Grade > 255 || item.Metadata.Rarity > 7 {
			return storage.ErrInvalid
		}
		frozen.Items = append(frozen.Items, item.Metadata)
	}
	r.luckSnapshot = frozen
	return nil
}

func DecodeFreeReceiptPlan(raw json.RawMessage) (*CardReward, error) {
	if isLegacyCardPlan(raw, false, false) {
		return readLegacyCardPlan(raw)
	}
	var frozen *CardCounterReward
	if err := decodeOptionalCard(raw, &frozen); err != nil || frozen == nil {
		return nil, err
	}
	if (frozen.Gold == 0) != (frozen.Policy == "PVFFreeZeroGold/v1") ||
		frozen.Policy == "PVFFreeCard/v2" && len(frozen.Items) == 0 ||
		frozen.Policy == "PVFFreeArtifactCard/v1" && frozen.Artifact == nil {
		return nil, storage.ErrInvalid
	}
	r := &CardReward{Policy: frozen.Policy, ResourceHash: frozen.ResourceHash, Gold: frozen.Gold}
	return r, r.decodeReceiptItems(frozen.Items, frozen.Luck, frozen.Artifact)
}

func DecodePaidReceiptPlan(raw json.RawMessage) (*CardReward, error) {
	if isLegacyCardPlan(raw, true, false) {
		return readLegacyCardPlan(raw)
	}
	var frozen *PaidReceiptCardPlan
	if err := decodeOptionalCard(raw, &frozen); err != nil || frozen == nil {
		return nil, err
	}
	r := &CardReward{Policy: frozen.Policy, ResourceHash: frozen.ResourceHash, Gold: frozen.Gold, Cost: frozen.Cost, Surprise: frozen.Surprise}
	if frozen.Items == nil || frozen.UnavailableReason != nil &&
		(*frozen.UnavailableReason == "" || len(*frozen.UnavailableReason) > 512 || frozen.Cost != 0 ||
			frozen.Gold != 0 || len(frozen.Items) != 0 || frozen.Artifact != nil) ||
		frozen.UnavailableReason == nil && (frozen.Cost == 0 || frozen.Gold == 0 && len(frozen.Items) == 0 && frozen.Artifact == nil) {
		return nil, storage.ErrInvalid
	}
	if frozen.UnavailableReason != nil {
		r.UnavailableReason = *frozen.UnavailableReason
	}
	return r, r.decodeReceiptItems(frozen.Items, frozen.Luck, frozen.Artifact)
}

func DecodeCompletionReceiptPlan(raw json.RawMessage) (*CardReward, error) {
	if isLegacyCardPlan(raw, false, true) {
		return readLegacyCardPlan(raw)
	}
	var frozen *CardCounterCompletion
	if err := decodeOptionalCard(raw, &frozen); err != nil || frozen == nil {
		return nil, err
	}
	if frozen.Policy != completionPlanPolicy || frozen.DungeonId == 0 || frozen.Tier > 4 || frozen.Total == 0 ||
		len(frozen.Items) == 0 || len(frozen.Items) > 1024 || !isHash(frozen.ResourceHash) {
		return nil, storage.ErrInvalid
	}
	r := &CardReward{Policy: frozen.Policy, ResourceHash: frozen.ResourceHash, completionSnapshot: frozen}
	var total uint64
	for _, row := range frozen.Items {
		item, err := cardItemFromReceipt(row, nil)
		if err != nil {
			return nil, err
		}
		r.Items = append(r.Items, item)
		total += uint64(row.Count)
	}
	if total != uint64(frozen.Total) {
		return nil, storage.ErrInvalid
	}
	return r, nil
}

func decodeOptionalCard(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

// Early Go settlements stored the runtime GroundReward and metadata wrapper.
// Keep that exact original proof for the claim transaction while producing
// the native plan for new claims.
func isLegacyCardPlan(raw json.RawMessage, paid, completion bool) bool {
	var row struct {
		Policy string
		Items  []struct {
			Reward json.RawMessage
		}
		UnavailableReason *string
	}
	if json.Unmarshal(raw, &row) != nil {
		return false
	}
	if completion {
		return row.Policy == "LocalDungeonPvFCompletion" && len(row.Items) != 0
	}
	return len(row.Items) > 0 && len(row.Items[0].Reward) > 0 ||
		paid && row.Items == nil && row.UnavailableReason != nil
}

func readLegacyCardPlan(raw json.RawMessage) (*CardReward, error) {
	var old CardReward
	if json.Unmarshal(raw, &old) != nil || !isHash(old.ResourceHash) || old.Policy == "" {
		return nil, storage.ErrInvalid
	}
	old.legacyPlan = raw
	return &old, nil
}

func (r *CardReward) PreviousReceiptPlan() json.RawMessage {
	if r == nil {
		return nil
	}
	return r.legacyPlan
}
func (r *CardReward) decodeReceiptItems(rows []cardCounterItem, luck *CardLuckSnapshot, artifact *CardCounterArtifact) error {
	if !isHash(r.ResourceHash) || r.Policy == "" || len(r.Policy) > 512 || len(rows) > 126 || artifact != nil && len(rows) != 0 {
		return storage.ErrInvalid
	}
	if luck != nil && (luck.Version != cardLuckVersion || !isHash(luck.Hash) || luck.Points < 0 || luck.Points > 100000 || luck.Multiplier < 0 || luck.Items == nil) {
		return storage.ErrInvalid
	}
	r.luckSnapshot = luck
	if artifact != nil {
		var template struct {
			TemplateId        uint32
			MaximumDurability uint16
			Kind              byte
		}
		if json.Unmarshal(artifact.Template, &template) != nil || template.TemplateId == 0 || template.Kind != 2 ||
			artifact.QualitySeed == 0 || artifact.QualitySeed >= 999999999 || luck != nil && len(luck.Items) != 0 {
			return storage.ErrInvalid
		}
		r.Items = []CardItem{{Reward: GroundReward{Artifact: artifact.Template, QualitySeed: artifact.QualitySeed,
			Durability: template.MaximumDurability}, Metadata: cardLuckItem{Item: template.TemplateId}}}
		return nil
	}
	n := 0
	for _, row := range rows {
		count := max(1, row.StackCount)
		if len(row.Detail) != 0 {
			return storage.ErrInvalid
		}
		item, err := cardItemFromReceipt(SpecialCardItem{Template: row.Template, Count: count,
			Attributes: row.Attributes, Raw119: row.Raw119, Detail: row.Detail}, &row)
		if err != nil {
			return err
		}
		if item.Reward.StackCount == 0 && luck != nil {
			if n == len(luck.Items) {
				return storage.ErrInvalid
			}
			meta := luck.Items[n]
			if meta.Item != item.Metadata.Item || meta.Grade < 1 || meta.Grade > 255 || meta.Rarity > 7 {
				return storage.ErrInvalid
			}
			item.Metadata = meta
			n++
		}
		r.Items = append(r.Items, item)
	}
	if luck != nil && n != len(luck.Items) {
		return storage.ErrInvalid
	}
	return nil
}

func cardItemFromReceipt(row SpecialCardItem, card *cardCounterItem) (CardItem, error) {
	var t dropTemplate
	if json.Unmarshal(row.Template, &t) != nil || t.TemplateID == 0 || t.Kind > 2 || t.StackLimit == 0 ||
		row.Count == 0 || row.Count > t.StackLimit || t.FirstBagSlot > t.LastBagSlot ||
		len(row.Raw119) != 119 || row.Detail == nil || row.Attributes.Durability > t.MaximumDurability {
		return CardItem{}, storage.ErrInvalid
	}
	if card != nil && (t.Kind == 2) != (card.StackCount != 0) || t.Kind != 2 && row.Count != 1 {
		return CardItem{}, storage.ErrInvalid
	}
	for _, data := range [][]uint16{row.Raw119, row.Detail} {
		for _, value := range data {
			if value > 255 {
				return CardItem{}, storage.ErrInvalid
			}
		}
	}
	reward := GroundReward{Template: row.Template, QualitySeed: row.Attributes.QualitySeed,
		Durability: row.Attributes.Durability, Reinforcement: row.Attributes.Reinforcement,
		AmplificationType: row.Attributes.AmplificationType, AmplificationValue: row.Attributes.AmplificationValue,
		ExpiresAt: row.Attributes.ExpiresAt, Mythology: row.Attributes.Mythology}
	if t.Kind == 2 {
		reward.StackCount = row.Count
	}
	return CardItem{Reward: reward, Metadata: cardLuckItem{Item: t.TemplateID}, frozen: &row}, nil
}

func grantFrozenCardItem(in storage.Inventory, item CardItem) ([]storage.QuestGrant, error) {
	rows, err := (GroundDrop{Reward: item.Reward}).Grant(in)
	if err != nil || len(rows) != 1 {
		return rows, err
	}
	g := &rows[0]
	if r := item.frozen; r != nil {
		g.Attributes, err = json.Marshal(r.Attributes)
		if err != nil {
			return nil, err
		}
		copyBytes := func(src []uint16) []byte {
			out := make([]byte, len(src))
			for i, value := range src {
				out[i] = byte(value)
			}
			return out
		}
		g.Core, g.Detail = copyBytes(r.Raw119), copyBytes(r.Detail)
	}
	if slot, shared := storage.SharedMaterialSlot(g.TemplateID); shared && g.Kind == 2 {
		g.Source, g.CharacterID, g.First, g.Last = "shared", 0, slot, slot
	} else if g.Kind == 2 && g.First == 65 {
		var template dropTemplate
		if err := json.Unmarshal(g.Template, &template); err != nil {
			return nil, err
		}
		g.QuickSlots = template.LastBagSlot == 120
	}
	return rows, nil
}
