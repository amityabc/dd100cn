package questwire

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
)

type receipt struct {
	Key, Input, Result string
	Revision           int64
	Grants             json.RawMessage
}
type qualification struct {
	Verified bool
	Jobs     []struct {
		Job   byte
		Skill uint16
	}
}

type grantTemplate struct {
	TemplateID, StackLimit, RelativeLifetimeSeconds                                  uint32
	Kind, MinimumLevel, MythologyOptions                                             byte
	FirstBagSlot, LastBagSlot, WornSlot, MaximumDurability, UsableJobs               uint16
	ResourceHash                                                                     string
	CashPackage, CashPackageReward, PvfGiftItem                                      bool
	AccountAfterFirstTrade, InitialSealed, DailyDeletion, FatigueTimeMonthlyDeletion bool
	FateCollection, AccountCommonInventory, PetConsumable, WornSlotIsNative          bool
	ResourceExpiration                                                               *int64
	WeeklyDeletionDay                                                                *byte
	MythologyHash                                                                    *string
	WeaponQualification                                                              *qualification
}

func hash(s string) bool { _, err := hex.DecodeString(s); return err == nil && len(s) == 64 }
func (t grantTemplate) valid() bool {
	emblem := t.Kind == 2 && t.FirstBagSlot == 289 && t.LastBagSlot == 360
	if t.TemplateID == 0 || t.TemplateID == math.MaxUint32 || t.Kind > 2 || t.FirstBagSlot < 3 || t.FirstBagSlot > t.LastBagSlot || t.LastBagSlot > 344 && !emblem || t.StackLimit == 0 || t.MinimumLevel == 0 || t.UsableJobs == 0 || !hash(t.ResourceHash) {
		return false
	}
	if t.ResourceExpiration != nil && (*t.ResourceExpiration <= 0 || *t.ResourceExpiration > math.MaxInt32 || !(t.CashPackage || t.CashPackageReward || t.PvfGiftItem)) {
		return false
	}
	if (t.CashPackage || t.CashPackageReward) && (t.Kind != 2 || t.ResourceExpiration == nil) {
		return false
	}
	if (t.AccountAfterFirstTrade || t.InitialSealed || t.DailyDeletion || t.RelativeLifetimeSeconds != 0 || t.WeeklyDeletionDay != nil || t.FatigueTimeMonthlyDeletion) && !t.PvfGiftItem {
		return false
	}
	if t.RelativeLifetimeSeconds > math.MaxInt32 || t.WeeklyDeletionDay != nil && *t.WeeklyDeletionDay > 6 || t.FatigueTimeMonthlyDeletion && (t.Kind != 2 || t.DailyDeletion || t.WeeklyDeletionDay != nil) {
		return false
	}
	if t.MythologyHash == nil {
		if t.MythologyOptions != 0 {
			return false
		}
	} else if t.Kind != 1 || t.MinimumLevel != 100 || t.MythologyOptions < 3 || t.MythologyOptions > 4 || !hash(*t.MythologyHash) {
		return false
	}
	if t.WeaponQualification != nil {
		w := t.WeaponQualification
		if w.Jobs == nil || len(w.Jobs) > 16 || (!w.Verified || t.Kind != 0) && len(w.Jobs) > 0 {
			return false
		}
		for i, row := range w.Jobs {
			if row.Job >= 16 || row.Skill == 0 || row.Skill >= 512 || i > 0 && w.Jobs[i-1].Job >= row.Job {
				return false
			}
		}
	}
	if t.Kind == 0 && t.WornSlotIsNative || t.FateCollection && (t.Kind != 2 || t.StackLimit != 1) || t.AccountCommonInventory && (t.Kind != 2 || t.FateCollection) || t.PetConsumable && (t.Kind != 2 || t.FateCollection || t.AccountCommonInventory || t.FirstBagSlot != 216 || t.LastBagSlot != 271) {
		return false
	}
	if t.Kind == 2 {
		return t.WornSlot == math.MaxUint16 && t.MaximumDurability == 0
	}
	return t.StackLimit == 1 && (t.MaximumDurability > 0 || t.Kind == 0 || t.WornSlotIsNative && t.WornSlot == 32) && t.WornSlot >= 12 && t.WornSlot <= 32
}
func (v view) childReceipt(q *Quest) (string, bool) {
	if q == nil || v.p.hist.Cleared[q.Id] == 0 {
		return "", false
	}
	key := fmt.Sprintf("quest:%d", q.Id)
	var saved receipt
	if json.Unmarshal(v.receipts[key], &saved) != nil || saved.Key != key || saved.Revision <= 0 || saved.Revision > v.in.Revision || saved.Input != fmt.Sprintf(`{"Id":%d,"Selection":65535,"Multiplier":1,"Tail":65535}`, q.Id) {
		return "", false
	}
	var result struct {
		QuestId                     uint16
		ObjectiveHash, DungeonRunId string
		Level, GrowType             byte
		Rewards                     []struct {
			Template *grantTemplate
			Count    uint32
		}
	}
	if json.Unmarshal([]byte(saved.Result), &result) != nil || result.QuestId != q.Id || result.ObjectiveHash != q.Hash || result.Level == 0 || result.GrowType > 0x3f || result.GrowType&15 > 5 || result.Rewards == nil || string(saved.Grants) == "null" {
		return "", false
	}
	want := q.Rewards
	if len(q.RewardRows) > 0 {
		want = nil
		for _, r := range q.RewardRows {
			if r.Job == -1 || r.Job == int(v.p.job) && (r.Grow == -1 || r.Grow == int(v.p.grow&15)) {
				want = append(want, r.Item)
			}
		}
	}
	if len(want) != len(result.Rewards) {
		return "", false
	}
	for i, r := range result.Rewards {
		if r.Template == nil || !r.Template.valid() || r.Count == 0 || r.Count != want[i].Count || r.Template.TemplateID != want[i].Template {
			return "", false
		}
	}
	run := strings.ToLower(strings.ReplaceAll(result.DungeonRunId, "-", ""))
	if run == "" || run == strings.Repeat("0", 32) {
		return "", true
	}
	_, err := hex.DecodeString(run)
	return run, len(run) == 32 && err == nil
}
func (v view) handInReady(q *Quest, visiting map[uint16]bool) bool {
	if len(q.Children) != 2 {
		return false
	}
	first, last := v.catalog.ordinary[q.Children[0]], v.catalog.ordinary[q.Children[1]]
	run, ok := v.childReceipt(first)
	if !ok || last == nil {
		return false
	}
	if q.Id == 3851 {
		if run == "" || first.Kind != clearMap || len(first.DestinationDungeons) != 1 || len(first.Data) != 1 {
			return false
		}
		found := false
		for key, raw := range v.receipts {
			if !strings.HasPrefix(key, "dungeon-death:"+run+":") {
				continue
			}
			var s receipt
			var entry struct {
				RunId   string
				Dungeon int
				Maps    []int
			}
			var out struct{ RoomCleared bool }
			if json.Unmarshal(raw, &s) == nil && json.Unmarshal([]byte(s.Input), &entry) == nil && strings.EqualFold(strings.ReplaceAll(entry.RunId, "-", ""), run) && entry.Dungeon == first.DestinationDungeons[0] && slices.Contains(entry.Maps, first.Data[0]) && json.Unmarshal([]byte(s.Result), &out) == nil && out.RoomCleared {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	} else if run != "" {
		return false
	}
	if v.p.hist.Cleared[last.Id] != 0 {
		run, ok := v.childReceipt(last)
		return ok && run == ""
	}
	if !v.catalog.ordinaryAvailable(v.p, last) {
		return false
	}
	left, err := v.remaining(last, visiting)
	return err == nil && left == 0
}
