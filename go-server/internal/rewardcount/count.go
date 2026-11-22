// Package rewardcount builds the ordinary N1734 projection from permanent
// free-card receipts. It never maintains or grants an independent counter.
package rewardcount

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"s9a1/server/internal/storage"
)

const ghent uint32 = 100000002

var policies = map[uint32]string{ghent: "LocalGhentResourceSpecial/v1", 100000176: "LocalMineOfAbyssResourceSpecial/v1", 100000177: "LocalMemoryLandResourceSpecial/v1", 100000178: "LocalCryptOfPainResourceSpecial/v1", 100000179: "LocalDarkTempleResourceSpecial/v1"}
var errReceipt = errors.New("invalid special reward receipt")
var errShape = errors.New("unsupported frozen free-card reward shape")

type grant struct {
	InstanceId string
	TemplateId uint32
	Location   uint8
	Slot       uint16
	Granted    uint32
}
type receipt struct {
	Key, Input, Result string
	Revision           int64
	Grants             []grant
	SpecialGrants      json.RawMessage
}
type creditInput struct {
	Version, OperationId               string
	CharacterId                        uint16
	Day                                int64
	Slot, BaseLimit, UsedBefore, Added uint8
	BeforeRevision                     int64
	Reason                             string
}
type creditResult struct{ LimitBefore, LimitAfter, RemainingBefore, RemainingAfter uint8 }
type regularReward struct {
	Policy, ResourceHash string
	Gold                 uint32
	Items                *[]freeItem `json:",omitempty"`
	Luck                 *cardLuck   `json:",omitempty"`
}
type freeItem struct {
	Template       *material
	Attributes     *attributes
	Raw119, Detail []uint16
	StackCount     *uint32 `json:",omitempty"`
}
type cardLuck struct {
	Version, Hash      string
	Points, Multiplier int32
	Items              []struct {
		Item          uint32
		Grade, Rarity int
	}
}
type completion struct {
	Policy, ResourceHash string
	DungeonId            uint32
	Maze, Tier           uint8
	Total                uint32
	Items                []specialItem
}
type cardInput struct {
	Version, Owner   string
	Epoch            int64
	CharacterId      uint16
	RunId, ResultKey string
	Member           uint8
	Reward           *regularReward
	SpecialReward    *specialReward
	CompletionReward *completion `json:",omitempty"`
}
type specialReward struct {
	Policy, ResourceHash string
	Day                  int64
	Limit, Ordinal       uint8
	Container            uint32
	Items                []specialItem
	DungeonId            uint32 `json:",omitempty"`
}
type specialItem struct {
	Template       *material
	Count          uint32
	Attributes     *attributes
	Raw119, Detail []uint16
}
type material struct {
	TemplateId                                                                                                        uint32
	Kind                                                                                                              int
	FirstBagSlot, LastBagSlot, WornSlot                                                                               uint16
	StackLimit                                                                                                        uint32
	MaximumDurability                                                                                                 uint16
	MinimumLevel                                                                                                      uint8
	UsableJobs                                                                                                        uint16
	ResourceHash                                                                                                      string
	WornSlotIsNative, CashPackage, CashPackageReward, PvfGiftItem, AccountAfterFirstTrade, MonsterCard, InitialSealed bool
	ResourceExpiration                                                                                                *int64
	FateCollection, AccountCommonInventory, PetConsumable, CharmEnergy                                                bool
	WeaponQualification                                                                                               struct {
		Verified bool
		Jobs     []struct {
			Job   uint8
			Skill uint16
		}
		Valid bool
	}
	RelativeLifetimeSeconds    uint32
	DailyDeletion              bool
	WeeklyDeletionDay          *uint8
	FatigueTimeMonthlyDeletion bool
}
type attributes struct {
	QualitySeed                             uint32
	Durability                              uint16
	Reinforcement, Forge, AmplificationType uint8
	AmplificationValue                      uint16
	Enchantment                             uint32
	Binding                                 uint8
	ExpiresAt                               *int64
	Locked                                  bool
	EnchantUpgradeCount                     uint8
}

func nonemptyGUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || s != strings.ToLower(s) {
		return false
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return err == nil && len(b) == 16 && !bytes.Equal(b, make([]byte, 16))
}
func hashOK(s string) bool { _, err := hex.DecodeString(s); return err == nil && len(s) == 64 }

