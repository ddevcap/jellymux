package handler

import (
	"strings"

	"github.com/ddevcap/jellyfin-proxy/api/middleware"
	"github.com/ddevcap/jellyfin-proxy/ent"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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

// jellyfinID converts a UUID to the dashless hex format that Jellyfin uses
// in API responses (e.g. "3c1e242c0e3f4c29a1350f70604586c7").
// The Jellyfin SDK deserializers expect this format.
func jellyfinID(id uuid.UUID) string {
	return strings.ReplaceAll(id.String(), "-", "")
}

// shouldDirectStream returns true when streaming requests should be redirected
// directly to the backend (302) instead of being piped through the proxy.
// The decision is based solely on the user's direct_stream field.
// When user is nil (unauthenticated request), defaults to false (proxy mode).
func shouldDirectStream(user *ent.User) bool {
	return user != nil && user.DirectStream
}
