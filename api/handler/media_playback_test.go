package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"

	"github.com/ddevcap/jellyfin-proxy/api/handler"
	"github.com/ddevcap/jellyfin-proxy/api/middleware"
	"github.com/ddevcap/jellyfin-proxy/backend"
	"github.com/ddevcap/jellyfin-proxy/config"
	"github.com/ddevcap/jellyfin-proxy/idtrans"
)

// ─────────────────────────────────────────────────────────────────────────────
// TOKEN GLOSSARY (so every test is unambiguous about which token is which):
//
//   pbProxyToken  – the proxy session token stored in the sessions table.
//                   The Jellyfin web client knows this as its "accessToken".
//                   Sent via X-Emby-Token on authenticated routes, and via
//                   the ApiKey query param on public (streaming) routes.
//
//   pbBackendToken – the token that authenticates against the real Jellyfin
//                    backend server. Stored in the backend_user mapping.
//                    The proxy injects this when talking to the backend.
//                    Must NEVER reach the client.
// ─────────────────────────────────────────────────────────────────────────────

const (
	pbPrefix        = "pb"
	pbBackendItemID = "aabbccdd11223344aabbccdd11223344"
	pbBackendToken  = "backend-secret-token"
	pbProxyToken    = "playback-test-session-token"
	pbBackendUserID = "backend-user-42"
)

// playbackRouter builds a router that mirrors the real route layout.
func playbackRouter() (*gin.Engine, string) {
	cfg := config.Config{
		ServerID:    "test-server-id",
		ServerName:  "Test Proxy",
		ExternalURL: "http://proxy:8096",
	}
	pool := backend.NewPool(db, cfg)
	mediaH := handler.NewMediaHandler(pool, cfg, db)

	r := gin.New()

	// Authenticated routes — mirrors real router.
	priv := r.Group("/")
	priv.Use(middleware.Auth(db, cfg))
	priv.GET("/items/:itemId/playbackinfo", mediaH.GetPlaybackInfo)
	priv.POST("/items/:itemId/playbackinfo", mediaH.GetPlaybackInfo)
	priv.POST("/sessions/playing", mediaH.ReportPlaybackStart)
	priv.POST("/sessions/playing/progress", mediaH.ReportPlaybackProgress)
	priv.POST("/sessions/playing/stopped", mediaH.ReportPlaybackStopped)

	// Public route — browsers fetch HLS/stream URLs without custom headers.
	r.GET("/videos/:itemId/*subpath", mediaH.VideoSubpath)

	proxyID := idtrans.Encode(pbPrefix, pbBackendItemID)
	return r, proxyID
}

// streamRouter builds a router for testing StreamVideo, StreamAudio, UniversalAudio, GetImage.
func streamRouter() (*gin.Engine, string) {
	cfg := config.Config{
		ServerID:    "test-server-id",
		ServerName:  "Test Proxy",
		ExternalURL: "http://proxy:8096",
	}
	pool := backend.NewPool(db, cfg)
	mediaH := handler.NewMediaHandler(pool, cfg, db)

	r := gin.New()

	priv := r.Group("/")
	priv.Use(middleware.Auth(db, cfg))
	priv.GET("/videos/:itemId/stream", mediaH.StreamVideo)
	priv.GET("/audio/:itemId/lyrics", mediaH.Lyrics)

	r.GET("/items/:itemId/images/:imageType", mediaH.GetImage)
	r.GET("/items/:itemId/images/:imageType/:imageIndex", mediaH.GetImage)
	r.GET("/audio/:itemId/stream", mediaH.StreamAudio)
	r.GET("/audio/:itemId/stream.:container", mediaH.StreamAudio)
	r.GET("/audio/:itemId/universal", mediaH.UniversalAudio)

	proxyID := idtrans.Encode(pbPrefix, pbBackendItemID)
	return r, proxyID
}

