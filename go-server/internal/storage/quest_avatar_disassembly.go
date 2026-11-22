package storage

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"slices"
	"time"
)

type AvatarDisassemblyCatalog struct {
	Targets map[uint32]struct {
		ResourceHash string
		Points       int
	}
	Emblems map[uint32]int
	Tiers   []struct {
		Minimum, Maximum int
		Boosters         []uint32
	}
	Nodes map[uint32]rewardGraphNode
}

func (x *CommerceTx) DisassembleAvatar(slot uint16, id uint32, c AvatarDisassemblyCatalog, now time.Time) error {
	var p struct{ Progression struct{ Hp uint32 } }
	if json.Unmarshal(x.Character.Gameplay, &p) != nil || p.Progression.Hp == 0 || now.Unix() <= 0 {
		return ErrInvalid
	}
	state, items, _, err := x.loadSpecial()
	if err != nil {
		return err
	}
	i, err := specialSlot(items, 1, slot)
	if err != nil {
		return err
	}
	d, ok := c.Targets[id]
	var a editableAvatar
	var attrs materialAttributes
	if !ok || json.Unmarshal(items[i], &a) != nil || json.Unmarshal(a.Attributes, &attrs) != nil ||
		a.Template.TemplateID != id || a.Template.ResourceHash != d.ResourceHash || a.Template.Kind != 0 || a.Avatar == nil ||
		attrs.Locked || attrs.Binding != 0 || attrs.ExpiresAt != nil || a.Template.InitialSealed || a.Template.ResourceExpiration != nil ||
		a.Template.RelativeLifetimeSeconds != 0 || a.Template.DailyDeletion || a.Template.WeeklyDeletionDay != nil ||
		len(a.Avatar.Sockets)%6 != 0 || len(a.Avatar.Sockets) > 30 {
		return ErrInvalid
	}
	points, top := d.Points, 0
	for at := 0; at < len(a.Avatar.Sockets); at += 6 {
		embedded := binary.LittleEndian.Uint32(a.Avatar.Sockets[at+2:])
		if embedded == 0 || embedded == math.MaxUint32 {
			continue
		}
		add, ok := c.Emblems[embedded]
		if !ok {
			return ErrInvalid
		}
		points += add
	}
	for _, t := range c.Tiers {
		top = max(top, t.Maximum)
	}
	score := min(points, top)
	var roots []uint32
	for _, t := range c.Tiers {
		if t.Minimum <= score && score <= t.Maximum {
			if len(roots) != 0 {
				return ErrInvalid
			}
			roots = t.Boosters
		}
	}
	rewards, err := x.drawRewardGraph(roots, c.Nodes, 255)
	if err != nil {
		return err
	}
	for _, r := range rewards {
		if err = x.grantCommerceItem(r.Template, r.Count, now, false); err != nil {
			return err
		}
	}
	if len(x.Grants) > 255 {
		return ErrInvalid
	}
	state["Items"], err = json.Marshal(slices.Delete(items, i, i+1))
	if err != nil {
		return err
	}
	return replaceMailFields(&x.Character, map[string]any{"SpecialAssets": state})
}