// The legacy contract reserializes a typed value and compares its full
// string. System.Text.Json's default encoder escapes non-ASCII UTF-16 units.
func canonical(v any, text string) bool {
	raw, err := json.Marshal(v)
	if err != nil {
		return false
	}
	var b strings.Builder
	for _, r := range string(raw) {
		if r >= 127 || r == '+' || r == '\'' || r == '`' {
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, "\\u%04X", u)
			}
		} else {
			b.WriteRune(r)
		}
	}
	s := b.String()
	s = strings.NewReplacer("\\u003c", "\\u003C", "\\u003e", "\\u003E", "\\u2028", "\\u2028", "\\u2029", "\\u2029").Replace(s)
	return s == text
}

func typed(text string, out any) error {
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", errShape, err)
	}
	if !canonical(out, text) {
		return errReceipt
	}
	return nil
}
func readReceipt(raw json.RawMessage, key string, rev int64) (receipt, error) {
	var r receipt
	if err := json.Unmarshal(raw, &r); err != nil || r.Key != key || r.Revision <= 0 || r.Revision > rev || r.Grants == nil {
		return r, errReceipt
	}
	return r, nil
}

func grantSlot(t *material, location uint8, slot uint16) bool {
	if t.Kind != 2 {
		expected := uint8(0)
		if t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32 {
			expected = 38
		}
		return location == expected && slot >= t.FirstBagSlot && slot <= t.LastBagSlot
	}
	if location != 0 {
		return false
	}
	if slot >= 3 && slot <= 8 || slot >= t.FirstBagSlot && slot <= t.LastBagSlot {
		return true
	}
	fixed, ok := storage.SharedMaterialSlot(t.TemplateId)
	return ok && slot == fixed
}

// Each frozen plain permanent grant retains its distinct slot kind. A common
// template can appear in the free, special and completion plans together.
func materialTotals(items []specialItem, grants []grant) error {
	amounts := map[uint32]int64{}
	templates := map[uint32]*material{}
	for _, i := range items {
		t := i.Template
		if t == nil || i.Attributes == nil || t.Kind < 0 || t.Kind > 2 || t.TemplateId == 0 || t.TemplateId == math.MaxUint32 ||
			t.FirstBagSlot < 3 || t.LastBagSlot > 344 && !(t.Kind == 2 && t.FirstBagSlot == 289 && t.LastBagSlot == 360) || t.FirstBagSlot > t.LastBagSlot ||
			t.MinimumLevel == 0 || t.UsableJobs == 0 || t.StackLimit == 0 || !hashOK(t.ResourceHash) ||
			t.CashPackage || t.CashPackageReward || t.AccountAfterFirstTrade || t.MonsterCard || t.InitialSealed || t.ResourceExpiration != nil ||
			t.FateCollection || t.AccountCommonInventory || t.PetConsumable || t.CharmEnergy || t.RelativeLifetimeSeconds != 0 || t.DailyDeletion || t.WeeklyDeletionDay != nil || t.FatigueTimeMonthlyDeletion ||
			!t.WeaponQualification.Valid || t.WeaponQualification.Jobs == nil || len(t.WeaponQualification.Jobs) > 16 {
			return errShape
		}
		q := t.WeaponQualification
		if len(q.Jobs) > 0 && (!q.Verified || t.Kind != 0) {
			return errReceipt
		}
		for n, j := range q.Jobs {
			if j.Job > 15 || j.Skill == 0 || j.Skill >= 512 || n > 0 && q.Jobs[n-1].Job >= j.Job {
				return errReceipt
			}
		}
		attr := *i.Attributes
		if t.Kind == 2 {
			if t.WornSlot != math.MaxUint16 || t.MaximumDurability != 0 || attr.QualitySeed != 0 || attr.Durability != 0 {
				return errReceipt
			}
		} else {
			if i.Count != 1 || t.StackLimit != 1 || t.WornSlot < 12 || t.WornSlot > 32 || t.WornSlotIsNative && t.Kind == 0 ||
				t.Kind == 1 && t.MaximumDurability == 0 && !(t.WornSlotIsNative && t.WornSlot == 32) || attr.QualitySeed == 0 || attr.QualitySeed >= 999999999 || attr.Durability > t.MaximumDurability {
				return errReceipt
			}
		}
		attr.QualitySeed, attr.Durability = 0, 0
		if attr != (attributes{}) {
			return errShape
		}
		if i.Count == 0 || len(i.Raw119) != 119 || i.Detail == nil || len(i.Detail) != 0 {
			return errReceipt
		}
		for n, v := range i.Raw119 {
			if v > 255 || v != 0 && n >= 10 && n != 11 && n != 12 {
				return errReceipt
			}
		}
		amounts[t.TemplateId] += int64(i.Count)
		templates[t.TemplateId] = t
	}
	seen := map[string]bool{}
	for _, g := range grants {
		t := templates[g.TemplateId]
		if t == nil || !nonemptyGUID(g.InstanceId) || seen[g.InstanceId] || g.Granted == 0 || !grantSlot(t, g.Location, g.Slot) {
			return errReceipt
		}
		seen[g.InstanceId] = true
		amounts[g.TemplateId] -= int64(g.Granted)
	}
	for _, v := range amounts {
		if v != 0 {
			return errReceipt
		}
	}
	return nil
}

