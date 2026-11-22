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

	"s9a1/server/internal/storage/legacy"
)

func main() {
	mode := flag.String("mode", "inspect", "inspect, stage, or activate")
	path := flag.String("source", "", "path to the old SQLite database or its consistent backup")
	account := flag.String("account", "", "destination account for stage/activate")
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
		report, err = legacy.Import(ctx, *path, os.Getenv("S9A1_DATABASE_URL"), *account, *mode == "activate")
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
