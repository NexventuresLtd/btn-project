# BTN Operations API (Go). Requires Go >= 1.26 and PostgreSQL; configuration comes from backend/.env.
# If `go` is not on your PATH (e.g. snap installs): make GO=/snap/go/current/bin/go <target>
GO ?= go
PORT ?= 4000

.PHONY: import run build test test-race migrate-up migrate-down migrate-status seed lint vet dev-users test-db pdf-demo bench tidy

run:            ## run the API on $(PORT) (dev: AUTH_DEV_LOG_OTP=true prints OTP codes to the log)
	PORT=$(PORT) $(GO) run ./cmd/server

build:          ## static binaries in ./bin (same flags as the Dockerfile; timetzdata embeds Africa/Kigali)
	CGO_ENABLED=0 $(GO) build -trimpath -tags timetzdata -ldflags="-s -w" -o bin/ ./cmd/server ./cmd/migrate ./cmd/seed ./cmd/import

test:           ## unit + HTTP tests (each test process gets its own schema in the *_test database)
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

migrate-up:     ## apply goose migrations to DATABASE_URL
	$(GO) run ./cmd/migrate up

migrate-down:   ## roll back one migration
	$(GO) run ./cmd/migrate down

migrate-status:
	$(GO) run ./cmd/migrate status

import:         ## legacy Excel import of data/samples/*.xlsx: make import ARGS="--dry-run" | ARGS=--replace (see cmd/import)
	$(GO) run ./cmd/import $(ARGS)

seed:           ## idempotent: plants, 11 products, 6 materials, settings, branding, super admin from SUPER_ADMIN_*
	$(GO) run ./cmd/seed

dev-users:      ## DEV ONLY: one account per role incl. super@btn.test (password DevPass1234!)
	$(GO) run ./cmd/devtools users

lint vet:       ## go vet
	$(GO) vet ./...

test-db:        ## create (if missing) and migrate the public schema of the test database
	MIGRATE_TARGET=test $(GO) run ./cmd/migrate up

pdf-demo:       ## render the en/am/rw sample report PDFs into ./tmp-pdf
	mkdir -p tmp-pdf
	PDF_DEMO_DIR=$(CURDIR)/tmp-pdf $(GO) test ./internal/pdf -run Demo -count=1

bench:          ## cached /api/lookups latency (p50/p95/p99)
	$(GO) test ./internal/app -run xxx -bench Lookups -benchtime 20000x

tidy:
	$(GO) mod tidy
