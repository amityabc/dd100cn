package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Asset retains frozen JSON, opaque native core and detail independently.
// CharacterID zero identifies an account-owned container.
type Asset struct {
	UID, Source          string
	CharacterID          uint16
	TemplateID, Count    uint32
	Location             uint8
	Slot                 uint16
	Template, Attributes json.RawMessage
	Weapon               *WeaponFacts
	Core, Detail         []byte
}
type WeaponFacts struct {
	QualitySeed                   uint32
	Durability, MaximumDurability uint16
	MinimumLevel                  uint8
	UsableJobs                    uint16
	ResourceHash                  string
	Qualification                 json.RawMessage
}
type Equipment struct {
	Slot                                    uint8
	TemplateID, QualitySeed                 uint32
	Durability                              uint16
	Reinforcement, Forge, AmplificationType uint8
	AmplificationValue                      uint16
	Core                                    []byte
}
type Inventory struct {
	CharacterID                                   uint16
	Revision, AssetRevision                       int64
	Gold, Coins, Cera, CargoGold                  int64
	TreasureGauge                                 uint32
	UniqueAvatarGauge                             uint16
	BagExpansion, PersonalCapacity, CargoCapacity uint16
	AccountPurchases                              map[uint32]CommercePurchase
	Items                                         []Asset
}

const assetColumns = `instance_id,source,coalesce(character_id,0),template_id,count,location,slot,
	template_json::text,attributes_json::text,weapon_facts::text,core,detail`

func scanAsset(row interface{ Scan(...any) error }) (Asset, error) {
	var a Asset
	var t, attr, weapon string
	if err := row.Scan(&a.UID, &a.Source, &a.CharacterID, &a.TemplateID, &a.Count, &a.Location, &a.Slot, &t, &attr, &weapon, &a.Core, &a.Detail); err != nil {
		return a, err
	}
	a.Template, a.Attributes = json.RawMessage(t), json.RawMessage(attr)
	if err := json.Unmarshal([]byte(weapon), &a.Weapon); err != nil {
		return a, err
	}
	return a, nil
}

type sealOption struct{ Id, FirstRatio, SecondRatio uint8 }
type sealState struct {
	First, Second, Third *sealOption
	Seed                 uint8
}

// WornEquipment mirrors the old server's native actor view. Magic-seal display
// decorates a copy of the 119-byte core and never modifies the saved bytes.
func (a Asset) WornEquipment(c Character) (Equipment, error) {
	e := Equipment{TemplateID: a.TemplateID}
	if a.Location != 3 || a.Count != 1 || a.Slot > 255 || len(a.Core) != 119 {
		return e, ErrInvalid
	}
	e.Slot = uint8(a.Slot)
	e.Core = bytes.Clone(a.Core)
	var attr struct {
		QualitySeed                             uint32
		Durability                              uint16
		Reinforcement, Forge, AmplificationType uint8
		AmplificationValue                      uint16
		InitialAmplification                    bool
		MagicSeal                               *sealState
	}
	var maximumDurability uint16
	switch a.Source {
	case "weapon":
		w := a.Weapon
		if w == nil || w.QualitySeed == 0 || w.QualitySeed >= 999999999 || w.Durability > w.MaximumDurability || (w.UsableJobs&(1<<c.Job)) == 0 {
			return e, ErrInvalid
		}
		if a.Slot != 12 && !(a.Slot == 24 && qualifiedOffhand(c, &a)) {
			return e, ErrInvalid
		}
		maximumDurability = w.MaximumDurability
		attr.QualitySeed, attr.Durability = w.QualitySeed, w.Durability
		attr.Reinforcement, attr.Forge, attr.AmplificationType = a.Core[10], a.Core[0x57], a.Core[19]
		attr.AmplificationValue = binary.LittleEndian.Uint16(a.Core[20:])
	case "item":
		var t struct {
			Kind                                    int
			TemplateID                              uint32
			WornSlot, UsableJobs, MaximumDurability uint16
			WornSlotIsNative                        bool
		}
		if err := json.Unmarshal(a.Template, &t); err != nil {
			return e, err
		}
		if t.Kind != 1 || t.TemplateID != a.TemplateID || !t.WornSlotIsNative || t.WornSlot != a.Slot || t.UsableJobs&(1<<c.Job) == 0 || !slices.Contains([]uint16{13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 25, 32}, a.Slot) {
			return e, ErrInvalid
		}
		maximumDurability = t.MaximumDurability
	default:
		return e, ErrInvalid
	}
	if !bytes.Equal(bytes.TrimSpace(a.Attributes), []byte("null")) {
		if err := json.Unmarshal(a.Attributes, &attr); err != nil {
			return e, err
		}
	}
	if attr.QualitySeed == 0 || attr.QualitySeed >= 999999999 || attr.Durability > maximumDurability ||
		a.Weapon != nil && (attr.Durability != a.Weapon.Durability || attr.QualitySeed != a.Weapon.QualitySeed) {
		return e, ErrInvalid
	}
	e.QualitySeed, e.Durability, e.Reinforcement, e.Forge = attr.QualitySeed, attr.Durability, attr.Reinforcement, attr.Forge
	e.AmplificationType, e.AmplificationValue = attr.AmplificationType, attr.AmplificationValue
	if attr.InitialAmplification && attr.AmplificationType&0x80 != 0 {
		e.AmplificationType = 0x80
		e.AmplificationValue = 0
	}
	if seal := attr.MagicSeal; seal != nil {
		if seal.First == nil || !bytes.Equal(e.Core[0x47:0x57], make([]byte, 16)) {
			return e, ErrInvalid
		}
		var seen [256]bool
		index := 0
		for _, opt := range []*sealOption{seal.First, seal.Second, seal.Third} {
			if opt == nil {
				continue
			}
			if opt.Id == 0 || seen[opt.Id] || opt.FirstRatio > 100 || opt.SecondRatio > 100 {
				return e, ErrInvalid
			}
			seen[opt.Id] = true
			e.Core[0x48+index], e.Core[0x4B+index], e.Core[0x4E+index] = opt.Id, opt.FirstRatio, opt.SecondRatio
			index++
		}
		e.Core[0x47], e.Core[0x51], e.Core[0x52] = byte(index), seal.Seed, 255
	}
	return e, nil
}

