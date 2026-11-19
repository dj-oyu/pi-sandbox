<script lang="ts">
  import { apiFetch, shortTime } from '../lib/api';
  import { renderMarkdown } from '../lib/markdown';

  export interface ArenaTurn {
    turn: number;
    speaker: string;
    model: string;
    content: string;
    filesRead?: string[];
    errorsRead?: boolean;
  }

  interface ArenaMatch {
    ts: string;
    topic: string;
    saasModel?: string;
    transcript: ArenaTurn[];
  }

  let {
    turns = $bindable([]),
    config,
    authVersion = 0,
    pendingAdvice = '',
    onConfigSaved
  }: {
    turns: ArenaTurn[];
    config: any;
    authVersion?: number;
    pendingAdvice?: string;
    onConfigSaved: () => void;
  } = $props();

  let history = $state<ArenaMatch[]>([]);
  let selectedMatchIdx = $state(-1);

  let showSettings = $state(false);
  let showContext = $state(false);
  let topic = $state('ローカルエージェントの検証器設計と失敗許容基準');
  let numTurns = $state(4);
  let running = $state(false);
  let errorMsg = $state('');

  let initialPrompt = $state('');
  let availableFiles = $state<string[]>([]);
  let allowedMap = $state<Record<string, boolean>>({});
  let allowErrors = $state(true);
  let contextLoaded = $state(false);

  const allowedFiles = $derived(availableFiles.filter((f) => allowedMap[f]));
  const saasSystemPreview = $derived(
    [
      saasSystem,
      initialPrompt.trim() ? `## Initial User Prompt\n${initialPrompt.trim()}` : '',
      allowedFiles.length > 0
        ? `## Visible Workspace Files\n${allowedFiles.join('\n')}\n\nUse \`read_file\` with any path above when you need to inspect file contents.`
        : ''
    ]
      .filter(Boolean)
      .join('\n\n')
  );

  let saasLabel = $state('');
  let apiFormat = $state('openai');
  let authType = $state('bearer');
  let customHeader = $state('');
  let baseUrl = $state('');
  let model = $state('');
  let apiKey = $state('');
  let extraHeadersJson = $state('{}');
  let strataSystem = $state('You are Sync-Strata (local open-weight AI). Discuss concisely and technically.');
  let saasSystem = $state('You are a peer SaaS AI. Challenge assumptions constructively and concisely.');

  const presets = $derived((config?.saasPresets || []) as Array<{ label: string; saasModel?: string; saasKeySet?: boolean }>);

  $effect(() => {
    if (config) {
      saasLabel = config.saasLabel || '';
      apiFormat = config.saasApiFormat || 'openai';
      authType = config.saasAuthType || 'bearer';
      customHeader = config.saasCustomHeader || '';
      baseUrl = config.saasBaseUrl || '';
      model = config.saasModel || '';
      extraHeadersJson = JSON.stringify(config.saasExtraHeaders || {});
      if (config.strataSystem) strataSystem = config.strataSystem;
      if (config.saasSystem) saasSystem = config.saasSystem;
    }
  });

  $effect(() => {
    void authVersion;
    loadHistory(turns.length === 0);
    loadContext();
  });

  async function loadContext() {
    try {
      const res = await apiFetch<{ initialPrompt: string; files: string[] }>('/admin/arena/context');
      if (!contextLoaded && res.initialPrompt) {
        initialPrompt = res.initialPrompt;
      }
      const files = res.files || [];
      availableFiles = files;
      const nextMap: Record<string, boolean> = {};
      for (const f of files) {
        nextMap[f] = f in allowedMap ? allowedMap[f] : true;
      }
      allowedMap = nextMap;
      contextLoaded = true;
    } catch {
      // ignore when unauthenticated
    }
  }

  function setAllFiles(val: boolean) {
    const next: Record<string, boolean> = {};
    for (const f of availableFiles) next[f] = val;
    allowedMap = next;
  }

  async function loadHistory(autoSelectLatest = false) {
    try {
      const res = await apiFetch<{ matches: ArenaMatch[] }>('/admin/arena/history');
      history = res.matches || [];
      if (autoSelectLatest && history.length > 0 && turns.length === 0) {
        selectMatch(0);
      }
    } catch {
      // ignore when unauthenticated
    }
  }

  function selectMatch(idx: number) {
    selectedMatchIdx = idx;
    const m = history[idx];
    if (!m) return;
    topic = m.topic || topic;
    turns = m.transcript || [];
  }

  function applyFormatPreset() {
    if (apiFormat === 'anthropic') {
      if (!baseUrl || baseUrl.includes('openai')) baseUrl = 'https://api.anthropic.com/v1/messages';
      if (!model || model.startsWith('gpt')) model = 'claude-sonnet-4-5';
      authType = 'x-api-key';
    } else {
      if (!baseUrl || baseUrl.includes('anthropic')) baseUrl = 'https://api.openai.com/v1/chat/completions';
      if (!model || model.startsWith('claude')) model = 'gpt-4o-mini';
      authType = 'bearer';
    }
  }

  async function patchActiveConfig() {
    let parsedHeaders: Record<string, string> = {};
    try {
      parsedHeaders = JSON.parse(extraHeadersJson || '{}');
    } catch {
      errorMsg = '追加ヘッダ JSON が不正です';
      return false;
    }
    errorMsg = '';
    const body: Record<string, any> = {
      saasLabel: saasLabel.trim(),
      saasApiFormat: apiFormat,
      saasAuthType: authType,
      saasCustomHeader: customHeader,
      saasExtraHeaders: parsedHeaders,
      saasBaseUrl: baseUrl,
      saasModel: model,
      strataSystem,
      saasSystem
    };
    if (apiKey.trim()) {
      body.saasApiKey = apiKey.trim();
    }
    await apiFetch('/admin/config', {
      method: 'PATCH',
      body: JSON.stringify(body)
    });
    apiKey = '';
    return true;
  }

  async function saveSaasSettings() {
    if (!(await patchActiveConfig())) return;
    showSettings = false;
    onConfigSaved();
  }

  async function saveToSlot() {
    if (!(await patchActiveConfig())) return;
    await apiFetch('/admin/saas/presets', {
      method: 'POST',
      body: JSON.stringify({ label: saasLabel.trim() || model.trim() || 'default' })
    });
    onConfigSaved();
  }

  async function activateSlot(label: string) {
    if (!label) return;
    await apiFetch('/admin/saas/presets/activate', {
      method: 'POST',
      body: JSON.stringify({ label })
    });
    onConfigSaved();
  }

  async function deleteSlot(label: string) {
    if (!label) return;
    await apiFetch(`/admin/saas/presets?label=${encodeURIComponent(label)}`, {
      method: 'DELETE'
    });
    onConfigSaved();
  }

  function buildAdviceText(): string {
    const saasTurns = turns.filter((t) => t.speaker === 'saas');
    const last = saasTurns[saasTurns.length - 1] || turns[turns.length - 1];
    return last ? last.content : '';
  }

  async function injectToPi() {
    const text = buildAdviceText();
    if (!text) return;
    await apiFetch('/admin/arena/advice', {
      method: 'POST',
      body: JSON.stringify({ text })
    });
  }

  async function clearPendingAdvice() {
    await apiFetch('/admin/arena/advice', { method: 'DELETE' });
  }

  function newSession() {
    turns = [];
    selectedMatchIdx = -1;
    errorMsg = '';
  }

  async function runArena() {
    if (!topic.trim() || running) return;
    running = true;
    errorMsg = '';
    turns = [];
    try {
      const res = await apiFetch<{ transcript: ArenaTurn[] }>('/admin/arena/run', {
        method: 'POST',
        body: JSON.stringify({
          topic,
          turns: Number(numTurns),
          strataSystem,
          saasSystem,
          initialPrompt,
          allowedFiles,
          allowErrors
        })
      });
      turns = res.transcript || [];
      await loadHistory(false);
      selectedMatchIdx = 0;
    } catch (e: any) {
      errorMsg = e.message;
    } finally {
      running = false;
    }
  }
