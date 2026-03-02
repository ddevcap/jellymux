package backend

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	entbackend "github.com/ddevcap/jellymux/ent/backend"
)

const (
	// Default interval between health checks.
	defaultHealthInterval = 30 * time.Second
	// Timeout for a single health-check ping.
	healthCheckTimeout = 5 * time.Second
)

// backendStatus tracks the availability of a single backend.
type backendStatus struct {
	available    bool
	lastChecked  time.Time
	lastErr      string
	failureCount int
}

// HealthChecker periodically pings every enabled backend and maintains an
// in-memory availability map. The Pool consults this map so that fan-out
// requests skip backends that are known to be offline.
type HealthChecker struct {
	pool     *Pool
	interval time.Duration

	mu       sync.RWMutex
	statuses map[string]*backendStatus // keyed by backend UUID string

	cancel context.CancelFunc
	done   chan struct{}
}

// NewHealthChecker creates a new health checker bound to the given pool.
// Call Start() to begin background checking.
func NewHealthChecker(pool *Pool, interval time.Duration) *HealthChecker {
	if interval <= 0 {
		interval = defaultHealthInterval
	}
	return &HealthChecker{
		pool:     pool,
		interval: interval,
		statuses: make(map[string]*backendStatus),
		done:     make(chan struct{}),
	}
}

// Start begins the background health-check loop. It runs an immediate check
// on startup, then repeats at the configured interval. Safe to call once.
func (hc *HealthChecker) Start(ctx context.Context) {
	ctx, hc.cancel = context.WithCancel(ctx)

	go func() {
		defer close(hc.done)

		// Immediate first check so backends are classified before the first request.
		hc.checkAll(ctx)

		ticker := time.NewTicker(hc.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hc.checkAll(ctx)
			}
		}
	}()
}

// Stop signals the health-check loop to stop and waits for it to finish.
func (hc *HealthChecker) Stop() {
	if hc.cancel != nil {
		hc.cancel()
	}
	<-hc.done
}

// IsAvailable reports whether the backend with the given UUID is considered
// reachable. Backends that have never been checked are assumed available so
// that the first requests aren't blocked.
func (hc *HealthChecker) IsAvailable(backendID string) bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	s, ok := hc.statuses[backendID]
	if !ok {
		return true // unknown = assume available until first check
	}
	return s.available
}

// RecordRequestFailure records a per-request failure for a backend (e.g.
// connection refused, timeout during a proxied request). This supplements
// the periodic health check — if a backend starts failing live requests,
// the circuit breaker trips it faster than waiting for the next health-check
// cycle. After consecutiveRequestFailuresThreshold failures, the backend is
// marked unavailable until the next successful health check restores it.
const consecutiveRequestFailuresThreshold = 5

func (hc *HealthChecker) RecordRequestFailure(backendID, name string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	s, ok := hc.statuses[backendID]
	if !ok {
		s = &backendStatus{available: true}
		hc.statuses[backendID] = s
	}

	s.failureCount++
	if s.failureCount >= consecutiveRequestFailuresThreshold && s.available {
		slog.Warn("circuit breaker: backend marked unavailable after repeated request failures",
			"backend", name, "id", backendID,
			"failures", s.failureCount)
		s.available = false
	}
}

// RecordRequestSuccess resets the per-request failure counter for a backend.
func (hc *HealthChecker) RecordRequestSuccess(backendID string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	s, ok := hc.statuses[backendID]
	if !ok {
		return
	}
	// Only reset request-failure count; don't change availability
	// (health checker owns that transition).
	if s.available {
		s.failureCount = 0
	}
}

// BackendHealthStatus is a snapshot of a backend's health for the admin API.
type BackendHealthStatus struct {
	BackendID    string    `json:"backend_id"`
	Available    bool      `json:"available"`
	LastChecked  time.Time `json:"last_checked"`
	LastError    string    `json:"last_error,omitempty"`
	FailureCount int       `json:"failure_count"`
}

// Statuses returns a snapshot of all tracked backend health statuses.
// Used by the admin API to expose health info.
func (hc *HealthChecker) Statuses() []BackendHealthStatus {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	result := make([]BackendHealthStatus, 0, len(hc.statuses))
	for id, s := range hc.statuses {
		result = append(result, BackendHealthStatus{
			BackendID:    id,
			Available:    s.available,
			LastChecked:  s.lastChecked,
			LastError:    s.lastErr,
			FailureCount: s.failureCount,
		})
	}
	return result
}

// checkAll queries the DB for all enabled backends and pings each one
// concurrently.
func (hc *HealthChecker) checkAll(ctx context.Context) {
	backends, err := hc.pool.db.Backend.Query().
		Where(entbackend.Enabled(true)).
		All(ctx)
	if err != nil {
		slog.Warn("health checker: failed to query backends", "error", err)
		return
	}

	var wg sync.WaitGroup
	for _, b := range backends {
		wg.Add(1)
		go func(id, name, rawURL string) {
			defer wg.Done()
			hc.checkOne(ctx, id, name, rawURL)
		}(b.ID.String(), b.Name, b.URL)
	}
	wg.Wait()
}

// checkOne pings a single backend's /System/Info/Public endpoint and updates
// the status map accordingly.
func (hc *HealthChecker) checkOne(ctx context.Context, id, name, rawURL string) {
	pingURL := strings.TrimRight(rawURL, "/") + "/System/Info/Public"

	reqCtx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, pingURL, nil)
	if err != nil {
		hc.recordResult(id, name, fmt.Errorf("bad url: %w", err))
		return
	}

	resp, err := hc.pool.jsonClient.Do(req)
	if err != nil {
		hc.recordResult(id, name, err)
		return
	}
	_ = resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		hc.recordResult(id, name, nil)
	} else {
		hc.recordResult(id, name, fmt.Errorf("status %d", resp.StatusCode))
	}
}

// recordResult updates the in-memory status for a backend.
// A backend is marked unavailable after 2 consecutive failures, and marked
// available again on the first success. This avoids flapping on transient
// single-request failures.
func (hc *HealthChecker) recordResult(id, name string, err error) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	s, ok := hc.statuses[id]
	if !ok {
		s = &backendStatus{available: true}
		hc.statuses[id] = s
	}

	s.lastChecked = time.Now()

	if err == nil {
		if !s.available {
			slog.Info("backend came back online", "backend", name, "id", id)
		}
		s.available = true
		s.failureCount = 0
		s.lastErr = ""
		return
	}

	s.failureCount++
	s.lastErr = err.Error()

	// Require 2 consecutive failures before marking unavailable to avoid
	// flapping on a single dropped packet.
	if s.failureCount >= 2 && s.available {
		slog.Warn("backend marked unavailable",
			"backend", name, "id", id,
			"failures", s.failureCount, "error", err)
		s.available = false
	}
}