// setupPlaybackDB creates the backend, user, backend-user mapping (with token),
// and session needed for playback tests.
func setupPlaybackDB(backendURL string) {
	b, err := db.Backend.Create().
		SetName("Playback Test Backend").
		SetURL(backendURL).
		SetExternalID(pbPrefix).
		Save(mediaCtx())
	Expect(err).NotTo(HaveOccurred())

	u := createUser("pbuser", "password1!", false)
	createBackendUser(b, u, pbBackendUserID, pbBackendToken)
	createSession(u, pbProxyToken)
}

// setupPlaybackDBDirectStream is like setupPlaybackDB but creates a user with
// direct_stream enabled — streaming requests for this user should 302-redirect.
func setupPlaybackDBDirectStream(backendURL string) {
	b, err := db.Backend.Create().
		SetName("Playback Test Backend").
		SetURL(backendURL).
		SetExternalID(pbPrefix).
		Save(mediaCtx())
	Expect(err).NotTo(HaveOccurred())

	u, err := db.User.Create().
		SetUsername("pbuser").
		SetDisplayName("pbuser").
		SetHashedPassword("$2a$04$dummy").
		SetDirectStream(true).
		Save(mediaCtx())
	Expect(err).NotTo(HaveOccurred())

	createBackendUser(b, u, pbBackendUserID, pbBackendToken)
	createSession(u, pbProxyToken)
}

// ═════════════════════════════════════════════════════════════════════════════
// PLAYBACK FLOW
//
//   Phase 1: GetPlaybackInfo  (client ──auth──▶ proxy ──backendToken──▶ backend)
//   Phase 2: HLS playlists    (browser ──ApiKey=proxyToken──▶ proxy ──backendToken──▶ backend)
//   Phase 3: HLS segments     (browser ──ApiKey=proxyToken──▶ proxy ──backendToken──▶ backend)
//
//   Two modes (controlled by user.direct_stream):
//     direct_stream=false (proxy):    proxy fetches bytes from backend, returns them
//     direct_stream=true  (redirect): proxy issues 302 to backend URL
// ═════════════════════════════════════════════════════════════════════════════

