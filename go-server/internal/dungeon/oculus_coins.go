package dungeon

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"s9a1/server/internal/storage"
)

func (r *Run) OculusCoins(mode OculusMode, monsterReceipts map[string]storage.DungeonSceneReceipt, ledger storage.OculusPlayerLedger) (storage.OculusClearCoins, error) {
	if _, err := r.OculusHistory(monsterReceipts); err != nil {
		return storage.OculusClearCoins{}, err
	}
	if mode.SourceCoinLimit == 0 || r.OculusEntry.Mode != mode.Mode || r.OculusEntry.ModeHash != mode.SourceHash ||
		ledger.AdmissionRevision <= 0 || ledger.CharacterRevision < ledger.AdmissionRevision {
		return storage.OculusClearCoins{}, storage.ErrConflict
	}
	bonuses := make(map[uint32]int, len(mode.Rules.KillCoinLimitBonuses))
	for _, row := range mode.Rules.KillCoinLimitBonuses {
		if row.Id == 0 || row.RawValue < 1 || row.RawValue > math.MaxUint8 || bonuses[row.Id] != 0 {
			return storage.OculusClearCoins{}, storage.ErrConflict
		}
		bonuses[row.Id] = row.RawValue
	}
	scenes := append(append([]*Run{}, r.OculusLayout.previous...), r)
	total := int(mode.SourceCoinLimit)
	for _, scene := range scenes {
		for position, room := range scene.rooms {
			first := scene.bases[position]
			for i, monster := range room.Monsters {
				if _, dead := scene.Dead[first+uint16(i)]; dead {
					total += bonuses[monster.Code]
				}
			}
		}
	}
	if total > math.MaxUint8 {
		return storage.OculusClearCoins{}, storage.ErrConflict
	}
	deathPrefix, revivePrefix := "death:"+r.idKey()+":", "revive:"+r.idKey()+":"
	deaths, revivals := map[uint32]storage.DungeonSceneReceipt{}, map[uint32]storage.DungeonSceneReceipt{}
	for key, row := range ledger.Receipts {
		target, prefix, result := deaths, deathPrefix, "self-forfeit-hp0"
		if strings.HasPrefix(key, revivePrefix) {
			target, prefix, result = revivals, revivePrefix, "coin:-1;hp/mp:maximum"
		}
		cycle, err := strconv.ParseUint(strings.TrimPrefix(key, prefix), 10, 32)
		if err != nil || cycle == 0 || cycle > uint64(r.DeathCycle) || key != prefix+strconv.FormatUint(cycle, 10) ||
			row.Key != key || row.Result != result || row.Revision <= ledger.AdmissionRevision || row.Revision > ledger.CharacterRevision {
			return storage.OculusClearCoins{}, storage.ErrConflict
		}
		target[uint32(cycle)] = row
	}
	if uint32(len(deaths)) != r.DeathCycle || len(revivals) > total {
		return storage.OculusClearCoins{}, storage.ErrConflict
	}
	previous := ledger.AdmissionRevision
	sourceIndex := 0
	for ordinal := uint64(1); ordinal <= uint64(r.DeathCycle); ordinal++ {
		cycle := uint32(ordinal)
		death, ok := deaths[cycle]
		if !ok || death.Revision <= previous {
			return storage.OculusClearCoins{}, storage.ErrConflict
		}
		var in struct {
			Version            byte
			RunId              string
			CharacterId        uint16
			Room               *struct{ X, Y byte }
			DeathCycle         uint32
			X, Y, RelatedActor uint16
		}
		if json.Unmarshal([]byte(death.Input), &in) != nil || in.Version != 1 || in.DeathCycle != cycle ||
			in.RunId != r.ID || in.CharacterId != r.Character || in.RelatedActor != 0 && in.RelatedActor != r.Character || in.Room == nil {
			return storage.OculusClearCoins{}, storage.ErrConflict
		}
		for sourceIndex < len(scenes) && cycle > scenes[sourceIndex].DeathCycle {
			sourceIndex++
		}
		if sourceIndex == len(scenes) {
			return storage.OculusClearCoins{}, storage.ErrConflict
		}
		source := scenes[sourceIndex]
		position := cell{in.Room.X, in.Room.Y}
		expected, err := source.playerDeathInput(source.rooms[position], cycle, in.X, in.Y, in.RelatedActor)
		if err != nil || !source.seen[scene{position, false}] || expected != death.Input {
			return storage.OculusClearCoins{}, storage.ErrConflict
		}
		previous = death.Revision
		if revival, ok := revivals[cycle]; ok {
			if revival.Revision <= previous || revival.Input != death.Input {
				return storage.OculusClearCoins{}, storage.ErrConflict
			}
			previous = revival.Revision
		}
	}
	if r.PlayerDeath != nil {
		_, alreadyRevived := revivals[r.DeathCycle]
		if r.DeathCycle == 0 || alreadyRevived || r.PlayerDeath.input != deaths[r.DeathCycle].Input {
			return storage.OculusClearCoins{}, storage.ErrConflict
		}
	}
	return storage.OculusClearCoins{Total: byte(total), Used: byte(len(revivals))}, nil
}
