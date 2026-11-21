#!/usr/bin/env node
// Post-task Dream Harness Evaluator (Optional Verifier + Local Qwen 1-shot Judge) for pi-sandbox.
// Usage: node eval-dream.mjs [--run <runId>] [--workspace <path>] [--no-judge] [--strata <url>] [--json]
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
  if (!fs.existsSync(full)) {
    return { exists: false, path: relPath, bytes: 0, totalEntries: 0, tiers: {}, entries: [], bloatWarning: false };
  }
  const text = fs.readFileSync(full, "utf8");
  const bytes = Buffer.byteLength(text, "utf8");
  const tiers = {};
  const entries = [];
  const lines = text.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const m = lines[i].match(/^###\s+\[(Principle|Insight|Dead End|Note|Epic|Nobel Prize|Genius|Insightful|Useful)\]\s*(?:([A-Za-z0-9_-]+)\s*[-—:]\s*)?(.+)$/i);
    if (m) {
      const tier = m[1];
      const id = m[2] || `M${entries.length + 1}`;
      const title = m[3].trim();
      const bodyLines = [];
      for (let j = i + 1; j < lines.length && bodyLines.length < 3; j++) {
        if (lines[j].startsWith("### ") || lines[j].startsWith("## ")) break;
        const t = lines[j].trim();
        if (t) bodyLines.push(t);
      }
      tiers[tier] = (tiers[tier] || 0) + 1;
      entries.push({ id, tier, title, body: bodyLines.join(" ").slice(0, 240) });
    }
  }
  const bloatWarning = bytes > 12 * 1024 || entries.length > 25;
  return { exists: true, path: relPath, bytes, totalEntries: entries.length, tiers, entries, bloatWarning };
}

function parseArgs(argv) {
  let runId = null;
  let workspace = null;
  let asJson = false;
  let useJudge = true;
  let strataUrl = process.env.STRATA_URL || "http://127.0.0.1:8080";
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === "--run" && argv[i + 1]) runId = argv[++i];
    else if (argv[i] === "--workspace" && argv[i + 1]) workspace = argv[++i];
    else if (argv[i] === "--strata" && argv[i + 1]) strataUrl = argv[++i];
    else if (argv[i] === "--no-judge") useJudge = false;
    else if (argv[i] === "--json") asJson = true;
    else if (!runId && !argv[i].startsWith("-")) runId = argv[i];
  }
  return { runId, workspace, asJson, useJudge, strataUrl };
}

