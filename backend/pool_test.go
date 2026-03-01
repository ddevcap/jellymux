package backend_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellyfin-proxy/backend"
	"github.com/ddevcap/jellyfin-proxy/config"
	"github.com/ddevcap/jellyfin-proxy/ent"
)

var _ = Describe("Pool", func() {
	var (
		ctx  context.Context
		pool *backend.Pool
	)

	BeforeEach(func() {
		ctx = context.Background()
		cleanDB()
		pool = backend.NewPool(db, config.Config{ServerID: "proxy-id"})
	})

	newBackend := func(name, url, externalID string) *ent.Backend {
		return db.Backend.Create().
			SetName(name).
			SetURL(url).
			SetExternalID(externalID).
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

	Describe("ForUser", func() {
		It("returns an empty token when no user mapping exists", func() {
			newBackend("Movies", "http://movies:8096", "mov")
			u := newUser("alice")

			sc, err := pool.ForUser(ctx, "mov", u)

			Expect(err).NotTo(HaveOccurred())
			Expect(sc.ExternalID()).To(Equal("mov"))
			Expect(sc.Token()).To(BeEmpty())
			Expect(sc.BackendUserID()).To(BeEmpty())
		})

		It("uses the per-user token when a mapping with a token exists", func() {
			b := newBackend("TV", "http://tv:8096", "tv")
			u := newUser("bob")
			tok := "user-specific-token"
			newBackendUser(b, u, "bob-backend-id", &tok)

			sc, err := pool.ForUser(ctx, "tv", u)

			Expect(err).NotTo(HaveOccurred())
			Expect(sc.Token()).To(Equal("user-specific-token"))
			Expect(sc.BackendUserID()).To(Equal("bob-backend-id"))
		})

		It("returns an empty token when the mapping has no user token", func() {
			b := newBackend("Music", "http://music:8096", "mus")
			u := newUser("carol")
			newBackendUser(b, u, "carol-backend-id", nil)

			sc, err := pool.ForUser(ctx, "mus", u)

			Expect(err).NotTo(HaveOccurred())
			Expect(sc.Token()).To(BeEmpty())
			Expect(sc.BackendUserID()).To(Equal("carol-backend-id"))
		})

		It("returns an error for an unknown server ID", func() {
			u := newUser("dave")

			_, err := pool.ForUser(ctx, "unknown", u)

			Expect(err).To(HaveOccurred())
		})
	})

	Describe("AllForUser", func() {
		It("returns a client for every enabled backend the user is mapped to", func() {
			b1 := newBackend("Movies", "http://movies:8096", "mov")
			b2 := newBackend("TV", "http://tv:8096", "tv")
			u := newUser("eve")
			tok := "eve-token"
			newBackendUser(b1, u, "eve-mov", &tok)
			newBackendUser(b2, u, "eve-tv", &tok)

			clients, err := pool.AllForUser(ctx, u)

			Expect(err).NotTo(HaveOccurred())
			Expect(clients).To(HaveLen(2))
			serverIDs := []string{clients[0].ExternalID(), clients[1].ExternalID()}
			Expect(serverIDs).To(ConsistOf("mov", "tv"))
		})

		It("returns an empty token when the mapping has no per-user token", func() {
			b := newBackend("Movies", "http://movies:8096", "mov")
			u := newUser("eve")
			newBackendUser(b, u, "eve-mov", nil)

			clients, err := pool.AllForUser(ctx, u)

			Expect(err).NotTo(HaveOccurred())
			Expect(clients).To(HaveLen(1))
			Expect(clients[0].Token()).To(BeEmpty())
		})

		It("returns an empty slice when the user has no mappings", func() {
			u := newUser("frank")

			clients, err := pool.AllForUser(ctx, u)

			Expect(err).NotTo(HaveOccurred())
			Expect(clients).To(BeEmpty())
		})
	})

	Describe("ForBackend", func() {
		It("returns a client with an empty token (no user credentials)", func() {
			newBackend("Movies", "http://movies:8096", "mov")

			sc, err := pool.ForBackend(ctx, "mov")

			Expect(err).NotTo(HaveOccurred())
			Expect(sc.ExternalID()).To(Equal("mov"))
			Expect(sc.Token()).To(BeEmpty())
		})

		It("returns an error for an unknown server ID", func() {
			_, err := pool.ForBackend(ctx, "nope")

			Expect(err).To(HaveOccurred())
		})
	})

	Describe("ServerClient accessors", func() {
		It("ServerURL trims a trailing slash from the backend URL", func() {
			newBackend("Test", "http://test:8096/", "tst")

			sc, err := pool.ForBackend(ctx, "tst")

			Expect(err).NotTo(HaveOccurred())
			Expect(sc.ServerURL()).To(Equal("http://test:8096"))
		})

		It("DirectURL builds a full URL with the backend base", func() {
			b := newBackend("Direct", "http://direct:8096", "dir")
			u := newUser("diruser")
			tok := "dir-token"
			newBackendUser(b, u, "dir-user-id", &tok)

			sc, err := pool.ForUser(ctx, "dir", u)

			Expect(err).NotTo(HaveOccurred())
			Expect(sc.DirectURL("/videos/abc/stream", nil)).To(
				SatisfyAll(
					HavePrefix("http://direct:8096"),
					ContainSubstring("ApiKey=dir-token"),
				),
			)
		})
	})

	Describe("SetHealthChecker / GetHealthChecker", func() {
		It("returns nil when no health checker is set", func() {
			Expect(pool.GetHealthChecker()).To(BeNil())
		})

		It("returns the health checker after it is set", func() {
			hc := backend.NewHealthChecker(pool, 0)
			pool.SetHealthChecker(hc)
			Expect(pool.GetHealthChecker()).To(Equal(hc))
			// Clean up
			pool.SetHealthChecker(nil)
		})
	})

	Describe("AllForUser (with health checker)", func() {
		It("skips backends marked as unavailable by the health checker", func() {
			b1 := newBackend("Movies", "http://movies:8096", "mov")
			b2 := newBackend("TV", "http://tv:8096", "tv")
			u := newUser("healthuser")
			tok := "tok"
			newBackendUser(b1, u, "u1", &tok)
			newBackendUser(b2, u, "u2", &tok)

			// Create a health checker and mark b1 as unavailable
			hc := backend.NewHealthChecker(pool, 0)
			pool.SetHealthChecker(hc)
			defer pool.SetHealthChecker(nil)

			// Record failures for b1 to make it unavailable
			for i := 0; i < 5; i++ {
				hc.RecordRequestFailure(b1.ID.String(), "Movies")
			}

			clients, err := pool.AllForUser(ctx, u)
			Expect(err).NotTo(HaveOccurred())
			// Only the healthy backend should be returned
			for _, c := range clients {
				Expect(c.ExternalID()).NotTo(Equal("mov"))
			}
		})
	})
})
