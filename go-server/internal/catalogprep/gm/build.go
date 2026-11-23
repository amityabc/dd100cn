package gm

import (
	"archive/zip"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"

	"s9a1/server/internal/pvf"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

const (
	// DisplayParserVersion matches the original cache parser identity.
	DisplayParserVersion = "442f1c44-d683-4dcf-a601-159788063536"
	memberBudget         = 1 << 20
	maxItems             = 600000
)

type Archive interface {
	ItemArchive
	LocalizedText(value string) (string, error)
}

type ItemArchive interface {
	Read(path string, max int) ([]byte, error)
	ReadText(path string, max int) (string, error)
	ReadIndex(category string, maximum ...int) (map[int64]string, error)
	ResolveString(id int32) (string, error)
}

type Metadata struct {
	PvfSha256, SidecarSha256, ProfileSha256 string
	Unprofiled                              bool
}

func FileMetadata(source, profile string, unprofiled bool) (Metadata, error) {
	m := Metadata{Unprofiled: unprofiled}
	for _, row := range []struct {
		path string
		hash *string
	}{{source, &m.PvfSha256}, {filepath.Join(filepath.Dir(source), "sk.dat"), &m.SidecarSha256}, {profile, &m.ProfileSha256}} {
		r, err := os.Open(row.path)
		if err != nil {
			return Metadata{}, err
		}
		h := sha256.New()
		_, err = io.Copy(h, r)
		closeErr := r.Close()
		if err != nil || closeErr != nil {
			return Metadata{}, errors.Join(err, closeErr)
		}
		*row.hash = strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
	}
	return m, nil
}

type Stats struct {
	Items, Definitions, Ordinary, Special, Unavailable, Named, Weighed, Clones int
}

func Build(ctx context.Context, source, profile, output string) (Stats, error) {
	var unprofiled bool
	if plain, err := pvf.Open(source, ""); err == nil {
		index, err := plain.ReadIndex("equipment")
		unprofiled = err == nil && len(index) > 0
		plain.Close()
	}
	a, err := pvf.Open(source, profile)
	if err != nil {
		return Stats{}, err
	}
	defer a.Close()
	m, err := FileMetadata(source, profile, unprofiled)
	if err != nil {
		return Stats{}, err
	}
	return BuildArchive(ctx, a, m, output)
}

// BuildDisplay reads the GM search index without preparing grant definitions.
func BuildDisplay(ctx context.Context, source, profile string) ([]resources.GmCatalogItem, error) {
	a, err := pvf.Open(source, profile)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	refs, _, err := readEntries(a)
	if err != nil {
		return nil, err
	}
	rows := make([]resources.GmCatalogItem, len(refs))
	var length int
	for _, where := range physicalOrder(a, refs) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		display, _, _ := displayRow(a, refs[where.index])
		rows[where.index] = display
		length += textUnits(display.Name) + textUnits(display.Path)
		if length > 64<<20 {
			return nil, errors.New("GM item index text exceeds its resource capacity")
		}
	}
	return rows, nil
}

type entry struct {
	id        uint32
	equipment bool
	path      string
}

type itemRow struct {
	Template *ItemTemplate
	Error    *string
	Weight   *int64
	Special  *SpecialProduct
}

func BuildArchive(ctx context.Context, a Archive, m Metadata, output string) (Stats, error) {
	var stats Stats
	for _, h := range []string{m.PvfSha256, m.SidecarSha256, m.ProfileSha256} {
		if _, err := hex.DecodeString(h); err != nil || len(h) != 64 {
			return stats, errors.New("GM build requires SHA-256 hashes for PVF, sk.dat and profile")
		}
	}
	refs, definitions, err := readEntries(a)
	if err != nil {
		return stats, err
	}
	var shards [256]int
	for id := range definitions {
		shards[id&255]++
	}
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return stats, err
	}
	file, err := os.CreateTemp(filepath.Dir(output), ".gm-catalog-*.tmp")
	if err != nil {
		return stats, err
	}
	defer os.Remove(file.Name())
	z := zip.NewWriter(file)
	z.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) { return flate.NewWriter(w, flate.BestSpeed) })
	stats.Items, stats.Definitions = len(refs), len(definitions)
	values := &buildValues{
		archive: a, definitions: definitions, refs: refs, items: make([]resources.GmCatalogItem, len(refs)),
		weights: make(map[uint32]int64), clones: make(map[uint32]AvatarClone), stats: stats,
	}
	buildErr := values.write(ctx, z, m, shards)
	if err := errors.Join(buildErr, z.Close(), file.Close()); err != nil {
		return values.stats, err
	}
	return values.stats, os.Rename(file.Name(), output)
}