var _ = Describe("Playback flow", func() {
	BeforeEach(func() {
		cleanDB()
	})

	// ═══════════════════════════════════════════════════════════════════════
	// PHASE 1: GetPlaybackInfo
	// ═══════════════════════════════════════════════════════════════════════

	Describe("Phase 1: GetPlaybackInfo", func() {
		It("rewrites backend item IDs to proxy-prefixed IDs", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{
					"MediaSources": [{
						"Id": "%s",
						"TranscodingUrl": "/videos/%s/master.m3u8?ApiKey=%s&MediaSourceId=%s",
						"DirectStreamUrl": "/videos/%s/stream?static=true&ApiKey=%s&MediaSourceId=%s"
					}]
				}`, pbBackendItemID,
					pbBackendItemID, pbBackendToken, pbBackendItemID,
					pbBackendItemID, pbBackendToken, pbBackendItemID)
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := playbackRouter()

			w := doPost(router, "/items/"+proxyItemID+"/playbackinfo",
				map[string]interface{}{},
				map[string]string{"X-Emby-Token": pbProxyToken})
			Expect(w.Code).To(Equal(http.StatusOK))

			var resp map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			source := resp["MediaSources"].([]interface{})[0].(map[string]interface{})

			transURL := source["TranscodingUrl"].(string)
			dsURL := source["DirectStreamUrl"].(string)

			Expect(source["Id"]).To(Equal(proxyItemID))
			Expect(transURL).To(ContainSubstring("/videos/" + proxyItemID + "/"))
			Expect(transURL).To(ContainSubstring("MediaSourceId=" + proxyItemID))
			Expect(dsURL).To(ContainSubstring("/videos/" + proxyItemID + "/"))
		})

		It("strips the backend ApiKey and injects the proxy session token", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{
					"MediaSources": [{
						"Id": "%s",
						"TranscodingUrl": "/videos/%s/master.m3u8?ApiKey=%s&MediaSourceId=%s",
						"DirectStreamUrl": "/videos/%s/stream?ApiKey=%s&static=true"
					}]
				}`, pbBackendItemID,
					pbBackendItemID, pbBackendToken, pbBackendItemID,
					pbBackendItemID, pbBackendToken)
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := playbackRouter()

			w := doPost(router, "/items/"+proxyItemID+"/playbackinfo",
				map[string]interface{}{},
				map[string]string{"X-Emby-Token": pbProxyToken})
			Expect(w.Code).To(Equal(http.StatusOK))

			var resp map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			source := resp["MediaSources"].([]interface{})[0].(map[string]interface{})

			transURL := source["TranscodingUrl"].(string)
			dsURL := source["DirectStreamUrl"].(string)

			// Backend secret must NEVER appear in the response.
			Expect(transURL).NotTo(ContainSubstring(pbBackendToken))
			Expect(dsURL).NotTo(ContainSubstring(pbBackendToken))

			// Proxy session token must be present so the browser can auth.
			Expect(transURL).To(ContainSubstring("ApiKey=" + pbProxyToken))
			Expect(dsURL).To(ContainSubstring("ApiKey=" + pbProxyToken))
		})

		It("replaces the backend host with the proxy ExternalURL", func() {
			var backendURL string
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{
					"MediaSources": [{
						"Id": "%s",
						"TranscodingUrl": "%s/videos/%s/master.m3u8?ApiKey=%s"
					}]
				}`, pbBackendItemID, backendURL, pbBackendItemID, pbBackendToken)
			}))
			defer fakeBackend.Close()
			backendURL = fakeBackend.URL

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := playbackRouter()

			w := doPost(router, "/items/"+proxyItemID+"/playbackinfo",
				map[string]interface{}{},
				map[string]string{"X-Emby-Token": pbProxyToken})
			Expect(w.Code).To(Equal(http.StatusOK))

			var resp map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			source := resp["MediaSources"].([]interface{})[0].(map[string]interface{})

			transURL := source["TranscodingUrl"].(string)
			Expect(transURL).To(HavePrefix("http://proxy:8096/"))
			Expect(transURL).NotTo(ContainSubstring(fakeBackend.URL))
		})

		It("handles GET (audio) and POST (video) identically", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{
					"MediaSources": [{"Id": "%s", "DirectStreamUrl": "/audio/%s/stream?ApiKey=%s"}]
				}`, pbBackendItemID, pbBackendItemID, pbBackendToken)
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := playbackRouter()

			// GET (audio playback info — no body)
			w := doGet(router, "/items/"+proxyItemID+"/playbackinfo",
				map[string]string{"X-Emby-Token": pbProxyToken})
			Expect(w.Code).To(Equal(http.StatusOK))

			var resp map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			source := resp["MediaSources"].([]interface{})[0].(map[string]interface{})
			dsURL := source["DirectStreamUrl"].(string)

			Expect(dsURL).NotTo(ContainSubstring(pbBackendToken))
			Expect(dsURL).To(ContainSubstring("ApiKey=" + pbProxyToken))
		})
	})

	// ═══════════════════════════════════════════════════════════════════════
	// PHASE 2: HLS playlists (master.m3u8 / main.m3u8)
	//
	// These are PUBLIC routes. The browser's <video> element makes these
	// requests — it does NOT send X-Emby-Token headers. The only way to
	// identify the user is via the ApiKey query parameter.
	// ═══════════════════════════════════════════════════════════════════════

	Describe("Phase 2: HLS playlists", func() {

		// ── DirectStream OFF (proxy mode) ────────────────────────────────────────────

		Context("DirectStream OFF", func() {
			It("resolves user from ApiKey query param and sends backend token to backend", func() {
				var receivedApiKey string
				var receivedHeader string
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					receivedApiKey = r.URL.Query().Get("ApiKey")
					receivedHeader = r.Header.Get("X-Emby-Token")
					w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
					_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-VERSION:6\n")
				}))
				defer fakeBackend.Close()

				setupPlaybackDB(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				w := doGet(router,
					"/videos/"+proxyItemID+"/master.m3u8?ApiKey="+pbProxyToken,
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				// Backend received the BACKEND token (not the proxy token).
				Expect(receivedApiKey).To(Equal(pbBackendToken))
				Expect(receivedHeader).To(Equal(pbBackendToken))
			})

			It("strips backend ApiKey from playlist URLs and injects proxy token", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
					// Backend includes its own ApiKey in the playlist URLs.
					_, _ = fmt.Fprint(w, strings.Join([]string{
						"#EXTM3U",
						"#EXT-X-VERSION:6",
						"#EXT-X-STREAM-INF:BANDWIDTH=1000000",
						"main.m3u8?ApiKey=" + pbBackendToken + "&MediaSourceId=abc123",
						"",
					}, "\n"))
				}))
				defer fakeBackend.Close()

				setupPlaybackDB(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				w := doGet(router,
					"/videos/"+proxyItemID+"/master.m3u8?ApiKey="+pbProxyToken,
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				body := w.Body.String()

				// Backend token must be stripped from all URLs.
				Expect(body).NotTo(ContainSubstring(pbBackendToken))
				// Proxy token must be injected into URL lines.
				Expect(body).To(ContainSubstring("ApiKey=" + pbProxyToken))
				// Other params preserved.
				Expect(body).To(ContainSubstring("MediaSourceId=abc123"))
				// Comment lines unchanged.
				Expect(body).To(ContainSubstring("#EXT-X-VERSION:6"))
			})

			It("handles variant playlists (main.m3u8) the same way", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
					_, _ = fmt.Fprint(w, strings.Join([]string{
						"#EXTM3U",
						"#EXTINF:6.000000,",
						"hls1/main/0.mp4?ApiKey=" + pbBackendToken,
						"#EXTINF:6.000000,",
						"hls1/main/1.mp4?ApiKey=" + pbBackendToken,
						"",
					}, "\n"))
				}))
				defer fakeBackend.Close()

				setupPlaybackDB(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				w := doGet(router,
					"/videos/"+proxyItemID+"/main.m3u8?ApiKey="+pbProxyToken,
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				body := w.Body.String()

				// Every segment URL must have proxy token, not backend token.
				Expect(body).NotTo(ContainSubstring(pbBackendToken))
				Expect(strings.Count(body, "ApiKey="+pbProxyToken)).To(Equal(2))
			})
		})

		// ── direct_stream=true (redirect mode) ───────────────────────────────────────

		Context("user direct_stream=true", func() {
			It("302-redirects to backend URL with the backend token", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK) // should not be reached
				}))
				defer fakeBackend.Close()

				setupPlaybackDBDirectStream(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				w := doGet(router,
					"/videos/"+proxyItemID+"/master.m3u8?ApiKey="+pbProxyToken,
				)

				Expect(w.Code).To(Equal(http.StatusFound))
				loc := w.Header().Get("Location")

				Expect(loc).To(HavePrefix(fakeBackend.URL + "/"))
				Expect(loc).To(ContainSubstring("/videos/" + pbBackendItemID + "/"))
				Expect(loc).To(ContainSubstring("ApiKey=" + pbBackendToken))
				// The proxy token must NOT leak to the backend redirect URL.
				Expect(loc).NotTo(ContainSubstring("ApiKey=" + pbProxyToken))
			})

			It("proxies when no user can be resolved (direct_stream only applies to known users)", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
					_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-VERSION:6\n")
				}))
				defer fakeBackend.Close()

				setupPlaybackDBDirectStream(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				w := doGet(router,
					"/videos/"+proxyItemID+"/master.m3u8?ApiKey=&MediaSourceId=x",
				)

				// No user resolved → no direct stream → proxy mode.
				Expect(w.Code).To(Equal(http.StatusOK))
			})
		})
	})

	// ═══════════════════════════════════════════════════════════════════════
	// PHASE 3: HLS segments (/hls1/...)
	//
	// The browser fetches these from URLs in the Phase 2 playlist.
	// They carry ApiKey=<proxyToken> (injected by the proxy in Phase 2).
	// They may ALSO carry a leaked backend ApiKey if the playlist wasn't
	// perfectly cleaned — the proxy must handle both gracefully.
	// ═══════════════════════════════════════════════════════════════════════

	Describe("Phase 3: HLS segments", func() {

		// ── DirectStream OFF ─────────────────────────────────────────────────────────

		Context("DirectStream OFF", func() {
			It("proxies segments using the backend token (single ApiKey)", func() {
				var receivedApiKey string
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					receivedApiKey = r.URL.Query().Get("ApiKey")
					w.Header().Set("Content-Type", "video/mp4")
					_, _ = fmt.Fprint(w, "segment-0-bytes")
				}))
				defer fakeBackend.Close()

				setupPlaybackDB(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				// Clean URL: only proxy token (happy path from playlist injection).
				w := doGet(router,
					"/videos/"+proxyItemID+"/hls1/main/0.mp4?ApiKey="+pbProxyToken,
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				Expect(w.Body.String()).To(Equal("segment-0-bytes"))
				Expect(receivedApiKey).To(Equal(pbBackendToken))
			})

			It("handles duplicate ApiKey params (backend token + proxy token)", func() {
				var receivedApiKey string
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					receivedApiKey = r.URL.Query().Get("ApiKey")
					w.Header().Set("Content-Type", "video/mp4")
					_, _ = fmt.Fprint(w, "segment-data")
				}))
				defer fakeBackend.Close()

				setupPlaybackDB(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				// Worst case: both backend and proxy tokens in URL.
				// tryResolveUser must try all candidates and find the valid session.
				w := doGet(router,
					"/videos/"+proxyItemID+"/hls1/main/-1.mp4?ApiKey="+pbBackendToken+"&other=val&ApiKey="+pbProxyToken,
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				Expect(w.Body.String()).To(Equal("segment-data"))
				Expect(receivedApiKey).To(Equal(pbBackendToken))
			})

			It("matches /hls1 as first path component (no session prefix)", func() {
				var gotRequest bool
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					gotRequest = true
					w.Header().Set("Content-Type", "video/mp4")
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()

				setupPlaybackDB(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				w := doGet(router,
					"/videos/"+proxyItemID+"/hls1/main/0.mp4?ApiKey="+pbProxyToken,
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				Expect(gotRequest).To(BeTrue())
			})

			It("matches /{sessionId}/hls1/{segId}/{file} (with session prefix)", func() {
				var gotRequest bool
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					gotRequest = true
					w.Header().Set("Content-Type", "video/mp4")
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()

				setupPlaybackDB(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				w := doGet(router,
					"/videos/"+proxyItemID+"/abc123/hls1/seg0/file.mp4?ApiKey="+pbProxyToken,
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				Expect(gotRequest).To(BeTrue())
			})
		})

		// ── direct_stream=true ───────────────────────────────────────────────────────

		Context("user direct_stream=true", func() {
			It("302-redirects segment requests to the backend with backend token", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK) // should not be reached
				}))
				defer fakeBackend.Close()

				setupPlaybackDBDirectStream(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				w := doGet(router,
					"/videos/"+proxyItemID+"/hls1/main/0.mp4?ApiKey="+pbProxyToken,
				)

				Expect(w.Code).To(Equal(http.StatusFound))
				loc := w.Header().Get("Location")

				Expect(loc).To(HavePrefix(fakeBackend.URL + "/"))
				Expect(loc).To(ContainSubstring("ApiKey=" + pbBackendToken))
				Expect(loc).NotTo(ContainSubstring("ApiKey=" + pbProxyToken))
			})

			It("handles duplicate ApiKey in redirect mode", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()

				setupPlaybackDBDirectStream(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				w := doGet(router,
					"/videos/"+proxyItemID+"/hls1/main/0.mp4?ApiKey="+pbBackendToken+"&ApiKey="+pbProxyToken,
				)

				Expect(w.Code).To(Equal(http.StatusFound))
				loc := w.Header().Get("Location")

				// Only the backend token should be in the redirect.
				Expect(loc).To(ContainSubstring("ApiKey=" + pbBackendToken))
				Expect(loc).NotTo(ContainSubstring("ApiKey=" + pbProxyToken))
			})
		})
	})

	// ═══════════════════════════════════════════════════════════════════════
	// DIRECT STREAM (non-HLS: /stream)
	// ═══════════════════════════════════════════════════════════════════════

	Describe("Video stream (/videos/:id/stream)", func() {
		Context("DirectStream OFF", func() {
			It("proxies the bytes through to the client", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/videos/" + pbBackendItemID + "/stream"))
					w.Header().Set("Content-Type", "video/mp4")
					_, _ = fmt.Fprint(w, "fake-video-bytes")
				}))
				defer fakeBackend.Close()

				setupPlaybackDB(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				w := doGet(router,
					"/videos/"+proxyItemID+"/stream?ApiKey="+pbProxyToken,
				)

				Expect(w.Code).To(Equal(http.StatusOK))
				Expect(w.Body.String()).To(Equal("fake-video-bytes"))
			})
		})

		Context("user direct_stream=true", func() {
			It("redirects to the backend stream URL", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()

				setupPlaybackDBDirectStream(fakeBackend.URL)
				router, proxyItemID := playbackRouter()

				w := doGet(router,
					"/videos/"+proxyItemID+"/stream?ApiKey="+pbProxyToken+"&static=true",
				)

				Expect(w.Code).To(Equal(http.StatusFound))
				loc := w.Header().Get("Location")
				Expect(loc).To(ContainSubstring("/videos/" + pbBackendItemID + "/stream"))
			})
		})
	})

	// ═══════════════════════════════════════════════════════════════════════
	// SECURITY: ApiKey handling invariants
	// ═══════════════════════════════════════════════════════════════════════

	Describe("Security: ApiKey handling", func() {
		It("never forwards the client's proxy token to the backend", func() {
			var receivedRawQuery string
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedRawQuery = r.URL.RawQuery
				w.Header().Set("Content-Type", "video/mp4")
				w.WriteHeader(http.StatusOK)
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := playbackRouter()

			doGet(router,
				"/videos/"+proxyItemID+"/stream?ApiKey="+pbProxyToken+"&static=true",
			)

			// The proxy token must never reach the backend.
			Expect(receivedRawQuery).NotTo(ContainSubstring(pbProxyToken))
			// Other params forwarded normally.
			Expect(receivedRawQuery).To(ContainSubstring("static=true"))
		})

		It("the backend token never appears in PlaybackInfo responses", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{
					"MediaSources": [{
						"Id": "%s",
						"TranscodingUrl": "/videos/%s/master.m3u8?ApiKey=%s",
						"DirectStreamUrl": "/videos/%s/stream?ApiKey=%s"
					}]
				}`, pbBackendItemID, pbBackendItemID, pbBackendToken, pbBackendItemID, pbBackendToken)
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := playbackRouter()

			w := doPost(router, "/items/"+proxyItemID+"/playbackinfo",
				map[string]interface{}{},
				map[string]string{"X-Emby-Token": pbProxyToken})

			// The entire response body must not contain the backend secret.
			Expect(w.Body.String()).NotTo(ContainSubstring(pbBackendToken))
		})

		It("the backend token never appears in HLS playlist responses", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
				_, _ = fmt.Fprintf(w, "#EXTM3U\nmain.m3u8?ApiKey=%s&foo=bar\n", pbBackendToken)
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := playbackRouter()

			w := doGet(router,
				"/videos/"+proxyItemID+"/master.m3u8?ApiKey="+pbProxyToken,
			)

			Expect(w.Body.String()).NotTo(ContainSubstring(pbBackendToken))
		})
	})

	// ═══════════════════════════════════════════════════════════════════════
	// StreamVideo (authenticated)
	// ═══════════════════════════════════════════════════════════════════════

	Describe("StreamVideo", func() {
		It("proxies video stream bytes from the backend", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/videos/" + pbBackendItemID + "/stream"))
				w.Header().Set("Content-Type", "video/mp4")
				_, _ = fmt.Fprint(w, "video-bytes")
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := streamRouter()

			w := doGet(router, "/videos/"+proxyItemID+"/stream",
				map[string]string{"X-Emby-Token": pbProxyToken})
			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal("video-bytes"))
		})

		It("returns 400 for invalid item ID", func() {
			setupPlaybackDB("http://unused")
			router, _ := streamRouter()

			w := doGet(router, "/videos/invalid-id/stream",
				map[string]string{"X-Emby-Token": pbProxyToken})
			Expect(w.Code).To(Equal(http.StatusBadRequest))
		})
	})

	// ═══════════════════════════════════════════════════════════════════════
	// StreamAudio (public)
	// ═══════════════════════════════════════════════════════════════════════

	Describe("StreamAudio", func() {
		It("proxies audio stream bytes", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/audio/" + pbBackendItemID + "/stream"))
				w.Header().Set("Content-Type", "audio/mp4")
				_, _ = fmt.Fprint(w, "audio-bytes")
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := streamRouter()

			w := doGet(router, "/audio/"+proxyItemID+"/stream?api_key="+pbProxyToken)
			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal("audio-bytes"))
		})

		It("handles stream with container extension", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/audio/" + pbBackendItemID + "/stream.mp3"))
				w.Header().Set("Content-Type", "audio/mpeg")
				_, _ = fmt.Fprint(w, "mp3-bytes")
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := streamRouter()

			w := doGet(router, "/audio/"+proxyItemID+"/stream.mp3?api_key="+pbProxyToken)
			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal("mp3-bytes"))
		})

		It("returns 400 for invalid item ID", func() {
			setupPlaybackDB("http://unused")
			router, _ := streamRouter()

			w := doGet(router, "/audio/invalid-id/stream?api_key="+pbProxyToken)
			Expect(w.Code).To(Equal(http.StatusBadRequest))
		})
	})

	// ═══════════════════════════════════════════════════════════════════════
	// UniversalAudio (public)
	// ═══════════════════════════════════════════════════════════════════════

	Describe("UniversalAudio", func() {
		It("proxies universal audio endpoint", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/audio/" + pbBackendItemID + "/universal"))
				w.Header().Set("Content-Type", "audio/aac")
				_, _ = fmt.Fprint(w, "universal-audio-bytes")
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := streamRouter()

			w := doGet(router, "/audio/"+proxyItemID+"/universal?api_key="+pbProxyToken)
			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal("universal-audio-bytes"))
		})

		It("returns 400 for invalid item ID", func() {
			setupPlaybackDB("http://unused")
			router, _ := streamRouter()

			w := doGet(router, "/audio/invalid-id/universal?api_key="+pbProxyToken)
			Expect(w.Code).To(Equal(http.StatusBadRequest))
		})
	})

	// ═══════════════════════════════════════════════════════════════════════
	// GetImage (public)
	// ═══════════════════════════════════════════════════════════════════════

	Describe("GetImage", func() {
		It("proxies image bytes from the backend", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/items/" + pbBackendItemID + "/images/primary"))
				w.Header().Set("Content-Type", "image/jpeg")
				_, _ = fmt.Fprint(w, "image-bytes")
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := streamRouter()

			w := doGet(router, "/items/"+proxyItemID+"/images/primary?api_key="+pbProxyToken)
			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(Equal("image-bytes"))
		})

		It("includes image index in the path when present", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/items/" + pbBackendItemID + "/images/backdrop/0"))
				w.Header().Set("Content-Type", "image/jpeg")
				w.WriteHeader(http.StatusOK)
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := streamRouter()

			w := doGet(router, "/items/"+proxyItemID+"/images/backdrop/0?api_key="+pbProxyToken)
			Expect(w.Code).To(Equal(http.StatusOK))
		})

		It("returns 400 for invalid item ID", func() {
			setupPlaybackDB("http://unused")
			router, _ := streamRouter()

			w := doGet(router, "/items/invalid-id/images/primary?api_key="+pbProxyToken)
			Expect(w.Code).To(Equal(http.StatusBadRequest))
		})
	})

	// ═══════════════════════════════════════════════════════════════════════
	// VideoSubpath: subtitle handling
	// ═══════════════════════════════════════════════════════════════════════

	Describe("VideoSubpath subtitle routing", func() {
		It("routes subtitle paths correctly", func() {
			msProxyID := idtrans.Encode(pbPrefix, "mediasource123")
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(ContainSubstring("/subtitles/"))
				w.Header().Set("Content-Type", "text/vtt")
				_, _ = fmt.Fprint(w, "WEBVTT\n\nsubtitle content")
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := playbackRouter()

			w := doGet(router, "/videos/"+proxyItemID+"/"+msProxyID+"/subtitles/0/stream.vtt?api_key="+pbProxyToken)
			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(w.Body.String()).To(ContainSubstring("WEBVTT"))
		})
	})

	// ═══════════════════════════════════════════════════════════════════════
	// forwardPlaybackReport
	// ═══════════════════════════════════════════════════════════════════════

	Describe("forwardPlaybackReport", func() {
		It("forwards Playing report to the backend", func() {
			var receivedPath string
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedPath = r.URL.Path
				w.WriteHeader(http.StatusNoContent)
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := playbackRouter()

			w := doPost(router, "/sessions/playing",
				map[string]interface{}{"ItemId": proxyItemID},
				map[string]string{"X-Emby-Token": pbProxyToken})
			Expect(w.Code).To(Equal(http.StatusNoContent))
			Expect(receivedPath).To(Equal("/sessions/Playing"))
		})

		It("returns 204 when body is empty", func() {
			setupPlaybackDB("http://unused")
			router, _ := playbackRouter()

			// Send a POST with genuinely empty body
			req, _ := http.NewRequest(http.MethodPost, "/sessions/playing", http.NoBody)
			req.Header.Set("X-Emby-Token", pbProxyToken)
			req.RemoteAddr = "127.0.0.1:12345"
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			Expect(w.Code).To(Equal(http.StatusNoContent))
		})

		It("returns 204 when ItemId is missing", func() {
			setupPlaybackDB("http://unused")
			router, _ := playbackRouter()

			w := doPost(router, "/sessions/playing",
				map[string]interface{}{"SomeField": "value"},
				map[string]string{"X-Emby-Token": pbProxyToken})
			Expect(w.Code).To(Equal(http.StatusNoContent))
		})

		It("returns 204 when ItemId cannot be decoded", func() {
			setupPlaybackDB("http://unused")
			router, _ := playbackRouter()

			w := doPost(router, "/sessions/playing",
				map[string]interface{}{"ItemId": "unknownid"},
				map[string]string{"X-Emby-Token": pbProxyToken})
			Expect(w.Code).To(Equal(http.StatusNoContent))
		})
	})

	// ═══════════════════════════════════════════════════════════════════════
	// HLS playlist token injection: #EXT-X-MAP:URI handling
	// ═══════════════════════════════════════════════════════════════════════

	Describe("HLS playlist URI tag injection", func() {
		It("injects token into #EXT-X-MAP:URI tags", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
				_, _ = fmt.Fprint(w, strings.Join([]string{
					"#EXTM3U",
					`#EXT-X-MAP:URI="init.mp4"`,
					"#EXTINF:6.0,",
					"segment0.mp4",
					"",
				}, "\n"))
			}))
			defer fakeBackend.Close()

			setupPlaybackDB(fakeBackend.URL)
			router, proxyItemID := playbackRouter()

			w := doGet(router, "/videos/"+proxyItemID+"/main.m3u8?ApiKey="+pbProxyToken)
			Expect(w.Code).To(Equal(http.StatusOK))
			body := w.Body.String()
			// URI in the tag should have ApiKey injected
			Expect(body).To(ContainSubstring(`URI="init.mp4?ApiKey=` + pbProxyToken + `"`))
			// Segment URLs should also have the token
			Expect(body).To(ContainSubstring("segment0.mp4?ApiKey=" + pbProxyToken))
		})
	})
})
