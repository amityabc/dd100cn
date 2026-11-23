package storage

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/big"
	"strings"
	"time"
)

type AvatarCashOffer struct {
	Template  json.RawMessage
	Cera      uint32
	Abilities map[byte]uint16
	Model     byte
}

type CashNameTag struct {
	Commodity, Cera, Days uint32
	Template              struct {
		TemplateID         uint32 `json:"TemplateId"`
		ResourceHash       string
		MinimumLevel       byte
		UsableJobs         uint16
		Visual             *NameTagVisual  `json:",omitempty"`
		Effects            *NameTagEffects `json:",omitempty"`
		UnavailableModules []string        `json:",omitempty"`
	}
}

type NameTagVisual struct {
	Image                   string
	ImageIndices, Offset    [3]int
	BarHeightRate, BarAlpha int
	BarOffset               [3]int
}

type NameTagEffects struct {
	PhysicalAttack, PhysicalDefense, MagicalAttack, MagicalDefense int
	AttackSpeed, CastSpeed, MoveSpeed                              int
}

type specialPurchaseTemplate struct {
	TemplateID                              uint32
	ResourceHash                            string
	Kind, MinimumLevel                      byte
	UsableJobs, WornSlot, MaximumDurability uint16
	RelativeLifetimeSeconds                 uint32
	InitialAvatarSocketCount                byte
	InitialAvatarSocketType                 uint16
	InitialAvatarSocketLayout               json.RawMessage
}

func (x *CommerceTx) loadSpecial() (map[string]json.RawMessage, []json.RawMessage, uint16, error) {
	var profile struct{ SpecialAssets map[string]json.RawMessage }
	if err := json.Unmarshal(x.Character.Gameplay, &profile); err != nil {
		return nil, nil, 0, err
	}
	m := profile.SpecialAssets
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	items := []json.RawMessage{}
	if raw := m["Items"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, nil, 0, err
		}
		if items == nil {
			return nil, nil, 0, ErrInvalid
		}
	}
	var expand uint16
	if raw := m["AvatarExpansion"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &expand); err != nil {
			return nil, nil, 0, err
		}
	}
	if expand > 360 || expand%8 != 0 || len(items) > 713 {
		return nil, nil, 0, ErrInvalid
	}
	return m, items, expand, nil
}

func validCashAvatar(c Character, line CashLine, offer AvatarCashOffer) bool {
	seconds := map[byte]uint32{1: 7 * 86400, 2: 30 * 86400, 3: 0}
	want, ok := seconds[line.PurchaseModel]
	if !ok || line.PurchaseModel != offer.Model || line.AbilityID == 255 || line.AvatarFlag != 0 ||
		line.CouponTemplate != 0 || line.CouponSlot != 0 || offer.Cera == 0 ||
		offer.Abilities[line.AbilityID]&(1<<c.Job) == 0 {
		return false
	}
	var t specialPurchaseTemplate
	var d materialDefinition
	if json.Unmarshal(offer.Template, &t) != nil || json.Unmarshal(offer.Template, &d) != nil ||
		t.Kind != 0 || t.MinimumLevel > c.Level || t.UsableJobs&(1<<c.Job) == 0 ||
		t.RelativeLifetimeSeconds != want || t.InitialAvatarSocketCount != 0 || t.InitialAvatarSocketType != 0 ||
		hasEffect(t.InitialAvatarSocketLayout) || d.ResourceExpiration != nil || d.DailyDeletion || d.WeeklyDeletionDay != nil || d.InitialSealed {
		return false
	}
	return true
}

