package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"s9a1/server/internal/config"
	"slices"
	"strings"
)

const matchingLauncherSHA256 = "82726B29688FE82D2476EF68854F8984040D7EF34B87BC53849FBA04BEFC9D31"

func fileFingerprint(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, errors.Join(err, errors.New("input must be a regular file"))
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(info, after) || n != info.Size() || after.Size() != n || after.ModTime() != info.ModTime() {
		return "", 0, errors.Join(err, errors.New("input changed during fingerprint"))
	}
	return fmt.Sprintf("%X", h.Sum(nil)), n, nil
}

func completeHex(s string, digits int) bool {
	return len(s) == digits && !strings.ContainsFunc(s, func(c rune) bool {
		return !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F')
	})
}

type launcherIdentity struct {
	Hash, BuildID, Digest string
	Source                bool
}

var sourceLauncherFiles = []string{
	"Launcher/DNF100Launcher.exe",
	"ResourceCompatibility/DNF100ResourceLauncher.exe",
	"ResourceCompatibility/DNF100ResourceLauncher.dll",
	"ResourceCompatibility/DNF100ResourceLauncher.deps.json",
	"ResourceCompatibility/DNF100ResourceLauncher.runtimeconfig.json",
}

func validateLauncher(client, launcher string) (launcherIdentity, error) {
	id := launcherIdentity{Hash: matchingLauncherSHA256}
	if launcher == "" {
		return id, errors.New("Gameplay:Launcher is required")
	}
	pointer := filepath.Join(client, "LocalTools", "source-tools.local.json")
	_, err := os.Lstat(pointer)
	if client == "" || errors.Is(err, os.ErrNotExist) {
		hash, n, e := fileFingerprint(launcher)
		if e != nil {
			return id, e
		}
		if n == 0 || hash != matchingLauncherSHA256 {
			return id, errors.New("launcher does not match the legacy pin and no source build is selected")
		}
		return id, nil
	}
	if err != nil {
		return id, err
	}
	id = launcherIdentity{Source: true}
	var sel struct {
		Schema  int
		BuildId string
	}
	raw, err := readSourceJSON(pointer, &sel)
	if err != nil {
		return id, err
	}
	if sel.Schema != 1 || !completeHex(sel.BuildId, 32) || strings.ToLower(sel.BuildId) != sel.BuildId {
		return id, errors.New("invalid client source build selection")
	}
	pkg := filepath.Join(filepath.Dir(pointer), "SourceBuilds", sel.BuildId)
	if !samePath(launcher, filepath.Join(pkg, filepath.FromSlash(sourceLauncherFiles[0]))) {
		return id, errors.New("Gameplay:Launcher must use the selected source build's paired launcher")
	}
	manifest := filepath.Join(pkg, "manifest.json")
	var m struct {
		Schema                                              int
		Kind, BuildId, SourceDigest, DelegateLauncherSHA256 string
		Files                                               []struct {
			Path, SHA256 string
			Bytes        int64
		}
	}
	manifestRaw, err := readSourceJSON(manifest, &m)
	if err != nil {
		return id, err
	}
	verifiedHash := func(h string) bool { return completeHex(h, 64) && strings.ToUpper(h) == h }
	if m.Schema != 1 || m.Kind != "DNF100ClientTools" || m.BuildId != sel.BuildId || !verifiedHash(m.SourceDigest) ||
		!verifiedHash(m.DelegateLauncherSHA256) || len(m.Files) < 5 || len(m.Files) > 64 {
		return id, errors.New("invalid client source build manifest")
	}
	names := map[string]bool{}
	launcherHash := ""
	for _, row := range m.Files {
		name := strings.ToLower(row.Path)
		if !safeSourceFile(row.Path) || names[name] || !verifiedHash(row.SHA256) || row.Bytes <= 0 {
			return id, errors.New("invalid or duplicate client source build file entry")
		}
		names[name] = true
		path := filepath.Join(pkg, filepath.FromSlash(row.Path))
		if err = rejectSourceLinks(path); err != nil {
			return id, err
		}
		hash, n, e := fileFingerprint(path)
		if e != nil {
			return id, e
		}
		if n != row.Bytes || hash != row.SHA256 {
			return id, fmt.Errorf("client source build file hash or length mismatch: %s", row.Path)
		}
		if row.Path == sourceLauncherFiles[0] {
			launcherHash = hash
		}
	}
	for _, name := range sourceLauncherFiles {
		if !names[strings.ToLower(name)] {
			return id, errors.New("client source build is incomplete")
		}
	}
	if launcherHash == "" || launcherHash != m.DelegateLauncherSHA256 {
		return id, errors.New("client source build delegate pin differs")
	}
	count := 0
	for _, dir := range []string{"Launcher", "ResourceCompatibility"} {
		path := filepath.Join(pkg, dir)
		if err = rejectSourceLinks(path); err != nil {
			return id, err
		}
		files, e := os.ReadDir(path)
		if e != nil {
			return id, e
		}
		for _, f := range files {
			if f.IsDir() || !names[strings.ToLower(dir+"/"+f.Name())] {
				return id, errors.New("client source build contains an unlisted entry")
			}
			if err = rejectSourceLinks(filepath.Join(path, f.Name())); err != nil {
				return id, err
			}
			count++
		}
	}
	if count != len(names) {
		return id, errors.New("client source build manifest paths differ from the package")
	}
	newSel, err := readSourceJSON(pointer, &sel)
	if err != nil {
		return id, err
	}
	newManifest, err := readSourceJSON(manifest, &m)
	if err != nil {
		return id, err
	}
	if !bytes.Equal(raw, newSel) || !bytes.Equal(manifestRaw, newManifest) {
		return id, errors.New("client source build selection or manifest changed")
	}
	return launcherIdentity{launcherHash, sel.BuildId, m.SourceDigest, true}, nil
}

