<script lang="ts">
  import { onMount } from 'svelte';
  import { apiFetch, shortTime } from '../lib/api';
  import { renderMarkdown } from '../lib/markdown';

  interface SessionInfo {
    id: string;
    path: string;
    size: number;
    modTime: string;
  }

  interface ToolCallItem {
    id: string;
    name: string;
    arguments: string;
  }

  interface SessionEntry {
    id: string;
    timestamp: string;
    kind: string;
    text?: string;
    thinking?: string;
    toolCalls?: ToolCallItem[];
    toolName?: string;
    isError?: boolean;
    usage?: Record<string, any>;
    model?: string;
  }

  let {
    strataConcurrency = $bindable(1),
    authVersion = 0,
    logs = [],
    onConfigSaved
  }: {
    strataConcurrency: number;
    authVersion?: number;
    logs?: Array<{ stream: string; record: Record<string, any> }>;
    onConfigSaved: () => void;
  } = $props();

  let sessions = $state<SessionInfo[]>([]);
  let selectedId = $state('');
  let latestKnownId = $state('');
  let userPinnedSession = $state(false);
  let entries = $state<SessionEntry[]>([]);
  let nextOffset = $state(-1);
  let prevCursor = $state(0);
  let hasMore = $state(false);
  let loadingOlder = $state(false);
  let promptInput = $state('');
  let sending = $state(false);
  let savingConc = $state(false);
  let streamEl: HTMLDivElement | undefined;

  const displayEntries = $derived.by(() => {
    if (entries.length === 0) return entries;
    const minTs = entries[0]?.timestamp || '';
    const maxTs = entries[entries.length - 1]?.timestamp || '';
    const injected: SessionEntry[] = logs
      .filter((x) => x.stream === 'control' && x.record?.event === 'advice_injected' && x.record?.ts)
      .filter((x) => {
        const ts = String(x.record.ts);
        if (minTs && ts < minTs) return false;
        if (hasMore && maxTs && ts > maxTs) return false;
        return true;
      })
      .map((x, idx) => ({
        id: `adv-${x.record.ts}-${idx}`,
        timestamp: String(x.record.ts),
        kind: 'advice',
        text: String(x.record.text || `[External AI Advice injected (${x.record.len || 0} chars)]`)
      }));
    if (injected.length === 0) return entries;
    return [...entries, ...injected].sort((a, b) => a.timestamp.localeCompare(b.timestamp));
  });

  function prettyArgs(raw: string): string {
    try {
      return JSON.stringify(JSON.parse(raw), null, 2);
    } catch {
      return raw;
    }
  }

  async function refreshSessionList() {
    try {
      const data = await apiFetch<{ sessions: SessionInfo[] }>('/admin/pi/sessions');
      sessions = data.sessions || [];
      if (sessions.length > 0) {
        const newest = sessions[0].id;
        const newSessionAppeared = newest !== latestKnownId;
        latestKnownId = newest;
        if (!selectedId || (newSessionAppeared && !userPinnedSession)) {
          selectedId = newest;
          await loadInitial(selectedId);
        }
      }
    } catch {
      // ignore when unauthenticated
    }
  }

  async function loadInitial(id: string) {
    if (!id) return;
    const res = await apiFetch<{
      entries: SessionEntry[];
      nextOffset: number;
      prevCursor: number;
      hasMore: boolean;
    }>(`/admin/pi/session?id=${encodeURIComponent(id)}&limit=40`);
    entries = res.entries || [];
    nextOffset = res.nextOffset ?? 0;
    prevCursor = res.prevCursor ?? 0;
    hasMore = !!res.hasMore;
    queueMicrotask(() => {
      if (streamEl) streamEl.scrollTop = streamEl.scrollHeight;
    });
  }

  async function loadOlder() {
    if (!selectedId || !hasMore || loadingOlder) return;
    loadingOlder = true;
    const prevHeight = streamEl?.scrollHeight || 0;
    try {
      const res = await apiFetch<{
        entries: SessionEntry[];
        prevCursor: number;
        hasMore: boolean;
      }>(`/admin/pi/session?id=${encodeURIComponent(selectedId)}&before=${prevCursor}&limit=40`);
      entries = [...(res.entries || []), ...entries];
      prevCursor = res.prevCursor ?? 0;
      hasMore = !!res.hasMore;
      queueMicrotask(() => {
        if (streamEl) {
          streamEl.scrollTop = streamEl.scrollHeight - prevHeight;
        }
      });
    } finally {
      loadingOlder = false;
    }
  }

  async function pollNew() {
    if (!selectedId || nextOffset < 0) return;
    try {
      const res = await apiFetch<{
        entries: SessionEntry[];
        nextOffset: number;
      }>(`/admin/pi/session?id=${encodeURIComponent(selectedId)}&offset=${nextOffset}`);
      if (res.entries && res.entries.length > 0) {
        const atBottom = streamEl
          ? streamEl.scrollHeight - streamEl.scrollTop - streamEl.clientHeight < 60
          : true;
        entries = [...entries, ...res.entries];
        // Keep browser memory bounded during long runs
        if (entries.length > 300) {
          entries = entries.slice(entries.length - 300);
        }
        if (atBottom) {
          queueMicrotask(() => {
            if (streamEl) streamEl.scrollTop = streamEl.scrollHeight;
          });
        }
      }
      if (typeof res.nextOffset === 'number') {
        nextOffset = res.nextOffset;
      }
    } catch {
      // ignore transient poll errors
    }
  }

  async function saveConcurrency() {
    savingConc = true;
    try {
      await apiFetch('/admin/config', {
        method: 'PATCH',
        body: JSON.stringify({ strataConcurrency: Number(strataConcurrency) })
      });
      onConfigSaved();
    } finally {
      savingConc = false;
    }
  }

  async function sendManualChat() {
    const p = promptInput.trim();
    if (!p || sending) return;
    sending = true;
    promptInput = '';
    entries = [
      ...entries,
      { id: `local-${Date.now()}`, timestamp: new Date().toISOString(), kind: 'user', text: `[manual] ${p}` }
    ];
    try {
      const res = await apiFetch<{ model: string; reply: string }>('/admin/strata/chat', {
        method: 'POST',
        body: JSON.stringify({ prompt: p })
      });
      entries = [
        ...entries,
        { id: `local-res-${Date.now()}`, timestamp: new Date().toISOString(), kind: 'assistant', model: res.model, text: res.reply }
      ];
    } catch (e: any) {
      entries = [
        ...entries,
        { id: `local-err-${Date.now()}`, timestamp: new Date().toISOString(), kind: 'toolResult', toolName: 'strata_chat', isError: true, text: e.message }
      ];
    } finally {
      sending = false;
      queueMicrotask(() => {
        if (streamEl) streamEl.scrollTop = streamEl.scrollHeight;
      });
    }
  }

  $effect(() => {
    void authVersion;
    userPinnedSession = false;
    refreshSessionList();
  });

  onMount(() => {
    const t1 = setInterval(pollNew, 2000);
    const t2 = setInterval(refreshSessionList, 3000);
    return () => {
      clearInterval(t1);
      clearInterval(t2);
    };
  });
