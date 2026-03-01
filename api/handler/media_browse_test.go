package handler_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gin-gonic/gin"

	"github.com/ddevcap/jellyfin-proxy/api/handler"
	"github.com/ddevcap/jellyfin-proxy/api/middleware"
	"github.com/ddevcap/jellyfin-proxy/backend"
	"github.com/ddevcap/jellyfin-proxy/config"
	"github.com/ddevcap/jellyfin-proxy/idtrans"
)

const (
	browsePrefix    = "bw"
	browseBackendID = "deadbeef12345678deadbeef12345678"
	browseToken     = "browse-test-session-token"
)

// browseRouter builds a router with all the endpoints tested in this file.
func browseRouter() (*gin.Engine, string) {
	cfg := config.Config{ServerID: "test-server-id", ServerName: "Test Proxy"}
	pool := backend.NewPool(db, cfg)
	mediaH := handler.NewMediaHandler(pool, cfg, db)

	r := gin.New()
	priv := r.Group("/")
	priv.Use(middleware.Auth(db, cfg))

	// TV Shows — fixed paths before parameterized paths
	priv.GET("/shows/nextup", mediaH.GetNextUp)
	priv.GET("/shows/upcoming", mediaH.GetUpcomingEpisodes)
	priv.GET("/shows/:seriesId/seasons", mediaH.GetSeasons)
	priv.GET("/shows/:seriesId/similar", mediaH.GetSimilarShows)

	// Movies
	priv.GET("/movies/:itemId/similar", mediaH.GetSimilarMovies)

	// Items
	priv.GET("/items/:itemId/similar", mediaH.GetSimilarItems)
	priv.GET("/items/:itemId/children", mediaH.GetItemChildren)
	priv.GET("/items/:itemId/specialfeatures", mediaH.GetSpecialFeatures)
	priv.GET("/items/:itemId/thememedia", mediaH.GetThemeMedia)
	priv.POST("/items/:itemId", mediaH.UpdateItem)
	priv.DELETE("/items/:itemId", mediaH.DeleteItem)
	priv.POST("/items/:itemId/refresh", mediaH.RefreshItem)
	priv.GET("/mediasegments/:itemId", mediaH.GetMediaSegments)

	// Users item actions
	priv.GET("/users/:userId/items/:itemId/localtrailers", mediaH.GetLocalTrailers)
	priv.GET("/users/:userId/items/:itemId/intros", mediaH.GetIntros)
	priv.POST("/users/:userId/favoriteitems/:itemId", mediaH.MarkFavorite)
	priv.DELETE("/users/:userId/favoriteitems/:itemId", mediaH.UnmarkFavorite)
	priv.POST("/users/:userId/playeditems/:itemId", mediaH.MarkPlayed)
	priv.DELETE("/users/:userId/playeditems/:itemId", mediaH.UnmarkPlayed)
	priv.POST("/users/:userId/items/:itemId/rating", mediaH.UpdateUserItemRating)
	priv.POST("/users/:userId/configuration", mediaH.UpdateUserConfiguration)
	priv.POST("/users/:userId/policy", mediaH.UpdateUserPolicy)

	// Aggregated browse endpoints
	priv.GET("/artists", mediaH.GetArtists)
	priv.GET("/artists/albumartists", mediaH.GetAlbumArtists)
	priv.GET("/genres", mediaH.GetGenres)
	priv.GET("/musicgenres", mediaH.GetMusicGenres)
	priv.GET("/studios", mediaH.GetStudios)
	priv.GET("/persons", mediaH.GetPersons)
	priv.GET("/channels", mediaH.GetChannels)
	priv.GET("/search/hints", mediaH.SearchHints)
	priv.GET("/trailers", mediaH.GetTrailers)

	// Playlists
	priv.GET("/playlists", mediaH.GetPlaylists)
	priv.GET("/playlists/:itemId/items", mediaH.GetPlaylistItems)

	// Static stubs
	priv.GET("/syncplay/list", mediaH.SyncPlayList)
	priv.GET("/sessions", mediaH.GetSessions)
	priv.GET("/scheduledtasks", mediaH.GetScheduledTasks)
	priv.GET("/plugins", mediaH.GetInstalledPlugins)
	priv.GET("/notifications/summary", mediaH.GetNotificationsSummary)

	// Users
	priv.GET("/users", mediaH.GetUsers)

	// Playback reports
	priv.POST("/sessions/playing", mediaH.ReportPlaybackStart)
	priv.POST("/sessions/playing/progress", mediaH.ReportPlaybackProgress)
	priv.POST("/sessions/playing/stopped", mediaH.ReportPlaybackStopped)

	// Images (public)
	r.GET("/items/:itemId/images/:imageType", mediaH.GetImage)
	r.GET("/items/:itemId/images/:imageType/:imageIndex", mediaH.GetImage)

	// Audio streams (public)
	r.GET("/audio/:itemId/stream", mediaH.StreamAudio)
	r.GET("/audio/:itemId/universal", mediaH.UniversalAudio)

	proxyID := idtrans.Encode(browsePrefix, browseBackendID)
	return r, proxyID
}

