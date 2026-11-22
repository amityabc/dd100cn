package operator

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"s9a1/server/internal/filelock"
)

type MailEntry struct {
	Intent       Mail
	State        string
	Created      time.Time
	ResourceHash string
	Result       *string
}

type Outbox struct{ Root string }

func (b Outbox) directory() string { return filepath.Join(b.Root, "runtime", "gm-mail-outbox") }
func (b Outbox) path(id uuid.UUID) string {
	return filepath.Join(b.directory(), strings.ReplaceAll(id.String(), "-", "")+".json")
}

func validQueuedMail(m Mail) bool {
	return m.Account == "local" && m.OperationId != uuid.Nil && m.Character != 0 && m.TemplateId != 0 && m.Count > 0 && m.Count <= 1_000_000 && ValidReason(m.Reason)
}

func validResource(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

func (b Outbox) Read(id uuid.UUID) (*MailEntry, error) {
	f, err := os.Open(b.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil {
		return nil, err
	}
	if len(data) > 16384 {
		return nil, errors.New("Mail intent exceeds size limit")
	}
	var e MailEntry
	if err = json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	if !validQueuedMail(e.Intent) || e.Intent.OperationId != id || !validResource(e.ResourceHash) {
		return nil, errors.New("Mail intent or resource identity differs")
	}
	return &e, nil
}

func (b Outbox) Pending(visit func(MailEntry) bool) error {
	files, err := os.ReadDir(b.directory())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, f := range files {
		name := f.Name()
		if f.IsDir() || len(name) != 37 || !strings.EqualFold(name[32:], ".json") {
			continue
		}
		id, err := uuid.Parse(name[:32])
		if err != nil {
			continue
		}
		e, err := b.Read(id)
		if err != nil {
			return err
		}
		if e != nil && e.State == "Queued" && !visit(*e) {
			break
		}
	}
	return nil
}

func (b Outbox) lock() (*os.File, error) {
	dir := b.directory()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return filelock.Open(filepath.Join(dir, "writer.lock"))
}

func (b Outbox) save(e MailEntry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(data) > 16384 {
		return errors.New("Mail intent exceeds size limit")
	}
	path := b.path(e.Intent.OperationId)
	stage, err := os.CreateTemp(b.directory(), ".mail-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	if _, err = stage.Write(data); err == nil {
		err = stage.Sync()
	}
	if closeErr := stage.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(stage.Name(), path)
}

func (b Outbox) Queue(m Mail, hash string) (Reply, error) {
	if !validQueuedMail(m) || !validResource(hash) {
		return Reply{}, errors.New("Invalid mail intent")
	}
	lock, err := b.lock()
	if err != nil {
		return Reply{}, err
	}
	defer lock.Close()
	e, err := b.Read(m.OperationId)
	if err != nil {
		return Reply{}, err
	}
	if e != nil {
		if e.Intent != m || e.ResourceHash != hash {
			return Reply{}, errors.New("Mail operation conflicts with its saved intent")
		}
		if e.Result != nil {
			var r Reply
			err = json.Unmarshal([]byte(*e.Result), &r)
			return r, err
		}
	} else {
		n := 0
		err = b.Pending(func(e MailEntry) bool { n++; return n < 1000 })
		if err != nil {
			return Reply{}, err
		}
		if n >= 1000 {
			return Reply{}, errors.New("离线待投递邮件已达 1000 封，请先上线处理。")
		}
		if err = b.save(MailEntry{Intent: m, State: "Queued", Created: time.Now().UTC(), ResourceHash: hash}); err != nil {
			return Reply{}, err
		}
	}
	r := Message("Queued", m.OperationId, "离线投递已保存，目标角色进入城镇后自动送达邮箱。")
	r.ReadBack = true
	return r, nil
}

func (b Outbox) Complete(m Mail, state string, r Reply) error {
	lock, err := b.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	e, err := b.Read(m.OperationId)
	if err != nil || e == nil {
		return err
	}
	if e.Intent != m {
		return errors.New("Mail operation conflicts with its saved intent")
	}
	result, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s := string(result)
	e.State, e.Result = state, &s
	return b.save(*e)
}
