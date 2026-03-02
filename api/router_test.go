package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellymux/api"
	"github.com/ddevcap/jellymux/api/handler"
	"github.com/ddevcap/jellymux/backend"
	"github.com/ddevcap/jellymux/config"
)

var _ = Describe("buildAllowedOrigins", func() {
	It("returns both http and https variants for an https URL", func() {
		origins := api.BuildAllowedOrigins("https://example.com:8096")
		Expect(origins).To(HaveKey("https://example.com:8096"))
		Expect(origins).To(HaveKey("http://example.com:8096"))
	})

	It("returns both http and https variants for an http URL", func() {
		origins := api.BuildAllowedOrigins("http://localhost:8096")
		Expect(origins).To(HaveKey("http://localhost:8096"))
		Expect(origins).To(HaveKey("https://localhost:8096"))
	})

	It("returns an empty map for an empty URL", func() {
		origins := api.BuildAllowedOrigins("")
		Expect(origins).To(BeEmpty())
	})

	It("strips the path from the URL", func() {
		origins := api.BuildAllowedOrigins("https://example.com/some/path")
		Expect(origins).To(HaveKey("https://example.com"))
		Expect(origins).NotTo(HaveKey("https://example.com/some/path"))
	})

	It("lowercases origins", func() {
		origins := api.BuildAllowedOrigins("https://Example.COM")
		Expect(origins).To(HaveKey("https://example.com"))
	})
})

var _ = Describe("NewRouter", func() {
	var (
		h    http.Handler
		stop func()
	)

	BeforeEach(func() {
		cfg := config.Config{
			ExternalURL:  "http://localhost:8096",
			ServerName:   "Test",
			ServerID:     "test-id",
			SessionTTL:   0,
			BitrateLimit: 0,
		}
		pool := backend.NewPool(db, cfg)
		wsHub := handler.NewWSHub()
		h, stop = api.NewRouter(db, cfg, pool, wsHub)
	})

	AfterEach(func() {
		if stop != nil {
			stop()
		}
	})

	It("lowercases request paths", func() {
		req, _ := http.NewRequest("GET", "/System/Info/Public", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))
	})

	It("returns 404 for unknown routes", func() {
		req, _ := http.NewRequest("GET", "/nonexistent/route", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusNotFound))
		var body map[string]interface{}
		Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
		Expect(body["error"]).To(Equal("endpoint not found"))
	})

	It("handles the /health endpoint", func() {
		req, _ := http.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))
	})

	It("handles /emby prefixed routes", func() {
		req, _ := http.NewRequest("GET", "/emby/system/info/public", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))
	})

	It("handles /jellyfin prefixed routes", func() {
		req, _ := http.NewRequest("GET", "/jellyfin/system/info/public", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))
	})

	It("requires auth for protected routes", func() {
		req, _ := http.NewRequest("GET", "/system/info", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusUnauthorized))
	})

	It("sets CORS headers for unknown origins", func() {
		req, _ := http.NewRequest("OPTIONS", "/system/info/public", nil)
		req.Header.Set("Origin", "http://unknown-origin.com")
		req.Header.Set("Access-Control-Request-Method", "GET")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		// Unknown origins are still allowed (returns the origin), but
		// the response should have CORS headers present
		Expect(w.Header().Get("Access-Control-Allow-Origin")).NotTo(BeEmpty())
	})

	It("allows credentialed requests from the configured external URL", func() {
		req, _ := http.NewRequest("OPTIONS", "/system/info/public", nil)
		req.Header.Set("Origin", "http://localhost:8096")
		req.Header.Set("Access-Control-Request-Method", "GET")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		Expect(w.Header().Get("Access-Control-Allow-Credentials")).To(Equal("true"))
	})
})