func setupBrowseDB(fakeURL string) {
	b, err := db.Backend.Create().
		SetName("Browse Backend").
		SetURL(fakeURL).
		SetExternalID(browsePrefix).
		Save(mediaCtx())
	Expect(err).NotTo(HaveOccurred())

	u := createUser("browseuser", "password1!", false)
	createBackendUser(b, u, "backend-user-browse", "browse-backend-token")
	createSession(u, browseToken)
}

func browseAuth() map[string]string {
	return map[string]string{"X-Emby-Token": browseToken}
}

// pagedJSON returns a standard Jellyfin paged response with a single item.
func pagedJSON(items string) string {
	return fmt.Sprintf(`{"Items":[%s],"TotalRecordCount":1,"StartIndex":0}`, items)
}

// ── Route-by-ID endpoints (single backend proxy) ─────────────────────────────

var _ = Describe("Route-by-ID browse endpoints", func() {
	var (
		router  *gin.Engine
		proxyID string
	)

	BeforeEach(func() {
		cleanDB()
		router, proxyID = browseRouter()
	})

	// Each of these has the same pattern: route by proxy ID → proxy to backend → return result.
	// We test: happy path (correct backend path), bad prefix (400), and no auth (401).

	type routeByIDCase struct {
		describe    string
		method      string
		pathFmt     string // %s will be replaced by proxyID
		backendPath string // expected path on the backend (%s = backendID)
		backendResp string
	}

	cases := []routeByIDCase{
		{"GetSeasons", "GET", "/shows/%s/seasons", "/shows/" + browseBackendID + "/seasons", pagedJSON(`{"Id":"s1","Name":"Season 1"}`)},
		{"GetSimilarItems", "GET", "/items/%s/similar", "/items/" + browseBackendID + "/similar", pagedJSON(`{"Id":"sim1","Name":"Similar"}`)},
		{"GetSimilarMovies", "GET", "/movies/%s/similar", "/movies/" + browseBackendID + "/similar", pagedJSON(`{"Id":"sim1","Name":"Similar"}`)},
		{"GetSimilarShows", "GET", "/shows/%s/similar", "/shows/" + browseBackendID + "/similar", pagedJSON(`{"Id":"sim1","Name":"Similar"}`)},
		{"GetItemChildren", "GET", "/items/%s/children", "/items/" + browseBackendID + "/children", pagedJSON(`{"Id":"ch1","Name":"Child"}`)},
		{"GetSpecialFeatures", "GET", "/items/%s/specialfeatures", "/items/" + browseBackendID + "/specialfeatures", `[{"Id":"sf1","Name":"Behind the Scenes"}]`},
		{"GetThemeMedia", "GET", "/items/%s/thememedia", "/items/" + browseBackendID + "/thememedia", `{"ThemeSongsResult":{"Items":[],"TotalRecordCount":0},"ThemeVideosResult":{"Items":[],"TotalRecordCount":0}}`},
		{"GetPlaylistItems", "GET", "/playlists/%s/items", "/playlists/" + browseBackendID + "/items", pagedJSON(`{"Id":"pi1","Name":"Track 1"}`)},
	}

	for _, tc := range cases {
		tc := tc // capture
		Describe(tc.describe, func() {
			Context("when the backend returns data", func() {
				It("returns 200 with the proxied response", func() {
					fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						Expect(r.URL.Path).To(Equal(tc.backendPath))
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprint(w, tc.backendResp)
					}))
					defer fakeBackend.Close()
					setupBrowseDB(fakeBackend.URL)

					path := fmt.Sprintf(tc.pathFmt, proxyID)
					w := doRequest(router, tc.method, path, nil, browseAuth())
					Expect(w.Code).To(Equal(http.StatusOK))
				})
			})

			Context("when the item ID has no prefix", func() {
				It("returns 400", func() {
					fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusOK)
					}))
					defer fakeBackend.Close()
					setupBrowseDB(fakeBackend.URL)

					path := fmt.Sprintf(tc.pathFmt, "noprefixid")
					w := doRequest(router, tc.method, path, nil, browseAuth())
					Expect(w.Code).To(Equal(http.StatusBadRequest))
				})
			})

			Context("without authentication", func() {
				It("returns 401", func() {
					path := fmt.Sprintf(tc.pathFmt, proxyID)
					w := doRequest(router, tc.method, path, nil)
					Expect(w.Code).To(Equal(http.StatusUnauthorized))
				})
			})
		})
	}

	// ── User-scoped item routes ──────────────────────────────────────────────────

	Describe("GetLocalTrailers", func() {
		It("proxies to the backend with the correct user path", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(ContainSubstring("/localtrailers"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `[]`)
			}))
			defer fakeBackend.Close()
			setupBrowseDB(fakeBackend.URL)

			w := doGet(router, "/users/ignored/items/"+proxyID+"/localtrailers", browseAuth())
			Expect(w.Code).To(Equal(http.StatusOK))
		})
	})

	Describe("GetIntros", func() {
		It("proxies to the backend", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(ContainSubstring("/intros"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
			}))
			defer fakeBackend.Close()
			setupBrowseDB(fakeBackend.URL)

			w := doGet(router, "/users/ignored/items/"+proxyID+"/intros", browseAuth())
			Expect(w.Code).To(Equal(http.StatusOK))
		})
	})

	// ── Mutating item actions ────────────────────────────────────────────────────

	type userItemCase struct {
		describe string
		method   string
		pathFmt  string
	}

	userItemCases := []userItemCase{
		{"MarkFavorite", "POST", "/users/ignored/favoriteitems/%s"},
		{"UnmarkFavorite", "DELETE", "/users/ignored/favoriteitems/%s"},
		{"MarkPlayed", "POST", "/users/ignored/playeditems/%s"},
		{"UnmarkPlayed", "DELETE", "/users/ignored/playeditems/%s"},
		{"UpdateUserItemRating", "POST", "/users/ignored/items/%s/rating"},
	}

	for _, tc := range userItemCases {
		tc := tc
		Describe(tc.describe, func() {
			It("returns success from the backend", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"IsFavorite":true}`)
				}))
				defer fakeBackend.Close()
				setupBrowseDB(fakeBackend.URL)

				path := fmt.Sprintf(tc.pathFmt, proxyID)
				w := doRequest(router, tc.method, path, map[string]interface{}{}, browseAuth())
				Expect(w.Code).To(Equal(http.StatusOK))
			})

			It("returns 400 for unprefixed ID", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				defer fakeBackend.Close()
				setupBrowseDB(fakeBackend.URL)

				path := fmt.Sprintf(tc.pathFmt, "noprefixid")
				w := doRequest(router, tc.method, path, map[string]interface{}{}, browseAuth())
				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})

			It("returns 401 without auth", func() {
				path := fmt.Sprintf(tc.pathFmt, proxyID)
				w := doRequest(router, tc.method, path, map[string]interface{}{})
				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})
	}

	// ── UpdateItem (POST /Items/:itemId) ─────────────────────────────────────────

	Describe("UpdateItem", func() {
		It("forwards the body to the backend", func() {
			var receivedBody []byte
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer fakeBackend.Close()
			setupBrowseDB(fakeBackend.URL)

			w := doPost(router, "/items/"+proxyID, map[string]string{"Name": "Updated"}, browseAuth())
			Expect(w.Code).To(Equal(http.StatusNoContent))
			Expect(string(receivedBody)).To(ContainSubstring("Updated"))
		})
	})

	// ── DeleteItem ───────────────────────────────────────────────────────────────

	Describe("DeleteItem", func() {
		It("forwards the DELETE to the backend", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Method).To(Equal("DELETE"))
				Expect(r.URL.Path).To(Equal("/items/" + browseBackendID))
				w.WriteHeader(http.StatusNoContent)
			}))
			defer fakeBackend.Close()
			setupBrowseDB(fakeBackend.URL)

			w := doDelete(router, "/items/"+proxyID, browseAuth())
			Expect(w.Code).To(Equal(http.StatusNoContent))
		})
	})

	// ── RefreshItem ──────────────────────────────────────────────────────────────

	Describe("RefreshItem", func() {
		It("forwards the POST to the backend", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/items/" + browseBackendID + "/refresh"))
				w.WriteHeader(http.StatusNoContent)
			}))
			defer fakeBackend.Close()
			setupBrowseDB(fakeBackend.URL)

			w := doPost(router, "/items/"+proxyID+"/refresh", nil, browseAuth())
			Expect(w.Code).To(Equal(http.StatusNoContent))
		})
	})

	// ── UpdateUserConfiguration ──────────────────────────────────────────────────

	Describe("UpdateUserConfiguration", func() {
		It("returns 204 (no-op stub)", func() {
			u := createUser("cfguser", "password1!", false)
			createSession(u, browseToken)
			w := doPost(router, "/users/ignored/configuration", map[string]interface{}{}, browseAuth())
			Expect(w.Code).To(Equal(http.StatusNoContent))
		})
	})

	// ── UpdateUserPolicy ─────────────────────────────────────────────────────────

	Describe("UpdateUserPolicy", func() {
		It("returns 204 (no-op stub)", func() {
			u := createUser("poluser", "password1!", false)
			createSession(u, browseToken)
			w := doPost(router, "/users/ignored/policy", map[string]interface{}{}, browseAuth())
			Expect(w.Code).To(Equal(http.StatusNoContent))
		})
	})
})

// ── Aggregated browse endpoints ──────────────────────────────────────────────

var _ = Describe("Aggregated browse endpoints", func() {
	var router *gin.Engine

	BeforeEach(func() {
		cleanDB()
		router, _ = browseRouter()
	})

	type aggCase struct {
		describe    string
		proxyPath   string
		backendPath string
	}

	cases := []aggCase{
		{"GetArtists", "/artists", "/artists"},
		{"GetAlbumArtists", "/artists/albumartists", "/artists/AlbumArtists"},
		{"GetGenres", "/genres", "/genres"},
		{"GetMusicGenres", "/musicgenres", "/musicgenres"},
		{"GetStudios", "/studios", "/studios"},
		{"GetPersons", "/persons", "/persons"},
		{"GetChannels", "/channels", "/channels"},
		{"GetTrailers", "/trailers", "/trailers"},
		{"GetNextUp", "/shows/nextup", "/shows/NextUp"},
		{"GetUpcomingEpisodes", "/shows/upcoming", "/shows/Upcoming"},
	}

	for _, tc := range cases {
		tc := tc
		Describe(tc.describe, func() {
			Context("with a single backend", func() {
				It("returns aggregated items", func() {
					fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						Expect(r.URL.Path).To(Equal(tc.backendPath))
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprint(w, pagedJSON(`{"Id":"a1","Name":"Item One"}`))
					}))
					defer fake.Close()
					setupBrowseDB(fake.URL)

					w := doGet(router, tc.proxyPath, browseAuth())
					Expect(w.Code).To(Equal(http.StatusOK))
					var resp map[string]interface{}
					Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
					Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 1))
				})
			})

			Context("with no backend mappings", func() {
				It("returns 200 with empty items", func() {
					// Create user+session but no backend mapping.
					u := createUser("nobackend", "password1!", false)
					createSession(u, browseToken)

					w := doGet(router, tc.proxyPath, browseAuth())
					Expect(w.Code).To(Equal(http.StatusOK))
					var resp map[string]interface{}
					Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
					Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 0))
				})
			})

			Context("without authentication", func() {
				It("returns 401", func() {
					w := doGet(router, tc.proxyPath)
					Expect(w.Code).To(Equal(http.StatusUnauthorized))
				})
			})
		})
	}

	// ── GetPlaylists ─────────────────────────────────────────────────────────────

	Describe("GetPlaylists", func() {
		It("returns playlists from the backend", func() {
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Query().Get("IncludeItemTypes")).To(Equal("Playlist"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, pagedJSON(`{"Id":"pl1","Name":"My Playlist"}`))
			}))
			defer fake.Close()
			setupBrowseDB(fake.URL)

			w := doGet(router, "/playlists", browseAuth())
			Expect(w.Code).To(Equal(http.StatusOK))
			var resp map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 1))
		})
	})

	// ── SearchHints ──────────────────────────────────────────────────────────────

	Describe("SearchHints", func() {
		It("aggregates search results from all backends", func() {
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(Equal("/search/hints"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"SearchHints":[{"Id":"h1","Name":"Hit"}],"TotalRecordCount":1}`)
			}))
			defer fake.Close()
			setupBrowseDB(fake.URL)

			w := doGet(router, "/search/hints?searchTerm=test", browseAuth())
			Expect(w.Code).To(Equal(http.StatusOK))
			var resp map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 1))
		})

		It("returns empty results when no backends are mapped", func() {
			u := createUser("nosearch", "password1!", false)
			createSession(u, browseToken)

			w := doGet(router, "/search/hints?searchTerm=test", browseAuth())
			Expect(w.Code).To(Equal(http.StatusOK))
			var resp map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 0))
		})
	})
})

