package pvf

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const (
	regionSize = 0xA00000
	prefixSize = 0x2800
)

// Decode only the authorized wrapped prefixes. The remaining archive bytes
// stay in the original file and are read by offset.
func envelopePrefixes(f *os.File, size int64, path, profilePath string) ([][]byte, error) {
	if profilePath == "" {
		return nil, errors.New("wrapped PVF requires an explicit external Client100 profile")
	}
	profile, err := readSmallFile(profilePath, 65536)
	if err != nil {
		return nil, err
	}
	var cfg struct{ RSAPem, MasterKey string }
	if err = json.Unmarshal(profile, &cfg); err != nil {
		return nil, errors.New("invalid external Client100 profile")
	}
	der, _ := pem.Decode([]byte(cfg.RSAPem))
	if der == nil {
		return nil, errors.New("invalid Client100 RSA profile")
	}
	var key *rsa.PrivateKey
	switch der.Type {
	case "PRIVATE KEY":
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(der.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(der.Bytes)
	}
	if err != nil || key == nil {
		return nil, errors.New("invalid Client100 RSA profile")
	}
	sidecar, err := readSmallFile(filepath.Join(filepath.Dir(path), "sk.dat"), 65536)
	if err != nil {
		return nil, err
	}
	block := key.Size()
	if len(sidecar) == 0 || len(sidecar)%block != 0 {
		return nil, errors.New("invalid Client100 sk.dat size")
	}
	keys := make([]byte, 0, len(sidecar))
	for off := 0; off < len(sidecar); off += block {
		p, err := rsa.DecryptPKCS1v15(rand.Reader, key, sidecar[off:off+block])
		if err != nil {
			return nil, errors.New("Client100 profile and sk.dat do not match")
		}
		keys = append(keys, p...)
	}
	master, err := hex.DecodeString(cfg.MasterKey)
	if err != nil {
		return nil, errors.New("invalid Client100 AES profile")
	}
	c, err := aes.NewCipher(master)
	if err != nil {
		return nil, errors.New("invalid Client100 AES profile")
	}
	var iv [aes.BlockSize]byte
	if n := len(keys) / 256 * 256; n != 0 {
		cipher.NewCBCDecrypter(c, iv[:]).CryptBlocks(keys[:n], keys[:n])
	}
	if len(keys) == 0 || len(keys)%32 != 0 {
		return nil, errors.New("invalid Client100 region key table")
	}
	prefixes := make([][]byte, len(keys)/32)
	last := int64(len(prefixes)-1) * regionSize
	if size < last+prefixSize || size > last+regionSize {
		return nil, errors.New("PVF length differs from the Client100 sk.dat region table")
	}
	for i := range prefixes {
		p := make([]byte, prefixSize)
		if _, err = f.ReadAt(p, int64(i)*regionSize); err != nil {
			return nil, err
		}
		block, err := aes.NewCipher(keys[i*32 : (i+1)*32])
		if err != nil {
			return nil, err
		}
		cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(p, p)
		prefixes[i] = p
	}
	return prefixes, nil
}

func readSmallFile(path string, maximum int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err == nil && (len(b) == 0 || int64(len(b)) > maximum) {
		err = errors.New("invalid Client100 profile or sidecar length")
	}
	return b, err
}
