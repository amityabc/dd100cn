package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"s9a1/server/internal/config"
	"s9a1/server/internal/host"
	"s9a1/server/internal/operator"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

func main() {
	if err := run(); err != nil {
		if !errors.Is(err, errOperatorDeclined) && !errors.Is(err, errPreflightFailed) {
			slog.Error("serve100", "error", err)
		}
		os.Exit(1)
	}
}

var errOperatorDeclined = errors.New("operator request declined")

func run() (runErr error) {
	for _, arg := range os.Args[1:] {
		if arg == "--create-release-database" {
			return releaseSeed(os.Args[1:])
		}
	}
	if kind := operator.CLIKind(os.Args[1:]); kind != "" {
		root, i, err := operator.ParseCLI(kind, os.Args[1:])
		if err != nil {
			return err
		}
		body, err := json.Marshal(i.Command)
		if err != nil {
			return err
		}
		timeout := 20 * time.Second
		if kind == "--gm-mail" {
			timeout = 30 * time.Second
		}
		ctx, done := context.WithTimeout(context.Background(), timeout)
		defer done()
		reply, err := operator.Call(ctx, root, body)
		if mail, ok := i.Command.(operator.Mail); ok && errors.Is(err, operator.ErrNotConnected) {
			if absolute, e := filepath.Abs(root); e == nil {
				cfg := config.Config{Root: absolute}
				if path := runtimeCatalogPath(cfg, false); path != cfg.CatalogPath() {
					if err := os.Setenv("S9A1_CREATION_CATALOG", path); err != nil {
						return err
					}
				}
			}
			reply, err = host.QueueDisconnectedMail(root, mail)
			if err != nil {
				reply, err = operator.Message("NotSent", mail.OperationId, "离线投递未保存："+err.Error()), nil
			}
		}
		if err != nil {
			return err
		}
		if err = json.NewEncoder(os.Stdout).Encode(reply); err != nil {
			return err
		}
		if !reply.ReadBack || reply.Status != "Granted" && reply.Status != "Replay" && !(kind == "--gm-mail" && reply.Status == "Queued") {
			return errOperatorDeclined
		}
		return nil
	}
	for _, arg := range os.Args[1:] {
		if arg == "--gm-index" {
			return gmIndex(os.Args[1:])
		}
	}
	var level slog.Level
	if raw := os.Getenv("S9A1_LOG_LEVEL"); raw != "" {
		if err := level.UnmarshalText([]byte(raw)); err != nil {
			return err
		}
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	}
	args, err := parseLifecycle(os.Args[1:])
	if err != nil {
		return err
	}
	c, err := config.Load(args.Root)
	if err != nil {
		return err
	}
	if args.Prepare {
		return prepareClient(c)
	}
	if args.Check {
		report := preflight(c, args.Evidence)
		if err = json.NewEncoder(os.Stdout).Encode(report); err != nil {
			return err
		}
		if !report.Passed {
			return errPreflightFailed
		}
		return nil
	}
	stopPath, err := c.StopPath()
	if err != nil {
		return err
	}
	readyPath := filepath.Join(c.Root, "runtime", "go-ready.json")
	legacyPath := filepath.Join(c.Root, "runtime", "next-ready.json")
	lockPath := filepath.Join(c.Root, "runtime", "go-instance.lock")
	if args.Stop {
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
	if err = guardReadyOwner(legacyPath); err != nil {
		return err
	}
	if err = os.Remove(stopPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = os.Remove(readyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = c.GamePorts(); err != nil {
		return err
	}
	logs, err := newLifecycleLog(c.Root, args.RunID, args.Bounded)
	if err != nil {
		return err
	}
	priorLogger := slog.Default()
	slog.SetDefault(slog.New(lifecycleHandler{log: logs, min: level}))
	defer func() {
		if runErr != nil {
			slog.Error("serve100", "error", runErr)
		}
		slog.SetDefault(priorLogger)
		runErr = errors.Join(runErr, logs.Close())
	}()
	ctx, done := signal.NotifyContext(context.Background(), os.Interrupt)
	defer done()
	begin := time.Now()
	if c.Gameplay.ClientDirectory != "" || c.Resources.PvfSha256 != "" {
		if err = startupPvfCheck(c); err != nil {
			return err
		}
	}
	catalogPath := runtimeCatalogPath(c, false)
	characters, err := resources.LoadCharacters(catalogPath, c.Path(c.Resources.Pvf), c.Path(c.Resources.ClientProfile), c.Gameplay.ExperienceMultiplier)
	if err != nil {
		return err
	}
	slog.Info("ResourceReady", "event", "ResourceReady", "catalog", catalogPath)
	dsn, err := configuredDSN(c.Root)
	if err != nil {
		return err
	}
	db, err := storage.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	pass := characters.Quests.BattlePass
	if err = pass.Configure(c.Gameplay.BattlePassStartsAt, c.Gameplay.BattlePassEndsAt); err != nil {
		return err
	}
	if c.Gameplay.BattlePassStartsAt != nil {
		db.BindBattlePass(pass.Update)
	}
	if err = db.Initialize(ctx); err != nil {
		return err
	}
	s, err := host.New(c, db, characters)
	if err != nil {
		return err
	}
	frames, err := startFrameLog(c, args.RunID)
	if err != nil {
		return err
	}
	if frames != nil {
		defer frames.file.Close()
		s.ObserveFrame = frames.observe
	}
	defer os.Remove(readyPath)
	defer os.Remove(legacyPath)
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
		if err = writeJSONAtomic(readyPath, status); err != nil {
			return err
		}
		if _, err = publishLegacyReady(legacyPath, c, args.RunID, logs.path, ports.Channel, ports.Games[19]); err != nil {
			return err
		}
		slog.Info("FoundationReady", "event", "FoundationReady", "channelPort", ports.Channel, "gamePort", ports.Games[19])
		if err = logs.failure(); err != nil {
			return err
		}
		return logs.say(string(data))
	})
}
