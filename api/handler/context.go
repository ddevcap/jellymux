package handler

import (
	"net"
	"strings"
	"sync"

	"github.com/ddevcap/jellyfin-proxy/api/middleware"
	"github.com/ddevcap/jellyfin-proxy/config"
	"github.com/ddevcap/jellyfin-proxy/ent"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func userFromCtx(c *gin.Context) *ent.User {
	u, _ := c.Get(middleware.ContextKeyUser)
	user, _ := u.(*ent.User)
	return user
}

// Fallback returns s if non-empty, otherwise def.
func Fallback(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// NilIfEmpty returns nil for the empty string, otherwise a pointer to s.
func NilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// dashlessUUID returns a 32-char hex ID (the format Jellyfin SDKs expect).
func dashlessUUID(id uuid.UUID) string {
	return strings.ReplaceAll(id.String(), "-", "")
}

// dashlessID strips dashes from a string UUID (e.g. cfg.ServerID).
func dashlessID(s string) string {
	return strings.ReplaceAll(s, "-", "")
}

// parsedNetworks lazily parses DirectStreamNetworks CIDRs once.
var (
	parsedNetworks   []*net.IPNet
	parsedNetworksOK bool
	parseOnce        sync.Once
)

func directStreamNets(cfg config.Config) []*net.IPNet {
	parseOnce.Do(func() {
		for _, cidr := range cfg.DirectStreamNetworks {
			cidr = strings.TrimSpace(cidr)
			if cidr == "" {
				continue
			}
			_, ipNet, err := net.ParseCIDR(cidr)
			if err != nil {
				continue
			}
			parsedNetworks = append(parsedNetworks, ipNet)
		}
		parsedNetworksOK = true
	})
	return parsedNetworks
}

// ShouldDirectStream returns true when streaming requests should be redirected
// directly to the backend (302) instead of being piped through the proxy.
// The decision is based on the user's direct_stream flag AND whether the client
// IP is on a local/allowed network. Remote clients always get proxied streams.
func ShouldDirectStream(user *ent.User, clientIP string, cfg config.Config) bool {
	if user == nil || !user.DirectStream {
		return false
	}

	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false
	}

	// If custom networks are configured, check those.
	nets := directStreamNets(cfg)
	if parsedNetworksOK && len(nets) > 0 {
		for _, n := range nets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}

	// Fall back to RFC 1918 / loopback / link-local.
	return ip.IsPrivate() || ip.IsLoopback()
}
