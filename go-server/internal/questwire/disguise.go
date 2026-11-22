package questwire

import (
	"encoding/json"
	"errors"
	"math"
	"s9a1/server/internal/storage"
	"slices"
	"time"
)

const DisguiseBook = uint32(490708126)

type DisguiseData struct {
	Source json.RawMessage
	Forms  []struct {
		Kind     byte
		Template uint32
		Seconds  int64
	}
}

type DisguiseState struct {
	Lease                   storage.Lease
	Character               uint16
	Town, Area              uint16
	Use                     GrowthRequest
	Revision, AssetRevision int64
	UsedAt, EndsAt          int64
	Kind                    byte
	Form                    uint32
}

func (d DisguiseState) Owns(l storage.Lease, id uint16) bool {
	return d.Lease == l && id != 0 && d.Character == id
}
func (d DisguiseState) Visible(l storage.Lease, id uint16, town, area uint16, now time.Time) bool {
	return d.Owns(l, id) && d.Town == town && d.Area == area && d.Kind != 0 && d.EndsAt > now.Unix()
}

type DisguiseRequest struct {
	Kind         byte
	Form, Source uint32
}

func ReadDisguise(body []byte) (DisguiseRequest, error) {
	var q DisguiseRequest
	if len(body) != 9 {
		return q, errors.New("native disguise211 requires U8/U32/U32")
	}
	q = DisguiseRequest{body[0], little.Uint32(body[1:]), little.Uint32(body[5:])}
	if q.Kind < 1 || q.Kind > 3 || q.Form == 0 || q.Form > math.MaxInt32 || q.Source == 0 || q.Source > math.MaxInt32 {
		return q, storage.ErrInvalid
	}
	return q, nil
}

func (d *DisguiseData) Consume(l storage.Lease, c storage.Character, in storage.Inventory, req GrowthRequest, prior DisguiseState, now time.Time) (storage.TownQuestMutation, DisguiseState, bool, error) {
	var out storage.TownQuestMutation
	if d == nil || len(d.Forms) == 0 || req.List != 0 || req.NativeReference == 0 || req.TemplateId != DisguiseBook || now.Unix() <= 0 {
		return out, prior, false, storage.ErrInvalid
	}
	if prior.Owns(l, c.ID) && prior.Use == req && prior.Revision == in.Revision && prior.AssetRevision == in.AssetRevision {
		return out, prior, true, nil
	}
	var p struct {
		Initialized bool
		Progression struct{ Hp uint32 }
	}
	if json.Unmarshal(c.Gameplay, &p) != nil || !p.Initialized || p.Progression.Hp == 0 || c.Job > 15 {
		return out, prior, false, storage.ErrInvalid
	}
	i := slices.IndexFunc(in.Items, func(a storage.Asset) bool { return a.Location == 0 && a.Slot == req.Slot })
	if i < 0 {
		return out, prior, false, storage.ErrInvalid
	}
	a := in.Items[i]
	var spec grantTemplate
	var attrs struct{ ExpiresAt *int64 }
	if a.TemplateID != DisguiseBook || a.Count == 0 || guidKey(a.UID) == "" || json.Unmarshal(d.Source, &spec) != nil || !spec.valid() || spec.Kind != 2 || !storage.QuestOrdinarySource(a, d.Source) ||
		!(a.Slot >= 3 && a.Slot <= 8 || a.Slot >= spec.FirstBagSlot && a.Slot <= min(spec.LastBagSlot, spec.FirstBagSlot+31+in.BagExpansion)) ||
		c.Level < spec.MinimumLevel || spec.UsableJobs&(1<<c.Job) == 0 || json.Unmarshal(a.Attributes, &attrs) != nil || attrs.ExpiresAt != nil && *attrs.ExpiresAt <= now.Unix() ||
		spec.ResourceExpiration != nil && *spec.ResourceExpiration <= now.Unix() {
		return out, prior, false, storage.ErrInvalid
	}
	assets, err := storage.QuestConsumeInstance(in, a.UID)
	if err != nil {
		return out, prior, false, err
	}
	out.Reward = &storage.QuestRewardMutation{Character: c, Inventory: assets}
	return out, DisguiseState{Lease: l, Character: c.ID, Town: c.Position.Town, Area: c.Position.Area, Use: req, Revision: c.Revision + 1, AssetRevision: in.AssetRevision, UsedAt: now.Unix()}, false, nil
}

func (d *DisguiseData) Apply(l storage.Lease, c storage.Character, in storage.Inventory, req DisguiseRequest, prior DisguiseState, now time.Time) (DisguiseState, error) {
	if d == nil || !prior.Owns(l, c.ID) || c.Position.Town != prior.Town || c.Position.Area != prior.Area || req.Source != DisguiseBook || req.Source != prior.Use.TemplateId || now.Unix() < prior.UsedAt {
		return prior, storage.ErrInvalid
	}
	i := slices.IndexFunc(d.Forms, func(form struct {
		Kind     byte
		Template uint32
		Seconds  int64
	}) bool {
		return form.Kind == req.Kind && form.Template == req.Form
	})
	if i < 0 {
		return prior, storage.ErrInvalid
	}
	if prior.Kind != 0 {
		if prior.Kind != req.Kind || prior.Form != req.Form || prior.EndsAt <= now.Unix() {
			return prior, storage.ErrInvalid
		}
		return prior, nil
	}
	if prior.UsedAt <= 0 || now.Unix()-prior.UsedAt > 30 || prior.Revision != in.Revision || prior.AssetRevision != in.AssetRevision || d.Forms[i].Seconds <= 0 || d.Forms[i].Seconds > 2147483 {
		return prior, storage.ErrInvalid
	}
	prior.Kind, prior.Form, prior.EndsAt = req.Kind, req.Form, now.Unix()+d.Forms[i].Seconds
	return prior, nil
}
