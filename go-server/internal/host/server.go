package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"s9a1/server/internal/config"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

type Server struct {
	DB                                *storage.Store
	Characters                        *resources.CharacterCatalog
	catalog                           protocol.ChannelCatalog
	maxFrame                          int
	native140, nativeParty, delegated bool
	slots                             chan struct{}
	mu                                sync.Mutex
	active                            map[string]*connection
}

type connection struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func New(c config.Config, db *storage.Store, templates *resources.CharacterCatalog) (*Server, error) {
	if _, err := c.GamePorts(); err != nil {
		return nil, err
	}
	script, err := os.ReadFile(c.Path(c.Gameplay.ChannelScript))
	if err != nil {
		return nil, err
	}
	catalog, err := protocol.ParseChannelCatalog(script, true)
	if err != nil {
		return nil, err
	}
	nativeParty := c.Gameplay.NativePartyNotices == nil || *c.Gameplay.NativePartyNotices
	return &Server{DB: db, Characters: templates, catalog: catalog, maxFrame: c.Server.MaximumFrameBytes, native140: c.Gameplay.NativeTownProfile140, nativeParty: nativeParty, delegated: c.Gameplay.ClientDelegatedCombatEnabled, slots: make(chan struct{}, c.Server.MaximumConnections), active: make(map[string]*connection)}, nil
}

type Ports struct {
	Channel uint16
	Games   map[uint16]uint16
}
type listener struct {
	net.Listener
	kind protocol.Transport
	id   uint16
}

// Listen binds every legacy entry point before exposing any of them. On failure it releases the partial set.
func (s *Server) Listen(ctx context.Context, c config.Config, ready func(Ports) error) error {
	gamePorts, err := c.GamePorts()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	var listeners []listener
	var wg sync.WaitGroup
	defer func() {
		cancel()
		for _, ln := range listeners {
			ln.Close()
		}
		wg.Wait()
	}()
	ports := Ports{Games: make(map[uint16]uint16)}
	bind := func(kind protocol.Transport, id uint16, p int) error {
		ln, err := net.Listen("tcp", net.JoinHostPort(c.Server.Address, strconv.Itoa(p)))
		if err != nil {
			return err
		}
		listeners = append(listeners, listener{ln, kind, id})
		actual := uint16(ln.Addr().(*net.TCPAddr).Port)
		if kind == protocol.Channel {
			ports.Channel = actual
		} else {
			ports.Games[id] = actual
		}
		return nil
	}
	if err := bind(protocol.Channel, 0, c.Server.ChannelPort); err != nil {
		return err
	}
	for _, entry := range s.catalog.Entries {
		if err := bind(protocol.Game, entry.ID, gamePorts[entry.ID]); err != nil {
			return err
		}
	}
	fail := make(chan error, len(listeners))
	for _, ln := range listeners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				conn, err := ln.Accept()
				if err != nil {
					select {
					case fail <- err:
					case <-ctx.Done():
					}
					return
				}
				select {
				case s.slots <- struct{}{}:
				case <-ctx.Done():
					conn.Close()
					return
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-s.slots }()
					err := s.ServeConnection(ctx, conn, "local", ln.kind, ln.id, ports.Games)
					if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && ctx.Err() == nil {
						slog.Debug("connection ended", "error", err)
					}
				}()
			}
		}()
	}
	if err := ready(ports); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-fail:
		return err
	}
}

// ServeConnection accepts an account already established by the calling entry point. Game packets never choose this identity.
func (s *Server) ServeConnection(parent context.Context, conn net.Conn, account string, kind protocol.Transport, id uint16, ports map[uint16]uint16) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if kind == protocol.Channel {
		var session protocol.ChannelSession
		directory := make([]protocol.ChannelEntry, 0, len(s.catalog.Entries))
		for _, entry := range s.catalog.Entries {
			port := ports[entry.ID]
			if port == 0 {
				return fmt.Errorf("missing channel %d", entry.ID)
			}
			directory = append(directory, protocol.ChannelEntry{ID: entry.ID, Port: port, Capacity: 500})
		}
		for {
			frame, err := protocol.ReadFrame(conn, kind, protocol.ClientToServer)
			if err != nil {
				return err
			}
			if len(frame.Raw) > s.maxFrame {
				return errors.New("configured frame limit exceeded")
			}
			reply, err := session.Reply(frame, time.Now(), s.catalog.Script, directory)
			if err != nil {
				return err
			}
			if err = writeFull(conn, reply); err != nil {
				return err
			}
		}
	}
	if kind != protocol.Game || ports[id] == 0 || account == "" {
		return errors.New("invalid game endpoint or account")
	}
	p := &connection{cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	old := s.active[account]
	s.active[account] = p
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.active[account] == p {
			delete(s.active, account)
		}
		s.mu.Unlock()
		close(p.done)
	}()
	if old != nil {
		old.cancel()
		<-old.done
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lease, err := s.DB.Acquire(ctx, account)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		if err := s.DB.Release(cleanup, lease); err != nil && !errors.Is(err, storage.ErrLease) {
			slog.Warn("release game session", "error", err)
		}
	}()
	g := game{server: s, conn: conn, lease: lease, channel: id}
	if err := g.open(ctx, ports[id]); err != nil {
		return err
	}
	for {
		frame, err := protocol.ReadFrame(conn, protocol.Game, protocol.ClientToServer)
		if err != nil {
			return err
		}
		if len(frame.Raw) > s.maxFrame {
			return errors.New("configured frame limit exceeded")
		}
		if err = g.handle(ctx, frame); err != nil {
			return fmt.Errorf("game opcode %d sequence %d: %w", frame.Opcode, frame.Sequence, err)
		}
		if err = g.showPremium(); err != nil {
			return err
		}
	}
}

func writeFull(conn net.Conn, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
