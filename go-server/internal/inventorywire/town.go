// Package inventorywire projects saved items into the original town inventory notifications.
package inventorywire

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"s9a1/server/internal/storage"
)

type Packet struct {
	Opcode uint16
	Body   []byte
}
type CashPolicy struct {
	Limit     uint32
	Period    int
	Character bool
}
type Purchase = storage.CommercePurchase
type Options struct {
	Now              time.Time
	CashPolicies     map[uint32]CashPolicy
	AccountPurchases map[uint32]Purchase
	CashLimitsBody   []byte
	PetDefaultName   func(template uint32, resourceHash string) (string, bool)
}
type writer []byte

func (w *writer) byte(v byte)    { *w = append(*w, v) }
func (w *writer) u16(v uint16)   { *w = binary.LittleEndian.AppendUint16(*w, v) }
func (w *writer) u32(v uint32)   { *w = binary.LittleEndian.AppendUint32(*w, v) }
func (w *writer) raw(b []byte)   { *w = append(*w, b...) }
func (w *writer) block(b []byte) { w.u32(uint32(len(b))); w.raw(b) }

var le = binary.LittleEndian
var errShape = errors.New("saved asset is inconsistent with its native container")
var wear = []uint16{12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 32}
var sharedMaterials = [...]uint32{3033, 3034, 3035, 3036, 3037, 3262, 10100115, 10100116, 10099773, 10099774, 10099775, 10158124}

type sealOption struct{ ID, FirstRatio, SecondRatio byte }
type attributes struct {
	QualitySeed                                      uint32
	Durability, AmplificationValue                   uint16
	Reinforcement, Forge, AmplificationType, Binding byte
	InitialAmplification                             bool
	ExpiresAt                                        *int64
	MagicSeal                                        *struct {
		First, Second, Third *sealOption
		Seed                 byte
	}
}

func (a attributes) equip(core []byte) {
	core[10] = a.Reinforcement
	le.PutUint16(core[11:], a.Durability)
	core[13] = a.Binding
	if a.InitialAmplification && a.AmplificationType&0x80 != 0 {
		a.AmplificationType = 0x80
		a.AmplificationValue = 0
	}
	core[19] = a.AmplificationType
	le.PutUint16(core[20:], a.AmplificationValue)
	core[0x57] = a.Forge
}
func (a attributes) seal(core []byte) error {
	s := a.MagicSeal
	if s == nil {
		return nil
	}
	if s.First == nil || !bytes.Equal(core[0x47:0x57], make([]byte, 16)) {
		return errShape
	}
	seen := map[byte]bool{}
	for _, o := range []*sealOption{s.First, s.Second, s.Third} {
		if o == nil {
			continue
		}
		if o.ID == 0 || seen[o.ID] || o.FirstRatio > 100 || o.SecondRatio > 100 {
			return errShape
		}
		n := len(seen)
		seen[o.ID] = true
		core[0x48+n], core[0x4b+n], core[0x4e+n] = o.ID, o.FirstRatio, o.SecondRatio
	}
	core[0x47], core[0x51], core[0x52] = byte(len(seen)), s.Seed, 255
	return nil
}

type row struct {
	slot uint16
	body []byte
}

func empty(slot uint16) []byte {
	b := make([]byte, 119)
	le.PutUint16(b, slot)
	le.PutUint32(b[2:], math.MaxUint32)
	return b
}
func walletRow(slot uint16, v uint32) []byte {
	b := make([]byte, 119)
	le.PutUint16(b, slot)
	le.PutUint32(b[2:], uint32(slot))
	le.PutUint32(b[6:], v)
	return b
}
func fixedRows(slots []uint16, values map[uint16][]byte) []byte {
	w := writer{3}
	w.u16(uint16(len(slots)))
	for _, slot := range slots {
		if b, ok := values[slot]; ok {
			w.raw(b)
		} else {
			w.raw(empty(slot))
		}
	}
	return w
}
func appendRows(w writer, rows []row) []byte {
	for _, r := range rows {
		w.raw(r.body)
	}
	return w
}
func counted(list byte, rows []row) []byte {
	w := writer{list}
	w.u16(uint16(len(rows)))
	return appendRows(w, rows)
}

