package gateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorilla/websocket"
)

const (
	ttydPort           = 7681
	backendDialTimeout = 30 * time.Second
	// maxWSFrameBytes caps a single WebSocket message from either peer to limit memory
	// use if a client or ttyd misbehaves (default gorilla limit is unlimited).
	maxWSFrameBytes = 1 << 20 // 1 MiB
)

// wsBackendDialer matches DefaultDialer but uses the same handshake timeout as
// backendDialTimeout and honors HTTP_PROXY for outbound dials from the gateway.
var wsBackendDialer = &websocket.Dialer{
	Proxy:            http.ProxyFromEnvironment,
	HandshakeTimeout: backendDialTimeout,
}

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	// Origin validation is handled by the OIDC auth layer before we get here.
	CheckOrigin:  func(_ *http.Request) bool { return true },
	Subprotocols: []string{"tty"},
}

// Proxy upgrades an HTTP request to WebSocket and bidirectionally proxies
// frames to a backend workspace pod.
type Proxy struct {
	log logr.Logger
}

// FrameObserver receives each proxied WebSocket frame directionally.
type FrameObserver func(direction string, msgType int, payload []byte)

// NewProxy creates a Proxy that uses log for structured logging.
func NewProxy(log logr.Logger) *Proxy {
	return &Proxy{log: log}
}

// ServeWS upgrades r to WebSocket and proxies traffic to backendURL.
// onActivity is called on each forwarded frame so callers can update an
// idle-timeout timestamp; pass nil to disable activity tracking.
// It blocks until either side closes the connection.
func (p *Proxy) ServeWS(w http.ResponseWriter, r *http.Request, backendURL string, onActivity func(), onFrame FrameObserver) error {
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return fmt.Errorf("upgrade client connection: %w", err)
	}
	defer func() { _ = clientConn.Close() }()

	// Use a separate context with a hard deadline for dialing the backend so that
	// a slow or unresponsive pod does not hold the goroutine open indefinitely.
	dialCtx, dialCancel := context.WithTimeout(r.Context(), backendDialTimeout)
	defer dialCancel()

	var backendHeaders http.Header
	if subproto := clientConn.Subprotocol(); subproto != "" {
		backendHeaders = http.Header{"Sec-WebSocket-Protocol": []string{subproto}}
	}
	backendConn, _, err := wsBackendDialer.DialContext(dialCtx, backendURL, backendHeaders)
	if err != nil {
		return fmt.Errorf("dial backend %q: %w", backendURL, err)
	}
	defer func() { _ = backendConn.Close() }()

	clientConn.SetReadLimit(maxWSFrameBytes)
	backendConn.SetReadLimit(maxWSFrameBytes)

	p.log.Info("WebSocket tunnel open", LogKeyComponent, ComponentGateway, LogKeyEvent, EventWSProxyStart, "backend", backendURL)

	errc := make(chan error, 2)
	go copyFrames(clientConn, backendConn, "client_to_backend", errc, onActivity, onFrame)
	go copyFrames(backendConn, clientConn, "backend_to_client", errc, onActivity, onFrame)

	err = <-errc
	p.log.Info("WebSocket tunnel closed", LogKeyComponent, ComponentGateway, LogKeyEvent, EventWSProxySessionEnd, "backend", backendURL, "reason", err)
	return nil
}

// BackendURL builds the WebSocket URL for a workspace pod's ttyd service.
func BackendURL(serviceEndpoint string) string {
	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("%s:%d", serviceEndpoint, ttydPort)}
	return u.String()
}

// BackendHTTPURL builds the HTTP URL for a workspace pod's ttyd service.
func BackendHTTPURL(serviceEndpoint string) string {
	u := url.URL{Scheme: "http", Host: fmt.Sprintf("%s:%d", serviceEndpoint, ttydPort)}
	return u.String()
}

// copyFrames reads WebSocket frames from src and writes them to dst.
// onActivity is invoked after each successfully forwarded frame; may be nil.
// On a normal close it propagates the close handshake to dst before returning.
func copyFrames(dst, src *websocket.Conn, direction string, errc chan<- error, onActivity func(), onFrame FrameObserver) {
	for {
		msgType, data, err := src.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				_ = dst.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			}
			errc <- err
			return
		}
		if err := dst.WriteMessage(msgType, data); err != nil {
			errc <- err
			return
		}
		if onActivity != nil {
			onActivity()
		}
		if onFrame != nil {
			onFrame(direction, msgType, data)
		}
	}
}

// backendReadyTimeout is the maximum time to wait for a TCP connection to the backend.
const backendReadyTimeout = 5 * time.Second

// BackendReady performs a quick TCP dial to serviceEndpoint:7681 to check
// whether the workspace pod's ttyd server is accepting connections.
// This avoids proxying a WebSocket dial that would hang or fail when the
// pod is running but the ttyd process hasn't started yet.
func BackendReady(serviceEndpoint string) bool {
	addr := net.JoinHostPort(serviceEndpoint, fmt.Sprintf("%d", ttydPort))
	conn, err := net.DialTimeout("tcp", addr, backendReadyTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
