package config_test

import (
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ddevcap/jellymux/config"
)

var _ = Describe("Load", func() {
	// Keys managed by these tests — saved and restored around each spec.
	var envKeys = []string{
		"DATABASE_URL", "LISTEN_ADDR", "EXTERNAL_URL", "SERVER_ID", "SERVER_NAME",
		"SESSION_TTL", "LOGIN_MAX_ATTEMPTS", "LOGIN_WINDOW", "LOGIN_BAN_DURATION",
		"INITIAL_ADMIN_USER", "INITIAL_ADMIN_PASSWORD",
	}

	var saved map[string]string

	BeforeEach(func() {
		saved = make(map[string]string, len(envKeys))
		for _, k := range envKeys {
			saved[k] = os.Getenv(k)
			Expect(os.Unsetenv(k)).To(Succeed())
		}
	})

	AfterEach(func() {
		for k, v := range saved {
			if v == "" {
				Expect(os.Unsetenv(k)).To(Succeed())
			} else {
				Expect(os.Setenv(k, v)).To(Succeed())
			}
		}
	})

	It("returns defaults when no env vars are set", func() {
		cfg, err := config.Load()
		Expect(err).NotTo(HaveOccurred())

		Expect(cfg.DatabaseURL).To(Equal("postgres://jellyfin:jellyfin@localhost:5432/jellymux?sslmode=disable"))
		Expect(cfg.ListenAddr).To(Equal(":8096"))
		Expect(cfg.ExternalURL).To(Equal("http://localhost:8096"))
		Expect(cfg.ServerID).To(Equal("jellymux-default-id"))
		Expect(cfg.ServerName).To(Equal("Jellymux"))
		Expect(cfg.SessionTTL).To(Equal(30 * 24 * time.Hour))
		Expect(cfg.LoginMaxAttempts).To(Equal(10))
		Expect(cfg.LoginWindow).To(Equal(15 * time.Minute))
		Expect(cfg.LoginBanDuration).To(Equal(15 * time.Minute))
		Expect(cfg.InitialAdminUser).To(Equal("admin"))
		Expect(cfg.InitialAdminPassword).To(BeEmpty())
	})

	It("reads string values from env vars", func() {
		Expect(os.Setenv("DATABASE_URL", "postgres://custom:pass@db:5432/mydb?sslmode=disable")).To(Succeed())
		Expect(os.Setenv("LISTEN_ADDR", ":9090")).To(Succeed())
		Expect(os.Setenv("EXTERNAL_URL", "https://jellyfin.example.com")).To(Succeed())
		Expect(os.Setenv("SERVER_ID", "my-server-id")).To(Succeed())
		Expect(os.Setenv("SERVER_NAME", "My Proxy")).To(Succeed())
		Expect(os.Setenv("INITIAL_ADMIN_USER", "superadmin")).To(Succeed())
		Expect(os.Setenv("INITIAL_ADMIN_PASSWORD", "secret123")).To(Succeed())

		cfg, err := config.Load()
		Expect(err).NotTo(HaveOccurred())

		Expect(cfg.DatabaseURL).To(Equal("postgres://custom:pass@db:5432/mydb?sslmode=disable"))
		Expect(cfg.ListenAddr).To(Equal(":9090"))
		Expect(cfg.ExternalURL).To(Equal("https://jellyfin.example.com"))
		Expect(cfg.ServerID).To(Equal("my-server-id"))
		Expect(cfg.ServerName).To(Equal("My Proxy"))
		Expect(cfg.InitialAdminUser).To(Equal("superadmin"))
		Expect(cfg.InitialAdminPassword).To(Equal("secret123"))
	})

	It("reads duration values from env vars", func() {
		Expect(os.Setenv("SESSION_TTL", "1h")).To(Succeed())
		Expect(os.Setenv("LOGIN_WINDOW", "5m")).To(Succeed())
		Expect(os.Setenv("LOGIN_BAN_DURATION", "30m")).To(Succeed())

		cfg, err := config.Load()
		Expect(err).NotTo(HaveOccurred())

		Expect(cfg.SessionTTL).To(Equal(time.Hour))
		Expect(cfg.LoginWindow).To(Equal(5 * time.Minute))
		Expect(cfg.LoginBanDuration).To(Equal(30 * time.Minute))
	})

	It("returns an error for an invalid duration", func() {
		Expect(os.Setenv("SESSION_TTL", "not-a-duration")).To(Succeed())

		_, err := config.Load()
		Expect(err).To(HaveOccurred())
	})

	It("returns an error for an invalid int", func() {
		Expect(os.Setenv("LOGIN_MAX_ATTEMPTS", "not-a-number")).To(Succeed())

		_, err := config.Load()
		Expect(err).To(HaveOccurred())
	})
})