func allCardItems(p *specialReward, r *regularReward, completion *completion) ([]specialItem, error) {
	for _, i := range p.Items {
		if i.Count > 1000000 {
			return nil, errReceipt
		}
	}
	var all []specialItem
	var equipment []uint32
	if r.Items != nil {
		if len(*r.Items) > 126 {
			return nil, errReceipt
		}
		for _, i := range *r.Items {
			if i.Template == nil {
				return nil, errReceipt
			}
			var count uint32 = 1
			if i.Template.Kind == 2 {
				if i.StackCount == nil || *i.StackCount == 0 || *i.StackCount > i.Template.StackLimit {
					return nil, errReceipt
				}
				count = *i.StackCount
			} else {
				if i.StackCount != nil {
					return nil, errReceipt
				}
				equipment = append(equipment, i.Template.TemplateId)
			}
			all = append(all, specialItem{i.Template, count, i.Attributes, i.Raw119, i.Detail})
		}
	}
	if r.Policy == "PVFFreeCard/v2" && len(all) == 0 || r.Policy == "PVFFreeArtifactCard/v1" {
		return nil, errReceipt
	}
	if l := r.Luck; l != nil {
		if l.Version != "SoloCardLuck/v1;preview-snapshot;successful-claim-debit;older-reference" || !hashOK(l.Hash) || l.Points < 0 || l.Points > 100000 || l.Multiplier < 0 || l.Items == nil || len(l.Items) != len(equipment) {
			return nil, errReceipt
		}
		for n, row := range l.Items {
			if row.Item != equipment[n] || row.Grade < 1 || row.Grade > 255 || row.Rarity < 0 || row.Rarity > 7 {
				return nil, errReceipt
			}
		}
	}
	all = append(all, p.Items...)
	if completion != nil {
		v := completion
		if v.Policy != "PVF-native-completion/v1;source-item-quantity;source-weight;no-fixed-total" || !hashOK(v.ResourceHash) || v.DungeonId == 0 || v.Tier > 4 || v.Total == 0 || len(v.Items) == 0 || len(v.Items) > 1024 {
			return nil, errReceipt
		}
		var total uint64
		for _, i := range v.Items {
			if i.Template == nil || i.Count == 0 || i.Count > i.Template.StackLimit {
				return nil, errReceipt
			}
			total += uint64(i.Count)
		}
		if total != uint64(v.Total) {
			return nil, errReceipt
		}
		all = append(all, v.Items...)
	}
	return all, nil
}

func claims(c storage.Character, all map[string]json.RawMessage, day int64, dungeon uint32) ([]int64, error) {
	revs := []int64{}
	for n := 1; n <= 255; n++ {
		key := fmt.Sprintf("dungeon-special:%d:%d:%d", dungeon, day, n)
		raw, ok := all[key]
		if !ok {
			continue
		}
		if n != len(revs)+1 {
			return nil, errReceipt
		}
		r, err := readReceipt(raw, key, c.Revision)
		if err != nil {
			return nil, err
		}
		var input cardInput
		if err = typed(r.Input, &input); err != nil {
			return nil, err
		}
		p := input.SpecialReward
		g := input.Reward
		if p == nil || g == nil || input.Version != "local100-free-special/v1" || !nonemptyGUID(input.Owner) || !nonemptyGUID(input.RunId) || input.Epoch <= 0 || input.Member != 0 || input.CharacterId != c.ID {
			return nil, errReceipt
		}
		actual := p.DungeonId
		if actual == 0 {
			actual = ghent
		}
		if actual == 100000180 || actual == 100000181 {
			actual = 100000179
		}
		if actual != dungeon || p.Policy != policies[dungeon] || !hashOK(p.ResourceHash) || p.Day != day || p.Limit == 0 || p.Limit > 32 || int(p.Ordinal) != n || p.Ordinal > p.Limit || p.Container == 0 ||
			len(p.Items) == 0 || len(p.Items) > 16 || strings.TrimSpace(g.Policy) == "" || len(utf16.Encode([]rune(g.Policy))) > 512 || !hashOK(g.ResourceHash) ||
			(g.Gold == 0) != (g.Policy == "PVFFreeZeroGold/v1") || len(r.SpecialGrants) != 0 {
			return nil, errReceipt
		}
		identity := fmt.Sprintf("%s:%d:%d:%s", strings.ReplaceAll(input.Owner, "-", ""), input.Epoch, c.ID, strings.ReplaceAll(input.RunId, "-", ""))
		if input.ResultKey != "dungeon-settlement:v1:"+identity+":result" {
			return nil, errReceipt
		}
		freekey := "dungeon-free-card:v1:" + identity
		free, err := readReceipt(all[freekey], freekey, c.Revision)
		if err != nil {
			return nil, err
		}
		free.Key = key
		if !reflect.DeepEqual(free, r) {
			return nil, errReceipt
		}
		items, err := allCardItems(p, g, input.CompletionReward)
		if err != nil {
			return nil, err
		}
		if err = materialTotals(items, r.Grants); err != nil {
			return nil, err
		}
		revs = append(revs, r.Revision)
	}
	return revs, nil
}

