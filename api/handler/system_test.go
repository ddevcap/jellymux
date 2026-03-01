package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"

	"github.com/ddevcap/jellyfin-proxy/api/handler"
	"github.com/ddevcap/jellyfin-proxy/config"
)

var _ = Describe("SystemHandler", func() {
	var h *handler.SystemHandler

	BeforeEach(func() {
		h = handler.NewSystemHandler(config.Config{
			ExternalURL: "https://example.com",
			ServerName:  "Test Proxy",
			ServerID:    "test-server-id",
		}, nil, nil)
	})

	// serve wires up a single-route router and returns the recorded response.
	serve := func(method, path string, fn gin.HandlerFunc, reqPath string) *httptest.ResponseRecorder {
		r := gin.New()
		r.Handle(method, path, fn)
		req, _ := http.NewRequest(method, reqPath, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	Describe("InfoPublic", func() {
		It("returns 200 with server identity fields", func() {
			w := serve("GET", "/system/info/public", h.InfoPublic, "/system/info/public")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body["Id"]).To(Equal("testserverid"))
			Expect(body["ServerName"]).To(Equal("Test Proxy"))
			Expect(body["LocalAddress"]).To(Equal("https://example.com"))
			Expect(body["StartupWizardCompleted"]).To(BeTrue())
		})
	})

	Describe("Info", func() {
		It("returns 200 with full server info", func() {
			w := serve("GET", "/system/info", h.Info, "/system/info")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body["Id"]).To(Equal("testserverid"))
			Expect(body["CanSelfRestart"]).To(BeFalse())
			Expect(body["SupportsLibraryMonitor"]).To(BeFalse())
		})
	})

	Describe("BrandingConfiguration", func() {
		It("returns 200 with empty branding fields", func() {
			w := serve("GET", "/branding/configuration", h.BrandingConfiguration, "/branding/configuration")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body["LoginDisclaimer"]).To(Equal(""))
			Expect(body["SplashscreenEnabled"]).To(BeFalse())
		})
	})

	Describe("UsersPublic", func() {
		It("returns 200 with an empty array", func() {
			w := serve("GET", "/users/public", h.UsersPublic, "/users/public")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(BeEmpty())
		})
	})

	Describe("QuickConnectEnabled", func() {
		It("returns 200 with false", func() {
			w := serve("GET", "/quickconnect/enabled", h.QuickConnectEnabled, "/quickconnect/enabled")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body bool
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(BeFalse())
		})
	})

	Describe("SessionCapabilitiesFull", func() {
		It("returns 204", func() {
			w := serve("POST", "/sessions/capabilities/full", h.SessionCapabilitiesFull, "/sessions/capabilities/full")

			Expect(w.Code).To(Equal(http.StatusNoContent))
		})
	})

	Describe("DisplayPreferencesGet", func() {
		It("returns 200 with the requested ID echoed back", func() {
			r := gin.New()
			r.GET("/displaypreferences/:id", h.DisplayPreferencesGet)
			req, _ := http.NewRequest("GET", "/displaypreferences/my-prefs-id", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			var body map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body["Id"]).To(Equal("my-prefs-id"))
		})
	})

	Describe("DisplayPreferencesUpdate", func() {
		It("returns 204", func() {
			r := gin.New()
			r.POST("/displaypreferences/:id", h.DisplayPreferencesUpdate)
			req, _ := http.NewRequest("POST", "/displaypreferences/any-id", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusNoContent))
		})
	})

	Describe("GetEndpointInfo", func() {
		It("returns 200 with RemoteEndPoint and IsLocal", func() {
			w := serve("GET", "/system/endpoint", h.GetEndpointInfo, "/system/endpoint")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(HaveKey("RemoteEndPoint"))
			Expect(body["IsLocal"]).To(BeTrue())
		})
	})

	Describe("ActivityLogEntries", func() {
		It("returns 200 with empty Items and zero TotalRecordCount", func() {
			w := serve("GET", "/system/activitylog/entries", h.ActivityLogEntries, "/system/activitylog/entries")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body["TotalRecordCount"]).To(BeNumerically("==", 0))
			Expect(body["Items"]).To(BeEmpty())
		})
	})

	Describe("InfoStorage", func() {
		It("returns 200 with empty Drives", func() {
			w := serve("GET", "/system/info/storage", h.InfoStorage, "/system/info/storage")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body["Drives"]).To(BeEmpty())
		})
	})

	Describe("GetDevices", func() {
		It("returns 200 with empty Items and zero TotalRecordCount", func() {
			w := serve("GET", "/devices", h.GetDevices, "/devices")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body["TotalRecordCount"]).To(BeNumerically("==", 0))
		})
	})

	Describe("GetConfiguration", func() {
		It("returns 200 with IsStartupWizardCompleted true", func() {
			w := serve("GET", "/system/configuration", h.GetConfiguration, "/system/configuration")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body["IsStartupWizardCompleted"]).To(BeTrue())
			Expect(body["QuickConnectAvailable"]).To(BeFalse())
		})
	})

	Describe("GetConfigurationNetwork", func() {
		It("returns 200 with EnableRemoteAccess true", func() {
			w := serve("GET", "/system/configuration/network", h.GetConfigurationNetwork, "/system/configuration/network")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body["EnableRemoteAccess"]).To(BeTrue())
			Expect(body["RequireHttps"]).To(BeFalse())
		})
	})

	Describe("GetLocalizationOptions", func() {
		It("returns 200 with an empty array", func() {
			w := serve("GET", "/localization/options", h.GetLocalizationOptions, "/localization/options")

			Expect(w.Code).To(Equal(http.StatusOK))
			var body []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(BeEmpty())
		})
	})

	Describe("BitrateTest", func() {
		It("returns the requested number of bytes", func() {
			r := gin.New()
			r.GET("/playback/bitratetest", h.BitrateTest)
			req, _ := http.NewRequest("GET", "/playback/bitratetest?Size=1024", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.Len()).To(Equal(1024))
		})

		It("uses the 100 KB default when Size is absent", func() {
			r := gin.New()
			r.GET("/playback/bitratetest", h.BitrateTest)
			req, _ := http.NewRequest("GET", "/playback/bitratetest", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.Len()).To(Equal(102400))
		})

		It("caps the response at 10 MB", func() {
			r := gin.New()
			r.GET("/playback/bitratetest", h.BitrateTest)
			req, _ := http.NewRequest("GET", "/playback/bitratetest?Size=99999999", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.Len()).To(Equal(10 * 1024 * 1024))
		})
	})

	// ── Missing system stubs ──────────────────────────────────────────────────

	Describe("BrandingCss", func() {
		It("returns 200 with text/css content type", func() {
			w := serve("GET", "/branding/css", h.BrandingCss, "/branding/css")
			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Header().Get("Content-Type")).To(ContainSubstring("text/css"))
		})
	})

	Describe("GetSystemLogs", func() {
		It("returns 200 with an empty array", func() {
			w := serve("GET", "/system/logs", h.GetSystemLogs, "/system/logs")
			Expect(w.Code).To(Equal(http.StatusOK))
			var body []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(BeEmpty())
		})
	})

	Describe("GetSystemLogFile", func() {
		It("returns 200 with empty content", func() {
			w := serve("GET", "/system/logs/log", h.GetSystemLogFile, "/system/logs/log")
			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Header().Get("Content-Type")).To(ContainSubstring("text/plain"))
		})
	})

	Describe("GetPackages", func() {
		It("returns 200 with an empty array", func() {
			w := serve("GET", "/packages", h.GetPackages, "/packages")
			Expect(w.Code).To(Equal(http.StatusOK))
			var body []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(BeEmpty())
		})
	})

	Describe("GetRepositories", func() {
		It("returns 200 with an empty array", func() {
			w := serve("GET", "/repositories", h.GetRepositories, "/repositories")
			Expect(w.Code).To(Equal(http.StatusOK))
			var body []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(BeEmpty())
		})
	})

	Describe("GetLocalizationCultures", func() {
		It("returns 200 with a non-empty array of cultures", func() {
			w := serve("GET", "/localization/cultures", h.GetLocalizationCultures, "/localization/cultures")
			Expect(w.Code).To(Equal(http.StatusOK))
			var body []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body).NotTo(BeEmpty())
		})
	})

	Describe("GetLocalizationCountries", func() {
		It("returns 200 with a non-empty array of countries", func() {
			w := serve("GET", "/localization/countries", h.GetLocalizationCountries, "/localization/countries")
			Expect(w.Code).To(Equal(http.StatusOK))
			var body []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body).NotTo(BeEmpty())
		})
	})

	Describe("GetParentalRatings", func() {
		It("returns 200 with an empty array", func() {
			w := serve("GET", "/parentalratings", h.GetParentalRatings, "/parentalratings")
			Expect(w.Code).To(Equal(http.StatusOK))
			var body []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(BeEmpty())
		})
	})
})