func safeSourceFile(s string) bool {
	dir, name, ok := strings.Cut(s, "/")
	return ok && (dir == "Launcher" || dir == "ResourceCompatibility") && name != "" && !strings.HasSuffix(name, ".") &&
		!strings.ContainsFunc(name, func(c rune) bool {
			return !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_' || c == '.')
		})
}

func readSourceJSON(path string, target any) ([]byte, error) {
	if err := rejectSourceLinks(path); err != nil {
		return nil, err
	}
	raw, err := boundedRead(path, 65536)
	if err != nil {
		return nil, err
	}
	if err = uniqueJSON(raw); err != nil {
		return nil, err
	}
	return raw, json.Unmarshal(raw, target)
}

func boundedRead(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || int64(len(b)) > limit {
		return nil, errors.New("JSON file is empty or exceeds its read budget")
	}
	return b, nil
}

func uniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) error
	value = func(depth int) error {
		if depth > 8 {
			return errors.New("source build JSON exceeds nesting limit")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'):
			keys := map[string]bool{}
			for d.More() {
				t, err = d.Token()
				if err != nil {
					return err
				}
				key, ok := t.(string)
				if !ok || keys[key] {
					return errors.New("invalid or duplicate JSON property")
				}
				keys[key] = true
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
		case json.Delim('['):
			for d.More() {
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
		}
		return err
	}
	return value(1)
}

func rejectSourceLinks(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		f, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if isReparse(f) {
			return errors.New("reparse points are not allowed in source build paths")
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}

func settingPath(c config.Config, s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return c.Path(s)
}

func prepareClient(c config.Config) error {
	if err := fixedLaunchConfig(c); err != nil {
		return err
	}
	client, launcher := settingPath(c, c.Gameplay.ClientDirectory), settingPath(c, c.Gameplay.Launcher)
	if client == "" {
		return errors.New("set Gameplay:ClientDirectory to the matching client")
	}
	if f, err := os.Stat(filepath.Join(client, "DNF.exe")); err != nil || !f.Mode().IsRegular() {
		return errors.New("set Gameplay:ClientDirectory to the matching client")
	}
	if _, err := validateLauncher(client, launcher); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	raw, err := boundedRead(filepath.Join(c.Root, "runtime", "next-ready.json"), 65536)
	if err != nil {
		return err
	}
	var ready legacyReady
	if json.Unmarshal(raw, &ready) != nil || !ready.matches(c, exe) {
		return errors.New("start the current Go server first; process, executable, ports or native profile differ from its readiness receipt")
	}
	project := filepath.Join(c.Root, "runtime", "client-launch")
	dir := filepath.Join(project, "runtime", "config")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	bridge := map[string]any{
		"server": map[string]any{"channelListen": "127.0.0.1:7001", "advertiseIp": "127.0.0.1"},
		"protocol": map[string]any{"showBootstrapChannel": true, "gameOuterToken": "de509f65e9ccaae621cb7278fc2b8e6c",
			"gameUpperHeader": "server16", "gameUpperBodyCodec": "plaintext", "gameUpperClientBodyCodec": "plaintext"},
		"client": map[string]any{"directory": client, "initialGamePort": 0, "localGmCommands": slices.Contains(c.Gameplay.GmAccounts, "local"),
			"nativePartyNotices": nativeParty(c), "nativeTownProfile140": c.Gameplay.NativeTownProfile140},
	}
	if err = writeJSONAtomic(filepath.Join(dir, "instance.json"), bridge); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Outcome, Launcher, Project, Client string
		ProcessId                          int
		ClientUI                           string
	}{
		"ClientLaunchPrepared", launcher, project, client, ready.ProcessId, "NOT_OBSERVED"})
}

func fixedLaunchConfig(c config.Config) error {
	_, err := c.GamePorts()
	if err != nil {
		return err
	}
	if c.Server.Address != "127.0.0.1" || c.Server.ChannelPort != 7001 || c.Server.GamePort != 10019 ||
		!nativeParty(c) || c.TestModules.CounterEnabled || c.TestModules.SharedBoundaryEnabled {
		return errors.New("the matching launcher requires local entry at 127.0.0.1:7001/10019 with test modules disabled")
	}
	return nil
}
