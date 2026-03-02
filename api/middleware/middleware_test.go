package middleware_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/ddevcap/jellymux/api/middleware"
	"github.com/ddevcap/jellymux/config"
	"github.com/ddevcap/jellymux/ent"
)

// newCtx builds a minimal gin.Context from a hand-crafted *http.Request.
func newCtx(req *http.Request) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	return c
}

var _ = Describe("ParseMediaBrowserAuth", func() {
	It("parses all key=value pairs from a standard Jellyfin header", func() {
		hdr := `MediaBrowser Client="Jellyfin Web", Device="Chrome", DeviceId="abc123", Version="10.8.0", Token="my-token"`
		result := middleware.ParseMediaBrowserAuth(hdr)

		Expect(result["Client"]).To(Equal("Jellyfin Web"))
		Expect(result["Device"]).To(Equal("Chrome"))
		Expect(result["DeviceId"]).To(Equal("abc123"))
		Expect(result["Version"]).To(Equal("10.8.0"))
		Expect(result["Token"]).To(Equal("my-token"))
	})

	It("returns an empty map for an empty header", func() {
		Expect(middleware.ParseMediaBrowserAuth("")).To(BeEmpty())
	})

	It("returns an empty map when no quoted pairs are present", func() {
		Expect(middleware.ParseMediaBrowserAuth("Bearer sometoken")).To(BeEmpty())
	})
})

var _ = Describe("ExtractToken", func() {
	gin.SetMode(gin.TestMode)

	It("prefers X-Emby-Token over all others", func() {
		req, _ := http.NewRequest(http.MethodGet, "/?api_key=query-token", nil)
		req.Header.Set("X-Emby-Token", "emby-token")
		req.Header.Set("X-MediaBrowser-Token", "mb-token")

		Expect(middleware.ExtractToken(newCtx(req))).To(Equal("emby-token"))
	})

	It("falls back to X-MediaBrowser-Token when X-Emby-Token is absent", func() {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-MediaBrowser-Token", "mb-token")

		Expect(middleware.ExtractToken(newCtx(req))).To(Equal("mb-token"))
	})

	It("extracts Token from the Authorization header", func() {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", `MediaBrowser Client="Test", Token="auth-token"`)

		Expect(middleware.ExtractToken(newCtx(req))).To(Equal("auth-token"))
	})

	It("falls back to the api_key query parameter", func() {
		req, _ := http.NewRequest(http.MethodGet, "/?api_key=query-token", nil)

		Expect(middleware.ExtractToken(newCtx(req))).To(Equal("query-token"))
	})

	It("returns an empty string when no token is present", func() {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)

		Expect(middleware.ExtractToken(newCtx(req))).To(BeEmpty())
	})
})

var _ = Describe("ClientIP", func() {
	gin.SetMode(gin.TestMode)

	It("uses X-Forwarded-For with default test context (trusts all)", func() {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		req.RemoteAddr = "5.6.7.8:1234"

		// gin.CreateTestContext defaults to trusting all proxies.
		Expect(middleware.ClientIP(newCtx(req))).To(Equal("1.2.3.4"))
	})

	It("falls back to RemoteAddr when X-Forwarded-For is absent", func() {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "5.6.7.8:1234"

		Expect(middleware.ClientIP(newCtx(req))).To(Equal("5.6.7.8"))
	})

	It("ignores X-Forwarded-For when engine has no trusted proxies", func() {
		r := gin.New()
		_ = r.SetTrustedProxies(nil) // trust nobody
		var gotIP string
		r.GET("/", func(c *gin.Context) {
			gotIP = middleware.ClientIP(c)
			c.Status(http.StatusOK)
		})
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		req.RemoteAddr = "5.6.7.8:1234"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		Expect(gotIP).To(Equal("5.6.7.8"))
	})

	It("trusts X-Forwarded-For when trusted proxies are set on the engine", func() {
		r := gin.New()
		_ = r.SetTrustedProxies([]string{"5.6.7.8"})
		var gotIP string
		r.GET("/", func(c *gin.Context) {
			gotIP = middleware.ClientIP(c)
			c.Status(http.StatusOK)
		})
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "1.2.3.4")
		req.RemoteAddr = "5.6.7.8:1234"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		Expect(gotIP).To(Equal("1.2.3.4"))
	})
})

