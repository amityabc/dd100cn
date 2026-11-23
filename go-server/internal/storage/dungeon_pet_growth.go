package storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const petGrowthPolicy = "90CN-room-clear-1-v1"

type PetGrowthRule struct {
	Pet struct {
		PetCareRule
		SpeciesID                                   uint32 `json:"SpeciesId"`
		SpeciesHash                                 string
		StartLevel, PermissionLevel, OverskillLevel byte
		AutomaticFeedThreshold                      byte
	}
	Thresholds []uint32
	TableHash  string
}

type PetGrowthDeath struct {
	Actor uint16
	Key   string
}

type DungeonPetGrowth struct {
	Rule           *PetGrowthRule
	Now            time.Time
	BlockingDeaths []PetGrowthDeath
	Applied        *PetGrowthNotice
}

type PetGrowthNotice struct {
	Level, Mode byte
	Experience  uint32
}

type dungeonDeathReceiptIdentity struct {
	OwnerGuid, Account, RunId, ResourceHash string
	Epoch                                   int64
	Character, Actor                        uint16
	Dungeon, MapID                          uint32
	MazeIndex, RoomX, RoomY                 byte
	BlocksRoom                              bool
}

func validPetGrowthRule(r *PetGrowthRule) bool {
	p := r.Pet
	if p.TemplateID != 100330838 || p.SpeciesID != 258 || p.StartLevel != 1 || p.MaximumLevel != 20 || p.PermissionLevel != 1 ||
		p.OverskillLevel != 20 || len(r.Thresholds) != 55 || r.Thresholds[0] == 0 {
		return false
	}
	for i, n := range r.Thresholds {
		if n > math.MaxInt32 || i > 0 && r.Thresholds[i-1] >= n {
			return false
		}
	}
	for _, h := range []string{p.EquipmentHash, p.SpeciesHash, p.CommonHash, r.TableHash} {
		if len(h) != 64 {
			return false
		}
		if _, err := hex.DecodeString(h); err != nil {
			return false
		}
	}
	return true
}

func petGrowthLevel(r *PetGrowthRule, exp uint32) byte {
	level := byte(1)
	for level < r.Pet.MaximumLevel && exp >= r.Thresholds[level-1] {
		level++
	}
	return level
}

func dungeonDeathReceiptKey(in dungeonDeathReceiptIdentity) string {
	return fmt.Sprintf("dungeon-death:%s:%d:%d:%d:%d", strings.ReplaceAll(in.RunId, "-", ""), in.MazeIndex, in.RoomX, in.RoomY, in.Actor)
}

