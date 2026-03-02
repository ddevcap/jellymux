package middleware

import (
	"net/http"

	"github.com/ddevcap/jellymux/ent"
	"github.com/gin-gonic/gin"
)

// AdminOnly rejects requests from non-admin users.
// Must be placed after the Auth middleware in the chain.
func AdminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, exists := c.Get(ContextKeyUser)
		if !exists {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		user, ok := raw.(*ent.User)
		if !ok || !user.IsAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
			return
		}
		c.Next()
	}
}
