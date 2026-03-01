package api_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellyfin-proxy/api"
	"github.com/ddevcap/jellyfin-proxy/config"
)

var _ = Describe("SessionCleaner", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
		cleanDB()
	})

	It("creates and stops without error", func() {
		sc := api.NewSessionCleaner(db, config.Config{SessionTTL: time.Hour})
		sc.Start(ctx)
		sc.Stop()
	})

	It("cleans up expired sessions", func() {
		cfg := config.Config{SessionTTL: time.Millisecond}

		// Create a user and an expired session
		u := db.User.Create().
			SetUsername("cleaner-user").
			SetDisplayName("Cleaner").
			SetHashedPassword("hash").
			SetIsAdmin(false).
			SaveX(ctx)

		db.Session.Create().
			SetToken("expired-token").
			SetDeviceID("dev").
			SetDeviceName("Dev").
			SetAppName("App").
			SetUser(u).
			SetLastActivity(time.Now().Add(-time.Hour)). // well in the past
			SaveX(ctx)

		// Run cleanup directly
		sc := api.NewSessionCleaner(db, cfg)
		sc.Cleanup(ctx)

		// Session should be gone
		count, err := db.Session.Query().Count(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(Equal(0))
	})

	It("does not clean sessions when TTL is 0", func() {
		cfg := config.Config{SessionTTL: 0}

		u := db.User.Create().
			SetUsername("nodelete-user").
			SetDisplayName("NoDelete").
			SetHashedPassword("hash").
			SetIsAdmin(false).
			SaveX(ctx)

		db.Session.Create().
			SetToken("keep-token").
			SetDeviceID("dev").
			SetDeviceName("Dev").
			SetAppName("App").
			SetUser(u).
			SetLastActivity(time.Now().Add(-24 * time.Hour)).
			SaveX(ctx)

		sc := api.NewSessionCleaner(db, cfg)
		sc.Cleanup(ctx)

		count, err := db.Session.Query().Count(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(Equal(1))
	})
})

