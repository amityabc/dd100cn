package protocol

import (
	"errors"
	"strings"
	"unicode"

	"golang.org/x/text/encoding/simplifiedchinese"
)

type PartyCreate struct {
	Kind, Style, Capacity, Difficulty, Detail, RaidType, Options byte
	Title                                                        []byte
	Dungeon                                                      uint32
	DetailWord, RaidMode                                         uint16
}

func ReadPartyCreate(body []byte) (PartyCreate, error) {
	var p PartyCreate
	if len(body) < 15 {
		return p, errors.New("short party creation body")
	}
	p.Kind, p.Style = body[0], body[1]
	b := body[2:]
	if p.Style == 0 {
		if len(b) < 17 || uint64(little.Uint32(b)) != uint64(len(b)-17) {
			return p, errors.New("invalid native party title length")
		}
		p.Title = b[4 : len(b)-13]
		b = b[len(b)-13:]
	}
	if len(b) != 13 {
		return p, errors.New("unexpected native party creation tail")
	}
	p.Capacity, p.Dungeon, p.Difficulty = b[0], little.Uint32(b[1:]), b[5]
	p.DetailWord, p.Detail, p.RaidType, p.RaidMode, p.Options = little.Uint16(b[6:]), b[8], b[9], little.Uint16(b[10:]), b[12]
	return p, nil
}

func (p PartyCreate) Ordinary() bool {
	return p.Kind <= 1 && p.Style <= 7 && len(p.Title) <= 128 && p.Capacity >= 1 && p.Capacity <= 4 && p.Difficulty <= 5 &&
		p.Detail <= 1 && p.Options&^7 == 0 && p.Dungeon == 0 && p.DetailWord == 0 && p.RaidType == 0 && p.RaidMode == 0
}

func displayHeader(id uint16, group, channel byte) bodyWriter {
	var w bodyWriter
	w.u16(1)
	w.u16(9999)
	w.u16(id)
	w.u8(group)
	w.u8(channel)
	return w
}

func PartyRemove(id uint16, group, channel byte, native bool) []byte {
	w := displayHeader(id, group, channel)
	w.raw([]byte{0, 3})
	if native {
		w.u16(0)
	}
	return w
}

func (p PartyCreate) Self(id uint16, group, channel byte) []byte {
	w := displayHeader(id, group, channel)
	w.zeros(4)
	w.u8(p.Kind)
	w.u8(p.Style)
	if p.Style == 0 {
		w.u32(0)
	}
	w.u8(0)
	w.u8(p.Capacity)
	w.u32(p.Dungeon)
	w.u8(p.Difficulty)
	w.u16(p.DetailWord)
	w.u8(p.Detail)
	w.zeros(16) // Ordinary party has no semi-raid or dungeon detail.
	w.u8(1)
	w.u8(0)
	w.u16(id)
	w.zeros(26) // One known member, zero extra pairs and trailing fields.
	return w
}

func Announcement(s string) ([]byte, error) {
	if strings.TrimSpace(s) == "" || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return nil, errors.New("invalid announcement")
	}
	b, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		return nil, err
	}
	if len(b) > 512 {
		return nil, errors.New("announcement is too long")
	}
	w := bodyWriter{0}
	w.block(b)
	return w, nil
}
