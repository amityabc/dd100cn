package multiplayer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"s9a1/server/internal/protocol"
)

// Run only with two newly issued, disposable profiles on an operator's test gateway.
// Persisting the created characters is part of the test.
func TestLivePostgresTwoAccounts(t *testing.T) {
	paths := []string{os.Getenv("S9A1_TEST_PLAYER_A"), os.Getenv("S9A1_TEST_PLAYER_B")}
	if paths[0] == "" && paths[1] == "" {
		t.Skip("requires two fresh live gateway test profiles")
	}
	if paths[0] == "" || paths[1] == "" {
		t.Fatal("set both S9A1_TEST_PLAYER_A and S9A1_TEST_PLAYER_B")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var salt [4]byte
	if _, err := rand.Read(salt[:]); err != nil {
		t.Fatal(err)
	}
	var players [2]PlayerConfig
	var addresses [2]string
	var connections [2]net.Conn
	var ids [2]uint16
	var savedSettings [2]bool
	var options, hotkeys [2][]byte
	for i := range players {
		options[i] = bytes.Repeat([]byte{byte(0x31 + i), 0, 0xff}, 124)
		hotkeys[i] = []byte{0, byte(0x80 + i), 0xff, 0}
	}
	names := [2]string{"Alice" + hex.EncodeToString(salt[:]), "Bob" + hex.EncodeToString(salt[:])}
	for i, path := range paths {
		player, err := LoadPlayer(path)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && (player.Account == players[0].Account || player.Gateway != players[0].Gateway) {
			t.Fatal("provide two different accounts on the same test gateway")
		}
		players[i] = player
		proxy, err := startLocal(ctx, player, func(id, _ uint16) (net.Listener, error) {
			l, err := net.Listen("tcp4", "127.0.0.1:0")
			if err == nil && id == 19 {
				addresses[i] = l.Addr().String()
			}
			return l, err
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer proxy.Close()
	}
	read := func(c net.Conn, class byte, opcode, seq uint16) []byte {
		t.Helper()
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		f, err := protocol.ReadFrame(c, protocol.Game, protocol.ServerToClient)
		if err != nil {
			t.Fatalf("waiting for class %d opcode %d seq %d: %v", class, opcode, seq, err)
		}
		if f.Class != class || f.Opcode != opcode || f.Sequence != seq {
			t.Fatalf("wanted class/opcode/seq %d/%d/%d, got %d/%d/%d", class, opcode, seq, f.Class, f.Opcode, f.Sequence)
		}
		return f.Body
	}
	send := func(c net.Conn, opcode, seq uint16, body []byte) {
		t.Helper()
		raw, err := protocol.EncodeGame(protocol.ClientToServer, 1, opcode, seq, body, false)
		if err != nil {
			t.Fatal(err)
		}
		c.SetWriteDeadline(time.Now().Add(3 * time.Second))
		if _, err = c.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	open := func(i int) net.Conn {
		t.Helper()
		c, err := net.DialTimeout("tcp4", addresses[i], 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		read(c, 0, 1, 0)
		login := read(c, 1, 1, 0)
		want, err := protocol.Login(10019, 1)
		if err != nil || !bytes.Equal(login, want) {
			t.Fatal("town login advertised an unexpected player endpoint")
		}
		if savedSettings[i] {
			block := binary.LittleEndian.AppendUint32(nil, uint32(len(options[i])))
			if b := read(c, 0, 173, 0); !bytes.Equal(b, append(append(block, options[i]...), make([]byte, 8)...)) {
				t.Fatal("reconnect restored different account game options")
			}
			block = binary.LittleEndian.AppendUint32([]byte{0}, uint32(len(hotkeys[i])))
			if b := read(c, 0, 440, 0); !bytes.Equal(b, append(block, hotkeys[i]...)) {
				t.Fatal("reconnect restored different account hotkeys")
			}
		}
		return c
	}
	checkRoster := func(b []byte, i int) {
		t.Helper()
		if len(b) < 2 || b[1] != 1 || !bytes.Contains(b, []byte(names[i])) || bytes.Contains(b, []byte(names[1-i])) {
			t.Fatal("one-character roster did not match its authenticated owner")
		}
	}
	create := func(c net.Conn, name string, seq uint16) {
		t.Helper()
		body := binary.LittleEndian.AppendUint32([]byte{0}, uint32(len(name)))
		send(c, 5, seq, append(body, name...))
	}
	list := []byte{0xff, 0xff, 2}
	empty, err := protocol.Roster(nil, 32)
	if err != nil {
		t.Fatal(err)
	}
	for i := range players {
		c := open(i)
		connections[i] = c
		send(c, 8, 1, list)
		if !bytes.Equal(read(c, 0, 2, 1), empty) {
			t.Fatal("live test requires freshly issued empty accounts")
		}
	}
	for i, c := range connections {
		create(c, names[i], 2)
	}
	for i, c := range connections {
		ack := read(c, 1, 5, 2)
		if len(ack) < 7 || ack[0] != 1 {
			t.Fatalf("creation failed for player %d: %x", i+1, ack)
		}
		ids[i] = binary.LittleEndian.Uint16(ack[1:])
		expect, err := protocol.CreateAccepted(ids[i], names[i])
		if err != nil || !bytes.Equal(ack, expect) {
			t.Fatal("invalid native create reply")
		}
		checkRoster(read(c, 0, 2, 2), i)
	}
	create(connections[1], names[0], 3)
	if b := read(connections[1], 1, 5, 3); !bytes.Equal(b, []byte{0, 18}) {
		t.Fatalf("duplicate name reply %x", b)
	}
	for _, c := range connections {
		send(c, 4, 4, []byte{0, 0})
	}
	checkSelected := func(c net.Conn, i int, seq uint16) {
		t.Helper()
		b := read(c, 1, 4, seq)
		if len(b) < 11 || b[0] != 1 || binary.LittleEndian.Uint16(b[9:]) != ids[i] {
			t.Fatal("selected character differs from created ID")
		}
		if b = read(c, 0, 1378, seq); !bytes.Equal(b, []byte{1, 0, 0, 0}) {
			t.Fatalf("unexpected selected level %x", b)
		}
		// The matched PVF sends the five original daily limits immediately after N1378.
		if b = read(c, 0, 1734, seq); hex.EncodeToString(b) != "0502e1f50506b0e1f50503b1e1f50503b2e1f50502b3e1f50502" {
			t.Fatalf("unexpected new-character daily limits %x", b)
		}
	}
	for i, c := range connections {
		checkSelected(c, i, 4)
	}
	ping := func(c net.Conn, seq uint16) {
		t.Helper()
		tick := binary.LittleEndian.AppendUint32(make([]byte, 13), uint32(seq))
		send(c, 1715, seq, tick)
		if b := read(c, 1, 1715, seq); !bytes.Equal(b, binary.LittleEndian.AppendUint32([]byte{1}, uint32(seq))) {
			t.Fatal("native ping did not match")
		}
	}
	for i, c := range connections {
		send(c, 197, 5, append(binary.LittleEndian.AppendUint32(nil, uint32(len(options[i]))), options[i]...))
		send(c, 198, 6, append(binary.LittleEndian.AppendUint32(nil, uint32(len(hotkeys[i]))), hotkeys[i]...))
		ping(c, 7)
		savedSettings[i] = true
	}
	for i := range players {
		connections[i].Close()
		ping(connections[1-i], uint16(10+i))
		c := open(i)
		connections[i] = c
		send(c, 8, 1, list)
		checkRoster(read(c, 0, 2, 1), i)
		send(c, 4, 2, []byte{0, 0})
		checkSelected(c, i, 2)
	}
	// Simulate a second login while the previous TCP connection is still open.
	replacement := open(0)
	send(replacement, 8, 1, list)
	checkRoster(read(replacement, 0, 2, 1), 0)
	connections[0].SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := protocol.ReadFrame(connections[0], protocol.Game, protocol.ServerToClient); !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("previous player connection did not close after replacement: %v", err)
	}
	ping(connections[1], 12)
	t.Log("Two concurrent accounts completed C5, isolated C8, C4, N1378 and N1734. Duplicate C5 returned 00 12. Reconnecting restored each player's C197/C198 data in N173/N440. Replacing a live session preserved the other player's C1715.")
}
