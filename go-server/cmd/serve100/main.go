package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"s9a1/server/internal/config"
	"s9a1/server/internal/host"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("serve100", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if raw := os.Getenv("S9A1_LOG_LEVEL"); raw != "" {
		var level slog.Level
		if err := level.UnmarshalText([]byte(raw)); err != nil {
			return err
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	}
	root := flag.String("root", ".", "server root containing config/server.json")
	stop := flag.Bool("request-stop", false, "request orderly shutdown of this instance")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	c, err := config.Load(*root)
	if err != nil {
		return err
	}
	stopPath, err := c.StopPath()
	if err != nil {
		return err
	}
	readyPath := filepath.Join(c.Root, "runtime", "go-ready.json")
	lockPath := filepath.Join(c.Root, "runtime", "go-instance.lock")
	if *stop {
		if err = os.MkdirAll(filepath.Dir(stopPath), 0700); err != nil {
			return err
		}
		if err = os.WriteFile(stopPath, []byte("stop\n"), 0600); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct{ Outcome, Path string }{"StopRequested", stopPath})
	}
	if err = os.MkdirAll(filepath.Dir(readyPath), 0700); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(stopPath), 0700); err != nil {
		return err
	}
	lock, err := lockInstance(lockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = os.Remove(stopPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = os.Remove(readyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = c.GamePorts(); err != nil {
		return err
	}
	ctx, done := signal.NotifyContext(context.Background(), os.Interrupt)
	defer done()
	begin := time.Now()
	catalogPath := os.Getenv("S9A1_CREATION_CATALOG")
	if catalogPath == "" {
		return errors.New("set S9A1_CREATION_CATALOG to the PVF creation catalog")
	}
	characters, err := resources.LoadCharacters(c.Path(catalogPath), c.Path(c.Resources.Pvf), c.Path(c.Resources.ClientProfile))
	if err != nil {
		return err
	}
	db, err := storage.Open(ctx, os.Getenv("S9A1_POSTGRES_DSN"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = db.Initialize(ctx); err != nil {
		return err
	}
	s, err := host.New(c, db, characters)
	if err != nil {
		return err
	}
	defer os.Remove(readyPath)
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(stopPath); err == nil {
					done()
					return
				}
			}
		}
	}()
	return listen(ctx, s, c, os.Getenv("S9A1_GATEWAY_CONFIG"), func(ports host.Ports, gateway string) error {
		// This is the candidate's own status. The legacy launcher's HostPath contract refers to its .NET host.
		status := struct {
			Pid                 int
			Address             string
			Ports               host.Ports
			Gateway             string `json:",omitempty"`
			StartupMilliseconds int64
		}{os.Getpid(), c.Server.Address, ports, gateway, time.Since(begin).Milliseconds()}
		data, err := json.Marshal(status)
		if err != nil {
			return err
		}
		tmp := readyPath + ".tmp"
		if err = os.WriteFile(tmp, data, 0600); err != nil {
			return err
		}
		defer os.Remove(tmp)
		if err = os.Rename(tmp, readyPath); err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	})
}
