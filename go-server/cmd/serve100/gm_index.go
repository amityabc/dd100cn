package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/config"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

type indexItem struct {
	Id    uint32 `json:"id"`
	Kind  string `json:"kind"`
	Group string `json:"group"`
	Path  string `json:"path"`
	Name  string `json:"name"`
}
type indexCounts struct {
	FileCount int   `json:"fileCount"`
	Bytes     int64 `json:"bytes"`
}

func gmIndex(args []string) error {
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		key := args[i]
		if key == "--gm-index" {
			continue
		}
		if i+1 >= len(args) || key != "--client" && key != "--output" && key != "--server-root" {
			return errors.New("GM index arguments require --client and --output, with optional --server-root")
		}
		i++
		if _, ok := values[key]; !ok {
			values[key] = args[i]
		}
	}
	if strings.TrimSpace(values["--client"]) == "" || strings.TrimSpace(values["--output"]) == "" {
		return errors.New("Missing --client or --output")
	}
	client, err := filepath.Abs(values["--client"])
	if err != nil {
		return err
	}
	output, err := filepath.Abs(values["--output"])
	if err != nil {
		return err
	}
	pvf := filepath.Join(client, "Script.pvf")
	source, err := os.Stat(pvf)
	if err != nil {
		return err
	}
	if !source.Mode().IsRegular() {
		return errors.New("Script.pvf must be a regular file")
	}
	root := values["--server-root"]
	if strings.TrimSpace(root) == "" {
		root = ""
	}
	var cfg config.Config
	var legacyDatabase string
	if root != "" {
		cfg, err = config.Load(root)
		if err != nil {
			return err
		}
		legacyDatabase, err = gmIndexDatabasePath(cfg)
		if err != nil {
			return err
		}
	} else {
		cfg.Root, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	paths := []string{filepath.Join(client, "Client100Profile.json")}
	if root != "" {
		paths = append(paths, cfg.Path("Server/Data/Pvf/Client100Profile.json"), cfg.Path("Data/Pvf/Client100Profile.json"))
	}
	profile := ""
	for _, p := range paths {
		if info, e := os.Stat(p); e == nil && info.Mode().IsRegular() {
			profile = p
			break
		}
	}
	imageRoot := filepath.Join(client, "ImagePacks2")
	imageDir, err := os.Stat(imageRoot)
	if err != nil {
		return err
	}
	if !imageDir.IsDir() {
		return errors.New("ImagePacks2 must be a directory")
	}
	imageRoot, err = filepath.EvalSymlinks(imageRoot)
	if err != nil {
		return err
	}
	var images indexCounts
	err = filepath.WalkDir(imageRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		ext := filepath.Ext(path)
		if !strings.EqualFold(ext, ".npk") && !strings.EqualFold(ext, ".im2") {
			return nil
		}
		st, err := entry.Info()
		if err == nil {
			images.FileCount++
			images.Bytes += st.Size()
		}
		return err
	})
	if err != nil {
		return err
	}
	pack := gmIndexPackPath(runtimeCatalogPath(cfg, true))
	cache := filepath.Join(filepath.Dir(output), "item-index.json")
	index, err := resources.LoadGMIndex(pack, pvf, profile, cache)
	if err != nil {
		var nativeErr error
		index, nativeErr = readNativeGMDisplay(pvf, profile, cache)
		if nativeErr != nil {
			return errors.Join(err, nativeErr)
		}
	}
	database := struct {
		indexCounts
		Characters []storage.GmCharacter `json:"characters"`
	}{Characters: []storage.GmCharacter{}}
	if root != "" {
		if f, err := os.Stat(legacyDatabase); err == nil && f.Mode().IsRegular() {
			database.FileCount = 1
			database.Bytes = f.Size()
		}
		if dsn, err := configuredDSN(cfg.Root); err == nil && dsn != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			db, e := storage.Open(ctx, dsn)
			if e == nil {
				if rows, e := db.GmCharacters(ctx); e == nil {
					database.Characters = rows
				}
				db.Close()
			}
			cancel()
		}
	}
	var equipment, unnamed int
	for _, r := range index.Rows {
		if r.Kind == "equipment" {
			equipment++
		}
		if r.Name == "" {
			unnamed++
		}
	}
	const version = "s7-gm-catalog-v2"
	report := map[string]any{"version": version, "generatedUtc": time.Now().UTC(), "clientPath": client,
		"pvf":         map[string]any{"path": filepath.Base(pvf), "size": source.Size(), "sha256": index.Hash, "equipmentCount": equipment, "stackableCount": len(index.Rows) - equipment, "cacheHit": index.CacheHit, "unnamedCount": unnamed},
		"imagePacks2": images, "database": database, "ready": true}
	stage, err := os.CreateTemp(filepath.Dir(output), ".gm-catalog-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	err = writeGMIndexReport(stage, report, index.Rows)
	if closeErr := stage.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(stage.Name(), output)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"version": version, "clientPath": client, "items": len(index.Rows), "ready": true})
}

