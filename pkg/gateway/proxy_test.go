package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestNewProxy(t *testing.T) {
	log := zap.New(zap.UseDevMode(true))
	p := NewProxy(log)
	if p == nil {
		t.Fatal("NewProxy returned nil")
	}
}

func TestCopyFrames_ForwardsMessages(t *testing.T) {
	wsUpgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}

	// "dst" side: records the first message it receives.
	gotMsg := make(chan string, 1)
	dstSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, b, err := c.ReadMessage()
		if err == nil {
			gotMsg <- string(b)
		}
	}))
	defer dstSrv.Close()

	// "src" side: hands the server-side conn to the test via a channel.
	srcServerConn := make(chan *websocket.Conn, 1)
	srcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		srcServerConn <- c
		time.Sleep(5 * time.Second) // keep alive for the test duration
	}))
	defer srcSrv.Close()

	// Dial both servers.
	srcClientConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srcSrv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial src server: %v", err)
	}
	defer srcClientConn.Close()
	src := <-srcServerConn

	dstClientConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(dstSrv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial dst server: %v", err)
	}
	defer dstClientConn.Close()

	// Wire copyFrames: src → dstClientConn.
	// Use atomic to avoid a data race between the copyFrames goroutine (writer)
	// and the test goroutine (reader).
	errc := make(chan error, 1)
	var activityCalled atomic.Bool
	go copyFrames(dstClientConn, src, errc, func() { activityCalled.Store(true) })

	// Inject a message through srcClientConn; the server-side (src) sees it and
	// copyFrames relays it to dstClientConn, which sends it to dstSrv handler.
	if err := srcClientConn.WriteMessage(websocket.TextMessage, []byte("relay-test")); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	select {
	case msg := <-gotMsg:
		if msg != "relay-test" {
			t.Errorf("forwarded message = %q, want relay-test", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: message was not forwarded by copyFrames")
	}
	if !activityCalled.Load() {
		t.Error("onActivity callback was not called after forwarding a frame")
	}
}

// TestServeWS exercises the full proxy path: HTTP upgrade → dial backend →
// bidirectional frame relay → close.
func TestServeWS(t *testing.T) {
	log := zap.New(zap.UseDevMode(true))
	proxy := NewProxy(log)

	// Backend: a WebSocket echo server.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
		conn, err := u.Upgrade(w, r, nil)
		if err != nil {
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
	defer backend.Close()
	backendWSURL := "ws" + strings.TrimPrefix(backend.URL, "http")

	// Frontend: an HTTP server that calls ServeWS to proxy to the backend.
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := proxy.ServeWS(w, r, backendWSURL, nil); err != nil {
			// Errors after the tunnel is set up are normal on close.
			t.Logf("ServeWS: %v", err)
		}
	}))
	defer frontend.Close()

	// Connect a WebSocket client to the frontend proxy.
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(frontend.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial frontend proxy: %v", err)
	}
	defer conn.Close()

	// Send through the proxy to the echo backend and read the echo back.
	if err := conn.WriteMessage(websocket.TextMessage, []byte("proxy-hello")); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	_, got, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if string(got) != "proxy-hello" {
		t.Errorf("echoed = %q, want proxy-hello", got)
	}
}

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

func TestBackendHTTPURL(t *testing.T) {
	tests := []struct {
		endpoint string
		want     string
	}{
		{"my-svc.default.svc.cluster.local", "http://my-svc.default.svc.cluster.local:7681"},
		{"10.0.0.5", "http://10.0.0.5:7681"},
	}
	for _, tt := range tests {
		got := BackendHTTPURL(tt.endpoint)
		if got != tt.want {
			t.Errorf("BackendHTTPURL(%q) = %q, want %q", tt.endpoint, got, tt.want)
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
