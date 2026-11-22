package storage

import "context"

type GmCharacter struct {
	Id   uint16 `json:"id"`
	Name string `json:"name"`
}

// The original desktop GM tool targets the local account and displays at most 64 characters.
func (s *Store) GmCharacters(ctx context.Context) ([]GmCharacter, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT wire_id,name FROM s9a1.characters WHERE account='local' ORDER BY wire_id LIMIT 64")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GmCharacter{}
	for rows.Next() {
		var c GmCharacter
		if err := rows.Scan(&c.Id, &c.Name); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
