package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	// wsKeepAliveInterval is how often the proxy sends KeepAlive messages to connected clients.
	wsKeepAliveInterval = 10 * time.Second
	// wsReadDeadline is the maximum time to wait for a pong before considering the connection dead.
	wsReadDeadline = 90 * time.Second
)

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	ReadBufferSize:   1024,
	WriteBufferSize:  1024,
	// Allow all origins — the proxy already enforces auth via api_key.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WSHub tracks all active WebSocket connections so they can be closed
// during graceful shutdown. Create one in main and pass it to the handler.
type WSHub struct {
	mu    sync.Mutex
	conns map[*websocket.Conn]struct{}
	done  chan struct{} // closed on shutdown
}

func NewWSHub() *WSHub {
	return &WSHub{
		conns: make(map[*websocket.Conn]struct{}),
		done:  make(chan struct{}),
	}
}

func (h *WSHub) add(conn *websocket.Conn) {
	h.mu.Lock()
	h.conns[conn] = struct{}{}
	h.mu.Unlock()
}

func (h *WSHub) remove(conn *websocket.Conn) {
	h.mu.Lock()
	delete(h.conns, conn)
	h.mu.Unlock()
}

// Shutdown closes all active WebSocket connections and signals handlers to exit.
func (h *WSHub) Shutdown() {
	close(h.done)
	h.mu.Lock()
	defer h.mu.Unlock()
	for conn := range h.conns {
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"),
			time.Now().Add(time.Second),
		)
		_ = conn.Close()
	}
	h.conns = make(map[*websocket.Conn]struct{})
}

// WebSocketHandler returns a gin handler that manages WebSocket connections
// with lifecycle tracking via the hub.
func WebSocketHandler(hub *WSHub) gin.HandlerFunc {
	return func(c *gin.Context) {
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		hub.add(conn)
		defer func() {
			hub.remove(conn)
			_ = conn.Close()
		}()

		if err := sendKeepAlive(conn); err != nil {
			return
		}

		ticker := time.NewTicker(wsKeepAliveInterval)
		defer ticker.Stop()

		_ = conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
		conn.SetPongHandler(func(string) error {
			_ = conn.SetReadDeadline(time.Now().Add(wsReadDeadline))
			return nil
		})

		readErr := make(chan error, 1)
		go func() {
			for {
				_, _, err := conn.ReadMessage()
				if err != nil {
					readErr <- err
					return
				}
			}
		}()

		for {
			select {
			case <-hub.done:
				return
			case <-ticker.C:
				if err := sendKeepAlive(conn); err != nil {
					slog.Debug("ws: keepalive write error", "error", err)
					return
				}
			case err := <-readErr:
				if websocket.IsUnexpectedCloseError(err,
					websocket.CloseGoingAway,
					websocket.CloseNormalClosure,
					websocket.CloseNoStatusReceived,
				) {
					slog.Debug("ws: unexpected close", "error", err)
				}
				return
			}
		}
	}
}

// sendKeepAlive writes a Jellyfin-format KeepAlive message.
// Format: {"MessageType":"KeepAlive","MessageId":"<uuid>"}.
// The MessageId field is required by the jellyfin-sdk-kotlin deserializer.
func sendKeepAlive(conn *websocket.Conn) error {
	msgID := strings.ReplaceAll(uuid.New().String(), "-", "")
	msg := fmt.Sprintf(`{"MessageType":"KeepAlive","MessageId":"%s"}`, msgID)
	return conn.WriteMessage(websocket.TextMessage, []byte(msg))
}
