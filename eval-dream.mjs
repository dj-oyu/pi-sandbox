#!/usr/bin/env node
// Post-task Dream Harness Evaluator for pi-sandbox.
// Usage: node eval-dream.mjs [--run <runId>] [--workspace <path>] [--json]
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const SESSIONS_DIR = path.join(__dirname, "logs", "sessions");
const DREAM_LOG = path.join(SESSIONS_DIR, "dream-metrics.jsonl");
const VERIFY_LOG = path.join(SESSIONS_DIR, "verify-metrics.jsonl");
const EVAL_OUT = path.join(SESSIONS_DIR, "dream-evals.jsonl");

function readJsonl(filePath) {
  if (!fs.existsSync(filePath)) return [];
  return fs
    .readFileSync(filePath, "utf8")
    .split(/\r?\n/)
    .filter(Boolean)
    .map((line) => {
      try {
        return JSON.parse(line);
      } catch {
        return null;
      }
    })
    .filter(Boolean);
}

function inspectMemoryFile(workspaceDir, relPath = ".pi/memory/dream.md") {
  if (!workspaceDir) return null;
  const full = path.join(workspaceDir, relPath);
  if (!fs.existsSync(full)) return { exists: false, bytes: 0, totalEntries: 0, tiers: {}, bloatWarning: false };
  const text = fs.readFileSync(full, "utf8");
  const bytes = Buffer.byteLength(text, "utf8");
  const tiers = {};
  let totalEntries = 0;
  for (const line of text.split(/\r?\n/)) {
    const m = line.match(/^###\s+\[(Principle|Insight|Dead End|Note|Epic|Nobel Prize|Genius|Insightful|Useful)\]/i);
    if (m) {
      const k = m[1];
      tiers[k] = (tiers[k] || 0) + 1;
      totalEntries++;
    }
  }
  const bloatWarning = bytes > 12 * 1024 || totalEntries > 25;
  return { exists: true, path: full, bytes, totalEntries, tiers, bloatWarning };
}

function parseArgs(argv) {
  let runId = null;
  let workspace = null;
  let asJson = false;
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === "--run" && argv[i + 1]) runId = argv[++i];
    else if (argv[i] === "--workspace" && argv[i + 1]) workspace = argv[++i];
    else if (argv[i] === "--json") asJson = true;
    else if (!runId && !argv[i].startsWith("-")) runId = argv[i];
  }
  return { runId, workspace, asJson };
}

function evaluateRun(runId, events, verifies, workspaceDir) {
  const startEv = events.find((e) => e.event === "session_start") || {};
  const finalEvs = events.filter((e) => e.event === "session_turn_final");
  const lastFinal = finalEvs[finalEvs.length - 1] || {};
  const writes = events.filter((e) => e.event === "dream_write");
  const recalls = events.filter((e) => e.event === "dream_recall");
  const followThroughs = events.filter((e) => e.event === "recall_followthrough");
  const guardSkips = events.filter((e) => e.event === "guard_skip");

  const maxTurn = Math.max(
    lastFinal.turn || 0,
    ...events.map((e) => (typeof e.turn === "number" ? e.turn : 0)),
    1
  );
  const totalInjections = writes.length + recalls.length;
  const overheadRatio = Number((totalInjections / maxTurn).toFixed(3));

  // Causal attribution: correlate each recall with the next <=3 verify-metrics records within 10 min
  const recallImpacts = recalls.map((rec) => {
    const t0 = Date.parse(rec.ts);
    const nextVerifies = verifies
      .filter((v) => {
        const tv = Date.parse(v.ts);
        return tv > t0 && tv - t0 <= 10 * 60 * 1000;
      })
      .slice(0, 3);
    const before = typeof rec.scoreBefore === "number" ? rec.scoreBefore : null;
    const bestAfter =
      nextVerifies.length > 0
        ? Math.max(...nextVerifies.map((v) => (typeof v.score === "number" ? v.score : 0)))
        : null;
    const delta = before !== null && bestAfter !== null ? bestAfter - before : null;
    return {
      turn: rec.turn,
      pickedIds: rec.pickedIds || [],
      scoreBefore: before,
      bestScoreAfter: bestAfter,
      scoreDelta: delta,
    };
  });

  const positiveLifts = recallImpacts.filter((r) => typeof r.scoreDelta === "number" && r.scoreDelta > 0);
  const scoredImpacts = recallImpacts.filter((r) => typeof r.scoreDelta === "number");
  const avgScoreLift =
    scoredImpacts.length > 0
      ? Number((scoredImpacts.reduce((a, b) => a + b.scoreDelta, 0) / scoredImpacts.length).toFixed(2))
      : null;

  const memoryHealth = inspectMemoryFile(workspaceDir, startEv.memoryFile || ".pi/memory/dream.md");
  const targetMet = lastFinal.targetMet === true;
  const finalScore = typeof lastFinal.latestScore === "number" ? lastFinal.latestScore : null;

  let verdict = "NEUTRAL";
  const reasons = [];
  if (positiveLifts.length > 0) {
    verdict = "HELPFUL";
    reasons.push(`${positiveLifts.length}/${recalls.length} recalls preceded a verify score jump (avg delta ${avgScoreLift})`);
  } else if (followThroughs.length > 0 && targetMet && overheadRatio <= 0.25) {
    verdict = "HELPFUL";
    reasons.push(`${followThroughs.length} recall(s) led to immediate code edits & target met with low overhead (${(overheadRatio * 100).toFixed(1)}%)`);
  }
  if (overheadRatio > 0.3 || memoryHealth?.bloatWarning || (recalls.length >= 3 && followThroughs.length === 0 && positiveLifts.length === 0)) {
    verdict = "NOISY";
    if (overheadRatio > 0.3) reasons.push(`high injection overhead (${(overheadRatio * 100).toFixed(1)}%)`);
    if (memoryHealth?.bloatWarning) reasons.push(`memory file bloated (${memoryHealth.bytes} B / ${memoryHealth.totalEntries} entries) — run consolidation`);
    if (recalls.length >= 3 && followThroughs.length === 0) reasons.push("recalls did not trigger follow-through edits");
  }
  if (reasons.length === 0) {
    reasons.push(totalInjections === 0 ? "short run (0 Dream checkpoints fired)" : "steady progress, no measurable lift or regression");
  }

  return {
    ts: new Date().toISOString(),
    runId,
    dreamEnabled: Boolean(startEv.dreamEnabled),
    totalTurns: maxTurn,
    writes: writes.length,
    recalls: recalls.length,
    followThroughs: followThroughs.length,
    guardSkips: guardSkips.length,
    overheadRatio,
    finalScore,
    targetMet,
    positiveLiftRecalls: positiveLifts.length,
    avgScoreLift,
    recallImpacts,
    memoryHealth,
    verdict,
    reasons,
  };
}

