package protocol

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

func fixture(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "protocol", name))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}
func fromHex(t *testing.T, value string) []byte {
	t.Helper()
	b, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func equalWire(t *testing.T, name string, got []byte, err error, want []byte) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !bytes.Equal(got, want) {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Fatalf("%s differs at byte %d (got %d bytes, want %d)", name, i, len(got), len(want))
	}
}

func TestLegacyPublicFrames(t *testing.T) {
	var src struct {
		Frames []struct {
			Name, Transport, Direction, FrameHex string
			Class                                byte
			Opcode, Sequence                     uint16
			Compressed                           bool
		}
	}
	fixture(t, "public-frames.json", &src)
	if len(src.Frames) != 55 {
		t.Fatalf("missing public vectors, got %d", len(src.Frames))
	}
	for _, row := range src.Frames {
		raw := fromHex(t, row.FrameHex)
		kind, d := Game, ClientToServer
		if row.Transport == "Channel" {
			kind = Channel
		}
		if row.Direction == "S2C" {
			d = ServerToClient
		}
		p, err := DecodeFrame(raw, kind, d)
		if err != nil {
			t.Fatalf("%s: %v", row.Name, err)
		}
		if p.Class != row.Class || p.Opcode != row.Opcode || p.Sequence != row.Sequence || p.Compressed != row.Compressed {
			t.Fatalf("header differs: %s", row.Name)
		}
		var out []byte
		if kind == Game {
			out, err = EncodeGame(d, p.Class, p.Opcode, p.Sequence, p.Body, p.Compressed)
		} else {
			out, err = EncodeChannel(d, byte(p.Opcode), p.Body)
		}
		equalWire(t, row.Name, out, err, raw)
	}
}

func TestSplitAndJoinedFrames(t *testing.T) {
	raw, _ := EncodeGame(ClientToServer, 1, 8, 19, []byte{255, 255, 2}, false)
	r := iotest.OneByteReader(bytes.NewReader(bytes.Repeat(raw, 2)))
	for i := 0; i < 2; i++ {
		f, err := ReadFrame(r, Game, ClientToServer)
		equalWire(t, "stream", f.Raw, err, raw)
	}
	if _, err := ReadFrame(r, Game, ClientToServer); !errors.Is(err, io.EOF) {
		t.Fatalf("complete stream: %v", err)
	}
	if _, err := ReadFrame(bytes.NewReader(raw[:13]), Game, ClientToServer); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial body: %v", err)
	}
	raw[len(raw)-1] ^= 1
	if _, err := DecodeFrame(raw, Game, ClientToServer); err == nil {
		t.Fatal("accepted damaged checksum")
	}
	ch := make([]byte, 11)
	little.PutUint32(ch[2:], 4097)
	if _, err := ReadFrame(bytes.NewReader(ch), Channel, ClientToServer); err == nil || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("unbounded channel header: %v", err)
	}
}

type legacyTranscript struct {
	GamePort      uint16
	Greeting      []string
	Directory     []ChannelEntry
	Channel, Game []struct {
		Name, Request, Response string
		Closed                  bool
	}
}

func TestOriginalHostChannelExchange(t *testing.T) {
	var v legacyTranscript
	fixture(t, "legacy-empty.json", &v)
	keyFrame := fromHex(t, v.Channel[0].Response)
	key := string(keyFrame[15:29])
	day, err := time.Parse("20060102", key[:8])
	if err != nil {
		t.Fatal(err)
	}
	encodedScript := fromHex(t, v.Channel[1].Response)
	padded, err := UnprotectChannel(encodedScript[11:], key)
	if err != nil {
		t.Fatal(err)
	}
	script := bytes.TrimRight(padded, "\x00")
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "s9a1", "Server", "Data", "channel_info.etc"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := ParseChannelCatalog(source, true)
	equalWire(t, "upstream original channel settings projected into S10", catalog.Script, err, script)
	if len(v.Directory) != 4 || v.Directory[0].ID != 19 || v.Directory[0].Port != v.GamePort {
		t.Fatal("incomplete captured directory")
	}
	var session ChannelSession
	for _, e := range v.Channel {
		req, err := DecodeFrame(fromHex(t, e.Request), Channel, ClientToServer)
		if err != nil {
			t.Fatal(err)
		}
		got, err := session.Reply(req, day, script, v.Directory)
		equalWire(t, "legacy protected channel reply", got, err, fromHex(t, e.Response))
	}
	if _, err := Expand(append(bytes.Clone(encodedScript[11:]), 0), MaximumFrameBytes); err == nil {
		t.Fatal("accepted trailing zlib member data")
	}
	if _, err := Expand(encodedScript[11:], 1); err == nil {
		t.Fatal("ignored expansion limit")
	}
}

