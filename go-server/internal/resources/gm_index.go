package resources

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"s9a1/server/internal/filelock"
)

type GmCatalogItem struct {
	Id                      uint32
	Kind, Group, Path, Name string
}

func (r *GmCatalogItem) UnmarshalJSON(data []byte) error {
	var fields struct {
		Id                      uint32
		Kind, Group, Path, Name *string
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields.Kind == nil || fields.Group == nil || fields.Path == nil || fields.Name == nil {
		return errors.New("GM item index row has an incomplete display projection")
	}
	*r = GmCatalogItem{fields.Id, *fields.Kind, *fields.Group, *fields.Path, *fields.Name}
	return nil
}

type gmIndexSnapshot struct {
	Version                 int
	Source, Profile, Parser string
	Items                   []GmCatalogItem
}
type GmIndex struct {
	Rows     []GmCatalogItem
	Hash     string
	CacheHit bool
}

func (d *gmIndexSnapshot) valid() bool {
	if d.Version != 1 || len(d.Items) == 0 || len(d.Items) > 600000 {
		return false
	}
	chars := 0
	for _, row := range d.Items {
		names, paths := utf16Units(row.Name), utf16Units(row.Path)
		if names > 4096 || paths > 2048 || row.Kind != "equipment" && row.Kind != "stackable" {
			return false
		}
		chars += names + paths
	}
	return chars <= 64<<20
}

func utf16Units(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// LauncherUI reads a separate display catalog. Eligibility to grant an item is
// checked by the item owner when the operator submits an intent.
func LoadGMIndex(pack, pvf, profile, cache string) (GmIndex, error) {
	var out GmIndex
	z, err := zip.OpenReader(pack)
	if err != nil {
		return out, err
	}
	defer z.Close()
	header, err := z.Open("manifest.json")
	if err != nil {
		return out, err
	}
	var m struct {
		Schema                                          int
		PvfSha256, ProfileSha256, SidecarSha256, Parser string
		Unprofiled                                      bool
	}
	err = json.NewDecoder(io.LimitReader(header, 4096)).Decode(&m)
	header.Close()
	if err != nil {
		return out, err
	}
	if m.Schema != 1 || !validHash(m.PvfSha256) || !validHash(m.ProfileSha256) || len(m.Parser) != 36 {
		return out, errors.New("rebuild the GM catalog with export-characters.ps1 -GmOnly")
	}
	if err = matchHash(pvf, m.PvfSha256); err != nil {
		return out, err
	}
	if err = matchHash(filepath.Join(filepath.Dir(pvf), "sk.dat"), m.SidecarSha256); err != nil {
		return out, err
	}
	effectiveHash := ""
	if profile != "" {
		if err = matchHash(profile, m.ProfileSha256); err != nil {
			return out, err
		}
		effectiveHash = m.ProfileSha256
	} else if !m.Unprofiled {
		return out, errors.New("封装PVF需要显式外部 Resources:ClientProfile")
	}
	if err = os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		return out, err
	}
	lock, err := filelock.Open(cache + ".lock")
	if err != nil {
		return out, err
	}
	defer lock.Close()
	read := func(r io.Reader) (gmIndexSnapshot, error) {
		var d gmIndexSnapshot
		b, err := io.ReadAll(io.LimitReader(r, (256<<20)+1))
		if err != nil {
			return d, err
		}
		if len(b) > 256<<20 {
			return d, errors.New("GM item index file exceeds its resource capacity")
		}
		err = json.Unmarshal(b, &d)
		return d, err
	}
	if f, err := os.Open(cache); err == nil {
		d, err := read(f)
		f.Close()
		if err == nil && d.Source == m.PvfSha256 && d.Profile == effectiveHash && strings.EqualFold(d.Parser, m.Parser) && d.valid() {
			return GmIndex{d.Items, d.Source, true}, nil
		}
	}
	f, err := z.Open("item-index.json")
	if err != nil {
		return out, err
	}
	defer f.Close()
	d, err := read(f)
	if err != nil {
		return out, err
	}
	if !d.valid() || d.Source != m.PvfSha256 || d.Profile != m.ProfileSha256 || !strings.EqualFold(d.Parser, m.Parser) {
		return out, errors.New("GM item index differs from its prepared inputs")
	}
	d.Profile = effectiveHash
	tmp, err := os.CreateTemp(filepath.Dir(cache), ".item-index-*.tmp")
	if err != nil {
		return out, err
	}
	defer os.Remove(tmp.Name())
	err = json.NewEncoder(tmp).Encode(d)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), cache)
	}
	return GmIndex{d.Items, d.Source, false}, err
}