func readEntries(a Archive) ([]entry, map[uint32]entry, error) {
	var refs []entry
	definitions := make(map[uint32]entry)
	for _, kind := range []string{"equipment", "stackable"} {
		index, err := a.ReadIndex(kind)
		if err != nil {
			return nil, nil, fmt.Errorf("GM %s index: %w", kind, err)
		}
		for id, path := range index {
			if id < 0 || id > 1<<32-1 || textUnits(path) > 2048 {
				return nil, nil, fmt.Errorf("GM %s index entry %d is invalid", kind, id)
			}
			r := entry{uint32(id), kind == "equipment", path}
			refs = append(refs, r)
			if _, exists := definitions[r.id]; !exists {
				definitions[r.id] = r
			}
		}
	}
	if len(refs) < 1 || len(refs) > maxItems || len(definitions) < 1 {
		return nil, nil, errors.New("GM item index exceeds its resource capacity")
	}
	slices.SortFunc(refs, func(a, b entry) int {
		if a.id == b.id {
			if a.equipment == b.equipment {
				return 0
			}
			if a.equipment {
				return -1
			}
			return 1
		}
		if a.id < b.id {
			return -1
		}
		return 1
	})
	return refs, definitions, nil
}

type location struct{ index, chunk, offset int }

func physicalOrder(a Archive, refs []entry) []location {
	sequence := make([]location, len(refs))
	physical, ordered := a.(interface {
		PhysicalIndex(path string) (chunk, offset int, ok bool)
	})
	for i, ref := range refs {
		sequence[i].index = i
		if ordered {
			if chunk, offset, ok := physical.PhysicalIndex(ref.path); ok {
				sequence[i].chunk, sequence[i].offset = chunk, offset
			}
		}
	}
	if ordered {
		slices.SortFunc(sequence, func(a, b location) int {
			if a.chunk != b.chunk {
				return a.chunk - b.chunk
			}
			return a.offset - b.offset
		})
	}
	return sequence
}

func displayRow(a Archive, ref entry) (resources.GmCatalogItem, script, error) {
	text, textErr := a.ReadText(ref.path, memberBudget)
	display := resources.GmCatalogItem{Id: ref.id, Kind: "stackable", Group: "道具 / 材料", Path: ref.path}
	if ref.equipment {
		display.Kind, display.Group = "equipment", "装备"
	}
	var fields script
	if textErr == nil {
		fields, textErr = parseScript(text)
	}
	if textErr == nil {
		if name, err := fields.section("name"); err == nil {
			name = strings.TrimSpace(name)
			if len(name) >= 2 && name[0] == '`' && name[len(name)-1] == '`' {
				name = name[1 : len(name)-1]
			}
			if name, err = a.LocalizedText(name); err == nil {
				name = strings.TrimSpace(name)
				if !(strings.HasPrefix(name, "<") && strings.Contains(name, "::")) && textUnits(name) <= 4096 {
					display.Name = name
				}
			}
		}
	}
	return display, fields, textErr
}

type buildValues struct {
	archive     Archive
	definitions map[uint32]entry
	refs        []entry
	items       []resources.GmCatalogItem
	weights     map[uint32]int64
	clones      map[uint32]AvatarClone
	cases       map[uint32]map[byte]uint16
	caseError   error
	casesLoaded bool
	stats       Stats
	textLength  int
}

