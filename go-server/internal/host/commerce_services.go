package host

import (
	"bytes"
	"context"
	"encoding/binary"
	"time"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/storage"
)

type characterServiceSession struct {
	character            uint16
	body                 []byte
	buffRun, recoveryRun string
	nextRefresh          int64
}

func (g *game) refreshCharacterServices(ctx context.Context, force bool) error {
	if g.opening != nil {
		return nil
	}
	now := time.Now()
	duelReady := duellobby.IsChannel(g.channel) && g.duelPosition != nil
	s, err := g.server.DB.CharacterServices(ctx, g.lease, now, duelReady)
	if err != nil || s.CharacterID == 0 {
		return err
	}
	run := g.ordinary.Run
	s.Used[4] = 0
	if run != nil && run.Room != nil && run.ID == g.characterServices.recoveryRun && run.Character == s.CharacterID {
		s.Used[4] = 1
	}
	body := []byte{1, 1, 0, 0, 0}
	everPurchased := false
	dailyUsed := false
	var nextRefresh int64
	for i, expires := range s.Expires {
		everPurchased = everPurchased || expires != 0
		used := s.Used[i]
		if int64(expires) <= now.Unix() {
			expires, used = 0, 0
		} else if nextRefresh == 0 || int64(expires) < nextRefresh {
			nextRefresh = int64(expires)
		}
		body = binary.LittleEndian.AppendUint32(body, 0)
		body = binary.LittleEndian.AppendUint32(body, expires)
		body = append(body, used)
		dailyUsed = dailyUsed || i != 4 && used != 0
	}
	if dailyUsed {
		d := now.UTC().Add(2 * time.Hour)
		midnight := time.Date(d.Year(), d.Month(), d.Day(), 22, 0, 0, 0, time.UTC).Unix()
		if nextRefresh == 0 || midnight < nextRefresh {
			nextRefresh = midnight
		}
	}
	old := g.characterServices
	g.characterServices.nextRefresh = nextRefresh
	if !force && (!everPurchased && len(old.body) == 0 || old.character == s.CharacterID && bytes.Equal(old.body, body)) {
		return nil
	}
	if err = g.send(1, 897, body); err != nil {
		return err
	}
	g.characterServices.body, g.characterServices.character = body, s.CharacterID
	return nil
}

func (g *game) useCharacterService(ctx context.Context, body []byte) error {
	if len(body) != 76 || body[1] != 0 || body[3] != 0 || !bytes.Equal(body[4:], make([]byte, 72)) {
		return storage.ErrInvalid
	}
	slot := body[2]
	reject := func() error {
		if slot == 3 {
			return g.refreshCharacterServices(ctx, true)
		}
		return g.send(1, 897, []byte{0, 0, 0, 0})
	}
	if body[0] == 4 && slot == 4 {
		return reject()
	}
	if body[0] != 1 || slot != 3 && slot != 4 {
		return storage.ErrInvalid
	}
	run := g.ordinary.Run
	if g.channel != 19 || g.opening != nil || run == nil || run.ID == "" || run.Room == nil || run.Cleared ||
		run.TournamentReward() != 0 || g.fateSession.Run == run.ID || g.ordinary.Returning() ||
		slot == 4 && run.PlayerDeath != nil {
		return reject()
	}
	stamp := &g.characterServices.buffRun
	if slot == 4 {
		stamp = &g.characterServices.recoveryRun
	}
	if *stamp == run.ID {
		if slot == 4 {
			now := time.Now()
			service, err := g.server.DB.CharacterServices(ctx, g.lease, now, false)
			if err != nil {
				return err
			}
			if service.CharacterID != run.Character || int64(service.Expires[slot]) <= now.Unix() {
				return reject()
			}
		}
		return g.refreshCharacterServices(ctx, true)
	}
	out, err := g.server.DB.Commerce(ctx, g.lease, 897, g.seq, body, storage.CommerceReplay{}, func(x *storage.CommerceTx) error {
		if x.Character.ID != run.Character {
			return storage.ErrPhase
		}
		return x.UseCharacterService(slot, time.Now())
	})
	if expectedCommerceError(err) {
		return reject()
	}
	if err != nil {
		return err
	}
	g.commerceState = out.Replay
	*stamp = run.ID
	return g.refreshCharacterServices(ctx, true)
}
