package resources

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"

	"s9a1/server/internal/storage"
)

// GmItems reads only the requested batch. The ordinary shop and quest catalogs
// have distinct eligibility rules and may only supply the original GetGm fallback.
type GmItems struct {
	Path, Pvf, Sidecar, Profile string
	ordinary                    map[uint32]json.RawMessage
}

type GmGrant struct {
	Template json.RawMessage
	Weight   *int64
	Special  *storage.GiftSpecial
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
	return row.GmGrant, nil
}
