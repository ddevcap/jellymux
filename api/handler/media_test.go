package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"

	"github.com/ddevcap/jellymux/api/handler"
	"github.com/ddevcap/jellymux/api/middleware"
	"github.com/ddevcap/jellymux/backend"
	"github.com/ddevcap/jellymux/config"
	"github.com/ddevcap/jellymux/idtrans"
)

// mediaCtx is a convenience shorthand for tests in this file.
func mediaCtx() context.Context { return context.Background() }

const (
	mediaTestPrefix    = "t1"
	mediaTestBackendID = "aabbccdd11223344aabbccdd11223344"
	mediaTestToken     = "media-test-session-token"
	liveTvToken        = "livetv-test-session-token"
)

// mediaTestRouter builds a gin router with the three new endpoints wired to a
// MediaHandler. The Download route is public; Lyrics and GetCollectionItems sit
// behind the Auth middleware.
func mediaTestRouter() (*gin.Engine, string) {
	cfg := config.Config{
		ServerID:   "test-server-id",
		ServerName: "Test Proxy",
	}
	pool := backend.NewPool(db, cfg)
	mediaH := handler.NewMediaHandler(pool, cfg, db)

	r := gin.New()

	// Public route (api_key / no session cookie required).
	r.GET("/items/:itemId/download", mediaH.Download)

	// Authenticated routes.
	priv := r.Group("/")
	priv.Use(middleware.Auth(db, cfg))
	priv.GET("/audio/:itemId/lyrics", mediaH.Lyrics)
	priv.GET("/collections/:itemId/items", mediaH.GetCollectionItems)
	priv.GET("/shows/:seriesId/episodes", mediaH.GetEpisodes)

	proxyID := idtrans.Encode(mediaTestPrefix, mediaTestBackendID)
	return r, proxyID
}

// liveTvRouter builds a minimal router with the three Live TV endpoints.
func liveTvRouter() *gin.Engine {
	cfg := config.Config{ServerID: "test-server-id", ServerName: "Test Proxy"}
	pool := backend.NewPool(db, cfg)
	mediaH := handler.NewMediaHandler(pool, cfg, db)

	r := gin.New()
	priv := r.Group("/")
	priv.Use(middleware.Auth(db, cfg))
	priv.GET("/livetv/channels", mediaH.GetLiveTvChannels)
	priv.GET("/livetv/programs", mediaH.GetLiveTvPrograms)
	priv.GET("/livetv/programs/recommended", mediaH.GetLiveTvRecommendedPrograms)
	priv.GET("/livetv/info", mediaH.GetLiveTvInfo)
	return r
}

// registerLiveTvBackend adds a backend + user mapping for the single user
// currently in the database (created by the Live TV BeforeEach).
func registerLiveTvBackend(name, url, externalID, backendUserID string) {
	b, err := db.Backend.Create().
		SetName(name).
		SetURL(url).
		SetExternalID(externalID).
		Save(mediaCtx())
	Expect(err).NotTo(HaveOccurred())

	u, err := db.User.Query().Only(mediaCtx())
	Expect(err).NotTo(HaveOccurred())
	createBackendUser(b, u, backendUserID)
}

// setupMediaDB registers fakeBackendURL as a backend, creates a proxy user and
// mapping, and creates a session. Returns the session token.
func setupMediaDB(fakeBackendURL string) string {
	b, err := db.Backend.Create().
		SetName("Test Backend").
		SetURL(fakeBackendURL).
		SetExternalID(mediaTestPrefix).
		Save(mediaCtx())
	Expect(err).NotTo(HaveOccurred())

	u := createUser("mediatest", "password1!", false)
	createBackendUser(b, u, "backend-user-id", "backend-test-token")
	createSession(u, mediaTestToken)

	return mediaTestToken
}

// setupMediaDBDirectStream is like setupMediaDB but creates a user with
// direct_stream enabled — streaming/download requests for this user should 302-redirect.
func setupMediaDBDirectStream(fakeBackendURL string) string {
	b, err := db.Backend.Create().
		SetName("Test Backend").
		SetURL(fakeBackendURL).
		SetExternalID(mediaTestPrefix).
		Save(mediaCtx())
	Expect(err).NotTo(HaveOccurred())

	u, err := db.User.Create().
		SetUsername("mediatest").
		SetDisplayName("mediatest").
		SetHashedPassword("$2a$04$dummy").
		SetDirectStream(true).
		Save(mediaCtx())
	Expect(err).NotTo(HaveOccurred())

	createBackendUser(b, u, "backend-user-id", "backend-test-token")
	createSession(u, mediaTestToken)

	return mediaTestToken
}

