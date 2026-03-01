package backend_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/ddevcap/jellyfin-proxy/idtrans"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellyfin-proxy/backend"
	"github.com/ddevcap/jellyfin-proxy/config"
	"github.com/ddevcap/jellyfin-proxy/ent"
)

var _ = Describe("ServerClient", func() {
	var (
		ctx  context.Context
		pool *backend.Pool
	)

	const proxyServerID = "proxy-server-id"

	newBackend := func(name, backendURL, jellyfinServerID string) *ent.Backend {
		return db.Backend.Create().
			SetName(name).
			SetURL(backendURL).
			SetJellyfinServerID(jellyfinServerID).
			SetEnabled(true).
			SaveX(ctx)
	}

	newUser := func(username string) *ent.User {
		return db.User.Create().
			SetUsername(username).
			SetDisplayName(username).
			SetHashedPassword("hash").
			SetIsAdmin(false).
			SaveX(ctx)
	}

	newBackendUser := func(b *ent.Backend, u *ent.User, backendUserID string, token *string) *ent.BackendUser {
		q := db.BackendUser.Create().
			SetBackend(b).
			SetUser(u).
			SetBackendUserID(backendUserID).
			SetEnabled(true)
		if token != nil {
			q = q.SetBackendToken(*token)
		}
		return q.SaveX(ctx)
	}

	BeforeEach(func() {
		ctx = context.Background()
		cleanDB()
		pool = backend.NewPool(db, config.Config{ServerID: proxyServerID})
	})

	// ── Pure-function tests ──────────────────────────────────────────

	Describe("DirectURL", func() {
		It("builds a full URL with ApiKey injected", func() {
			b := newBackend("d1", "http://host:8096", "d1")
			u := newUser("alice")
			tok := "my-token"
			newBackendUser(b, u, "alice-id", &tok)

			sc, err := pool.ForUser(ctx, "d1", u)
			Expect(err).NotTo(HaveOccurred())

			result := sc.DirectURL("/Items/abc/Download", url.Values{"foo": {"bar"}})
			Expect(result).To(HavePrefix("http://host:8096/Items/abc/Download?"))
			Expect(result).To(ContainSubstring("ApiKey=my-token"))
			Expect(result).To(ContainSubstring("foo=bar"))
		})

		It("omits ApiKey when token is empty", func() {
			newBackend("d2", "http://host:8096", "d2")

			sc, err := pool.ForBackend(ctx, "d2")
			Expect(err).NotTo(HaveOccurred())

			result := sc.DirectURL("/path", url.Values{"a": {"1"}})
			Expect(result).NotTo(ContainSubstring("ApiKey"))
			Expect(result).To(ContainSubstring("a=1"))
		})
	})

	// ── ProxyJSON ────────────────────────────────────────────────────

	Describe("ProxyJSON", func() {
		It("rewrites IDs in a 200 JSON response", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Header.Get("X-Emby-Token")).To(Equal("tok"))
				Expect(r.Header.Get("Accept")).To(Equal("application/json"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"Id":"abc123","ServerId":"backend-srv"}`))
			}))
			defer srv.Close()

			b := newBackend("json1", srv.URL, "j1")
			u := newUser("bob")
			tok := "tok"
			newBackendUser(b, u, "bob-backend", &tok)

			sc, err := pool.ForUser(ctx, "j1", u)
			Expect(err).NotTo(HaveOccurred())

			body, status, err := sc.ProxyJSON(ctx, "GET", "/Items", nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))

			var m map[string]interface{}
			Expect(json.Unmarshal(body, &m)).To(Succeed())
			// Id should be a 32-char hex UUID (encoded from server ID + backend ID)
			Expect(m["Id"]).To(Equal(idtrans.Encode("j1", "abc123")))
			// ServerId should be replaced with proxy server ID (dashless)
			Expect(m["ServerId"]).To(Equal(strings.ReplaceAll(proxyServerID, "-", "")))
		})

		It("passes through non-2xx responses without rewriting", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"not found"}`))
			}))
			defer srv.Close()

			b := newBackend("json2", srv.URL, "j2")
			u := newUser("carol")
			tok := "tok2"
			newBackendUser(b, u, "carol-backend", &tok)

			sc, err := pool.ForUser(ctx, "j2", u)
			Expect(err).NotTo(HaveOccurred())

			body, status, err := sc.ProxyJSON(ctx, "GET", "/Items/missing", nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(404))
			Expect(string(body)).To(ContainSubstring("not found"))
		})

		It("rewrites request body: strips ID prefix and replaces UserId", func() {
			var receivedBody map[string]interface{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))
				raw, _ := io.ReadAll(r.Body)
				Expect(json.Unmarshal(raw, &receivedBody)).To(Succeed())
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			b := newBackend("json3", srv.URL, "j3")
			u := newUser("dave")
			tok := "tok3"
			newBackendUser(b, u, "dave-backend-id", &tok)

			sc, err := pool.ForUser(ctx, "j3", u)
			Expect(err).NotTo(HaveOccurred())

			reqBody := []byte(`{"MediaSourceId":"j3_source123","UserId":"proxy-user-id"}`)
			_, status, err := sc.ProxyJSON(ctx, "POST", "/PlaybackInfo", nil, reqBody)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))

			// MediaSourceId should have prefix stripped
			Expect(receivedBody["MediaSourceId"]).To(Equal("source123"))
			// UserId should be replaced with backend user ID
			Expect(receivedBody["UserId"]).To(Equal("dave-backend-id"))
		})

		It("returns an error when the backend is unreachable", func() {
			newBackend("json4", "http://127.0.0.1:1", "j4")

			sc, err := pool.ForBackend(ctx, "j4")
			Expect(err).NotTo(HaveOccurred())

			_, _, err = sc.ProxyJSON(ctx, "GET", "/test", nil, nil)
			Expect(err).To(HaveOccurred())
		})

		It("forwards query parameters to the backend", func() {
			var receivedQuery url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedQuery = r.URL.Query()
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			newBackend("json5", srv.URL, "j5")
			sc, err := pool.ForBackend(ctx, "j5")
			Expect(err).NotTo(HaveOccurred())

			q := url.Values{"Limit": {"10"}, "StartIndex": {"0"}}
			_, status, err := sc.ProxyJSON(ctx, "GET", "/Items", q, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(receivedQuery.Get("Limit")).To(Equal("10"))
			Expect(receivedQuery.Get("StartIndex")).To(Equal("0"))
		})
	})

	// ── ProxyRaw ─────────────────────────────────────────────────────

	Describe("ProxyRaw", func() {
		It("returns the raw body without ID rewriting", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-mpegURL")
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF\n/video/main.m3u8"))
			}))
			defer srv.Close()

			newBackend("raw1", srv.URL, "r1")
			sc, err := pool.ForBackend(ctx, "r1")
			Expect(err).NotTo(HaveOccurred())

			body, status, err := sc.ProxyRaw(ctx, "GET", "/master.m3u8", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(string(body)).To(ContainSubstring("#EXTM3U"))
		})

		It("passes through non-2xx responses", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("denied"))
			}))
			defer srv.Close()

			newBackend("raw2", srv.URL, "r2")
			sc, err := pool.ForBackend(ctx, "r2")
			Expect(err).NotTo(HaveOccurred())

			body, status, err := sc.ProxyRaw(ctx, "GET", "/forbidden", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(403))
			Expect(string(body)).To(Equal("denied"))
		})

		It("returns an error when the backend is unreachable", func() {
			newBackend("raw3", "http://127.0.0.1:1", "r3")
			sc, err := pool.ForBackend(ctx, "r3")
			Expect(err).NotTo(HaveOccurred())

			_, _, err = sc.ProxyRaw(ctx, "GET", "/test", nil)
			Expect(err).To(HaveOccurred())
		})
	})

	// ── ProxyStream ──────────────────────────────────────────────────

	Describe("ProxyStream", func() {
		It("streams response body to the writer", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Content-Length", "5")
				w.WriteHeader(200)
				_, _ = w.Write([]byte("video"))
			}))
			defer srv.Close()

			newBackend("str1", srv.URL, "s1")
			sc, err := pool.ForBackend(ctx, "s1")
			Expect(err).NotTo(HaveOccurred())

			rec := httptest.NewRecorder()
			err = sc.ProxyStream(ctx, "GET", "/stream", nil, http.Header{}, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Code).To(Equal(200))
			Expect(rec.Body.String()).To(Equal("video"))
			Expect(rec.Header().Get("Content-Type")).To(Equal("video/mp4"))
			Expect(rec.Header().Get("Content-Length")).To(Equal("5"))
		})

		It("forwards the Range header from the incoming request", func() {
			var receivedRange string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedRange = r.Header.Get("Range")
				w.Header().Set("Content-Range", "bytes 100-199/1000")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("partial"))
			}))
			defer srv.Close()

			newBackend("str2", srv.URL, "s2")
			sc, err := pool.ForBackend(ctx, "s2")
			Expect(err).NotTo(HaveOccurred())

			inHeader := http.Header{}
			inHeader.Set("Range", "bytes=100-199")

			rec := httptest.NewRecorder()
			err = sc.ProxyStream(ctx, "GET", "/stream", nil, inHeader, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(receivedRange).To(Equal("bytes=100-199"))
			Expect(rec.Code).To(Equal(206))
			Expect(rec.Header().Get("Content-Range")).To(Equal("bytes 100-199/1000"))
		})

		It("copies only allowed response headers and ignores others", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "audio/flac")
				w.Header().Set("Accept-Ranges", "bytes")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("X-Custom-Backend", "secret")
				w.Header().Set("Server", "Jellyfin")
				w.WriteHeader(200)
				_, _ = w.Write([]byte("audio"))
			}))
			defer srv.Close()

			newBackend("str3", srv.URL, "s3")
			sc, err := pool.ForBackend(ctx, "s3")
			Expect(err).NotTo(HaveOccurred())

			rec := httptest.NewRecorder()
			err = sc.ProxyStream(ctx, "GET", "/audio", nil, http.Header{}, rec)
			Expect(err).NotTo(HaveOccurred())

			// Allowed headers should be present
			Expect(rec.Header().Get("Content-Type")).To(Equal("audio/flac"))
			Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"))
			Expect(rec.Header().Get("Cache-Control")).To(Equal("no-cache"))
			// Disallowed headers should NOT be present
			Expect(rec.Header().Get("X-Custom-Backend")).To(BeEmpty())
			Expect(rec.Header().Get("Server")).To(BeEmpty())
		})

		It("sets Transfer-Encoding chunked when Content-Length is absent", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "video/mp4")
				// Flush to prevent Go from auto-computing Content-Length.
				w.WriteHeader(200)
				flusher := w.(http.Flusher)
				flusher.Flush()
				_, _ = w.Write([]byte("streaming"))
			}))
			defer srv.Close()

			newBackend("str4", srv.URL, "s4")
			sc, err := pool.ForBackend(ctx, "s4")
			Expect(err).NotTo(HaveOccurred())

			rec := httptest.NewRecorder()
			err = sc.ProxyStream(ctx, "GET", "/stream", nil, http.Header{}, rec)
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Header().Get("Transfer-Encoding")).To(Equal("chunked"))
		})

		It("returns an error when the backend is unreachable", func() {
			newBackend("str5", "http://127.0.0.1:1", "s5")
			sc, err := pool.ForBackend(ctx, "s5")
			Expect(err).NotTo(HaveOccurred())

			rec := httptest.NewRecorder()
			err = sc.ProxyStream(ctx, "GET", "/stream", nil, http.Header{}, rec)
			Expect(err).To(HaveOccurred())
		})
	})
})
