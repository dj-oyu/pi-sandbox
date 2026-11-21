import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";

const PKG_ROOT = "/usr/local/node/lib/node_modules/@earendil-works/pi-coding-agent";

function replaceExactOnce(source, target, replacement, label) {
    const first = source.indexOf(target);
    assert.notEqual(first, -1, `[patch-pi-coding-agent] Target pattern not found for: ${label}`);
    const second = source.indexOf(target, first + target.length);
    assert.equal(second, -1, `[patch-pi-coding-agent] Target pattern matched multiple times for: ${label}`);
    return source.slice(0, first) + replacement + source.slice(first + target.length);
}

function patchFile(relPath, hunks) {
    const fullPath = path.join(PKG_ROOT, relPath);
    let content = fs.readFileSync(fullPath, "utf8");
    for (const { label, target, replacement } of hunks) {
        content = replaceExactOnce(content, target, replacement, `${relPath}: ${label}`);
    }
    fs.writeFileSync(fullPath, content, "utf8");
    console.log(`[patch-pi-coding-agent] Patched ${relPath} (${hunks.length} hunk(s))`);
}

const pkgJson = JSON.parse(fs.readFileSync(path.join(PKG_ROOT, "package.json"), "utf8"));
assert.equal(
    pkgJson.version,
    "0.74.2",
    `[patch-pi-coding-agent] Expected @earendil-works/pi-coding-agent@0.74.2, found ${pkgJson.version}`,
);

// 1. Allow wrapRegisteredTool/wrapRegisteredTools to accept a dynamic runner getter () => ExtensionRunner
patchFile("dist/core/extensions/wrapper.js", [
    {
        label: "wrapRegisteredTool and wrapRegisteredTools dynamic runner",
        target: `export function wrapRegisteredTool(registeredTool, runner) {
    return wrapToolDefinition(registeredTool.definition, () => runner.createContext());
}
/**
 * Wrap all registered tools into AgentTools.
 * Uses the runner's createContext() for consistent context across tools and event handlers.
 */
export function wrapRegisteredTools(registeredTools, runner) {
    return wrapToolDefinitions(registeredTools.map((registeredTool) => registeredTool.definition), () => runner.createContext());
}`,
        replacement: `export function wrapRegisteredTool(registeredTool, runner) {
    const getRunner = typeof runner === "function" ? runner : () => runner;
    return wrapToolDefinition(registeredTool.definition, () => getRunner().createContext());
}
/**
 * Wrap all registered tools into AgentTools.
 * Uses the runner's createContext() for consistent context across tools and event handlers.
 */
export function wrapRegisteredTools(registeredTools, runner) {
    const getRunner = typeof runner === "function" ? runner : () => runner;
    return wrapToolDefinitions(registeredTools.map((registeredTool) => registeredTool.definition), () => getRunner().createContext());
}`,
    },
]);

// 2. Forward nextTurnContext to Agent.prepareNextTurn so AgentSession can refresh loop context.tools
patchFile("node_modules/@earendil-works/pi-agent-core/dist/agent.js", [
    {
        label: "forward nextTurnContext in createLoopConfig",
        target: `            prepareNextTurn: this.prepareNextTurn ? async () => await this.prepareNextTurn?.(this.signal) : undefined,`,
        replacement: `            prepareNextTurn: this.prepareNextTurn ? async (nextTurnContext) => await this.prepareNextTurn?.(this.signal, nextTurnContext) : undefined,`,
    },
]);

// 3. Abort active agent/turn/compaction cleanly before tearing down or disposing AgentSessionRuntime
patchFile("dist/core/agent-session-runtime.js", [
    {
        label: "abort session in teardownCurrent",
        target: `    async teardownCurrent(reason, targetSessionFile) {
        await emitSessionShutdownEvent(this.session.extensionRunner, {
            type: "session_shutdown",
            reason,
            targetSessionFile,
        });
        this.beforeSessionInvalidate?.();
        this.session.dispose();
    }`,
        replacement: `    async teardownCurrent(reason, targetSessionFile) {
        await this.session.abort();
        await emitSessionShutdownEvent(this.session.extensionRunner, {
            type: "session_shutdown",
            reason,
            targetSessionFile,
        });
        this.beforeSessionInvalidate?.();
        this.session.dispose();
    }`,
    },
    {
        label: "abort session in dispose",
        target: `    async dispose() {
        await emitSessionShutdownEvent(this.session.extensionRunner, {
            type: "session_shutdown",
            reason: "quit",
        });
        this.beforeSessionInvalidate?.();
        this.session.dispose();
    }`,
        replacement: `    async dispose() {
        await this.session.abort();
        await emitSessionShutdownEvent(this.session.extensionRunner, {
            type: "session_shutdown",
            reason: "quit",
        });
        this.beforeSessionInvalidate?.();
        this.session.dispose();
    }`,
    },
]);

