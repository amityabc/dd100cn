package fate

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"maps"
	"math"
	"math/big"
	"slices"
	"time"
)

func Random(n int) (int, error) {
	if n <= 0 {
		return 0, ErrInvalid
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}
func (d *Definition) ChoosePresets() ([]int, error) {
	options := []int{}
	for _, l := range d.Layouts {
		if l.Grid == d.DefaultGrid {
			options = append(options, l.Preset)
		}
	}
	slices.Sort(options)
	presets := make([]int, 0, 10)
	for range 5 {
		n, err := Random(len(options))
		if err != nil {
			return nil, err
		}
		presets = append(presets, d.DefaultGrid, options[n])
	}
	return presets, nil
}
func (d *Definition) Start(id string, presets []int) (*Run, error) {
	if len(presets) != 10 {
		return nil, ErrInvalid
	}
	r := &Run{RunId: id, ResourceHash: d.ResourceHash, EventId: d.EventId, DungeonId: d.DungeonId,
		Presets: slices.Clone(presets), PhaseRewards: d.Rewards, Collection: d.Collection, ShopProducts: d.ShopProducts,
		ShopNodes: []string{}, CompletedNodes: map[string]string{}, CompletedPhases: []int{}, Drops: []Drop{}, PendingRewards: []Reward{}, DeliveredRewards: []Reward{}}
	for phase := 0; phase < 5; phase++ {
		layout := d.layout(presets[phase*2], presets[phase*2+1])
		if layout == nil {
			return nil, ErrInvalid
		}
		for _, n := range layout.Nodes {
			if slices.Contains(d.ShopNodeTypes, n.Type) {
				r.ShopNodes = append(r.ShopNodes, fmt.Sprintf("%d:%d", phase, n.Id))
			}
		}
	}
	slices.Sort(r.ShopNodes)
	if !d.validRun(r) {
		return nil, ErrInvalid
	}
	return r, nil
}

func eventDate(now time.Time) time.Time { return now.UTC().Add(2 * time.Hour) }
func dayKey(now time.Time) string       { return eventDate(now).Format("20060102") }
func weekKey(now time.Time) string {
	t := eventDate(now)
	return t.AddDate(0, 0, -(int(t.Weekday())-int(time.Thursday)+7)%7).Format("20060102")
}

type Visit struct {
	Phase, Node int
	Body        string
}

func ReadVisit(b []byte) (Visit, error) {
	if len(b) != 19 && len(b) != 24 || !zeros(b[19:]) || b[13] > 4 {
		return Visit{}, ErrPacket
	}
	return Visit{int(b[13]), int(b[14]), base64.StdEncoding.EncodeToString(b)}, nil
}
func (v Visit) Key() string { return fmt.Sprintf("%d:%d", v.Phase, v.Node) }
func (d *Definition) ShopNode(r *Run) bool {
	if r == nil || r.LastNode == nil || r.Phase < 0 || r.Phase > 4 || len(r.Presets) != 10 {
		return false
	}
	l := d.layout(r.Presets[r.Phase*2], r.Presets[r.Phase*2+1])
	if l == nil {
		return false
	}
	n, ok := l.Nodes[*r.LastNode]
	return ok && slices.Contains(d.ShopNodeTypes, n.Type)
}

func (d *Definition) Visit(p Profile, v Visit, now time.Time) (Profile, int, bool, error) {
	if d.Validate(p) != nil || p.Run == nil || v.Phase < 0 || v.Phase > 4 {
		return p, 0, false, ErrInvalid
	}
	r := *p.Run
	l := d.layout(r.Presets[2*v.Phase], r.Presets[2*v.Phase+1])
	if l == nil {
		return p, 0, false, ErrInvalid
	}
	n, ok := l.Nodes[v.Node]
	if !ok {
		return p, 0, false, ErrInvalid
	}
	if prev, ok := r.CompletedNodes[v.Key()]; ok {
		if prev != v.Body {
			return p, 0, false, ErrInvalid
		}
		return p, n.Type, true, nil
	}
	if r.Dead || r.Settled {
		return p, 0, false, ErrInvalid
	}
	complete := slices.Contains(r.CompletedPhases, r.Phase)
	if v.Phase == r.Phase+1 && complete {
		if v.Node != 0 && !slices.Contains(l.Nodes[0].Next, v.Node) {
			return p, 0, false, ErrInvalid
		}
	} else if v.Phase != r.Phase || complete || r.LastNode == nil && v.Node != 0 || r.LastNode != nil && !slices.Contains(l.Nodes[*r.LastNode].Next, v.Node) {
		return p, 0, false, ErrInvalid
	}
	values := d.Points[n.Type]
	if len(values) != 5 || values[v.Phase] < 0 || values[v.Phase] > int(d.PointLimit) {
		return p, 0, false, ErrInvalid
	}
	points := min(int32(values[v.Phase]), d.PointLimit-p.ArchangelPoints)
	if int64(r.EarnedPoints)+int64(points) > math.MaxInt32 {
		return p, 0, false, ErrInvalid
	}
	r.EarnedPoints += points
	p.ArchangelPoints += points
	week := weekKey(now)
	if (n.Type == 3 || n.Type == 4) && p.StoryCount < int32(d.StoryCapacity) && p.StoryWeek != week {
		p.StoryCount++
		p.StoryWeek = week
	}
	r.Phase, r.LastNode = v.Phase, &v.Node
	r.CompletedNodes = maps.Clone(r.CompletedNodes)
	if r.CompletedNodes == nil {
		r.CompletedNodes = map[string]string{}
	}
	r.CompletedNodes[v.Key()] = v.Body
	if n.Type == 3 {
		r.CompletedPhases = append(slices.Clone(r.CompletedPhases), v.Phase)
		slices.Sort(r.CompletedPhases)
		r.PendingRewards = make([]Reward, 0, len(r.CompletedPhases))
		for _, i := range r.CompletedPhases {
			if i >= len(d.Rewards) {
				return p, 0, false, ErrInvalid
			}
			r.PendingRewards = append(r.PendingRewards, d.Rewards[i])
		}
	}
	p.Run = &r
	return p, n.Type, false, d.Validate(p)
}
func (d *Definition) RollEffect(p Profile) (*CollectionItem, error) {
	if d.DropWeight <= 0 || d.NoDropWeight <= 0 || d.SinPointLimit <= 0 || len(d.Collection) != 24 {
		return nil, ErrInvalid
	}
	sin := int32(0)
	for i, r := range p.Sins {
		sin += r * d.Sins[i].Cost
	}
	total := (d.DropWeight + d.NoDropWeight) * int(d.SinPointLimit)
	threshold := d.DropWeight*int(d.SinPointLimit) + d.NoDropWeight*int(max(0, min(sin, d.SinPointLimit)))
	r, err := Random(total)
	if err != nil || r >= threshold {
		return nil, err
	}
	i, err := Random(len(d.Collection))
	if err != nil {
		return nil, err
	}
	return &d.Collection[i], nil
}

func (d *Definition) Settle(p Profile, now time.Time, forfeit bool, body string) (Profile, []int, error) {
	if d.Validate(p) != nil || p.Run == nil {
		return p, nil, ErrInvalid
	}
	r := *p.Run
	if r.Settled || (!forfeit && !r.Dead && len(r.CompletedPhases) != 5) {
		return p, nil, nil
	}
	day := dayKey(now)
	claimed := []int{}
	phases := slices.Sorted(slices.Values(r.CompletedPhases))
	p.DailyClaims = slices.Clone(p.DailyClaims)
	r.DeliveredRewards = []Reward{}
	for _, phase := range phases {
		k := fmt.Sprintf("%s:%d", day, phase)
		if !slices.Contains(p.DailyClaims, k) {
			if phase >= len(d.Rewards) {
				return p, nil, ErrInvalid
			}
			p.DailyClaims = append(p.DailyClaims, k)
			claimed = append(claimed, phase)
			r.DeliveredRewards = append(r.DeliveredRewards, d.Rewards[phase])
		}
	}
	if len(phases) == 5 {
		if p.FullClears == math.MaxInt32 {
			return p, nil, ErrInvalid
		}
		p.FullClears++
	}
	r.Dead = r.Dead || forfeit
	r.Settled = true
	r.ResultBody = body
	r.SettlementDay = day
	r.SettlementCause = "native-result"
	if forfeit {
		r.SettlementCause = "forfeit"
	}
	p.Run = &r
	return p, claimed, d.Validate(p)
}
