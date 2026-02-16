package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestBackendURL(t *testing.T) {
	tests := []struct {
		endpoint string
		want     string
	}{
		{"my-svc.default.svc.cluster.local", "ws://my-svc.default.svc.cluster.local:7681"},
		{"10.0.0.5", "ws://10.0.0.5:7681"},
	}
	for _, tt := range tests {
		got := BackendURL(tt.endpoint)
		if got != tt.want {
			t.Errorf("BackendURL(%q) = %q, want %q", tt.endpoint, got, tt.want)
		}
	}
}

func TestCopyFrames(t *testing.T) {
	// Create a WebSocket echo server
	echoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("echo server upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}))
	defer echoServer.Close()

	wsURL := "ws" + strings.TrimPrefix(echoServer.URL, "http")

	// Connect as a client
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial echo server: %v", err)
	}
	defer clientConn.Close()

	// Send a message and read back
	msg := []byte("hello workspace")
	if err := clientConn.WriteMessage(websocket.TextMessage, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, got, err := clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(msg) {
		t.Errorf("echo = %q, want %q", got, msg)
	}
}
