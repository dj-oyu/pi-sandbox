import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import fs from "node:fs";
import path from "node:path";

interface DreamConfig {
  enabled: boolean;
  intervalMin: number;
  recallProb: number;
  recallCooldownTurns: number;
  maxWrites: number;
  maxRecalls: number;
  memoryFile: string;
}

interface MemoryEntry {
  id: string;
  tier: string;
  heading: string;
  snippet: string;
}

const METRICS_LOG = "/home/agent/.pi/agent/sessions/dream-metrics.jsonl";
const VERIFY_LOG = "/home/agent/.pi/agent/sessions/verify-metrics.jsonl";

function loadDreamConfig(cwd: string): DreamConfig {
  const defaults: DreamConfig = {
    enabled: process.env.PI_DREAM === "1",
    intervalMin: Number(process.env.PI_DREAM_INTERVAL_MIN || 12) || 12,
    recallProb: Number(process.env.PI_DREAM_RECALL_PROB || 0.22) || 0.22,
    recallCooldownTurns: Number(process.env.PI_DREAM_COOLDOWN || 4) || 4,
    maxWrites: Number(process.env.PI_DREAM_MAX_WRITES || 6) || 6,
    maxRecalls: Number(process.env.PI_DREAM_MAX_RECALLS || 4) || 4,
    memoryFile: process.env.PI_DREAM_MEMORY_FILE || ".pi/memory/dream.md",
  };
  try {
    const cfgPath = path.join(cwd, ".pi", "dream.json");
    if (fs.existsSync(cfgPath)) {
      const raw = JSON.parse(fs.readFileSync(cfgPath, "utf8"));
      if (typeof raw.enabled === "boolean" && process.env.PI_DREAM !== "1" && process.env.PI_DREAM !== "0") {
        defaults.enabled = raw.enabled;
      }
      if (typeof raw.intervalMin === "number" && raw.intervalMin > 0) defaults.intervalMin = raw.intervalMin;
      if (typeof raw.recallProb === "number" && raw.recallProb >= 0 && raw.recallProb <= 1) defaults.recallProb = raw.recallProb;
      if (typeof raw.recallCooldownTurns === "number" && raw.recallCooldownTurns >= 1) defaults.recallCooldownTurns = raw.recallCooldownTurns;
      if (typeof raw.maxWrites === "number" && raw.maxWrites >= 0) defaults.maxWrites = raw.maxWrites;
      if (typeof raw.maxRecalls === "number" && raw.maxRecalls >= 0) defaults.maxRecalls = raw.maxRecalls;
      if (typeof raw.memoryFile === "string" && raw.memoryFile.trim()) defaults.memoryFile = raw.memoryFile.trim();
    }
  } catch {
    // ignore malformed dream.json
  }
  if (process.env.PI_DREAM === "0") defaults.enabled = false;
  return defaults;
}

function logDreamEvent(entry: Record<string, unknown>) {
  try {
    fs.mkdirSync(path.dirname(METRICS_LOG), { recursive: true });
    fs.appendFileSync(
      METRICS_LOG,
      JSON.stringify({ ts: new Date().toISOString(), ...entry }) + "\n",
      "utf8"
    );
  } catch {
    // non-fatal telemetry failure
  }
}

function getLatestVerifyScore(): { score: number | null; targetMet: boolean } {
  try {
    if (!fs.existsSync(VERIFY_LOG)) return { score: null, targetMet: false };
    const lines = fs.readFileSync(VERIFY_LOG, "utf8").trim().split(/\r?\n/).filter(Boolean);
    if (lines.length === 0) return { score: null, targetMet: false };
    const last = JSON.parse(lines[lines.length - 1]);
    return {
      score: typeof last.score === "number" ? last.score : null,
      targetMet: last.targetMet === true,
    };
  } catch {
    return { score: null, targetMet: false };
  }
}

function isAlreadyTargetMet(cwd: string): boolean {
  try {
    const p = path.join(cwd, ".pi", "DONE.json");
    if (!fs.existsSync(p)) return false;
    const obj = JSON.parse(fs.readFileSync(p, "utf8"));
    return obj && obj.targetMet === true;
  } catch {
    return false;
  }
}