func qualifiedOffhand(c Character, a *Asset) bool {
	var p struct {
		Progression struct{ GrowType uint8 }
		Skills      []struct{ ID, Level uint16 }
	}
	t, err := moveTemplate(a)
	q := t.WeaponQualification
	if err != nil || c.Job != 11 || json.Unmarshal(c.Gameplay, &p) != nil || p.Progression.GrowType&15 != 4 || !q.Verified || len(q.Jobs) > 16 {
		return false
	}
	required, skill, offhand := false, false, false
	for i, j := range q.Jobs {
		if j.Job > 15 || j.Skill == 0 || j.Skill >= 512 || i > 0 && q.Jobs[i-1].Job >= j.Job {
			return false
		}
		if j.Job == 11 && j.Skill == 95 {
			required = true
		}
	}
	for _, s := range p.Skills {
		if s.Level > 0 {
			if s.ID == 95 {
				skill = true
			}
			if s.ID == 251 {
				offhand = true
			}
		}
	}
	return required && skill && offhand
}

func loadWorn(ctx context.Context, tx *sql.Tx, account string, chars []Character) error {
	if len(chars) == 0 {
		return nil
	}
	index := make(map[uint16]int, len(chars))
	for i, c := range chars {
		index[c.ID] = i
	}
	query := "SELECT " + assetColumns + " FROM s9a1.assets WHERE account=$1 AND location=3"
	args := []any{account}
	if len(chars) == 1 {
		query += " AND character_id=$2"
		args = append(args, chars[0].ID)
	}
	rows, err := tx.QueryContext(ctx, query+" ORDER BY character_id,slot", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return err
		}
		i, ok := index[a.CharacterID]
		if !ok {
			return ErrInvalid
		}
		e, err := a.WornEquipment(chars[i])
		if err != nil {
			return fmt.Errorf("saved equipment %s: %w", a.UID, err)
		}
		chars[i].Worn = append(chars[i].Worn, e)
	}
	return rows.Err()
}

// Inventory loads one selected character plus the owning account's shared
// containers, under the same ownership lock as the account and wallet data.
func (s *Store) Inventory(ctx context.Context, l Lease) (Inventory, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Inventory, error) {
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil {
			return Inventory{}, err
		}
		if r.SelectedID == 0 {
			return Inventory{}, ErrPhase
		}
		return readInventory(ctx, tx, l.Account, r)
	})
}

