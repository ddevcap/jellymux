package api

import (
	"context"
	"log/slog"

	"github.com/ddevcap/jellymux/api/handler"
	"github.com/ddevcap/jellymux/config"
	"github.com/ddevcap/jellymux/ent"
	"golang.org/x/crypto/bcrypt"
)

// SeedInitialAdmin creates a single admin user when the database has no users
// yet. It is a no-op when users already exist, so it is safe to call on every
// startup.
//
// The credentials are taken from cfg.InitialAdminUser /
// cfg.InitialAdminPassword. If InitialAdminPassword is empty the function logs
// a warning and skips seeding — the operator must set INITIAL_ADMIN_PASSWORD.
func SeedInitialAdmin(ctx context.Context, db *ent.Client, cfg config.Config) {
	count, err := db.User.Query().Count(ctx)
	if err != nil {
		slog.Error("seed: failed to count users", "error", err)
		return
	}
	if count > 0 {
		// Users already exist; nothing to do.
		return
	}

	if cfg.InitialAdminPassword == "" {
		slog.Warn("seed: no users found but INITIAL_ADMIN_PASSWORD is not set — skipping admin seeding. " +
			"Set INITIAL_ADMIN_PASSWORD to auto-create the first admin on startup.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.InitialAdminPassword), handler.BcryptCost)
	if err != nil {
		slog.Error("seed: failed to hash initial admin password", "error", err)
		return
	}

	_, err = db.User.Create().
		SetUsername(cfg.InitialAdminUser).
		SetDisplayName(cfg.InitialAdminUser).
		SetHashedPassword(string(hash)).
		SetIsAdmin(true).
		Save(ctx)
	if err != nil {
		slog.Error("seed: failed to create initial admin user", "error", err)
		return
	}

	slog.Info("seed: created initial admin user", "username", cfg.InitialAdminUser)
}