var _ = Describe("AdminOnly middleware", func() {
	gin.SetMode(gin.TestMode)

	// helper spins up a router with AdminOnly in the chain and a trivial 200 handler.
	routerWithAdmin := func() *gin.Engine {
		r := gin.New()
		r.GET("/secret", middleware.AdminOnly(), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
		return r
	}

	Context("when the caller is an admin", func() {
		It("allows the request through", func() {
			r := gin.New()
			r.GET("/secret", func(c *gin.Context) {
				// Inject an admin user before AdminOnly runs.
				c.Set(middleware.ContextKeyUser, &ent.User{IsAdmin: true})
			}, middleware.AdminOnly(), func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			req, _ := http.NewRequest(http.MethodGet, "/secret", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
		})
	})

	Context("when the caller is not an admin", func() {
		It("returns 403", func() {
			r := gin.New()
			r.GET("/secret", func(c *gin.Context) {
				c.Set(middleware.ContextKeyUser, &ent.User{IsAdmin: false})
			}, middleware.AdminOnly(), func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			req, _ := http.NewRequest(http.MethodGet, "/secret", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusForbidden))
		})
	})

	Context("when no user is set in the context", func() {
		It("returns 401", func() {
			req, _ := http.NewRequest(http.MethodGet, "/secret", nil)
			w := httptest.NewRecorder()
			routerWithAdmin().ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusUnauthorized))
		})
	})
})

var _ = Describe("LoginRateLimiter", func() {
	gin.SetMode(gin.TestMode)

	// buildLimiter wires up a router with the limiter middleware and returns
	// the router plus the onFailure / onSuccess callbacks.
	buildLimiter := func(maxAttempts int) (*gin.Engine, func(string), func(string)) {
		cfg := config.Config{
			LoginMaxAttempts: maxAttempts,
			LoginWindow:      time.Minute,
			LoginBanDuration: time.Minute,
		}
		mw, onFailure, onSuccess, stopLimiter := middleware.LoginRateLimiter(cfg)
		DeferCleanup(stopLimiter)
		r := gin.New()
		r.POST("/login", mw, func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
		return r, onFailure, onSuccess
	}

	Context("before the threshold is reached", func() {
		It("allows requests through", func() {
			r, onFailure, _ := buildLimiter(3)

			// Two failures — still under the threshold of 3.
			onFailure("1.2.3.4")
			onFailure("1.2.3.4")

			req, _ := http.NewRequest(http.MethodPost, "/login", nil)
			req.RemoteAddr = "1.2.3.4:0"
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
		})
	})

	Context("after the threshold is reached", func() {
		It("returns 429", func() {
			r, onFailure, _ := buildLimiter(3)

			// Record exactly MaxAttempts failures to trigger the ban.
			onFailure("1.2.3.4")
			onFailure("1.2.3.4")
			onFailure("1.2.3.4")

			req, _ := http.NewRequest(http.MethodPost, "/login", nil)
			req.RemoteAddr = "1.2.3.4:0"
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusTooManyRequests))
		})
	})

	Context("after a successful login resets the counter", func() {
		It("allows the IP again even if it had previous failures", func() {
			r, onFailure, onSuccess := buildLimiter(3)

			// Two failures followed by a success — counter should reset.
			onFailure("1.2.3.4")
			onFailure("1.2.3.4")
			onSuccess("1.2.3.4")
			// One more failure after reset — still under threshold.
			onFailure("1.2.3.4")

			req, _ := http.NewRequest(http.MethodPost, "/login", nil)
			req.RemoteAddr = "1.2.3.4:0"
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
		})
	})

	Context("when LoginMaxAttempts is 0 (rate limiting disabled)", func() {
		It("allows all requests regardless of failures", func() {
			r, onFailure, _ := buildLimiter(0)

			// Record many failures — shouldn't matter.
			for i := 0; i < 100; i++ {
				onFailure("1.2.3.4")
			}

			req, _ := http.NewRequest(http.MethodPost, "/login", nil)
			req.RemoteAddr = "1.2.3.4:0"
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
		})
	})

	Context("banning one IP does not affect another", func() {
		It("allows the clean IP through", func() {
			r, onFailure, _ := buildLimiter(3)

			// Ban 1.2.3.4.
			onFailure("1.2.3.4")
			onFailure("1.2.3.4")
			onFailure("1.2.3.4")

			// A different IP should be unaffected.
			req, _ := http.NewRequest(http.MethodPost, "/login", nil)
			req.RemoteAddr = "9.9.9.9:0"
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			Expect(w.Code).To(Equal(http.StatusOK))
		})
	})

	Context("when LoginMaxAttempts is 0 (rate limiting disabled)", func() {
		It("the onFailure callback does not panic", func() {
			_, onFailure, _ := buildLimiter(0)
			Expect(func() { onFailure("1.2.3.4") }).NotTo(Panic())
		})
	})
})

