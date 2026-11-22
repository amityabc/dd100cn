package questwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"s9a1/server/internal/storage"
	"slices"
	"strconv"
	"strings"
	"time"
)

type growthItems struct {
	Experience   map[uint32]experienceItem
	LevelTickets map[uint32]levelTicket
	Capsules     map[uint32]capsuleItem
	QuestTickets map[uint32]questTicketItem
	Emblems      storage.EmblemCatalog
	AvatarEdits  storage.AvatarEditCatalog
	Teleports    map[uint32]teleportDefinition
	PetGrowth    *storage.PetGrowthRule
}

func (c *Catalog) PetGrowthRule() *storage.PetGrowthRule { return c.growth.PetGrowth }

type levelTicket struct {
	Template             json.RawMessage
	MaximumLevel         byte
	CooldownMilliseconds uint32
}

type experienceItem struct {
	levelTicket
	Kind  byte
	Value uint32
	Bands []struct {
		MinimumLevel, MaximumLevel byte
		Amount                     uint32
	}
	CooldownGroup  *uint32
	UsablePlaces   []string
	ImpossibleJobs uint16
	NativeBook     bool
}

type GrowthRequest struct {
	Opcode, Sequence, Slot      uint16
	List                        byte
	NativeReference, TemplateId uint32
}

func ReadGrowth(op, seq uint16, b []byte) (GrowthRequest, error) {
	r := GrowthRequest{Opcode: op, Sequence: seq}
	bad := errors.New("native growth item request boundary mismatch")
	switch op {
	case 30:
		if len(b) != 2 {
			return r, bad
		}
	case 410:
		if len(b) != 3 {
			return r, bad
		}
		r.List = b[2]
	case 44:
		if len(b) != 15 {
			return r, bad
		}
		r.List = b[2]
		r.NativeReference = little.Uint32(b[3:])
		r.TemplateId = little.Uint32(b[7:])
		r.Slot = little.Uint16(b)
		if (r.List != 0 && r.List != 7) || r.List == 7 && (r.Slot < 216 || r.Slot > 271) || r.NativeReference == 0 || r.TemplateId == 0 || r.TemplateId > math.MaxInt32 || little.Uint32(b[11:]) != 0 {
			return r, bad
		}
	default:
		return r, bad
	}
	r.Slot = little.Uint16(b)
	return r, nil
}

func (r GrowthRequest) Reply(used bool, award uint32, sp, tp uint16) []byte {
	b := []byte{0, 0, 0}
	if used {
		b = little.AppendUint16([]byte{1}, r.Slot)
	}
	if r.Opcode == 44 {
		b = append(b, r.List)
		b = little.AppendUint32(b, r.NativeReference)
		return little.AppendUint32(b, r.TemplateId)
	}
	if !used {
		return b
	}
	b = little.AppendUint32(append(b, r.List), award)
	b = little.AppendUint16(b, sp)
	return little.AppendUint16(b, tp)
}

type GrowthReplay struct {
	Request                 GrowthRequest
	Character               uint16
	Town, Area              uint16
	Scene                   string
	Revision, AssetRevision int64
}

func (r GrowthReplay) identity(c storage.Character, in storage.Inventory, req GrowthRequest, scene string) GrowthReplay {
	return GrowthReplay{req, c.ID, c.Position.Town, c.Position.Area, scene, in.Revision, in.AssetRevision}
}

type GrowthContext struct {
	Now          time.Time
	Place, Scene string
	TownValid    bool
	Range        func(byte) (uint64, uint64, error)
	Apply        func(storage.Character, uint32) (storage.Character, uint32, error)
	Replay       GrowthReplay
}

type GrowthResult struct {
	Mutation storage.TownQuestMutation
	Replay   GrowthReplay
	Repeated bool
	Award    uint32
	Sp, Tp   uint16
	Removed  []uint16
}

func (catalog *Catalog) HasExperience(template uint32) bool {
	_, ok := catalog.growth.Experience[template]
	return ok
}

