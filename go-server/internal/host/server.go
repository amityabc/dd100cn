package host

import (
	"bufio"
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

	"github.com/google/uuid"

	"s9a1/server/internal/config"
	"s9a1/server/internal/operator"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

type Server struct {
	DB                                               *storage.Store
	Characters                                       *resources.CharacterCatalog
	catalog                                          protocol.ChannelCatalog
	maxFrame                                         int
	readChunk, receiveQueue, sendQueue, sessionQueue int
	native140, nativeParty, delegated                bool
	slots                                            chan struct{}
	mu                                               sync.Mutex
	active                                           map[string]*connection
	gmAccounts                                       []string
	mailGate                                         sync.Mutex
	outbox                                           operator.Outbox
	ObserveFrame                                     func(FrameObservation)
}

type FrameObservation struct {
	Frame                                     protocol.Frame
	Connection, LocalEndpoint, RemoteEndpoint string
	Outcome                                   string
	Length, BodyLength                        int
	Sensitive                                 bool
}

func (s *Server) recordFrame(conn net.Conn, id, outcome string, f protocol.Frame) {
	if s.ObserveFrame == nil {
		return
	}
	event := FrameObservation{Frame: f, Connection: id, LocalEndpoint: conn.LocalAddr().String(),
		RemoteEndpoint: conn.RemoteAddr().String(), Outcome: outcome, Length: len(f.Raw), BodyLength: len(f.Body),
		Sensitive: f.Transport == protocol.Channel || f.Opcode == 1}
	if event.Sensitive {
		event.Frame.Raw, event.Frame.Body = nil, nil
	}
	s.ObserveFrame(event)
}

func (s *Server) recordSent(conn net.Conn, id string, kind protocol.Transport, raw []byte) {
	if s.ObserveFrame == nil {
		return
	}
	f, err := protocol.DecodeFrame(raw, kind, protocol.ServerToClient)
	if err == nil {
		s.recordFrame(conn, id, "Sent", f)
	}
}

type connection struct {
	cancel context.CancelFunc
	done   chan struct{}
	ctx    context.Context
	serial serialQueue
	player *game
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
	db.BindDailyExpiry(templates.Commerce.DailyExpiryTemplates)
	return &Server{DB: db, Characters: templates, catalog: catalog, maxFrame: c.Server.MaximumFrameBytes,
		readChunk: c.Server.ReceiveChunkBytes, receiveQueue: c.Server.ReceiveQueueCapacity, sendQueue: c.Server.SendQueueCapacity, sessionQueue: c.Server.SessionQueueCapacity,
		native140: c.Gameplay.NativeTownProfile140, nativeParty: nativeParty, delegated: c.Gameplay.ClientDelegatedCombatEnabled,
		slots: make(chan struct{}, c.Server.MaximumConnections), active: make(map[string]*connection), gmAccounts: c.Gameplay.GmAccounts, outbox: operator.Outbox{Root: c.Root}}, nil
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
	if c.Gameplay.LocalAccountEnabled {
		wg.Add(1)
		go func() { defer wg.Done(); s.pumpOperatorMail(ctx) }()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := operator.Serve(ctx, c.Root, s.operatorCommand); err != nil && ctx.Err() == nil {
				select {
				case fail <- err:
				case <-ctx.Done():
				}
			}
		}()
	}
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
	conn = &nativeConn{Conn: conn, reader: bufio.NewReaderSize(chunkReader{conn, s.readChunk}, s.readChunk),
		ctx: ctx, maxFrame: s.maxFrame, writer: newSerialQueue(s.sendQueue)}
	defer cancel()
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	observationID := ""
	if s.ObserveFrame != nil {
		observationID = uuid.NewString()
	}
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
			frame, err := protocol.ReadFrameLimited(conn, kind, protocol.ClientToServer, s.maxFrame)
			if err != nil {
				return err
			}
			s.recordFrame(conn, observationID, "Received", frame)
			if !protocol.RegisteredRequest(frame, s.delegated) {
				s.recordFrame(conn, observationID, "UnsupportedOpcode", frame)
				continue
			}
			reply, err := session.Reply(frame, time.Now(), s.catalog.Script, directory)
			if err != nil {
				return err
			}
			if err = writeFull(conn, reply); err != nil {
				return err
			}
			s.recordSent(conn, observationID, kind, reply)
		}
	}
	if kind != protocol.Game || ports[id] == 0 || account == "" {
		return errors.New("invalid game endpoint or account")
	}
	p := &connection{cancel: cancel, done: make(chan struct{}), ctx: ctx, serial: newSerialQueue(s.sessionQueue)}
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
	g := game{server: s, conn: conn, lease: lease, channel: id, observationID: observationID}
	if err := g.open(ctx, ports[id]); err != nil {
		return err
	}
	p.serial.Lock()
	p.player = &g
	p.serial.Unlock()
	return g.serveFrames(ctx, p)
}

