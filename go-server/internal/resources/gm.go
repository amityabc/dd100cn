package resources

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"

	"s9a1/server/internal/itemdata"
	"s9a1/server/internal/savejson"
	"s9a1/server/internal/storage"
)

const gmPreparedItemLimit = 32768
const gmPreparedByteLimit = 16 << 20

var ErrGmItemBudget = errors.New("GM acquisition catalog exceeds its prepared item budget")
var ErrGmSpecialBudget = errors.New("GM special catalog exceeds its prepared item budget")

// GmItems reads only the requested batch. The ordinary shop and quest catalogs
// have distinct eligibility rules and may only supply the original GetGm fallback.
type GmItems struct {
	Path, Pvf, Sidecar, Profile string
	ordinary                    map[uint32]json.RawMessage
	prepared                    map[uint32]GmGrant
	ordinaryUsage, specialUsage gmPreparedUsage
}

type gmPreparedUsage struct{ items, bytes int }

type GmGrant struct {
	Template json.RawMessage
	Weight   *int64
	Special  *storage.GiftSpecial
}

// WithGrant publishes one prepared native grant in a new immutable view.
func (g GmItems) WithGrant(id uint32, grant GmGrant) (GmItems, error) {
	if _, exists := g.prepared[id]; exists {
		return g, nil
	}
	if grant.Special == nil && (len(grant.Template) == 0 || string(grant.Template) == "null") {
		return g, nil
	}
	if _, err := g.checkedGrant(id, grant); err != nil {
		return g, err
	}
	size, err := gmPreparedGrantSize(grant)
	if err != nil {
		return g, err
	}
	usage, limitErr := &g.ordinaryUsage, ErrGmItemBudget
	if grant.Special != nil {
		usage, limitErr = &g.specialUsage, ErrGmSpecialBudget
	}
	if usage.items >= gmPreparedItemLimit || size > gmPreparedByteLimit-usage.bytes {
		return g, limitErr
	}
	rows := make(map[uint32]GmGrant, len(g.prepared)+1)
	maps.Copy(rows, g.prepared)
	rows[id] = grant
	g.prepared = rows
	usage.items++
	usage.bytes += size
	return g, nil
}

func (g GmItems) PreparedGrant(id uint32) (GmGrant, bool) {
	row, ok := g.prepared[id]
	return row, ok
}

func (g GmItems) OrdinaryTemplate(id uint32) (json.RawMessage, error) {
	row, err := g.checkedGrant(id, GmGrant{})
	return row.Template, err
}

func gmPreparedGrantSize(grant GmGrant) (int, error) {
	if s := grant.Special; s != nil {
		// The special product's pet name is the only non-ASCII value in the
		// DTO. System.Text.Json escapes it before charging its UTF-8 bytes.
		raw, err := json.Marshal(struct {
			Template       json.RawMessage
			Abilities      map[byte]uint16
			Model          uint32
			ResourcePeriod int64
			AttachType     byte
			InitialPetName savejson.String
		}{s.Template, s.Abilities, s.Model, s.ResourcePeriod, s.AttachType, savejson.String(s.InitialPetName)})
		return len(raw), err
	}
	// ParseGm's typed LocalItemTemplate contains only ASCII resource hashes
	// and numeric fields. Re-encode the complete DTO with its native defaults.
	var template itemdata.ItemTemplate
	if err := json.Unmarshal(grant.Template, &template); err != nil {
		return 0, err
	}
	raw, err := json.Marshal(template)
	return len(raw), err
}

// WithOrdinary binds the immutable ordinary-template map of the new catalog.
func (g GmItems) WithOrdinary(rows map[uint32]json.RawMessage) GmItems {
	g.ordinary = rows
	return g
}

func gmZipJSON(z *zip.ReadCloser, name string, limit int64, result any) error {
	f, err := z.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(b)) > limit {
		return errors.New("PVF GM item batch exceeds its limit")
	}
	return json.Unmarshal(b, result)
}

