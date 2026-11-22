package questwire

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"s9a1/server/internal/storage"
	"slices"
	"strings"
	"time"
)

type BattlePassRequest struct {
	Opcode       uint16
	Action, Tier byte
	ID           uint16
	Level        int
}

var ErrPassWire = errors.New("Invalid Battle Pass claim body")

func ReadBattlePass(op uint16, b []byte) (BattlePassRequest, error) {
	r := BattlePassRequest{Opcode: op}
	switch op {
	case 1767:
		if len(b) != 29 || b[13] > 1 || b[24] != 0 || little.Uint32(b[25:]) != 0 {
			return r, ErrPassWire
		}
		r.Action, r.ID = b[13], little.Uint16(b[14:])
		arg, slot := int32(little.Uint32(b[16:])), int32(little.Uint32(b[20:]))
		if r.ID == 0 || r.Action == 1 && (arg < 0 || arg > 2 || slot < 0 || slot > 5) || r.Action == 0 && (r.ID != 2 || arg <= 0 || slot != 0) {
			return r, ErrPassWire
		}
		if r.Action == 1 {
			r.Tier = byte(arg)
		}
	case 1768:
		if len(b) != 19 || b[13] > 2 || b[14] > 1 {
			return r, ErrPassWire
		}
		r.Action, r.Tier, r.Level = b[13], b[14], int(int32(little.Uint32(b[15:])))
		if r.Level < 1 || r.Level > 100 {
			return r, ErrPassWire
		}
	case 1772:
		if len(b) != 15 || b[13] > 2 || b[13] == 1 && b[14] > 2 || b[13] != 1 && b[14] != 0 {
			return r, ErrPassWire
		}
		r.Action = b[13]
	default:
		return r, ErrPassWire
	}
	return r, nil
}

func (r BattlePassRequest) Reply(ok bool, body []byte) []byte {
	if !ok {
		return []byte{0, 0, 0}
	}
	if r.Opcode == 1767 {
		return append([]byte{1}, body[13:]...)
	}
	return []byte{1}
}

func (d *BattlePassData) mission(raw json.RawMessage, r BattlePassRequest, now time.Time) (storage.BattlePassPlan, error) {
	var out storage.BattlePassPlan
	s, err := d.refresh(raw, now)
	if err != nil {
		return out, err
	}
	p := &s.MissionState
	if r.Action == 0 {
		def, ok := d.Definition.Missions[2]
		if !ok || def.EventType != 11 || def.Parameter != -1 {
			return out, storage.ErrInvalid
		}
		found, changed := false, false
		for _, rows := range [][]passProgress{p.Daily, p.Weekly} {
			for i := range rows {
				row := &rows[i]
				if row.Id != 2 {
					continue
				}
				found = true
				if !row.Claimed && row.Progress < def.Target {
					row.Progress++
					changed = true
				}
			}
		}
		if !found {
			return out, storage.ErrInvalid
		}
		if !changed {
			return out, nil
		}
	} else {
		var rows []passProgress
		switch r.Tier {
		case 0:
			rows = p.Daily
		case 1:
			rows = p.Weekly
		case 2:
			rows = p.Season
		default:
			return out, storage.ErrInvalid
		}
		i := slices.IndexFunc(rows, func(row passProgress) bool { return row.Id == r.ID })
		m, ok := d.Definition.Missions[r.ID]
		if i < 0 || !ok || rows[i].Claimed || rows[i].Progress != m.Target || r.Tier == 1 && p.WeeklyClaims >= d.Definition.WeeklyMissionRewardLimit {
			return out, storage.ErrInvalid
		}
		rows[i].Claimed = true
		if r.Tier == 0 {
			if p.SeasonDailyClaims == math.MaxInt32 {
				return out, storage.ErrInvalid
			}
			p.DailyClaims++
			p.SeasonDailyClaims++
		} else if r.Tier == 1 {
			if p.SeasonWeeklyClaims == math.MaxInt32 {
				return out, storage.ErrInvalid
			}
			p.WeeklyClaims++
			p.SeasonWeeklyClaims++
		}
		if r.Tier <= 1 {
			d.event(&s, storage.BattlePassEvent{Type: 36, Count: 1, Parameter: int(r.Tier)})
		}
		gain := int64(m.Experience)
		if r.Tier == 0 && s.advanced() {
			gain *= int64(d.Definition.GoldenMissionExpRatio)
		}
		if gain < 0 || gain > math.MaxInt32 {
			return out, storage.ErrInvalid
		}
		xp := int64(s.Experience) + gain
		for s.Level < 100 {
			need := int64(d.Definition.Experience[s.Level-1])
			if need <= 0 {
				return out, storage.ErrInvalid
			}
			if xp < need {
				break
			}
			xp -= need
			s.Level++
		}
		if xp > math.MaxInt32 {
			return out, storage.ErrInvalid
		}
		s.Experience = int(xp)
	}
	out.State, err = json.Marshal(s)
	return out, err
}

