package questwire

import (
	"encoding/json"
	"fmt"
	"math"
	"s9a1/server/internal/storage"
	"slices"
	"time"
)

type capsuleItem struct {
	levelTicket
	NativeAction      uint32
	TargetLevel       byte
	UsablePlaces      []string
	CooldownGroup     *uint32
	ForbidScene8      bool
	UnsupportedReason *string
}

const capsule193 = uint32(193)
const legacyCapsule = uint32(490016725)

type CapsuleRequest struct {
	Slot                                   uint16
	List                                   byte
	Reserved, Action, Argument0, Argument1 uint32
	Sequence                               uint16
}

func ReadCapsule(seq uint16, b []byte) (CapsuleRequest, error) {
	if len(b) != 19 {
		return CapsuleRequest{}, storage.ErrInvalid
	}
	return CapsuleRequest{little.Uint16(b), b[2], little.Uint32(b[3:]), little.Uint32(b[7:]), little.Uint32(b[11:]), little.Uint32(b[15:]), seq}, nil
}

func (r CapsuleRequest) Reply(success bool) []byte {
	b := []byte{1}
	if !success {
		b = []byte{0, 0, 0}
	}
	b = append(little.AppendUint16(b, r.Slot), r.List)
	return little.AppendUint32(b, r.Action)
}

func (r CapsuleRequest) replay(c storage.Character, in storage.Inventory) GrowthReplay {
	return GrowthReplay{Request: GrowthRequest{Opcode: 507, Sequence: r.Sequence, Slot: r.Slot, List: r.List, NativeReference: r.Action, TemplateId: r.Argument0}, Character: c.ID,
		Town: c.Position.Town, Area: c.Position.Area, Scene: fmt.Sprintf("%d:%d", r.Reserved, r.Argument1), Revision: in.Revision, AssetRevision: in.AssetRevision}
}

func (catalog *Catalog) UseCapsule(c storage.Character, in storage.Inventory, r CapsuleRequest, now time.Time, prior GrowthReplay,
	apply func(storage.Character, byte, byte) (storage.Character, error)) (GrowthResult, error) {
	var out GrowthResult
	var doc skillPage
	var life struct {
		Initialized bool
		Progression struct{ Hp uint32 }
	}
	if json.Unmarshal(c.Gameplay, &doc) != nil || json.Unmarshal(c.Gameplay, &life) != nil || !life.Initialized || life.Progression.Hp == 0 || c.Job > 15 ||
		r.Action != capsule193 || r.List != 0 || r.Reserved != 0 || r.Argument0 > 5 || r.Argument1 != 0 {
		return out, storage.ErrInvalid
	}
	replay := r.replay(c, in)
	if prior == replay {
		out.Repeated = true
		return out, nil
	}
	i := slices.IndexFunc(in.Items, func(a storage.Asset) bool { return a.Location == 0 && a.Slot == r.Slot })
	if i < 0 {
		return out, storage.ErrInvalid
	}
	a := in.Items[i]
	d, ok := catalog.growth.Capsules[a.TemplateID]
	var spec grantTemplate
	var attrs struct{ ExpiresAt *int64 }
	if !ok || d.NativeAction != capsule193 || d.TargetLevel < 2 || d.TargetLevel > 100 || d.MaximumLevel >= d.TargetLevel || d.CooldownMilliseconds > math.MaxInt32 || !slices.Equal(d.UsablePlaces, []string{"seria room"}) ||
		json.Unmarshal(d.Template, &spec) != nil || !spec.valid() || spec.Kind != 2 || !spec.PvfGiftItem || spec.FirstBagSlot != 65 || spec.LastBagSlot != 120 || guidKey(a.UID) == "" ||
		c.Level < spec.MinimumLevel || c.Level > d.MaximumLevel || spec.UsableJobs&(1<<c.Job) == 0 || !storage.QuestOrdinarySource(a, d.Template) ||
		!(a.Slot >= 3 && a.Slot <= 8 || a.Slot >= 65 && a.Slot <= min(120, 96+in.BagExpansion)) || json.Unmarshal(a.Attributes, &attrs) != nil {
		return out, storage.ErrInvalid
	}
	at := now.UnixMilli()
	if at <= 0 || attrs.ExpiresAt != nil && *attrs.ExpiresAt <= at/1000 || spec.ResourceExpiration != nil && *spec.ResourceExpiration <= at/1000 {
		return out, storage.ErrInvalid
	}
	var legacy int64
	if doc["Level100CapsuleCooldownUntilMilliseconds"] != nil && json.Unmarshal(doc["Level100CapsuleCooldownUntilMilliseconds"], &legacy) != nil {
		return out, storage.ErrInvalid
	}
	if a.TemplateID == legacyCapsule && legacy > at {
		return out, storage.ErrInvalid
	}
	key := fmt.Sprintf("item:%d", a.TemplateID)
	if d.CooldownGroup != nil {
		key = fmt.Sprintf("group:%d", *d.CooldownGroup)
	}
	cooldowns, err := growthCooldowns(doc, key, at, d.CooldownMilliseconds)
	if err != nil {
		return out, err
	}
	next, err := apply(c, d.TargetLevel, byte(r.Argument0))
	if err != nil {
		return out, storage.ErrInvalid
	}
	var progress struct {
		Experience uint64
		Sp, Tp     uint16
		GrowType   byte
	}
	if json.Unmarshal(next.Gameplay, &doc) != nil || doc == nil || json.Unmarshal(doc["Progression"], &progress) != nil {
		return out, storage.ErrInvalid
	}
	doc["PotionCooldowns"], _ = json.Marshal(cooldowns)
	if a.TemplateID == legacyCapsule && d.CooldownMilliseconds > 0 {
		doc["Level100CapsuleCooldownUntilMilliseconds"], _ = json.Marshal(at + int64(d.CooldownMilliseconds))
	}
	next.Gameplay, _ = json.Marshal(doc)
	inv, err := storage.QuestConsumeInstance(in, a.UID)
	if err != nil {
		return out, err
	}
	key = fmt.Sprintf("level100-capsule:%s:%d", guidKey(a.UID), c.Revision+1)
	input, _ := json.Marshal(struct {
		Request  CapsuleRequest
		Source   string
		Template uint32
		Hash     string
		Target   byte
	}{r, wireGUID(a.UID), a.TemplateID, spec.ResourceHash, d.TargetLevel})
	result, _ := json.Marshal(struct {
		Version, Level byte
		Experience     uint64
		Sp, Tp         uint16
		Grow           byte
	}{1, next.Level, progress.Experience, progress.Sp, progress.Tp, progress.GrowType})
	raw, _ := json.Marshal(receipt{Key: key, Input: string(input), Result: string(result), Revision: c.Revision + 1, Grants: json.RawMessage("[]")})
	out.Mutation.Reward = &storage.QuestRewardMutation{Character: next, Inventory: inv, Receipts: map[string]json.RawMessage{key: raw}}
	replay.Revision++
	out.Replay = replay
	return out, nil
}
