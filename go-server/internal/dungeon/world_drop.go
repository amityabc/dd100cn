package dungeon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"s9a1/server/internal/storage"
)

const worldDropPolicy = "AwakeningSeals/v4;user-authorized-A21-port;complete-world-sources"

type WorldDropCatalog struct {
	Hash  string
	Rows  [][]uint32
	Seals []struct {
		Monster, Item uint32
		Probabilities []uint32
		Difficulty    int
	}
	Templates map[uint32]json.RawMessage
}

type WorldDropProof struct {
	Policy, OrdinaryHash, MaterialHash string
	FirstDrop, Count                   int
	SelectionHash                      string
}

// A21's material generator uses three independent high-bit slices per draw.
type worldLCG uint32

func (s *worldLCG) next(n uint32) uint32 {
	a := uint32(*s)*1103515245 + 12345
	b := a*1103515245 + 12345
	c := b*1103515245 + 12345
	*s = worldLCG(c)
	q := ((((a>>16)&0x7ff)<<10)^((b>>16)&0x3ff))<<10 | ((c >> 16) & 0x3ff)
	return q % n
}

func combineDropHash(ordinary, other string) string {
	h := sha256.Sum256([]byte("local100-combined-drop/v1\n" + ordinary + "\n" + other))
	return strings.ToUpper(hex.EncodeToString(h[:]))
}

func (p *WorldDropCatalog) Plan(run *Run, d Death, z *DeathResult) error {
	m, def := d.Monster, run.Definition.Dungeon
	if p == nil || def.DisableDrops || def.Tournament != nil || d.Removal || m.AiScriptIndex != nil || m.Dummy || !m.BlocksRoom || m.HellPhase == 8 {
		return nil
	}
	if !isHash(p.Hash) || len(p.Rows) != 200 || m.Type > 3 {
		return storage.ErrInvalid
	}
	id, err := guidWire(run.ID)
	if err != nil {
		return err
	}
	var seed [60]byte
	le.PutUint32(seed[:], run.Seed)
	copy(seed[4:], id)
	le.PutUint16(seed[20:], d.Actor)
	le.PutUint32(seed[22:], m.Code)
	seed[26], seed[27] = m.Level, def.Difficulty
	if _, err = hex.Decode(seed[28:], []byte(p.Hash)); err != nil {
		return err
	}
	h := sha256.Sum256(seed[:])
	random := worldLCG(le.Uint32(h[:]))
	var ids []uint32
	for _, row := range p.Seals {
		if row.Monster != m.Code || row.Difficulty >= 0 && row.Difficulty != int(def.Difficulty) {
			continue
		}
		if len(row.Probabilities) != 5 {
			return storage.ErrInvalid
		}
		if chance := row.Probabilities[min(4, def.Difficulty)]; chance > 0 && random.next(1000000) < chance {
			ids = append(ids, row.Item)
		}
	}
	if m.Level >= 1 && m.Level <= 199 {
		row := p.Rows[m.Level-1]
		if len(row) < 2 || len(row)%2 != 0 || row[0] != uint32(m.Level) {
			return storage.ErrInvalid
		}
		var total uint64
		for i := 3; i < len(row); i += 2 {
			total += uint64(row[i])
		}
		if total > math.MaxInt32 {
			return storage.ErrInvalid
		}
		if total != 0 && uint64(random.next(100000)) < total {
			ticket := random.next(uint32(total))
			for i := 2; i < len(row); i += 2 {
				w := row[i+1]
				if ticket < w {
					if len(p.Templates[row[i]]) != 0 {
						ids = append(ids, row[i])
					}
					break
				}
				ticket -= w
			}
		}
	}
	proof := &WorldDropProof{Policy: worldDropPolicy, OrdinaryHash: z.DropRuleHash, MaterialHash: p.Hash, FirstDrop: len(z.Drops), Count: len(ids)}
	if len(ids) > 3 {
		return storage.ErrInvalid
	}
	selected := make([]GroundReward, 0, len(ids))
	for _, item := range ids {
		raw := p.Templates[item]
		var t dropTemplate
		if json.Unmarshal(raw, &t) != nil || t.Kind != 2 || t.TemplateID != item {
			return storage.ErrInvalid
		}
		reward := GroundReward{Template: raw, PvfDungeonReward: t.PvfGiftItem}
		if t.PvfGiftItem {
			reward.StackCount = 1
		}
		selected = append(selected, reward)
	}
	raw, err := json.Marshal(selected)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("world-material-selection/v1\n%s\n%s\n%s\n%d\n%d\n%s",
		proof.Policy, proof.OrdinaryHash, proof.MaterialHash, proof.FirstDrop, proof.Count, raw)))
	proof.SelectionHash = strings.ToUpper(hex.EncodeToString(sum[:]))
	rewards := make([]GroundReward, 0, len(z.Drops)+len(selected))
	for _, drop := range z.Drops {
		rewards = append(rewards, drop.Reward)
	}
	drops, err := run.freezeDrops(d.Actor, d.Key, append(rewards, selected...))
	if err != nil {
		return err
	}
	z.Drops, z.MaterialPolicy, z.DropRuleHash = drops, proof, combineDropHash(z.DropRuleHash, p.Hash)
	return nil
}
