package commercewire

import (
	"encoding/binary"
	"math"

	"s9a1/server/internal/storage"
)

var le = binary.LittleEndian

type Buy struct{ TemplateID, Count, Shop, NPC uint32 }

func ReadBuy(b []byte) (Buy, error) {
	if len(b) != 17 || b[16] != 0 {
		return Buy{}, storage.ErrInvalid
	}
	q := Buy{le.Uint32(b), le.Uint32(b[4:]), le.Uint32(b[8:]), le.Uint32(b[12:])}
	if q.TemplateID == 0 || q.TemplateID == math.MaxUint32 || q.Count > math.MaxUint16 {
		return Buy{}, storage.ErrInvalid
	}
	return q, nil
}

type Sale struct {
	Shop, NPC uint32
	Rows      []storage.CommerceSale
}

func ReadSale(b []byte) (Sale, error) {
	if len(b) < 9 || b[8] == 0 || len(b) != 9+11*int(b[8]) {
		return Sale{}, storage.ErrInvalid
	}
	q := Sale{Shop: le.Uint32(b[4:]), NPC: le.Uint32(b)}
	seen := map[uint16]bool{}
	for at := 9; at < len(b); at += 11 {
		list, slot, count, checksum := b[at], le.Uint16(b[at+1:]), le.Uint32(b[at+3:]), le.Uint32(b[at+7:])
		expected := (uint32(list) + uint32(slot) + count) * 2
		if list != 0 || (slot < 3 || slot > 344) && (slot < 363 || slot > 374) || count == 0 || seen[slot] ||
			checksum != expected && checksum != expected+1 {
			return Sale{}, storage.ErrInvalid
		}
		seen[slot] = true
		q.Rows = append(q.Rows, storage.CommerceSale{Slot: slot, Count: count})
	}
	return q, nil
}

func Bought(gold, medals, cera uint32, core []byte) []byte {
	b := le.AppendUint32([]byte{1}, min(gold, math.MaxInt32))
	b = le.AppendUint32(b, medals)
	b = le.AppendUint32(b, 0)
	b = le.AppendUint32(b, cera)
	b = append(b, core...)
	return append(b, make([]byte, 35)...)
}

func Sold(gold uint32, rows []storage.CommerceSale) []byte {
	b := le.AppendUint32([]byte{1}, min(gold, math.MaxInt32))
	b = le.AppendUint32(b, uint32(len(rows)))
	for _, row := range rows {
		b = le.AppendUint16(append(b, 0), row.Slot)
		b = le.AppendUint32(b, row.Count)
	}
	return b
}

type CashSelection struct {
	Commodity                            uint32
	AvatarFlag, AbilityID, PurchaseModel byte
	CouponTemplate                       uint32
	CouponSlot                           uint16
}

func (s CashSelection) Ordinary() bool {
	return s.AvatarFlag == 0 && s.AbilityID == 255 && (s.PurchaseModel == 0 || s.PurchaseModel == 255)
}

func ReadCash(b []byte) ([]CashSelection, error) {
	if len(b) < 4 || b[0] != 0 || b[1] != 0 || b[2] == 0 || b[3] > 1 || len(b) != 4+15*int(b[2]) {
		return nil, storage.ErrInvalid
	}
	rows := make([]CashSelection, int(b[2]))
	coupon := false
	for i := range rows {
		r := b[4+i*15:]
		s := CashSelection{Commodity: le.Uint32(r[3:]), AvatarFlag: r[0], AbilityID: r[1], PurchaseModel: r[2], CouponTemplate: le.Uint32(r[9:]), CouponSlot: le.Uint16(r[13:])}
		if s.AvatarFlag > 1 || r[7] != 0 || r[8] != 0 || s.Commodity == 0 || s.Commodity == math.MaxUint32 ||
			s.CouponTemplate == math.MaxUint32 || s.CouponTemplate == 0 && s.CouponSlot != 0 ||
			s.CouponTemplate != 0 && (s.AvatarFlag != 0 || (s.CouponSlot < 3 || s.CouponSlot > 8) && (s.CouponSlot < 65 || s.CouponSlot > 120)) {
			return nil, storage.ErrInvalid
		}
		coupon = coupon || s.CouponTemplate != 0
		rows[i] = s
	}
	if (b[3] == 1) != coupon {
		return nil, storage.ErrInvalid
	}
	return rows, nil
}

func CashBought(commodity, index, last uint32, category int32) []byte {
	b := le.AppendUint32([]byte{1, 0}, uint32(category))
	b = le.AppendUint32(b, commodity)
	b = le.AppendUint32(b, index)
	b = le.AppendUint32(b, last)
	return append(b, 0, 0, 0, 0, 0, 0)
}

func CashRejected(commodity uint32, code uint16) []byte {
	b := le.AppendUint16([]byte{0}, code)
	b = le.AppendUint32(append(b, 0), math.MaxUint32)
	b = le.AppendUint32(b, commodity)
	return append(b, make([]byte, 12)...)
}
