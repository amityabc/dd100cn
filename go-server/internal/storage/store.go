// Package storage owns PostgreSQL transactions for authenticated game sessions.
package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schemaSQL string

//go:embed assets.sql
var assetsSQL string

//go:embed premium.sql
var premiumSQL string

//go:embed moves.sql
var movesSQL string

//go:embed opening.sql
var openingSQL string

//go:embed opening_combat.sql
var openingCombatSQL string

//go:embed daily_receipts.sql
var dailyReceiptsSQL string

//go:embed town_transition.sql
var townTransitionSQL string

var (
	ErrLease         = errors.New("session no longer owns this account")
	ErrPhase         = errors.New("operation is unavailable in this phase")
	ErrInvalid       = errors.New("invalid game data")
	ErrCapacity      = errors.New("character capacity reached")
	ErrNameTaken     = errors.New("name is already used or reserved")
	ErrConflict      = errors.New("state revision or receipt conflicts")
	ErrFunds         = errors.New("wallet change would exceed its bounds")
	ErrCommitUnknown = errors.New("database commit result is unknown")
)

type Phase uint8

const (
	Authenticated Phase = iota
	CharacterList
	Selected
	TownLoading
	TownReady
	Exited
)

type Lease struct {
	Account, Owner string
	Epoch          int64
}

type Position struct {
	Town, Area, X, Y, WireTail uint16
	Direction, State           uint8
}

type Character struct {
	ID              uint16
	StorageSlot     uint8
	Name            string
	Job, Level      uint8
	Created         uint32
	Revision        int64
	Stats, Gameplay json.RawMessage
	ResourceHash    string
	Position        Position
	RoomReturn      *Position
	Gold, Coins     int64
	Worn            []Equipment
}

type CreateCharacter struct {
	Name            string
	Job             uint8
	Created         uint32
	Stats, Gameplay json.RawMessage
	ResourceHash    string
	Opening         *Position
}

type Roster struct {
	Characters                                                                  []Character
	SelectedID                                                                  uint16
	Phase                                                                       Phase
	Capacity                                                                    uint8
	Cera, CargoGold, AssetRevision                                              int64
	ConquerorExpiresAt, SkillMasterExpiresAt, CrystalExpiresAt, GrowthExpiresAt int64
	GameOptions, Hotkeys                                                        []byte
}

type Store struct{ db *sql.DB }

func Open(ctx context.Context, dsn string) (*Store, error) {
	if dsn == "" {
		return nil, errors.New("PostgreSQL connection string is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Initialize(ctx context.Context) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(1395736881,1)"); err != nil {
			return struct{}{}, err
		}
		if _, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS s9a1;
			CREATE TABLE IF NOT EXISTS s9a1.schema_version (singleton boolean PRIMARY KEY CHECK (singleton), version integer NOT NULL)`); err != nil {
			return struct{}{}, err
		}
		var version int
		err := tx.QueryRowContext(ctx, "SELECT version FROM s9a1.schema_version WHERE singleton").Scan(&version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return struct{}{}, err
		}
		migrations := []string{schemaSQL, assetsSQL, premiumSQL, movesSQL, openingSQL, openingCombatSQL, dailyReceiptsSQL, townTransitionSQL}
		if version < 0 || version > len(migrations) {
			return struct{}{}, fmt.Errorf("unsupported PostgreSQL schema version %d", version)
		}
		for version < len(migrations) {
			if _, err = tx.ExecContext(ctx, migrations[version]); err != nil {
				return struct{}{}, err
			}
			version++
			if _, err = tx.ExecContext(ctx, `INSERT INTO s9a1.schema_version VALUES (true,$1)
				ON CONFLICT(singleton) DO UPDATE SET version=excluded.version`, version); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

// A failed connection during COMMIT may have persisted the change. Callers
// must retire that session and recover durable state before interpreting it.
func writeTx[T any](ctx context.Context, db *sql.DB, fn func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	value, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return zero, err
		}
		return zero, fmt.Errorf("%w: %w", ErrCommitUnknown, err)
	}
	return value, nil
}

func validName(name string) bool {
	if !utf8.ValidString(name) {
		return false
	}
	n := 0
	for _, r := range name {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
		n++
	}
	return n >= 2 && n <= 30
}

func nameKey(name string) string {
	// The original folds ASCII letters only. Unicode case folding changes old names.
	raw := []byte(name)
	for i, c := range raw {
		if c >= 'A' && c <= 'Z' {
			raw[i] = c + ('a' - 'A')
		}
	}
	return string(raw)
}