func (g GmItems) manifest(z *zip.ReadCloser) (int, error) {
	var m struct {
		Schema, Count                           int
		PvfSha256, SidecarSha256, ProfileSha256 string
	}
	if err := gmZipJSON(z, "manifest.json", 4096, &m); err != nil {
		return 0, err
	}
	if m.Schema != 1 || m.Count < 1 || m.Count > 600000 || m.PvfSha256 != g.Pvf || m.SidecarSha256 != g.Sidecar || m.ProfileSha256 != g.Profile {
		return 0, errors.New("PVF GM item catalog does not match the active resources")
	}
	return m.Count, nil
}

func (g GmItems) CarryAndClones() (map[uint32]int64, map[uint32]storage.AvatarClone, error) {
	z, err := zip.OpenReader(g.Path)
	if err != nil {
		return nil, nil, err
	}
	defer z.Close()
	count, err := g.manifest(z)
	if err != nil {
		return nil, nil, err
	}
	var weights map[uint32]int64
	if err = gmZipJSON(z, "weights.json", 32<<20, &weights); err != nil {
		return nil, nil, err
	}
	if weights == nil || len(weights) > count {
		return nil, nil, errors.New("PVF GM item weight index is incomplete or oversized")
	}
	for id, grams := range weights {
		if id == 0 || grams < 0 || grams > math.MaxInt32 {
			return nil, nil, errors.New("PVF GM item weight index has an invalid row")
		}
	}
	var clones map[uint32]storage.AvatarClone
	err = gmZipJSON(z, "avatar-clones.json", 16<<20, &clones)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	for id, clone := range clones {
		if id != clone.ID || clone.Slot > 7 || !validHash(clone.Hash) {
			return nil, nil, errors.New("PVF GM avatar index has an invalid row")
		}
	}
	return weights, clones, nil
}

func (g GmItems) Template(id uint32) (json.RawMessage, error) {
	row, err := g.Grant(id)
	if err == nil && len(row.Template) == 0 {
		return nil, errors.New("PVF item or equipment has a dedicated special owner")
	}
	return row.Template, err
}

func (g GmItems) Grant(id uint32) (GmGrant, error) {
	row, err := g.PreparedOrIndexedGrant(id)
	if err != nil {
		return GmGrant{}, err
	}
	return g.checkedGrant(id, row)
}

// PreparedOrIndexedGrant supplies the acquisition parser's own result.
// Callers can still distinguish an uncounted ordinary fallback template.
func (g GmItems) PreparedOrIndexedGrant(id uint32) (GmGrant, error) {
	if row, ok := g.prepared[id]; ok {
		return row, nil
	}
	var none GmGrant
	unknown := errors.New("PVF item or equipment ID is unknown or unsupported")
	z, err := zip.OpenReader(g.Path)
	if err != nil {
		return none, fmt.Errorf("PVF GM item catalog: %w", err)
	}
	defer z.Close()
	if _, err = g.manifest(z); err != nil {
		return none, err
	}
	var batch map[uint32]struct {
		GmGrant
		Error string
	}
	if err = gmZipJSON(z, fmt.Sprintf("%02x.json", id&255), 32<<20, &batch); errors.Is(err, fs.ErrNotExist) {
		return none, unknown
	} else if err != nil {
		return none, err
	}
	row, ok := batch[id]
	if !ok {
		return none, unknown
	}
	if row.Error != "" {
		return none, errors.New(row.Error)
	}
	return row.GmGrant, nil
}

func (g GmItems) checkedGrant(id uint32, row GmGrant) (GmGrant, error) {
	var none GmGrant
	unknown := errors.New("PVF item or equipment ID is unknown or unsupported")
	if row.Special != nil {
		var t struct {
			TemplateID   uint32
			ResourceHash string
			Kind         byte
		}
		if json.Unmarshal(row.Special.Template, &t) != nil || t.TemplateID != id || !validHash(t.ResourceHash) || t.Kind > 2 {
			return none, unknown
		}
		return GmGrant{Special: row.Special}, nil
	}
	if len(row.Template) == 0 || string(row.Template) == "null" {
		row.Template = g.ordinary[id]
	}
	var t struct {
		TemplateId   uint32
		ResourceHash string
		Kind         int
		StackLimit   uint32
	}
	if len(row.Template) == 0 || json.Unmarshal(row.Template, &t) != nil || t.TemplateId != id || t.Kind < 0 || t.Kind > 2 || t.StackLimit == 0 || !validHash(t.ResourceHash) {
		return none, unknown
	}
	return row, nil
}