// ── Static stub endpoints ────────────────────────────────────────────────────

var _ = Describe("Static stub endpoints", func() {
	var router *gin.Engine

	BeforeEach(func() {
		cleanDB()
		router, _ = browseRouter()
		u := createUser("stubuser", "password1!", false)
		createSession(u, browseToken)
	})

	Describe("SyncPlayList", func() {
		It("returns 200 with an empty array", func() {
			w := doGet(router, "/syncplay/list", browseAuth())
			Expect(w.Code).To(Equal(http.StatusOK))
			var resp []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp).To(BeEmpty())
		})
	})

	Describe("GetSessions", func() {
		It("returns 200 with an empty array", func() {
			w := doGet(router, "/sessions", browseAuth())
			Expect(w.Code).To(Equal(http.StatusOK))
			var resp []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp).To(BeEmpty())
		})
	})

	Describe("GetScheduledTasks", func() {
		It("returns 200 with an empty array", func() {
			w := doGet(router, "/scheduledtasks", browseAuth())
			Expect(w.Code).To(Equal(http.StatusOK))
			var resp []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp).To(BeEmpty())
		})
	})

	Describe("GetInstalledPlugins", func() {
		It("returns 200 with an empty array", func() {
			w := doGet(router, "/plugins", browseAuth())
			Expect(w.Code).To(Equal(http.StatusOK))
			var resp []interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp).To(BeEmpty())
		})
	})

	Describe("GetNotificationsSummary", func() {
		It("returns 200 with UnreadCount 0", func() {
			w := doGet(router, "/notifications/summary", browseAuth())
			Expect(w.Code).To(Equal(http.StatusOK))
			var resp map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp["UnreadCount"]).To(BeNumerically("==", 0))
		})
	})

	Describe("GetMediaSegments", func() {
		It("returns 200 with empty items", func() {
			w := doGet(router, "/mediasegments/"+idtrans.Encode(browsePrefix, "abc"), browseAuth())
			Expect(w.Code).To(Equal(http.StatusOK))
			var resp map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 0))
		})
	})
})