func withCredits(c storage.Character, all map[string]json.RawMessage, day int64, base uint8, claims []int64) (int, error) {
	limit := int(base)
	previous := int64(0)
	byID := map[string]bool{}
	for slot := 1; slot <= 32; slot++ {
		key := fmt.Sprintf("dungeon-special-credit:%d:%d:%d", ghent, day, slot)
		raw, exists := all[key]
		if !exists {
			continue
		}
		if len(byID)+1 != slot {
			return 0, errReceipt
		}
		r, err := readReceipt(raw, key, c.Revision)
		if err != nil {
			return 0, err
		}
		var in creditInput
		var result creditResult
		if err = typed(r.Input, &in); err != nil {
			return 0, err
		}
		if err = typed(r.Result, &result); err != nil {
			return 0, err
		}
		used := 0
		for _, v := range claims {
			if v < r.Revision {
				used++
			}
		}
		next := limit + int(in.Added)
		if r.Revision <= previous || in.BeforeRevision < 0 || r.Revision-1 != in.BeforeRevision || len(r.Grants) != 0 || in.Version != "local100-ghent-special-credit/v1" ||
			!nonemptyGUID(in.OperationId) || byID[in.OperationId] || in.CharacterId != c.ID || in.Day != day || int(in.Slot) != slot || in.BaseLimit != base || int(in.UsedBefore) != used || used > limit ||
			in.Added == 0 || next > 32 || strings.TrimSpace(in.Reason) == "" || len(utf16.Encode([]rune(in.Reason))) > 256 ||
			result != (creditResult{uint8(limit), uint8(next), uint8(limit - used), uint8(next - used)}) {
			return 0, errReceipt
		}
		byID[in.OperationId] = true
		previous = r.Revision
		limit = next
	}
	return limit, nil
}

func Body(c storage.Character, receipts map[string]json.RawMessage, limits map[uint32]byte, now time.Time) ([]byte, error) {
	var profile struct{ Initialized bool }
	if c.ID == 0 || json.Unmarshal(c.Gameplay, &profile) != nil || !profile.Initialized {
		return nil, storage.ErrPhase
	}
	if len(limits) == 0 {
		return nil, nil
	}
	if len(limits) > 5 {
		return nil, errReceipt
	}
	d := now.UTC().Add(2 * time.Hour)
	day := int64(d.Year()*10000 + int(d.Month())*100 + d.Day())
	if day < 20000101 {
		return nil, errReceipt
	}
	keys := make([]uint32, 0, len(limits))
	for k, v := range limits {
		if policies[k] == "" || v == 0 || v > 32 {
			return nil, errReceipt
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := []byte{byte(len(keys))}
	for _, k := range keys {
		base := limits[k]
		if len(receipts) == 0 {
			out = binary.LittleEndian.AppendUint32(out, k)
			out = append(out, base)
			continue
		}
		limit := int(base)
		claims, err := claims(c, receipts, day, k)
		if err != nil {
			return nil, err
		}
		if k == ghent {
			limit, err = withCredits(c, receipts, day, base, claims)
			if err != nil {
				return nil, err
			}
		}
		if len(claims) > limit {
			return nil, errReceipt
		}
		out = binary.LittleEndian.AppendUint32(out, k)
		out = append(out, byte(limit-len(claims)))
	}
	return out, nil
}
