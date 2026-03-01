package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	// DatabaseURL is the PostgreSQL connection string.
	DatabaseURL string `env:"DATABASE_URL" envDefault:"postgres://jellyfin:jellyfin@localhost:5432/jellyfin_proxy?sslmode=disable"`
	// ListenAddr is the address the proxy HTTP server binds to.
	ListenAddr string `env:"LISTEN_ADDR" envDefault:":8096"`
	// ExternalURL is the publicly reachable URL for this proxy, reported to clients.
	ExternalURL string `env:"EXTERNAL_URL" envDefault:"http://localhost:8096"`
	// ServerID is the UUID the proxy presents as its Jellyfin server ID.
	ServerID string `env:"SERVER_ID" envDefault:"jellyfin-proxy-default-id"`
	// ServerName is the human-readable name reported to clients.
	ServerName string `env:"SERVER_NAME" envDefault:"Jellyfin Proxy"`
	// SessionTTL is how long a session token remains valid after its last activity.
	// Set to 0 to disable expiry (not recommended for production).
	SessionTTL time.Duration `env:"SESSION_TTL" envDefault:"720h"`
	// LoginMaxAttempts is the number of failed login attempts allowed per IP
	// within LoginWindow before the IP is temporarily blocked.
	LoginMaxAttempts int `env:"LOGIN_MAX_ATTEMPTS" envDefault:"10"`
	// LoginWindow is the sliding window duration for counting failed logins.
	LoginWindow time.Duration `env:"LOGIN_WINDOW" envDefault:"15m"`
	// LoginBanDuration is how long an IP is blocked after exceeding LoginMaxAttempts.
	LoginBanDuration time.Duration `env:"LOGIN_BAN_DURATION" envDefault:"15m"`
	// InitialAdminUser is the username for the auto-created admin account on first
	// startup. Only used when no users exist in the database.
	InitialAdminUser string `env:"INITIAL_ADMIN_USER" envDefault:"admin"`
	// InitialAdminPassword is the plaintext password for the auto-created admin
	// account. Only used when no users exist in the database.
	InitialAdminPassword string `env:"INITIAL_ADMIN_PASSWORD"`
	// ShutdownTimeout is the maximum duration to wait for in-flight requests
	// to complete during graceful shutdown.
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`
	// CORSOrigins is an additional set of origins (comma-separated) that are
	// allowed to make credentialed cross-origin requests. The ExternalURL
	// origin is always included automatically.
	CORSOrigins []string `env:"CORS_ORIGINS" envSeparator:","`
	// BitrateLimit is the maximum bitrate (in bits/s) that clients are allowed
	// to stream at. 0 means unlimited. Applied via the Jellyfin user policy's
	// RemoteClientBitrateLimit field.
	BitrateLimit int `env:"BITRATE_LIMIT" envDefault:"0"`
	// HealthCheckInterval is how often the proxy pings each backend to determine
	// availability. Backends that fail 2 consecutive checks are skipped in
	// fan-out requests until they recover. Default: 30s.
	HealthCheckInterval time.Duration `env:"HEALTH_CHECK_INTERVAL" envDefault:"30s"`
}

// Load parses configuration from environment variables.
// Returns an error if a value cannot be parsed into the expected type.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}
