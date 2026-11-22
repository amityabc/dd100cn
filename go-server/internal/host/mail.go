package host

import (
	"context"
	"encoding/binary"
	"errors"

	"s9a1/server/internal/mailwire"
	"s9a1/server/internal/storage"
)

func (g *game) mailRecipient(ctx context.Context, body []byte) error {
	reject := func(code uint16) error { return g.send(1, 324, binary.LittleEndian.AppendUint16([]byte{0}, code)) }
	request, err := mailwire.ReadRecipient(body)
	if err != nil {
		return reject(2)
	}
	recipient, err := g.server.DB.MailRecipient(ctx, g.lease, request.Name)
	if errors.Is(err, storage.ErrLease) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil || g.opening != nil || !request.Ordinary() {
		return reject(2)
	}
	if recipient.ID == 0 {
		return reject(21)
	}
	if recipient.ID == recipient.SenderID {
		return reject(7)
	}
	response, err := mailwire.RecipientAccepted(recipient.Name, uint16(recipient.Level), recipient.Job, recipient.GrowType)
	if err != nil {
		return err
	}
	return g.send(1, 324, response)
}