var _ = Describe("RequestID middleware", func() {
	gin.SetMode(gin.TestMode)

	It("sets X-Request-Id header on response when none is provided", func() {
		r := gin.New()
		r.Use(middleware.RequestID())
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(w.Header().Get("X-Request-Id")).NotTo(BeEmpty())
	})

	It("reuses incoming X-Request-Id when provided", func() {
		r := gin.New()
		r.Use(middleware.RequestID())
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Request-Id", "my-custom-id")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(w.Header().Get("X-Request-Id")).To(Equal("my-custom-id"))
	})
})

var _ = Describe("ExtractAllTokens", func() {
	gin.SetMode(gin.TestMode)

	It("returns tokens from all sources", func() {
		req, _ := http.NewRequest(http.MethodGet, "/?api_key=query1&ApiKey=query2", nil)
		req.Header.Set("X-Emby-Token", "emby-token")
		req.Header.Set("X-MediaBrowser-Token", "mb-token")
		req.Header.Set("Authorization", `MediaBrowser Token="auth-token"`)

		tokens := middleware.ExtractAllTokens(newCtx(req))
		Expect(tokens).To(ContainElements("emby-token", "mb-token", "auth-token", "query1", "query2"))
	})

	It("returns empty slice when no tokens present", func() {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		tokens := middleware.ExtractAllTokens(newCtx(req))
		Expect(tokens).To(BeEmpty())
	})

	It("returns only header tokens when no query params", func() {
		req, _ := http.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Emby-Token", "only-token")
		tokens := middleware.ExtractAllTokens(newCtx(req))
		Expect(tokens).To(Equal([]string{"only-token"}))
	})
})

var _ = Describe("Auth middleware", func() {
	gin.SetMode(gin.TestMode)

	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
		cleanDB()
	})

	createUserAndSession := func(username, token string) *ent.User {
		hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
		u := db.User.Create().
			SetUsername(username).
			SetDisplayName(username).
			SetHashedPassword(string(hash)).
			SetIsAdmin(false).
			SaveX(ctx)
		db.Session.Create().
			SetToken(token).
			SetDeviceID("dev").
			SetDeviceName("Dev").
			SetAppName("App").
			SetUser(u).
			SaveX(ctx)
		return u
	}

	It("allows requests with a valid token", func() {
		createUserAndSession("authuser", "valid-token")

		r := gin.New()
		r.Use(middleware.Auth(db, config.Config{}))
		r.GET("/test", func(c *gin.Context) {
			u, _ := c.Get(middleware.ContextKeyUser)
			Expect(u).NotTo(BeNil())
			Expect(u.(*ent.User).Username).To(Equal("authuser"))
			c.Status(http.StatusOK)
		})

		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Emby-Token", "valid-token")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))
	})

	It("rejects requests with no token", func() {
		r := gin.New()
		r.Use(middleware.Auth(db, config.Config{}))
		r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusUnauthorized))
	})

	It("rejects requests with an invalid token", func() {
		r := gin.New()
		r.Use(middleware.Auth(db, config.Config{}))
		r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Emby-Token", "bogus")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusUnauthorized))
	})

	It("rejects and deletes expired sessions", func() {
		u := createUserAndSession("expuser", "expired-token")
		// Backdate session last_activity
		db.Session.Update().
			Where().
			SetLastActivity(time.Now().Add(-2 * time.Hour)).
			ExecX(ctx)
		_ = u

		r := gin.New()
		r.Use(middleware.Auth(db, config.Config{SessionTTL: time.Hour}))
		r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Emby-Token", "expired-token")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusUnauthorized))

		// Session should be deleted
		count, _ := db.Session.Query().Count(ctx)
		Expect(count).To(Equal(0))
	})

	It("debounces last-activity updates", func() {
		createUserAndSession("debounce-user", "debounce-token")

		r := gin.New()
		r.Use(middleware.Auth(db, config.Config{}))
		r.GET("/test", func(c *gin.Context) { c.Status(http.StatusOK) })

		// First request
		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Emby-Token", "debounce-token")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))
	})
})

var _ = Describe("RequestLogger middleware", func() {
	gin.SetMode(gin.TestMode)

	It("calls next and completes the request", func() {
		r := gin.New()
		r.Use(middleware.RequestID(), middleware.RequestLogger())
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		req, _ := http.NewRequest(http.MethodGet, "/test?foo=bar", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		Expect(w.Code).To(Equal(http.StatusOK))
	})
})
