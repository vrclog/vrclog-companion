# VRClog Companion

A local resident application that passively reads VRChat's local log files, persists a canonical Observation stream to SQLite, and projects that stream into World/Presence/Media state — including recovering the original URL of media that failed to play in-world.

[日本語版はこちら](./README.ja.md)

## Architecture

VRClog Companion is one of three cooperating repositories:

```text
vrclog-go          canonical Event model, Record/Cursor, Engine, log Follow/ReadFile
  ← vrclog-adapters community-project Adapters (YamaPlayer, iwaSync3, ...)
    ← vrclog-companion (this repo)  Adapter composition, SQLite persistence,
                                     Projectors, HTTP API/SSE, Web UI, notifications
```

```text
VRChat output_log
        │
        ▼
vrclog-go Follow → Record
        │
        ▼
Engine (vrchat.core + community adapters)
        │
        ▼
Result { Observations, Diagnostics }
        │
        ▼
per-Record SQLite transaction (Store.CommitRecord)
  ├─ observations
  ├─ diagnostics
  └─ ingest cursor
        │
        ▼  (newly inserted Observations only)
Projector Manager
  ├─ WorldProjector
  ├─ PresenceProjector
  └─ MediaProjector
        │
        ├─ HTTP API / SSE
        ├─ Web UI
        └─ Discord notifications (World/Player only)
```

**Local-first, always.** No cloud upload, no telemetry. Everything lives in a SQLite database on the user's own machine.

## Why this exists: media URL recovery

VRChat's built-in video players (YamaPlayer, iwaSync3, and others) sometimes fail to play a video for a single user while it plays fine for everyone else. When that happens, VRChat's log still records the original URL — this app extracts and correlates it so it can be copied or opened in a browser, without guessing or fetching external metadata.

## Verified adapters

| Adapter | Origin | Status |
|---------|--------|--------|
| `vrchat.core` | vrclog-go (built-in) | Verified |
| `community.yamaplayer` | vrclog-adapters | Verified against real log fixtures |
| `community.iwasync3` | vrclog-adapters | Verified against real log fixtures |

Adapters are composed at compile time (`internal/adapter`). There is no runtime plugin loading, no YAML pattern configuration, and no remote adapter catalog.

## Requirements

- Go 1.25+
- Node.js 20+ (for Web UI build)
- Windows 11 (target OS; macOS supported for development)

## Directory Structure

```text
vrclog-companion/
├── cmd/
│   └── vrclog-companion/  # Main entry point
├── internal/
│   ├── adapter/           # Compile-time Adapter composition
│   ├── api/                # HTTP API server (JSON + SSE + auth + rate limiting)
│   ├── app/                # Use case layer
│   ├── config/              # Configuration/secrets management
│   ├── ingest/              # RecordSource, per-Record ingest Runner
│   ├── notify/              # Discord notifications (Change-based)
│   ├── observation/         # Observation persistence DTO
│   ├── projector/           # World/Presence/Media projected state
│   ├── sse/                 # Generic Observation SSE broadcaster
│   └── store/                # SQLite persistence (schema v3)
├── web/                    # Web UI (React + Vite)
├── webembed/                # Embedded Web UI (go:embed)
├── test/
│   ├── integration/          # HTTP API integration tests
│   └── e2e/                   # Real-fixture media URL recovery E2E tests
├── go.mod
├── SPEC.md                  # Specification
├── LICENSE                   # MIT License
└── README.md
```

## Build & Run

### Build

```bash
# Build Web UI
cd web && npm install && npm run build && cd ..
mkdir -p webembed/dist && cp -r web/dist/* webembed/dist/

# Cross-compile for Windows (from macOS/Linux)
GOOS=windows GOARCH=amd64 go build -o vrclog.exe ./cmd/vrclog-companion

# Build for local environment
go build -o vrclog ./cmd/vrclog-companion
```

### Run

```bash
# Default (port 8080)
./vrclog

# Specify port
./vrclog -port 9000
```

### Verify

```bash
curl http://127.0.0.1:8080/api/v1/health
# {"status":"ok","database":"ok","ingest":"running","last_ingest_error":"","last_record_at":"...","loaded_adapters":3}

# Web UI
# Open http://127.0.0.1:8080 in your browser
```

## API Endpoints

| Method | Path | Auth (LAN mode) | Description |
|--------|------|------------------|-------------|
| GET | /api/v1/health | No | Health check (status/ingest/adapter count only) |
| GET | /api/v1/observations | Yes | Generic Observation history, cursor pagination |
| GET | /api/v1/state | Yes | Current world, players, latest openable media |
| GET | /api/v1/media/recent | Yes | Recent media playback attempts |
| GET | /api/v1/adapters | Yes | Loaded adapters |
| GET | /api/v1/stream | Yes | Generic SSE stream (`event: observation`) |
| POST | /api/v1/auth/token | Yes (Basic Auth only) | Issue SSE token (5min TTL) |
| GET | /api/v1/config | Yes | Get config (secrets excluded) |
| PUT | /api/v1/config | Yes | Update config |
| GET | /api/v1/stats/basic | Yes | Today's statistics |

This is a breaking renewal: the old flat `Event` model, `/api/v1/events`, `/api/v1/now`, and the old SQLite schema no longer exist. See [SPEC.md](./SPEC.md) for the full contract and [CHANGELOG.md](./CHANGELOG.md) for what changed.

## Database schema reset

The SQLite schema is versioned via `PRAGMA user_version` (currently version 3). There is **no automatic migration** from any prior schema. If the app refuses to start because of a schema mismatch, stop the app and rename or delete the database file (`vrclog.sqlite` in the app's data directory) to start fresh — history will be lost, but no data corruption can occur.

## Testing

```bash
go test ./...
go test -tags=integration ./test/integration/...
go test -tags=e2e ./test/e2e/...
```

## CI

GitHub Actions runs tests automatically on Windows and Linux runners.

- Triggered on `push` / `pull_request`
- Build verification on Windows

## Security & Privacy

### Local mode (default)

Binds to `127.0.0.1` only. No authentication is required or offered, since nothing outside the machine can reach it.

### LAN Mode

Setting `lan_enabled=true` in `config.json` allows access from other devices on your local network.

- **Basic Auth is required**: automatically enabled when LAN mode is on
- **Auto-generated password on first run**: if credentials are not configured, a strong random password is generated and saved to `generated_password.txt` in the data directory
- **Rate limiting and auth-failure lockout** are enabled automatically

> **Warning**: Basic Auth provides no protection against eavesdropping without TLS. Only use LAN mode on trusted local networks, and consider a TLS-terminating reverse proxy if you need it over an untrusted network. Port forwarding to the public internet is **not supported**.

### Media URLs are sensitive

Media URLs recovered from VRChat logs may embed session tokens, private instance identifiers, or otherwise-private content links. This app:

- never sends media URLs to Discord,
- never fetches external metadata (no oEmbed, no thumbnail/title fetch),
- never opens a URL automatically — only an explicit user click can copy or open one,
- only accepts `http`/`https` schemes for the browser-open action.

### No VRChat process or API interaction

This app only reads VRChat's local log files. It does not attach to the VRChat process, does not call any VRChat API, and does not modify VRChat in any way.

## License

MIT License - See [LICENSE](./LICENSE) for details.