function summarizeAB(allDreamEvents) {
  const byRun = new Map();
  for (const ev of allDreamEvents) {
    if (!ev.runId) continue;
    if (!byRun.has(ev.runId)) byRun.set(ev.runId, []);
    byRun.get(ev.runId).push(ev);
  }
  const buckets = { on: [], off: [] };
  for (const [rid, evs] of byRun.entries()) {
    const s = evs.find((e) => e.event === "session_start");
    if (!s) continue;
    const finals = evs.filter((e) => e.event === "session_turn_final");
    const last = finals[finals.length - 1] || {};
    const turns = Math.max(last.turn || 0, ...evs.map((e) => (typeof e.turn === "number" ? e.turn : 0)));
    buckets[s.dreamEnabled ? "on" : "off"].push({
      runId: rid,
      turns,
      targetMet: last.targetMet === true,
      score: typeof last.latestScore === "number" ? last.latestScore : null,
    });
  }
  const agg = (arr) => {
    const met = arr.filter((x) => x.targetMet).length;
    const avgTurns = arr.length ? Number((arr.reduce((a, b) => a + b.turns, 0) / arr.length).toFixed(1)) : 0;
    const scored = arr.filter((x) => x.score !== null);
    const avgScore = scored.length ? Number((scored.reduce((a, b) => a + b.score, 0) / scored.length).toFixed(1)) : null;
    return { runs: arr.length, targetMetRate: arr.length ? `${Math.round((met / arr.length) * 100)}%` : "-", avgTurns, avgScore };
  };
  return { dreamOn: agg(buckets.on), dreamOff: agg(buckets.off) };
}

function main() {
  const { runId: reqRunId, workspace, asJson } = parseArgs(process.argv.slice(2));
  const dreamEvents = readJsonl(DREAM_LOG);
  const verifies = readJsonl(VERIFY_LOG);

  if (dreamEvents.length === 0) {
    console.log("[dream-eval] No dream-metrics.jsonl records yet.");
    process.exit(0);
  }

  let targetRunId = reqRunId;
  if (!targetRunId) {
    const starts = dreamEvents.filter((e) => e.event === "session_start" && e.runId);
    targetRunId = starts.length > 0 ? starts[starts.length - 1].runId : dreamEvents[dreamEvents.length - 1].runId;
  }

  const runEvents = dreamEvents.filter((e) => e.runId === targetRunId);
  const report = evaluateRun(targetRunId, runEvents, verifies, workspace);
  const ab = summarizeAB(dreamEvents);

  try {
    fs.mkdirSync(SESSIONS_DIR, { recursive: true });
    fs.appendFileSync(EVAL_OUT, JSON.stringify(report) + "\n", "utf8");
  } catch {
    // non-fatal
  }

  if (asJson) {
    console.log(JSON.stringify({ report, ab }, null, 2));
    return;
  }

  console.log("\n=== Dream Harness Post-Task Evaluation ===");
  console.log(`Run ID:        ${report.runId} (dream=${report.dreamEnabled ? "ON" : "OFF"})`);
  console.log(`Verdict:       ${report.verdict} — ${report.reasons.join("; ")}`);
  console.log(`Activity:      turns=${report.totalTurns}, writes=${report.writes}, recalls=${report.recalls}, followThrough=${report.followThroughs}, overhead=${(report.overheadRatio * 100).toFixed(1)}%`);
  console.log(`Outcome:       finalScore=${report.finalScore ?? "-"}, targetMet=${report.targetMet}, postRecallScoreLifts=${report.positiveLiftRecalls}/${report.recalls} (avgDelta=${report.avgScoreLift ?? "-"})`);
  if (report.memoryHealth) {
    const mh = report.memoryHealth;
    console.log(`Memory Health: entries=${mh.totalEntries}, bytes=${mh.bytes}, tiers=${JSON.stringify(mh.tiers)}${mh.bloatWarning ? " [BLOAT WARNING]" : ""}`);
  }
  console.log(`Historical A/B: ON(runs=${ab.dreamOn.runs}, met=${ab.dreamOn.targetMetRate}, avgTurns=${ab.dreamOn.avgTurns}) vs OFF(runs=${ab.dreamOff.runs}, met=${ab.dreamOff.targetMetRate}, avgTurns=${ab.dreamOff.avgTurns})`);
  console.log("==========================================\n");
}

main();