func (catalog *Catalog) UseGrowth(ctx context.Context, c storage.Character, in storage.Inventory, req GrowthRequest, opts GrowthContext) (GrowthResult, error) {
	var out GrowthResult
	p, err := playerOf(c)
	if err != nil || !p.initialized || c.Job > 15 || c.Level == 0 || req.List != 0 || !opts.TownValid && opts.Place != "dungeon" || req.Opcode == 410 && opts.Place == "dungeon" {
		return out, storage.ErrInvalid
	}
	var progress struct {
		Hp         uint32
		Experience uint64
		Sp, Tp     uint16
	}
	var doc skillPage
	if json.Unmarshal(c.Gameplay, &doc) != nil || json.Unmarshal(doc["Progression"], &progress) != nil || progress.Hp == 0 {
		return out, storage.ErrInvalid
	}
	identity := opts.Replay.identity(c, in, req, opts.Scene)
	if opts.Replay == identity {
		out.Repeated = true
		return out, nil
	}
	i := slices.IndexFunc(in.Items, func(a storage.Asset) bool { return a.Location == 0 && a.Slot == req.Slot })
	if i < 0 || c.Level >= 100 {
		return out, storage.ErrInvalid
	}
	source := in.Items[i]
	minimum, threshold, err := opts.Range(c.Level)
	if err != nil || progress.Experience < minimum || progress.Experience >= threshold {
		return out, storage.ErrInvalid
	}
	def, exists := catalog.growth.Experience[source.TemplateID]
	if req.Opcode == 410 {
		lt, found := catalog.growth.LevelTickets[source.TemplateID]
		if !found || threshold-progress.Experience > math.MaxInt32 {
			return out, storage.ErrInvalid
		}
		def = experienceItem{levelTicket: lt, Value: uint32(threshold - progress.Experience), UsablePlaces: []string{"village", "seria room"}}
		exists = found
	}
	if !exists || def.NativeBook != (req.Opcode == 30) || req.Opcode == 44 && source.TemplateID != req.TemplateId {
		return out, storage.ErrInvalid
	}
	var spec grantTemplate
	var attrs struct{ ExpiresAt *int64 }
	if json.Unmarshal(def.Template, &spec) != nil || !spec.valid() || spec.Kind != 2 || spec.FirstBagSlot != 65 || spec.LastBagSlot != 120 || guidKey(source.UID) == "" ||
		c.Level < spec.MinimumLevel || c.Level > def.MaximumLevel || def.MaximumLevel > 100 || spec.UsableJobs&(1<<c.Job) == 0 || def.ImpossibleJobs&(1<<c.Job) != 0 ||
		!(source.Slot >= 3 && source.Slot <= 8 || source.Slot >= 65 && source.Slot <= min(120, 96+in.BagExpansion)) || !storage.QuestOrdinarySource(source, def.Template) ||
		json.Unmarshal(source.Attributes, &attrs) != nil || def.CooldownMilliseconds > math.MaxInt32 || len(def.UsablePlaces) > 0 && !slices.Contains(def.UsablePlaces, opts.Place) {
		return out, storage.ErrInvalid
	}
	now := opts.Now.UnixMilli()
	if now <= 0 || attrs.ExpiresAt != nil && *attrs.ExpiresAt <= now/1000 || spec.ResourceExpiration != nil && *spec.ResourceExpiration <= now/1000 {
		return out, storage.ErrInvalid
	}
	award, err := def.award(c.Level, threshold-minimum)
	if err != nil || progress.Experience > math.MaxUint64-uint64(award) {
		return out, storage.ErrInvalid
	}
	stamp := fmt.Sprintf("item:%d", spec.TemplateID)
	if def.CooldownGroup != nil {
		stamp = fmt.Sprintf("group:%d", *def.CooldownGroup)
	}
	cooldowns, err := growthCooldowns(doc, stamp, now, def.CooldownMilliseconds)
	if err != nil {
		return out, err
	}
	next, actual, err := opts.Apply(c, award)
	var saved struct {
		Sp, Tp     uint16
		Experience uint64
	}
	if err != nil || actual != award || json.Unmarshal(next.Gameplay, &doc) != nil || doc == nil || json.Unmarshal(doc["Progression"], &saved) != nil {
		return out, storage.ErrInvalid
	}
	doc["PotionCooldowns"], _ = json.Marshal(cooldowns)
	if req.Opcode == 410 {
		if next.Level != c.Level+1 || saved.Experience != threshold || saved.Sp < progress.Sp || saved.Tp < progress.Tp {
			return out, storage.ErrInvalid
		}
		b, ids, err := catalog.levelSideQuests(ctx, c)
		if err != nil {
			return out, err
		}
		if b != nil {
			doc["Quests"] = b
		}
		out.Removed = ids
		out.Sp, out.Tp = saved.Sp-progress.Sp, saved.Tp-progress.Tp
	}
	next.Gameplay, _ = json.Marshal(doc)
	inv, err := storage.QuestConsumeInstance(in, source.UID)
	if err != nil {
		return out, err
	}
	identity.Revision++
	out.Mutation.Reward = &storage.QuestRewardMutation{Character: next, Inventory: inv}
	out.Replay, out.Award = identity, award
	return out, nil
}

