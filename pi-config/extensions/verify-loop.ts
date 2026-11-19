import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import fs from "node:fs";
import path from "node:path";
import { spawn } from "node:child_process";

interface VerifierCheckSpec {
  name: string;
  command: string;
  weight: number;
}

interface VerifierConfig {
  targetScore: number;
  checks: VerifierCheckSpec[];
}

interface CheckResult {
  name: string;
  command: string;
  weight: number;
  exitCode: number;
  passed: boolean;
  durationMs: number;
  outputTail: string;
}

interface RunSummary {
  ts: string;
  cwd: string;
  score: number;
  targetScore: number;
  targetMet: boolean;
  passedCount: number;
  totalCount: number;
  totalDurationMs: number;
  results: CheckResult[];
}

function loadProjectVerifierConfig(cwd: string): VerifierConfig | null {
  const cfgPath = path.join(cwd, ".pi", "verifier.json");
  if (!fs.existsSync(cfgPath)) return null;
  try {
    const raw = JSON.parse(fs.readFileSync(cfgPath, "utf8"));
    if (!Array.isArray(raw.checks) || raw.checks.length === 0) return null;
    const checks: VerifierCheckSpec[] = raw.checks
      .filter((c: unknown): c is { name?: string; command: string; weight?: number } =>
        Boolean(c && typeof (c as { command?: unknown }).command === "string"),
      )
      .map((c, idx) => ({
        name: c.name || `check-${idx + 1}`,
        command: c.command,
        weight: typeof c.weight === "number" && c.weight > 0 ? c.weight : 1,
      }));
    if (checks.length === 0) return null;
    const targetScore = typeof raw.targetScore === "number" ? raw.targetScore : 100;
    return { targetScore, checks };
  } catch {
    return null;
  }
}

function detectVerifierChecks(cwd: string): VerifierConfig {
  const custom = loadProjectVerifierConfig(cwd);
  if (custom) return custom;

  const rawCmds: Array<{ name: string; command: string }> = [];

  const pkgPath = path.join(cwd, "package.json");
  if (fs.existsSync(pkgPath)) {
    try {
      const pkg = JSON.parse(fs.readFileSync(pkgPath, "utf8"));
      const scripts = pkg.scripts || {};
      for (const name of ["typecheck", "lint", "test", "build"]) {
        if (typeof scripts[name] === "string") {
          rawCmds.push({ name, command: `npm run ${name}` });
        }
      }
    } catch {
      // ignore invalid package.json
    }
  }

  if (fs.existsSync(path.join(cwd, "Cargo.toml"))) {
    rawCmds.push({ name: "cargo-check", command: "cargo check --all-targets" });
    rawCmds.push({ name: "cargo-test", command: "cargo test" });
  }

  if (fs.existsSync(path.join(cwd, "go.mod"))) {
    rawCmds.push({ name: "go-vet", command: "go vet ./..." });
    rawCmds.push({ name: "go-test", command: "go test ./..." });
  }

  if (fs.existsSync(path.join(cwd, "pyproject.toml"))) {
    rawCmds.push({ name: "pytest", command: "uv run pytest" });
  }

  const cmakePath = path.join(cwd, "CMakeLists.txt");
  if (fs.existsSync(cmakePath)) {
    try {
      const cmakeText = fs.readFileSync(cmakePath, "utf8");
      if (cmakeText.includes("idf.cmake") || fs.existsSync(path.join(cwd, "sdkconfig"))) {
        rawCmds.push({ name: "idf-build", command: "idf.py build" });
      }
    } catch {
      // ignore
    }
  }

  const weight = rawCmds.length > 0 ? 100 / rawCmds.length : 100;
  return {
    targetScore: 100,
    checks: rawCmds.map((c) => ({ ...c, weight })),
  };
}

const CHECK_TIMEOUT_MS = 90_000;