func (g *game) serveFrames(ctx context.Context, p *connection) error {
	type readResult struct {
		frame protocol.Frame
		err   error
	}
	frames := make(chan readResult, g.server.receiveQueue)
	readerDone := make(chan struct{})
	reading, stop := context.WithCancel(ctx)
	defer func() { stop(); g.conn.Close(); <-readerDone }()
	go func() {
		defer close(readerDone)
		for {
			frame, err := protocol.ReadFrameLimited(g.conn, protocol.Game, protocol.ClientToServer, g.server.maxFrame)
			select {
			case frames <- readResult{frame, err}:
			case <-reading.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		p.serial.Lock()
		deadline := g.dungeonDeadline()
		p.serial.Unlock()
		var tick <-chan time.Time
		if !deadline.IsZero() {
			delay := time.Until(deadline)
			if timer == nil {
				timer = time.NewTimer(delay)
			} else {
				timer.Reset(delay)
			}
			tick = timer.C
		} else if timer != nil {
			timer.Stop()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
			p.serial.Lock()
			err := g.dungeonTick(ctx)
			p.serial.Unlock()
			if err != nil {
				return err
			}
		case received := <-frames:
			if received.err != nil {
				return received.err
			}
			f := received.frame
			g.server.recordFrame(g.conn, g.observationID, "Received", f)
			if !protocol.RegisteredRequest(f, g.server.delegated) {
				g.server.recordFrame(g.conn, g.observationID, "UnsupportedOpcode", f)
				continue
			}
			p.serial.Lock()
			g.seq = f.Sequence
			request, reads := storage.TrackInventoryProjection(ctx)
			g.inventoryFrame = &inventoryFrame{reads: reads}
			pause := f.Opcode == 3 || f.Opcode == 7 || f.Opcode == 8
			var err error
			err = g.syncPetClock(ctx, pause, f.Opcode == 44)
			if err == nil {
				err = g.handle(request, f)
			}
			if err == nil {
				err = g.showPremium()
			}
			if err == nil {
				err = g.sendBattlePass(ctx, false)
			}
			if err == nil {
				switch f.Opcode {
				case 16, 35, 37, 42, 72, 120, 132, 143, 1715:
					err = g.refreshCharacterServices(ctx, false)
				default:
					if g.characterServices.nextRefresh > 0 && time.Now().Unix() >= g.characterServices.nextRefresh {
						err = g.refreshCharacterServices(ctx, false)
					}
				}
			}
			if err == nil && f.Opcode != 527 {
				err = g.restoreCrystal(ctx)
			}
			if err == nil && f.Class == 1 {
				err = g.refreshBuffProjection(ctx, f.Opcode, reads.Current)
			}
			if err == nil && !pause {
				err = g.syncPetClock(ctx, false, f.Opcode == 44)
			}
			g.inventoryFrame = nil
			p.serial.Unlock()
			if err != nil {
				return fmt.Errorf("game opcode %d sequence %d: %w", f.Opcode, f.Sequence, err)
			}
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