func (x *CommerceTx) expandAvatars(next uint16) error {
	state, _, current, err := x.loadSpecial()
	if err != nil {
		return err
	}
	if next != current+8 || next > 360 {
		return ErrInvalid
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	state["AvatarExpansion"] = raw
	if len(state["Items"]) == 0 {
		state["Items"] = json.RawMessage("[]")
	}
	return replaceMailFields(&x.Character, map[string]any{"SpecialAssets": state})
}

func (x *CommerceTx) grantSpecial(template json.RawMessage, durability uint16, name string, now time.Time, forBuyer bool) error {
	_, err := x.grantSpecialWithQuality(template, durability, name, now, forBuyer, 0)
	return err
}

func (x *CommerceTx) grantSpecialWithQuality(template json.RawMessage, durability uint16, name string, now time.Time, forBuyer bool, fixedQuality uint32) (json.RawMessage, error) {
	var t specialPurchaseTemplate
	var lifetime materialDefinition
	if json.Unmarshal(template, &t) != nil || json.Unmarshal(template, &lifetime) != nil ||
		t.Kind > 2 || t.TemplateID == 0 || t.TemplateID == math.MaxUint32 || t.MinimumLevel == 0 || t.UsableJobs == 0 ||
		forBuyer && (t.MinimumLevel > x.Character.Level || t.UsableJobs&(1<<x.Character.Job) == 0) {
		return nil, ErrInvalid
	}
	state, items, expansion, err := x.loadSpecial()
	if err != nil {
		return nil, err
	}
	location, first, last := byte(1), uint16(0), 119+expansion
	switch t.Kind {
	case 0:
		if t.WornSlot > 11 || durability > 254 {
			return nil, ErrInvalid
		}
	case 1:
		if t.WornSlot != 26 || durability != 0 {
			return nil, ErrInvalid
		}
		location, last = 7, 159
	case 2:
		if t.WornSlot < 27 || t.WornSlot > 29 || durability > t.MaximumDurability {
			return nil, ErrInvalid
		}
		location, first, last = 7, 160, 215
	}
	occupied := map[uint16]bool{}
	for _, raw := range items {
		var item struct {
			Location byte
			Slot     uint16
		}
		if err = json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		if item.Location == location {
			occupied[item.Slot] = true
		}
	}
	slot := first
	for slot <= last && occupied[slot] {
		slot++
	}
	if slot > last {
		return nil, ErrCapacity
	}
	attributes, ordinary, err := commerceItemCore(ordinaryTemplate{Kind: 2, MaximumDurability: durability}, lifetime, now)
	if err != nil {
		return nil, err
	}
	core := make([]byte, 153)
	copy(core, ordinary)
	binary.LittleEndian.PutUint32(core[2:], t.TemplateID)
	binary.LittleEndian.PutUint16(core[11:], durability)
	if t.Kind != 1 {
		seed := fixedQuality
		if seed == 0 {
			quality, err := rand.Int(rand.Reader, big.NewInt(999999998))
			if err != nil {
				return nil, err
			}
			seed = uint32(quality.Uint64() + 1)
		}
		x.specialValue = seed
		binary.LittleEndian.PutUint32(core[6:], seed)
		var a map[string]json.RawMessage
		if err = json.Unmarshal(attributes, &a); err != nil {
			return nil, err
		}
		a["QualitySeed"], err = json.Marshal(seed)
		if err != nil {
			return nil, err
		}
		attributes, err = json.Marshal(a)
		if err != nil {
			return nil, err
		}
	}
	uid, err := newMailUUID()
	if err != nil {
		return nil, err
	}
	detail, err := newMailUUID()
	if err != nil {
		return nil, err
	}
	record := map[string]any{
		"InstanceId": materialGUID(uid), "Template": template, "Location": location, "Slot": slot,
		"Attributes": attributes, "Core153": legacyByteArray(core),
	}
	switch t.Kind {
	case 0:
		sockets, err := initialSpecialSockets(t)
		if err != nil {
			return nil, err
		}
		record["Avatar"] = map[string]any{"DetailId": materialGUID(detail), "Sockets": legacyByteArray(sockets), "Colors": []uint16{}, "Option": 0}
	case 1:
		serial, err := x.allocatePetSerial()
		if err != nil {
			return nil, err
		}
		x.specialValue = serial
		record["Pet"] = map[string]any{"DetailId": materialGUID(detail), "NativeSerial": serial, "Name": name,
			"Level": 1, "Exp": 0, "Satiety": 100, "SatietyMicros": 0, "ModeFlag": 0, "Evolution": 0, "EvolutionProgress": 0, "TailFlag": 0}
	case 2:
		record["Artifact"] = map[string]any{"DetailId": materialGUID(detail), "Color": t.WornSlot}
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	state["Items"], err = json.Marshal(append(items, raw))
	if err != nil {
		return nil, err
	}
	return raw, replaceMailFields(&x.Character, map[string]any{"SpecialAssets": state})
}

func (x *CommerceTx) allocatePetSerial() (uint32, error) {
	// Imported SQLite counters keep their next value. Existing JSON rows also
	// provide a floor so an older save cannot duplicate a visible pet serial.
	var next uint64
	err := x.tx.QueryRowContext(x.ctx, `SELECT greatest(1,coalesce(max((i.item->'Pet'->>'NativeSerial')::bigint),0)+1)
		FROM s9a1.characters c CROSS JOIN LATERAL json_array_elements(
			CASE WHEN json_typeof(c.gameplay_json->'SpecialAssets'->'Items')='array'
				THEN c.gameplay_json->'SpecialAssets'->'Items' ELSE '[]'::json END
		) i(item) WHERE c.account=$1`, x.account).Scan(&next)
	if err != nil {
		return 0, err
	}
	var id uint32
	err = x.tx.QueryRowContext(x.ctx, `UPDATE s9a1.accounts SET pet_serial_next=greatest(pet_serial_next,$2)+1
		WHERE account=$1 AND greatest(pet_serial_next,$2)<=4294967295 RETURNING pet_serial_next-1`, x.account, next).Scan(&id)
	if err == nil {
		x.Inventory.AssetRevision++
	}
	return id, err
}

func legacyByteArray(b []byte) []uint16 {
	out := make([]uint16, len(b))
	for i, v := range b {
		out[i] = uint16(v)
	}
	return out
}

func initialSpecialSockets(t specialPurchaseTemplate) ([]byte, error) {
	count := t.InitialAvatarSocketCount
	if count == 0 {
		if t.InitialAvatarSocketType != 0 || hasEffect(t.InitialAvatarSocketLayout) {
			return nil, ErrInvalid
		}
		return nil, nil
	}
	if count > 5 {
		return nil, ErrInvalid
	}
	types := [5]uint16{}
	if hasEffect(t.InitialAvatarSocketLayout) {
		var layout struct{ First, Second, Third, Fourth, Fifth uint16 }
		if t.InitialAvatarSocketType != 0 || json.Unmarshal(t.InitialAvatarSocketLayout, &layout) != nil {
			return nil, ErrInvalid
		}
		types = [5]uint16{layout.First, layout.Second, layout.Third, layout.Fourth, layout.Fifth}
	} else {
		for i := range count {
			types[i] = t.InitialAvatarSocketType
		}
	}
	b := make([]byte, 30)
	for i, v := range types[:count] {
		if v == 0 {
			return nil, ErrInvalid
		}
		binary.LittleEndian.PutUint16(b[6*i:], v)
		binary.LittleEndian.PutUint32(b[6*i+2:], math.MaxUint32)
	}
	return b, nil
}

func (x *CommerceTx) buyNameTag(o CashOffer, line CashLine, now time.Time) error {
	tag := o.NameTag
	t := tag.Template
	if line.PurchaseModel != 255 || tag.Commodity != o.Commodity || o.Count != 1 || tag.Cera != o.Cera ||
		t.TemplateID != o.TemplateID || tag.Days != 7 && tag.Days != 30 || t.MinimumLevel > x.Character.Level ||
		t.UsableJobs&(1<<x.Character.Job) == 0 {
		return ErrInvalid
	}
	type saved struct {
		TemplateId   uint32
		ExpiresAt    int64
		ResourceHash string
	}
	var p struct{ NameTag *saved }
	if err := json.Unmarshal(x.Character.Gameplay, &p); err != nil {
		return err
	}
	start := now.Unix()
	if p.NameTag != nil && p.NameTag.TemplateId == t.TemplateID && p.NameTag.ExpiresAt > start {
		if !strings.EqualFold(p.NameTag.ResourceHash, t.ResourceHash) {
			return ErrInvalid
		}
		start = p.NameTag.ExpiresAt
	}
	expires := start + int64(tag.Days)*86400
	if expires > math.MaxInt32 {
		return ErrInvalid
	}
	return replaceMailFields(&x.Character, map[string]any{"NameTag": saved{t.TemplateID, expires, t.ResourceHash}})
}
