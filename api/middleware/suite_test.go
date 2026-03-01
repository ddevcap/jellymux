package middleware_test

import (
	"context"
	"database/sql"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellyfin-proxy/ent"
	"github.com/ddevcap/jellyfin-proxy/ent/enttest"
	_ "modernc.org/sqlite"
)

func init() {
	tmp, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic(err)
	}
	drv := tmp.Driver()
	_ = tmp.Close()
	sql.Register("sqlite3", drv)
}

var db *ent.Client

func TestMiddleware(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Middleware Suite")
}

var _ = BeforeSuite(func() {
	db = enttest.Open(GinkgoT(), "sqlite3", "file:middleware_test?mode=memory&cache=shared&_pragma=foreign_keys(1)")
})

var _ = AfterSuite(func() {
	if db != nil {
		Expect(db.Close()).To(Succeed())
	}
})

func cleanDB() {
	ctx := context.Background()
	db.BackendUser.Delete().ExecX(ctx)
	db.Session.Delete().ExecX(ctx)
	db.Backend.Delete().ExecX(ctx)
	db.User.Delete().ExecX(ctx)
}

