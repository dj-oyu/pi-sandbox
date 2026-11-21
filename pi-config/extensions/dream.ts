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
  fullText: string;
}

interface PickedMemory {
  entry: MemoryEntry;
  mode: "vec" | "rand";
  score: number;
}

const METRICS_LOG = "/home/agent/.pi/agent/sessions/dream-metrics.jsonl";
const VERIFY_LOG = "/home/agent/.pi/agent/sessions/verify-metrics.jsonl";

// ---------------------------------------------------------------------------
// Zero-dependency CPU Vector Engine (Word + Code Identifier + CJK 2/3-gram TF-IDF Cosine)
// ---------------------------------------------------------------------------

function tokenize(text: string): string[] {
  const out: string[] = [];
  const lower = text.toLowerCase();

  // 1. ASCII words / code identifiers (split camelCase & snake_case/paths too)
  const rawWords = lower.match(/[a-z0-9_.-]{2,}/g) || [];
  for (const w of rawWords) {
    out.push(w);
    const parts = w.split(/[_./-]+/).filter((p) => p.length >= 2);
    if (parts.length > 1) {
      for (const p of parts) out.push(p);
    }
  }

  // 2. CJK / Japanese character 2-grams and 3-grams for morphological-free matching
  const cjkRuns = text.match(/[\u3040-\u30ff\u3400-\u9fff]{2,}/g) || [];
  for (const run of cjkRuns) {
    for (let i = 0; i + 2 <= run.length; i++) out.push(run.slice(i, i + 2));
    for (let i = 0; i + 3 <= run.length; i++) out.push(run.slice(i, i + 3));
  }

  return out;
}

function buildTfMap(tokens: string[]): Map<string, number> {
  const m = new Map<string, number>();
  for (const t of tokens) m.set(t, (m.get(t) || 0) + 1);
  // Sublinear TF: 1 + ln(count)
  for (const [k, v] of m.entries()) m.set(k, 1 + Math.log(v));
  return m;
}

function cosineSim(a: Map<string, number>, b: Map<string, number>, idf: Map<string, number>): number {
  let dot = 0;
  let normA = 0;
  let normB = 0;
  for (const [k, va] of a.entries()) {
    const w = idf.get(k) ?? 1.0;
    const wa = va * w;
    normA += wa * wa;
    const vb = b.get(k);
    if (vb !== undefined) {
      dot += wa * (vb * w);
    }
  }
  for (const [k, vb] of b.entries()) {
    const w = idf.get(k) ?? 1.0;
    const wb = vb * w;
    normB += wb * wb;
  }
  if (normA === 0 || normB === 0) return 0;
  return dot / (Math.sqrt(normA) * Math.sqrt(normB));
}

function pickHybridMemories(queryText: string, entries: MemoryEntry[], maxItems = 2): PickedMemory[] {
  if (entries.length === 0) return [];
  if (entries.length === 1) {
    return [{ entry: entries[0], mode: "rand", score: 0 }];
  }

  const docTfs = entries.map((e) => buildTfMap(tokenize(e.fullText)));
  const qTf = buildTfMap(tokenize(queryText));

  // Compute IDF across memory bank
  const df = new Map<string, number>();
  for (const tf of docTfs) {
    for (const k of tf.keys()) df.set(k, (df.get(k) || 0) + 1);
  }
  const nDocs = entries.length;
  const idf = new Map<string, number>();
  for (const [k, count] of df.entries()) {
    idf.set(k, Math.log(1 + nDocs / (1 + count)) + 1);
  }

  const picked: PickedMemory[] = [];
  const usedIdx = new Set<number>();

  // Slot 1: Vector Top-1 (most relevant to current edited files / recent tool context)
  if (qTf.size > 0) {
    let bestIdx = -1;
    let bestScore = 0;
    for (let i = 0; i < entries.length; i++) {
      const s = cosineSim(qTf, docTfs[i], idf);
      if (s > bestScore) {
        bestScore = s;
        bestIdx = i;
      }
    }
    if (bestIdx >= 0 && bestScore >= 0.05) {
      usedIdx.add(bestIdx);
      picked.push({ entry: entries[bestIdx], mode: "vec", score: Number(bestScore.toFixed(3)) });
    }
  }

  // Remaining slot(s): Random serendipity pick from unused entries
  const remaining = entries.map((e, i) => ({ e, i })).filter((x) => !usedIdx.has(x.i));
  while (picked.length < maxItems && remaining.length > 0) {
    const r = Math.floor(Math.random() * remaining.length);
    const chosen = remaining.splice(r, 1)[0];
    picked.push({ entry: chosen.e, mode: "rand", score: 0 });
  }

  return picked;
}

// ---------------------------------------------------------------------------
// Config & Logging
// ---------------------------------------------------------------------------

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
    // non-fatal
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
        const bodyLines: string[] = [];
        for (let j = i + 1; j < lines.length && bodyLines.length < 4; j++) {
          if (lines[j].startsWith("### ") || lines[j].startsWith("## ")) break;
          const trimmed = lines[j].trim();
          if (trimmed) bodyLines.push(trimmed);
        }
        const body = bodyLines.join(" ");
        entries.push({
          id,
          tier,
          heading: `[${tier}] ${id}: ${title}`,
          snippet: body.slice(0, 180),
          fullText: `${tier} ${title} ${body}`,
        });
      }
    }
    return { entries, bytes: Buffer.byteLength(text, "utf8") };
  } catch {
    return { entries: [], bytes: 0 };
  }
}