</script>

<section class="pane">
  <div class="pane-head">
    <span>AI vs AI</span>
    <div class="pane-controls">
      {#if history.length > 0}
        <select
          value={selectedMatchIdx}
          onchange={(e) => selectMatch(Number((e.currentTarget as HTMLSelectElement).value))}
          style="max-width:165px;"
          title="保存済み履歴から再生"
        >
          <option value={-1}>履歴 ({history.length})</option>
          {#each history as m, i}
            <option value={i}>{shortTime(m.ts)} · {m.topic}</option>
          {/each}
        </select>
      {/if}
      {#if presets.length > 0}
        <select
          value={saasLabel}
          onchange={(e) => activateSlot((e.currentTarget as HTMLSelectElement).value)}
          style="max-width:130px;"
          title="保存済み API スロット"
        >
          <option value="">スロット...</option>
          {#each presets as pr}
            <option value={pr.label}>{pr.label}</option>
          {/each}
        </select>
      {/if}
      <span class="tag {config?.saasKeySet ? 'ok' : 'warn'}">
        {saasLabel || apiFormat} · {model || '(未設定)'} · {config?.saasKeySet ? 'KEY SET' : 'NO KEY'}
      </span>
      <button class="ghost" onclick={() => { showContext = !showContext; if (showContext) loadContext(); }}>
        コンテキスト ({allowedFiles.length}/{availableFiles.length}) ▾
      </button>
      <button class="ghost" onclick={() => (showSettings = !showSettings)}>設定 ▾</button>
    </div>
  </div>

  {#if showContext}
    <div class="inline-drawer" style="max-height:280px; overflow:auto;">
      <div class="field" style="margin-bottom:8px;">
        <label>一番初めの Pi プロンプト（相手側 System Prompt に同梱）</label>
        <textarea rows="2" bind:value={initialPrompt} placeholder="(取得中 / 任意入力)" style="width:100%;"></textarea>
      </div>
      <div class="row" style="justify-content:space-between; margin-bottom:4px;">
        <label class="row" style="gap:5px; font-size:11.5px;">
          <input type="checkbox" bind:checked={allowErrors} />
          エラー参照を許可 (<code>get_recent_errors</code>)
        </label>
      </div>
      <div class="row" style="justify-content:space-between; margin-bottom:4px;">
        <span class="sub">共有ファイル（チェックされたファイルのみツリー表示＋read_file許可）</span>
        <div class="row">
          <button class="ghost" onclick={() => setAllFiles(true)}>全選択</button>
          <button class="ghost" onclick={() => setAllFiles(false)}>全解除</button>
          <button class="ghost" onclick={loadContext}>再読込</button>
        </div>
      </div>
      <div style="display:grid; grid-template-columns:repeat(auto-fill, minmax(180px, 1fr)); gap:4px; margin-bottom:8px;">
        {#each availableFiles as f}
          <label class="row" style="gap:5px; font-family:var(--mono); font-size:11px;">
            <input type="checkbox" checked={!!allowedMap[f]} onchange={(e) => (allowedMap = { ...allowedMap, [f]: (e.currentTarget as HTMLInputElement).checked })} />
            {f}
          </label>
        {/each}
      </div>
      <details class="thinking">
        <summary>相手側 System Prompt プレビュー</summary>
        <pre class="term" style="max-height:140px;">{saasSystemPreview}</pre>
      </details>
    </div>
  {/if}

  {#if showSettings}
    <div class="inline-drawer">
      <div class="form-grid">
        <div class="field" style="grid-column: 1 / -1;">
          <label>表示ラベル / スロット名 (例: Refuseless / Claude)</label>
          <input class="mono" bind:value={saasLabel} placeholder="任意のラベル名" style="width:100%;" />
        </div>
        <div class="field">
          <label>API 互換フォーマット</label>
          <select bind:value={apiFormat} style="width:100%;" onchange={applyFormatPreset}>
            <option value="anthropic">Anthropic 互換</option>
            <option value="openai">OpenAI 互換</option>
          </select>
        </div>
        <div class="field">
          <label>認証方式</label>
          <select bind:value={authType} style="width:100%;">
            <option value="x-api-key">x-api-key</option>
            <option value="bearer">Authorization: Bearer</option>
            <option value="custom">Custom Header</option>
            <option value="none">None</option>
          </select>
        </div>
        <div class="field">
          <label>Endpoint URL</label>
          <input class="mono" bind:value={baseUrl} placeholder="https://api.openai.com/v1/chat/completions" style="width:100%;" />
        </div>
        <div class="field">
          <label>Model</label>
          <input class="mono" bind:value={model} placeholder="claude-sonnet-4-5" style="width:100%;" />
        </div>
        <div class="field">
          <label>Credential (マスク済み・コンテナ非共有)</label>
          <input type="password" class="mono" bind:value={apiKey} placeholder="変更時のみ入力" style="width:100%;" />
        </div>
        {#if authType === 'custom'}
          <div class="field">
            <label>カスタムヘッダ名</label>
            <input class="mono" bind:value={customHeader} placeholder="X-Api-Token" style="width:100%;" />
          </div>
        {/if}
        <div class="field">
          <label>追加ヘッダ (JSON)</label>
          <input class="mono" bind:value={extraHeadersJson} style="width:100%;" />
        </div>
        <div class="field">
          <label>Strata System Prompt</label>
          <textarea rows="2" bind:value={strataSystem} style="width:100%;"></textarea>
        </div>
        <div class="field">
          <label>SaaS AI System Prompt</label>
          <textarea rows="2" bind:value={saasSystem} style="width:100%;"></textarea>
        </div>
      </div>
      <div class="row" style="justify-content:space-between; margin-top:8px;">
        <div class="row">
          <button class="ghost" onclick={saveToSlot}>スロットに保存</button>
          {#if presets.some((p) => p.label === saasLabel)}
            <button class="danger" onclick={() => deleteSlot(saasLabel)}>このスロットを削除</button>
          {/if}
        </div>
        <button onclick={saveSaasSettings}>設定を反映</button>
      </div>
    </div>
  {/if}

  <div class="pane-body" style="display:flex; flex-direction:column;">
    <div class="row" style="margin-bottom:8px;">
      <input bind:value={topic} style="flex:1;" placeholder="対話テーマ..." />
      <input type="number" bind:value={numTurns} min="2" max="12" style="width:54px;" />
      <button onclick={runArena} disabled={running}>{running ? '実行中...' : '開始'}</button>
      {#if turns.length > 0}
        <button class="ghost" onclick={injectToPi} title="次の Pi ターンに 1 回だけ自動注入">Pi に注入</button>
        <button class="ghost" onclick={newSession}>新規</button>
      {/if}
    </div>
    {#if pendingAdvice}
      <div class="row" style="justify-content:space-between; margin-bottom:6px; padding:4px 8px; background:#0b101b; border:1px solid var(--accent); border-radius:4px;">
        <span class="sub" style="color:var(--accent);">Pi 注入待機中（次の /v1/chat/completions に自動挿入されます）</span>
        <button class="ghost" onclick={clearPendingAdvice}>取消</button>
      </div>
    {/if}
    {#if errorMsg}
      <div class="msg err" style="margin-bottom:6px;">{errorMsg}</div>
    {/if}
    <div class="stream-box">
      {#if turns.length === 0}
        <div class="sub">トピックを指定して「開始」を押すと、1 ターン完了ごとに SSE で即時追記されます。</div>
      {:else}
        {#each turns as t}
          <div class="msg {t.speaker === 'strata' ? 'strata' : 'saas'}">
            <div class="mhead">TURN {t.turn} · {t.speaker.toUpperCase()} ({t.model})</div>
            {#if (t.filesRead && t.filesRead.length > 0) || t.errorsRead}
              <div class="sub" style="margin-bottom:4px;">
                {#if t.filesRead && t.filesRead.length > 0}
                  <span class="tag">read_file</span> {t.filesRead.join(', ')}
                {/if}
                {#if t.errorsRead}
                  <span class="tag warn">get_recent_errors</span>
                {/if}
              </div>
            {/if}
            <div class="md">{@html renderMarkdown(t.content)}</div>
          </div>
        {/each}
      {/if}
    </div>
  </div>
</section>
