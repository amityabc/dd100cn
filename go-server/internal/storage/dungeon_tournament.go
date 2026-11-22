package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type TournamentWinProof struct {
	RunId, ResourceHash string
	Dungeon             uint32
	Deaths              []string
}

func (p TournamentWinProof) key(l Lease, c Character) string {
	return fmt.Sprintf("tournament-claim:v1:%s:%d:%d:%s", strings.ReplaceAll(l.Owner, "-", ""), l.Epoch, c.ID, strings.ReplaceAll(p.RunId, "-", ""))
}

func (p TournamentWinProof) item() uint32 {
	switch p.Dungeon {
	case 123:
		return 3329
	case 124:
		return 3323
	}
	return 0
}

func (s *Store) ValidateTournamentAdmission(ctx context.Context, l Lease, expected Character) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		c, err := dungeonCharacter(ctx, tx, l, expected)
		if err != nil {
			return struct{}{}, err
		}
		var p dungeonProgression
		if json.Unmarshal(c.Gameplay, &p) != nil || !p.Initialized || p.Progression.Hp == 0 {
			return struct{}{}, ErrPhase
		}
		return struct{}{}, nil
	})
	return err
}

func readTournamentWins(ctx context.Context, tx *sql.Tx, l Lease, c Character, p TournamentWinProof) ([]deathReceipt, error) {
	if p.item() == 0 || !matchesUUID(p.RunId) || !matchesSHA(p.ResourceHash) || len(p.Deaths) != 4 {
		return nil, ErrInvalid
	}
	wins := make([]deathReceipt, 0, 4)
	codes := map[uint32]bool{}
	var revision int64
	var first uint16
	for i, key := range p.Deaths {
		win, err := readDungeonReceipt(ctx, tx, c, key)
		if err != nil {
			return nil, err
		}
		var in struct {
			Account, OwnerGuid, RunId string
			ResourceHash              string
			Epoch                     int64
			Character, Actor          uint16
			Dungeon, MapId, Code      uint32
			MazeIndex, RoomX, RoomY   byte
			Type, Level               byte
			BlocksRoom, Dummy         bool
			AiScriptIndex             *byte
		}
		var result struct {
			Actor                       uint16
			ExperienceAward             uint32
			Totals                      [4]uint32
			RoomCleared, DungeonCleared bool
			Drops                       []json.RawMessage
			DropRuleHash                string
		}
		if json.Unmarshal([]byte(win.Input), &in) != nil || json.Unmarshal([]byte(win.Result), &result) != nil {
			return nil, ErrConflict
		}
		if i == 0 {
			first = in.Actor
		}
		if win.Revision <= revision || win.Grants == nil || len(win.Grants) != 0 || in.Account != l.Account ||
			in.OwnerGuid != l.Owner || in.Epoch != l.Epoch || in.Character != c.ID || in.RunId != p.RunId ||
			in.Dungeon != p.Dungeon || in.ResourceHash != p.ResourceHash || in.MapId != 95265+p.Dungeon-123 ||
			in.MazeIndex != 0 || in.RoomX != 0 || in.RoomY != 0 || first < 402 || first > 65531 ||
			in.Actor != first+uint16(i) || !in.BlocksRoom || in.Dummy || in.AiScriptIndex != nil ||
			in.Code == 0 || codes[in.Code] || in.Type != 0 || in.Level != 75 || result.Actor != in.Actor ||
			result.ExperienceAward != 0 || result.Totals != ([4]uint32{}) || result.RoomCleared != (i == 3) ||
			result.DungeonCleared != (i == 3) || result.Drops == nil || len(result.Drops) != 0 || result.DropRuleHash != "" {
			return nil, ErrConflict
		}
		want := fmt.Sprintf("dungeon-death:%s:0:0:0:%d", strings.ReplaceAll(p.RunId, "-", ""), in.Actor)
		if key != want {
			return nil, ErrConflict
		}
		revision, codes[in.Code] = win.Revision, true
		wins = append(wins, win)
	}
	return wins, nil
}

