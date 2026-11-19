import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { isToolCallEventType } from "@earendil-works/pi-coding-agent";
import fs from "node:fs";
import path from "node:path";

const COOLDOWN_TURNS = 2;
const MAX_PERTURBS_PER_PROMPT = 3;

const REFRAMING_ANGLES = [
  {
    name: "first_principles",
    prompt: [
      "[HARNESS PERTURBATION: FIRST PRINCIPLES CHECK]",
      "Pause and question your current assumptions before editing again:",
      "1. State 2-3 assumptions you are making (about root cause, API, config, or environment) that you have NOT directly verified.",
      "2. Check if the real issue lives in another layer (caller, config, build, types, or runtime state).",
      "3. Verify one assumption read-only before making the next edit.",
    ].join("\n"),
  },
  {
    name: "alternative_approaches",
    prompt: [
      "[HARNESS PERTURBATION: EXPLORE ALTERNATIVES]",
      "Avoid repeating local patches along a single path:",
      "1. Summarize in 1-2 sentences why the current approach has not converged.",
      "2. Propose 2-3 structurally different approaches (data flow, API usage, simplification vs. decomposition).",
      "3. Pick the option with the clearest objective verifier and test it.",
    ].join("\n"),
  },
  {
    name: "minimal_reproduction",
    prompt: [
      "[HARNESS PERTURBATION: MINIMAL REPRODUCTION]",
      "Shrink the feedback loop:",
      "1. Isolate the smallest reproducible failing case (one test, command, or minimal input).",
      "2. Inspect `git diff` to check for unintended side effects from prior edits.",
      "3. Confirm the exact failure mechanism before applying a targeted fix.",
    ].join("\n"),
  },
  {
    name: "wider_context",
    prompt: [
      "[HARNESS PERTURBATION: WIDER CONTEXT SCAN]",
      "Step back and widen your search:",
      "1. Check callers, configs, tests, `/refs` docs, or `git log -n 5` for missed constraints.",
      "2. Re-check whether your verifier command actually measures the real goal.",
      "3. State a revised hypothesis explicitly before acting.",
    ].join("\n"),
  },
];

function clamp01(v: number): number {
  return Math.max(0, Math.min(1, v));
}

function logPerturbation(entry: Record<string, unknown>) {
  const logPath = "/home/agent/.pi/agent/sessions/perturbation.jsonl";
  try {
    fs.mkdirSync(path.dirname(logPath), { recursive: true });
    fs.appendFileSync(logPath, JSON.stringify({ ts: new Date().toISOString(), ...entry }) + "\n", "utf8");
  } catch {
    // non-fatal
  }
}