function runShellCommand(spec: VerifierCheckSpec, cwd: string, signal?: AbortSignal): Promise<CheckResult> {
  const startedAt = Date.now();
  return new Promise((resolve) => {
    const child = spawn("bash", ["-c", spec.command], { cwd, env: process.env, detached: true });
    let combined = "";
    let timedOut = false;

    const append = (chunk: Buffer) => {
      combined += chunk.toString("utf8");
      if (combined.length > 16000) {
        combined = combined.slice(-16000);
      }
    };

    child.stdout.on("data", append);
    child.stderr.on("data", append);

    const killGroup = () => {
      try {
        if (child.pid) process.kill(-child.pid, "SIGKILL");
      } catch {
        child.kill("SIGKILL");
      }
    };

    const timer = setTimeout(() => {
      timedOut = true;
      killGroup();
    }, CHECK_TIMEOUT_MS);

    const onAbort = () => killGroup();
    if (signal) signal.addEventListener("abort", onAbort, { once: true });

    child.on("close", (code) => {
      clearTimeout(timer);
      if (signal) signal.removeEventListener("abort", onAbort);
      const exitCode = timedOut ? 124 : (code ?? 1);
      const lines = combined.trim().split(/\r?\n/);
      let outputTail = lines.slice(-40).join("\n");
      if (timedOut) {
        outputTail = `[TIMEOUT after ${CHECK_TIMEOUT_MS / 1000}s]\n${outputTail}`;
      }
      resolve({
        name: spec.name,
        command: spec.command,
        weight: spec.weight,
        exitCode,
        passed: exitCode === 0,
        durationMs: Date.now() - startedAt,
        outputTail,
      });
    });

    child.on("error", (err) => {
      clearTimeout(timer);
      if (signal) signal.removeEventListener("abort", onAbort);
      resolve({
        name: spec.name,
        command: spec.command,
        weight: spec.weight,
        exitCode: 1,
        passed: false,
        durationMs: Date.now() - startedAt,
        outputTail: err.message,
      });
    });
  });
}

function appendMetricLog(summary: RunSummary) {
  const logPath = "/home/agent/.pi/agent/sessions/verify-metrics.jsonl";
  try {
    fs.mkdirSync(path.dirname(logPath), { recursive: true });
    fs.appendFileSync(logPath, JSON.stringify(summary) + "\n", "utf8");
  } catch {
    // non-fatal
  }
}