func validGrowthCooldown(key string) bool {
	if len(key) > 32 {
		return false
	}
	val, ok := strings.CutPrefix(key, "item:")
	if !ok {
		val, ok = strings.CutPrefix(key, "group:")
	}
	if !ok || val == "" || strings.ContainsFunc(val, func(r rune) bool { return r < '0' || r > '9' }) {
		return false
	}
	_, err := strconv.ParseUint(val, 10, 32)
	return err == nil
}

func growthCooldowns(doc skillPage, stamp string, now int64, duration uint32) (map[string]int64, error) {
	var cds map[string]int64
	if doc["PotionCooldowns"] != nil && json.Unmarshal(doc["PotionCooldowns"], &cds) != nil || len(cds) > 4096 {
		return nil, storage.ErrInvalid
	}
	if cds == nil {
		cds = map[string]int64{}
	}
	for key, end := range cds {
		if !validGrowthCooldown(key) || end < 0 {
			return nil, storage.ErrInvalid
		}
		if end <= now {
			delete(cds, key)
		}
	}
	if cds[stamp] > now {
		return nil, storage.ErrInvalid
	}
	if duration != 0 {
		if len(cds) >= 4096 || now > math.MaxInt64-int64(duration) {
			return nil, storage.ErrInvalid
		}
		cds[stamp] = now + int64(duration)
	}
	return cds, nil
}

func (d experienceItem) award(level byte, width uint64) (uint32, error) {
	if d.Kind == 2 {
		if d.Value != 0 || len(d.Bands) == 0 || len(d.Bands) > 100 {
			return 0, storage.ErrInvalid
		}
		prior := byte(0)
		var award uint32
		for _, x := range d.Bands {
			if x.MinimumLevel <= prior || x.MaximumLevel < x.MinimumLevel || x.MaximumLevel > 100 || x.Amount == 0 || x.Amount > math.MaxInt32 {
				return 0, storage.ErrInvalid
			}
			prior = x.MaximumLevel
			if level >= x.MinimumLevel && level <= x.MaximumLevel {
				award = x.Amount
			}
		}
		if award == 0 {
			return 0, storage.ErrInvalid
		}
		return award, nil
	}
	if d.Value == 0 || d.Value > math.MaxInt32 || len(d.Bands) != 0 {
		return 0, storage.ErrInvalid
	}
	if d.Kind == 0 {
		return d.Value, nil
	}
	hi, lo := bits.Mul64(width, uint64(d.Value))
	if d.Kind != 1 || hi != 0 || lo/100 > math.MaxUint32 {
		return 0, storage.ErrInvalid
	}
	return uint32(lo / 100), nil
}