func TestOriginalHostEntryExchange(t *testing.T) {
	var v legacyTranscript
	fixture(t, "legacy-empty.json", &v)
	// N1 contains a clock and random material. Extract just the two values
	// from the captured original message; reconstruct and compare every byte.
	first, err := DecodeFrame(fromHex(t, v.Greeting[0]), Game, ServerToClient)
	if err != nil {
		t.Fatal(err)
	}
	data := first.Body[4:]
	var material []byte
	var stamp int64
	for len(data) > 0 {
		key, n := binary.Uvarint(data)
		if n <= 0 {
			t.Fatal("N1 key")
		}
		data = data[n:]
		val, n := binary.Uvarint(data)
		if n <= 0 {
			t.Fatal("N1 wire value")
		}
		data = data[n:]
		switch key & 7 {
		case 0:
			if key>>3 == 11 {
				stamp = int64(val)
			}
		case 2:
			if val > uint64(len(data)) {
				t.Fatal("N1 truncated field")
			}
			if key>>3 == 3 {
				material = data[:val]
			}
			data = data[val:]
		default:
			t.Fatalf("N1 unknown wire type for %d", key)
		}
	}
	body, err := ChannelInformation(19, stamp, material)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := EncodeGame(ServerToClient, 0, 1, 0, body, false)
	equalWire(t, "native greeting", packet, err, first.Raw)
	body, err = Login(v.GamePort, 1)
	if err != nil {
		t.Fatal(err)
	}
	packet, err = EncodeGame(ServerToClient, 1, 1, 0, body, false)
	equalWire(t, "automatic login", packet, err, fromHex(t, v.Greeting[1]))
	for _, e := range v.Game {
		req, err := DecodeFrame(fromHex(t, e.Request), Game, ClientToServer)
		if err != nil {
			t.Fatal(err)
		}
		op, class := req.Opcode, byte(1)
		switch e.Name {
		case "roster":
			if err = CheckListRequest(req.Body); err != nil {
				t.Fatal(err)
			}
			class, op = 0, 2
			body, err = Roster(nil, 32)
		case "emptyRosterProbe":
			if err = CheckEmptyRosterProbe(req.Body); err != nil {
				t.Fatal(err)
			}
			body = Failure(0)
		case "ping":
			body, err = Ping(req.Body)
		case "availableName", "oneCharacterName":
			name, parseErr := ReadNameRequest(req.Body)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if name == "测试" {
				body = Ack()
			} else if name == "测" {
				body = Failure(23)
			} else {
				t.Fatalf("C692 decoded %q", name)
			}
		case "absentSelectIndex":
			index, parseErr := ReadSelectRequest(req.Body)
			if parseErr != nil || index != 0 || !e.Closed {
				t.Fatal("invalid selection fixture")
			}
			continue // Host owns the phase rejection and socket close.
		default:
			t.Fatalf("unexpected original-host case %q", e.Name)
		}
		if err != nil {
			t.Fatal(err)
		}
		packet, err = EncodeGame(ServerToClient, class, op, req.Sequence, body, false)
		equalWire(t, e.Name, packet, err, fromHex(t, e.Response))
	}
}

