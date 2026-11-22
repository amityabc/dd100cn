package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
)

func (s *Store) AccrueDungeonPlayMinute(ctx context.Context, l Lease, expected Character, interval, points int64) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		c, err := dungeonCharacter(ctx, tx, l, expected)
		if err != nil {
			return struct{}{}, err
		}
		var profile dungeonProgression
		if err := json.Unmarshal(c.Gameplay, &profile); err != nil {
			return struct{}{}, err
		}
		if !profile.Initialized {
			return struct{}{}, ErrPhase
		}
		progress := profile.Progression
		luck, minutes := int64(progress.LuckPoints), int64(progress.LuckPlayMinutes)
		if luck < 0 || luck > 100000 || minutes < 0 || interval <= 0 || interval > math.MaxInt32 ||
			points < 0 || points > math.MaxInt32 {
			return struct{}{}, ErrInvalid
		}
		minutes++
		if minutes >= interval {
			minutes = 0
			if luck == 100000 || points == 100000 {
				luck = 100000
			} else {
				luck = min(99999, luck+points)
			}
		}
		if minutes == int64(progress.LuckPlayMinutes) && luck == int64(progress.LuckPoints) {
			return struct{}{}, nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE s9a1.characters SET gameplay_json=
			jsonb_set(gameplay_json::jsonb, '{Progression}', gameplay_json::jsonb->'Progression' ||
				jsonb_build_object('LuckPlayMinutes',$4::int,'LuckPoints',$5::int))::json,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, c.ID, c.Revision, minutes, luck)
		return struct{}{}, err
	})
	return err
}
