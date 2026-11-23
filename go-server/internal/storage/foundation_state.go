package storage

import (
	"context"
	"database/sql"
	"errors"
)

type FoundationValue struct {
	Value        string
	SourceDigest string
}

type ReleaseSeedStatus struct {
	Policy     string
	Account    string
	Cera       int64
	Characters int
	FoundationValue
}

// FoundationState reads the original value for a specific target account.
func (s *Store) FoundationState(ctx context.Context, account, key string) (FoundationValue, bool, error) {
	var result FoundationValue
	var value []byte
	err := s.db.QueryRowContext(ctx, `SELECT state_value,coalesce(source_digest,'') FROM s9a1.foundation_state
		WHERE account=$1 AND key_sha256=sha256($2::bytea) AND state_key=$2`, account, []byte(key)).Scan(&value, &result.SourceDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return FoundationValue{}, false, nil
	}
	result.Value = string(value)
	return result, err == nil, err
}

// ReleaseSeedStatus reads the historical issue receipt and its source. Its Cera
// and character count describe the seed, while the live account can advance.
func (s *Store) ReleaseSeedStatus(ctx context.Context, account string) (ReleaseSeedStatus, bool, error) {
	var result ReleaseSeedStatus
	var value []byte
	var digest sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT r.policy,r.account,r.cera,r.characters,f.state_value,f.source_digest
		FROM s9a1.release_seed r JOIN s9a1.foundation_state f ON f.account=r.account
		AND f.key_sha256=sha256(convert_to(r.policy,'UTF8')) AND f.state_key=convert_to(r.policy,'UTF8')
		WHERE r.account=$1 AND r.policy=$2`, account, ReleaseSeedPolicy).Scan(&result.Policy, &result.Account, &result.Cera, &result.Characters, &value, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return ReleaseSeedStatus{}, false, nil
	}
	result.Value, result.SourceDigest = string(value), digest.String
	return result, err == nil, err
}
