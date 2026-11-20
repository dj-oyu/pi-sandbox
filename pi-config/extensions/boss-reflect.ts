import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import fs from "node:fs";
import path from "node:path";

const REFLECT_INTERVAL_MS = 15 * 60 * 1000; // 15 minutes
const RECALL_COOLDOWN_TURNS = 5;
const RECALL_PROBABILITY = 0.22;
const MAX_REFLECTS_PER_SESSION = 6;
const MAX_RECALLS_PER_SESSION = 4;

function isBossMode(cwd: string): boolean {
  try {
    return fs.existsSync(path.join(cwd, ".pi", "BOSS_MODE"));
  } catch {
    return false;
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

function readRandomDiscoveries(cwd: string, maxItems = 2): string[] {
  const p = path.join(cwd, "docs", "DISCOVERIES.md");
  try {
    if (!fs.existsSync(p)) return [];
    const text = fs.readFileSync(p, "utf8");
    const headings = text
      .split(/\r?\n/)
      .filter((line) => /^###\s+\[(Epic|Nobel Prize|Genius|Insightful|Useful|Note|Dead End)\]/i.test(line));
    if (headings.length === 0) return [];
    const picked: string[] = [];
    const pool = [...headings];
    while (picked.length < maxItems && pool.length > 0) {
      const idx = Math.floor(Math.random() * pool.length);
      picked.push(pool.splice(idx, 1)[0]);
    }
    return picked;
  } catch {
    return [];
  }
}

export default function (pi: ExtensionAPI) {
  let lastReflectMs = Date.now();
  let turnCount = 0;
  let turnsSinceRecall = 0;
  let totalReflects = 0;
  let totalRecalls = 0;
  let hadToolCallThisTurn = false;
  let hadCodeEditSinceLastInject = false;

  pi.on("tool_call", (event) => {
    hadToolCallThisTurn = true;
    const toolName = String((event as { toolName?: string; name?: string })?.toolName || (event as { name?: string })?.name || "");
    if (toolName === "edit" || toolName === "write") {
      const input = (event as { input?: { path?: string } })?.input;
      const targetPath = String(input?.path || "");
      // Only count edits outside docs/DISCOVERIES.md as real code progress
      if (!targetPath.endsWith("DISCOVERIES.md")) {
        hadCodeEditSinceLastInject = true;
      }
    }
  });

  pi.on("turn_end", (_event, ctx) => {
    const hadTools = hadToolCallThisTurn;
    hadToolCallThisTurn = false;

    const cwd = ctx.cwd || "/workspace";
    if (!isBossMode(cwd)) return;

    // Never hijack a final assistant response (no tool calls) or an already-completed run
    if (!hadTools || isAlreadyTargetMet(cwd)) return;

    turnCount++;
    turnsSinceRecall++;
    const now = Date.now();

    // 1. Non-intrusive 15-minute reflection prompt (only while actively working & capped)
    if (
      totalReflects < MAX_REFLECTS_PER_SESSION &&
      now - lastReflectMs >= REFLECT_INTERVAL_MS
    ) {
      lastReflectMs = now;
      turnsSinceRecall = 0;
      totalReflects++;
      hadCodeEditSinceLastInject = false;
      const samples = readRandomDiscoveries(cwd, 2);
      const recallBlock =
        samples.length > 0
          ? `\nAlso re-read these past entries from docs/DISCOVERIES.md and check for synergies:\n${samples.map((s) => `- ${s}`).join("\n")}`
          : "";
      pi.sendUserMessage(
        [
          `[BOSS HARNESS — 15-MINUTE REFLECTION CHECKPOINT ${totalReflects}/${MAX_REFLECTS_PER_SESSION}]`,
          "Without discarding your current momentum, briefly update `docs/DISCOVERIES.md` with what you learned in the last 15 minutes.",
          "Rate each entry on the strict 7-tier scale: `Epic` > `Nobel Prize` > `Genius` > `Insightful` > `Useful` > `Note` > `Dead End`.",
          "Even if no speedup landed yet, record what you tried and why it was a `[Dead End]` or `[Note]` — negative evidence is valuable.",
          recallBlock,
        ].join("\n"),
        { deliverAs: "followUp" }
      );
      return;
    }

    // 2. Random periodic re-read of docs/DISCOVERIES.md
    // Only fires if real code was edited since the last injection (prevents audit-only infinite loops)
    if (
      hadCodeEditSinceLastInject &&
      totalRecalls < MAX_RECALLS_PER_SESSION &&
      turnCount >= 4 &&
      turnsSinceRecall >= RECALL_COOLDOWN_TURNS &&
      Math.random() < RECALL_PROBABILITY
    ) {
      const samples = readRandomDiscoveries(cwd, 2);
      if (samples.length > 0) {
        turnsSinceRecall = 0;
        totalRecalls++;
        hadCodeEditSinceLastInject = false;
        pi.sendUserMessage(
          [
            `[BOSS HARNESS — RANDOM DISCOVERY RECALL ${totalRecalls}/${MAX_RECALLS_PER_SESSION}]`,
            "Take a quick look back at `docs/DISCOVERIES.md` (especially these entries) before your next experiment:",
            ...samples.map((s) => `- ${s}`),
            "Can any earlier insight or dead-end lesson sharpen your next optimization?",
          ].join("\n"),
          { deliverAs: "followUp" }
        );
      }
    }
  });
}