func (d *BattlePassData) Plan(r BattlePassRequest, in storage.Inventory, raw json.RawMessage, now time.Time) (storage.BattlePassPlan, error) {
	var out storage.BattlePassPlan
	if d == nil {
		return out, ErrPassUnavailable
	}
	if !d.Active(now) {
		return out, ErrPassClosed
	}
	if r.Opcode == 1772 {
		return out, nil
	}
	if r.Opcode == 1767 {
		return d.mission(raw, r, now)
	}
	s, err := d.bind(raw)
	if err != nil {
		return out, err
	}
	if r.Action > 2 || r.Tier > 1 || r.Level < 1 || r.Level > s.Level || r.Tier == 1 && !s.advanced() {
		return out, ErrPassQualification
	}
	var rewards []passReward
	if r.Action == 2 {
		b := d.Definition.MaximumBonus
		if b == nil {
			return out, errors.New("Battle Pass maximum-level reward is absent")
		}
		if s.Level != 100 || b.Experience <= 0 || s.Experience < b.Experience {
			return out, ErrPassMax
		}
		s.Experience -= b.Experience
		rewards = append(rewards, passReward{b.Template, b.Count, r.Tier})
	} else {
		bits, _ := hex.DecodeString(s.Claims)
		first, tierFirst, tierLast := r.Level, r.Tier, r.Tier
		if r.Action == 1 {
			first, tierFirst, tierLast = 1, 0, 0
			if s.advanced() {
				tierLast = 1
			}
		}
		for level := first; level <= r.Level; level++ {
			for tier := tierFirst; tier <= tierLast; tier++ {
				bit := int(tier)*112 + level - 1
				if bits[bit/8]&(1<<(bit%8)) != 0 {
					continue
				}
				prev := len(rewards)
				for _, reward := range d.Definition.Rewards[level] {
					if reward.Tier == tier {
						rewards = append(rewards, reward)
					}
				}
				if len(rewards) > prev {
					bits[bit/8] |= 1 << (bit % 8)
				}
			}
		}
		if len(rewards) == 0 {
			return out, ErrPassClaimed
		}
		s.Claims = strings.ToUpper(hex.EncodeToString(bits))
	}
	var grants []storage.QuestGrant
	for _, reward := range rewards {
		asset, _, err := rewardAsset(in, uint16(r.Level), s.Season, Material{reward.Template, reward.Count}, d.Templates[reward.Template], now)
		if err != nil {
			return out, err
		}
		if asset.Kind != 2 {
			return out, errors.New("Battle Pass equipment reward needs an explicit instance planner")
		}
		grants = append(grants, asset)
	}
	next, err := storage.QuestLoot(in, nil, grants, in.Gold, now)
	if err != nil {
		return out, err
	}
	out.State, err = json.Marshal(s)
	out.Inventory = &next
	return out, err
}

func (d *BattlePassData) Projection(raw, previous json.RawMessage) ([]Packet, error) {
	s, err := readPass(raw)
	if d != nil {
		s, err = d.bind(raw)
	}
	if err != nil {
		return nil, err
	}
	b := make([]byte, 21)
	if s.advanced() {
		b[0] = 1
	}
	little.PutUint32(b[4:], uint32(s.Level))
	little.PutUint32(b[8:], uint32(s.Experience))
	var packets []Packet
	if d != nil && s.advanced() {
		prev, _ := readPass(previous)
		var codes []uint32
		if s.Deluxe && !prev.Deluxe {
			codes = append(codes, d.Definition.BuyCode)
		}
		if !prev.advanced() {
			codes = append(codes, d.Definition.BuyCode+1)
		}
		if len(codes) != 0 {
			body := little.AppendUint16([]byte{0}, uint16(len(codes)))
			for _, c := range codes {
				if c == 0 || c > math.MaxInt32 {
					return nil, storage.ErrInvalid
				}
				body = little.AppendUint32(body, c)
			}
			packets = append(packets, Packet{Opcode: 68, Body: body})
		}
	}
	packets = append(packets, Packet{Opcode: 1850, Body: b})
	p := s.MissionState
	if d != nil && len(p.Daily) == 6 && len(p.Weekly) == 6 && len(p.Season) == 30 {
		body := make([]byte, 505)
		at := 0
		for _, rows := range [][]passProgress{p.Daily, p.Weekly, p.Season} {
			for _, r := range rows {
				m := d.Definition.Missions[r.Id]
				little.PutUint16(body[at:], r.Id)
				little.PutUint32(body[at+4:], uint32(r.Progress))
				if r.Progress >= m.Target {
					body[at+8] = 1
				}
				if r.Claimed {
					body[at+9] = 1
				}
				at += 12
			}
		}
		if p.WeeklyClaims >= d.Definition.WeeklyMissionRewardLimit {
			body[504] = 1
		}
		packets = append(packets, Packet{Opcode: 1851, Body: body})
	}
	claims, _ := hex.DecodeString(s.Claims)
	return append(packets, Packet{Opcode: 1852, Body: claims}), nil
}