func TestCharacterBodiesFromOldAssembly(t *testing.T) {
	var v map[string]string
	fixture(t, "entry-bodies.json", &v)
	w := Equipment{Slot: 12, TemplateID: 701234, QualitySeed: 87654, Durability: 98, Raw119: make([]byte, 119)}
	c := RosterCharacter{ID: 7, Name: "测试", Job: 12, GrowType: 2, Level: 57, Worn: []Equipment{w}}
	b, err := Roster([]RosterCharacter{c}, 32)
	equalWire(t, "roster", b, err, fromHex(t, v["roster"]))
	b, err = CreateAccepted(7, "测试")
	equalWire(t, "create", b, err, fromHex(t, v["created"]))
	selected := SelectCharacter{ID: 7, Created: 1234567, FatigueUsed: 8, FatigueLimit: 156, TutorialDone: true}
	b, err = Select(selected, 10, 22, 0, 700, true)
	equalWire(t, "select native", b, err, fromHex(t, v["selectNative"]))
	b, err = Select(selected, 38, 0, 0, 0, false)
	equalWire(t, "select legacy", b, err, fromHex(t, v["selectLegacy"]))
	a := Actor{RosterCharacter: c, Stats: make([]byte, StatsBytes), Experience: 1234, Town: 38, Area: 1, X: 450, Y: 234, Direction: 5, AreaState: 3, FatigueUsed: 8, FatigueLimit: 156, SkillStyle: 255}
	a.Stats[84] = 100
	b, err = Mode0(a, false)
	equalWire(t, "mode0", b, err, fromHex(t, v["mode0"]))
	b, err = Mode0(a, true)
	equalWire(t, "mode0Native140", b, err, fromHex(t, v["mode0Native140"]))
	b, err = Mode1(a, false)
	equalWire(t, "mode1", b, err, fromHex(t, v["mode1"]))
	b, err = Mode1(a, true)
	equalWire(t, "mode1Full", b, err, fromHex(t, v["mode1Full"]))
	b, err = Mode3(a)
	equalWire(t, "mode3", b, err, fromHex(t, v["mode3"]))
	for key, got := range map[string][]byte{"position": Position(a), "area": Area(a), "town": Town(a), "fatigue": Fatigue(a)} {
		equalWire(t, key, got, nil, fromHex(t, v[key]))
	}
	options := make([]byte, 372)
	options[0], options[371] = 255, 128
	saved, err := RestoreGameOptions(options)
	equalWire(t, "game options", saved, err, fromHex(t, v["gameOptions"]))
	b, err = ReadGameOptions(saved[:376])
	equalWire(t, "parse game options", b, err, options)
	keys := []byte{0, 128, 255, 1, 0}
	saved, err = RestoreHotkeys(keys)
	equalWire(t, "hotkeys", saved, err, fromHex(t, v["hotkeys"]))
	b, err = ReadHotkeys(saved[1:])
	equalWire(t, "parse hotkeys", b, err, keys)
}

func TestStrictCodePage54936(t *testing.T) {
	for _, x := range []struct{ raw, name string }{{"b2e2cad4", "测试"}, {"a8bca6d9", "\ue7c7\ue78d"}, {"a2e341", "€A"}, {"8431a43741", "�A"}, {"90308130", "𐀀"}} {
		want := fromHex(t, x.raw)
		b, err := encodeName(x.name)
		equalWire(t, "CP54936 encode", b, err, want)
		name, err := decodeName(want)
		if err != nil || name != x.name {
			t.Fatalf("decode %s: %q %v", x.raw, name, err)
		}
	}
	for _, raw := range []string{"80", "ff", "8130", "813a8130", "8431a530", "e3329a36"} {
		if _, err := decodeName(fromHex(t, raw)); err == nil {
			t.Fatalf("accepted invalid %s", raw)
		}
	}
}

func TestConcurrentCompressionBounds(t *testing.T) {
	if _, err := CompressPayload(make([]byte, MaximumFrameBytes+1)); err == nil {
		t.Fatal("oversized zlib input accepted")
	}
	samples := [][]byte{nil, []byte("频道"), bytes.Repeat([]byte{0xfe, 0, 43, 0}, MaximumFrameBytes/4)}
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Go(func() {
			s := samples[i%len(samples)]
			b, err := CompressPayload(s)
			if err != nil {
				t.Error(err)
				return
			}
			if len(s) == 0 {
				if len(b) != 0 {
					t.Errorf("empty .NET stream wrote %d bytes", len(b))
				}
				return
			}
			decoded, err := Expand(b, MaximumFrameBytes)
			if err != nil || !bytes.Equal(s, decoded) {
				t.Errorf("concurrent compression of %d bytes: %v", len(s), err)
			}
		})
	}
	group.Wait()
}

