package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorilla/websocket"
)

const (
	ttydPort           = 7681
	backendDialTimeout = 30 * time.Second
)

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	// Origin validation is handled by the OIDC auth layer before we get here.
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// Proxy upgrades an HTTP request to WebSocket and bidirectionally proxies
// frames to a backend workspace pod.
type Proxy struct {
	log logr.Logger
}

// NewProxy creates a Proxy that uses log for structured logging.
func NewProxy(log logr.Logger) *Proxy {
	return &Proxy{log: log}
}

// ServeWS upgrades r to WebSocket and proxies traffic to backendURL.
// onActivity is called on each forwarded frame so callers can update an
// idle-timeout timestamp; pass nil to disable activity tracking.
// It blocks until either side closes the connection.
func (p *Proxy) ServeWS(w http.ResponseWriter, r *http.Request, backendURL string, onActivity func()) error {
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return fmt.Errorf("upgrade client connection: %w", err)
	}
	defer clientConn.Close()

	// Use a separate context with a hard deadline for dialing the backend so that
	// a slow or unresponsive pod does not hold the goroutine open indefinitely.
	dialCtx, dialCancel := context.WithTimeout(r.Context(), backendDialTimeout)
	defer dialCancel()

	backendConn, _, err := websocket.DefaultDialer.DialContext(dialCtx, backendURL, nil)
	if err != nil {
		return fmt.Errorf("dial backend %q: %w", backendURL, err)
	}
	defer backendConn.Close()

	p.log.Info("WebSocket tunnel open", "backend", backendURL)

	errc := make(chan error, 2)
	go copyFrames(clientConn, backendConn, errc, onActivity)
	go copyFrames(backendConn, clientConn, errc, onActivity)

	err = <-errc
	p.log.Info("WebSocket tunnel closed", "backend", backendURL, "reason", err)
	return nil
}

// BackendURL builds the WebSocket URL for a workspace pod's ttyd service.
func BackendURL(serviceEndpoint string) string {
	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("%s:%d", serviceEndpoint, ttydPort)}
	return u.String()
}

// copyFrames reads WebSocket frames from src and writes them to dst.
// onActivity is invoked after each successfully forwarded frame; may be nil.
// On a normal close it propagates the close handshake to dst before returning.
func copyFrames(dst, src *websocket.Conn, errc chan<- error, onActivity func()) {
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
	}
}
