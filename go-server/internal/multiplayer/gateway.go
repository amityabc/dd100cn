// Package multiplayer carries the existing channel and game streams over a separate authenticated TLS connection.
package multiplayer

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"s9a1/server/internal/protocol"
)

const alpn = "s9a1-gateway/1"

const (
	probeStream byte = iota
	channelStream
	gameStream
)

type GatewayConfig struct {
	CertFile, KeyFile, AccountsFile string
	MaximumConnections              int
	Ready                           func()
	Report                          func(error)
}

type Handler func(context.Context, net.Conn, string, protocol.Transport, uint16, map[uint16]uint16) error

// Port zero in the map identifies the channel listener, not a native game-channel ID.
func clientPorts() map[uint16]uint16 {
	return map[uint16]uint16{0: 7001, 19: 10019, 251: 10501, 252: 10252, 63: 10063}
}

func Serve(ctx context.Context, listener net.Listener, config GatewayConfig, handler Handler) error {
	defer listener.Close()
	cert, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		return err
	}
	if _, err = readAccounts(config.AccountsFile); err != nil {
		return err
	}
	if handler == nil {
		return errors.New("multiplayer gateway requires a handler")
	}
	limit := config.MaximumConnections
	if limit == 0 {
		limit = 16
	}
	if limit < 0 {
		return errors.New("gateway connection limit must be positive")
	}
	active := make(chan struct{}, limit)
	transport := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{alpn}}
	serverCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	stopAccept := context.AfterFunc(serverCtx, func() { listener.Close() })
	defer stopAccept()
	if ctx.Err() != nil {
		return nil
	}
	if config.Ready != nil {
		config.Ready()
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case active <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-active }()
			defer conn.Close()
			stop := context.AfterFunc(serverCtx, func() { conn.Close() })
			defer stop()
			if err := acceptStream(serverCtx, tls.Server(conn, transport), config.AccountsFile, handler); err != nil && serverCtx.Err() == nil && config.Report != nil {
				config.Report(err)
			}
		}()
	}
}

func acceptStream(ctx context.Context, conn *tls.Conn, accounts string, handler Handler) error {
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		return err
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		return err
	}
	if conn.ConnectionState().NegotiatedProtocol != alpn {
		return errors.New("unrecognized gateway protocol")
	}
	// Header, account name and secret are confined to this TLS transport. No part reaches the game handler.
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	streamType, id, length := header[0], binary.BigEndian.Uint16(header[1:3]), int(header[3])
	ports := clientPorts()
	if length < 1 || length > 64 || !(streamType == probeStream && id == 0 || streamType == channelStream && id == 0 || streamType == gameStream && id != 0 && ports[id] != 0) {
		return errors.New("invalid gateway stream header")
	}
	var token [32]byte
	name := make([]byte, length)
	if _, err := io.ReadFull(conn, name); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, token[:]); err != nil {
		return err
	}
	account := string(name)
	registry, err := readAccounts(accounts)
	if err != nil {
		return fmt.Errorf("load gateway accounts: %w", err)
	}
	digest := sha256.Sum256(token[:])
	want, _ := hex.DecodeString(registry[account])
	if reservedAccount(account) || len(want) != len(digest) || subtle.ConstantTimeCompare(digest[:], want) != 1 {
		_, _ = conn.Write([]byte{1})
		return errors.New("gateway authentication rejected")
	}
	if _, err = conn.Write([]byte{0}); err != nil {
		return err
	}
	if streamType == probeStream {
		return nil
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	kind := protocol.Channel
	if streamType == gameStream {
		kind = protocol.Game
	}
	return handler(ctx, conn, account, kind, id, ports)
}