async function runQwenJudge(strataUrl, report, recallExcerpts) {
  try {
    // 1. Discover active model on local Sync-Strata (fast 3s timeout)
    const mRes = await fetch(`${strataUrl}/v1/models`, { signal: AbortSignal.timeout(3000) });
    if (!mRes.ok) return { available: false, error: `models HTTP ${mRes.status}` };
    const mData = await mRes.json();
    const modelId = mData?.data?.[0]?.id || "qwen3.8-flash-next-sync-s";

    const payloadSummary = {
      runId: report.runId,
      totalTurns: report.totalTurns,
      writes: report.writes,
      recalls: report.recalls,
      verifier: report.hasVerifier
        ? { finalScore: report.finalScore, targetMet: report.targetMet, postRecallScoreLifts: report.positiveLiftRecalls, avgScoreLift: report.avgScoreLift }
        : null,
      recallEvents: recallExcerpts,
      memoryEntries: (report.memoryHealth?.entries || []).map((e) => ({
        id: e.id,
        tier: e.tier,
        title: e.title,
        body: e.body,
      })),
    };

    const systemPrompt = [
      "You are a strict, skeptical evaluator of an AI coding agent's 'Dream' memory harness.",
      "Do NOT flatter or assume Dream helped. Default to IGNORED unless there is concrete evidence.",
      "Respond ONLY with a single valid JSON object (no markdown fences, no extra prose) matching:",
      '{ "verdict": "HELPFUL" | "NEUTRAL" | "HARMFUL", "summary": "<1 concise Japanese sentence>", "recalls": [{ "turn": number, "status": "USED" | "IGNORED" | "DISTRACTED", "reason": "<short Japanese reason>" }], "memory": [{ "id": string, "action": "KEEP" | "PRUNE", "reason": "<short Japanese reason>" }] }',
      "Rules:",
      "- KEEP only genuinely reusable cross-task principles or concrete dead-end warnings.",
      "- PRUNE one-off task completion status logs ('Phase X landed at 100/100') or redundant notes.",
      "- If recalls is empty, judge based on memory quality and overhead (1.8% overhead with clean reusable notes => NEUTRAL or HELPFUL; bloated/status-only notes => NEUTRAL or HARMFUL).",
    ].join("\n");

    const cRes = await fetch(`${strataUrl}/v1/chat/completions`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      signal: AbortSignal.timeout(25000),
      body: JSON.stringify({
        model: modelId,
        temperature: 0.1,
        max_tokens: 2048,
        messages: [
          { role: "system", content: systemPrompt },
          { role: "user", content: JSON.stringify(payloadSummary) },
        ],
      }),
    });
    if (!cRes.ok) return { available: false, error: `chat HTTP ${cRes.status}` };
    const cData = await cRes.json();
    let content = String(cData?.choices?.[0]?.message?.content || "").trim();
    // Strip optional <think>...</think> or markdown fences if present
    content = content.replace(/<think>[\s\S]*?<\/think>/g, "").trim();
    const fence = content.match(/```(?:json)?\s*([\s\S]*?)```/i);
    if (fence) content = fence[1].trim();
    const startIdx = content.indexOf("{");
    const endIdx = content.lastIndexOf("}");
    if (startIdx >= 0 && endIdx > startIdx) content = content.slice(startIdx, endIdx + 1);
    const parsed = JSON.parse(content);
    return {
      available: true,
      model: modelId,
      verdict: parsed.verdict || "NEUTRAL",
      summary: parsed.summary || "",
      recalls: Array.isArray(parsed.recalls) ? parsed.recalls : [],
      memory: Array.isArray(parsed.memory) ? parsed.memory : [],
    };
  } catch (e) {
    return { available: false, error: String(e?.message || e) };
  }
}

function evaluateRun(runId, events, verifies, workspaceDir) {
  const startEv = events.find((e) => e.event === "session_start") || {};
  const finalEvs = events.filter((e) => e.event === "session_turn_final");
  const lastFinal = finalEvs[finalEvs.length - 1] || {};
  const writes = events.filter((e) => e.event === "dream_write");
  const recalls = events.filter((e) => e.event === "dream_recall");
  const followThroughs = events.filter((e) => e.event === "recall_followthrough");
  const guardSkips = events.filter((e) => e.event === "guard_skip");

  const startMs = startEv.ts ? Date.parse(startEv.ts) : 0;
  const runVerifies = startMs > 0 ? verifies.filter((v) => Date.parse(v.ts) >= startMs - 5000) : [];
  const hasVerifier = runVerifies.length > 0 || typeof lastFinal.latestScore === "number";

  const maxTurn = Math.max(
    lastFinal.turn || 0,
    ...events.map((e) => (typeof e.turn === "number" ? e.turn : 0)),
    1
  );
  const totalInjections = writes.length + recalls.length;
  const overheadRatio = Number((totalInjections / maxTurn).toFixed(3));

  const recallImpacts = recalls.map((rec) => {
    const t0 = Date.parse(rec.ts);
    const nextVerifies = runVerifies
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
    const ft = followThroughs.find((f) => f.turn >= rec.turn && f.turn <= rec.turn + 2);
    return {
      turn: rec.turn,
      ts: rec.ts,
      pickedIds: rec.pickedIds || [],
      pickedHeadings: rec.pickedHeadings || [],
      scoreBefore: before,
      bestScoreAfter: bestAfter,
      scoreDelta: delta,
      editedFilesAfter: ft?.editedFiles || [],
    };
  });

  const positiveLifts = recallImpacts.filter((r) => typeof r.scoreDelta === "number" && r.scoreDelta > 0);
  const scoredImpacts = recallImpacts.filter((r) => typeof r.scoreDelta === "number");
  const avgScoreLift =
    scoredImpacts.length > 0
      ? Number((scoredImpacts.reduce((a, b) => a + b.scoreDelta, 0) / scoredImpacts.length).toFixed(2))
      : null;

  const memoryHealth = inspectMemoryFile(workspaceDir, startEv.memoryFile || ".pi/memory/dream.md");
  const targetMet = lastFinal.targetMet === true || (runVerifies.length > 0 && runVerifies[runVerifies.length - 1].targetMet === true);
  const finalScore =
    typeof lastFinal.latestScore === "number"
      ? lastFinal.latestScore
      : runVerifies.length > 0 && typeof runVerifies[runVerifies.length - 1].score === "number"
        ? runVerifies[runVerifies.length - 1].score
        : null;

  let verdict = "NEUTRAL";
  const reasons = [];
  if (positiveLifts.length > 0) {
    verdict = "HELPFUL";
    reasons.push(`${positiveLifts.length}/${recalls.length} recalls preceded a verify score jump (avg delta ${avgScoreLift})`);
  }
  if (overheadRatio > 0.3 || memoryHealth?.bloatWarning) {
    verdict = "NOISY";
    if (overheadRatio > 0.3) reasons.push(`high injection overhead (${(overheadRatio * 100).toFixed(1)}%)`);
    if (memoryHealth?.bloatWarning) reasons.push(`memory file bloated (${memoryHealth.bytes} B / ${memoryHealth.totalEntries} entries)`);
  }
  if (reasons.length === 0) {
    reasons.push(totalInjections === 0 ? "short run (0 Dream checkpoints fired)" : "low overhead; see Qwen Judge for qualitative verdict");
  }

  return {
    ts: new Date().toISOString(),
    runId,
    dreamEnabled: Boolean(startEv.dreamEnabled),
    hasVerifier,
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
    judge: null,
  };
}

