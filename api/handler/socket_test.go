package handler_test

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gorilla/websocket"

	"github.com/gin-gonic/gin"

	"github.com/ddevcap/jellymux/api/handler"
)

var _ = Describe("WSHub", func() {
	Describe("NewWSHub / Shutdown", func() {
		It("creates a hub and shuts it down without error", func() {
			hub := handler.NewWSHub()
			Expect(hub).NotTo(BeNil())
			hub.Shutdown()
		})
	})

	Describe("WebSocketHandler", func() {
		It("accepts a WebSocket connection and sends a KeepAlive message", func() {
			hub := handler.NewWSHub()
			defer hub.Shutdown()

			r := gin.New()
			r.GET("/socket", handler.WebSocketHandler(hub))
			server := httptest.NewServer(r)
			defer server.Close()

			// Connect via WebSocket
			wsURL := "ws" + server.URL[4:] + "/socket" // http → ws
			conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusSwitchingProtocols))
			defer func() { _ = conn.Close() }()

			// Should receive a KeepAlive message
			_, msg, err := conn.ReadMessage()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(msg)).To(ContainSubstring("KeepAlive"))
			Expect(string(msg)).To(ContainSubstring("MessageId"))
		})

		It("closes connection when hub is shut down", func() {
			hub := handler.NewWSHub()

			r := gin.New()
			r.GET("/socket", handler.WebSocketHandler(hub))
			server := httptest.NewServer(r)
			defer server.Close()

			wsURL := "ws" + server.URL[4:] + "/socket"
			conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			Expect(err).NotTo(HaveOccurred())

			// Read the initial KeepAlive
			_, _, err = conn.ReadMessage()
			Expect(err).NotTo(HaveOccurred())

			// Shutdown the hub — should cause the connection to close
			hub.Shutdown()

			// Next read should fail
			_, _, err = conn.ReadMessage()
			Expect(err).To(HaveOccurred())
		})
	})
})
