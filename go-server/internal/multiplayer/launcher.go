package multiplayer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

type PlayerConfig struct {
	Gateway           string `json:"gateway"`
	Account           string `json:"account"`
	Token             string `json:"token"`
	ServerCertificate string `json:"serverCertificate"`
}

func LoadPlayer(path string) (PlayerConfig, error) {
	var player PlayerConfig
	err := readJSON(path, &player)
	return player, err
}

type LocalProxy struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *LocalProxy) Close() { p.cancel(); <-p.done }

func StartLocal(ctx context.Context, player PlayerConfig, report func(error)) (*LocalProxy, error) {
	return startLocal(ctx, player, func(_ uint16, port uint16) (net.Listener, error) {
		return net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	}, report)
}

func startLocal(ctx context.Context, player PlayerConfig, listen func(uint16, uint16) (net.Listener, error), report func(error)) (*LocalProxy, error) {
	host, _, err := net.SplitHostPort(player.Gateway)
	if err != nil || host == "" || !validAccount(player.Account) || reservedAccount(player.Account) {
		return nil, errors.New("invalid player endpoint or account")
	}
	token, err := base64.RawURLEncoding.DecodeString(player.Token)
	if err != nil || len(token) != 32 {
		return nil, errors.New("invalid player credential")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(player.ServerCertificate)) {
		return nil, errors.New("invalid trusted gateway certificate")
	}
	dialer := tls.Dialer{Config: &tls.Config{RootCAs: pool, ServerName: host, MinVersion: tls.VersionTLS13, NextProtos: []string{alpn}}}
	dial := func(ctx context.Context, stream byte, id uint16) (net.Conn, error) {
		timed, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		conn, err := dialer.DialContext(timed, "tcp", player.Gateway)
		if err != nil {
			return nil, err
		}
		if conn.(*tls.Conn).ConnectionState().NegotiatedProtocol != alpn {
			conn.Close()
			return nil, errors.New("gateway protocol mismatch")
		}
		stop := context.AfterFunc(timed, func() { conn.SetDeadline(time.Now()) })
		defer stop()
		deadline, _ := timed.Deadline()
		conn.SetDeadline(deadline)
		request := make([]byte, 4, 4+len(player.Account)+len(token))
		request[0] = stream
		binary.BigEndian.PutUint16(request[1:3], id)
		request[3] = byte(len(player.Account))
		request = append(request, player.Account...)
		request = append(request, token...)
		if _, err = conn.Write(request); err == nil {
			var reply [1]byte
			_, err = io.ReadFull(conn, reply[:])
			if err == nil && reply[0] != 0 {
				err = errors.New("gateway rejected player credentials")
			}
		}
		if err != nil {
			conn.Close()
			return nil, err
		}
		if !stop() {
			conn.Close()
			return nil, context.DeadlineExceeded
		}
		if err = conn.SetDeadline(time.Time{}); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	}
	check, err := dial(ctx, probeStream, 0)
	if err != nil {
		return nil, err
	}
	check.Close()
	listeners := make(map[uint16]net.Listener)
	for id, port := range clientPorts() {
		l, err := listen(id, port)
		if err != nil {
			for _, opened := range listeners {
				opened.Close()
			}
			return nil, fmt.Errorf("local port %d: %w", port, err)
		}
		listeners[id] = l
	}
	child, cancel := context.WithCancel(ctx)
	p := &LocalProxy{cancel: cancel, done: make(chan struct{})}
	var workers sync.WaitGroup
	for id, l := range listeners {
		workers.Add(1)
		go func() {
			defer workers.Done()
			stop := context.AfterFunc(child, func() { l.Close() })
			defer stop()
			defer l.Close()
			for {
				local, err := l.Accept()
				if err != nil {
					if child.Err() == nil && report != nil {
						report(err)
					}
					return
				}
				workers.Add(1)
				go func() {
					defer workers.Done()
					defer local.Close()
					stream := channelStream
					if id != 0 {
						stream = gameStream
					}
					remote, err := dial(child, stream, id)
					if err != nil {
						if child.Err() == nil && report != nil {
							report(err)
						}
						return
					}
					defer remote.Close()
					stop := context.AfterFunc(child, func() { local.Close(); remote.Close() })
					defer stop()
					copied := make(chan struct{})
					go func() {
						copyStream(remote, local)
						close(copied)
					}()
					copyStream(local, remote)
					<-copied
				}()
			}
		}()
	}
	go func() { workers.Wait(); close(p.done) }()
	return p, nil
}

func copyStream(dst, src net.Conn) {
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		src.Close()
		return
	}
	// Deliver EOF in this direction while allowing a final response in the opposite direction.
	if half, ok := dst.(interface{ CloseWrite() error }); ok {
		half.CloseWrite()
	} else {
		dst.Close()
	}
}
