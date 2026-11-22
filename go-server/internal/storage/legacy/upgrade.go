package legacy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	sqlite "modernc.org/sqlite"
)

const latestLocalEntry = 35

type UpgradeOptions struct {
	ProjectDir string
	Dotnet     string
	Assembly   string
}

func (s *source) copySnapshot(ctx context.Context, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return s.conn.Raw(func(driver any) error {
		d, ok := driver.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite driver cannot produce a snapshot backup")
		}
		// The read transaction is still open on this exact connection. The
		// backup sees the same committed WAL pages as digest calculation and staging.
		b, err := d.NewBackup(sqliteURI(path, "mode=rw"))
		if err != nil {
			return err
		}
		for err == nil {
			if err = ctx.Err(); err != nil {
				break
			}
			var more bool
			more, err = b.Step(256)
			if !more {
				break
			}
		}
		return errors.Join(err, b.Finish())
	})
}

func (s *source) upgrade(ctx context.Context, opts UpgradeOptions) (_ *source, _ func(), retErr error) {
	if opts.ProjectDir == "" {
		return nil, nil, errors.New("v1–v34 activation needs a project directory for a private SQLite snapshot")
	}
	project, err := filepath.EvalSymlinks(opts.ProjectDir)
	if err != nil {
		return nil, nil, err
	}
	project, err = filepath.Abs(project)
	if err != nil {
		return nil, nil, err
	}
	dir, err := privateUpgradeDir(project)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	defer func() {
		if retErr != nil {
			cleanup()
		}
	}()
	copyPath := filepath.Join(dir, "snapshot.sqlite3")
	if err = s.copySnapshot(ctx, copyPath); err != nil {
		return nil, nil, fmt.Errorf("copy the staged SQLite view: %w", err)
	}
	copied, err := Inspect(ctx, copyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect private backup: %w", err)
	}
	if copied.SHA256 != s.report.SHA256 || copied.ModuleVersion != s.report.ModuleVersion {
		return nil, nil, errors.New("private SQLite backup differs from the staged snapshot")
	}
	if opts.Assembly != "" {
		err = runUpgrade(ctx, opts, project, dir, copyPath)
	} else {
		err = upgradeNative(ctx, copyPath, s.report)
	}
	if err != nil {
		return nil, nil, err
	}
	install, err := openSource(ctx, copyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect upgraded copy: %w", err)
	}
	if install.report.ModuleVersion != latestLocalEntry || install.report.Characters != s.report.Characters ||
		s.report.ModuleVersion >= 5 && install.report.Gold != s.report.Gold {
		install.close()
		return nil, nil, errors.New("upgraded SQLite copy changed the source version, character count, or carried gold unexpectedly")
	}
	return install, func() { install.close(); cleanup() }, nil
}

func privateUpgradeDir(project string) (string, error) {
	work := filepath.Join(project, ".work")
	if err := os.Mkdir(work, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	work, err := resolveProjectDir(project, work)
	if err != nil {
		return "", err
	}
	p := filepath.Join(work, "legacy-import")
	if err = os.Mkdir(p, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	real, err := resolveProjectDir(project, p)
	if err != nil {
		return "", err
	}
	return os.MkdirTemp(real, "snapshot-")
}

func resolveProjectDir(project, path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(project, real)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("SQLite upgrade workspace resolves outside the Go project")
	}
	return real, nil
}

func runUpgrade(ctx context.Context, opts UpgradeOptions, project, dir, copyPath string) error {
	dotnet := opts.Dotnet
	if dotnet == "" {
		dotnet = "dotnet"
	} else if strings.ContainsAny(dotnet, `/\`) {
		var err error
		dotnet, err = filepath.Abs(dotnet)
		if err != nil {
			return err
		}
	}
	dll := opts.Assembly
	if dll == "" {
		dll = filepath.Join(project, ".work", "save-upgrade-build", "bin", "LegacyUpgrade", "release", "LegacyUpgrade.dll")
	}
	dll, err := filepath.Abs(dll)
	if err != nil {
		return err
	}
	if info, err := os.Stat(dll); err != nil || !info.Mode().IsRegular() {
		return errors.New("build the offline upgrader with tools/build-save-upgrade.ps1 or pass -upgrader")
	}
	cmd := exec.CommandContext(ctx, dotnet, dll, copyPath)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"DOTNET_CLI_HOME="+filepath.Join(project, ".work", "dotnet-home"),
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
		"DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1",
		"DOTNET_GENERATE_ASPNET_CERTIFICATE=false",
		"TEMP="+dir,
		"TMP="+dir,
		"TMPDIR="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run legacy v35 migrations: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