var _ = Describe("MediaHandler", func() {
	BeforeEach(func() {
		cleanDB()
	})

	// ── Download ─────────────────────────────────────────────────────────────────

	Describe("Download", func() {
		Context("when the backend returns the file", func() {
			It("streams the response body and status back to the client", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/items/" + mediaTestBackendID + "/download"))
					w.Header().Set("Content-Type", "video/mp4")
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprint(w, "fake-file-bytes")
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				w := doGet(router, "/items/"+proxyID+"/download",
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusOK))
				Expect(w.Body.String()).To(Equal("fake-file-bytes"))
			})
		})

		Context("when the item ID has no prefix", func() {
			It("returns 400", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, _ := mediaTestRouter()

				w := doGet(router, "/items/no-prefix-id/download",
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("with user direct_stream enabled", func() {
			It("redirects the client directly to the backend URL", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK) // should not be reached
				}))
				defer fakeBackend.Close()

				token := setupMediaDBDirectStream(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				w := doGet(router, "/items/"+proxyID+"/download",
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusFound))
				Expect(w.Header().Get("Location")).To(
					SatisfyAll(
						ContainSubstring("/items/"+mediaTestBackendID+"/download"),
						ContainSubstring("ApiKey="),
					),
				)
			})
		})
	})

	// ── Lyrics ───────────────────────────────────────────────────────────────────

	Describe("Lyrics", func() {
		Context("when the backend returns lyrics", func() {
			It("returns 200 with the lyrics JSON", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/audio/" + mediaTestBackendID + "/lyrics"))
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprint(w, `{"Lyrics":[{"Text":"Hello","Start":0}]}`)
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				w := doGet(router, "/audio/"+proxyID+"/lyrics",
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).To(HaveKey("Lyrics"))
			})
		})

		Context("when the backend returns 404", func() {
			It("passes through the 404 status", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNotFound)
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				w := doGet(router, "/audio/"+proxyID+"/lyrics",
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusNotFound))
			})
		})

		Context("when the item ID has no prefix", func() {
			It("returns 400", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, _ := mediaTestRouter()

				w := doGet(router, "/audio/no-prefix-id/lyrics",
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("without authentication", func() {
			It("returns 401", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()

				setupMediaDB(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				w := doGet(router, "/audio/"+proxyID+"/lyrics") // no auth header

				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	// ── GetCollectionItems ───────────────────────────────────────────────────────

	Describe("GetCollectionItems", func() {
		Context("when the backend returns collection items", func() {
			It("returns 200 and rewrites item IDs to proxy-prefixed IDs", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/collections/" + mediaTestBackendID + "/items"))
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprintf(w,
						`{"Items":[{"Id":%q,"Name":"Item One"}],"TotalRecordCount":1,"StartIndex":0}`,
						mediaTestBackendID,
					)
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				w := doGet(router, "/collections/"+proxyID+"/items",
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 1))
				items := resp["Items"].([]interface{})
				Expect(items).To(HaveLen(1))
				// The proxy should have rewritten the bare backend ID to a UUID.
				item := items[0].(map[string]interface{})
				Expect(item["Id"]).To(MatchRegexp(`^[0-9a-f]{32}$`))
			})
		})

		Context("when the backend returns an empty collection", func() {
			It("returns 200 with an empty Items array", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0,"StartIndex":0}`)
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				w := doGet(router, "/collections/"+proxyID+"/items",
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 0))
				Expect(resp["Items"].([]interface{})).To(BeEmpty())
			})
		})

		Context("when the item ID has no prefix", func() {
			It("returns 400", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, _ := mediaTestRouter()

				w := doGet(router, "/collections/no-prefix-id/items",
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})

		Context("without authentication", func() {
			It("returns 401", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()

				setupMediaDB(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				w := doGet(router, "/collections/"+proxyID+"/items") // no auth header

				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	// ── GetEpisodes ──────────────────────────────────────────────────────────────

	Describe("GetEpisodes", func() {
		Context("when the backend returns episodes", func() {
			It("returns 200 with rewritten IDs", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/shows/" + mediaTestBackendID + "/episodes"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w,
						`{"Items":[{"Id":%q,"Name":"Episode 1"}],"TotalRecordCount":1,"StartIndex":0}`,
						mediaTestBackendID)
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				w := doGet(router, "/shows/"+proxyID+"/episodes",
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				items := resp["Items"].([]interface{})
				Expect(items).To(HaveLen(1))
				item := items[0].(map[string]interface{})
				Expect(item["Id"]).To(MatchRegexp(`^[0-9a-f]{32}$`))
			})
		})

		Context("when startItemId is a proxy-prefixed ID", func() {
			It("strips the proxy prefix before forwarding to the backend", func() {
				var receivedStartItemId string
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					receivedStartItemId = r.URL.Query().Get("StartItemId")
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0,"StartIndex":0}`)
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				startItemProxyID := idtrans.Encode(mediaTestPrefix, "deadbeef1234")
				w := doGet(router,
					"/shows/"+proxyID+"/episodes?startItemId="+startItemProxyID+"&limit=100",
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusOK))
				// The backend must receive the bare ID, not the proxy-prefixed one.
				Expect(receivedStartItemId).To(Equal("deadbeef1234"))
			})
		})

		Context("when AdjacentTo is a proxy-prefixed ID", func() {
			It("strips the proxy prefix before forwarding to the backend", func() {
				var receivedAdjacentTo string
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					receivedAdjacentTo = r.URL.Query().Get("AdjacentTo")
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0,"StartIndex":0}`)
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				adjacentProxyID := idtrans.Encode(mediaTestPrefix, "cafebabe5678")
				w := doGet(router,
					"/shows/"+proxyID+"/episodes?AdjacentTo="+adjacentProxyID,
					map[string]string{"X-Emby-Token": token})

				Expect(w.Code).To(Equal(http.StatusOK))
				Expect(receivedAdjacentTo).To(Equal("cafebabe5678"))
			})
		})

		Context("without authentication", func() {
			It("returns 401", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()

				setupMediaDB(fakeBackend.URL)
				router, proxyID := mediaTestRouter()

				w := doGet(router, "/shows/"+proxyID+"/episodes")
				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})

		Context("with an invalid series ID", func() {
			It("returns 400", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()

				token := setupMediaDB(fakeBackend.URL)
				router, _ := mediaTestRouter()

				w := doGet(router, "/shows/invalid-series-id/episodes",
					map[string]string{"X-Emby-Token": token})
				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})
})

var _ = Describe("Live TV handlers", func() {
	var router *gin.Engine

	BeforeEach(func() {
		cleanDB()
		u := createUser("livetv-user", "password1!", false)
		createSession(u, liveTvToken)
		router = liveTvRouter()
	})

	// ── GetLiveTvChannels ────────────────────────────────────────────────────────

	Describe("GetLiveTvChannels", func() {
		Context("with a single backend", func() {
			It("returns 200 with the channels from that backend", func() {
				fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/livetv/Channels"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"ch1","Name":"BBC One"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fake.Close()
				registerLiveTvBackend("Backend A", fake.URL, "ba", "user-ba")

				w := doGet(router, "/livetv/channels", map[string]string{"X-Emby-Token": liveTvToken})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 1))
				items := resp["Items"].([]interface{})
				Expect(items).To(HaveLen(1))
				Expect(items[0].(map[string]interface{})["Name"]).To(Equal("BBC One"))
			})
		})

		Context("with two backends", func() {
			It("merges channels from both backends into a single response", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"ch1","Name":"BBC One"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"ch2","Name":"CNN"},{"Id":"ch3","Name":"ESPN"}],"TotalRecordCount":2,"StartIndex":0}`)
				}))
				defer fakeB.Close()

				registerLiveTvBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerLiveTvBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/livetv/channels", map[string]string{"X-Emby-Token": liveTvToken})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 3))
				Expect(resp["Items"].([]interface{})).To(HaveLen(3))
			})
		})

		Context("when one backend errors", func() {
			It("returns channels from the healthy backend and silently skips the failing one", func() {
				fakeOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"ch1","Name":"BBC One"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeOK.Close()

				fakeBad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				defer fakeBad.Close()

				registerLiveTvBackend("Good", fakeOK.URL, "ok", "user-ok")
				registerLiveTvBackend("Bad", fakeBad.URL, "bd", "user-bd")

				w := doGet(router, "/livetv/channels", map[string]string{"X-Emby-Token": liveTvToken})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 1))
			})
		})

		Context("when the user has no backend mappings", func() {
			It("returns 200 with an empty Items array", func() {
				w := doGet(router, "/livetv/channels", map[string]string{"X-Emby-Token": liveTvToken})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 0))
				Expect(resp["Items"].([]interface{})).To(BeEmpty())
			})
		})

		Context("without authentication", func() {
			It("returns 401", func() {
				w := doGet(router, "/livetv/channels")
				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	// ── GetLiveTvPrograms ────────────────────────────────────────────────────────

	Describe("GetLiveTvPrograms", func() {
		Context("with two backends", func() {
			It("merges programs from both backends into a single response", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/livetv/Programs"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"p1","Name":"News at Ten"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/livetv/Programs"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"p2","Name":"Football"},{"Id":"p3","Name":"Documentary"}],"TotalRecordCount":2,"StartIndex":0}`)
				}))
				defer fakeB.Close()

				registerLiveTvBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerLiveTvBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/livetv/programs", map[string]string{"X-Emby-Token": liveTvToken})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 3))
				Expect(resp["Items"].([]interface{})).To(HaveLen(3))
			})
		})

		Context("when the user has no backend mappings", func() {
			It("returns 200 with an empty Items array", func() {
				w := doGet(router, "/livetv/programs", map[string]string{"X-Emby-Token": liveTvToken})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["Items"].([]interface{})).To(BeEmpty())
			})
		})

		Context("without authentication", func() {
			It("returns 401", func() {
				w := doGet(router, "/livetv/programs")
				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	// ── GetLiveTvRecommendedPrograms ─────────────────────────────────────────────

	Describe("GetLiveTvRecommendedPrograms", func() {
		Context("with two backends", func() {
			It("merges recommended programs from both backends into a single response", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/livetv/Programs/Recommended"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"r1","Name":"Top Gear"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/livetv/Programs/Recommended"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"r2","Name":"The News"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeB.Close()

				registerLiveTvBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerLiveTvBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/livetv/programs/recommended", map[string]string{"X-Emby-Token": liveTvToken})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 2))
				Expect(resp["Items"].([]interface{})).To(HaveLen(2))
			})
		})

		Context("without authentication", func() {
			It("returns 401", func() {
				w := doGet(router, "/livetv/programs/recommended")
				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	// ── GetLiveTvInfo ────────────────────────────────────────────────────────────

	Describe("GetLiveTvInfo", func() {
		Context("with a single backend", func() {
			It("returns 200 with the info from that backend", func() {
				fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/livetv/Info"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"IsEnabled":true,"RecordingCount":2}`)
				}))
				defer fake.Close()
				registerLiveTvBackend("Backend A", fake.URL, "ba", "user-ba")

				w := doGet(router, "/livetv/info", map[string]string{"X-Emby-Token": liveTvToken})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["IsEnabled"]).To(BeTrue())
				Expect(resp["RecordingCount"]).To(BeNumerically("==", 2))
			})
		})

		Context("with two backends", func() {
			It("returns info from the first backend only", func() {
				callCount := 0
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					callCount++
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"IsEnabled":true,"RecordingCount":5}`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					callCount++
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"IsEnabled":true,"RecordingCount":99}`)
				}))
				defer fakeB.Close()

				registerLiveTvBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerLiveTvBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/livetv/info", map[string]string{"X-Emby-Token": liveTvToken})

				Expect(w.Code).To(Equal(http.StatusOK))
				Expect(callCount).To(Equal(1))
			})
		})

		Context("when the user has no backend mappings", func() {
			It("returns 200 with an empty object", func() {
				w := doGet(router, "/livetv/info", map[string]string{"X-Emby-Token": liveTvToken})

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp).To(BeEmpty())
			})
		})

		Context("without authentication", func() {
			It("returns 401", func() {
				w := doGet(router, "/livetv/info")
				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})
	})
})