func planDungeonPetGrowth(ctx context.Context, tx *sql.Tx, l Lease, before, next Character, ch DungeonChange) (Character, *PetGrowthNotice, string, string, error) {
	p := ch.PetGrowth
	if p == nil {
		return next, nil, "", "", nil
	}
	invalid := func() (Character, *PetGrowthNotice, string, string, error) { return next, nil, "", "", ErrConflict }
	if p.Rule == nil || !validPetGrowthRule(p.Rule) || p.Now.UnixMilli() <= 0 {
		return invalid()
	}
	rule := p.Rule
	base := CommerceTx{Character: before}
	_, items, _, err := base.loadSpecial()
	if err != nil {
		return next, nil, "", "", err
	}
	pet, err := readPetAt(items, 3, 26)
	if errors.Is(err, ErrInvalid) {
		return next, nil, "", "", nil
	}
	if err != nil {
		return next, nil, "", "", err
	}
	if pet.template.TemplateID != rule.Pet.TemplateID || pet.template.ResourceHash != rule.Pet.EquipmentHash ||
		pet.level >= rule.Pet.MaximumLevel {
		return next, nil, "", "", nil
	}
	exp, err := rawPetValue[uint32](pet.pet, "Exp")
	if err != nil {
		return next, nil, "", "", err
	}
	if pet.level != petGrowthLevel(rule, exp) || exp == math.MaxUint32 {
		return next, nil, "", "", nil
	}
	for _, f := range []string{"ModeFlag", "Evolution", "EvolutionProgress"} {
		n, err := rawPetValue[byte](pet.pet, f)
		if err != nil {
			return next, nil, "", "", err
		}
		if n != 0 {
			return next, nil, "", "", nil
		}
	}
	expiry, err := rawPetValue[*int64](pet.attributes, "ExpiresAt")
	if err != nil {
		return next, nil, "", "", err
	}
	if expiry != nil && *expiry <= p.Now.Unix() {
		return next, nil, "", "", nil
	}
	var life materialDefinition
	if json.Unmarshal(pet.fields["Template"], &life) != nil {
		return invalid()
	}
	if life.ResourceExpiration != nil && *life.ResourceExpiration <= p.Now.Unix() {
		return next, nil, "", "", nil
	}
	clock, err := rawPetValue[*savedPetClock](pet.pet, "SatietyClock")
	if err != nil {
		return next, nil, "", "", err
	}
	if clock == nil || clock.Owner != l.Owner || clock.Epoch != l.Epoch || clock.Mode != PetSpending ||
		clock.ResourceHash != rule.Pet.CommonHash || clock.SpendSeconds != rule.Pet.SpendSeconds ||
		clock.RecoverSeconds != rule.Pet.RecoverSeconds || clock.Maximum != rule.Pet.MaximumSatiety {
		return next, nil, "", "", nil
	}
	satiety, err := petClockValue(*clock, p.Now.UnixMilli())
	if err != nil {
		return next, nil, "", "", err
	}
	if satiety <= 0 {
		return next, nil, "", "", nil
	}
	var last dungeonDeathReceiptIdentity
	var result struct {
		Actor       uint16
		RoomCleared bool
	}
	if json.Unmarshal([]byte(ch.Input), &last) != nil || json.Unmarshal([]byte(ch.Result), &result) != nil ||
		!result.RoomCleared || result.Actor != last.Actor || last.OwnerGuid != l.Owner || last.Account != l.Account || last.Epoch != l.Epoch ||
		last.Character != before.ID || !matchesUUID(last.RunId) || ch.Key != dungeonDeathReceiptKey(last) {
		return invalid()
	}
	key := fmt.Sprintf("pet-room-growth:%s:%d:%d:%d", strings.ReplaceAll(last.RunId, "-", ""), last.MazeIndex, last.RoomX, last.RoomY)
	if _, err = readDungeonReceipt(ctx, tx, before, key); err == nil {
		return next, nil, "", "", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return next, nil, "", "", err
	}
	seen := map[uint16]bool{last.Actor: true}
	for _, prior := range p.BlockingDeaths {
		r, err := readDungeonReceipt(ctx, tx, before, prior.Key)
		if err != nil {
			return next, nil, "", "", err
		}
		var death dungeonDeathReceiptIdentity
		var priorResult struct{ Actor uint16 }
		if json.Unmarshal([]byte(r.Input), &death) != nil || json.Unmarshal([]byte(r.Result), &priorResult) != nil ||
			death.Actor != prior.Actor || priorResult.Actor != prior.Actor || seen[prior.Actor] || !death.BlocksRoom || prior.Key != dungeonDeathReceiptKey(death) ||
			death.OwnerGuid != last.OwnerGuid || death.Account != last.Account || death.Epoch != last.Epoch ||
			death.Character != last.Character || death.RunId != last.RunId || death.Dungeon != last.Dungeon || death.MazeIndex != last.MazeIndex ||
			death.RoomX != last.RoomX || death.RoomY != last.RoomY || death.MapID != last.MapID || death.ResourceHash != last.ResourceHash {
			return invalid()
		}
		seen[prior.Actor] = true
	}
	mutation := CommerceTx{Character: next}
	state, afterItems, _, err := mutation.loadSpecial()
	if err != nil {
		return next, nil, "", "", err
	}
	after, err := readPetAt(afterItems, 3, 26)
	if err != nil || !equalJSON(items[pet.index], afterItems[after.index]) {
		return invalid()
	}
	notice := &PetGrowthNotice{Level: petGrowthLevel(rule, exp+1), Experience: exp + 1}
	if err = setPetValue(after.pet, "Exp", notice.Experience); err != nil {
		return next, nil, "", "", err
	}
	if err = setPetValue(after.pet, "Level", notice.Level); err != nil {
		return next, nil, "", "", err
	}
	if err = mutation.savePetAt(state, afterItems, after, nil); err != nil {
		return next, nil, "", "", err
	}
	petID, err := rawPetValue[string](after.fields, "InstanceId")
	if err != nil {
		return next, nil, "", "", err
	}
	proof, err := json.Marshal(struct {
		Policy, RunId                                     string
		Actor                                             uint16
		OwnerGuid                                         string
		Epoch                                             int64
		Character                                         uint16
		Pet                                               string
		NativeSerial                                      uint32
		EquipmentHash, SpeciesHash, CommonHash, TableHash string
		Now                                               int64
	}{petGrowthPolicy, last.RunId, last.Actor, l.Owner, l.Epoch, before.ID, petID, pet.serial,
		rule.Pet.EquipmentHash, rule.Pet.SpeciesHash, rule.Pet.CommonHash, rule.TableHash, p.Now.UnixMilli()})
	if err != nil {
		return next, nil, "", "", err
	}
	return mutation.Character, notice, key, string(proof), nil
}
