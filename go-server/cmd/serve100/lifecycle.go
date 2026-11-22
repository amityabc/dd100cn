package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"s9a1/server/internal/config"
	"slices"
	"strings"

	"github.com/google/uuid"
)

var errPreflightFailed = errors.New("input preflight failed")

type lifecycleOptions struct {
	Root, RunID, Evidence         string
	Stop, Prepare, Check, Bounded bool
}

func parseLifecycle(args []string) (l lifecycleOptions, err error) {
	l.Root = "."
	seen := map[string]bool{}
	for len(args) > 0 {
		key := args[0]
		args = args[1:]
		if seen[key] && key != "--root" {
			return l, fmt.Errorf("duplicate lifecycle argument %s", key)
		}
		seen[key] = true
		switch key {
		case "--root", "--run-id", "--evidence-root":
			if len(args) == 0 {
				return l, fmt.Errorf("%s requires a value", key)
			}
			v := args[0]
			args = args[1:]
			if strings.TrimSpace(v) == "" {
				return l, fmt.Errorf("%s requires a value", key)
			}
			switch key {
			case "--root":
				l.Root = v
			case "--run-id":
				id, e := uuid.Parse(v)
				if e != nil || len(v) != 32 {
					return l, errors.New("RunId must be a GUID in N format")
				}
				l.RunID = strings.ReplaceAll(id.String(), "-", "")
			case "--evidence-root":
				l.Evidence = v
			}
		case "--request-stop":
			l.Stop = true
		case "--prepare-client":
			l.Prepare = true
		case "--check-inputs":
			l.Check = true
		case "--bounded-console":
			l.Bounded = true
		default:
			return l, fmt.Errorf("unsupported lifecycle argument %q", key)
		}
	}
	modes := 0
	for _, v := range []bool{l.Stop, l.Prepare, l.Check} {
		if v {
			modes++
		}
	}
	if modes > 1 || l.Evidence != "" && !l.Check || modes != 0 && (l.RunID != "" || l.Bounded) {
		return l, errors.New("stop, prepare-client and check-inputs are separate operations; evidence-root is for check-inputs; run-id and bounded-console are for a server run")
	}
	if l.RunID == "" && modes == 0 {
		l.RunID = strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	return l, nil
}

type legacyReady struct {
	Schema, ProcessId                                                     int
	StartedUtcTicks                                                       int64
	HostPath, HostSha256, RunId, ServerEventsPath                         string
	LocalEntry, LocalGmCommands, NativePartyNotices, NativeTownProfile140 bool
	ChannelPort, GamePort                                                 uint16
}

func (l legacyReady) matches(c config.Config, exe string) bool {
	img, ticks, err := processIdentity(l.ProcessId)
	if err != nil || l.Schema != 1 || !l.LocalEntry || ticks != l.StartedUtcTicks || !samePath(l.HostPath, exe) || !samePath(img, exe) ||
		l.ChannelPort != 7001 || l.GamePort != 10019 || l.NativePartyNotices != nativeParty(c) || l.NativeTownProfile140 != c.Gameplay.NativeTownProfile140 {
		return false
	}
	sha, _, err := fileFingerprint(exe)
	return err == nil && l.HostSha256 == sha
}

func nativeParty(c config.Config) bool {
	return c.Gameplay.NativePartyNotices == nil || *c.Gameplay.NativePartyNotices
}

func configuredDSN(root string) (string, error) {
	if v := strings.TrimSpace(os.Getenv("S9A1_POSTGRES_DSN")); v != "" {
		return v, nil
	}
	p := filepath.Join(root, "runtime", "postgres-dsn.txt")
	raw, err := boundedRead(p, 8192)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if v := strings.TrimSpace(string(raw)); v != "" {
		return v, nil
	}
	return "", errors.New("runtime/postgres-dsn.txt is empty")
}
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func writeJSONAtomic(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return replaceFile(f.Name(), path)
}

func publishLegacyReady(path string, c config.Config, runID, events string, channelPort, gamePort uint16) (legacyReady, error) {
	var out legacyReady
	exe, ticks, err := processIdentity(os.Getpid())
	if err != nil {
		return out, err
	}
	hash, _, err := fileFingerprint(exe)
	if err != nil {
		return out, err
	}
	out = legacyReady{1, os.Getpid(), ticks, exe, hash, runID, events, c.Gameplay.LocalAccountEnabled,
		c.Gameplay.LocalAccountEnabled && slices.Contains(c.Gameplay.GmAccounts, "local"), nativeParty(c), c.Gameplay.NativeTownProfile140,
		channelPort, gamePort}
	return out, writeJSONAtomic(path, out)
}

func guardReadyOwner(path string) error {
	raw, err := boundedRead(path, 65536)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var ready legacyReady
	if json.Unmarshal(raw, &ready) != nil || ready.ProcessId <= 0 {
		return nil
	}
	_, ticks, err := processIdentity(ready.ProcessId)
	if err == nil && ready.StartedUtcTicks == ticks {
		return errors.New("server root already has a running owner")
	}
	return nil
}