// 4. Patch AgentSession lifecycle: dynamic runner lookup in tools, next-turn tool refresh,
//    safe reload() gating & oldRunner invalidation, and abort/timer cleanup on dispose().
patchFile("dist/core/agent-session.js", [
    {
        label: "add _disposed and _continueTimers fields",
        target: `    // Base system prompt (without extension appends) - used to apply fresh appends each turn
    _baseSystemPrompt = "";
    _baseSystemPromptOptions;`,
        replacement: `    // Base system prompt (without extension appends) - used to apply fresh appends each turn
    _baseSystemPrompt = "";
    _baseSystemPromptOptions;
    _disposed = false;
    _continueTimers = new Set();`,
    },
    {
        label: "install next-turn refresh and continue timer helpers",
        target: `        this._unsubscribeAgent = this.agent.subscribe(this._handleAgentEvent);
        this._installAgentToolHooks();
        this._buildRuntime({
            activeToolNames: this._initialActiveToolNames,
            includeAllExtensionTools: true,
        });
    }
    /** Model registry for API key resolution and model discovery */`,
        replacement: `        this._unsubscribeAgent = this.agent.subscribe(this._handleAgentEvent);
        this._installAgentToolHooks();
        this._installAgentNextTurnRefresh();
        this._buildRuntime({
            activeToolNames: this._initialActiveToolNames,
            includeAllExtensionTools: true,
        });
    }
    _installAgentNextTurnRefresh() {
        const previousPrepareNextTurn = this.agent.prepareNextTurn;
        this.agent.prepareNextTurn = async (signal, nextTurnContext) => {
            const previousSnapshot = await previousPrepareNextTurn?.(signal, nextTurnContext);
            const baseContext = previousSnapshot?.context ?? nextTurnContext?.context;
            return {
                ...previousSnapshot,
                context: baseContext
                    ? {
                        ...baseContext,
                        systemPrompt: this.agent.state.systemPrompt,
                        tools: this.agent.state.tools.slice(),
                    }
                    : undefined,
                model: previousSnapshot?.model ?? this.agent.state.model,
                thinkingLevel: previousSnapshot?.thinkingLevel ?? this.agent.state.thinkingLevel,
            };
        };
    }
    _scheduleAgentContinue(delayMs) {
        if (this._disposed) {
            return;
        }
        const timer = setTimeout(() => {
            this._continueTimers.delete(timer);
            if (this._disposed) {
                return;
            }
            this.agent.continue().catch(() => { });
        }, delayMs);
        this._continueTimers.add(timer);
    }
    _clearContinueTimers() {
        for (const timer of this._continueTimers) {
            clearTimeout(timer);
        }
        this._continueTimers.clear();
    }
    /** Model registry for API key resolution and model discovery */`,
    },
    {
        label: "abort in-flight operations and clear continue timers in dispose()",
        target: `    dispose() {
        this._extensionRunner.invalidate("This extension ctx is stale after session replacement or reload. Do not use a captured pi or command ctx after ctx.newSession(), ctx.fork(), ctx.switchSession(), or ctx.reload(). For newSession, fork, and switchSession, move post-replacement work into withSession and use the ctx passed to withSession. For reload, do not use the old ctx after await ctx.reload().");
        this._disconnectFromAgent();
        this._eventListeners = [];
        cleanupSessionResources(this.sessionId);
    }`,
        replacement: `    dispose() {
        this._disposed = true;
        this._clearContinueTimers();
        try {
            this.abortRetry();
            this.abortCompaction();
            this.abortBranchSummary();
            this.abortBash();
            this.agent.abort();
        }
        catch {
            // Dispose must succeed even if an abort hook throws.
        }
        this._extensionRunner.invalidate("This extension ctx is stale after session replacement or reload. Do not use a captured pi or command ctx after ctx.newSession(), ctx.fork(), ctx.switchSession(), or ctx.reload(). For newSession, fork, and switchSession, move post-replacement work into withSession and use the ctx passed to withSession. For reload, do not use the old ctx after await ctx.reload().");
        this._disconnectFromAgent();
        this._eventListeners = [];
        cleanupSessionResources(this.sessionId);
    }`,
    },
    {
        label: "abort compaction/bash/timers in abort()",
        target: `    async abort() {
        this.abortRetry();
        this.agent.abort();
        await this.agent.waitForIdle();
    }`,
        replacement: `    async abort() {
        this._clearContinueTimers();
        this.abortRetry();
        this.abortCompaction();
        this.abortBranchSummary();
        this.abortBash();
        this.agent.abort();
        await this.agent.waitForIdle();
    }`,
    },
    {
        label: "track auto-compaction continuation timers",
        target: `            if (willRetry) {
                const messages = this.agent.state.messages;
                const lastMsg = messages[messages.length - 1];
                if (lastMsg?.role === "assistant" && lastMsg.stopReason === "error") {
                    this.agent.state.messages = messages.slice(0, -1);
                }
                setTimeout(() => {
                    this.agent.continue().catch(() => { });
                }, 100);
            }
            else if (this.agent.hasQueuedMessages()) {
                // Auto-compaction can complete while follow-up/steering/custom messages are waiting.
                // Kick the loop so queued messages are actually delivered.
                setTimeout(() => {
                    this.agent.continue().catch(() => { });
                }, 100);
            }`,
        replacement: `            if (willRetry) {
                const messages = this.agent.state.messages;
                const lastMsg = messages[messages.length - 1];
                if (lastMsg?.role === "assistant" && lastMsg.stopReason === "error") {
                    this.agent.state.messages = messages.slice(0, -1);
                }
                this._scheduleAgentContinue(100);
            }
            else if (this.agent.hasQueuedMessages()) {
                // Auto-compaction can complete while follow-up/steering/custom messages are waiting.
                // Kick the loop so queued messages are actually delivered.
                this._scheduleAgentContinue(100);
            }`,
    },
    {
        label: "pass dynamic runner getter in _refreshToolRegistry",
        target: `        const runner = this._extensionRunner;
        const wrappedExtensionTools = wrapRegisteredTools(allCustomTools, runner);
        const wrappedBuiltInTools = wrapRegisteredTools(Array.from(this._baseToolDefinitions.values())
            .filter((definition) => isAllowedTool(definition.name))
            .map((definition) => ({
            definition,
            sourceInfo: createSyntheticSourceInfo(\`<builtin:\${definition.name}>\`, { source: "builtin" }),
        })), runner);`,
        replacement: `        const getRunner = () => this._extensionRunner;
        const wrappedExtensionTools = wrapRegisteredTools(allCustomTools, getRunner);
        const wrappedBuiltInTools = wrapRegisteredTools(Array.from(this._baseToolDefinitions.values())
            .filter((definition) => isAllowedTool(definition.name))
            .map((definition) => ({
            definition,
            sourceInfo: createSyntheticSourceInfo(\`<builtin:\${definition.name}>\`, { source: "builtin" }),
        })), getRunner);`,
    },
    {
        label: "guard reload during streaming/compaction and invalidate oldRunner",
        target: `    async reload() {
        const previousFlagValues = this._extensionRunner.getFlagValues();
        await emitSessionShutdownEvent(this._extensionRunner, { type: "session_shutdown", reason: "reload" });
        await this.settingsManager.reload();
        resetApiProviders();
        await this._resourceLoader.reload();`,
        replacement: `    async reload() {
        if (this.isStreaming) {
            throw new Error("Wait for the current response to finish before reloading.");
        }
        if (this.isCompacting) {
            throw new Error("Wait for compaction to finish before reloading.");
        }
        const oldRunner = this._extensionRunner;
        const previousFlagValues = oldRunner.getFlagValues();
        await emitSessionShutdownEvent(oldRunner, { type: "session_shutdown", reason: "reload" });
        oldRunner.invalidate();
        await this.settingsManager.reload();
        resetApiProviders();
        await this._resourceLoader.reload();`,
    },
    {
        label: "track auto-retry continuation timer",
        target: `        // Retry via continue() - use setTimeout to break out of event handler chain
        setTimeout(() => {
            this.agent.continue().catch(() => {
                // Retry failed - will be caught by next agent_end
            });
        }, 0);`,
        replacement: `        // Retry via continue() - use setTimeout to break out of event handler chain
        this._scheduleAgentContinue(0);`,
    },
]);

console.log("[patch-pi-coding-agent] All patches applied and verified.");
