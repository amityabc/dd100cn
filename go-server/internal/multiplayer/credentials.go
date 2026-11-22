package multiplayer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func validAccount(account string) bool {
	return len(account) >= 1 && len(account) <= 64 && utf8.ValidString(account) && strings.IndexFunc(account, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) < 0
}

// The raw host's loopback listeners own the legacy local account exclusively.
func reservedAccount(account string) bool { return strings.EqualFold(account, "local") }

func readJSON(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing JSON data")
	}
	return nil
}

func readAccounts(path string) (map[string]string, error) {
	var accounts map[string]string
	if err := readJSON(path, &accounts); err != nil {
		return nil, err
	}
	if accounts == nil {
		return nil, errors.New("invalid account registry")
	}
	for account, digest := range accounts {
		value, err := hex.DecodeString(digest)
		if !validAccount(account) || err != nil || len(value) != sha256.Size {
			return nil, errors.New("invalid gateway account registry entry")
		}
	}
	return accounts, nil
}

type endpointSettings struct {
	Gateway string `json:"gateway"`
}

func newFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

// Initialize creates new operator-owned trust material. Player files trust only the issued gateway certificate.
func Initialize(dir, endpoint, listen string) error {
	host, port, err := net.SplitHostPort(endpoint)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || host == "" || n < 1 || n > 65535 {
		return errors.New("gateway requires the hostname or IP and port players will use")
	}
	if listen == "" {
		listen = endpoint
	}
	if _, err := net.ResolveTCPAddr("tcp", listen); err != nil {
		return fmt.Errorf("invalid gateway listen address: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return fmt.Errorf("new gateway directory: %w", err)
	}
	created := []string{}
	finished := false
	defer func() {
		if !finished {
			for _, name := range created {
				os.Remove(name)
			}
			os.Remove(dir)
		}
	}()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(2, 0, 0), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	setting, err := json.Marshal(endpointSettings{endpoint})
	if err != nil {
		return err
	}
	server, err := json.MarshalIndent(struct{ Listen, CertFile, KeyFile, AccountsFile string }{listen, "gateway-cert.pem", "gateway-key.pem", "accounts.json"}, "", "  ")
	if err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"gateway-key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0600},
		{"gateway-cert.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644},
		{"accounts.json", []byte("{}\n"), 0600}, {"endpoint.json", append(setting, '\n'), 0600},
		{"gateway.json", append(server, '\n'), 0600},
	}
	for _, entry := range files {
		path := filepath.Join(dir, entry.name)
		if err := newFile(path, entry.data, entry.mode); err != nil {
			return err
		}
		created = append(created, path)
	}
	finished = true
	return nil
}

// Issue writes one private player file; only its SHA-256 token digest is stored in the server's registry.
func Issue(dir, account, output string) error {
	if reservedAccount(account) {
		return errors.New("local is reserved for the host's direct loopback connections")
	}
	if !validAccount(account) {
		return errors.New("account must be 1 to 64 UTF-8 bytes without whitespace or control characters")
	}
	lock := filepath.Join(dir, "accounts.lock")
	if err := newFile(lock, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return fmt.Errorf("account registry in use or unavailable: %w", err)
	}
	defer os.Remove(lock)
	path := filepath.Join(dir, "accounts.json")
	accounts, err := readAccounts(path)
	if err != nil {
		return err
	}
	if _, exists := accounts[account]; exists {
		return errors.New("account already issued")
	}
	var settings endpointSettings
	if err := readJSON(filepath.Join(dir, "endpoint.json"), &settings); err != nil {
		return err
	}
	cert, err := os.ReadFile(filepath.Join(dir, "gateway-cert.pem"))
	if err != nil {
		return err
	}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		return err
	}
	fprint := sha256.Sum256(token[:])
	accounts[account] = hex.EncodeToString(fprint[:])
	credentials, err := json.MarshalIndent(PlayerConfig{Gateway: settings.Gateway, Account: account, Token: base64.RawURLEncoding.EncodeToString(token[:]), ServerCertificate: string(cert)}, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return err
	}
	if err = newFile(output, append(credentials, '\n'), 0600); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			os.Remove(output)
		}
	}()
	replacement, err := os.CreateTemp(dir, ".accounts-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(replacement.Name())
	if err = json.NewEncoder(replacement).Encode(accounts); err == nil {
		err = replacement.Sync()
	}
	if closeErr := replacement.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(replacement.Name(), path); err != nil {
		return err
	}
	committed = true
	return nil
}
