package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	"s9a1/server/internal/config"
	"s9a1/server/internal/host"
	"s9a1/server/internal/multiplayer"
)

type gatewaySettings struct {
	Listen, CertFile, KeyFile, AccountsFile string
}

func readGateway(path string) (gatewaySettings, error) {
	var settings gatewaySettings
	data, err := os.ReadFile(path)
	if err != nil {
		return settings, err
	}
	if err = json.Unmarshal(data, &settings); err != nil {
		return settings, err
	}
	if settings.Listen == "" || settings.CertFile == "" || settings.KeyFile == "" || settings.AccountsFile == "" {
		return settings, errors.New("gateway config requires Listen, CertFile, KeyFile and AccountsFile")
	}
	for _, p := range []*string{&settings.CertFile, &settings.KeyFile, &settings.AccountsFile} {
		if !filepath.IsAbs(*p) {
			*p = filepath.Join(filepath.Dir(path), *p)
		}
	}
	return settings, nil
}

func listen(ctx context.Context, server *host.Server, c config.Config, gatewayPath string, ready func(host.Ports, string) error) error {
	if gatewayPath == "" {
		return server.Listen(ctx, c, func(p host.Ports) error { return ready(p, "") })
	}
	settings, err := readGateway(c.Path(gatewayPath))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", settings.Listen)
	if err != nil {
		return fmt.Errorf("bind multiplayer gateway: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- multiplayer.Serve(ctx, listener, multiplayer.GatewayConfig{
			CertFile: settings.CertFile, KeyFile: settings.KeyFile, AccountsFile: settings.AccountsFile,
			MaximumConnections: c.Server.MaximumConnections,
			Ready:              func() { close(started) },
			Report:             func(err error) { slog.Debug("gateway connection ended", "error", err) },
		}, server.ServeConnection)
		cancel()
	}()
	select {
	case <-started:
	case err := <-finished:
		return err
	}
	localErr := server.Listen(ctx, c, func(p host.Ports) error { return ready(p, listener.Addr().String()) })
	cancel()
	return errors.Join(localErr, <-finished)
}
