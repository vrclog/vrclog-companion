# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

VRClog Companion is a Windows local resident app that passively reads VRChat's local log files, persists a canonical Observation stream to SQLite, and projects it into World/Presence/Media state — including recovering the original URL of media that failed to play in-world. It provides an HTTP API + Web UI and Discord Webhook notifications for World/Player changes.

**Key principle**: No central server. All data stays on the user's PC only.

This is one of three cooperating repositories: `vrclog-go` (canonical Event, Engine, log Follow) ← `vrclog-adapters` (community Adapters) ← `vrclog-companion` (this repo). Companion consumes their public contracts only — it does not implement its own Parser, Event type, or Adapter interface.

## Tech Stack

- Go 1.25+, SQLite (WAL mode), React + Vite (embedded via go:embed)
- Target: Windows 11 (macOS supported for development)
- Pure Go SQLite (modernc.org/sqlite, no CGO)

## Commands

```bash
go test ./...                                    # Run all tests
go test -run TestName ./internal/store           # Run single test
go test -tags=integration ./test/integration/... # Integration tests
go test -tags=e2e ./test/e2e/...                 # Media URL recovery E2E (real fixtures)
GOOS=windows GOARCH=amd64 go build -o vrclog.exe ./cmd/vrclog-companion  # Windows build
go build ./...                                   # Quick build check (cross-platform)
cd web && npm run build && cd .. && mkdir -p webembed/dist && cp -r web/dist/* webembed/dist/  # Build Web UI
cd web && npm run dev                            # Frontend dev server (proxy to :8080)
```

## Architecture

### Data Flow

```text
VRChat output_log → vrclog.Follow → Record
        ↓
Engine.Process (vrchat.core + community adapters) → Result{Observations, Diagnostics}
        ↓
Store.CommitRecord — ONE SQLite transaction: observations + diagnostics + cursor
        ↓ (newly inserted Observations only)
Projector Manager.Apply → World / Presence / Media state
        ├── SSE broadcast (generic "observation" event)
        └── Discord notification (World/Player Change only, never media URLs)
```

### Internal Packages

| Package | Purpose |
|---------|---------|
| `internal/adapter` | Compile-time Adapter composition (core + community) |
| `internal/api` | HTTP API server (JSON + SSE + Auth + Rate Limiting) |
| `internal/app` | Use case layer (business logic interfaces) |
| `internal/config` | Config/secrets management with atomic writes |
| `internal/ingest` | RecordSource + per-Record ingest Runner (bounded backoff retry) |
| `internal/notify` | Discord Webhook notifications, Change-based, sanitized |
| `internal/observation` | Observation persistence DTO + vrclog.Observation conversion |
| `internal/projector` | World/Presence/Media derived state, rebuildable from DB |
| `internal/sse` | Generic Observation broadcaster (single SSE event type) |
| `internal/store` | SQLite persistence (schema v3, CommitRecord atomicity) |
| `webembed` | Embedded web UI filesystem (go:embed) |

### Dependency Injection

- `internal/app` defines use case interfaces (e.g., `HealthUsecase`, `ObservationsUsecase`)
- `internal/api` depends only on interfaces, not implementations
- `cmd/vrclog-companion/main.go` wires concrete implementations

## Claude Directives

- Clarify unknowns before starting implementation
- Use `plan mode` for changes spanning 2+ files or design decisions
- Always run `go test ./...` and `GOOS=windows go build ./...` before completing
- Never log secrets - use `Secret` type with `[REDACTED]` output
- Respect existing design patterns; minimize change scope

## Workflow

- **Feature**: Explore → Plan → Code → Test → Commit
- **Bug fix**: Reproduce → Diagnose → Fix → Test → Commit

## Permanent Architectural Rules

These hold regardless of future feature work — violating them is a regression, not a stylistic choice:

- **Canonical Event is owned by `vrclog-go`.** Companion never defines its own Event/EventKind type or a competing Adapter interface.
- **Per-Record transaction.** `Store.CommitRecord` persists Observations + Diagnostics + cursor advancement atomically. The cursor is never committed separately from what produced it, even for zero-Observation Records.
- **Observation ID is the only dedupe key.** Never raw-line hashing, URL canonicalization, or time-window dedupe. A same-ID-different-content conflict rolls back the transaction (`ErrObservationConflict`) rather than silently overwriting.
- **Projectors rebuild from DB.** `projector.Manager.Rebuild` replays all Observations in sequence order at startup and must reach the same state a live `Apply` sequence would. Never persist projector state as a separate source of truth.
- **No legacy migration.** SQLite schema is versioned via `PRAGMA user_version`; any unexpected version is fatal, never auto-migrated. Old databases are reset by the user renaming/deleting the file, not by app code.
- **Media URLs are sensitive.** Never sent to Discord, never auto-opened, never fed to external metadata lookups (no oEmbed/thumbnail/title fetch). Only `http`/`https` schemes may ever be presented as an "open in browser" action, and only on explicit user click.
- **Generic SSE.** `/api/v1/stream` emits a single `event: observation` type; never add per-EventKind SSE event names. Last-Event-ID recovery uses `Store.LatestSequence()` (DB-backed, correct immediately after a process restart) as the backlog bound, not `Broadcaster.HighWaterSequence()` (in-memory, resets to 0 on restart) — see `internal/sse` and `internal/api/stream.go`.
- **Adapter composition is compile-time.** `internal/adapter.BuildEngine()` wires `vrclog.NewVRChatAdapter()` + explicit community adapter constructors (`yamaplayer.New()`, `iwasync3.New()`) in fixed order. No runtime plugin loading, no YAML pattern config, no remote adapter catalog.

## Key Design Decisions

- **Atomic writes**: Config files use tmp→rename (POSIX) or MoveFileEx (Windows)
- **Single instance**: Windows uses CreateMutex (session-scoped)
- **Security defaults**: Binds to `127.0.0.1` by default; LAN mode requires Basic Auth + rate limiting + auth-failure lockout + CSRF protection
- **Secrets safety**: `SecretsLoadStatus` prevents overwriting corrupted secrets files
- **Config resilience**: Corrupt/missing config falls back to defaults (non-fatal)
- **Cursor pagination**: `/api/v1/observations` uses the raw `sequence` int64 as an opaque cursor (no base64 encoding)
- **Timestamps**: Fixed-width RFC3339 (`2006-01-02T15:04:05.000000000Z`) for lexicographic ordering in storage; RFC3339Nano in API responses
- **Error responses**: Use `writeError(w, status, public, err)` for consistent JSON errors; 5xx logs internally
- **SSE reconnection**: Supports `Last-Event-ID` header and `last_event_id` query parameter; unknown IDs get an `event: reset` with empty `id:` rather than silent data loss
- **Readiness gate**: `Server.SetReady(false)` during startup Projector rebuild — every route except `/api/v1/health` returns 503 until rebuild completes

## Testing Patterns

- **Interface abstraction**: `RecordSource`/`RecordSourceFactory`, `RecordStore`, `Engine` interfaces allow mocking vrclog-go and the store in `internal/ingest`
- **Clock injection**: `WithClock()` option for deterministic timestamps
- **Timer injection**: `WithAfterFunc()` for deterministic batch tests in `internal/notify`
- **Integration tests**: `test/integration/` with `//go:build integration` tag — real SQLite + real HTTP server
- **E2E tests**: `test/e2e/` with `//go:build e2e` tag — real `vrclog-adapters` log fixtures through the full pipeline to `GET /api/v1/media/recent`

## API Routes

| Method | Path | Auth (LAN) | Description |
|--------|------|------|-------------|
| GET | /api/v1/health | No | Health check (status/database/ingest/adapter count only) |
| GET | /api/v1/observations | Yes | Generic Observation history, cursor pagination |
| GET | /api/v1/state | Yes | Current world, players, latest openable media |
| GET | /api/v1/media/recent | Yes | Recent media playback attempts |
| GET | /api/v1/adapters | Yes | Loaded adapters |
| GET | /api/v1/stream | Yes | Generic SSE stream (`event: observation`) |
| POST | /api/v1/auth/token | Yes (Basic Auth only) | Issue SSE token (5min TTL) |
| GET | /api/v1/config | Yes | Get config (secrets excluded) |
| PUT | /api/v1/config | Yes | Update config |
| GET | /api/v1/stats/basic | Yes | Today's statistics |

The old `/api/v1/events` and `/api/v1/now` endpoints do not exist — see `SPEC.md` and `CHANGELOG.md`.

## PR Rules

1. Keep PRs small
2. `go test ./...` must pass
3. `GOOS=windows go build ./...` must pass
4. Never log secrets (mask them)

## References

- `SPEC.md` - Full specification (Japanese)
- `CHANGELOG.md` - Breaking renewal history
