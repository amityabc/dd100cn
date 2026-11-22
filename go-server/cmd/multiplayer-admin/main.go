package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"s9a1/server/internal/multiplayer"
)

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: multiplayer-admin init|issue [options]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := flags.String("dir", "", "operator gateway directory")
	switch args[0] {
	case "init":
		gateway := flags.String("gateway", "", "DNS hostname or IP and port reached by the players")
		listen := flags.String("listen", "", "optional server bind address; defaults to -gateway")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *dir == "" || *gateway == "" || flags.NArg() != 0 {
			return errors.New("init needs -dir and -gateway")
		}
		if err := multiplayer.Initialize(*dir, *gateway, *listen); err != nil {
			return err
		}
		fmt.Println("Created gateway settings and TLS certificate in", *dir)
	case "issue":
		account := flags.String("account", "", "player account identifier")
		out := flags.String("out", "", "new private player credential file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *dir == "" || *account == "" || *out == "" || flags.NArg() != 0 {
			return errors.New("issue needs -dir, -account and -out")
		}
		if err := multiplayer.Issue(*dir, *account, *out); err != nil {
			return err
		}
		fmt.Println("Wrote private player credential to", *out)
	default:
		return errors.New("unknown command; use init or issue")
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
