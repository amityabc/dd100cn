package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"s9a1/server/internal/inventorywire"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/questwire"
)

type CharacterTemplate struct {
	Job, GrowType             byte
	SourceHash                string
	Stats, Gameplay           json.RawMessage
	OutdoorQuests, RoomQuests []uint16
}

type SeedData struct {
	DailyLimits map[uint32]byte
	Weights     map[uint32]int64
	Fate        *struct {
		StoryCapacity int
		SinCosts      []int32
		MinimumLevel  byte
		Town          FateTownRoute
	}
	OpeningCombat opening.CombatCatalog
}
type CharacterCatalog struct {
	rows  map[[2]byte]CharacterTemplate
	Towns *TownCatalog
	SeedData
	Openings     map[byte]opening.Definition
	CashPolicies map[uint32]inventorywire.CashPolicy
	Quests       *questwire.Catalog
}

func LoadCharacters(path, pvf, profile string) (*CharacterCatalog, error) {
	var raw struct {
		Schema                                                                    int
		PvfSha256, SidecarSha256, ProfileSha256, InfrastructureSha256, GameSha256 string
		Characters                                                                []CharacterTemplate
		Towns                                                                     townData
		SeedData
		Openings                                            []opening.Definition
		Quests                                              questwire.Data
		CashPurchasePolicies, CharacterCashPurchasePolicies map[uint32]inventorywire.CashPolicy
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Schema != 4 || len(raw.Characters) == 0 || len(raw.Characters) > 16*6 || !validHash(raw.InfrastructureSha256) || !validHash(raw.GameSha256) {
		return nil, fmt.Errorf("unsupported character catalog %s", path)
	}
	for _, entry := range []struct{ path, hash string }{{pvf, raw.PvfSha256}, {filepath.Join(filepath.Dir(pvf), "sk.dat"), raw.SidecarSha256}, {profile, raw.ProfileSha256}} {
		if err := matchHash(entry.path, entry.hash); err != nil {
			return nil, err
		}
	}
	towns, err := newTownCatalog(raw.Towns)
	if err != nil {
		return nil, err
	}
	c := &CharacterCatalog{rows: make(map[[2]byte]CharacterTemplate, len(raw.Characters)), Towns: towns,
		SeedData: raw.SeedData, Openings: make(map[byte]opening.Definition), CashPolicies: make(map[uint32]inventorywire.CashPolicy)}
	for _, d := range raw.Openings {
		if _, dup := c.Openings[d.Job]; dup || !validHash(d.Hash) || !validHash(d.Dungeon.ResourceHash) {
			return nil, fmt.Errorf("invalid opening for job %d", d.Job)
		}
		if _, _, err := d.Reserve(402); err != nil {
			return nil, err
		}
		c.Openings[d.Job] = d
	}
	for id, p := range raw.CashPurchasePolicies {
		c.CashPolicies[id] = p
	}
	for id, p := range raw.CharacterCashPurchasePolicies {
		if _, dup := c.CashPolicies[id]; dup {
			return nil, fmt.Errorf("ambiguous shop policy %d", id)
		}
		p.Character = true
		c.CashPolicies[id] = p
	}
	c.Quests, err = questwire.New(raw.Quests)
	if err != nil {
		return nil, err
	}
	if err = c.Quests.RequireOperations(); err != nil {
		return nil, err
	}
	for _, row := range raw.Characters {
		key := [2]byte{row.Job, row.GrowType}
		_, exists := c.rows[key]
		if row.Job > 15 || row.GrowType > 5 || exists || !validHash(row.SourceHash) || len(row.Stats) == 0 || len(row.Gameplay) == 0 {
			return nil, fmt.Errorf("invalid creation template %v", key)
		}
		c.rows[key] = row
	}
	return c, nil
}

func (c *CharacterCatalog) Template(job, grow byte) (CharacterTemplate, bool) {
	r, ok := c.rows[[2]byte{job, grow}]
	return r, ok
}

func validHash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size
}

func matchHash(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expected) {
		return fmt.Errorf("catalog input changed: %s", path)
	}
	return nil
}

// Creation preserves opaque fields from the legacy template, including complete integer precision.
func (c *CharacterCatalog) Creation(job, grow byte, now time.Time) (CharacterTemplate, bool, error) {
	r, ok := c.rows[[2]byte{job, grow}]
	if !ok {
		return CharacterTemplate{}, false, nil
	}
	var gameplay, progression map[string]json.RawMessage
	if err := json.Unmarshal(r.Gameplay, &gameplay); err != nil {
		return r, false, err
	}
	if err := json.Unmarshal(gameplay["Progression"], &progression); err != nil {
		return r, false, err
	}
	if _, found := progression["FatigueDay"]; !found {
		return r, false, fmt.Errorf("template lacks FatigueDay")
	}
	// LocalProgressionRules.DayKey resets at 06:00 UTC+8.
	day := now.UTC().Add(2 * time.Hour)
	progression["FatigueDay"] = json.RawMessage(strconv.Itoa(day.Year()*10000 + int(day.Month())*100 + day.Day()))
	var err error
	gameplay["Progression"], err = json.Marshal(progression)
	if err != nil {
		return r, false, err
	}
	r.Gameplay, err = json.Marshal(gameplay)
	return r, true, err
}
