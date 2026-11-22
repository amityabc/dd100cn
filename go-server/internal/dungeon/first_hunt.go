package dungeon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"s9a1/server/internal/opening"
	"s9a1/server/internal/storage"
)

type FirstHuntPolicy struct {
	Hash                            string
	BossMultiplier, NamedMultiplier uint32
}

type FirstHunt struct {
	Version, Hash               string
	Code                        uint32
	Difficulty, Kind            byte
	Named                       bool
	History                     byte
	Multiplier                  uint32
	PreviousKey, PreviousDigest *string
}

type historicalDeath struct {
	Account, OwnerGuid, RunId, ResourceHash, ExperienceHash, Version string
	Epoch                                                            int64
	Character, Actor                                                 uint16
	Dungeon, MapId, Code, ExperienceAward                            uint32
	MazeIndex, RoomX, RoomY, Level, Type                             byte
	Maps                                                             []uint32
	BlocksRoom, Dummy                                                bool
	AiScriptIndex                                                    *byte
	CleanupBossActor                                                 uint16
	ScriptTerminal, StormCleanup                                     json.RawMessage
	Body                                                             []byte
}

type huntReceipt struct {
	Key, Input, Result string
	Revision           int64
}

const firstHuntVersion = "OrdinarySoloFirstHunt/v1;native-mon-classification;character-difficulty-history;unknown-history-no-extra"

func (c *Catalog) FirstHunt(r *Run, d Death) *FirstHunt {
	p := c.Drops.FirstHunt
	def, monster := r.Definition.Dungeon, d.Monster
	if p == nil || d.Removal || def.Difficulty > 4 || !def.UsesOrdinaryMonsterDrops || !monster.BlocksRoom ||
		monster.Dummy || monster.AiScriptIndex != nil || monster.HellPhase != 0 {
		return nil
	}
	mon := c.Drops.Monsters[monster.Code]
	named := legacyNamed(monster.Code) || mon.Named != nil && *mon.Named
	plan := &FirstHunt{Version: firstHuntVersion, Hash: p.Hash, Code: monster.Code, Difficulty: def.Difficulty, Named: named, Multiplier: 1}
	switch {
	case monster.Type == 3:
		plan.Kind = 1
	case named:
		plan.Kind = 2
	}
	return plan
}

func legacyNamed(code uint32) bool {
	return code >= 50000 && code <= 51000 || code >= 56000 && code <= 60000
}

func (p FirstHunt) IndexKey() string {
	return fmt.Sprintf("dungeon-first-hunt:v1:%d:%d:%d", p.Kind, p.Code, p.Difficulty)
}