export default function (pi: ExtensionAPI) {
  let baseline: RunSummary | null = null;
  let previous: RunSummary | null = null;
  let autoContinueCount = 0;
  const MAX_AUTO_CONTINUE = 3;

  async function executeVerification(cwd: string, customCommands?: string[], signal?: AbortSignal) {
    const config: VerifierConfig =
      customCommands && customCommands.length > 0
        ? {
            targetScore: 100,
            checks: customCommands.map((cmd, idx) => ({
              name: `custom-${idx + 1}`,
              command: cmd,
              weight: 100 / customCommands.length,
            })),
          }
        : detectVerifierChecks(cwd);

    if (config.checks.length === 0) {
      return {
        summary: null as RunSummary | null,
        text: `No verifier commands detected in ${cwd}. Define .pi/verifier.json or pass explicit commands.`,
      };
    }

    const results: CheckResult[] = [];
    for (const check of config.checks) {
      results.push(await runShellCommand(check, cwd, signal));
    }

    const totalWeight = results.reduce((acc, r) => acc + r.weight, 0) || 1;
    const earnedWeight = results.reduce((acc, r) => acc + (r.passed ? r.weight : 0), 0);
    const score = Math.round((earnedWeight / totalWeight) * 100);
    const passedCount = results.filter((r) => r.passed).length;
    const totalCount = results.length;
    const totalDurationMs = results.reduce((acc, r) => acc + r.durationMs, 0);
    const targetMet = score >= config.targetScore;

    const summary: RunSummary = {
      ts: new Date().toISOString(),
      cwd,
      score,
      targetScore: config.targetScore,
      targetMet,
      passedCount,
      totalCount,
      totalDurationMs,
      results,
    };

    if (!baseline) baseline = summary;
    const prev = previous;
    previous = summary;

    appendMetricLog(summary);

    const deltaPrev = prev ? score - prev.score : 0;
    const deltaBase = baseline ? score - baseline.score : 0;
    const deltaStr = prev
      ? ` (vs prev: ${deltaPrev >= 0 ? "+" : ""}${deltaPrev}, vs baseline: ${deltaBase >= 0 ? "+" : ""}${deltaBase})`
      : " (baseline recorded)";

    const lines = [
      `Verification Score: ${score}/100 (target: ${config.targetScore}, ${targetMet ? "TARGET MET" : "BELOW TARGET"}) — ${passedCount}/${totalCount} passed in ${totalDurationMs}ms${deltaStr}`,
      "",
      ...results.map((r) => {
        const tag = r.passed ? "PASS" : `FAIL(exit=${r.exitCode})`;
        const detail = r.passed ? "" : `\n  --- output tail ---\n${r.outputTail.split("\n").map((l) => "  " + l).join("\n")}`;
        return `- [${tag}] **${r.name}** \`${r.command}\` (weight=${Math.round(r.weight)}, ${r.durationMs}ms)${detail}`;
      }),
    ];

    return { summary, text: lines.join("\n") };
  }

  pi.registerTool({
    name: "verify_metrics",
    label: "Verify Metrics",
    description:
      "Run objective project verifiers (.pi/verifier.json or auto-detected checks), compute weighted score vs targetScore, compare with baseline/previous runs, and log results.",
    parameters: Type.Object({
      commands: Type.Optional(Type.Array(Type.String(), { description: "Optional explicit verifier commands to run instead of .pi/verifier.json / auto-detection." })),
      cwd: Type.Optional(Type.String({ description: "Working directory (defaults to /workspace)." })),
    }),
    async execute(_toolCallId, params, signal, _onUpdate, ctx) {
      const targetCwd = params.cwd || ctx.cwd || "/workspace";
      const { summary, text } = await executeVerification(targetCwd, params.commands, signal);
      return {
        content: [{ type: "text", text }],
        details: summary || {},
      };
    },
  });

  pi.registerCommand("verify", {
    description: "Run objective project verifiers and show score delta",
    handler: async (args, ctx) => {
      const cmds = args.trim() ? [args.trim()] : undefined;
      const { summary, text } = await executeVerification(ctx.cwd || "/workspace", cmds);
      const level = summary && summary.targetMet ? "info" : "warning";
      ctx.ui.notify(text.split("\n")[0], level);
    },
  });

  pi.on("agent_end", async (_event, ctx) => {
    if (previous && !previous.targetMet && autoContinueCount < MAX_AUTO_CONTINUE) {
      autoContinueCount += 1;
      pi.sendUserMessage(
        `[HARNESS AUTO-CONTINUE ${autoContinueCount}/${MAX_AUTO_CONTINUE}] targetScore (${previous.targetScore}) is not met yet (current: ${previous.score}/${previous.targetScore}). Continue using tools to fix remaining issues, run verify_metrics until TARGET MET, and finish all deliverables.`,
        { deliverAs: "followUp" },
      );
      return;
    }

    const cwd = ctx.cwd || "/workspace";
    const donePath = path.join(cwd, ".pi", "DONE.json");
    try {
      fs.mkdirSync(path.dirname(donePath), { recursive: true });
      const payload = previous
        ? {
            ts: new Date().toISOString(),
            score: previous.score,
            targetScore: previous.targetScore,
            targetMet: previous.targetMet,
            passedCount: previous.passedCount,
            totalCount: previous.totalCount,
          }
        : {
            ts: new Date().toISOString(),
            status: "completed_without_verify",
          };
      fs.writeFileSync(donePath, JSON.stringify(payload, null, 2) + "\n", "utf8");
    } catch {
      // non-fatal
    }
  });
}
