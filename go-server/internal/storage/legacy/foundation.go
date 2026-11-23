package legacy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"s9a1/server/internal/storage"
)

type foundationRow struct {
	key, value   []byte
	keyType      string
	valueType    string
}

func (s *source) walkFoundation(ctx context.Context, visit func(foundationRow) error) error {
	found := false
	for _, table := range s.report.Tables {
		if table.Name == "foundation_state" {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	rows, err := s.tx.QueryContext(ctx, `SELECT CAST(key AS BLOB),CAST(value AS BLOB),typeof(key),typeof(value)
		FROM foundation_state ORDER BY key COLLATE BINARY`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row foundationRow
		if err := rows.Scan(&row.key, &row.value, &row.keyType, &row.valueType); err != nil {
			return err
		}
		if row.value == nil && row.valueType == "text" {
			row.value = []byte{}
		}
		if err := visit(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

type legacySeedReceipt struct {
	Policy     string
	Account    string
	Cera       int64
	Characters *int
	Build      string
}

func parseLegacySeed(raw []byte) (legacySeedReceipt, error) {
	var receipt legacySeedReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return receipt, fmt.Errorf("official release seed receipt is invalid: %w", err)
	}
	build, err := uuid.Parse(receipt.Build)
	if receipt.Policy != storage.ReleaseSeedPolicy || receipt.Account != "local" || receipt.Cera != storage.ReleaseSeedCera ||
		receipt.Characters == nil || *receipt.Characters != 0 || err != nil || build == uuid.Nil || !strings.EqualFold(build.String(), receipt.Build) {
		return receipt, fmt.Errorf("official release seed receipt differs from the legacy issuance contract")
	}
	return receipt, nil
}

func (s *source) checkFoundation(ctx context.Context) error {
	return s.walkFoundation(ctx, func(row foundationRow) error {
		if len(row.key) == 0 || row.keyType != "text" || row.valueType != "text" {
			s.report.ActivationIssues = append(s.report.ActivationIssues, "foundation_state contains an invalid text key or value")
			return nil
		}
		if string(row.key) == storage.ReleaseSeedPolicy {
			if _, err := parseLegacySeed(row.value); err != nil {
				s.report.ActivationIssues = append(s.report.ActivationIssues, err.Error())
			}
		}
		return nil
	})
}

func (s *source) copyFoundation(ctx context.Context, tx *sql.Tx, account, digest string) error {
	return s.walkFoundation(ctx, func(row foundationRow) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO s9a1.foundation_state(account,state_key,state_value,source_digest)
			VALUES($1,$2,$3,$4)`, account, row.key, row.value, digest); err != nil {
			return err
		}
		if string(row.key) != storage.ReleaseSeedPolicy {
			return nil
		}
		receipt, err := parseLegacySeed(row.value)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO s9a1.release_seed(policy,account,cera,characters)
			VALUES($1,$2,$3,$4)`, receipt.Policy, account, receipt.Cera, *receipt.Characters)
		return err
	})
}
