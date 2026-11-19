---
name: git-workflow
description: Safe Git and GitHub workflow inside the sandbox (commit hygiene, diffs, and token-aware GitHub API usage).
---

# Git & GitHub Workflow

- Before editing, check `git status` and `git log -n 5 --oneline`.
- Keep changes focused; inspect `git diff` before committing.
- Never use `git push --force` (blocked by safety-guard).
- **Pushing / PRs**:
  - If `GITHUB_TOKEN` / `GH_TOKEN` is not set in the environment, commit cleanly in `/workspace` and let the user push from the Windows host.
  - If `GITHUB_TOKEN` is set, HTTPS `git push` works through the allowlist proxy (`github.com`), and GitHub REST API calls can be made via `node -e "fetch('https://api.github.com/...')"`.
