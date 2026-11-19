import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { isToolCallEventType } from "@earendil-works/pi-coding-agent";

const DANGEROUS_BASH_PATTERNS: Array<{ re: RegExp; reason: string }> = [
  { re: /\brm\s+-[a-zA-Z]*r[a-zA-Z]*f[a-zA-Z]*\s+(\/|\/\*|~|\/workspace)\s*($|[;&|])/, reason: "Refusing catastrophic recursive delete on root/home/workspace" },
  { re: /\bmkfs(\.[a-z0-9]+)?\b/, reason: "Refusing filesystem format command" },
  { re: /\bdd\s+.*\bof=\/dev\//, reason: "Refusing raw device write via dd" },
  { re: /:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:/, reason: "Refusing fork bomb" },
  { re: /\bchmod\s+-[a-zA-Z]*R[a-zA-Z]*\s+777\s+\/(\s|$)/, reason: "Refusing recursive 777 on root" },
  { re: /\bgit\s+push\b.*(--force|-f)\b/, reason: "Refusing force push inside autonomous sandbox" },
  {
    re: /(^|[;&|\n]\s*)gh(\s|$)/,
    reason:
      "`gh` CLI is not installed in this sandbox. For real-time CI/CD status and logs, read /home/agent/.pi/agent/skills/github-ci/SKILL.md and query $PI_PROXY_URL/gh-ci/... with Bearer $STRATA_PROXY_TOKEN. For pushing/PRs, commit in /workspace and ask the user to push from the host.",
  },
];

export default function (pi: ExtensionAPI) {
  pi.on("tool_call", async (event) => {
    if (isToolCallEventType("bash", event)) {
      const cmd = event.input.command || "";
      for (const rule of DANGEROUS_BASH_PATTERNS) {
        if (rule.re.test(cmd)) {
          return { block: true, reason: rule.reason };
        }
      }
      if (typeof event.input.timeout !== "number" || event.input.timeout <= 0) {
        event.input.timeout = 90;
      } else if (event.input.timeout > 180) {
        event.input.timeout = 180;
      }
    }

    if (isToolCallEventType("write", event) || isToolCallEventType("edit", event)) {
      const target = String((event.input as { path?: string }).path || "");
      if (target.startsWith("/home/agent/.pi/agent/") && !target.startsWith("/home/agent/.pi/agent/sessions/")) {
        return { block: true, reason: "Harness config under ~/.pi/agent is read-only" };
      }
    }
  });
}
