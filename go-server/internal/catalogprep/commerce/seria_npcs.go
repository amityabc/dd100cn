package commerce

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/towns"
)

const seriaRoom = "map/cataclysm/town/elvengard/new_seria_room.map"

func seriaNPCs(a gm.ItemArchive) ([]towns.NPC, error) {
	text, err := a.ReadText(seriaRoom, scriptLimit)
	if err != nil {
		return nil, err
	}
	rows, err := gm.ItemFields(text)
	if err != nil {
		return nil, err
	}
	if err := seriaScope(rows); err != nil {
		return nil, err
	}
	imports := matchingFields(rows, "import script")
	if len(imports) != 1 || !strings.EqualFold(imports[0], "`Common/Gate_Seria.map`") {
		return nil, fmt.Errorf("Seria commerce requires its single confirmed import")
	}
	dependency := "map/common/gate_seria.map"
	imported, err := a.ReadText(dependency, scriptLimit)
	if errors.Is(err, fs.ErrNotExist) {
		dependency = "map/common/(r)gate_seria.map"
		imported, err = a.ReadText(dependency, scriptLimit)
	}
	if err != nil {
		return nil, err
	}
	raw, err := a.Read(dependency, scriptLimit)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if strings.ToUpper(hex.EncodeToString(digest[:])) != "75B9C318A530F039632B7549A867FB37B7ACAAC110EF1C6D03ABA594FD8B0C0F" || len(text)+len(imported) > scriptLimit {
		return nil, fmt.Errorf("Seria commerce dependency hash or combined byte budget is invalid")
	}
	ir, err := gm.ItemFields(imported)
	if err != nil {
		return nil, err
	}
	if err := seriaScope(ir); err != nil {
		return nil, err
	}
	if len(matchingFields(ir, "import script")) > 0 || len(matchingFields(ir, "npc")) != 1 {
		return nil, fmt.Errorf("Seria resource requires one frozen NPC block")
	}
	var placements []towns.NPC
	for _, npc := range towns.NPCs(imported) {
		if npc.Id == 2 {
			if npc.X != 455 || npc.Y != 125 {
				return nil, fmt.Errorf("Seria resource NPC2 differs from its frozen placement")
			}
			placements = append(placements, npc)
		}
	}
	if len(placements) != 1 {
		return nil, fmt.Errorf("Seria resource NPC2 is absent or ambiguous")
	}
	for _, local := range matchingFields(rows, "npc") {
		placements = towns.NPCs("[NPC]\n" + local + "\n[/NPC]\n")
	}
	return placements, nil
}

func matchingFields(fields []gm.Field, name string) []string {
	var out []string
	for _, f := range fields {
		if strings.EqualFold(f.Name, name) {
			out = append(out, f.Value)
		}
	}
	return out
}

func seriaScope(fields []gm.Field) error {
	paired := map[string]bool{}
	for _, f := range fields {
		if strings.HasPrefix(f.Name, "/") {
			paired[strings.ToLower(f.Name[1:])] = true
		}
	}
	var scope []string
	for _, f := range fields {
		name := strings.ToLower(f.Name)
		if strings.HasPrefix(name, "/") {
			if len(scope) == 0 || scope[len(scope)-1] != name[1:] {
				return fmt.Errorf("Seria map has an unmatched block scope")
			}
			scope = scope[:len(scope)-1]
			continue
		}
		if (name == "npc" || name == "import script") && len(scope) != 0 || name == "condition" && !paired[name] {
			return fmt.Errorf("Seria map contains conditional NPCs, nested imports or unknown scope")
		}
		for _, tag := range scope {
			if tag == "npc" {
				return fmt.Errorf("Seria map contains conditional NPCs, nested imports or unknown scope")
			}
		}
		if paired[name] {
			scope = append(scope, name)
		}
	}
	if len(scope) != 0 {
		return fmt.Errorf("Seria map has an incomplete block scope")
	}
	return nil
}
