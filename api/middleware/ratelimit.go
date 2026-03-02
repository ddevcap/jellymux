package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/ddevcap/jellymux/config"
	"github.com/gin-gonic/gin"
	"github.com/jellydator/ttlcache/v3"
)

// ipEntry tracks failed login attempts for a single IP.
type ipEntry struct {
	attempts    int
	windowEnd   time.Time // when the current sliding window expires
	bannedUntil time.Time
}

// loginLimiter is an in-memory rate limiter for the login endpoint.
// It uses ttlcache for automatic expiry of stale entries.
type loginLimiter struct {
	mu    sync.Mutex
	cache *ttlcache.Cache[string, *ipEntry]
	cfg   config.Config
	ttl   time.Duration // per-entry TTL = max(window, ban)
}

func newLoginLimiter(cfg config.Config) *loginLimiter {
	entryTTL := cfg.LoginWindow
	if cfg.LoginBanDuration > entryTTL {
		entryTTL = cfg.LoginBanDuration
	}
	if entryTTL <= 0 {
		entryTTL = 15 * time.Minute // sensible fallback
	}

	cache := ttlcache.New[string, *ipEntry](
		ttlcache.WithTTL[string, *ipEntry](entryTTL),
	)
	go cache.Start() // background eviction of expired entries

	return &loginLimiter{
		cache: cache,
		cfg:   cfg,
		ttl:   entryTTL,
	}
}

// allow returns true if the IP is permitted to attempt a login.
// Call recordFailure after a failed attempt.
func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	item := l.cache.Get(ip)
	if item == nil {
		return true
	}
	if time.Now().Before(item.Value().bannedUntil) {
		return false
	}
	return true
}

// recordFailure increments the failure count for an IP and bans it if
// the threshold is exceeded within the window.
func (l *loginLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()

	var e *ipEntry
	if item := l.cache.Get(ip); item != nil {
		e = item.Value()
	}

	if e == nil || now.After(e.windowEnd) {
		// Start a fresh window.
		e = &ipEntry{
			attempts:  1,
			windowEnd: now.Add(l.cfg.LoginWindow),
		}
		l.cache.Set(ip, e, l.ttl)
		return
	}
	e.attempts++
	if e.attempts >= l.cfg.LoginMaxAttempts {
		e.bannedUntil = now.Add(l.cfg.LoginBanDuration)
	}
	l.cache.Set(ip, e, l.ttl)
}

// recordSuccess resets the failure count for an IP after a successful login.
func (l *loginLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cache.Delete(ip)
}

// LoginRateLimiter returns a middleware + a recordFailure/recordSuccess pair
// so the auth handler can signal outcomes.
// Returns the middleware, two callbacks: onFailure(ip), onSuccess(ip), and a
// stop function to clean up the background goroutine on shutdown.
func LoginRateLimiter(cfg config.Config) (gin.HandlerFunc, func(string), func(string), func()) {
	limiter := newLoginLimiter(cfg)

	mw := func(c *gin.Context) {
		if cfg.LoginMaxAttempts <= 0 {
			c.Next()
			return
		}
		ip := ClientIP(c)
		if !limiter.allow(ip) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Too many failed login attempts. Please try again later.",
			})
			return
		}
		c.Next()
	}

	stop := func() { limiter.cache.Stop() }

	return mw, limiter.recordFailure, limiter.recordSuccess, stop
}

// ClientIP extracts the client IP using Gin's built-in ClientIP method,
// which honours the engine's trusted-proxy configuration and safely handles
// X-Forwarded-For chains. Falls back to RemoteAddr when no proxy is trusted.
func ClientIP(c *gin.Context) string {
	return c.ClientIP()
}
