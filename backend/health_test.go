package backend_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellymux/backend"
	"github.com/ddevcap/jellymux/config"
)

var _ = Describe("HealthChecker", func() {
	BeforeEach(func() {
		cleanDB()
	})

	It("marks a healthy backend as available", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ServerName":"test"}`))
		}))
		defer srv.Close()

		ctx := context.Background()
		pool := backend.NewPool(db, config.Config{ServerID: "test"})

		b, err := db.Backend.Create().
			SetName("healthy").
			SetURL(srv.URL).
			SetExternalID("jf-healthy").
			SetEnabled(true).
			Save(ctx)
		Expect(err).NotTo(HaveOccurred())

		hc := backend.NewHealthChecker(pool, 100*time.Millisecond)
		hc.Start(ctx)
		defer hc.Stop()

		Eventually(func() bool {
			return hc.IsAvailable(b.ID.String())
		}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())
	})

	It("marks an unreachable backend as unavailable after consecutive failures", func() {
		ctx := context.Background()
		pool := backend.NewPool(db, config.Config{ServerID: "test"})

		b, err := db.Backend.Create().
			SetName("dead").
			SetURL("http://127.0.0.1:1"). // nothing listening
			SetExternalID("jf-dead").
			SetEnabled(true).
			Save(ctx)
		Expect(err).NotTo(HaveOccurred())

		hc := backend.NewHealthChecker(pool, 100*time.Millisecond)
		hc.Start(ctx)
		defer hc.Stop()

		// Should become unavailable after 2 consecutive failures.
		Eventually(func() bool {
			return !hc.IsAvailable(b.ID.String())
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())
	})

	It("recovers a backend when it comes back online", func() {
		var healthy atomic.Bool
		healthy.Store(true)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if healthy.Load() {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		}))
		defer srv.Close()

		ctx := context.Background()
		pool := backend.NewPool(db, config.Config{ServerID: "test"})

		b, err := db.Backend.Create().
			SetName("flaky").
			SetURL(srv.URL).
			SetExternalID("jf-flaky").
			SetEnabled(true).
			Save(ctx)
		Expect(err).NotTo(HaveOccurred())

		hc := backend.NewHealthChecker(pool, 100*time.Millisecond)
		hc.Start(ctx)
		defer hc.Stop()

		// Starts healthy.
		Eventually(func() bool {
			return hc.IsAvailable(b.ID.String())
		}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

		// Take it down.
		healthy.Store(false)
		Eventually(func() bool {
			return !hc.IsAvailable(b.ID.String())
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())

		// Bring it back.
		healthy.Store(true)
		Eventually(func() bool {
			return hc.IsAvailable(b.ID.String())
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())
	})

	Describe("RecordRequestFailure (circuit breaker)", func() {
		It("trips the circuit after threshold failures", func() {
			ctx := context.Background()
			pool := backend.NewPool(db, config.Config{ServerID: "test"})

			// Create a healthy backend so the checker starts it as available.
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			b, err := db.Backend.Create().
				SetName("circuit-test").
				SetURL(srv.URL).
				SetExternalID("jf-circuit").
				SetEnabled(true).
				Save(ctx)
			Expect(err).NotTo(HaveOccurred())

			hc := backend.NewHealthChecker(pool, 1*time.Hour) // long interval so only manual checks
			hc.Start(ctx)
			defer hc.Stop()

			// Wait for initial check to mark it available.
			Eventually(func() bool {
				return hc.IsAvailable(b.ID.String())
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			// Record failures below the threshold — should stay available.
			for i := 0; i < 4; i++ {
				hc.RecordRequestFailure(b.ID.String(), "circuit-test")
			}
			Expect(hc.IsAvailable(b.ID.String())).To(BeTrue())

			// One more failure should trip the breaker (threshold = 5).
			hc.RecordRequestFailure(b.ID.String(), "circuit-test")
			Expect(hc.IsAvailable(b.ID.String())).To(BeFalse())
		})

		It("resets failure count on success", func() {
			ctx := context.Background()
			pool := backend.NewPool(db, config.Config{ServerID: "test"})

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			b, err := db.Backend.Create().
				SetName("reset-test").
				SetURL(srv.URL).
				SetExternalID("jf-reset").
				SetEnabled(true).
				Save(ctx)
			Expect(err).NotTo(HaveOccurred())

			hc := backend.NewHealthChecker(pool, 1*time.Hour)
			hc.Start(ctx)
			defer hc.Stop()

			Eventually(func() bool {
				return hc.IsAvailable(b.ID.String())
			}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())

			// Record 3 failures then a success — counter should reset.
			for i := 0; i < 3; i++ {
				hc.RecordRequestFailure(b.ID.String(), "reset-test")
			}
			hc.RecordRequestSuccess(b.ID.String())

			// Now 4 more failures should NOT trip (only 4, not 5).
			for i := 0; i < 4; i++ {
				hc.RecordRequestFailure(b.ID.String(), "reset-test")
			}
			Expect(hc.IsAvailable(b.ID.String())).To(BeTrue())
		})
	})

	Describe("Statuses", func() {
		It("returns status snapshots for tracked backends", func() {
			ctx := context.Background()
			pool := backend.NewPool(db, config.Config{ServerID: "test"})

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			b, err := db.Backend.Create().
				SetName("status-test").
				SetURL(srv.URL).
				SetExternalID("jf-status").
				SetEnabled(true).
				Save(ctx)
			Expect(err).NotTo(HaveOccurred())

			hc := backend.NewHealthChecker(pool, 100*time.Millisecond)
			hc.Start(ctx)
			defer hc.Stop()

			Eventually(func() int {
				return len(hc.Statuses())
			}, 2*time.Second, 50*time.Millisecond).Should(BeNumerically(">=", 1))

			statuses := hc.Statuses()
			found := false
			for _, s := range statuses {
				if s.BackendID == b.ID.String() {
					found = true
					Expect(s.Available).To(BeTrue())
					Expect(s.FailureCount).To(Equal(0))
				}
			}
			Expect(found).To(BeTrue())
		})
	})
})
