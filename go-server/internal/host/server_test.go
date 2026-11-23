package host

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"s9a1/server/internal/protocol"
)

func TestDisconnectCancelsQueuedGameCommands(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &connection{ctx: ctx, cancel: cancel, serial: newSerialQueue(1)}
	g := game{conn: conn, server: &Server{maxFrame: 4096, receiveQueue: 1}}
	result := make(chan error, 1)
	p.serial.Lock()
	go func() { result <- g.serveFrames(ctx, p) }()
	packet, err := protocol.EncodeGame(protocol.ClientToServer, 1, 8, 1, nil, false)
	if err == nil {
		err = peer.SetWriteDeadline(time.Now().Add(2 * time.Second))
	}
	if err == nil {
		_, err = peer.Write(packet)
	}
	peer.Close()
	if err != nil {
		cancel()
		p.serial.Unlock()
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		p.serial.Unlock()
	case <-time.After(2 * time.Second):
		cancel()
		p.serial.Unlock()
		t.Fatal("peer close did not cancel the owner while a command was queued")
	}
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("serveFrames error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("game owner did not join the reader")
	}
}
