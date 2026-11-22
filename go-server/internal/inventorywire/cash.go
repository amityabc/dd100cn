package inventorywire

import (
	"bytes"
	"math"
	"slices"
	"time"
)

var serverTime = time.FixedZone("UTC+8", 8*3600)

func cashWindow(period int, now time.Time, previous Purchase) (start, reset int64) {
	local := now.In(serverTime)
	bound := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, serverTime)
	switch period {
	case 0:
		return 1, 0x7fff53f0
	case 1:
		if local.Before(bound) {
			bound = bound.AddDate(0, 0, -1)
		}
		return bound.Unix(), bound.AddDate(0, 0, 1).Unix()
	case 101:
		legacy := time.Unix(previous.PeriodStart, 0).In(serverTime)
		if previous.PeriodStart > 0 && previous.PeriodStart <= now.Unix() && now.Unix() < previous.PeriodStart+7*86400 && legacy.Weekday() == time.Thursday && legacy.Hour() == 6 && legacy.Minute() == 0 && legacy.Second() == 0 {
			return previous.PeriodStart, previous.PeriodStart + 7*86400
		}
		bound = bound.AddDate(0, 0, -((int(bound.Weekday()) + 6) % 7))
		if local.Before(bound) {
			bound = bound.AddDate(0, 0, -7)
		}
		return bound.Unix(), bound.AddDate(0, 0, 7).Unix()
	case 102:
		bound = time.Date(local.Year(), local.Month(), 1, 6, 0, 0, 0, serverTime)
		if local.Before(bound) {
			bound = bound.AddDate(0, -1, 0)
		}
		return bound.Unix(), bound.AddDate(0, 1, 0).Unix()
	}
	return 0, 0
}
func cashBody(o Options, personal map[uint32]Purchase) ([]byte, error) {
	if o.CashLimitsBody != nil {
		raw := o.CashLimitsBody
		if len(raw) < 4 || len(raw) > 4+4096*12 || int(le.Uint32(raw))*12 != len(raw)-4 {
			return nil, errShape
		}
		var last uint32
		for i := 4; i < len(raw); i += 12 {
			id, reset, left := le.Uint32(raw[i:]), le.Uint32(raw[i+4:]), le.Uint32(raw[i+8:])
			if id <= last || id > math.MaxInt32 || reset == 0 || reset > math.MaxInt32 || left > 255 {
				return nil, errShape
			}
			last = id
		}
		return bytes.Clone(raw), nil
	}
	if len(o.CashPolicies) == 0 {
		hasCashPurchase := false
		for id := range o.AccountPurchases {
			if id&0x80000000 == 0 {
				hasCashPurchase = true
				break
			}
		}
		if !hasCashPurchase {
			return nil, nil
		}
		// The C# overload without a catalog retains the schema-9 weekly limit.
		if o.CashPolicies == nil {
			o.CashPolicies = map[uint32]CashPolicy{310: {Limit: 1, Period: 101}}
		}
	}
	if len(o.CashPolicies) > 4096 {
		return nil, errShape
	}
	ids := make([]uint32, 0, len(o.CashPolicies))
	characterLimits := false
	for id, policy := range o.CashPolicies {
		ids = append(ids, id)
		characterLimits = characterLimits || policy.Character
	}
	if characterLimits {
		if len(personal) > 4096 {
			return nil, errShape
		}
		for id, p := range personal {
			if id == 0 || id > math.MaxInt32 || p.PeriodStart <= 0 || p.Count > 255 {
				return nil, errShape
			}
		}
	}
	slices.Sort(ids)
	w := writer{}
	w.u32(uint32(len(ids)))
	for _, id := range ids {
		p := o.CashPolicies[id]
		if id == 0 || id > math.MaxInt32 || p.Limit == 0 || p.Limit > 255 || (p.Character && p.Period != 0 && p.Period != 1) || (!p.Character && p.Period != 101 && p.Period != 102) {
			return nil, errShape
		}
		previous, found := o.AccountPurchases[id]
		if p.Character {
			previous, found = personal[id]
		}
		if found && (previous.PeriodStart <= 0 || previous.Count > 255) {
			return nil, errShape
		}
		start, reset := cashWindow(p.Period, o.Now, previous)
		if reset <= 0 || reset > math.MaxInt32 {
			return nil, errShape
		}
		left := p.Limit
		switch {
		case previous.PeriodStart > start:
			left = 0
		case previous.PeriodStart == start:
			left -= min(left, previous.Count)
		}
		w.u32(id)
		w.u32(uint32(reset))
		w.u32(left)
	}
	return w, nil
}