export default function (pi: ExtensionAPI) {
  let turnCount = 0;
  let turnsSinceProgress = 0;
  let turnsSincePerturb = COOLDOWN_TURNS;
  let perturbsThisPrompt = 0;
  let hasEditedWorkspace = false;
  let consecutiveFailures = 0;
  let editsWithoutVerify = 0;
  let angleIndex = 0;
  let lastScore: number | null = null;
  let manualTriggerReason: string | null = null;
  const editCounts = new Map<string, number>();

  function computeStuckness() {
    let maxFileEdits = 0;
    let maxEditedFile = "";
    for (const [file, count] of editCounts.entries()) {
      if (count > maxFileEdits) {
        maxFileEdits = count;
        maxEditedFile = path.basename(file);
      }
    }

    const failureTerm = Math.min(0.45, consecutiveFailures * 0.22);
    const editThrashTerm = Math.min(0.35, Math.max(0, maxFileEdits - 1) * 0.17);
    // Only count turnsSinceProgress heavily once workspace edits have started
    const turnTerm = hasEditedWorkspace ? Math.min(0.25, Math.max(0, turnsSinceProgress - 1) * 0.05) : 0;
    const blindEditTerm = Math.min(0.20, Math.max(0, editsWithoutVerify - 1) * 0.08);

    const score = clamp01(failureTerm + editThrashTerm + turnTerm + blindEditTerm);
    const baseProb = hasEditedWorkspace ? 0.05 : 0.02;
    const probability = Number(Math.min(0.95, baseProb + 0.90 * Math.pow(score, 1.4)).toFixed(3));

    return {
      score: Number(score.toFixed(3)),
      probability,
      breakdown: {
        hasEditedWorkspace,
        consecutiveFailures,
        maxFileEdits,
        maxEditedFile,
        turnsSinceProgress,
        editsWithoutVerify,
        perturbsThisPrompt,
      },
    };
  }

  function resetAll() {
    turnCount = 0;
    turnsSinceProgress = 0;
    turnsSincePerturb = COOLDOWN_TURNS;
    perturbsThisPrompt = 0;
    hasEditedWorkspace = false;
    consecutiveFailures = 0;
    editsWithoutVerify = 0;
    lastScore = null;
    manualTriggerReason = null;
    editCounts.clear();
  }

  pi.on("agent_start", async () => {
    resetAll();
  });

  pi.on("tool_call", async (event) => {
    if (isToolCallEventType("write", event) || isToolCallEventType("edit", event)) {
      const target = String((event.input as { path?: string }).path || "");
      if (target.startsWith("/workspace")) {
        hasEditedWorkspace = true;
      }
      editsWithoutVerify += 1;
      if (target) {
        editCounts.set(target, (editCounts.get(target) || 0) + 1);
      }
    }
  });

  pi.on("tool_result", async (event) => {
    if (event.toolName === "verify_metrics") {
      editsWithoutVerify = 0;
      const details = (event.details || {}) as { score?: number; targetMet?: boolean };
      if (typeof details.score === "number") {
        if (details.targetMet) {
          consecutiveFailures = 0;
          turnsSinceProgress = 0;
          editCounts.clear();
        } else if (lastScore !== null && details.score > lastScore) {
          consecutiveFailures = Math.max(0, consecutiveFailures - 1);
          turnsSinceProgress = 0;
        } else {
          consecutiveFailures += 1;
        }
        lastScore = details.score;
      }
    } else if (event.isError) {
      consecutiveFailures += 1;
    } else if (event.toolName === "bash" && consecutiveFailures > 0) {
      consecutiveFailures = Math.max(0, consecutiveFailures - 0.5);
    }
  });

  pi.on("context", async (event) => {
    turnCount += 1;
    turnsSinceProgress += 1;
    turnsSincePerturb += 1;

    const state = computeStuckness();
    const inCooldown = turnsSincePerturb < COOLDOWN_TURNS;
    const capReached = perturbsThisPrompt >= MAX_PERTURBS_PER_PROMPT;
    const roll = Math.random();

    let triggered = false;
    let triggerReason = "";

    if (manualTriggerReason) {
      triggered = true;
      triggerReason = manualTriggerReason;
      manualTriggerReason = null;
    } else if (!inCooldown && !capReached && roll < state.probability) {
      triggered = true;
      triggerReason = `adaptive_roll(p=${state.probability}, roll=${roll.toFixed(3)})`;
    }

    const introspectLine =
      `[SELF-INTROSPECTION] stuckness=${state.score} (p_perturb=${state.probability}, ` +
      `failures=${state.breakdown.consecutiveFailures}, ` +
      `maxEdits=${state.breakdown.maxFileEdits}${state.breakdown.maxEditedFile ? "@" + state.breakdown.maxEditedFile : ""}, ` +
      `turnsSinceProgress=${state.breakdown.turnsSinceProgress}, ` +
      `editsWithoutVerify=${state.breakdown.editsWithoutVerify})`;

    if (!triggered) {
      return;
    }

    turnsSincePerturb = 0;
    perturbsThisPrompt += 1;
    const angle = REFRAMING_ANGLES[angleIndex % REFRAMING_ANGLES.length];
    angleIndex += 1;

    logPerturbation({
      event: "perturbation_triggered",
      reason: triggerReason,
      angle: angle.name,
      turnCount,
      stuckness: state.score,
      probability: state.probability,
      roll: Number(roll.toFixed(3)),
      breakdown: state.breakdown,
    });

    return {
      messages: [
        ...event.messages,
        {
          role: "user" as const,
          content: [
            {
              type: "text" as const,
              text: `${introspectLine}\n${angle.prompt}\n(Trigger: ${triggerReason}. Internal harness note: continue executing tools—do not stop with a text-only status update.)`,
            },
          ],
          timestamp: Date.now(),
        },
      ],
    };
  });

  pi.registerCommand("perturb", {
    description: "Show stuckness introspection status or queue a manual perturbation",
    handler: async (args, ctx) => {
      const state = computeStuckness();
      if (args.trim() === "status") {
        ctx.ui.notify(
          `stuckness=${state.score} p=${state.probability} turn=${turnCount} perturbs=${perturbsThisPrompt}/${MAX_PERTURBS_PER_PROMPT} failures=${state.breakdown.consecutiveFailures}`,
          "info",
        );
        return;
      }
      manualTriggerReason = args.trim() ? `manual:${args.trim()}` : "manual";
      ctx.ui.notify(`Perturbation queued (current stuckness=${state.score}).`, "info");
    },
  });
}
