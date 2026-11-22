package host

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"

	"s9a1/server/internal/config"
	"s9a1/server/internal/operator"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

func (s *Server) operatorMail(ctx context.Context, m operator.Mail) operator.Reply {
	s.mailGate.Lock()
	defer s.mailGate.Unlock()
	fail := func(e error) operator.Reply { return operator.Message("Rejected", m.OperationId, e.Error()) }
	original, err := s.outbox.Read(m.OperationId)
	if err != nil {
		return fail(err)
	}
	if original != nil && original.Intent != m {
		return fail(errors.New("Mail operation conflicts with its saved intent"))
	}
	raw, err := s.Characters.GM.Template(m.TemplateId)
	if err != nil {
		return fail(err)
	}
	var t struct{ ResourceHash string }
	if err = json.Unmarshal(raw, &t); err != nil {
		return fail(err)
	}
	if original != nil && original.ResourceHash != t.ResourceHash {
		return fail(errors.New("物品资源已变化，原离线投递未执行，请重新选择物品发送。"))
	}
	queue := func() operator.Reply {
		r, err := s.outbox.Queue(m, t.ResourceHash)
		if err != nil {
			return fail(err)
		}
		return r
	}
	s.mu.Lock()
	p := s.active[m.Account]
	s.mu.Unlock()
	if p == nil {
		return queue()
	}
	p.serial.Lock()
	defer p.serial.Unlock()
	g := p.player
	if g == nil || p.ctx.Err() != nil {
		return queue()
	}
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)
	defer func() { stop(); cancel() }()
	r, err := s.DB.Snapshot(work, g.lease)
	if err != nil {
		return operator.Message("Unconfirmed", m.OperationId, err.Error())
	}
	if r.SelectedID != m.Character || r.Phase != storage.TownReady {
		return queue()
	}
	result, err := s.DB.OperatorMail(work, g.lease, m, raw, g.opening == nil && !g.ordinary.Active() && !g.ordinary.Returning())
	if errors.Is(err, storage.ErrPhase) {
		return queue()
	}
	if errors.Is(err, storage.ErrCommitUnknown) {
		p.cancel()
	}
	if err != nil {
		return operator.Message("Unconfirmed", m.OperationId, err.Error())
	}
	return result
}

func (s *Server) pumpOperatorMail(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		p := s.active["local"]
		s.mu.Unlock()
		if p == nil {
			continue
		}
		// Most sessions have no pending mail. Compare the selected character only
		// after finding queue entries, without doing a database read every tick.
		var target uint16
		looked := false
		sent := 0
		var actionErr error
		err := s.outbox.Pending(func(e operator.MailEntry) bool {
			if !looked {
				looked = true
				p.serial.Lock()
				if p.player != nil && p.ctx.Err() == nil {
					r, err := s.DB.Snapshot(ctx, p.player.lease)
					actionErr = err
					if err == nil && r.Phase == storage.TownReady {
						target = r.SelectedID
					}
				}
				p.serial.Unlock()
			}
			if target == 0 || ctx.Err() != nil {
				return false
			}
			if e.Intent.Character != target {
				return true
			}
			m := e.Intent
			r := s.operatorCommand(ctx, operator.Intent{Account: m.Account, Character: m.Character, OperationID: m.OperationId, Reason: m.Reason, Command: m})
			switch {
			case r.ReadBack && (r.Status == "Granted" || r.Status == "Replay"):
				actionErr = s.outbox.Complete(m, "Delivered", r)
			case r.Status == "Rejected" || r.Status == "Forbidden":
				actionErr = s.outbox.Complete(m, "Rejected", r)
			}
			sent++
			return actionErr == nil && sent < 16
		})
		if err == nil {
			err = actionErr
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("OperatorMailOutboxRetry", "error", err)
		}
	}
}

func QueueDisconnectedMail(root string, m operator.Mail) (operator.Reply, error) {
	c, err := config.Load(root)
	if err != nil {
		return operator.Reply{}, err
	}
	if !slices.Contains(c.Gameplay.GmAccounts, m.Account) {
		return operator.Message("Forbidden", m.OperationId, "本地账号未授权 GM。"), nil
	}
	catalog, err := resources.LoadCharacters(c.CatalogPath(), c.Path(c.Resources.Pvf), c.Path(c.Resources.ClientProfile), c.Gameplay.ExperienceMultiplier)
	if err != nil {
		return operator.Reply{}, err
	}
	raw, err := catalog.GM.Template(m.TemplateId)
	if err != nil {
		return operator.Reply{}, err
	}
	var t struct {
		ResourceHash string
		Kind         int
		StackLimit   uint32
	}
	if err = json.Unmarshal(raw, &t); err != nil {
		return operator.Reply{}, err
	}
	if m.Count == 0 || m.Count > t.StackLimit || t.Kind != 2 && m.Count != 1 {
		return operator.Message("Rejected", m.OperationId, "物品不支持普通邮件附件，或数量超过 PVF 上限。"), nil
	}
	return (operator.Outbox{Root: c.Root}).Queue(m, t.ResourceHash)
}
