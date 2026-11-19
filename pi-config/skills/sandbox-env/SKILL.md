---
name: sandbox-env
description: Container environment rules, available tools, /refs read-only docs, allowlist proxy, and localhost web server verification inside pi-sandbox.
---

# pi-sandbox Environment Guide

## Available vs. Absent Tools
- **Available**: `node` (v22), `npm`, `cargo`, `rustc`, `go`, `uv`, `gcc`, `make`, `git`, `rg` (ripgrep), `sed`, `awk`, `find`, `xargs`.
- **Absent by design**: `apt`/`dpkg`, `curl`, `wget`, `jq`, `sudo`, global `python`.
- **Workarounds**:
  - Python: use `uv run python ...` or `uv run pytest`.
  - HTTP checks: use `node -e "fetch('http://127.0.0.1:3000/').then(r=>r.text()).then(console.log)"`.
  - JSON parsing: use `node -e "const d=JSON.parse(require('fs').readFileSync(0,'utf8')); ..."`.

## Directories & Network
- `/workspace`: read-write project directory (shared with Windows host).
- `/refs`: read-only reference specs, docs, and images mounted from host. Check `/refs` when looking for external specifications.
- Outbound internet is restricted by the host allowlist proxy (package registries, GitHub, Espressif/M5Stack docs). Other domains return `403`.

## Web Server Verification
- Exposed host-to-container ports: `3000`, `5173`, `8000` (bound on host `127.0.0.1` only).
- Bind dev servers to `0.0.0.0` inside the container (e.g. `--host 0.0.0.0 --port 3000`) so the user can open `http://127.0.0.1:3000` on Windows.
- Verify endpoints from inside the container with `node -e "fetch(...)"` and stop background test servers before finishing if not meant to stay running.
