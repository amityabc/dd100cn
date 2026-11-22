// import-save reads a consistent SQLite snapshot and can stage its rows in
// PostgreSQL or activate compatible character data under an explicit account.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"s9a1/server/internal/storage/legacy"
)

func main() {
	mode := flag.String("mode", "inspect", "inspect, stage, or activate")
	path := flag.String("source", "", "path to the old SQLite database or its consistent backup")
	account := flag.String("account", "", "destination account for stage/activate")
	dotnet := flag.String("dotnet", os.Getenv("S9A1_DOTNET"), "dotnet runtime for an explicitly selected reference upgrader")
	upgrader := flag.String("upgrader", "", "optional LegacyUpgrade.dll for reference comparison (default uses Go)")
	flag.Parse()
	if *path == "" {
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	var report legacy.Report
	var err error
	switch *mode {
	case "inspect":
		report, err = legacy.Inspect(ctx, *path)
	case "stage", "activate":
		report, err = legacy.ImportWithUpgrade(ctx, *path, os.Getenv("S9A1_DATABASE_URL"), *account, *mode == "activate",
			legacy.UpgradeOptions{ProjectDir: findGoProject(), Dotnet: *dotnet, Assembly: *upgrader})
	default:
		fmt.Fprintln(os.Stderr, "unknown mode")
		os.Exit(2)
	}
	if report.SHA256 != "" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if e := enc.Encode(report); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func findGoProject() string {
	var paths []string
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, cwd, filepath.Join(cwd, "go-server"))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		paths = append(paths, dir, filepath.Dir(dir), filepath.Dir(filepath.Dir(dir)))
	}
	for _, raw := range paths {
		path, err := filepath.Abs(raw)
		if err != nil {
			continue
		}
		mod, err := os.ReadFile(filepath.Join(path, "go.mod"))
		if err == nil && strings.HasPrefix(string(mod), "module s9a1/server") {
			return path
		}
		if stat, err := os.Stat(filepath.Join(path, "release-manifest.json")); err == nil && stat.Mode().IsRegular() {
			return path
		}
	}
	return ""
}