// ---------------------------------------------------------------------------
// Extension Entry
// ---------------------------------------------------------------------------

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
  let recentContextTerms: string[] = [];

  function pushContext(s: string) {
    if (!s) return;
    recentContextTerms.push(s.slice(0, 200));
    if (recentContextTerms.length > 12) recentContextTerms.shift();
  }

  pi.on("tool_call", (event) => {
    hadToolCallThisTurn = true;
    const ev = event as { toolName?: string; name?: string; input?: { path?: string; command?: string; oldText?: string } };
    const toolName = String(ev?.toolName || ev?.name || "");
    const targetPath = String(ev?.input?.path || "");
    if (targetPath) pushContext(targetPath);
    if (ev?.input?.command) pushContext(String(ev.input.command));

    if (toolName === "edit" || toolName === "write") {
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
        vectorEngine: "tfidf-word-cjk3gram-cosine",
        initialEntries: mem.entries.length,
        initialBytes: mem.bytes,
      });
    }

    if (!cfg.enabled) return;

    turnCount++;
    turnsSinceRecall++;

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

    if (isAlreadyTargetMet(cwd)) {
      logDreamEvent({ event: "guard_skip", runId, turn: turnCount, reason: "target_met" });
      return;
    }

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
    const queryText = [...lastEditedFiles.slice(-4), ...recentContextTerms.slice(-6)].join(" ");

    // 1. Periodic Dream Write Checkpoint
    if (totalWrites < cfg.maxWrites && now - lastWriteMs >= intervalMs) {
      lastWriteMs = now;
      turnsSinceRecall = 0;
      totalWrites++;
      hadCodeEditSinceLastInject = false;
      lastInjectedTurn = turnCount;
      lastInjectedKind = "write";

      const mem = parseMemoryEntries(cwd, cfg.memoryFile);
      const samples = pickHybridMemories(queryText, mem.entries, 2);
      const v = getLatestVerifyScore();

      logDreamEvent({
        event: "dream_write",
        runId,
        turn: turnCount,
        writeIndex: totalWrites,
        scoreBefore: v.score,
        memoryEntries: mem.entries.length,
        memoryBytes: mem.bytes,
        pickedIds: samples.map((s) => s.entry.id),
        pickedModes: samples.map((s) => (s.mode === "vec" ? `vec:${s.score}` : "rand")),
      });

      const recallBlock =
        samples.length > 0
          ? [
              "",
              `Also briefly cross-check these notes from \`${cfg.memoryFile}\`:`,
              ...samples.map((s) => {
                const tag = s.mode === "vec" ? `[vec ${s.score}]` : `[rand]`;
                return `- ${tag} ${s.entry.heading}${s.entry.snippet ? ` — ${s.entry.snippet}` : ""}`;
              }),
            ].join("\n")
          : "";

      pi.sendUserMessage(
        [
          `[DREAM HARNESS — PERIODIC MEMORY CHECKPOINT ${totalWrites}/${cfg.maxWrites}]`,
          `Briefly append 1-3 concise bullets to \`${cfg.memoryFile}\` capturing reusable lessons since the last checkpoint (avoid one-off status logs).`,
          "Use the heading format: `### [Principle|Insight|Dead End|Note] D<n> — <short title>` followed by 1-2 lines with concrete evidence.",
          recallBlock,
        ].join("\n"),
        { deliverAs: "followUp" }
      );
      return;
    }

    // 2. Hybrid Recall (Vector Top-1 + Random Serendipity)
    if (
      hadCodeEditSinceLastInject &&
      totalRecalls < cfg.maxRecalls &&
      turnCount >= 4 &&
      turnsSinceRecall >= cfg.recallCooldownTurns &&
      Math.random() < cfg.recallProb
    ) {
      const mem = parseMemoryEntries(cwd, cfg.memoryFile);
      const samples = pickHybridMemories(queryText, mem.entries, 2);
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
          pickedIds: samples.map((s) => s.entry.id),
          pickedModes: samples.map((s) => (s.mode === "vec" ? `vec:${s.score}` : "rand")),
          pickedHeadings: samples.map((s) => `${s.mode === "vec" ? `[vec ${s.score}]` : "[rand]"} ${s.entry.heading}`),
        });

        pi.sendUserMessage(
          [
            `[DREAM HARNESS — HYBRID MEMORY RECALL ${totalRecalls}/${cfg.maxRecalls}]`,
            `Before your next edit, glance at these recalled notes from \`${cfg.memoryFile}\` (relevant Top-1 + random serendipity):`,
            ...samples.map((s) => {
              const tag = s.mode === "vec" ? `[vec ${s.score}]` : `[rand]`;
              return `- ${tag} ${s.entry.heading}${s.entry.snippet ? ` — ${s.entry.snippet}` : ""}`;
            }),
            "If relevant, apply the lesson or avoid the dead end; if unrelated, ignore and continue.",
          ].join("\n"),
          { deliverAs: "followUp" }
        );
      }
    }
  });
}
