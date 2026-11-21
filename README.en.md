# MinCode

A desktop developer workspace built with [Tango](https://github.com/tangoui/tango): terminals, SSH/SFTP, databases, HTTP debugging, notes, an AI assistant and AI CLI sessions in one window.

This repository is the open source edition of MinCode, and a complete example of a mid-sized Tango app: a Go backend and a Vue frontend hosted in Tango's system WebView, packaged with `tan` and updated from GitHub Releases.

[中文](README.md)

## Features

| Module | What it does |
|---|---|
| Dashboard | Frequent connections, recent sessions and shortcuts |
| Terminal | Local terminals with tabs and splits; starts in your home folder, or pick another |
| SSH / SFTP | Hosts, keys and jump hosts, remote terminals and file transfer |
| Databases | MySQL, PostgreSQL, SQLite, SQL Server, Redis, MongoDB, Elasticsearch, Cassandra, Neo4j, InfluxDB, TDengine, Trino and more; SQL editor, result grid, schema compare, import/export, backups |
| HTTP | Requests, collections, environments, cURL import, code generation, WebSocket / SSE / Socket.IO |
| Notes | Markdown notes |
| Sessions | Browse local Claude Code, Codex and other AI CLI session history |
| AI assistant | Bring your own model (OpenAI-compatible, Anthropic, ...) or a local agent over ACP, with tools for terminals, databases and HTTP |
| Settings | Appearance, shortcuts, terminal, AI, data import/export, updates |

The commercial edition's project editor, Git, debugger, built-in browser, plugin system and cloud services are not part of this repository. DuckDB, Oracle, YashanDB, ClickHouse and Dameng (DM) drivers are not built in.

## Requirements

- macOS 12+, Windows 10+ (x64), Linux (x64)
- To build from source: Go (see `go.mod`), Node.js 26, pnpm 11 (see `packageManager` in `package.json`) and a C compiler (cgo is required)

## Development

```sh
pnpm install --frozen-lockfile
go install github.com/tangoui/tango/cmd/tan@latest   # ideally the tango version in go.mod
pnpm dev                                             # tan dev: desktop app with frontend hot reload
```

## Build and test

```sh
(cd vue-app && pnpm build)                           # writes frontend/dist, embedded by main.go
CGO_ENABLED=1 go build -tags production,nocoredrivers .
pnpm package:desktop                                 # tan package, installers in dist/

CGO_ENABLED=1 go test ./internal/... ./backend/...
(cd vue-app && pnpm lint && pnpm typecheck && pnpm test && pnpm test:components)
CGO_ENABLED=1 TANGO_LAUNCH_BACKGROUND=1 go run ./smoketest
CGO_ENABLED=1 TANGO_E2E=1 go test ./e2e -run E2E -v
```

E2E tests launch the app with their own name, ID and temporary data directory (`MINCODE_APP_NAME`, `MINCODE_APP_ID`, `MINCODE_DATA_DIR`), so they never touch your everyday data.

## Releases and updates

Pushing a `vX.Y.Z` tag makes GitHub Actions package four platforms, generate a signed update manifest with binary patches, and create a draft release; installed apps see the update once the draft is published. Updates use Tango's `service/updater`: the manifest signature and file digests are verified, a patch is preferred with fallback to the full package, and the new version is confirmed on launch or rolled back.

Before the first release, generate a key with `tan keygen` and store the private key as the repository secret `TANGO_UPDATE_KEY`. See [docs/release.md](docs/release.md).

Installers are not code-signed by default. If macOS blocks the first launch, choose "Open Anyway" in System Settings → Privacy & Security, or run `xattr -dr com.apple.quarantine /Applications/MinCode.app`.

## Layout

| Path | Contents |
|---|---|
| `main.go` | Desktop entry: window, tray, menus, single instance, update confirmation, backend startup |
| `internal/host/` | Host glue: Tango IPC handlers, backend streams, ordered events |
| `backend/` | Go commands and services by domain, independent of Tango |
| `vue-app/` | Vue 3 + TypeScript frontend |
| `frontend/dist/` | Frontend build output (not committed), embedded with `//go:embed` |
| `smoketest/`, `e2e/` | Real-window smoke and end-to-end tests (windows stay in the background) |
| `tools/e2e-sshd/` | Local SSH server used by E2E tests |
| `.github/workflows/` | CI, packaging and releases, weekly manifest re-signing |
| `tan.json` | Tango toolchain configuration |

## License

[AGPL-3.0](LICENSE).