func (b *buildValues) write(ctx context.Context, z *zip.Writer, m Metadata, shards [256]int) error {
	add := func(name string, value any) error {
		w, err := z.Create(name)
		if err != nil {
			return err
		}
		return json.NewEncoder(w).Encode(value)
	}
	if err := add("manifest.json", struct {
		Schema, Count int
		Parser        string
		Metadata
	}{1, b.stats.Definitions, DisplayParserVersion, m}); err != nil {
		return err
	}
	sequence := physicalOrder(b.archive, b.refs)
	batches := [256]map[uint32]itemRow{}
	for n, count := range shards {
		if count > 0 {
			batches[n] = make(map[uint32]itemRow, count)
		}
	}
	for _, where := range sequence {
		if err := ctx.Err(); err != nil {
			return err
		}
		index := where.index
		ref := b.refs[index]
		display, fields, textErr := displayRow(b.archive, ref)
		b.items[index] = display
		b.textLength += textUnits(display.Name) + textUnits(display.Path)
		if b.textLength > 64<<20 {
			return errors.New("GM item index text exceeds its resource capacity")
		}
		if display.Name != "" {
			b.stats.Named++
		}
		if b.definitions[ref.id] != ref {
			continue
		}
		row := b.resourceRow(ref, fields, textErr)
		batches[ref.id&255][ref.id] = row
		switch {
		case row.Error != nil:
			b.stats.Unavailable++
		case row.Special != nil:
			b.stats.Special++
		case row.Template != nil:
			b.stats.Ordinary++
		}
	}
	for n, rows := range batches {
		if len(rows) > 0 {
			if err := add(fmt.Sprintf("%02x.json", n), rows); err != nil {
				return err
			}
		}
	}
	if err := add("item-index.json", struct {
		Version                 int
		Source, Profile, Parser string
		Items                   []resources.GmCatalogItem
	}{1, m.PvfSha256, m.ProfileSha256, DisplayParserVersion, b.items}); err != nil {
		return err
	}
	b.stats.Weighed, b.stats.Clones = len(b.weights), len(b.clones)
	if err := add("weights.json", b.weights); err != nil {
		return err
	}
	return add("avatar-clones.json", b.clones)
}

func (b *buildValues) resourceRow(ref entry, fields script, readErr error) itemRow {
	reject := func(err error) itemRow { reason := err.Error(); return itemRow{Error: &reason} }
	if readErr != nil {
		return reject(readErr)
	}
	raw, err := b.archive.Read(ref.path, memberBudget)
	if err != nil {
		return reject(err)
	}
	digest := sha256.Sum256(raw)
	hash := strings.ToUpper(hex.EncodeToString(digest[:]))
	return b.grantRow(b.archive, ref, fields, hash, raw)
}

func (b *buildValues) grantRow(a ItemArchive, ref entry, fields script, hash string, raw []byte) itemRow {
	reject := func(err error) itemRow { reason := err.Error(); return itemRow{Error: &reason} }
	normalized := fields.normalized()
	item, special, err := parseItem(ref.id, ref.equipment, normalized, hash, raw, a.ResolveString)
	if err != nil {
		return reject(err)
	}
	if special {
		if len(fields.values("ability case index")) != 0 && !b.casesLoaded {
			b.casesLoaded = true
			pvf, err := a.ReadText("skill/abilitydatas.dat", memberBudget)
			if err == nil {
				b.cases, err = parseCases(pvf)
			}
			b.caseError = err
		}
		if b.caseError != nil && len(fields.values("ability case index")) != 0 {
			return reject(b.caseError)
		}
		product, err := parseSpecial(ref.id, normalized, hash, b.cases)
		if err != nil {
			return reject(err)
		}
		if product.Template.Kind == 0 && product.Template.WornSlot <= 7 {
			if clone, ok := cloneTemplate(ref.id, fields, hash); b.clones != nil && ok {
				b.clones[ref.id] = clone
			}
		}
		return itemRow{Special: product}
	}
	if !item.FateCollection && !item.AccountCommonInventory && !item.PetConsumable &&
		(item.Kind != 2 || item.FirstBagSlot == 65 || item.FirstBagSlot == 121) &&
		len(fields.values("attach type")) > 1 {
		return reject(errors.New("Ambiguous commerce field: attach type"))
	}
	row := itemRow{Template: item}
	if weight, err := parseWeight(fields); err == nil {
		row.Weight = &weight
		if b.weights != nil {
			b.weights[ref.id] = weight
		}
	}
	return row
}

// PrepareGrant uses the same acquisition parser as the prepared GM ZIP.
func PrepareGrant(a ItemArchive, item ItemSource) (resources.GmGrant, error) {
	fields, err := parseScript(item.Text)
	if err != nil {
		return resources.GmGrant{}, err
	}
	var builder buildValues
	row := builder.grantRow(a, entry{item.Id, item.Equipment, item.Path}, fields, item.Hash, item.Raw)
	if row.Error != nil {
		return resources.GmGrant{}, errors.New(*row.Error)
	}
	grant := resources.GmGrant{Weight: row.Weight}
	if special := row.Special; special != nil {
		template, err := json.Marshal(special.Template)
		if err != nil {
			return grant, err
		}
		grant.Special = &storage.GiftSpecial{Template: template, Abilities: special.Abilities,
			Model: special.Model, ResourcePeriod: special.ResourcePeriod, AttachType: special.AttachType, InitialPetName: special.InitialPetName}
	} else if row.Template != nil {
		grant.Template, err = json.Marshal(row.Template)
	}
	return grant, err
}

func textUnits(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
