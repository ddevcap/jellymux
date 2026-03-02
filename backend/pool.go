// Package backend provides an HTTP client for forwarding requests to backend
// Jellyfin servers with per-user credential resolution and ID rewriting.
package backend

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ddevcap/jellymux/config"
	"github.com/ddevcap/jellymux/ent"
	entbackend "github.com/ddevcap/jellymux/ent/backend"
	entbackenduser "github.com/ddevcap/jellymux/ent/backenduser"
	entuser "github.com/ddevcap/jellymux/ent/user"
)

// Pool manages HTTP connections to all registered backend Jellyfin servers.
// A single Pool is created at startup and shared across all request handlers.
type Pool struct {
	db            *ent.Client
	cfg           config.Config
	proxyServerID string       // dashless 32-char hex proxy server ID, pre-computed once
	jsonClient    *http.Client // bounded timeout — for JSON API calls
	streamClient  *http.Client // no total timeout — for binary media streams
	health        *HealthChecker
}

func NewPool(db *ent.Client, cfg config.Config) *Pool {
	// JSON transport: short timeouts for API calls.
	jsonTransport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConnsPerHost:   10,
	}
	// Stream transport: longer header timeout to handle slow-starting transcoding.
	// The backend may take many seconds to produce the first bytes of a segment
	// while ffmpeg encodes. No total timeout — streams run indefinitely.
	streamTransport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 60 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute, // segments can take time to start
		MaxIdleConnsPerHost:   20,
		DisableCompression:    true, // avoid buffering compressed streams
	}
	return &Pool{
		db:            db,
		cfg:           cfg,
		proxyServerID: strings.ReplaceAll(cfg.ServerID, "-", ""),
		jsonClient: &http.Client{
			Transport: jsonTransport,
			Timeout:   10 * time.Second,
		},
		streamClient: &http.Client{
			Transport: streamTransport,
			Timeout:   0, // streams can run indefinitely
		},
	}
}

// SetHealthChecker attaches a health checker. Must be called before serving.
func (p *Pool) SetHealthChecker(hc *HealthChecker) { p.health = hc }

// GetHealthChecker returns the attached health checker, or nil.
func (p *Pool) GetHealthChecker() *HealthChecker { return p.health }

// isAvailable reports whether the backend is reachable.
// Without a health checker, all backends are assumed available.
func (p *Pool) isAvailable(backendID string) bool {
	if p.health == nil {
		return true
	}
	return p.health.IsAvailable(backendID)
}

// ForUser returns a ServerClient for the given backend and user.
// When no user mapping exists, the token and backend user ID will be empty.
func (p *Pool) ForUser(ctx context.Context, externalID string, user *ent.User) (*ServerClient, error) {
	b, err := p.db.Backend.Query().
		Where(entbackend.ExternalID(externalID), entbackend.Enabled(true)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("backend: server %q not found: %w", externalID, err)
	}

	var token string
	var backendUserID string

	bu, err := p.db.BackendUser.Query().
		Where(
			entbackenduser.HasUserWith(entuser.ID(user.ID)),
			entbackenduser.HasBackendWith(entbackend.ID(b.ID)),
			entbackenduser.Enabled(true),
		).
		Only(ctx)
	if err == nil {
		backendUserID = bu.BackendUserID
		if bu.BackendToken != nil {
			token = *bu.BackendToken
		}
	}

	return &ServerClient{
		backend:       b,
		token:         token,
		backendUserID: backendUserID,
		pool:          p,
	}, nil
}

// AllForUser returns a ServerClient for every enabled, healthy backend the
// user is mapped to.
func (p *Pool) AllForUser(ctx context.Context, user *ent.User) ([]*ServerClient, error) {
	backendUsers, err := p.db.BackendUser.Query().
		Where(
			entbackenduser.HasUserWith(entuser.ID(user.ID)),
			entbackenduser.Enabled(true),
		).
		WithBackend(func(q *ent.BackendQuery) {
			q.Where(entbackend.Enabled(true))
		}).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("backend: querying user backends: %w", err)
	}

	clients := make([]*ServerClient, 0, len(backendUsers))
	for _, bu := range backendUsers {
		b := bu.Edges.Backend
		if b == nil {
			continue // backend disabled
		}
		if !p.isAvailable(b.ID.String()) {
			continue // backend offline — skip to avoid timeout
		}
		var token string
		if bu.BackendToken != nil {
			token = *bu.BackendToken
		}
		clients = append(clients, &ServerClient{
			backend:       b,
			token:         token,
			backendUserID: bu.BackendUserID,
			pool:          p,
		})
	}
	return clients, nil
}

// ForBackend returns a ServerClient without user credentials.
// Used for public requests (images, etc.) where no user session is available.
func (p *Pool) ForBackend(ctx context.Context, externalID string) (*ServerClient, error) {
	b, err := p.db.Backend.Query().
		Where(entbackend.ExternalID(externalID), entbackend.Enabled(true)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("backend: server %q not found: %w", externalID, err)
	}
	return &ServerClient{
		backend: b,
		pool:    p,
	}, nil
}
