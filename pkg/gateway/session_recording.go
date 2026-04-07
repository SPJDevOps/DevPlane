package gateway

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
)

// SessionRecordingMode controls how much terminal data is persisted.
type SessionRecordingMode string

const (
	// SessionRecordingOff disables capture.
	SessionRecordingOff SessionRecordingMode = "off"
	// SessionRecordingMetadataOnly writes hashes, sizes, and timing only.
	SessionRecordingMetadataOnly SessionRecordingMode = "metadata"
	// SessionRecordingFull writes hashes plus payload bytes (base64).
	SessionRecordingFull SessionRecordingMode = "full"
)

// SessionRecordingConfig configures optional session capture output.
type SessionRecordingConfig struct {
	Enabled bool
	Mode    SessionRecordingMode
	Dir     string
}

// SessionMeta is immutable per terminal session and included in manifest output.
type SessionMeta struct {
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId,omitempty"`
	Subject   string `json:"subject,omitempty"`
	UserID    string `json:"userId,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Backend   string `json:"backend,omitempty"`
	Remote    string `json:"remote,omitempty"`
	StartedAt string `json:"startedAt"`
}

// SessionFrameRecord stores one directional WebSocket message observation.
type SessionFrameRecord struct {
	TimestampUTC string `json:"timestampUtc"`
	Seq          uint64 `json:"seq"`
	Direction    string `json:"direction"`
	MessageType  int    `json:"messageType"`
	PayloadBytes int    `json:"payloadBytes"`
	PayloadSHA   string `json:"payloadSha256"`
	PrevHash     string `json:"prevHash"`
	EntryHash    string `json:"entryHash"`
	PayloadB64   string `json:"payloadBase64,omitempty"`
}

// SessionManifest provides immutable closeout metadata for exported records.
type SessionManifest struct {
	SchemaVersion string      `json:"schemaVersion"`
	Mode          string      `json:"mode"`
	Meta          SessionMeta `json:"meta"`
	FrameCount    uint64      `json:"frameCount"`
	FinalHash     string      `json:"finalHash"`
	ClosedAt      string      `json:"closedAt"`
	CloseReason   string      `json:"closeReason,omitempty"`
}

type SessionRecorder struct {
	log   logr.Logger
	cfg   SessionRecordingConfig
	meta  SessionMeta
	file  *os.File
	enc   *json.Encoder
	seq   uint64
	prev  string
	mutex sync.Mutex
}

// SessionRecordingConfigFromEnv reads gateway session recording controls.
// SESSION_RECORDING_ENABLED=true enables capture.
// SESSION_RECORDING_MODE in {metadata,full}; default metadata.
// SESSION_RECORDING_DIR defaults to /tmp/devplane-sessions.
func SessionRecordingConfigFromEnv() SessionRecordingConfig {
	enabled := strings.EqualFold(strings.TrimSpace(os.Getenv("SESSION_RECORDING_ENABLED")), "true")
	mode := SessionRecordingMetadataOnly
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SESSION_RECORDING_MODE"))) {
	case "", "metadata":
		mode = SessionRecordingMetadataOnly
	case "full":
		mode = SessionRecordingFull
	case "off":
		mode = SessionRecordingOff
	default:
		mode = SessionRecordingMetadataOnly
	}
	dir := strings.TrimSpace(os.Getenv("SESSION_RECORDING_DIR"))
	if dir == "" {
		dir = "/tmp/devplane-sessions"
	}
	if !enabled {
		mode = SessionRecordingOff
	}
	return SessionRecordingConfig{
		Enabled: enabled && mode != SessionRecordingOff,
		Mode:    mode,
		Dir:     dir,
	}
}

// NewSessionRecorder creates a hash-chained recorder. On setup failure, it logs
// and returns nil so terminal traffic is never blocked by recording failures.
func NewSessionRecorder(log logr.Logger, cfg SessionRecordingConfig, meta SessionMeta) *SessionRecorder {
	if !cfg.Enabled || cfg.Mode == SessionRecordingOff {
		return nil
	}
	if meta.SessionID == "" {
		meta.SessionID = uuid.NewString()
	}
	if meta.StartedAt == "" {
		meta.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		log.Error(err, "session recording disabled: failed to create directory", "dir", cfg.Dir)
		return nil
	}
	recordPath := filepath.Join(cfg.Dir, fmt.Sprintf("%s.ndjson", meta.SessionID))
	f, err := os.OpenFile(recordPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		log.Error(err, "session recording disabled: failed to create file", "path", recordPath)
		return nil
	}
	r := &SessionRecorder{
		log:  log,
		cfg:  cfg,
		meta: meta,
		file: f,
		enc:  json.NewEncoder(f),
	}
	log.Info("Session recording enabled",
		"sessionId", meta.SessionID,
		"mode", string(cfg.Mode),
		"path", recordPath,
		LogKeyComponent, ComponentGateway,
		LogKeyEvent, "devplane.session_recording.start",
	)
	return r
}

// RecordFrame appends one directional frame observation.
func (r *SessionRecorder) RecordFrame(direction string, messageType int, payload []byte) {
	if r == nil {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.seq++
	payloadHash := sha256.Sum256(payload)
	payloadSHA := hex.EncodeToString(payloadHash[:])
	ts := time.Now().UTC().Format(time.RFC3339Nano)

	sumInput := fmt.Sprintf("%s|%d|%s|%d|%s|%s", r.prev, r.seq, direction, messageType, payloadSHA, ts)
	entryHashRaw := sha256.Sum256([]byte(sumInput))
	entryHash := hex.EncodeToString(entryHashRaw[:])

	rec := SessionFrameRecord{
		TimestampUTC: ts,
		Seq:          r.seq,
		Direction:    direction,
		MessageType:  messageType,
		PayloadBytes: len(payload),
		PayloadSHA:   payloadSHA,
		PrevHash:     r.prev,
		EntryHash:    entryHash,
	}
	if r.cfg.Mode == SessionRecordingFull {
		rec.PayloadB64 = base64.StdEncoding.EncodeToString(payload)
	}
	if err := r.enc.Encode(rec); err != nil {
		r.log.Error(err, "session recording write failed",
			"sessionId", r.meta.SessionID,
			LogKeyComponent, ComponentGateway,
			LogKeyEvent, "devplane.session_recording.write_error",
		)
		return
	}
	r.prev = entryHash
}

// Close flushes and writes a manifest with final chain hash.
func (r *SessionRecorder) Close(closeErr error) {
	if r == nil {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.file == nil {
		return
	}
	_ = r.file.Sync()
	_ = r.file.Close()

	manifestPath := filepath.Join(r.cfg.Dir, fmt.Sprintf("%s.manifest.json", r.meta.SessionID))
	manifestFile, err := os.OpenFile(manifestPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err == nil {
		closeReason := ""
		if closeErr != nil {
			closeReason = closeErr.Error()
		}
		manifest := SessionManifest{
			SchemaVersion: "1",
			Mode:          string(r.cfg.Mode),
			Meta:          r.meta,
			FrameCount:    r.seq,
			FinalHash:     r.prev,
			ClosedAt:      time.Now().UTC().Format(time.RFC3339Nano),
			CloseReason:   closeReason,
		}
		enc := json.NewEncoder(manifestFile)
		enc.SetIndent("", "  ")
		_ = enc.Encode(manifest)
		_ = manifestFile.Close()
	} else {
		r.log.Error(err, "session recording manifest write failed", "sessionId", r.meta.SessionID)
	}

	r.log.Info("Session recording closed",
		"sessionId", r.meta.SessionID,
		"frameCount", r.seq,
		"finalHash", r.prev,
		"manifestPath", manifestPath,
		LogKeyComponent, ComponentGateway,
		LogKeyEvent, "devplane.session_recording.end",
	)
	r.file = nil
}
