package middleware

import (
	"net/http"
	"regexp"
	"time"

	"github.com/ddevcap/jellymux/config"
	"github.com/ddevcap/jellymux/ent"
	entsession "github.com/ddevcap/jellymux/ent/session"
	"github.com/gin-gonic/gin"
)

const (
	ContextKeyUser    = "user"
	ContextKeySession = "session"
)

// mediaBrowserParamRe matches key="value" pairs in a MediaBrowser auth header.
var mediaBrowserParamRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// ParseMediaBrowserAuth parses the Jellyfin Authorization header into a map.
// Header format: MediaBrowser Client="...", Device="...", DeviceId="...", Version="...", Token="...".
func ParseMediaBrowserAuth(header string) map[string]string {
	result := make(map[string]string)
	for _, match := range mediaBrowserParamRe.FindAllStringSubmatch(header, -1) {
		result[match[1]] = match[2]
	}
	return result
}

// ExtractToken retrieves the bearer token from the request using Jellyfin's
// supported auth mechanisms, in priority order:
//  1. X-Emby-Token / X-MediaBrowser-Token headers
//  2. Token field in the MediaBrowser Authorization header
//  3. api_key query parameter
func ExtractToken(c *gin.Context) string {
	if token := c.GetHeader("X-Emby-Token"); token != "" {
		return token
	}
	if token := c.GetHeader("X-MediaBrowser-Token"); token != "" {
		return token
	}
	if auth := c.GetHeader("Authorization"); auth != "" {
		if token := ParseMediaBrowserAuth(auth)["Token"]; token != "" {
			return token
		}
	}
	// Jellyfin clients use both "api_key" and "ApiKey" in query strings.
	if token := c.Query("api_key"); token != "" {
		return token
	}
	return c.Query("ApiKey")
}

// ExtractAllTokens returns every candidate auth token from the request.
// Headers are returned first (highest priority), followed by all api_key and
// ApiKey query parameter values. This is needed on public streaming routes
// where HLS URLs may carry both a leaked backend token and the injected proxy
// session token — the caller tries each until one matches a valid session.
func ExtractAllTokens(c *gin.Context) []string {
	var tokens []string
	if t := c.GetHeader("X-Emby-Token"); t != "" {
		tokens = append(tokens, t)
	}
	if t := c.GetHeader("X-MediaBrowser-Token"); t != "" {
		tokens = append(tokens, t)
	}
	if auth := c.GetHeader("Authorization"); auth != "" {
		if t := ParseMediaBrowserAuth(auth)["Token"]; t != "" {
			tokens = append(tokens, t)
		}
	}
	tokens = append(tokens, c.QueryArray("api_key")...)
	tokens = append(tokens, c.QueryArray("ApiKey")...)
	return tokens
}

// Auth validates the Jellyfin token on every protected request, loads the
// associated user, and stores both in the gin context for downstream handlers.
// If cfg.SessionTTL > 0 sessions that have been idle longer than the TTL are
// rejected and deleted automatically.
func Auth(db *ent.Client, cfg config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := ExtractToken(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}

		session, err := db.Session.Query().
			Where(entsession.Token(token)).
			WithUser().
			Only(c.Request.Context())
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}

		// Enforce session TTL based on last activity.
		if cfg.SessionTTL > 0 && time.Since(session.LastActivity) > cfg.SessionTTL {
			// Delete the expired session so it doesn't accumulate.
			_ = db.Session.DeleteOne(session).Exec(c.Request.Context())
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Session expired"})
			return
		}

		// Debounce last-activity updates to avoid a DB write on every request.
		// Only update if the last recorded activity was more than 5 minutes ago.
		if time.Since(session.LastActivity) > 5*time.Minute {
			_ = session.Update().SetLastActivity(time.Now()).Exec(c.Request.Context())
		}

		c.Set(ContextKeyUser, session.Edges.User)
		c.Set(ContextKeySession, session)
		c.Next()
	}
}
