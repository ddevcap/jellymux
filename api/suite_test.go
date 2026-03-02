package api_test

import (
	"context"
	"database/sql"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellymux/ent"
	"github.com/ddevcap/jellymux/ent/enttest"
	_ "modernc.org/sqlite"
)

func init() {
	// modernc.org/sqlite registers as "sqlite"; ent expects "sqlite3".
	tmp, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic(err)
	}
	drv := tmp.Driver()
	_ = tmp.Close()
	sql.Register("sqlite3", drv)
}

var db *ent.Client

func TestAPI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "API Suite")
}

var _ = BeforeSuite(func() {
	db = enttest.Open(GinkgoT(), "sqlite3", "file:api_test?mode=memory&cache=shared&_pragma=foreign_keys(1)")
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
