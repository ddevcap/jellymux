package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/ddevcap/jellymux/config"
	"github.com/ddevcap/jellymux/ent"
	entsession "github.com/ddevcap/jellymux/ent/session"
)

// SessionCleaner periodically deletes expired sessions from the database.
// This prevents the session table from growing unbounded when clients don't
// explicitly log out.
type SessionCleaner struct {
	db     *ent.Client
	cfg    config.Config
	cancel context.CancelFunc
	done   chan struct{}
}

// NewSessionCleaner creates a cleaner that runs every hour.
func NewSessionCleaner(db *ent.Client, cfg config.Config) *SessionCleaner {
	return &SessionCleaner{
		db:   db,
		cfg:  cfg,
		done: make(chan struct{}),
	}
}

// Start begins the background cleanup loop.
func (sc *SessionCleaner) Start(ctx context.Context) {
	ctx, sc.cancel = context.WithCancel(ctx)
	go func() {
		defer close(sc.done)
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sc.Cleanup(ctx)
			}
		}
	}()
}

// Stop signals the cleanup loop to stop and waits for it.
func (sc *SessionCleaner) Stop() {
	if sc.cancel != nil {
		sc.cancel()
	}
	<-sc.done
}

// Cleanup deletes sessions that have been inactive longer than the configured
// SessionTTL.
func (sc *SessionCleaner) Cleanup(ctx context.Context) {
	if sc.cfg.SessionTTL <= 0 {
		return // no TTL configured, nothing to clean
	}
	cutoff := time.Now().Add(-sc.cfg.SessionTTL)
	n, err := sc.db.Session.Delete().
		Where(entsession.LastActivityLT(cutoff)).
		Exec(ctx)
	if err != nil {
		slog.Warn("session cleanup failed", "error", err)
		return
	}
	if n > 0 {
		slog.Info("expired sessions cleaned up", "count", n)
	}
}