// ── GetUsers ─────────────────────────────────────────────────────────────────

var _ = Describe("GetUsers", func() {
	var router *gin.Engine

	BeforeEach(func() {
		cleanDB()
		router, _ = browseRouter()
		u := createUser("getusers-user", "password1!", false)
		createSession(u, browseToken)
	})

	It("returns 200 with a user list containing the current user", func() {
		w := doGet(router, "/users", browseAuth())
		Expect(w.Code).To(Equal(http.StatusOK))
		// Response is an array of user objects.
		var resp []interface{}
		Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
		Expect(resp).NotTo(BeEmpty())
	})
})

// ── Playback reports ─────────────────────────────────────────────────────────

var _ = Describe("Playback report endpoints", func() {
	var router *gin.Engine

	BeforeEach(func() {
		cleanDB()
		router, _ = browseRouter()
	})

	type reportCase struct {
		describe string
		path     string
	}

	cases := []reportCase{
		{"ReportPlaybackStart", "/sessions/playing"},
		{"ReportPlaybackProgress", "/sessions/playing/progress"},
		{"ReportPlaybackStopped", "/sessions/playing/stopped"},
	}

	for _, tc := range cases {
		tc := tc
		Describe(tc.describe, func() {
			It("forwards the report to the backend and returns 204", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				}))
				defer fakeBackend.Close()
				setupBrowseDB(fakeBackend.URL)

				proxyID := idtrans.Encode(browsePrefix, browseBackendID)
				w := doPost(router, tc.path,
					map[string]interface{}{"ItemId": proxyID},
					browseAuth())
				Expect(w.Code).To(Equal(http.StatusNoContent))
			})

			It("returns 401 without auth", func() {
				w := doPost(router, tc.path, map[string]interface{}{})
				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})
	}
})