type template struct {
	TemplateID                                            uint32
	Kind                                                  uint8
	FirstBagSlot, LastBagSlot                             uint16
	WornSlot                                              uint16
	WornSlotIsNative                                      bool
	PetConsumable, AccountCommonInventory, FateCollection bool
}

func ordinaryRow(a storage.Asset, c storage.Character) (row, template, error) {
	var t template
	var attr attributes
	if len(a.Core) != 119 || a.TemplateID == 0 || a.TemplateID == math.MaxUint32 || a.Count == 0 {
		return row{}, t, errShape
	}
	if a.Source == "weapon" {
		if a.Weapon == nil || a.Count != 1 {
			return row{}, t, errShape
		}
		t = template{TemplateID: a.TemplateID, Kind: 0}
		attr = attributes{QualitySeed: a.Weapon.QualitySeed, Durability: a.Weapon.Durability, Reinforcement: a.Core[10], Forge: a.Core[0x57], Binding: a.Core[13], AmplificationType: a.Core[19], AmplificationValue: le.Uint16(a.Core[20:])}
	} else if a.Source != "item" && a.Source != "shared" {
		return row{}, t, errShape
	}
	if len(a.Template) > 0 && !bytes.Equal(bytes.TrimSpace(a.Template), []byte("null")) {
		if err := json.Unmarshal(a.Template, &t); err != nil {
			return row{}, t, err
		}
	}
	if t.TemplateID != a.TemplateID || t.Kind > 2 || (a.Source == "weapon" && t.Kind != 0) {
		return row{}, t, errShape
	}
	if len(a.Attributes) > 0 && !bytes.Equal(bytes.TrimSpace(a.Attributes), []byte("null")) {
		if err := json.Unmarshal(a.Attributes, &attr); err != nil {
			return row{}, t, err
		}
	}
	value := a.Count
	core := bytes.Clone(a.Core)
	if t.Kind != 2 {
		if a.Count != 1 || attr.QualitySeed == 0 || attr.QualitySeed >= 999999999 {
			return row{}, t, errShape
		}
		value = attr.QualitySeed
		attr.equip(core)
	}
	if err := attr.seal(core); err != nil {
		return row{}, t, err
	}
	if a.Location == 3 {
		e, err := a.WornEquipment(c)
		if err != nil {
			return row{}, t, err
		}
		if e.QualitySeed != value {
			return row{}, t, errShape
		}
	}
	le.PutUint16(core, a.Slot)
	le.PutUint32(core[2:], a.TemplateID)
	le.PutUint32(core[6:], value)
	return row{a.Slot, core}, t, nil
}

func OrdinaryWornUpdate(c storage.Character, a storage.Asset) ([]byte, error) {
	if a.Location != 3 || a.Slot < 12 || a.Slot > 25 && a.Slot != 32 {
		return nil, errShape
	}
	r, t, err := ordinaryRow(a, c)
	if err != nil {
		return nil, err
	}
	if t.Kind == 2 {
		return nil, errShape
	}
	return append([]byte{3, 1, 0}, r.body...), nil
}

// MailItem119 is the original commerce projection for a complete escrow asset.
// The mail reader specifies durability again for both equipment and stackables.
func MailItem119(a storage.Asset) ([]byte, error) {
	a.Location = 0
	projected, _, err := ordinaryRow(a, storage.Character{})
	if err != nil {
		return nil, err
	}
	var saved struct{ Durability uint16 }
	if err = json.Unmarshal(a.Attributes, &saved); err != nil {
		return nil, err
	}
	le.PutUint16(projected.body[11:], saved.Durability)
	return projected.body, nil
}

