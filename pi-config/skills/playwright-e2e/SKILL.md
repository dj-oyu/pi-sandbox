---
name: playwright-e2e
description: Run lightweight headless Chromium E2E checks against local dev servers (127.0.0.1) inside pi-sandbox-web.
---

# Lightweight Playwright E2E Verification

In `pi-sandbox-web`, `playwright` (with Chromium only) is preinstalled globally (`NODE_PATH=/usr/local/node/lib/node_modules`).

## Rules
- Target only local servers (`http://127.0.0.1:<port>`). External browsing is blocked by the host allowlist proxy.
- Always launch headless (`{ headless: true }`) and close `browser` in a `finally` block so no Chromium processes linger.
- Capture `page.on("console")` (errors) and `page.on("pageerror")` to catch runtime failures objectively.
- Prefer DOM/text/status assertions over screenshots; only take screenshots when visual layout verification is explicitly needed (image reading on CPU takes ~10+ seconds).

## Quick Verification Snippet
```bash
node -e '
const { chromium } = require("playwright");
(async () => {
  const browser = await chromium.launch({ headless: true });
  const errors = [];
  try {
    const page = await browser.newPage();
    page.on("pageerror", e => errors.push("pageerror: " + e.message));
    page.on("console", m => { if (m.type() === "error") errors.push("console: " + m.text()); });
    await page.goto("http://127.0.0.1:3000", { waitUntil: "networkidle", timeout: 15000 });
    const title = await page.title();
    console.log("TITLE:", title, "ERRORS:", errors.length);
    if (errors.length) { console.error(errors.join("\n")); process.exit(1); }
  } finally {
    await browser.close();
  }
})();
'
```
