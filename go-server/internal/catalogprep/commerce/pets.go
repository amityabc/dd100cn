package commerce

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/storage"
)

type Pets struct {
	PetHatch      map[uint32]storage.PetHatchDefinition
	PetNames      map[uint32]storage.PetNameRule
	PetRenameCard *storage.PetRenameCard
	PetCare       *storage.PetCareRule
	PetFeed       *storage.PetFeedRule
}

func BuildPets(items *Items, roots []uint32) (*Pets, error) {
	out := &Pets{PetHatch: map[uint32]storage.PetHatchDefinition{}, PetNames: map[uint32]storage.PetNameRule{}}
	root := map[uint32]bool{}
	for _, id := range roots {
		root[id] = true
	}
	for id := range root {
		src, err := items.Source(id)
		if err != nil || !src.Equipment || cashField(src, "equipment type") != "`[creature]` 0" {
			continue
		}
		switch cashField(src, "sub type") {
		case "0":
			if p, err := src.SpecialGM(nil); err == nil && !p.Template.InitialSealed && p.InitialPetName != "" {
				out.PetNames[id] = storage.PetNameRule{TemplateID: id, ResourceHash: src.Hash, DefaultName: p.InitialPetName}
			}
		case "1":
			dest, err := unsigned(cashField(src, "output index"), 32)
			if err != nil || dest == 0 || uint32(dest) == id {
				continue
			}
			target, err := items.Source(uint32(dest))
			if err != nil || !target.Equipment || cashField(target, "sub type") != "0" {
				continue
			}
			egg, err := src.SpecialGM(nil)
			if err != nil {
				continue
			}
			pet, err := target.SpecialGM(nil)
			permanent := func(p *gm.SpecialProduct) bool {
				return p.Template.Kind == 1 && p.ResourcePeriod == 0 && p.Template.ResourceExpiration == nil &&
					!p.Template.DailyDeletion && p.Template.WeeklyDeletionDay == nil
			}
			if err != nil || !permanent(egg) || !permanent(pet) || pet.InitialPetName == "" || pet.Template.InitialSealed {
				continue
			}
			eggJSON, err := json.Marshal(egg.Template)
			if err != nil {
				return nil, err
			}
			petJSON, err := json.Marshal(pet.Template)
			if err != nil {
				return nil, err
			}
			out.PetHatch[id] = storage.PetHatchDefinition{Egg: eggJSON, Pet: petJSON, Name: pet.InitialPetName}
			out.PetNames[uint32(dest)] = storage.PetNameRule{TemplateID: uint32(dest), ResourceHash: target.Hash, DefaultName: pet.InitialPetName}
		}
	}
	if src, err := items.Source(25); err == nil {
		allowed := []string{"name", "explain", "grade", "attach type", "creation rate", "rarity", "weight", "usable job", "/usable job", "creature minimum level",
			"icon", "field image", "stackable type", "cool time", "move wav"}
		if strictFieldSet(src.Text, allowed) && cashField(src, "stackable type") == "`[creature]` 1" && cashField(src, "usable job") == "`[all]`" &&
			cashField(src, "attach type") == "`[trade]`" && cashField(src, "creature minimum level") == "1" && cashField(src, "cool time") == "10000" {
			if t, err := src.Gift(); err == nil && t.Kind == 2 && t.PetConsumable {
				data, err := json.Marshal(t)
				if err != nil {
					return nil, err
				}
				out.PetRenameCard = &storage.PetRenameCard{Source: data, MinimumPetLevel: 1, CooldownMilliseconds: 10000}
			}
		}
	}
	if items.equipment[100330838] != "" {
		r, err := items.Source(100330838)
		if err != nil {
			return nil, err
		}
		if cashField(r, "creature species") != "258" || cashField(r, "equipment type") != "`[creature]` 0" || cashField(r, "sub type") != "0" {
			return nil, fmt.Errorf("Golden Bobi no longer matches the verified species contract")
		}
		index, err := items.source.ReadIndex("creature")
		if err != nil {
			return nil, err
		}
		species, sh, err := sourceTextAndHash(items.source, index[258])
		if err != nil {
			return nil, err
		}
		common, ch, err := sourceTextAndHash(items.source, "creature/common/creaturecommon.cre")
		if err != nil {
			return nil, err
		}
		for f, value := range map[string]string{"start level": "1", "max level": "20", "permission level": "1", "learn overskill level": "20", "evolution quest": "0", "evolution creature id": "0", "evolution level": "0"} {
			v, err := gm.ItemField(species, f)
			if err != nil || v != value {
				return nil, fmt.Errorf("pet growth/evolution differs from the verified Bobi resource: %s", f)
			}
		}
		read := func(f string, bits int) uint64 {
			t, err := gm.ItemField(common, f)
			if err != nil {
				return 0
			}
			n, _ := unsigned(t, bits)
			return n
		}
		spend, recover, limit, auto := read("second per fill point spend", 32), read("second per fill point charge", 32), read("MAX fill point", 8), read("fill point to eat feed", 8)
		if spend < 1 || spend > 2147483 || recover < 1 || recover > 2147483 || limit < 1 || limit > 100 || auto > limit {
			return nil, fmt.Errorf("native creature clock resource is out of bounds")
		}
		out.PetCare = &storage.PetCareRule{TemplateID: 100330838, EquipmentHash: r.Hash, CommonHash: ch, SpendSeconds: uint32(spend), RecoverSeconds: uint32(recover),
			MaximumSatiety: byte(limit), MaximumLevel: 20, SpeciesId: 258, SpeciesHash: sh, StartLevel: 1, PermissionLevel: 1, OverskillLevel: 20, AutomaticFeedThreshold: byte(auto)}
		if items.stackable[24] != "" {
			f, err := items.Source(24)
			if err != nil {
				return nil, err
			}
			feed, err := petFeed(f)
			if err != nil {
				return nil, err
			}
			out.PetFeed = feed
		}
	}
	return out, nil
}

