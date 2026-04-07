package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

func TestSessionRecordingConfigFromEnv(t *testing.T) {
	t.Setenv("SESSION_RECORDING_ENABLED", "true")
	t.Setenv("SESSION_RECORDING_MODE", "full")
	t.Setenv("SESSION_RECORDING_DIR", "/tmp/devplane-test")

	cfg := SessionRecordingConfigFromEnv()
	if !cfg.Enabled {
		t.Fatal("expected recording enabled")
	}
	if cfg.Mode != SessionRecordingFull {
		t.Fatalf("mode = %q, want %q", cfg.Mode, SessionRecordingFull)
	}
	if cfg.Dir != "/tmp/devplane-test" {
		t.Fatalf("dir = %q, want /tmp/devplane-test", cfg.Dir)
	}
}

func TestSessionRecorderWritesManifestAndFrames(t *testing.T) {
	dir := t.TempDir()
	cfg := SessionRecordingConfig{
		Enabled: true,
		Mode:    SessionRecordingMetadataOnly,
		Dir:     dir,
	}
	meta := SessionMeta{
		SessionID: "session-test",
		RequestID: "req-1",
		UserID:    "user-a",
		StartedAt: "2026-04-07T00:00:00Z",
	}
	r := NewSessionRecorder(log.Log, cfg, meta)
	if r == nil {
		t.Fatal("expected recorder")
	}
	r.RecordFrame("client_to_backend", 1, []byte("hello"))
	r.RecordFrame("backend_to_client", 1, []byte("world"))
	r.Close(nil)

	recordPath := filepath.Join(dir, "session-test.ndjson")
	manifestPath := filepath.Join(dir, "session-test.manifest.json")
	recordBytes, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read record file: %v", err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest file: %v", err)
	}
	if !strings.Contains(string(recordBytes), "\"direction\":\"client_to_backend\"") {
		t.Fatalf("record file missing frame direction: %s", string(recordBytes))
	}
	if !strings.Contains(string(manifestBytes), "\"finalHash\"") {
		t.Fatalf("manifest missing final hash: %s", string(manifestBytes))
	}
}