function parseMemoryEntries(cwd: string, relPath: string): { entries: MemoryEntry[]; bytes: number } {
  const full = path.join(cwd, relPath);
  try {
    if (!fs.existsSync(full)) return { entries: [], bytes: 0 };
    const text = fs.readFileSync(full, "utf8");
    const lines = text.split(/\r?\n/);
    const entries: MemoryEntry[] = [];
    for (let i = 0; i < lines.length; i++) {
      const m = lines[i].match(/^###\s+\[(Principle|Insight|Dead End|Note|Epic|Nobel Prize|Genius|Insightful|Useful)\]\s*(?:([A-Za-z0-9_-]+)\s*[-—:]\s*)?(.+)$/i);
      if (m) {
        const tier = m[1];
        const id = m[2] || `M${entries.length + 1}`;
        const title = m[3].trim();
        // Grab up to 2 body lines as compact context
        const bodyLines: string[] = [];
        for (let j = i + 1; j < lines.length && bodyLines.length < 2; j++) {
          if (lines[j].startsWith("### ") || lines[j].startsWith("## ")) break;
          const trimmed = lines[j].trim();
          if (trimmed) bodyLines.push(trimmed);
        }
        entries.push({
          id,
          tier,
          heading: `[${tier}] ${id}: ${title}`,
          snippet: bodyLines.join(" ").slice(0, 180),
        });
      }
    }
    return { entries, bytes: Buffer.byteLength(text, "utf8") };
  } catch {
    return { entries: [], bytes: 0 };
  }
}

function pickRandomEntries(entries: MemoryEntry[], maxItems = 2): MemoryEntry[] {
  if (entries.length === 0) return [];
  const pool = [...entries];
  const picked: MemoryEntry[] = [];
  while (picked.length < maxItems && pool.length > 0) {
    const idx = Math.floor(Math.random() * pool.length);
    picked.push(pool.splice(idx, 1)[0]);
  }
  return picked;
}

export default function (pi: ExtensionAPI) {
  const runId = process.env.PI_DREAM_RUN_ID || `run-${Date.now()}`;
  let startedLogged = false;
  let lastWriteMs = Date.now();
  let turnCount = 0;
  let turnsSinceRecall = 0;
  let totalWrites = 0;
  let totalRecalls = 0;
  let hadToolCallThisTurn = false;
  let hadCodeEditSinceLastInject = false;
  let lastInjectedTurn = -1;
  let lastInjectedKind: string | null = null;
  let lastEditedFiles: string[] = [];

  pi.on("tool_call", (event) => {
    hadToolCallThisTurn = true;
    const ev = event as { toolName?: string; name?: string; input?: { path?: string } };
    const toolName = String(ev?.toolName || ev?.name || "");
    if (toolName === "edit" || toolName === "write") {
      const targetPath = String(ev?.input?.path || "");
      const norm = targetPath.replace(/\\/g, "/");
      if (!norm.endsWith("dream.md") && !norm.endsWith("DISCOVERIES.md")) {
        hadCodeEditSinceLastInject = true;
        if (targetPath && !lastEditedFiles.includes(targetPath)) {
          lastEditedFiles.push(targetPath);
          if (lastEditedFiles.length > 10) lastEditedFiles.shift();
        }
      }
    }
  });

  pi.on("turn_end", (_event, ctx) => {
    const hadTools = hadToolCallThisTurn;
    hadToolCallThisTurn = false;

    const cwd = ctx.cwd || "/workspace";
    const cfg = loadDreamConfig(cwd);

    if (!startedLogged) {
      startedLogged = true;
      const mem = parseMemoryEntries(cwd, cfg.memoryFile);
      logDreamEvent({
        event: "session_start",
        runId,
        cwd,
        dreamEnabled: cfg.enabled,
        intervalMin: cfg.intervalMin,
        recallProb: cfg.recallProb,
        memoryFile: cfg.memoryFile,
        initialEntries: mem.entries.length,
        initialBytes: mem.bytes,
      });
    }

    if (!cfg.enabled) return;

    turnCount++;
    turnsSinceRecall++;

    // Guard 1: Never hijack a final assistant response (no tool calls)
    if (!hadTools) {
      const mem = parseMemoryEntries(cwd, cfg.memoryFile);
      const v = getLatestVerifyScore();
      logDreamEvent({
        event: "session_turn_final",
        runId,
        turn: turnCount,
        totalWrites,
        totalRecalls,
        memoryEntries: mem.entries.length,
        memoryBytes: mem.bytes,
        latestScore: v.score,
        targetMet: v.targetMet || isAlreadyTargetMet(cwd),
      });
      return;
    }

    // Guard 2: Stop once DONE.json targetMet is achieved
    if (isAlreadyTargetMet(cwd)) {
      logDreamEvent({ event: "guard_skip", runId, turn: turnCount, reason: "target_met" });
      return;
    }

    // Track follow-through after a recent recall
    if (lastInjectedKind === "recall" && turnCount - lastInjectedTurn <= 2 && hadCodeEditSinceLastInject) {
      const v = getLatestVerifyScore();
      logDreamEvent({
        event: "recall_followthrough",
        runId,
        turn: turnCount,
        turnsAfterRecall: turnCount - lastInjectedTurn,
        editedFiles: lastEditedFiles.slice(-3),
        scoreNow: v.score,
      });
      lastInjectedKind = null;
    }

    const now = Date.now();
    const intervalMs = cfg.intervalMin * 60 * 1000;

    // 1. Periodic Dream Write Checkpoint
    if (totalWrites < cfg.maxWrites && now - lastWriteMs >= intervalMs) {
      lastWriteMs = now;
      turnsSinceRecall = 0;
      totalWrites++;
      hadCodeEditSinceLastInject = false;
      lastInjectedTurn = turnCount;
      lastInjectedKind = "write";

      const mem = parseMemoryEntries(cwd, cfg.memoryFile);
      const samples = pickRandomEntries(mem.entries, 2);
      const v = getLatestVerifyScore();

      logDreamEvent({
        event: "dream_write",
        runId,
        turn: turnCount,
        writeIndex: totalWrites,
        scoreBefore: v.score,
        memoryEntries: mem.entries.length,
        memoryBytes: mem.bytes,
        pickedIds: samples.map((s) => s.id),
      });

      const recallBlock =
        samples.length > 0
          ? [
              "",
              `Also briefly cross-check these past notes from \`${cfg.memoryFile}\`:`,
              ...samples.map((s) => `- ${s.heading}${s.snippet ? ` — ${s.snippet}` : ""}`),
            ].join("\n")
          : "";

      pi.sendUserMessage(
        [
          `[DREAM HARNESS — PERIODIC MEMORY CHECKPOINT ${totalWrites}/${cfg.maxWrites}]`,
          `Briefly append 1-3 concise bullets to \`${cfg.memoryFile}\` capturing what you learned since the last checkpoint (without losing momentum).`,
          "Use the heading format: `### [Principle|Insight|Dead End|Note] D<n> — <short title>` followed by 1-2 lines with concrete evidence/numbers.",
          "Recording `[Dead End]` findings is just as valuable as successes so you do not repeat failed paths.",
          recallBlock,
        ].join("\n"),
        { deliverAs: "followUp" }
      );
      return;
    }

    // 2. Stochastic Recall (only after real code edits, respecting cooldown and session cap)
    if (
      hadCodeEditSinceLastInject &&
      totalRecalls < cfg.maxRecalls &&
      turnCount >= 4 &&
      turnsSinceRecall >= cfg.recallCooldownTurns &&
      Math.random() < cfg.recallProb
    ) {
      const mem = parseMemoryEntries(cwd, cfg.memoryFile);
      const samples = pickRandomEntries(mem.entries, 2);
      if (samples.length > 0) {
        turnsSinceRecall = 0;
        totalRecalls++;
        hadCodeEditSinceLastInject = false;
        lastInjectedTurn = turnCount;
        lastInjectedKind = "recall";
        const v = getLatestVerifyScore();

        logDreamEvent({
          event: "dream_recall",
          runId,
          turn: turnCount,
          recallIndex: totalRecalls,
          scoreBefore: v.score,
          memoryEntries: mem.entries.length,
          memoryBytes: mem.bytes,
          pickedIds: samples.map((s) => s.id),
          pickedHeadings: samples.map((s) => s.heading),
        });

        pi.sendUserMessage(
          [
            `[DREAM HARNESS — STOCHASTIC MEMORY RECALL ${totalRecalls}/${cfg.maxRecalls}]`,
            `Before your next edit, glance at these recalled notes from \`${cfg.memoryFile}\`:`,
            ...samples.map((s) => `- ${s.heading}${s.snippet ? ` — ${s.snippet}` : ""}`),
            "If relevant, apply the lesson or avoid the dead end; if unrelated, ignore and continue.",
          ].join("\n"),
          { deliverAs: "followUp" }
        );
      }
    }
  });
}