func TestOriginalRuntimeCompressionDigests(t *testing.T) {
	var v struct {
		Vectors []struct {
			Size, Compressed int
			Mixed            bool
			SHA256           string
		}
	}
	fixture(t, "compression-hashes.json", &v)
	for _, x := range v.Vectors {
		input := make([]byte, x.Size)
		var state uint32 = 0x536a100
		for i := range input {
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			if !x.Mixed || i%257 >= 158 {
				input[i] = byte(state)
			}
		}
		b, err := CompressPayload(input)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(b)
		if len(b) != x.Compressed || !bytes.Equal(digest[:], fromHex(t, x.SHA256)) {
			t.Errorf("native .NET %d bytes (mixed %t) expected length %d and %s, got %d and %x", x.Size, x.Mixed, x.Compressed, x.SHA256, len(b), digest)
		}
	}
}

func TestOriginalHostTownActors(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "protocol", "legacy-town.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	type action struct {
		Action    string
		Responses []string
		State     []struct {
			ID                                       uint16
			Name                                     string
			Job, Level, Town, Area, Direction, State byte
			X, Y                                     uint16
			StatsJSON                                string `json:"stats_json"`
			GameplayJSON                             string `json:"gameplay_json"`
		}
	}
	var transcript struct {
		NativeTownProfile140 bool
		Opening, Town        []action
	}
	if err := json.NewDecoder(z).Decode(&transcript); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, phase := range [][]action{transcript.Opening, transcript.Town} {
		for _, evt := range phase {
			if len(evt.State) != 1 {
				continue
			}
			row := evt.State[0]
			var stat struct{ Values map[string]int64 }
			var progress struct {
				Skills      []ActorSkill
				Progression struct {
					GrowType              byte
					Experience            uint64
					Fatigue, FatigueLimit uint16
				}
			}
			if err := json.Unmarshal([]byte(row.StatsJSON), &stat); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(row.GameplayJSON), &progress); err != nil {
				t.Fatal(err)
			}
			stats, err := Stats(stat.Values)
			if err != nil {
				t.Fatal(err)
			}
			p := progress.Progression
			a := Actor{RosterCharacter: RosterCharacter{ID: row.ID, Name: row.Name, Job: row.Job, Level: row.Level, GrowType: p.GrowType}, Stats: stats, Skills: progress.Skills, SkillStyle: 255,
				Experience: p.Experience, Channel: 19, Town: row.Town, Area: row.Area, Direction: row.Direction, AreaState: row.State, X: row.X, Y: row.Y, FatigueUsed: p.FatigueLimit - p.Fatigue, FatigueLimit: p.FatigueLimit}
			for _, wire := range evt.Responses {
				pkt, err := DecodeFrame(fromHex(t, wire), Game, ServerToClient)
				if err != nil {
					t.Fatal(err)
				}
				if pkt.Class != 0 {
					continue
				}
				var b []byte
				switch pkt.Opcode {
				case 356:
					if !pkt.Compressed {
						t.Fatal("N356 lost marker16")
					}
					var plain []byte
					plain, err = Expand(pkt.Body, MaximumFrameBytes)
					if err == nil {
						b, err = CompressPayload(plain)
					}
				case 2:
					switch pkt.Body[0] {
					case 0:
						b, err = Mode0(a, transcript.NativeTownProfile140)
					case 1:
						b, err = Mode1(a, true)
					default:
						continue
					}
				case 22:
					b = Position(a)
				case 23:
					b = Area(a)
				case 24:
					b = Town(a)
				case 36:
					b = Fatigue(a)
				default:
					continue
				}
				equalWire(t, evt.Action, b, err, pkt.Body)
				checked++
			}
		}
	}
	if checked < 15 {
		t.Fatalf("only %d original actor notices checked", checked)
	}
}
