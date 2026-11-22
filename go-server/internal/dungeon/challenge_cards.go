package dungeon

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"s9a1/server/internal/storage"
)

const challengeCardPolicy = "LocalFarmingChallengeResourcePremium/v1"

type challengeCardData struct {
	Dungeon      uint32
	Stage        uint16
	Hash         string
	Chance, Cost uint32
	Choices      []specialCardChoice
	Templates    map[uint32]json.RawMessage
	Definitions  map[byte]string
}

func (c *CardData) paidPlan(r *Run, weight uint32, level byte, luck *LuckRoll) (*CardReward, error) {
	d := r.Definition.Dungeon
	var source *challengeCardData
	for i := range c.Challenge {
		if row := &c.Challenge[i]; row.Dungeon == d.ID && row.Stage == d.ChallengeStage {
			source = row
			break
		}
	}
	if source == nil {
		return c.Paid.plan(r, weight, level, luck)
	}
	if d.Difficulty != 4 || d.ChallengeStage == 0 || source.Chance > 10000 || source.Cost == 0 ||
		source.Definitions[d.MazeIndex] != d.ResourceHash || !isHash(source.Hash) {
		return nil, storage.ErrInvalid
	}
	policy := fmt.Sprintf("%s/%d/%d", challengeCardPolicy, d.ID, d.ChallengeStage)
	draw := newSpecialCardDraw(fmt.Appendf(nil, "%s\n%d\n%d\n%s", r.idKey(), r.Seed, weight, source.Hash))
	ticket, err := draw(10000)
	if err != nil {
		return nil, err
	}
	if ticket >= source.Chance {
		fallback, err := c.Paid.plan(r, weight, level, luck)
		if err != nil || fallback == nil {
			return fallback, err
		}
		fingerprint := sha256.Sum256([]byte(source.Hash + "\n" + fallback.ResourceHash))
		fallback.ResourceHash, fallback.Policy = strings.ToUpper(hex.EncodeToString(fingerprint[:])), policy+";ordinary="+fallback.Policy
		return fallback, nil
	}
	selected, err := chooseSpecialCard(source.Choices, draw)
	if err != nil {
		return nil, err
	}
	template := source.Templates[selected.Id]
	var t dropTemplate
	if json.Unmarshal(template, &t) != nil || t.TemplateID != selected.Id ||
		t.Kind != 1 && t.Kind != 2 || selected.Count == 0 || t.Kind == 1 && selected.Count != 1 {
		return nil, storage.ErrInvalid
	}
	var quality uint32
	if t.Kind == 1 {
		seed, err := rand.Int(rand.Reader, big.NewInt(999999998))
		if err != nil {
			return nil, err
		}
		quality = uint32(seed.Uint64()) + 1
	}
	item, ok, err := cardItem(dropCandidate{Id: selected.Id, Template: template}, selected.Count, quality, nil, nil)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, storage.ErrInvalid
	}
	return &CardReward{Policy: policy, ResourceHash: source.Hash, Cost: source.Cost, Surprise: true, Items: []CardItem{item}}, nil
}
