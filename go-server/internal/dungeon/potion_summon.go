package dungeon

import (
	"time"

	"s9a1/server/internal/storage"
)

func (r *Run) RegisterPotionSummon(auth *storage.PotionSummonAuthorization) bool {
	if auth == nil || !r.GameMasterReady() || r.InLayer || auth.RunID != r.ID || auth.Character != r.Character || auth.Key == "" {
		return false
	}
	r.NativeSummon, r.nativeSummonActor = auth, 0
	return true
}

func (r *Run) nativeSummonKill(body []byte, now time.Time) *storage.PotionSummonKill {
	s := r.NativeSummon
	if s == nil || s.RunID != r.ID || s.Character != r.Character || r.InLayer || !r.GameMasterReady() {
		return nil
	}
	at := now.UnixMilli()
	actor := storage.SummonReportedActor(body)
	if actor == 0 || actor == r.Character || r.nativeSummonActor != 0 && r.nativeSummonActor != actor || at < s.UsedAt || at >= s.ExpiresAt {
		return nil
	}
	return &storage.PotionSummonKill{AuthorizationKey: s.Key, Actor: actor, ReportedAt: at, RunID: r.ID, Body: body}
}

func (r *Run) CommitSummonKill(k *storage.PotionSummonKill) {
	if k != nil && r.NativeSummon != nil && r.NativeSummon.Key == k.AuthorizationKey {
		r.nativeSummonActor = k.Actor
	}
}
