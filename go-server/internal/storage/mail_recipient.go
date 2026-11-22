package storage

import (
	"context"
	"database/sql"
	"errors"
)

type MailRecipient struct {
	ID, SenderID         uint16
	Name                 string
	Job, Level, GrowType byte
}

// MailRecipient reads the lease, selected sender, and matching character in
// one database snapshot. C324 needs only these five recipient fields. Other
// accounts, including other owners of a valid name, are outside its lookup.
func (s *Store) MailRecipient(ctx context.Context, l Lease, name string) (MailRecipient, error) {
	var r MailRecipient
	var phase Phase
	err := s.db.QueryRowContext(ctx, `SELECT a.phase,coalesce(a.selected_id,0),coalesce(c.wire_id,0),
		coalesce(c.name,''),coalesce(c.job,0),coalesce(c.level,0),
		coalesce((c.gameplay_json #>> '{Progression,GrowType}')::int,0)
		FROM s9a1.accounts a LEFT JOIN s9a1.characters c
		ON c.account=a.account AND c.name_key=$4 AND a.phase=$5
		WHERE a.account=$1 AND a.owner=$2::uuid AND a.epoch=$3 AND a.phase<>5`,
		l.Account, l.Owner, l.Epoch, nameKey(name), TownReady).Scan(&phase, &r.SenderID, &r.ID, &r.Name, &r.Job, &r.Level, &r.GrowType)
	if errors.Is(err, sql.ErrNoRows) {
		return MailRecipient{}, ErrLease
	}
	if err != nil {
		return MailRecipient{}, err
	}
	if phase != TownReady || r.SenderID == 0 {
		return MailRecipient{}, ErrPhase
	}
	return r, nil
}
