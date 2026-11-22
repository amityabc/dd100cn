package storage

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDuelPageInitializedOnce(t *testing.T) {
	ctx, s, account := testStore(t)
	l, err := s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.List(ctx, l); err != nil {
		t.Fatal(err)
	}
	g := json.RawMessage(`{"Initialized":true,"DuelSkills":null,"Skills":[{"Id":7,"Level":2,"Slot":1}],"Progression":{"Sp":123,"Tp":4},"Future":18446744073709551615}`)
	c, err := s.Create(ctx, l, CreateCharacter{Name: account, ResourceHash: strings.Repeat("A", 64), Stats: []byte(`{}`), Gameplay: g, Opening: &Position{Town: 38, X: 481}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnsureDuelSkills(ctx, l, c.ID, nil); !errors.Is(err, ErrPhase) {
		t.Fatalf("before selection: %v", err)
	}
	c, err = s.Select(ctx, l, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnsureDuelSkills(ctx, l, c.ID, func(Character) (json.RawMessage, error) { return nil, ErrInvalid }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rejected plan: %v", err)
	}
	build := json.RawMessage(`{"Skills":[{"Id":7,"Level":2,"Slot":1}],"Sp":123,"Tp":4}`)
	var calls atomic.Int32
	plan := func(locked Character) (json.RawMessage, error) {
		calls.Add(1)
		if locked.Revision != c.Revision || locked.ID != c.ID {
			return nil, ErrConflict
		}
		return build, nil
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.EnsureDuelSkills(ctx, l, c.ID, plan)
			if err == nil && (v.Revision != c.Revision+1 || v.Position != c.Position) {
				err = ErrConflict
			}
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("ran planner %d times", calls.Load())
	}
	r, err := s.Snapshot(ctx, l)
	if err != nil || r.Phase != Selected || len(r.Characters) != 1 {
		t.Fatalf("duel phase: %v", err)
	}
	var old, next map[string]json.RawMessage
	if json.Unmarshal(c.Gameplay, &old) != nil || json.Unmarshal(r.Characters[0].Gameplay, &next) != nil || !equalJSON(build, next["DuelSkills"]) {
		t.Fatal("stored duel page differs from plan")
	}
	delete(old, "DuelSkills")
	delete(next, "DuelSkills")
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(next)
	if !equalJSON(a, b) {
		t.Fatal("ordinary character profile changed")
	}
	newOwner, err := s.Acquire(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnsureDuelSkills(ctx, l, c.ID, plan); !errors.Is(err, ErrLease) {
		t.Fatalf("old owner: %v", err)
	}
	if _, err = s.Select(ctx, newOwner, 0); err != nil {
		t.Fatal(err)
	}
	if v, err := s.EnsureDuelSkills(ctx, newOwner, c.ID, nil); err != nil || v.Revision != c.Revision+1 {
		t.Fatalf("relogin: %v", err)
	}
}
