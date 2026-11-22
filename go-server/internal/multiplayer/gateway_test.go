package multiplayer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"s9a1/server/internal/protocol"
)

func TestTwoAccountsCarryUnchangedLegacyFrames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "operator")
	if err := Initialize(dir, listener.Addr().String(), ""); err != nil {
		t.Fatal(err)
	}
	type sample struct{ Request, Response string }
	var fixture struct{ Channel, Game []sample }
	raw, err := os.ReadFile("../../testdata/protocol/legacy-empty.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	legacy := map[protocol.Transport]sample{protocol.Channel: fixture.Channel[0], protocol.Game: fixture.Game[0]}
	type arrival struct {
		account string
		id      uint16
		kind    protocol.Transport
	}
	arrivals := make(chan arrival, 8)
	ready := make(chan struct{})
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- Serve(ctx, listener, GatewayConfig{CertFile: filepath.Join(dir, "gateway-cert.pem"), KeyFile: filepath.Join(dir, "gateway-key.pem"), AccountsFile: filepath.Join(dir, "accounts.json"), MaximumConnections: 4, Ready: func() { close(ready) }},
			func(_ context.Context, c net.Conn, account string, kind protocol.Transport, id uint16, ports map[uint16]uint16) error {
				if len(ports) != 5 || ports[0] != 7001 || ports[19] != 10019 || ports[251] != 10501 || ports[252] != 10252 || ports[63] != 10063 {
					return fmt.Errorf("wrong advertised ports %v", ports)
				}
				request, _ := hex.DecodeString(legacy[kind].Request)
				reply, _ := hex.DecodeString(legacy[kind].Response)
				wire := make([]byte, len(request))
				if _, err := io.ReadFull(c, wire); err != nil {
					return err
				}
				if !bytes.Equal(request, wire) {
					return fmt.Errorf("unexpected bytes on native handler")
				}
				arrivals <- arrival{account, id, kind}
				_, err := c.Write(reply)
				return err
			})
	}()
	defer func() {
		cancel()
		select {
		case e := <-serveDone:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(3 * time.Second):
			t.Error("gateway shutdown did not finish")
		}
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("gateway did not report readiness")
	}
	addresses := map[string]map[uint16]string{}
	var profiles []PlayerConfig
	for _, account := range []string{"alice", "bob"} {
		file := filepath.Join(root, account+".json")
		if err := Issue(dir, account, file); err != nil {
			t.Fatal(err)
		}
		profile, err := LoadPlayer(file)
		if err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, profile)
		addresses[account] = map[uint16]string{}
		proxy, err := startLocal(ctx, profile, func(id, _ uint16) (net.Listener, error) {
			l, err := net.Listen("tcp4", "127.0.0.1:0")
			if err == nil {
				addresses[account][id] = l.Addr().String()
			}
			return l, err
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer proxy.Close()
	}
	bad := profiles[0]
	bad.Token = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if proxy, err := startLocal(ctx, bad, func(uint16, uint16) (net.Listener, error) {
		t.Fatal("unauthenticated profile reached listener setup")
		return nil, nil
	}, nil); err == nil {
		proxy.Close()
		t.Fatal("invalid credential accepted")
	}
	// An old or hand-edited registry must never grant the direct host identity.
	if err := Issue(dir, "LoCaL", filepath.Join(root, "reserved.json")); err == nil {
		t.Fatal("issued reserved loopback account")
	}
	token, err := base64.RawURLEncoding.DecodeString(profiles[0].Token)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(dir, "accounts.json")
	registry, err := readAccounts(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(token)
	registry["local"] = hex.EncodeToString(digest[:])
	encoded, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(registryPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(profiles[0].ServerCertificate))
	probe, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS13, NextProtos: []string{alpn}})
	if err != nil {
		t.Fatal(err)
	}
	probe.SetDeadline(time.Now().Add(2 * time.Second))
	request := append([]byte{gameStream, 0, 19, 5}, []byte("local")...)
	request = append(request, token...)
	if _, err = probe.Write(request); err != nil {
		t.Fatal(err)
	}
	var rejection [1]byte
	if _, err = io.ReadFull(probe, rejection[:]); err != nil || rejection[0] != 1 {
		t.Fatal("gateway admitted reserved identity")
	}
	probe.Close()
	// Both players' real TCP connections are open at once, behind the same remote IP.
	for _, id := range []uint16{0, 19, 251} {
		kind := protocol.Game
		if id == 0 {
			kind = protocol.Channel
		}
		var active []net.Conn
		for _, player := range profiles {
			c, err := net.DialTimeout("tcp", addresses[player.Account][id], 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			c.SetDeadline(time.Now().Add(3 * time.Second))
			active = append(active, c)
			defer c.Close()
		}
		for i, c := range active {
			request, _ := hex.DecodeString(legacy[kind].Request)
			expected, _ := hex.DecodeString(legacy[kind].Response)
			if _, err := c.Write(request); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(expected))
			if _, err := io.ReadFull(c, got); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, expected) {
				t.Fatal("changed legacy response")
			}
			want := arrival{profiles[i].Account, id, kind}
			if got := <-arrivals; got != want {
				t.Fatalf("got %#v want %#v", got, want)
			}
		}
	}
	select {
	case extra := <-arrivals:
		t.Fatalf("extra native callback: %#v", extra)
	default:
	}
}
