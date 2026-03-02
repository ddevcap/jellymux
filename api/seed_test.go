package api_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/crypto/bcrypt"

	"github.com/ddevcap/jellymux/api"
	"github.com/ddevcap/jellymux/config"
)

var _ = Describe("SeedInitialAdmin", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
		cleanDB()
	})

	Context("when no users exist and a password is configured", func() {
		It("creates an admin user with the configured username and a valid password hash", func() {
			cfg := config.Config{
				InitialAdminUser:     "seedadmin",
				InitialAdminPassword: "seedpassword",
			}

			api.SeedInitialAdmin(ctx, db, cfg)

			count, err := db.User.Query().Count(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(count).To(Equal(1))

			u, err := db.User.Query().Only(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(u.Username).To(Equal("seedadmin"))
			Expect(u.IsAdmin).To(BeTrue())
			Expect(bcrypt.CompareHashAndPassword([]byte(u.HashedPassword), []byte("seedpassword"))).To(Succeed())
		})
	})

	Context("when no users exist and InitialAdminPassword is empty", func() {
		It("skips seeding and leaves the database empty", func() {
			cfg := config.Config{
				InitialAdminUser:     "admin",
				InitialAdminPassword: "",
			}

			api.SeedInitialAdmin(ctx, db, cfg)

			count, err := db.User.Query().Count(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(count).To(Equal(0))
		})
	})

	Context("when users already exist", func() {
		It("is a no-op and does not create an additional user", func() {
			db.User.Create().
				SetUsername("existing").
				SetDisplayName("Existing").
				SetHashedPassword("hash").
				SetIsAdmin(false).
				SaveX(ctx)

			cfg := config.Config{
				InitialAdminUser:     "admin",
				InitialAdminPassword: "password",
			}

			api.SeedInitialAdmin(ctx, db, cfg)

			count, err := db.User.Query().Count(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(count).To(Equal(1))
		})
	})
})
