// Command town-state reads the single local replay character and its receipts.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func run() error {
	path := flag.String("dsn-file", "", "file containing the isolated replay database connection string")
	flag.Parse()
	b, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	db, err := sql.Open("pgx", strings.TrimSpace(string(b)))
	if err != nil {
		return errors.New("invalid replay database connection settings")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var row string
	err = db.QueryRowContext(ctx, `SELECT row_to_json(p)::text FROM (
		SELECT wire_id AS id,name,job,level,created,revision,town,area,x,y,direction,state,wire_tail,gold,coin,
		       stats_json,gameplay_json,bag_expansion,
		       (SELECT coalesce(json_object_agg(event_key,payload),'{}'::json)
		          FROM s9a1.reward_receipts WHERE character_id=c.wire_id) AS receipts
		  FROM s9a1.characters c WHERE account='local' AND wire_id=1
	) p`).Scan(&row)
	if err != nil {
		return fmt.Errorf("reading the isolated replay character: %w", err)
	}
	if !json.Valid([]byte(row)) {
		return errors.New("invalid character snapshot")
	}
	fmt.Println(row)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