// ── Image proxy ──────────────────────────────────────────────────────────────

var _ = Describe("GetImage", func() {
	var router *gin.Engine

	BeforeEach(func() {
		cleanDB()
		router, _ = browseRouter()
	})

	It("proxies the image from the backend", func() {
		fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.URL.Path).To(Equal("/items/" + browseBackendID + "/images/primary"))
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF}) // JPEG magic bytes
		}))
		defer fakeBackend.Close()
		setupBrowseDB(fakeBackend.URL)

		proxyID := idtrans.Encode(browsePrefix, browseBackendID)
		w := doGet(router, "/items/"+proxyID+"/images/primary")
		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(w.Header().Get("Content-Type")).To(ContainSubstring("image/"))
	})

	It("proxies images with an index", func() {
		fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.URL.Path).To(Equal("/items/" + browseBackendID + "/images/backdrop/0"))
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF})
		}))
		defer fakeBackend.Close()
		setupBrowseDB(fakeBackend.URL)

		proxyID := idtrans.Encode(browsePrefix, browseBackendID)
		w := doGet(router, "/items/"+proxyID+"/images/backdrop/0")
		Expect(w.Code).To(Equal(http.StatusOK))
	})

	It("returns 400 for unprefixed ID", func() {
		w := doGet(router, "/items/noprefixid/images/primary")
		Expect(w.Code).To(Equal(http.StatusBadRequest))
	})
})

