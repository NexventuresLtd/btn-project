// Package deps is the dependency bundle handed to every module's Register function.
package deps

import (
	"btn-ops/internal/auth"
	"btn-ops/internal/config"
	"btn-ops/internal/mail"
	"btn-ops/internal/settings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps is created once by internal/app and shared (read-only) by all modules.
type Deps struct {
	Cfg      *config.Config
	Pool     *pgxpool.Pool
	Settings *settings.Service
	Auth     *auth.Service
	Mail     mail.Sender
}
