package legacy

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/storage"
)

type assetIndex struct {
	seen, owned map[uuid.UUID]bool
	pets        map[uint32]bool
}

func (i *assetIndex) add(id uuid.UUID) error {
	if id == uuid.Nil || i.seen[id] {
		return storage.ErrInvalid
	}
	i.seen[id] = true
	return nil
}

func (i *assetIndex) character(c storage.Character) error {
	rows, err := inventorywire.SavedSpecial(c)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if i.add(row.Instance) != nil || i.add(row.Detail) != nil || (row.PetSerial != 0 && i.pets[row.PetSerial]) {
			return storage.ErrInvalid
		}
		if row.PetSerial != 0 {
			i.pets[row.PetSerial] = true
		}
	}
	mail, err := storage.ReadMailbox(c)
	if err != nil {
		return err
	}
	for _, letter := range mail.Mails {
		for _, a := range letter.Attachments {
			if !a.Claimed {
				id, err := uuid.Parse(a.Asset.InstanceID)
				if err != nil || i.add(id) != nil {
					return storage.ErrInvalid
				}
			}
		}
	}
	for _, t := range mail.ClaimTransfers {
		source, err := uuid.Parse(t.Source.InstanceID)
		if err != nil {
			return err
		}
		retained := slices.ContainsFunc(t.Targets, func(target storage.MailTarget) bool {
			id, err := uuid.Parse(target.InstanceID)
			return err == nil && id == source
		})
		if !retained && i.add(source) != nil {
			return storage.ErrInvalid
		}
	}
	var imports struct {
		SharedMaterialImports map[string]struct {
			Source struct{ InstanceId uuid.UUID }
			Target uuid.UUID
		}
	}
	if err = json.Unmarshal(c.Gameplay, &imports); err != nil {
		return err
	}
	for _, fact := range imports.SharedMaterialImports {
		if fact.Source.InstanceId != fact.Target && i.add(fact.Source.InstanceId) != nil {
			return storage.ErrInvalid
		}
	}
	return nil
}

// Iterate each SQLite result before opening the next one. The source is one read-only snapshot.
func (s *source) checkAssetIdentities(ctx context.Context) error {
	idx := assetIndex{map[uuid.UUID]bool{}, map[uuid.UUID]bool{}, map[uint32]bool{}}
	const assets = "SELECT instance_id FROM local_weapon_assets UNION ALL SELECT instance_id FROM local_item_assets UNION ALL SELECT instance_id FROM local_shared_material_assets"
	rows, err := s.tx.QueryContext(ctx, assets)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		id, parse := uuid.Parse(raw)
		if parse != nil || len(raw) != 32 || idx.add(id) != nil {
			s.report.ActivationIssues = append(s.report.ActivationIssues, "ordinary inventory has an empty or duplicate asset identity")
			rows.Close()
			return nil
		}
		idx.owned[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(s.report.Tables, func(t Table) bool { return t.Name == "local_fate_exchange_purchases" && t.Rows > 0 }) {
		rows, err = s.tx.QueryContext(ctx, "SELECT receipt_grants_json FROM local_fate_exchange_purchases WHERE account='local'")
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			var grants []struct{ InstanceId uuid.UUID }
			if json.Unmarshal(raw, &grants) != nil || grants == nil {
				s.report.ActivationIssues = append(s.report.ActivationIssues, "Fate exchange inventory history is malformed")
				rows.Close()
				return nil
			}
			for _, g := range grants {
				if !idx.owned[g.InstanceId] && idx.add(g.InstanceId) != nil {
					s.report.ActivationIssues = append(s.report.ActivationIssues, "Fate exchange history has a duplicate asset identity")
					rows.Close()
					return nil
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	rows, err = s.tx.QueryContext(ctx, "SELECT id,job,level,gameplay_json FROM local_entry_characters WHERE account='local' ORDER BY id")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c storage.Character
		var raw []byte
		if err = rows.Scan(&c.ID, &c.Job, &c.Level, &raw); err != nil {
			return err
		}
		c.Gameplay = raw
		if err = idx.character(c); err != nil {
			s.report.ActivationIssues = append(s.report.ActivationIssues, fmt.Sprintf("character %d has invalid special, mail or transferred asset identities: %v", c.ID, err))
			break
		}
	}
	return rows.Err()
}
