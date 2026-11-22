package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

const consoleQuota = 8 * 1024 * 1024
const eventsQuota = 16 * 1024 * 1024

type cappedLog struct {
	mu          sync.Mutex
	path        string
	f           *os.File
	size, limit int64
}

func openCappedLog(path string, limit int64) (*cappedLog, error) {
	w := &cappedLog{path: path, limit: limit}
	return w, w.open()
}

func (w *cappedLog) open() error {
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, info.Size()
	return nil
}

func (w *cappedLog) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if int64(len(b)) > w.limit {
		return 0, errors.New("runtime log record exceeds its quota")
	}
	if w.size+int64(len(b)) > w.limit {
		if err := w.f.Close(); err != nil {
			return 0, err
		}
		if err := os.Remove(w.path); err != nil {
			return 0, err
		}
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(b)
	w.size += int64(n)
	return n, err
}

func (w *cappedLog) Close() error {
	if w == nil || w.f == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

type lifecycleLog struct {
	mu                     sync.Mutex
	id, path               string
	eventRoot              string
	start                  time.Time
	events, stdout, stderr *cappedLog
	fail                   error
	record                 int64
	retainedBytes          int64
}

func newLifecycleLog(root, id string, bounded bool) (*lifecycleLog, error) {
	base := filepath.Join(root, "runtime")
	eventRoot := filepath.Join(base, "logs", "server")
	dir := filepath.Join(eventRoot, id)
	if err := os.MkdirAll(eventRoot, 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	l := &lifecycleLog{id: id, eventRoot: eventRoot, path: filepath.Join(dir, "events.jsonl"), start: time.Now()}
	exe, ticks, err := processIdentity(os.Getpid())
	if err != nil {
		return nil, err
	}
	if err = writeJSONAtomic(filepath.Join(dir, "run.json"), map[string]any{
		"Schema": 1, "RunId": id, "StartedUtc": l.start.UTC(), "ProcessId": os.Getpid(), "ProcessStartUtc": time.Unix(0, (ticks-621355968000000000)*100).UTC(),
		"ProcessPath": exe, "BaseDirectory": filepath.Dir(exe), "Framework": runtime.Version(), "HostAssembly": filepath.Base(exe),
		"MonotonicOrigin": 0, "MonotonicFrequency": int64(time.Second), "QueueCapacity": 0, "FlushIntervalMilliseconds": 0,
		"ThreadIdMeaning": "Go records can migrate between OS threads", "RetentionPool": "events", "RetentionLimitBytes": eventsQuota,
		"RuntimeLogTotalLimitBytes": 100 * 1024 * 1024, "Retention": "Complete event records stay in the bounded runtime events pool.",
		"Acceptance": "The Go writer appends records synchronously.", "LossBoundary": "The operating system controls durability of completed appends.",
	}); err != nil {
		return nil, err
	}
	l.events, err = openCappedLog(l.path, eventsQuota)
	if err != nil {
		return nil, err
	}
	if bounded {
		l.stdout, err = openCappedLog(filepath.Join(base, "local-server.stdout.log"), consoleQuota)
		if err == nil {
			l.stderr, err = openCappedLog(filepath.Join(base, "local-server.stderr.log"), consoleQuota)
		}
		if err == nil {
			_, err = fmt.Fprintf(l.stdout, "=== server launch %s ===\n", l.start.UTC().Format(time.RFC3339Nano))
		}
		if err != nil {
			l.Close()
			return nil, err
		}
	}
	if err = l.trimEvents(0); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func (l *lifecycleLog) trimEvents(needed int64) error {
	active := l.events.size + needed
	if active > eventsQuota {
		active = needed
	}
	dirs, err := os.ReadDir(l.eventRoot)
	if err != nil {
		return err
	}
	total := int64(0)
	type kept struct {
		path string
		size int64
		at   time.Time
	}
	var retired []kept
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == l.id || !completeHex(d.Name(), 32) {
			continue
		}
		p := filepath.Join(l.eventRoot, d.Name(), "events.jsonl")
		f, e := os.Lstat(p)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		if !f.Mode().IsRegular() || isReparse(f) {
			continue
		}
		retired = append(retired, kept{p, f.Size(), f.ModTime()})
		total += f.Size()
	}
	slices.SortFunc(retired, func(a, b kept) int { return a.at.Compare(b.at) })
	for _, f := range retired {
		if total+active <= eventsQuota {
			break
		}
		if err = os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		total -= f.size
	}
	l.retainedBytes = total
	return nil
}

func (l *lifecycleLog) failure() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fail
}

func (l *lifecycleLog) Close() error {
	if l == nil {
		return nil
	}
	return errors.Join(l.stdout.Close(), l.stderr.Close(), l.events.Close(), l.fail)
}

func (l *lifecycleLog) say(s string) error {
	var w io.Writer = os.Stdout
	if l.stdout != nil {
		w = l.stdout
	}
	_, err := fmt.Fprintln(w, s)
	return err
}

type lifecycleHandler struct {
	log   *lifecycleLog
	min   slog.Level
	attrs []slog.Attr
	group string
}

func (h lifecycleHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.min }
func (h lifecycleHandler) WithAttrs(a []slog.Attr) slog.Handler {
	h.attrs = append(slices.Clone(h.attrs), a...)
	return h
}
func (h lifecycleHandler) WithGroup(name string) slog.Handler {
	if h.group != "" {
		h.group += "."
	}
	h.group += name
	return h
}
func (h lifecycleHandler) Handle(_ context.Context, r slog.Record) error {
	l := h.log
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail != nil {
		return l.fail
	}
	l.record++
	type field struct {
		Key   string
		Value any
	}
	state := []field{}
	add := func(a slog.Attr) bool {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		value := a.Value.Resolve().Any()
		if err, ok := value.(error); ok {
			value = err.Error()
		}
		state = append(state, field{key, value})
		return true
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(add)
	name := ""
	for _, a := range state {
		if a.Key == "event" {
			name, _ = a.Value.(string)
			break
		}
	}
	level := "Information"
	switch {
	case r.Level >= slog.LevelError:
		level = "Error"
	case r.Level >= slog.LevelWarn:
		level = "Warning"
	case r.Level < slog.LevelInfo:
		level = "Debug"
	}
	line, err := json.Marshal(map[string]any{"Time": r.Time.UTC(), "RunId": l.id, "RecordId": l.record, "ProcessId": os.Getpid(),
		"ThreadId": 0, "NativeThreadId": nil, "MonotonicTimestamp": time.Since(l.start).Nanoseconds(), "MonotonicFrequency": int64(time.Second),
		"Level": level, "Category": "Serve100.Host", "EventId": 0, "EventName": name, "Message": r.Message, "State": state, "Scopes": []any{}, "Exception": nil})
	if err == nil {
		line = append(line, '\n')
		if l.retainedBytes+l.events.size+int64(len(line)) > eventsQuota {
			err = l.trimEvents(int64(len(line)))
		}
	}
	if err == nil {
		_, err = l.events.Write(line)
	}
	l.fail = err
	if err != nil {
		return err
	}
	var w io.Writer = os.Stderr
	if l.stderr != nil {
		w = l.stderr
	}
	message := strings.TrimRight(r.Message, "\r\n")
	for _, a := range state {
		if a.Key == "error" {
			message += fmt.Sprintf(" error=%q", a.Value)
			break
		}
	}
	_, err = io.WriteString(w, message+"\n")
	return err
}