</script>

<section class="pane">
  <div class="pane-head">
    <span>Pi Console</span>
    <div class="pane-controls">
      <select
        bind:value={selectedId}
        onchange={() => { userPinnedSession = selectedId !== latestKnownId; loadInitial(selectedId); }}
        style="max-width:190px;"
      >
        {#if sessions.length === 0}
          <option value="">(セッションなし)</option>
        {:else}
          {#each sessions as s}
            <option value={s.id}>{s.id.split('/').pop()}</option>
          {/each}
        {/if}
      </select>
      <button class="ghost" onclick={refreshSessionList}>更新</button>
      <span>並列枠 <input type="number" bind:value={strataConcurrency} min="1" max="16" style="width:46px;" /></span>
      <button class="ghost" onclick={saveConcurrency} disabled={savingConc}>適用</button>
    </div>
  </div>
  <div class="pane-body" style="display:flex; flex-direction:column;">
    <div class="stream-box" bind:this={streamEl}>
      {#if hasMore}
        <div class="row" style="justify-content:center; margin-bottom:4px;">
          <button class="ghost" onclick={loadOlder} disabled={loadingOlder}>
            {loadingOlder ? '読み込み中...' : '過去の履歴を読み込む (Load older)'}
          </button>
        </div>
      {/if}

      {#if displayEntries.length === 0}
        <div class="sub">Pi セッションログ待機中...</div>
      {:else}
        {#each displayEntries as item (item.id + item.timestamp)}
          <div class="msg {item.kind === 'assistant' || item.kind === 'advice' ? 'strata' : item.isError ? 'err' : 'user'}">
            <div class="mhead">
              <span>{item.kind === 'advice' ? 'ADVICE INJECTED' : item.kind === 'toolResult' ? `TOOL RESULT · ${item.toolName || ''}${item.isError ? ' · ERROR' : ''}` : item.kind.toUpperCase()}</span>
              <span>· {shortTime(item.timestamp)}</span>
              {#if item.usage?.output}<span>· out:{item.usage.output}</span>{/if}
            </div>
            {#if item.thinking}
              <details class="thinking">
                <summary>thinking ({item.thinking.length} chars)</summary>
                {item.thinking}
              </details>
            {/if}
            {#if item.toolCalls && item.toolCalls.length > 0}
              {#each item.toolCalls as tc}
                <div style="margin:4px 0;">
                  <span class="tag">toolCall: {tc.name}</span>
                  <pre class="term" style="margin-top:4px; max-height:160px;">{prettyArgs(tc.arguments)}</pre>
                </div>
              {/each}
            {/if}
            {#if item.text}
              {#if item.kind === 'toolResult'}
                <pre class="term" style="max-height:180px;">{item.text}</pre>
              {:else}
                <div class="md">{@html renderMarkdown(item.text)}</div>
              {/if}
            {/if}
          </div>
        {/each}
      {/if}
    </div>
    <div class="composer">
      <input
        bind:value={promptInput}
        style="flex:1;"
        placeholder="Sync-Strata へ直接テスト送信 (/admin/strata/chat)..."
        onkeydown={(e) => e.key === 'Enter' && sendManualChat()}
      />
      <button onclick={sendManualChat} disabled={sending}>送信</button>
    </div>
  </div>
</section>