func (c *Catalog) ReadFirstHunt(p *FirstHunt, who storage.Character, account string, receipts map[string]json.RawMessage) error {
	if p == nil || p.Kind == 0 {
		return nil
	}
	reference := func(receipt huntReceipt, history byte) {
		s := sha256.Sum256([]byte(receipt.Key + "\n" + receipt.Input + "\n" + receipt.Result))
		digest := strings.ToUpper(hex.EncodeToString(s[:]))
		p.History, p.PreviousKey, p.PreviousDigest = history, &receipt.Key, &digest
	}
	if raw, ok := receipts[p.IndexKey()]; ok {
		var index huntReceipt
		var content struct {
			Version, Account, DeathKey, Digest string
			Character                          uint16
			Code                               uint32
			Kind, Difficulty                   byte
		}
		if json.Unmarshal(raw, &index) != nil || json.Unmarshal([]byte(index.Input), &content) != nil ||
			index.Key != p.IndexKey() || index.Result != "observed" || index.Revision <= 0 || index.Revision > who.Revision ||
			content.Version != "OrdinarySoloFirstHuntIndex/v1" || content.Account != account || content.Character != who.ID ||
			content.Code != p.Code || content.Kind != p.Kind || content.Difficulty != p.Difficulty {
			return storage.ErrConflict
		}
		death, _, result, err := historicalFact(who, account, content.DeathKey, receipts[content.DeathKey])
		if err != nil || death.Revision != index.Revision || result.FirstHunt == nil || result.FirstHunt.Kind != p.Kind ||
			result.FirstHunt.Code != p.Code || result.FirstHunt.Difficulty != p.Difficulty {
			return storage.ErrConflict
		}
		reference(death, 2)
		if *p.PreviousDigest != content.Digest {
			return storage.ErrConflict
		}
		return nil
	}
	keys := make([]string, 0, len(receipts))
	for k := range receipts {
		if strings.HasPrefix(k, "dungeon-death:") {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var seen, unknown *huntReceipt
	for _, key := range keys {
		var raw huntReceipt
		var input historicalDeath
		if json.Unmarshal(receipts[key], &raw) != nil || json.Unmarshal([]byte(raw.Input), &input) != nil {
			return storage.ErrConflict
		}
		if input.Code != p.Code || !input.BlocksRoom || input.Dummy || input.AiScriptIndex != nil || input.CleanupBossActor != 0 ||
			len(input.ScriptTerminal) > 0 && string(input.ScriptTerminal) != "null" ||
			len(input.StormCleanup) > 0 && string(input.StormCleanup) != "null" ||
			input.Version == "TrustedLocalClientUnattributedDeath/v1" {
			continue
		}
		r, in, result, err := historicalFact(who, account, key, receipts[key])
		if err != nil {
			return err
		}
		if result.FirstHunt != nil {
			typed := result.FirstHunt
			if typed.Code == p.Code && typed.Kind == p.Kind && typed.Difficulty == p.Difficulty {
				seen = &r
				break
			}
			continue
		}
		if (in.Type == 3) != (p.Kind == 1) {
			continue
		}
		diff, known := c.historicalDifficulty(in)
		if known && diff != p.Difficulty {
			continue
		}
		if known && (p.Kind == 1 || legacyNamed(p.Code)) {
			seen = &r
			break
		}
		if unknown == nil {
			unknown = &r
		}
	}
	switch {
	case seen != nil:
		reference(*seen, 2)
	case unknown != nil:
		reference(*unknown, 3)
	default:
		p.History = 1
		p.Multiplier = c.Drops.FirstHunt.NamedMultiplier
		if p.Kind == 1 {
			p.Multiplier = c.Drops.FirstHunt.BossMultiplier
		}
	}
	return nil
}

func historicalFact(c storage.Character, account, key string, raw json.RawMessage) (huntReceipt, historicalDeath, DeathResult, error) {
	var receipt huntReceipt
	var in historicalDeath
	var result DeathResult
	bad := func() (huntReceipt, historicalDeath, DeathResult, error) {
		return receipt, in, result, storage.ErrConflict
	}
	if json.Unmarshal(raw, &receipt) != nil || json.Unmarshal([]byte(receipt.Input), &in) != nil ||
		json.Unmarshal([]byte(receipt.Result), &result) != nil || receipt.Key != key || receipt.Revision <= 0 ||
		receipt.Revision > c.Revision || in.Account != account || in.Character != c.ID || in.Epoch <= 0 ||
		!isHash(in.ResourceHash) || !isHash(in.ExperienceHash) || in.Actor < 402 || result.Actor != in.Actor ||
		!slices.Equal(in.Maps, result.Maps) || result.ExperienceAward > in.ExperienceAward || len(in.Body) < 74 ||
		len(in.Body) != 74+14*int(in.Body[30]) || le.Uint32(in.Body) != uint32(in.Actor) {
		return bad()
	}
	derived := fmt.Sprintf("dungeon-death:%s:%d:%d:%d:%d", strings.ReplaceAll(in.RunId, "-", ""), in.MazeIndex, in.RoomX, in.RoomY, in.Actor)
	if key != derived {
		return bad()
	}
	return receipt, in, result, nil
}

func (c *Catalog) historicalDifficulty(in historicalDeath) (byte, bool) {
	diffs := map[byte]bool{}
	for _, ids := range c.rows {
		for _, row := range ids[in.Dungeon] {
			d := row.Dungeon
			if d.MazeIndex != in.MazeIndex || !strings.EqualFold(d.ResourceHash, in.ResourceHash) {
				continue
			}
			for _, room := range d.Rooms {
				if room.X != in.RoomX || room.Y != in.RoomY || room.MapID != in.MapId || !slices.Equal(roomMaps(&room), in.Maps) ||
					!slices.ContainsFunc(room.Monsters, func(m opening.Monster) bool {
						return m.Code == in.Code && m.Type == in.Type && m.BlocksRoom == in.BlocksRoom && !m.Dummy && m.AiScriptIndex == nil && m.HellPhase == 0
					}) {
					continue
				}
				diffs[d.Difficulty] = true
				for _, v := range d.SelectableDifficulties {
					diffs[v] = true
				}
			}
		}
	}
	for one := range diffs {
		return one, len(diffs) == 1
	}
	return 0, false
}
