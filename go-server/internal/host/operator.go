package host

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"
	"s9a1/server/internal/operator"
	"s9a1/server/internal/storage"
)

// The legacy audit field identifies the executor build. Derive the Go build's UUID from its executable.
var operatorBuild = sync.OnceValues(func() (uuid.UUID, error) {
	name, err := os.Executable()
	if err != nil {
		return uuid.Nil, err
	}
	f, err := os.Open(name)
	if err != nil {
		return uuid.Nil, err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	copy(id[:], hash.Sum(nil))
	id[6] = (id[6] & 0x0f) | 0x80
	id[8] = (id[8] & 0x3f) | 0x80
	return id, nil
})

func (s *Server) operatorCommand(ctx context.Context, i operator.Intent) operator.Reply {
	message := func(status, why string) operator.Reply { return operator.Message(status, i.OperationID, why) }
	permission := "Account is not authorized for operator CERA grants"
	unavailable := "No current selected online owner for the requested account and character"
	switch i.Command.(type) {
	case operator.Wallet:
		permission = "Account is not authorized for operator wallet grants"
		unavailable = "Exactly one selected online owner is required for the target"
	case operator.Mail:
		permission = "Account is not authorized for operator mail grants"
	case operator.Wisdom:
		permission = "Account has no operator permission"
		unavailable = "Exactly one current selected owner is required"
	}
	if strings.TrimSpace(i.Account) == "" || !slices.Contains(s.gmAccounts, i.Account) {
		return message("Forbidden", permission)
	}
	if mail, ok := i.Command.(operator.Mail); ok {
		return s.operatorMail(ctx, mail)
	}
	s.mu.Lock()
	p := s.active[i.Account]
	s.mu.Unlock()
	if p == nil {
		return message("Rejected", unavailable)
	}
	p.serial.Lock()
	defer p.serial.Unlock()
	if p.ctx.Err() != nil || p.player == nil {
		return message("Rejected", unavailable)
	}
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)
	defer func() { stop(); cancel() }()
	g := p.player
	ready := g.opening == nil && !g.ordinary.Active() && !g.ordinary.Returning()
	build, err := operatorBuild()
	if err != nil {
		return message("Unconfirmed", err.Error())
	}
	var result operator.Reply
	if m, ok := i.Command.(operator.Wisdom); ok {
		catalog := s.itemsCatalog.Load()
		result, err = s.DB.OperatorWisdom(work, g.lease, m, catalog.Commerce.Templates[storage.WisdomStone], ready, build)
	} else {
		result, err = s.DB.OperatorWallet(work, g.lease, i, ready, build)
	}
	if errors.Is(err, storage.ErrCommitUnknown) {
		p.cancel()
	}
	if errors.Is(err, storage.ErrLease) {
		return message("LeaseLost", "Current owner has retired")
	}
	if err != nil {
		return message("Unconfirmed", err.Error())
	}
	return result
}
