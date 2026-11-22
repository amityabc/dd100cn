package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"s9a1/server/internal/config"
	"s9a1/server/internal/host"
	"s9a1/server/internal/protocol"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type runtimeFrameLog struct {
	mu       sync.Mutex
	file     *cappedLog
	full     bool
	runID    string
	started  time.Time
	disabled bool
}

func frameLogPath(c config.Config) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(c.Diagnostics.FrameMode))
	if mode == "" || mode == "off" {
		return "", nil
	}
	if mode != "metadata" && mode != "full" {
		return "", errors.New("Diagnostics:FrameMode must be off, metadata or full")
	}
	path := c.Path(c.Diagnostics.Frames)
	dir := filepath.Join(c.Root, "runtime", "logs")
	if !samePath(dir, filepath.Dir(path)) || filepath.Ext(path) != ".jsonl" {
		return "", errors.New("Diagnostics:Frames must name a JSONL file directly under runtime/logs")
	}
	return path, nil
}

func startFrameLog(c config.Config, runID string) (*runtimeFrameLog, error) {
	path, err := frameLogPath(c)
	if err != nil || path == "" {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err = rejectSourceLinks(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || isReparse(info)) {
		return nil, errors.New("frame output must be an owned regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := openCappedLog(path, 32*1024*1024)
	if err != nil {
		return nil, err
	}
	return &runtimeFrameLog{file: file, full: strings.EqualFold(strings.TrimSpace(c.Diagnostics.FrameMode), "full"), runID: runID, started: time.Now()}, nil
}

func (w *runtimeFrameLog) observe(p host.FrameObservation) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.disabled {
		return
	}
	wire := p.Frame
	kind, direction := "Game", "ClientToServer"
	if wire.Transport == protocol.Channel {
		kind = "Channel"
	}
	if wire.Direction == protocol.ServerToClient {
		direction = "ServerToClient"
	}
	var seq *uint16
	if wire.Transport == protocol.Game {
		seq = &wire.Sequence
	}
	var body, plain, encoding, decodeError *string
	var plainLen *int
	decodeStatus, rawHex := "Unavailable", ""
	switch {
	case p.Sensitive:
		reason := "authentication/bootstrap payload redacted"
		decodeError = &reason
	case !w.full:
		reason := "metadata-only frame capture"
		decodeError = &reason
	default:
		rawHex = strings.ToUpper(hex.EncodeToString(wire.Raw))
		h := strings.ToUpper(hex.EncodeToString(wire.Body))
		body = &h
		enc := "wire"
		decoded := wire.Body
		decodeStatus = "Decoded"
		if wire.Compressed {
			enc = "zlib"
			var err error
			decoded, err = protocol.Expand(wire.Body, protocol.MaximumFrameBytes)
			if err != nil {
				decodeStatus = "Invalid"
				msg := err.Error()
				decodeError = &msg
			}
		}
		encoding = &enc
		if decodeStatus == "Decoded" {
			h := strings.ToUpper(hex.EncodeToString(decoded))
			n := len(decoded)
			plain, plainLen = &h, &n
		}
	}
	ctx := map[string]any{"RunId": w.runID, "SessionId": p.Connection, "TraceId": strings.ReplaceAll(uuid.NewString(), "-", ""), "RequestTraceId": nil,
		"LocalEndpoint": p.LocalEndpoint, "RemoteEndpoint": p.RemoteEndpoint, "ProcessId": os.Getpid(), "ThreadId": 0,
		"MonotonicTimestamp": time.Since(w.started).Nanoseconds(), "MonotonicFrequency": int64(time.Second), "RequestClass": nil,
		"RequestOpcode": nil, "RequestSequence": nil, "Session": nil, "NativeThreadId": nil, "CurrentSession": nil, "PreviousSession": nil}
	line, err := json.Marshal(map[string]any{
		"Time": time.Now().UTC(), "Connection": p.Connection, "Transport": kind, "Direction": direction, "Class": wire.Class, "Opcode": wire.Opcode,
		"RawHex": rawHex, "Length": p.Length, "Outcome": p.Outcome, "Detail": nil, "Sequence": seq, "Compressed": wire.Compressed,
		"BodyHex": body, "BodyLength": p.BodyLength, "PlainBodyHex": plain, "PlainBodyLength": plainLen, "PlainBodyEncoding": encoding,
		"DecodeStatus": decodeStatus, "DecodeError": decodeError, "ParseError": nil, "Context": ctx, "Exception": nil,
	})
	if err == nil {
		_, err = w.file.Write(append(line, '\n'))
	}
	if err != nil {
		w.disabled = true
		slog.Warn("frame capture disabled after write failure", "error", err)
	}
}
