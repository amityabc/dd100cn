package duellobby

import (
	"bytes"
	"encoding/hex"
	"errors"
	"slices"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

type ArcadeDefinition struct {
	Index, Difficulty, Rounds, MaximumRevives byte
	Opponents, RandomOpponents                []uint32
	FixedOpponents                            map[byte]uint32
	SourceHash                                string
}

type ArcadeRow struct {
	Job, Grow  byte
	Definition ArcadeDefinition
}

type ArcadeCatalog map[[3]byte]*ArcadeDefinition

func NewArcadeCatalog(rows []ArcadeRow) (ArcadeCatalog, error) {
	if len(rows) == 0 {
		return nil, errors.New("missing arcade definitions, re-export the catalog")
	}
	cat := make(ArcadeCatalog, len(rows))
	for i := range rows {
		r := &rows[i]
		d := &r.Definition
		key := [3]byte{r.Job, r.Grow, d.Index}
		hash, err := hex.DecodeString(d.SourceHash)
		if r.Job > 15 || r.Grow > 5 || cat[key] != nil || d.Index > 2 && (d.Index < 80 || d.Index > 82) ||
			d.Difficulty > 2 || d.Index <= 2 && d.Index != d.Difficulty || d.Rounds == 0 || len(d.Opponents) == 0 || err != nil || len(hash) != 32 {
			return nil, errors.New("invalid frozen arcade definition")
		}
		cat[key] = d
	}
	return cat, nil
}

func (cat ArcadeCatalog) Get(job, grow, index byte) *ArcadeDefinition {
	if grow > 0x3f {
		return nil
	}
	return cat[[3]byte{job, grow & 15, index}]
}

type TrainingStage struct {
	ID               uint16
	Hash, Acceptance string
	Stage            byte
}

// A room belongs to a single selection and never replaces the durable town position.
type Room struct {
	Owner               storage.Lease
	Character, ID       uint16
	Mode, PracticeIndex byte
	Definition          *ArcadeDefinition
	Training            TrainingStage
	CreateBody          []byte
	Completed, Revives  byte
	Start, Last         *protocol.ArcadeRound
	Closed, Qualified   bool
}

func (r *Room) Owned(lease storage.Lease, character uint16) bool {
	return r != nil && r.Owner == lease && r.Character == character
}

func (r *Room) finished() bool {
	return r.Mode == protocol.ArcadeMode && r.Start == nil && (r.Completed == r.Definition.Rounds || r.Last != nil && !r.Last.Won)
}

func (r *Room) CanCompleteQuest(lease storage.Lease, character uint16) bool {
	return r == nil || r.Closed || r.Owned(lease, character) && r.finished()
}

// An unfinished room accepts the original request again without publishing a second room.
func (r *Room) Existing(body []byte, advanced bool) (reuse, blocked bool) {
	if r == nil || r.Closed || r.finished() || advanced {
		return false, false
	}
	return bytes.Equal(r.CreateBody, body), true
}

func (r *Room) Qualifies(lease storage.Lease, character uint16, t TrainingStage) bool {
	if !r.Owned(lease, character) || !r.Qualified || t.ID == 0 || t.Acceptance == "" || r.Training != t {
		return false
	}
	if t.Stage == 1 {
		return r.Mode == protocol.PracticeMode && (t.ID == 2672 && r.PracticeIndex == 12 || t.ID == 2674 && r.PracticeIndex == 13)
	}
	return r.Mode == protocol.ArcadeMode && r.PracticeIndex == 0
}

// Report returns silently for stale, duplicate or unsupported reports, as C1279 has no reply.
func (r *Room) Report(q protocol.ArcadeRound, trainingReady func() bool) {
	if r == nil || r.Closed || r.Mode != protocol.ArcadeMode || q.Phase > 1 {
		return
	}
	d := r.Definition
	if q.Round == 0 || q.Total != d.Rounds || q.Round > d.Rounds || !slices.Contains(d.Opponents, q.Opponent) ||
		r.Start != nil && *r.Start == q || r.Last != nil && *r.Last == q {
		return
	}
	revive := r.finished() && r.Last != nil && !r.Last.Won && q.Phase == 0 && q.Round == r.Last.Round &&
		q.Opponent == r.Last.Opponent && q.Elapsed > r.Last.Elapsed && r.Revives < d.MaximumRevives
	if r.finished() && !revive || r.Training.ID != 0 && !trainingReady() {
		return
	}
	if fixed, ok := d.FixedOpponents[q.Round]; ok {
		if q.Opponent != fixed {
			return
		}
	} else if d.RandomOpponents != nil && !slices.Contains(d.RandomOpponents, q.Opponent) {
		return
	}
	if int(q.Round) != int(r.Completed)+1 {
		return
	}
	if q.Phase == 0 {
		if r.Start == nil && !q.Won {
			r.Start = &q
			if revive {
				r.Revives++
			}
		}
		return
	}
	if r.Start == nil || r.Start.Opponent != q.Opponent || r.Start.Round != q.Round || q.Elapsed < r.Start.Elapsed {
		return
	}
	r.Start, r.Last = nil, &q
	if q.Won {
		r.Completed++
	}
	r.Qualified = r.Training.ID != 0 && r.Completed == d.Rounds
}
