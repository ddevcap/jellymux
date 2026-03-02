package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"

	"github.com/ddevcap/jellymux/api/handler"
	"github.com/ddevcap/jellymux/config"
)

// serveSystem wires up a single-route gin router and fires the request.
func serveSystem(method, path string, fn gin.HandlerFunc, reqPath string) *httptest.ResponseRecorder {
	r := gin.New()
	r.Handle(method, path, fn)
	req, _ := http.NewRequest(method, reqPath, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

var _ = Describe("DisplayPreferencesGet", func() {
	var h *handler.SystemHandler

	BeforeEach(func() {
		h = handler.NewSystemHandler(config.Config{
			ExternalURL: "https://example.com",
			ServerName:  "Test Proxy",
			ServerID:    "test-server-id",
		}, nil, nil)
	})

	It("returns defaults when nothing is stored", func() {
		w := serveSystem("GET", "/displaypreferences/:id", h.DisplayPreferencesGet, "/displaypreferences/usersettings")
		Expect(w.Code).To(Equal(http.StatusOK))

		var resp map[string]interface{}
		Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
		Expect(resp["Id"]).To(Equal("usersettings"))
		Expect(resp["SortBy"]).To(Equal("SortName"))
	})
})

var _ = Describe("DisplayPreferencesUpdate", func() {
	var h *handler.SystemHandler

	BeforeEach(func() {
		h = handler.NewSystemHandler(config.Config{
			ExternalURL: "https://example.com",
			ServerName:  "Test Proxy",
			ServerID:    "test-server-id",
		}, nil, nil)
	})

	It("returns 204", func() {
		w := serveSystem("POST", "/displaypreferences/:id", h.DisplayPreferencesUpdate, "/displaypreferences/usersettings")
		Expect(w.Code).To(Equal(http.StatusNoContent))
	})
})

var _ = Describe("HealthLive", func() {
	var h *handler.SystemHandler

	BeforeEach(func() {
		h = handler.NewSystemHandler(config.Config{}, nil, nil)
	})

	It("returns 200 ok", func() {
		w := serveSystem("GET", "/health", h.HealthLive, "/health")
		Expect(w.Code).To(Equal(http.StatusOK))

		var resp map[string]interface{}
		Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
		Expect(resp["status"]).To(Equal("ok"))
	})
})

var _ = Describe("HealthReady", func() {
	It("returns 200 when DB is reachable", func() {
		h := handler.NewSystemHandler(config.Config{}, db, nil)
		w := serveSystem("GET", "/ready", h.HealthReady, "/ready")
		Expect(w.Code).To(Equal(http.StatusOK))

		var resp map[string]interface{}
		Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
		Expect(resp["status"]).To(Equal("ready"))
	})
})