func strictFieldSet(text string, allowed []string) bool {
	fields, err := gm.ItemFields(text)
	if err != nil {
		return false
	}
	seen := map[string]bool{}
	for _, f := range fields {
		if !slices.Contains(allowed, f.Name) || seen[f.Name] && f.Name != "attach type" {
			return false
		}
		if f.Name == "attach type" {
			if _, err := gm.ItemField(text, f.Name); err != nil {
				return false
			}
		}
		seen[f.Name] = true
	}
	return true
}

func petFeed(r ItemSource) (*storage.PetFeedRule, error) {
	allowed := []string{"name", "explain", "grade", "rarity", "usable job", "/usable job", "attach type", "weight", "creation rate", "cool time", "icon", "field image",
		"stackable type", "move wav", "creature minimum level", "int data", "/int data"}
	bad := fmt.Errorf("feed native callback or effect vector is unqualified")
	if !strictFieldSet(r.Text, allowed) || cashField(r, "stackable type") != "`[feed]` 1" || cashField(r, "usable job") != "`[all]`" || cashField(r, "attach type") != "`[trade]`" {
		return nil, bad
	}
	data, err := pvftext.Integers(cashField(r, "int data"))
	min, e := unsigned(cashField(r, "creature minimum level"), 8)
	cool, ce := unsigned(cashField(r, "cool time"), 31)
	if err != nil || e != nil || ce != nil || min == 0 || len(data) != 3 || data[0] < 1 || data[0] > 2147483 || data[1] < -100 || data[1] > 0 || data[2] < 1 || data[2] > 100 {
		return nil, bad
	}
	t, err := r.Gift()
	if err != nil || !t.PetConsumable {
		return nil, bad
	}
	js, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	return &storage.PetFeedRule{Source: js, MinimumPetLevel: byte(min), CooldownMilliseconds: uint32(cool), SatietyRecovery: byte(data[2]), NativeBuffSeconds: uint32(data[0]), NativeChargePercent: data[1]}, nil
}

func sourceTextAndHash(a gm.ItemArchive, path string) (string, string, error) {
	text, err := a.ReadText(path, scriptLimit)
	if err != nil {
		return "", "", err
	}
	raw, err := a.Read(path, scriptLimit)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(raw)
	return text, strings.ToUpper(hex.EncodeToString(digest[:])), nil
}
