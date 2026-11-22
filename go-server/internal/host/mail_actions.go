package host

import (
	"context"
	"errors"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/storage"
)

type mailSession struct {
	initialized bool
	character   uint16
	unread      map[int64]struct{}
	positive    bool
	replay      storage.MailSendReplay
}

// Town already emits N97 and N99. Remember the baseline after both completed.
func (g *game) observeTownMailbox(c storage.Character) error {
	m, err := storage.ReadMailbox(c)
	if err != nil {
		return err
	}
	unread := m.Page().Unread
	g.mailState.initialized, g.mailState.character = true, c.ID
	g.mailState.unread, g.mailState.positive = unread, len(unread) > 0
	g.mailState.replay = storage.MailSendReplay{}
	g.trackPetCare(c)
	return nil
}

func (g *game) mailbox(ctx context.Context, op uint16, body []byte) error {
	reject := func() error { return g.send(1, op, mailwire.Rejected(4)) }
	if !g.commerceInTown() {
		return reject()
	}
	var ids []int64
	var state uint16
	if op == 96 {
		if len(body) != 0 {
			return reject()
		}
	} else {
		var err error
		ids, state, err = mailwire.ReadBatch(body, op == 134)
		if err != nil {
			return reject()
		}
	}
	catalog := g.server.Characters
	out, err := g.server.DB.MailboxAction(ctx, g.lease, op, ids, state, catalog.GoldCarryLimits, catalog.Weights)
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrFunds) || errors.Is(err, storage.ErrCapacity) || errors.Is(err, storage.ErrPhase) {
		return reject()
	}
	if err != nil {
		return err
	}
	packets, err := mailwire.Pages(out.Page)
	if err != nil {
		return err
	}
	var ack []byte
	var bags []inventorywire.Packet
	switch op {
	case 95:
		ack = mailwire.Claimed(out.Claims)
		bags, err = inventorywire.CommerceBags(out.Inventory, out.Character, true)
		if err != nil {
			return err
		}
	case 96:
		ack = mailwire.Opened(out.Page.Overflow)
	case 134:
		ack = mailwire.Changed(ids, state)
	}
	if err = g.send(1, op, ack); err != nil {
		return err
	}
	if err = g.projected(bags); err != nil {
		return err
	}
	for _, p := range packets {
		if err = g.send(0, p.Opcode, p.Body); err != nil {
			return err
		}
	}
	return g.publishMailAlarm(out.Character.ID, out.Page.Unread, op == 96)
}

func (g *game) publishMailAlarm(character uint16, unread map[int64]struct{}, suppress bool) error {
	prior := g.mailState
	fresh := !prior.initialized || prior.character != character
	positive := !fresh && prior.positive
	send := false
	if len(unread) == 0 {
		send = fresh || positive
		positive = false
	} else if !suppress {
		send = fresh
		for id := range unread {
			if _, ok := prior.unread[id]; !ok {
				send = true
				break
			}
		}
		positive = positive || send
	}
	if send {
		if err := g.send(0, 99, mailwire.Alarm(len(unread))); err != nil {
			return err
		}
	}
	g.mailState.character, g.mailState.unread, g.mailState.positive, g.mailState.initialized = character, unread, positive, true
	return nil
}

func mailBag(in storage.Inventory, c storage.Character) ([]byte, error) {
	packets, err := inventorywire.BagAndWear(in, c)
	if err != nil {
		return nil, err
	}
	return packets[0].Body, nil
}

func (g *game) sendMail(ctx context.Context, op uint16, body []byte) error {
	reject := func(code uint16) error { return g.send(1, op, mailwire.Rejected(code)) }
	req, err := mailwire.ReadSend(op, body)
	if err != nil || !g.commerceInTown() {
		return reject(17)
	}
	catalog := g.server.Characters
	out, err := g.server.DB.SendMail(ctx, g.lease, g.seq, req, g.mailState.replay, catalog.GoldCarryLimits, catalog.MailAttachments)
	var code storage.MailRejection
	if errors.As(err, &code) {
		return reject(uint16(code))
	}
	if errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrPhase) || errors.Is(err, storage.ErrCapacity) {
		return reject(17)
	}
	if err != nil {
		return err
	}
	bag, err := mailBag(out.Inventory, out.Character)
	if err != nil {
		return err
	}
	g.mailState.replay = out.Replay
	if err = g.send(1, op, []byte{1}); err != nil {
		return err
	}
	return g.send(0, 13, bag)
}