func bagSlot(s uint16, t template) bool {
	if s >= 3 && s <= 344 {
		return true
	}
	if t.Kind != 2 {
		return false
	}
	if s >= 345 && s <= 360 {
		return (t.FirstBagSlot == 289 && t.LastBagSlot == 360) || (t.TemplateID >= 2500000 && t.TemplateID <= 2530004 && t.TemplateID%10000 <= 4)
	}
	return s >= 363 && s <= 374 && t.TemplateID == sharedMaterials[s-363]
}
func ordinary(in storage.Inventory, c storage.Character) (map[byte][]row, error) {
	if in.CharacterID == 0 || in.CharacterID != c.ID || in.Gold < 0 || in.Gold > math.MaxUint32 || in.Coins < 0 || in.Coins > math.MaxUint32 || in.CargoGold < 0 || in.CargoGold > math.MaxInt32 || in.BagExpansion > 24 || in.BagExpansion%8 != 0 || in.PersonalCapacity > 264 || in.CargoCapacity > 256 {
		return nil, errShape
	}
	lists := map[byte][]row{}
	seen := map[[2]uint16]bool{}
	for _, a := range in.Items {
		if a.CharacterID != c.ID && a.CharacterID != 0 {
			return nil, errShape
		}
		shared := a.Source == "shared" || a.Location == 12 || a.Location == 44 || a.Location == 45
		if shared != (a.CharacterID == 0) {
			return nil, errShape
		}
		r, t, err := ordinaryRow(a, c)
		if err != nil {
			return nil, fmt.Errorf("container %d slot %d: %w", a.Location, a.Slot, err)
		}
		s := a.Slot
		if a.Source == "shared" && (a.Location != 0 || s < 363 || s > 374 || t.Kind != 2 || a.TemplateID != sharedMaterials[s-363]) {
			return nil, errShape
		}
		ok := false
		switch a.Location {
		case 0:
			ok = bagSlot(s, t)
		case 2:
			ok = s < in.PersonalCapacity
		case 3:
			ok = t.Kind != 2 && slices.Contains(wear, s)
		case 7:
			ok = t.PetConsumable && t.Kind == 2 && s >= 216 && s <= 271
		case 12:
			ok = s < in.CargoCapacity
		case 38:
			ok = t.Kind == 1 && t.WornSlotIsNative && t.WornSlot == 32 && s <= 55
		case 44:
			ok = t.FateCollection && t.Kind == 2 && s < 24 && a.Count == 1
		case 45:
			ok = t.AccountCommonInventory && t.Kind == 2 && s < 50 && a.Count <= math.MaxInt32
		}
		key := [2]uint16{uint16(a.Location), s}
		if !ok || seen[key] {
			return nil, fmt.Errorf("container %d slot %d: %w", a.Location, s, errShape)
		}
		seen[key] = true
		lists[a.Location] = append(lists[a.Location], r)
	}
	for _, rows := range lists {
		slices.SortFunc(rows, func(a, b row) int { return int(a.slot) - int(b.slot) })
	}
	return lists, nil
}

func bagAndWear(in storage.Inventory, lists map[byte][]row) []Packet {
	w := writer{0}
	w.u16(in.BagExpansion)
	w.u16(uint16(2 + len(lists[0])))
	w.raw(walletRow(0, uint32(min(in.Gold, math.MaxInt32))))
	w.raw(walletRow(1, uint32(in.Coins)))
	worn := map[uint16][]byte{}
	for _, r := range lists[3] {
		worn[r.slot] = r.body
	}
	return []Packet{{13, appendRows(w, lists[0])}, {14, fixedRows(wear, worn)}}
}

// BagAndWear builds only the two inventory lists projected by an ordinary C19 move.
func BagAndWear(in storage.Inventory, c storage.Character) ([]Packet, error) {
	assets := make([]storage.Asset, 0, len(in.Items))
	for _, asset := range in.Items {
		if asset.Location == 0 || asset.Location == 3 {
			assets = append(assets, asset)
		}
	}
	in.Items = assets
	lists, err := ordinary(in, c)
	if err != nil {
		return nil, err
	}
	return bagAndWear(in, lists), nil
}

