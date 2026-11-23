// Command prepare-catalog builds original S9A1 resource tables from the user's PVF.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"s9a1/server/internal/catalogprep"
	"s9a1/server/internal/catalogprep/arcade"
	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/dungeons"
	"s9a1/server/internal/catalogprep/fate"
	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/quest"
	"s9a1/server/internal/catalogprep/towns"
	"s9a1/server/internal/pvf"
	"s9a1/server/internal/questwire"
	"s9a1/server/internal/resources"
)

func main() {
	source := flag.String("pvf", "", "path to Script.pvf")
	profile := flag.String("profile", "", "path to Client100Profile.json")
	output := flag.String("output", "", "destination JSON file (ZIP for gm)")
	section := flag.String("section", "all", "all, gm, characters, progression, skills, openings, opening-combat, towns, quests, quest-items, dungeons, drop-candidates, independent-drops, fate or arcade")
	flag.Parse()
	if *source == "" || *profile == "" || *output == "" || *section == "" || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *source, *profile, *output, *section); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, source, profile, output, section string) error {
	destination, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	destInfo, err := os.Stat(destination)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, path := range []string{source, filepath.Join(filepath.Dir(source), "sk.dat"), profile} {
		input, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(input)
		if err != nil {
			return err
		}
		if strings.EqualFold(input, destination) || destInfo != nil && os.SameFile(info, destInfo) {
			return fmt.Errorf("output overlaps resource input %s", input)
		}
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	if section == "gm" {
		stats, err := gm.Build(ctx, source, profile, destination)
		if err == nil {
			fmt.Printf("GM items %d, ordinary %d, special %d; %s\n", stats.Items, stats.Ordinary, stats.Special, destination)
		}
		return err
	}
	archive, err := pvf.Open(source, profile)
	if err != nil {
		return err
	}
	defer archive.Close()
	var result any
	switch section {
	case "all":
		identity, e := gm.FileMetadata(source, profile, false)
		if e != nil {
			return e
		}
		result, err = buildAll(ctx, archive, identity)
		if err == nil {
			after, e := gm.FileMetadata(source, profile, false)
			if e != nil {
				return e
			}
			if after != identity {
				return errors.New("resource inputs changed during catalog preparation")
			}
		}
	case "progression":
		result, err = buildProgression(archive)
	case "skills":
		result, err = catalogprep.Skills(archive)
	case "openings":
		result, err = dungeons.BuildOpenings(archive)
	case "characters":
		result, err = buildCharacters(archive)
	case "opening-combat":
		result, err = buildOpeningCombat(archive)
	case "dungeons":
		result, err = buildDungeons(ctx, archive)
	case "drop-candidates", "independent-drops":
		var items *commerce.Items
		items, err = commerce.NewItems(archive)
		if err == nil {
			if section == "drop-candidates" {
				result, err = dungeons.BuildDropCandidates(ctx, archive, items)
			} else {
				result, err = dungeons.BuildIndependent(archive, items, nil)
			}
		}
	case "towns":
		var town *towns.Catalog
		town, err = towns.Build(archive)
		if err == nil {
			var rows quest.Result
			rows, err = quest.Build(archive)
			if err == nil {
				town.Ranges = rows.Ranges
				result = town
			}
		}
	case "quest-items":
		result, err = buildQuestItems(archive)
	case "quests":
		result, err = buildQuests(archive)
	case "arcade":
		result, err = arcade.Build(archive)
	case "fate":
		result, err = fate.Build(archive)
	default:
		return fmt.Errorf("unknown preparation section %q", section)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", section, err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = writeJSON(destination, result); err == nil {
		fmt.Printf("Prepared %s in %s\n", section, destination)
	}
	return err
}

func buildQuests(a *pvf.Archive) (*catalogprep.QuestTables, error) {
	rows, err := quest.Build(a)
	if err != nil {
		return nil, err
	}
	items, err := commerce.NewItems(a)
	if err != nil {
		return nil, err
	}
	skills, err := catalogprep.Skills(a)
	if err != nil {
		return nil, err
	}
	return catalogprep.PrepareQuestTables(a, items, &rows, skills)
}

func buildProgression(a *pvf.Archive) (any, error) {
	levels, replacement, err := catalogprep.Progression(a)
	if err != nil {
		return nil, err
	}
	gold, err := catalogprep.GoldCarryLimits(a)
	if err != nil {
		return nil, err
	}
	daily, err := catalogprep.DailyLimits(a)
	if err != nil {
		return nil, err
	}
	growth, err := catalogprep.CharacterGrowthTables(a)
	if err != nil {
		return nil, err
	}
	return struct {
		Levels          []catalogprep.Level
		Replacement     catalogprep.ClassReplacement
		GoldCarryLimits map[byte]int64
		DailyLimits     map[uint32]byte
		Jobs            map[byte]catalogprep.CharacterGrowth
	}{levels, replacement, gold, daily, growth}, nil
}

func buildOpeningCombat(a *pvf.Archive) (*catalogprep.OpeningCombat, error) {
	items, err := commerce.NewItems(a)
	if err != nil {
		return nil, err
	}
	levels, replacement, err := catalogprep.Progression(a)
	if err != nil {
		return nil, err
	}
	jobs, err := catalogprep.CharacterGrowthTables(a)
	if err != nil {
		return nil, err
	}
	skills, err := catalogprep.Skills(a)
	if err != nil {
		return nil, err
	}
	tutorials, err := dungeons.BuildOpenings(a)
	if err != nil {
		return nil, err
	}
	experience, err := dungeons.BuildExperience(a)
	if err != nil {
		return nil, err
	}
	return catalogprep.OpeningCombatTables(items, levels, replacement, jobs, skills, tutorials, experience)
}

func buildCharacters(a *pvf.Archive) ([]resources.CharacterTemplate, error) {
	rows, err := quest.Build(a)
	if err != nil {
		return nil, err
	}
	book, err := questwire.New(rows.Data)
	if err != nil {
		return nil, err
	}
	jobs, err := catalogprep.CharacterGrowthTables(a)
	if err != nil {
		return nil, err
	}
	skills, err := catalogprep.Skills(a)
	if err != nil {
		return nil, err
	}
	levels, _, err := catalogprep.Progression(a)
	if err != nil {
		return nil, err
	}
	tutorials, err := dungeons.BuildOpenings(a)
	if err != nil {
		return nil, err
	}
	return catalogprep.CharacterTemplates(jobs, skills, levels, tutorials, book)
}

func buildQuestItems(a *pvf.Archive) (any, error) {
	rows, err := quest.Build(a)
	if err != nil {
		return nil, err
	}
	items, err := commerce.NewItems(a)
	if err != nil {
		return nil, err
	}
	if err = catalogprep.QuestItems(items, &rows); err != nil {
		return nil, err
	}
	return struct {
		AcceptanceGrants []questwire.AcceptanceGrant
		RewardTemplates  []json.RawMessage
	}{rows.Data.AcceptanceGrants, rows.Data.RewardTemplates}, nil
}

func writeJSON(path string, value any) error {
	out, err := os.CreateTemp(filepath.Dir(path), ".prepare-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	w := bufio.NewWriterSize(out, 256*1024)
	err = json.NewEncoder(w).Encode(value)
	err = errors.Join(err, w.Flush(), out.Close())
	if err != nil {
		return err
	}
	return os.Rename(out.Name(), path)
}
