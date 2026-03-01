package handler

import (
	"github.com/ddevcap/jellyfin-proxy/api/middleware"
	"github.com/ddevcap/jellyfin-proxy/ent"
	"github.com/gin-gonic/gin"
)

// userFromCtx extracts the authenticated proxy user from the gin context.
func userFromCtx(c *gin.Context) *ent.User {
	u, _ := c.Get(middleware.ContextKeyUser)
	user, _ := u.(*ent.User)
	return user
}

func fallback(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// shouldDirectStream returns true when streaming requests should be redirected
// directly to the backend (302) instead of being piped through the proxy.
// The decision is based solely on the user's direct_stream field.
// When user is nil (unauthenticated request), defaults to false (proxy mode).
func shouldDirectStream(user *ent.User) bool {
	return user != nil && user.DirectStream
}