func OrdinaryBag(in storage.Inventory, c storage.Character) ([]byte, error) {
	in.Items = slices.DeleteFunc(slices.Clone(in.Items), func(a storage.Asset) bool { return a.Location != 0 })
	lists, err := ordinary(in, c)
	if err != nil {
		return nil, err
	}
	return bagAndWear(in, lists)[0].Body, nil
}

// NewBagItem is the original acquisition delta when one previously empty slot
// receives an item. It carries the absolute gold, coin and changed item rows.
func NewBagItem(in storage.Inventory, c storage.Character, a storage.Asset) (Packet, error) {
	if a.Location != 0 || a.CharacterID != c.ID || in.CharacterID != c.ID || a.UID == "" || in.Gold < 0 || in.Gold > math.MaxUint32 || in.Coins < 0 || in.Coins > math.MaxUint32 {
		return Packet{}, errShape
	}
	r, _, err := ordinaryRow(a, c)
	if err != nil {
		return Packet{}, err
	}
	w := writer{0}
	w.u16(3)
	w.raw(walletRow(0, uint32(min(in.Gold, math.MaxInt32))))
	w.raw(walletRow(1, uint32(in.Coins)))
	w.raw(r.body)
	return Packet{14, w}, nil
}

// Town returns the two ordered notification groups sent by LocalAssetsModule and LocalSpecialAssetsModule.
func Town(in storage.Inventory, c storage.Character, opts Options) (regular, special []Packet, err error) {
	lists, err := ordinary(in, c)
	if err != nil {
		return nil, nil, err
	}
	var game struct {
		SpecialAssets          specialState
		PetSkinUnlocked        bool
		CharacterCashPurchases map[uint32]Purchase
	}
	if err = json.Unmarshal(c.Gameplay, &game); err != nil {
		return nil, nil, err
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	special, err = specialPackets(game.SpecialAssets, game.PetSkinUnlocked, lists[7], opts)
	if err != nil {
		return nil, nil, err
	}
	wallet := writer{1}
	wallet.u32(uint32(max(0, min(in.Cera, math.MaxInt32))))
	wallet.u32(0)
	wallet.u32(0)
	regular = append(bagAndWear(in, lists), Packet{53, wallet})
	if len(lists[7]) > 0 {
		regular = append(regular, special[1])
	}
	cash, err := cashBody(opts, game.CharacterCashPurchases)
	if err != nil {
		return nil, nil, err
	}
	if len(cash) > 0 {
		regular = append(regular, Packet{851, cash})
	}
	if in.PersonalCapacity > 0 {
		w := writer{2}
		w.u16(in.PersonalCapacity)
		w.u16(uint16(len(lists[2])))
		regular = append(regular, Packet{13, append(appendRows(w, lists[2]), 0)})
	}
	if in.CargoCapacity > 0 {
		w := writer{12}
		w.u16(in.CargoCapacity)
		w.u32(uint32(in.CargoGold))
		w.u16(uint16(len(lists[12])))
		regular = append(regular, Packet{13, appendRows(w, lists[12])})
	}
	for _, list := range []byte{45, 38} {
		if len(lists[list]) > 0 {
			regular = append(regular, Packet{13, counted(list, lists[list])})
		}
	}
	return regular, special, nil
}

// FateContainers runs at the Fate module's own bootstrap point, after the ordinary/special town groups.
func FateContainers(in storage.Inventory, c storage.Character) ([]Packet, error) {
	rows, err := ordinary(in, c)
	if err != nil {
		return nil, err
	}
	result := []Packet{{13, counted(44, rows[44])}}
	if len(rows[45]) == 0 {
		result = append(result, Packet{13, counted(45, nil)})
	}
	return result, nil
}