func (s *Store) ValidateTournamentWins(ctx context.Context, l Lease, c Character, p TournamentWinProof) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) ([]deathReceipt, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return nil, err
		}
		return readTournamentWins(ctx, tx, l, current, p)
	})
	return err
}

func (s *Store) ClaimTournament(ctx context.Context, l Lease, c Character, p TournamentWinProof, group, card byte, now time.Time,
	grant func(uint32, Inventory) ([]QuestGrant, error)) (Inventory, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (Inventory, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return Inventory{}, err
		}
		if group != 0 || card > 1 || now.IsZero() || grant == nil {
			return Inventory{}, ErrInvalid
		}
		wins, err := readTournamentWins(ctx, tx, l, current, p)
		if err != nil {
			return Inventory{}, err
		}
		r, err := lockAccount(ctx, tx, l, false)
		if err != nil {
			return Inventory{}, err
		}
		in, err := readInventory(ctx, tx, l.Account, r)
		if err != nil {
			return Inventory{}, err
		}
		item := p.item()
		items, err := grant(item, in)
		if err != nil {
			return Inventory{}, err
		}
		if len(items) != 1 || items[0].TemplateID != item || items[0].Count != 1 || items[0].Location != 0 {
			return Inventory{}, ErrInvalid
		}
		input := struct {
			Policy, Owner string
			Epoch         int64
			Character     uint16
			Run           string
			Dungeon       uint32
			ResourceHash  string
			Group, Card   byte
			Deaths        []deathReceipt
			Reward        json.RawMessage
		}{"local100-tournament-four-rounds-basis-level-fixed-material-v1", l.Owner, l.Epoch, current.ID,
			p.RunId, p.Dungeon, p.ResourceHash, group, card, wins, items[0].Template}
		frozen, err := json.Marshal(input)
		if err != nil {
			return Inventory{}, err
		}
		result, err := json.Marshal(struct{ Item, Count uint32 }{item, 1})
		if err != nil {
			return Inventory{}, err
		}
		key := p.key(l, current)
		if prior, err := readDungeonReceipt(ctx, tx, current, key); err == nil {
			if prior.Input != string(frozen) || prior.Result != string(result) || len(prior.Grants) != 1 || prior.Revision <= wins[3].Revision {
				return Inventory{}, ErrConflict
			}
			return in, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Inventory{}, err
		}
		after, err := QuestLoot(in, nil, items, in.Gold, now)
		if err != nil {
			return Inventory{}, err
		}
		after.AssetRevision, err = writeQuestInventory(ctx, tx, l, current, in, after)
		if err != nil {
			return Inventory{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE s9a1.characters SET revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3`, l.Account, current.ID, current.Revision); err != nil {
			return Inventory{}, err
		}
		var grants []json.RawMessage
		if err := json.Unmarshal(QuestGranted(in, after), &grants); err != nil || len(grants) != 1 {
			return Inventory{}, ErrConflict
		}
		if err := putDungeonReceiptWithGrants(ctx, tx, current, key, string(frozen), string(result), grants); err != nil {
			return Inventory{}, err
		}
		after.Revision++
		return after, nil
	})
}

func (s *Store) ValidateTournamentClaim(ctx context.Context, l Lease, c Character, p TournamentWinProof) error {
	_, err := writeTx(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		current, err := dungeonCharacter(ctx, tx, l, c)
		if err != nil {
			return struct{}{}, err
		}
		wins, err := readTournamentWins(ctx, tx, l, current, p)
		if err != nil {
			return struct{}{}, err
		}
		prior, err := readDungeonReceipt(ctx, tx, current, p.key(l, current))
		if errors.Is(err, sql.ErrNoRows) {
			return struct{}{}, ErrPhase
		}
		if err != nil {
			return struct{}{}, err
		}
		var z struct{ Item, Count uint32 }
		if prior.Revision <= wins[3].Revision || len(prior.Grants) != 1 || json.Unmarshal([]byte(prior.Result), &z) != nil ||
			z.Item != p.item() || z.Count != 1 {
			return struct{}{}, ErrConflict
		}
		return struct{}{}, nil
	})
	return err
}
