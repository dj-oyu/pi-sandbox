#!/bin/sh
exec node --input-type=module - "$@" <<'EOF'
const args = process.argv.slice(2);
const sub = args[0] || "";

if (!sub || sub === "--help" || sub === "-h") {
  console.log("Usage: idf.py <build|set-target <chip>|qemu|fullclean|--version>");
  process.exit(0);
}

if (sub === "--version") {
  console.log("ESP-IDF v6.0.1");
  process.exit(0);
}

let body;
if (sub === "build") {
  body = { action: "build", timeoutSec: 180 };
} else if (sub === "fullclean") {
  body = { action: "fullclean" };
} else if (sub === "set-target" && args[1]) {
  body = { action: "set-target", target: args[1] };
} else if (sub === "qemu") {
  body = { action: "qemu", timeoutSec: 15 };
} else {
  console.error(`idf.py '${args.join(" ")}' is not supported in this sandbox (supported: build, set-target, qemu, fullclean). Physical flashing must be done on the host.`);
  process.exit(1);
}

const proxyUrl = process.env.PI_PROXY_URL;
const token = process.env.STRATA_PROXY_TOKEN || "";
if (!proxyUrl) {
  console.error("PI_PROXY_URL is not set.");
  process.exit(1);
}

try {
  const res = await fetch(`${proxyUrl}/esp-idf/run`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
    },
    body: JSON.stringify(body),
  });
  const data = await res.json();
  if (!res.ok) {
    console.error(data.error || `HTTP ${res.status}`);
    process.exit(1);
  }
  if (data.stdout) process.stdout.write(data.stdout);
  if (data.stderr) process.stderr.write(data.stderr);
  if (data.timedOut) {
    console.error(`[idf.py ${sub}] stopped after timeout (${data.durationMs}ms)`);
  }
  process.exit(typeof data.exitCode === "number" ? data.exitCode : 0);
} catch (err) {
  console.error("idf.py proxy error:", err.message);
  process.exit(1);
}
EOF
