package handler_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/crypto/bcrypt"

	"github.com/ddevcap/jellyfin-proxy/ent"
	"github.com/ddevcap/jellyfin-proxy/ent/enttest"
	"github.com/ddevcap/jellyfin-proxy/idtrans"
	_ "modernc.org/sqlite"
)

func init() {
	// modernc.org/sqlite registers itself as "sqlite" in database/sql, but
	// ent's dialect layer recognises only "sqlite3". We fetch the already-
	// registered driver via a temporary connection and re-register it under
	// the name ent expects, so enttest.Open works without further changes.
	tmp, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic(err)
	}
	drv := tmp.Driver()
	_ = tmp.Close()
	sql.Register("sqlite3", drv)
}

// db is the shared ent client opened once per suite against an in-memory SQLite
// database. The schema is auto-migrated on open; rows are cleared in BeforeEach.
var db *ent.Client

func TestHandlers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Handler Suite")
}

var _ = BeforeSuite(func() {
	idtrans.PrewarmMerged()
	// file: URI — named in-process SQLite database.
	// cache=shared lets multiple connections in the same process share it.
	// _fk=1 enables foreign-key enforcement, matching production Postgres behaviour.
	// modernc.org/sqlite uses _pragma=foreign_keys(1) rather than the
	// mattn/go-sqlite3-style _fk=1 parameter.
	db = enttest.Open(GinkgoT(), "sqlite3", "file:handler_test?mode=memory&cache=shared&_pragma=foreign_keys(1)")
})

var _ = AfterSuite(func() {
	if db != nil {
		Expect(db.Close()).To(Succeed())
	}
})

// cleanDB deletes all rows in foreign-key-safe order. Call at the top of each
// BeforeEach so every spec starts from a blank slate.
func cleanDB() {
	ctx := context.Background()
	db.BackendUser.Delete().ExecX(ctx)
	db.Session.Delete().ExecX(ctx)
	db.Backend.Delete().ExecX(ctx)
	db.User.Delete().ExecX(ctx)
}

// ── DB helpers ───────────────────────────────────────────────────────────────

// createUser inserts a user with a bcrypt hash. bcrypt.MinCost (4 rounds) is
// used intentionally to keep tests fast without affecting correctness.
func createUser(username, password string, isAdmin bool) *ent.User {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	Expect(err).NotTo(HaveOccurred())
	u, err := db.User.Create().
		SetUsername(username).
		SetDisplayName(username).
		SetHashedPassword(string(hash)).
		SetIsAdmin(isAdmin).
		Save(context.Background())
	Expect(err).NotTo(HaveOccurred())
	return u
}

// createBackend inserts a backend with a unique external_id.
func createBackend(name, url, externalID string) *ent.Backend {
	b, err := db.Backend.Create().
		SetName(name).
		SetURL(url).
		SetExternalID(externalID).
		Save(context.Background())
	Expect(err).NotTo(HaveOccurred())
	return b
}

// createBackendUser inserts a BackendUser mapping between a proxy user and a backend.
func createBackendUser(backend *ent.Backend, user *ent.User, backendUserID string, token ...string) *ent.BackendUser {
	builder := db.BackendUser.Create().
		SetBackend(backend).
		SetUser(user).
		SetBackendUserID(backendUserID)
	if len(token) > 0 && token[0] != "" {
		builder = builder.SetBackendToken(token[0])
	}
	bu, err := builder.Save(context.Background())
	Expect(err).NotTo(HaveOccurred())
	return bu
}

// createSession inserts a session for the given user with the supplied token.
func createSession(user *ent.User, token string) *ent.Session {
	s, err := db.Session.Create().
		SetToken(token).
		SetDeviceID("test-device").
		SetDeviceName("Test Device").
		SetAppName("Test App").
		SetUser(user).
		Save(context.Background())
	Expect(err).NotTo(HaveOccurred())
	return s
}

// ── HTTP helpers ─────────────────────────────────────────────────────────────

// doRequest fires an HTTP request against handler r and returns the recorder.
// body is JSON-encoded when non-nil. Extra header maps are applied in order.
func doRequest(r http.Handler, method, path string, body interface{}, headers ...map[string]string) *httptest.ResponseRecorder {
	var reqBody io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, path, reqBody)
	req.RemoteAddr = "127.0.0.1:12345" // loopback IP for shouldDirectStream tests
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func doPost(r http.Handler, path string, body interface{}, headers ...map[string]string) *httptest.ResponseRecorder {
	return doRequest(r, http.MethodPost, path, body, headers...)
}

func doPatch(r http.Handler, path string, body interface{}, headers ...map[string]string) *httptest.ResponseRecorder { //nolint:unparam
	return doRequest(r, http.MethodPatch, path, body, headers...)
}

func doGet(r http.Handler, path string, headers ...map[string]string) *httptest.ResponseRecorder {
	return doRequest(r, http.MethodGet, path, nil, headers...)
}

func doDelete(r http.Handler, path string, headers ...map[string]string) *httptest.ResponseRecorder {
	return doRequest(r, http.MethodDelete, path, nil, headers...)
}

// doRawPost fires a POST with an arbitrary raw body and Content-Type, useful
// for testing binary uploads such as avatar images.
func doRawPost(r http.Handler, path string, body []byte, contentType string, headers ...map[string]string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
