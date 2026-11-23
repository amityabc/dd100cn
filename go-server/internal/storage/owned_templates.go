package storage

import (
	"context"
	"fmt"
	"math"
)

// OwnedTemplateIDs takes one database snapshot of current inventory, special
// assets and mail escrow across all accounts, including offline characters.
func (s *Store) OwnedTemplateIDs(ctx context.Context) ([]uint32, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT template_id FROM s9a1.assets
		UNION
		SELECT (item->'Template'->>'TemplateId')::bigint
		FROM s9a1.characters
		CROSS JOIN LATERAL json_array_elements(gameplay_json->'SpecialAssets'->'Items') AS held(item)
		UNION
		SELECT (attachment->'Asset'->'Template'->>'TemplateId')::bigint
		FROM s9a1.characters
		CROSS JOIN LATERAL json_array_elements(gameplay_json->'Mailbox'->'Mails') AS letters(mail)
		CROSS JOIN LATERAL json_array_elements(mail->'Attachments') AS escrow(attachment)
		WHERE NOT coalesce((attachment->>'Claimed')::boolean,false)
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uint32
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id <= 0 || id >= math.MaxUint32 {
			return nil, fmt.Errorf("saved asset template ID %d: %w", id, ErrInvalid)
		}
		ids = append(ids, uint32(id))
	}
	return ids, rows.Err()
}
