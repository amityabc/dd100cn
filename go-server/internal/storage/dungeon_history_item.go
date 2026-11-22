package storage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
)

// ValidDungeonHistoryCore applies the legacy OrdinaryNew/SupportedNew
// contract to a frozen award, including the original visible inventory core.
func ValidDungeonHistoryCore(template, attributes json.RawMessage, core []byte, special bool) bool {
	var t materialDefinition
	var v materialAttributes
	if len(core) != 119 || json.Unmarshal(template, &t) != nil || json.Unmarshal(attributes, &v) != nil {
		return false
	}
	plain := func(core []byte, attr materialAttributes) bool {
		ok, err := ordinaryNewCore(Asset{Core: core}, t, attr)
		return err == nil && ok
	}
	if plain(core, v) {
		return true
	}
	if !special {
		return false
	}
	var gear struct {
		WornSlotIsNative bool
		MythologyHash    string
		MythologyOptions byte
	}
	if json.Unmarshal(template, &gear) != nil {
		return false
	}
	equipment := t.Kind != 2 && (t.PvfGiftItem || t.Kind == 0 || gear.WornSlotIsNative)
	raw := bytes.Clone(core)
	if v.Reinforcement != 0 {
		if !equipment || v.Reinforcement > 31 || raw[10] != v.Reinforcement {
			return false
		}
		raw[10], v.Reinforcement = 0, 0
		return plain(raw, v)
	}
	if v.InitialAmplification {
		kind, value := v.AmplificationType, v.AmplificationValue
		base := kind & 0x7f
		if !equipment || base < 1 || base > 4 || value == 0 {
			return false
		}
		if kind&0x80 != 0 {
			kind, value = 0x80, 0
		}
		if raw[19] != kind || binary.LittleEndian.Uint16(raw[20:22]) != value {
			return false
		}
		clear(raw[19:22])
		v.InitialAmplification, v.AmplificationType, v.AmplificationValue = false, 0, 0
	}
	if v.Mythology != nil {
		var myth struct {
			SourceHash                                 string
			Count, First, Second, Third, Fourth, Fifth byte
			Valid                                      bool
		}
		if json.Unmarshal(*v.Mythology, &myth) != nil || t.Kind != 1 || !myth.Valid || !matchesSHA(myth.SourceHash) ||
			myth.SourceHash != gear.MythologyHash || myth.Count != gear.MythologyOptions ||
			myth.Count != 3 && myth.Count != 4 || myth.Fifth != 0 || myth.Count == 3 && myth.Fourth != 0 ||
			!bytes.Equal(raw[114:119], []byte{myth.First, myth.Second, myth.Third, myth.Fourth, myth.Fifth}) {
			return false
		}
		clear(raw[114:119])
		v.Mythology = nil
	}
	return plain(raw, v)
}
