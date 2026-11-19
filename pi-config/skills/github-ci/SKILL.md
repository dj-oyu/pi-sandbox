---
name: github-ci
description: Poll GitHub Actions CI/CD workflow runs and inspect failed job logs in real time via the host read-only /gh-ci proxy (no GitHub token inside container).
---

# GitHub Actions CI/CD Real-Time Monitoring

The host proxy (`$PI_PROXY_URL`) exposes a **read-only** `/gh-ci` gateway backed by the host's `gh` CLI. No GitHub token is needed inside the container; authenticate only with `$STRATA_PROXY_TOKEN`.

Allowed read-only endpoints (`GET` only):
- `${PI_PROXY_URL}/gh-ci/repos/<owner>/<repo>/actions/runs?head_sha=<sha>&per_page=5`
- `${PI_PROXY_URL}/gh-ci/repos/<owner>/<repo>/actions/runs/<runId>/jobs`
- `${PI_PROXY_URL}/gh-ci/repos/<owner>/<repo>/actions/jobs/<jobId>/logs`

## 1. Detect owner/repo & current commit SHA
```bash
git remote get-url origin
git rev-parse HEAD
```

## 2. Check latest workflow runs for the current commit
```bash
node -e '
const [owner, repo, sha] = process.argv.slice(1);
fetch(`${process.env.PI_PROXY_URL}/gh-ci/repos/${owner}/${repo}/actions/runs?head_sha=${sha}&per_page=5`, {
  headers: { Authorization: `Bearer ${process.env.STRATA_PROXY_TOKEN}` }
}).then(r => r.json()).then(d => {
  for (const run of d.workflow_runs || []) {
    console.log(run.id, run.name, run.status, run.conclusion, run.html_url);
  }
});
' <owner> <repo> <sha>
```

## 3. Inspect jobs & failed job logs
```bash
node -e '
const [owner, repo, runId] = process.argv.slice(1);
fetch(`${process.env.PI_PROXY_URL}/gh-ci/repos/${owner}/${repo}/actions/runs/${runId}/jobs`, {
  headers: { Authorization: `Bearer ${process.env.STRATA_PROXY_TOKEN}` }
}).then(r => r.json()).then(d => {
  for (const j of d.jobs || []) {
    console.log(j.id, j.name, j.status, j.conclusion);
    for (const s of (j.steps || []).filter(x => x.conclusion === "failure")) {
      console.log("  FAILED STEP:", s.number, s.name);
    }
  }
});
' <owner> <repo> <runId>
```

For failing job logs:
```bash
node -e '
const [owner, repo, jobId] = process.argv.slice(1);
fetch(`${process.env.PI_PROXY_URL}/gh-ci/repos/${owner}/${repo}/actions/jobs/${jobId}/logs`, {
  headers: { Authorization: `Bearer ${process.env.STRATA_PROXY_TOKEN}` }
}).then(r => r.text()).then(t => console.log(t.slice(-8000)));
' <owner> <repo> <jobId>
```

## 4. Polling guideline
- Wait 15–30 seconds between polls while `status` is `queued` or `in_progress`.
- Once `status === "completed"` and `conclusion === "failure"`, fetch the failing job log and diagnose the root cause.