// ── Audio streams ────────────────────────────────────────────────────────────

var _ = Describe("Audio stream endpoints", func() {
	var router *gin.Engine

	BeforeEach(func() {
		cleanDB()
		router, _ = browseRouter()
	})

	Describe("StreamAudio", func() {
		It("proxies the audio stream from the backend", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(ContainSubstring("/audio/" + browseBackendID + "/stream"))
				w.Header().Set("Content-Type", "audio/mpeg")
				_, _ = w.Write([]byte("fake-audio-bytes"))
			}))
			defer fakeBackend.Close()
			setupBrowseDB(fakeBackend.URL)

			proxyID := idtrans.Encode(browsePrefix, browseBackendID)
			w := doGet(router, "/audio/"+proxyID+"/stream?api_key="+browseToken)
			Expect(w.Code).To(Equal(http.StatusOK))
		})
	})

	Describe("UniversalAudio", func() {
		It("proxies the universal audio from the backend", func() {
			fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.URL.Path).To(ContainSubstring("/audio/" + browseBackendID + "/universal"))
				w.Header().Set("Content-Type", "audio/mpeg")
				_, _ = w.Write([]byte("fake-audio-bytes"))
			}))
			defer fakeBackend.Close()
			setupBrowseDB(fakeBackend.URL)

			proxyID := idtrans.Encode(browsePrefix, browseBackendID)
			w := doGet(router, "/audio/"+proxyID+"/universal?api_key="+browseToken)
			Expect(w.Code).To(Equal(http.StatusOK))
		})
	})
})