async function main() {
  const { runId: reqRunId, workspace, asJson, useJudge, strataUrl } = parseArgs(process.argv.slice(2));
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

  if (useJudge && (report.writes > 0 || report.recalls > 0 || (report.memoryHealth?.totalEntries || 0) > 0)) {
    report.judge = await runQwenJudge(strataUrl, report, report.recallImpacts);
    if (report.judge?.available && report.judge.verdict) {
      // Combine verifier + Qwen Judge: if verifier didn't already flag NOISY/HELPFUL, adopt Judge verdict
      if (report.verdict === "NEUTRAL") {
        report.verdict = report.judge.verdict;
      }
    }
  }

  try {
    fs.mkdirSync(SESSIONS_DIR, { recursive: true });
    fs.appendFileSync(EVAL_OUT, JSON.stringify(report) + "\n", "utf8");
  } catch {
    // non-fatal
  }

  if (asJson) {
    console.log(JSON.stringify(report, null, 2));
    return;
  }

  console.log("\n=== Dream Harness Post-Task Evaluation (Verifier + Qwen Judge) ===");
  console.log(`Run ID:        ${report.runId} (dream=${report.dreamEnabled ? "ON" : "OFF"})`);
  console.log(`Verdict:       ${report.verdict} — ${report.reasons.join("; ")}`);
  console.log(`Activity:      turns=${report.totalTurns}, writes=${report.writes}, recalls=${report.recalls}, overhead=${(report.overheadRatio * 100).toFixed(1)}%`);
  if (report.hasVerifier) {
    console.log(`Verifier:      finalScore=${report.finalScore ?? "-"}, targetMet=${report.targetMet}, postRecallScoreLifts=${report.positiveLiftRecalls}/${report.recalls} (avgDelta=${report.avgScoreLift ?? "-"})`);
  } else {
    console.log("Verifier:      (none — generic project mode)");
  }
  if (report.memoryHealth) {
    const mh = report.memoryHealth;
    console.log(`Memory Health: entries=${mh.totalEntries}, bytes=${mh.bytes}, tiers=${JSON.stringify(mh.tiers)}${mh.bloatWarning ? " [BLOAT WARNING]" : ""}`);
  }
  if (report.judge?.available) {
    console.log(`Qwen Judge:    [${report.judge.verdict}] ${report.judge.summary} (${report.judge.model})`);
    for (const m of report.judge.memory || []) {
      console.log(`  - Memory ${m.id}: [${m.action}] ${m.reason}`);
    }
    for (const r of report.judge.recalls || []) {
      console.log(`  - Recall T${r.turn}: [${r.status}] ${r.reason}`);
    }
  } else if (report.judge) {
    console.log(`Qwen Judge:    unavailable (${report.judge.error})`);
  }
  console.log("==================================================================\n");
}

main();
