package mailwire

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"s9a1/server/internal/protocol"
)

func TestStrictRecipientCodePage(t *testing.T) {
	// Fingerprint of U16 LE words, in lead/trail byte order, from the .NET
	// CodePagesEncodingProvider for CP936 with exception fallbacks.
	var words []byte
	for i := range 126 * 190 {
		pair := binary.BigEndian.AppendUint16(nil, pairAt(i))
		got, err := decode936(pair)
		if err != nil {
			t.Fatal(err)
		}
		back, err := encode936(got)
		if err != nil || !bytes.Equal(back, pair) {
			t.Fatalf("CP936 round-trip %x returned %x (%v)", pair, back, err)
		}
		for _, r := range got {
			words = binary.LittleEndian.AppendUint16(words, uint16(r))
		}
	}
	digest := sha256.Sum256(words)
	if hex.EncodeToString(digest[:]) != "30fbe8bd730fb9b3f176d640261c77be60c102c5d7ccb89f96e510ea000cfe42" {
		t.Fatal("CP936 differs from original .NET index")
	}
	for _, p := range []struct {
		bytes []byte
		name  string
	}{{[]byte{0x80}, "€"}, {[]byte{0xff}, "\uf8f5"}, {[]byte{0xa2, 0xe3}, "\ue76c"}} {
		got, err := decode936(p.bytes)
		if err != nil || got != p.name {
			t.Fatal("incorrect strict CP936 character")
		}
		b, err := encode936(p.name)
		if err != nil || !bytes.Equal(b, p.bytes) {
			t.Fatal("incorrect strict CP936 encoding")
		}
	}
	b, err := RecipientAccepted("测试", 55, 0, 1)
	if err != nil || hex.EncodeToString(b) != "0104000000b2e2cad40637000001000000" {
		t.Fatal("original synthetic upstream C324 success fields differ")
	}
}

func TestOriginalRecipientQueries(t *testing.T) {
	f, err := os.Open("testdata/recipient-legacy.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var original struct {
		Records []struct{ Name, Request, Response string }
	}
	if err = json.NewDecoder(reader).Decode(&original); err != nil {
		t.Fatal(err)
	}
	if len(original.Records) != 21 {
		t.Fatal("incomplete original C324 transcript")
	}
	for _, e := range original.Records {
		req, err := hex.DecodeString(e.Request)
		if err != nil {
			t.Fatal(err)
		}
		p, err := protocol.DecodeFrame(req, protocol.Game, protocol.ClientToServer)
		if err != nil {
			t.Fatal(err)
		}
		request, err := ReadRecipient(p.Body)
		var body []byte
		switch e.Name {
		case "sameAccount", "asciiCase":
			if err != nil {
				t.Fatal(err)
			}
			target := "大厅新角"
			if e.Name == "asciiCase" {
				target = "MailCase甲"
				if request.Name != "mAILcASE甲" {
					t.Fatal("request case was lost")
				}
			}
			body, err = RecipientAccepted(target, 1, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
		case "self", "unknown", "foreign", "GBK80", "GBKFF", "privatePair":
			if err != nil {
				t.Fatalf("%s parser rejected valid GBK", e.Name)
			}
			body = []byte{0, 21, 0}
			if e.Name == "self" {
				body[1] = 7
			}
		case "authenticated", "roster", "selected", "townLoading", "returned":
			if err != nil {
				t.Fatal(err)
			}
			body = []byte{0, 2, 0}
		case "empty", "mode", "whitespace":
			if err != nil || request.Ordinary() {
				t.Fatalf("%s should parse and then fail the ordinary mail rule", e.Name)
			}
			body = []byte{0, 2, 0}
		default:
			if err == nil {
				t.Fatalf("%s accepted unsupported native request", e.Name)
			}
			body = []byte{0, 2, 0}
		}
		wire, err := protocol.EncodeGame(protocol.ServerToClient, 1, 324, p.Sequence, body, false)
		if err != nil || hex.EncodeToString(wire) != e.Response {
			t.Fatalf("%s did not reproduce original frame", e.Name)
		}
	}
}
