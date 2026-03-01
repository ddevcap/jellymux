package handler_test

import (
	"encoding/json"
	"fmt"
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

const viewsToken = "views-test-session-token"

// viewsRouter builds a router with the merged-views and item-browsing endpoints.
func viewsRouter() *gin.Engine {
	cfg := config.Config{ServerID: "test-server-id", ServerName: "Test Proxy"}
	pool := backend.NewPool(db, cfg)
	mediaH := handler.NewMediaHandler(pool, cfg, db)

	r := gin.New()
	priv := r.Group("/")
	priv.Use(middleware.Auth(db, cfg))

	priv.GET("/users/:userId/views", mediaH.GetViews)
	priv.GET("/userviews", mediaH.GetUserViews)
	priv.GET("/users/:userId/items", mediaH.GetUserItems)
	priv.GET("/users/:userId/items/latest", mediaH.GetLatestItems)
	priv.GET("/users/:userId/items/resume", mediaH.GetResumeItems)
	priv.GET("/users/:userId/items/:itemId", mediaH.GetUserItem)
	priv.GET("/items", mediaH.GetItems)
	priv.GET("/items/:itemId", mediaH.GetItem)
	priv.GET("/items/filters", mediaH.GetQueryFilters)
	priv.GET("/items/filters2", mediaH.GetQueryFilters)
	priv.GET("/items/counts", mediaH.GetItemCounts)
	priv.GET("/items/suggestions", mediaH.GetSuggestedItems)
	priv.GET("/items/:itemId/playbackinfo", mediaH.GetPlaybackInfo)
	priv.POST("/items/:itemId/playbackinfo", mediaH.GetPlaybackInfo)
	return r
}

// registerViewsBackend creates a backend + user mapping for the single user in
// the DB (created by the test BeforeEach).
func registerViewsBackend(name, url, externalID, backendUserID string) {
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

// auth is a convenience for building an auth header map.
func auth() map[string]string {
	return map[string]string{"X-Emby-Token": viewsToken}
}

// ── GetViews / mergedViews ────────────────────────────────────────────────────

var _ = Describe("Merged library views", func() {
	var router *gin.Engine

	BeforeEach(func() {
		cleanDB()
		u := createUser("viewsuser", "password1!", false)
		createSession(u, viewsToken)
		router = viewsRouter()
	})

	Describe("GetViews", func() {
		Context("with two backends that both have a movies library", func() {
			It("collapses them into a single merged_movies view", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"lib-a1","Name":"Movies A","CollectionType":"movies"}],"TotalRecordCount":1}`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"lib-b1","Name":"Movies B","CollectionType":"movies"}],"TotalRecordCount":1}`)
				}))
				defer fakeB.Close()

				registerViewsBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerViewsBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/users/ignored/views", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp struct {
					Items            []json.RawMessage `json:"Items"`
					TotalRecordCount int               `json:"TotalRecordCount"`
				}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				// Two movie libraries → collapsed into one merged view.
				Expect(resp.Items).To(HaveLen(1))

				var item map[string]interface{}
				Expect(json.Unmarshal(resp.Items[0], &item)).To(Succeed())
				Expect(item["Id"]).To(Equal(idtrans.EncodeMerged("movies")))
				Expect(item["CollectionType"]).To(Equal("movies"))
			})
		})

		Context("with two backends with different library types", func() {
			It("returns both libraries unmerged", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"lib-a1","Name":"Movies","CollectionType":"movies"}],"TotalRecordCount":1}`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"lib-b1","Name":"TV Shows","CollectionType":"tvshows"}],"TotalRecordCount":1}`)
				}))
				defer fakeB.Close()

				registerViewsBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerViewsBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/users/ignored/views", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp struct {
					Items []json.RawMessage `json:"Items"`
				}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				// Different types → both kept.
				Expect(resp.Items).To(HaveLen(2))
			})
		})

		Context("when the user has no backend mappings", func() {
			It("returns 200 with an empty Items array", func() {
				w := doGet(router, "/users/ignored/views", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp struct {
					Items []json.RawMessage `json:"Items"`
				}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Items).To(BeEmpty())
			})
		})

		Context("without authentication", func() {
			It("returns 401", func() {
				w := doGet(router, "/users/ignored/views")
				Expect(w.Code).To(Equal(http.StatusUnauthorized))
			})
		})
	})

	// ── GetItem with merged virtual ID ────────────────────────────────────────

	Describe("GetItem", func() {
		Context("when the item ID is a merged virtual ID", func() {
			It("returns a synthetic CollectionFolder", func() {
				w := doGet(router, "/items/"+idtrans.EncodeMerged("movies"), auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["Id"]).To(Equal(idtrans.EncodeMerged("movies")))
				Expect(resp["Name"]).To(Equal("Movies"))
				Expect(resp["Type"]).To(Equal("CollectionFolder"))
				Expect(resp["CollectionType"]).To(Equal("movies"))
				Expect(resp["IsFolder"]).To(BeTrue())
				Expect(resp["ServerId"]).To(Equal("testserverid"))
			})
		})

		Context("with merged_tvshows", func() {
			It("returns the correct display name", func() {
				w := doGet(router, "/items/"+idtrans.EncodeMerged("tvshows"), auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["Name"]).To(Equal("TV Shows"))
			})
		})

		Context("when the item ID is a regular prefixed ID", func() {
			It("proxies to the correct backend", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/items/abc123"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Id":"abc123","Name":"Test Movie","Type":"Movie"}`)
				}))
				defer fakeBackend.Close()

				registerViewsBackend("Backend A", fakeBackend.URL, "ba", "user-ba")

				w := doGet(router, "/items/ba_abc123", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["Name"]).To(Equal("Test Movie"))
			})
		})

		Context("when the item ID has no prefix", func() {
			It("returns 400", func() {
				w := doGet(router, "/items/noprefixhere", auth())
				Expect(w.Code).To(Equal(http.StatusBadRequest))
			})
		})
	})

	// ── GetUserItem ──────────────────────────────────────────────────────────

	Describe("GetUserItem", func() {
		Context("when the item ID is a merged virtual ID", func() {
			It("returns a synthetic CollectionFolder", func() {
				w := doGet(router, "/users/ignored/items/"+idtrans.EncodeMerged("movies"), auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["Id"]).To(Equal(idtrans.EncodeMerged("movies")))
				Expect(resp["Name"]).To(Equal("Movies"))
				Expect(resp["Type"]).To(Equal("CollectionFolder"))
				Expect(resp["CollectionType"]).To(Equal("movies"))
				Expect(resp["IsFolder"]).To(BeTrue())
				Expect(resp["ServerId"]).To(Equal("testserverid"))
			})
		})

		Context("with merged_tvshows", func() {
			It("returns the correct display name", func() {
				w := doGet(router, "/users/ignored/items/"+idtrans.EncodeMerged("tvshows"), auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["Name"]).To(Equal("TV Shows"))
			})
		})
	})

	// ── GetItems ──────────────────────────────────────────────────────────────

	Describe("GetItems", func() {
		Context("with a merged parentId", func() {
			It("fans out to all backends and merges items", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/items"))
					Expect(r.URL.Query().Get("IncludeItemTypes")).To(Equal("movie"))
					Expect(r.URL.Query().Get("Recursive")).To(Equal("true"))
					// ParentId should be stripped.
					Expect(r.URL.Query().Get("ParentId")).To(BeEmpty())
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"m1","Name":"Movie 1"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"m2","Name":"Movie 2"},{"Id":"m3","Name":"Movie 3"}],"TotalRecordCount":2,"StartIndex":0}`)
				}))
				defer fakeB.Close()

				registerViewsBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerViewsBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/items?parentId="+idtrans.EncodeMerged("movies"), auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 3))
				items := resp["Items"].([]interface{})
				Expect(items).To(HaveLen(3))
			})
		})

		Context("with a regular backend prefix parentId", func() {
			It("routes to that specific backend", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/items"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"m1","Name":"Test"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeBackend.Close()

				registerViewsBackend("Backend A", fakeBackend.URL, "ba", "user-ba")

				w := doGet(router, "/items?parentId=ba_someid", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 1))
			})
		})

		Context("without parentId or searchTerm", func() {
			It("returns an empty paged list", func() {
				w := doGet(router, "/items", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 0))
			})
		})

		Context("with a searchTerm and no parentId", func() {
			It("fans out to all backends", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"s1","Name":"Search Hit"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeA.Close()

				registerViewsBackend("Backend A", fakeA.URL, "ba", "user-ba")

				w := doGet(router, "/items?searchTerm=test", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 1))
			})
		})
	})

	// ── GetUserItems ──────────────────────────────────────────────────────────

	Describe("GetUserItems", func() {
		Context("with a merged parentId", func() {
			It("fans out to all backends and merges items", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Query().Get("IncludeItemTypes")).To(Equal("movie"))
					Expect(r.URL.Query().Get("Recursive")).To(Equal("true"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"m1","Name":"Movie A"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"m2","Name":"Movie B"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeB.Close()

				registerViewsBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerViewsBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/users/ignored/items?parentId="+idtrans.EncodeMerged("movies"), auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 2))
			})
		})

		Context("without parentId", func() {
			It("returns an empty paged list", func() {
				w := doGet(router, "/users/ignored/items", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 0))
			})
		})
	})

	// ── GetLatestItems ────────────────────────────────────────────────────────

	Describe("GetLatestItems", func() {
		Context("with a merged parentId", func() {
			It("fans out and returns a merged bare array", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `[{"Id":"l1","Name":"Latest A"}]`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `[{"Id":"l2","Name":"Latest B"}]`)
				}))
				defer fakeB.Close()

				registerViewsBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerViewsBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/users/ignored/items/latest?parentId="+idtrans.EncodeMerged("movies"), auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var items []interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &items)).To(Succeed())
				Expect(items).To(HaveLen(2))
			})
		})

		Context("without parentId", func() {
			It("returns an empty array", func() {
				w := doGet(router, "/users/ignored/items/latest", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var items []interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &items)).To(Succeed())
				Expect(items).To(BeEmpty())
			})
		})

		Context("with a regular backend parentId", func() {
			It("routes to that specific backend", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `[{"Id":"l1","Name":"Latest"}]`)
				}))
				defer fakeBackend.Close()

				registerViewsBackend("Backend A", fakeBackend.URL, "ba", "user-ba")

				w := doGet(router, "/users/ignored/items/latest?parentId=ba_libid", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var items []interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &items)).To(Succeed())
				Expect(items).To(HaveLen(1))
			})
		})
	})

	// ── GetQueryFilters ───────────────────────────────────────────────────────

	Describe("GetQueryFilters", func() {
		Context("with a merged parentId", func() {
			It("aggregates filters from all backends and deduplicates", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/items/Filters2"))
					Expect(r.URL.Query().Get("IncludeItemTypes")).To(Equal("movie"))
					Expect(r.URL.Query().Get("ParentId")).To(BeEmpty())
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{
						"Genres":[{"Name":"Action"},{"Name":"Comedy"}],
						"Tags":[{"Name":"4K"}],
						"OfficialRatings":["PG-13","R"],
						"Years":[2024,2025]
					}`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{
						"Genres":[{"Name":"Comedy"},{"Name":"Drama"}],
						"Tags":[{"Name":"4K"},{"Name":"HDR"}],
						"OfficialRatings":["R","PG"],
						"Years":[2025,2026]
					}`)
				}))
				defer fakeB.Close()

				registerViewsBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerViewsBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/items/filters2?parentId="+idtrans.EncodeMerged("movies"), auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp struct {
					Genres          []map[string]string `json:"Genres"`
					Tags            []map[string]string `json:"Tags"`
					OfficialRatings []string            `json:"OfficialRatings"`
					Years           []int               `json:"Years"`
				}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())

				// Deduplicated: Action, Comedy, Drama
				Expect(resp.Genres).To(HaveLen(3))
				// Deduplicated: 4K, HDR
				Expect(resp.Tags).To(HaveLen(2))
				// Deduplicated: PG-13, R, PG
				Expect(resp.OfficialRatings).To(HaveLen(3))
				// Deduplicated: 2024, 2025, 2026
				Expect(resp.Years).To(HaveLen(3))
			})
		})

		Context("with a regular backend prefix parentId", func() {
			It("routes to that specific backend", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/items/Filters2"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{
						"Genres":[{"Name":"Action"}],
						"Tags":[],
						"OfficialRatings":["PG"],
						"Years":[2024]
					}`)
				}))
				defer fakeBackend.Close()

				registerViewsBackend("Backend A", fakeBackend.URL, "ba", "user-ba")

				w := doGet(router, "/items/filters?parentId=ba_libid", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp struct {
					Genres []map[string]string `json:"Genres"`
					Years  []int               `json:"Years"`
				}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Genres).To(HaveLen(1))
				Expect(resp.Years).To(HaveLen(1))
			})
		})

		Context("without parentId", func() {
			It("returns empty filter arrays", func() {
				w := doGet(router, "/items/filters", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp struct {
					Genres          []interface{} `json:"Genres"`
					Tags            []interface{} `json:"Tags"`
					OfficialRatings []interface{} `json:"OfficialRatings"`
					Years           []interface{} `json:"Years"`
				}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Genres).To(BeEmpty())
				Expect(resp.Tags).To(BeEmpty())
				Expect(resp.OfficialRatings).To(BeEmpty())
				Expect(resp.Years).To(BeEmpty())
			})
		})

		Context("when one backend errors", func() {
			It("returns filters from the healthy backend only", func() {
				fakeOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{
						"Genres":[{"Name":"Action"}],
						"Tags":[],
						"OfficialRatings":[],
						"Years":[2024]
					}`)
				}))
				defer fakeOK.Close()

				fakeBad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				}))
				defer fakeBad.Close()

				registerViewsBackend("Good", fakeOK.URL, "ok", "user-ok")
				registerViewsBackend("Bad", fakeBad.URL, "bd", "user-bd")

				w := doGet(router, "/items/filters2?parentId="+idtrans.EncodeMerged("movies"), auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp struct {
					Genres []map[string]string `json:"Genres"`
					Years  []int               `json:"Years"`
				}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp.Genres).To(HaveLen(1))
				Expect(resp.Years).To(HaveLen(1))
			})
		})
	})

	// ── GetItemCounts ─────────────────────────────────────────────────────────

	Describe("GetItemCounts", func() {
		Context("with two backends", func() {
			It("sums item counts across all backends", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/items/Counts"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"MovieCount":10,"SeriesCount":5}`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"MovieCount":3,"SeriesCount":7}`)
				}))
				defer fakeB.Close()

				registerViewsBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerViewsBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/items/counts", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["MovieCount"]).To(BeNumerically("==", 13))
				Expect(resp["SeriesCount"]).To(BeNumerically("==", 12))
			})
		})

		Context("with no backend mappings", func() {
			It("returns all zeros", func() {
				w := doGet(router, "/items/counts", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["MovieCount"]).To(BeNumerically("==", 0))
			})
		})
	})

	// ── GetSuggestedItems ─────────────────────────────────────────────────────

	Describe("GetSuggestedItems", func() {
		Context("with a single backend", func() {
			It("returns suggestions from that backend", func() {
				fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/items/Suggestions"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"s1","Name":"Suggestion"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fake.Close()

				registerViewsBackend("Backend A", fake.URL, "ba", "user-ba")

				w := doGet(router, "/items/suggestions", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 1))
			})
		})
	})

	// ── collectionTypeToItemType / mergedDisplayName ──────────────────────────

	Describe("collectionTypes mapping (integration)", func() {
		It("returns correct display names for all known collection types", func() {
			types := map[string]string{
				idtrans.EncodeMerged("movies"):      "Movies",
				idtrans.EncodeMerged("tvshows"):     "TV Shows",
				idtrans.EncodeMerged("music"):       "Music",
				idtrans.EncodeMerged("books"):       "Books",
				idtrans.EncodeMerged("boxsets"):     "Collections",
				idtrans.EncodeMerged("musicvideos"): "Music Videos",
				idtrans.EncodeMerged("photos"):      "Photos",
				idtrans.EncodeMerged("homevideos"):  "Home Videos",
				idtrans.EncodeMerged("livetv"):      "Live TV",
			}

			for mergedID, expectedName := range types {
				w := doGet(router, "/items/"+mergedID, auth())
				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["Name"]).To(Equal(expectedName), "expected %s for %s", expectedName, mergedID)
			}
		})

		It("title-cases unknown collection types", func() {
			w := doGet(router, "/items/"+idtrans.EncodeMerged("podcasts"), auth())
			Expect(w.Code).To(Equal(http.StatusOK))
			var resp map[string]interface{}
			Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
			Expect(resp["Name"]).To(Equal("Podcasts"))
		})

		It("maps merged_movies fan-out to IncludeItemTypes=movie", func() {
			called := false
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				Expect(r.URL.Query().Get("IncludeItemTypes")).To(Equal("movie"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0,"StartIndex":0}`)
			}))
			defer fake.Close()

			registerViewsBackend("Backend A", fake.URL, "ba", "user-ba")

			w := doGet(router, "/items?parentId="+idtrans.EncodeMerged("movies"), auth())
			Expect(w.Code).To(Equal(http.StatusOK))
			Expect(called).To(BeTrue())
		})

		It("maps merged_tvshows fan-out to IncludeItemTypes=series", func() {
			var gotItemType string
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotItemType = r.URL.Query().Get("IncludeItemTypes")
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0,"StartIndex":0}`)
			}))
			defer fake.Close()

			registerViewsBackend("Backend A", fake.URL, "ba", "user-ba")

			doGet(router, "/items?parentId="+idtrans.EncodeMerged("tvshows"), auth())
			Expect(gotItemType).To(Equal("series"))
		})

		It("maps merged_music fan-out to IncludeItemTypes=musicalbum", func() {
			var gotItemType string
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotItemType = r.URL.Query().Get("IncludeItemTypes")
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0,"StartIndex":0}`)
			}))
			defer fake.Close()

			registerViewsBackend("Backend A", fake.URL, "ba", "user-ba")

			doGet(router, "/items?parentId="+idtrans.EncodeMerged("music"), auth())
			Expect(gotItemType).To(Equal("musicalbum"))
		})
	})

	// ── GetResumeItems ────────────────────────────────────────────────────────

	Describe("GetResumeItems", func() {
		Context("with two backends", func() {
			It("aggregates resume items from both backends", func() {
				fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"r1","Name":"Resume A"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeA.Close()

				fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"r2","Name":"Resume B"}],"TotalRecordCount":1,"StartIndex":0}`)
				}))
				defer fakeB.Close()

				registerViewsBackend("Backend A", fakeA.URL, "ba", "user-ba")
				registerViewsBackend("Backend B", fakeB.URL, "bb", "user-bb")

				w := doGet(router, "/users/ignored/items/resume", auth())

				Expect(w.Code).To(Equal(http.StatusOK))
				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())
				Expect(resp["TotalRecordCount"]).To(BeNumerically("==", 2))
			})
		})
	})

	// ── Encode/Decode round-trip via GetViews → GetItems ─────────────────────

	Describe("End-to-end merged flow", func() {
		It("GetViews returns merged_movies, then GetItems with that ID returns items from both backends", func() {
			fakeA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/items":
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"m1","Name":"Movie A"}],"TotalRecordCount":1,"StartIndex":0}`)
				default:
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"lib-a","Name":"Movies","CollectionType":"movies"}],"TotalRecordCount":1}`)
				}
			}))
			defer fakeA.Close()

			fakeB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/items":
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"m2","Name":"Movie B"}],"TotalRecordCount":1,"StartIndex":0}`)
				default:
					_, _ = fmt.Fprint(w, `{"Items":[{"Id":"lib-b","Name":"Films","CollectionType":"movies"}],"TotalRecordCount":1}`)
				}
			}))
			defer fakeB.Close()

			registerViewsBackend("Backend A", fakeA.URL, "ba", "user-ba")
			registerViewsBackend("Backend B", fakeB.URL, "bb", "user-bb")

			// Step 1: Get views — should get merged_movies
			w1 := doGet(router, "/users/ignored/views", auth())
			Expect(w1.Code).To(Equal(http.StatusOK))
			var views struct {
				Items []json.RawMessage `json:"Items"`
			}
			Expect(json.Unmarshal(w1.Body.Bytes(), &views)).To(Succeed())
			Expect(views.Items).To(HaveLen(1))

			var viewItem map[string]interface{}
			Expect(json.Unmarshal(views.Items[0], &viewItem)).To(Succeed())
			mergedID := viewItem["Id"].(string)
			Expect(mergedID).To(Equal(idtrans.EncodeMerged("movies")))

			// Step 2: Browse into that merged library
			w2 := doGet(router, "/items?parentId="+mergedID, auth())
			Expect(w2.Code).To(Equal(http.StatusOK))
			var itemResp map[string]interface{}
			Expect(json.Unmarshal(w2.Body.Bytes(), &itemResp)).To(Succeed())
			Expect(itemResp["TotalRecordCount"]).To(BeNumerically("==", 2))

			// Step 3: Get item details for the merged library itself
			w3 := doGet(router, "/items/"+mergedID, auth())
			Expect(w3.Code).To(Equal(http.StatusOK))
			var detail map[string]interface{}
			Expect(json.Unmarshal(w3.Body.Bytes(), &detail)).To(Succeed())
			Expect(detail["Type"]).To(Equal("CollectionFolder"))
			Expect(detail["Name"]).To(Equal("Movies"))
		})
	})

	// ── GetPlaybackInfo URL rewriting ─────────────────────────────────────────

	Describe("GetPlaybackInfo", func() {
		const backendItemID = "41950bcbdaad6e204085bfae8d0c09b2"

		Context("when the backend returns a TranscodingUrl with bare IDs and ApiKey", func() {
			It("rewrites the item ID to proxy-prefixed and strips ApiKey", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					Expect(r.URL.Path).To(Equal("/items/" + backendItemID + "/playbackinfo"))
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{
						"MediaSources": [{
							"Id": "%s",
							"TranscodingUrl": "/videos/%s/master.m3u8?ApiKey=backend-secret&MediaSourceId=%s&AudioBitrate=384000",
							"DirectStreamUrl": "/videos/%s/stream?static=true&ApiKey=backend-secret&MediaSourceId=%s"
						}]
					}`, backendItemID, backendItemID, backendItemID, backendItemID, backendItemID)
				}))
				defer fakeBackend.Close()

				registerViewsBackend("Backend A", fakeBackend.URL, "ba", "user-ba")
				proxyItemID := idtrans.Encode("ba", backendItemID)

				w := doPost(router, "/items/"+proxyItemID+"/playbackinfo",
					map[string]interface{}{},
					auth())

				Expect(w.Code).To(Equal(http.StatusOK))

				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())

				sources := resp["MediaSources"].([]interface{})
				Expect(sources).To(HaveLen(1))
				source := sources[0].(map[string]interface{})

				// The JSON Id field should be proxy-prefixed by RewriteResponse.
				Expect(source["Id"]).To(Equal(proxyItemID))

				// TranscodingUrl should have the proxy-prefixed item ID.
				transURL, ok := source["TranscodingUrl"].(string)
				Expect(ok).To(BeTrue())
				Expect(transURL).To(ContainSubstring("/videos/" + proxyItemID + "/"))
				Expect(transURL).To(ContainSubstring("MediaSourceId=" + proxyItemID))
				// Backend ApiKey should have been stripped and replaced with the proxy session token.
				Expect(transURL).NotTo(ContainSubstring("backend-secret"))
				Expect(transURL).To(ContainSubstring("ApiKey=" + viewsToken))

				// DirectStreamUrl should also be rewritten.
				dsURL, ok := source["DirectStreamUrl"].(string)
				Expect(ok).To(BeTrue())
				Expect(dsURL).To(ContainSubstring("/videos/" + proxyItemID + "/"))
				Expect(dsURL).NotTo(ContainSubstring("backend-secret"))
				Expect(dsURL).To(ContainSubstring("ApiKey=" + viewsToken))
			})
		})

		Context("when ApiKey is the first query parameter", func() {
			It("strips backend ApiKey and injects proxy session token", func() {
				fakeBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{
						"MediaSources": [{
							"Id": "%s",
							"TranscodingUrl": "/videos/%s/master.m3u8?ApiKey=backend-secret&MediaSourceId=%s"
						}]
					}`, backendItemID, backendItemID, backendItemID)
				}))
				defer fakeBackend.Close()

				registerViewsBackend("Backend A", fakeBackend.URL, "ba", "user-ba")
				proxyItemID := idtrans.Encode("ba", backendItemID)

				w := doPost(router, "/items/"+proxyItemID+"/playbackinfo",
					map[string]interface{}{},
					auth())

				Expect(w.Code).To(Equal(http.StatusOK))

				var resp map[string]interface{}
				Expect(json.Unmarshal(w.Body.Bytes(), &resp)).To(Succeed())

				sources := resp["MediaSources"].([]interface{})
				source := sources[0].(map[string]interface{})

				transURL := source["TranscodingUrl"].(string)
				Expect(transURL).NotTo(ContainSubstring("backend-secret"))
				Expect(transURL).To(ContainSubstring("ApiKey=" + viewsToken))
				// MediaSourceId should still be present and rewritten.
				Expect(transURL).To(ContainSubstring("MediaSourceId=" + proxyItemID))
			})
		})
	})
})
