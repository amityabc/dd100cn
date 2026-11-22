package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type DungeonFirstHunt struct {
	Code                        uint32
	Kind, Difficulty, History   byte
	PreviousKey, PreviousDigest *string
}

func firstHuntDigest(key, input, result string) string {
	sum := sha256.Sum256([]byte(key + "\n" + input + "\n" + result))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func attachDungeonFirstHunt(ctx context.Context, tx *sql.Tx, l Lease, c Character, ch DungeonChange) error {
	p := ch.FirstHunt
	if p == nil || p.Kind == 0 {
		return nil
	}
	if p.Code == 0 || p.Kind > 2 || p.Difficulty > 4 || p.History < 1 || p.History > 3 {
		return ErrInvalid
	}
	if p.PreviousKey != nil && p.PreviousDigest != nil {
		previous, err := readDungeonReceipt(ctx, tx, c, *p.PreviousKey)
		if err != nil {
			return err
		}
		if p.History == 1 || firstHuntDigest(previous.Key, previous.Input, previous.Result) != *p.PreviousDigest {
			return ErrConflict
		}
	} else if p.History != 1 || p.PreviousKey != nil || p.PreviousDigest != nil {
		return ErrInvalid
	}
	key := fmt.Sprintf("dungeon-first-hunt:v1:%d:%d:%d", p.Kind, p.Code, p.Difficulty)
	_, err := readDungeonReceipt(ctx, tx, c, key)
	if err == nil {
		if p.History != 2 {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	body, err := json.Marshal(struct {
		Version, Account string
		Character        uint16
		Code             uint32
		Kind, Difficulty byte
		DeathKey, Digest string
	}{"OrdinarySoloFirstHuntIndex/v1", l.Account, c.ID, p.Code, p.Kind, p.Difficulty, ch.Key, firstHuntDigest(ch.Key, ch.Input, ch.Result)})
	if err != nil {
		return err
	}
	return putDungeonReceipt(ctx, tx, c, key, string(body), "observed")
}
