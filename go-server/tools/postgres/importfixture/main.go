// importfixture restores a legacy backup into a fresh database on the project's
// isolated PostgreSQL test cluster. Existing databases are left in place.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"s9a1/server/internal/storage/legacy"
)

func run() error {
	name := flag.String("name", "", "new fixture name (letters, digits and underscores)")
	account := flag.String("account", "", "destination game account")
	source := flag.String("source", "", "path to an independent SQLite backup")
	flag.Parse()
	if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`).MatchString(*name) || *source == "" || *account == "" {
		return errors.New("provide name, account and source")
	}
	local, err := filepath.Abs(filepath.Join("..", ".work", "postgres-storage"))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(local, "test-dsn.txt"))
	if err != nil {
		return err
	}
	dsn, err := url.Parse(strings.TrimSpace(string(data)))
	if err != nil || dsn.Hostname() != "127.0.0.1" || dsn.User.Username() != "s9a1_test" {
		return errors.New("expected the project PostgreSQL connection")
	}
	ctx := context.Background()
	pg, err := sql.Open("pgx", dsn.String())
	if err != nil {
		return err
	}
	defer pg.Close()
	var directory, user string
	if err = pg.QueryRowContext(ctx, "SELECT current_setting('data_directory'),current_user").Scan(&directory, &user); err != nil {
		return err
	}
	actual, err := os.Stat(directory)
	if err != nil {
		return err
	}
	expected, err := os.Stat(filepath.Join(local, "data"))
	if err != nil || user != "s9a1_test" || !os.SameFile(actual, expected) {
		return errors.New("server does not own the project test data directory")
	}
	database := "s9a1_" + *name + "_fixture"
	if _, err = pg.ExecContext(ctx, "CREATE DATABASE "+database); err != nil {
		return fmt.Errorf("create fresh fixture: %w", err)
	}
	dsn.Path = "/" + database
	r, err := legacy.Import(ctx, *source, dsn.String(), *account, true)
	if err != nil {
		return err
	}
	if !r.Activated {
		return fmt.Errorf("fixture activation: %v", r.ActivationIssues)
	}
	out := filepath.Join(local, *name+"-dsn.txt")
	if err = os.WriteFile(out, []byte(dsn.String()), 0600); err != nil {
		return err
	}
	fmt.Printf("Restored %s account %s (%s). Connection file %s\n", database, *account, r.SHA256, out)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