func readInventory(ctx context.Context, tx *sql.Tx, account string, r Roster) (Inventory, error) {
	in := Inventory{CharacterID: r.SelectedID, Cera: r.Cera, CargoGold: r.CargoGold, AssetRevision: r.AssetRevision, TreasureGauge: r.TreasureGauge, UniqueAvatarGauge: r.UniqueAvatarGauge}
	err := tx.QueryRowContext(ctx, `SELECT c.revision,c.gold,c.coin,c.bag_expansion,c.personal_capacity,a.cargo_capacity
			FROM s9a1.characters c JOIN s9a1.accounts a ON c.account=a.account WHERE c.account=$1 AND c.wire_id=$2`, account, r.SelectedID).Scan(&in.Revision, &in.Gold, &in.Coins, &in.BagExpansion, &in.PersonalCapacity, &in.CargoCapacity)
	if err != nil {
		return in, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+assetColumns+" FROM s9a1.assets WHERE account=$1 AND (character_id=$2 OR character_id IS NULL) ORDER BY location,slot", account, r.SelectedID)
	if err != nil {
		return in, err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return in, err
		}
		in.Items = append(in.Items, a)
	}
	if err = rows.Err(); err != nil {
		return in, err
	}
	rows.Close()
	in.AccountPurchases, err = readCommercePurchases(ctx, tx, account)
	if err == nil {
		observeInventoryProjection(ctx, in)
	}
	return in, err
}

func (s *Store) Receipt(ctx context.Context, l Lease, key string) (json.RawMessage, bool, error) {
	type found struct {
		payload json.RawMessage
		ok      bool
	}
	v, err := writeTx(ctx, s.db, func(tx *sql.Tx) (found, error) {
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil {
			return found{}, err
		}
		if r.SelectedID == 0 {
			return found{}, ErrPhase
		}
		var p string
		err = tx.QueryRowContext(ctx, "SELECT payload::text FROM s9a1.reward_receipts WHERE character_id=$1 AND event_key=$2", r.SelectedID, key).Scan(&p)
		if errors.Is(err, sql.ErrNoRows) {
			return found{}, nil
		}
		return found{json.RawMessage(p), true}, err
	})
	return v.payload, v.ok, err
}

// Receipts reads complete original entries for the selected character under
// one account lock so daily projections can join credit and claim keys.
func (s *Store) Receipts(ctx context.Context, l Lease) (map[string]json.RawMessage, error) {
	return s.readReceipts(ctx, l, "", nil)
}

// DailyRewardReceipts uses one transaction for the current day's counters,
// credits and their matching original free cards. Unrelated past combat and
// settlement receipts are not loaded into the game session.
func (s *Store) DailyRewardReceipts(ctx context.Context, l Lease, now time.Time) (map[string]json.RawMessage, error) {
	d := now.UTC().Add(2 * time.Hour)
	return s.readReceipts(ctx, l, `WITH counters AS MATERIALIZED (
		SELECT event_key,payload FROM s9a1.reward_receipts WHERE character_id=$1
		AND split_part(event_key,':',1) IN ('dungeon-special','dungeon-special-credit') AND split_part(event_key,':',3)=$2),
		identities AS (SELECT (payload->>'Input')::json AS input FROM counters WHERE split_part(event_key,':',1)='dungeon-special'),
		keys AS (SELECT DISTINCT 'dungeon-free-card:v1:'||replace(input->>'Owner','-','')||':'||(input->>'Epoch')||':'||
			(input->>'CharacterId')||':'||replace(input->>'RunId','-','') AS event_key FROM identities)
		SELECT event_key,payload::text FROM counters UNION ALL
		SELECT r.event_key,r.payload::text FROM keys JOIN s9a1.reward_receipts r USING(event_key) WHERE r.character_id=$1`, fmt.Sprint(d.Year()*10000+int(d.Month())*100+d.Day()))
}

func (s *Store) readReceipts(ctx context.Context, l Lease, query string, day any) (map[string]json.RawMessage, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (map[string]json.RawMessage, error) {
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil {
			return nil, err
		}
		if r.SelectedID == 0 {
			return nil, ErrPhase
		}
		args := []any{r.SelectedID}
		if query == "" {
			query = "SELECT event_key,payload::text FROM s9a1.reward_receipts WHERE character_id=$1"
		} else {
			args = append(args, day)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]json.RawMessage{}
		for rows.Next() {
			var key, p string
			if err = rows.Scan(&key, &p); err != nil {
				return nil, err
			}
			out[key] = json.RawMessage(p)
		}
		return out, rows.Err()
	})
}
