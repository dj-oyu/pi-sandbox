---
name: dev-toolchains
description: Idioms and objective verification commands for npm (Node.js), cargo (Rust), go (Go), and uv (Python) projects.
---

# Development Toolchains & Verification

## Node.js / TypeScript (`package.json`)
- Install deps: `npm ci` (prefer lockfile) or `npm install`.
- Verify: `npm run typecheck`, `npm run lint`, `npm test`, `npm run build` (or `verify_metrics`).

## Rust (`Cargo.toml`)
- Fast check: `cargo check --all-targets`
- Lint & test: `cargo clippy --all-targets -- -D warnings` and `cargo test`.

## Go (`go.mod`)
- Static check & test: `go vet ./...` and `go test -race ./...` (or `go test ./...`).
- Format check: `gofmt -l .`.

## Python (`pyproject.toml` / scripts)
- Always run Python via `uv`: `uv sync`, `uv run pytest`, `uv run python script.py`.
- Do not assume global `python` or `pip` exists.