func readNativeGMDisplay(source, profile, cache string) (resources.GmIndex, error) {
	sourceHash, _, err := fileFingerprint(source)
	if err != nil {
		return resources.GmIndex{}, err
	}
	var profileHash string
	if profile != "" {
		profileHash, _, err = fileFingerprint(profile)
		if err != nil {
			return resources.GmIndex{}, err
		}
	}
	return resources.LoadGMDisplayCache(cache, sourceHash, profileHash, gm.DisplayParserVersion, func() ([]resources.GmCatalogItem, error) {
		return gm.BuildDisplay(context.Background(), source, profile)
	})
}

func gmIndexDatabasePath(c config.Config) (string, error) {
	if strings.TrimSpace(c.Storage.Database) == "" {
		return "", errors.New("Missing Storage:Database")
	}
	path := c.Path(c.Storage.Database)
	rel, err := filepath.Rel(c.Path("runtime"), path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("Storage:Database must be under Next/runtime")
	}
	return path, nil
}

func gmIndexPackPath(catalog string) string {
	return strings.TrimSuffix(catalog, filepath.Ext(catalog)) + ".gm.zip"
}

func runtimeCatalogPath(cfg config.Config, gmIndex bool) string {
	path := cfg.CatalogPath()
	regular := func(p string) bool { st, e := os.Stat(p); return e == nil && st.Mode().IsRegular() }
	available := regular
	if gmIndex {
		available = func(p string) bool { return regular(gmIndexPackPath(p)) }
	}
	if os.Getenv("S9A1_CREATION_CATALOG") != "" || available(path) {
		return path
	}
	launchRoot := cfg.Root
	if strings.EqualFold(filepath.Base(cfg.Root), "Server") {
		launchRoot = filepath.Dir(cfg.Root)
	} else if !gmIndex {
		return path
	}
	if regular(filepath.Join(launchRoot, "release-manifest.json")) {
		return path
	}
	projects := []string{filepath.Join(launchRoot, "go-server")}
	if gmIndex && strings.EqualFold(filepath.Base(launchRoot), "go-server") {
		projects = append(projects, launchRoot)
	}
	if strings.EqualFold(filepath.Base(launchRoot), "s9a1") {
		projects = append(projects, filepath.Join(filepath.Dir(launchRoot), "go-server"))
	}
	for _, project := range projects {
		candidate := filepath.Join(project, ".work", "catalog", "characters.json")
		if regular(filepath.Join(project, "go.mod")) && available(candidate) {
			return candidate
		}
	}
	return path
}

func writeGMIndexReport(dst io.Writer, fields map[string]any, rows []resources.GmCatalogItem) error {
	header, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	out := bufio.NewWriter(dst)
	if _, err := out.Write(header[:len(header)-1]); err != nil {
		return err
	}
	if _, err := out.WriteString(",\"items\":[\n"); err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	for i, row := range rows {
		if i > 0 {
			if err := out.WriteByte(','); err != nil {
				return err
			}
		}
		if err := enc.Encode(indexItem{row.Id, row.Kind, row.Group, row.Path, row.Name}); err != nil {
			return err
		}
	}
	if _, err := out.WriteString("]}\n"); err != nil {
		return err
	}
	return out.Flush()
}
