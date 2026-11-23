package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"s9a1/server/internal/storage"
)

func releaseSeed(args []string) error {
	if len(args) != 3 || args[0] != "--root" || strings.TrimSpace(args[1]) == "" || args[2] != "--create-release-database" {
		return errors.New("use --root <new release directory> --create-release-database")
	}
	root, err := filepath.Abs(args[1])
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "runtime", "data")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	dest := filepath.Join(dir, "release-seed.sql")
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	digest, _, err := fileFingerprint(exe)
	if err != nil {
		return err
	}
	buildID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("sha256:"+strings.ToLower(digest)))
	data := storage.ReleaseSeedSQL(buildID)
	f, err := os.CreateTemp(dir, ".release-seed-*.sql")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err = publishNewFile(f.Name(), dest); err != nil {
		return fmt.Errorf("release seed requires a new destination: %w", err)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Account        string
		Cera           int
		Characters     int
		Policy, SHA256 string
	}{"local", storage.ReleaseSeedCera, 0, storage.ReleaseSeedPolicy, fmt.Sprintf("%X", sha256.Sum256(data))})
}
