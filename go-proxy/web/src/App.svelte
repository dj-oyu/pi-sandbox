<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { apiFetch, getToken, setToken, withTokenQuery } from './lib/api';
  import RuntimeMonitorPane from './components/RuntimeMonitorPane.svelte';
  import PiConsolePane from './components/PiConsolePane.svelte';
  import LogsEgressPane, { type LogItem } from './components/LogsEgressPane.svelte';
  import AiVsAiPane, { type ArenaTurn } from './components/AiVsAiPane.svelte';
  import FilesView from './components/FilesView.svelte';
  import McpView from './components/McpView.svelte';

  let activeView = $state<'cockpit' | 'files' | 'mcp'>('cockpit');
  let tokenInput = $state(getToken());
  let sseStatus = $state<'connected' | 'connecting' | 'error'>('connecting');
  let strataSpeed = $state<number | null>(null);

  let config = $state<any>(null);
  let targets = $state<any[]>([]);
  let jobs = $state<any[]>([]);
  let logs = $state<LogItem[]>([]);
  let arenaTurns = $state<ArenaTurn[]>([]);

  let autoHeal = $state(true);
  let healthIntervalSec = $state(10);
  let strataConcurrency = $state(1);
  let authVersion = $state(0);
  let pendingAdvice = $state('');

  let es: EventSource | null = null;

  async function loadInitialState() {
    try {
      const [cfg, healthRes, jobsRes, logsRes, advRes] = await Promise.all([
        apiFetch('/admin/config'),
        apiFetch('/admin/health'),
        apiFetch('/admin/jobs'),
        apiFetch('/admin/logs/recent?stream=all&tail=80'),
        apiFetch<{ pending: string }>('/admin/arena/advice')
      ]);
      config = cfg;
      autoHeal = !!cfg.autoHealEnabled;
      healthIntervalSec = cfg.healthIntervalSec || 10;
      strataConcurrency = cfg.strataConcurrency || 1;
      targets = healthRes.targets || [];
      jobs = jobsRes.jobs || [];
      logs = logsRes.items || [];
      pendingAdvice = advRes?.pending || '';
    } catch {
      // Wait for user to enter PI_PROXY_TOKEN
    }
  }

  function connectSSE() {
    es?.close();
    sseStatus = 'connecting';
    const nextEs = new EventSource(withTokenQuery('/admin/events'));
    es = nextEs;

    nextEs.addEventListener('ready', () => {
      sseStatus = 'connected';
    });
    nextEs.onerror = () => {
      sseStatus = 'error';
    };
    nextEs.addEventListener('log', (e: MessageEvent) => {
      try {
        const ev = JSON.parse(e.data);
        if (ev?.data) {
          logs = [...logs, ev.data].slice(-400);
        }
      } catch {}
    });
    nextEs.addEventListener('health', (e: MessageEvent) => {
      try {
        const ev = JSON.parse(e.data);
        if (ev?.data?.targets) targets = ev.data.targets;
      } catch {}
    });
    nextEs.addEventListener('job', (e: MessageEvent) => {
      try {
        const ev = JSON.parse(e.data);
        const j = ev?.data;
        if (!j?.id) return;
        const idx = jobs.findIndex((x) => x.id === j.id);
        if (idx >= 0) {
          const next = [...jobs];
          next[idx] = j;
          jobs = next;
        } else {
          jobs = [j, ...jobs].slice(0, 60);
        }
      } catch {}
    });
    nextEs.addEventListener('strata_speed', (e: MessageEvent) => {
      try {
        const ev = JSON.parse(e.data);
        if (typeof ev?.data?.tokPerSec === 'number') {
          strataSpeed = ev.data.tokPerSec;
        }
      } catch {}
    });
    nextEs.addEventListener('arena_turn', (e: MessageEvent) => {
      try {
        const ev = JSON.parse(e.data);
        const t = ev?.data?.turn;
        if (t && !arenaTurns.some((x) => x.turn === t.turn)) {
          arenaTurns = [...arenaTurns, t];
        }
      } catch {}
    });
    nextEs.addEventListener('advice', (e: MessageEvent) => {
      try {
        const ev = JSON.parse(e.data);
        if (typeof ev?.data?.pending === 'string') {
          pendingAdvice = ev.data.pending;
        }
      } catch {}
    });
  }

  function applyToken() {
    setToken(tokenInput.trim());
    authVersion += 1;
    loadInitialState();
    connectSSE();
  }

  onMount(() => {
    const params = new URLSearchParams(window.location.search);
    const urlTok = params.get('token');
    if (urlTok) {
      tokenInput = urlTok;
      setToken(urlTok);
      authVersion += 1;
      params.delete('token');
      const qs = params.toString();
      history.replaceState(null, '', window.location.pathname + (qs ? `?${qs}` : ''));
    }
    loadInitialState();
    connectSSE();
  });

  onDestroy(() => {
    es?.close();
  });
</script>

<header class="topbar">
  <div class="topbar-left">
    <span class="brand">pi-proxy Console</span>
    <div class="view-switch">
      <button class={activeView === 'cockpit' ? 'active' : ''} onclick={() => (activeView = 'cockpit')}>Cockpit</button>
      <button class={activeView === 'files' ? 'active' : ''} onclick={() => (activeView = 'files')}>Files</button>
      <button class={activeView === 'mcp' ? 'active' : ''} onclick={() => (activeView = 'mcp')}>MCP</button>
    </div>
    <span class="stat-inline">
      <span class="dot {sseStatus === 'connected' ? 'ok' : sseStatus === 'connecting' ? 'warn' : 'err'}"></span>
      SSE /admin/events
    </span>
    <span class="stat-inline">
      <span class="dot ok"></span>sync-strata :{config?.strataPort || 8080}
    </span>
    <span class="stat-inline">
      Strata <strong style="color:var(--accent);">{strataSpeed !== null ? `${strataSpeed.toFixed(1)} tok/s` : '-- tok/s'}</strong>
    </span>
    {#if config?.awsEmuPort}
      <span class="stat-inline"><span class="dot ok"></span>aws-emulator :{config.awsEmuPort}</span>
    {/if}
    <span class="stat-inline">Egress :{config?.egressPort || 18888}</span>
  </div>
  <div class="topbar-right">
    <input
      class="mono"
      type="password"
      bind:value={tokenInput}
      placeholder="PI_PROXY_TOKEN"
      style="width:160px;"
      onkeydown={(e) => e.key === 'Enter' && applyToken()}
    />
    <button class="ghost" onclick={applyToken}>接続</button>
  </div>
</header>

{#if activeView === 'cockpit'}
  <div class="cockpit">
    <RuntimeMonitorPane
      {targets}
      {jobs}
      bind:autoHeal
      bind:healthIntervalSec
      onConfigSaved={loadInitialState}
    />
    <PiConsolePane
      bind:strataConcurrency
      {authVersion}
      onConfigSaved={loadInitialState}
    />
    <LogsEgressPane
      bind:logs
      allowedHosts={config?.allowedHosts || []}
      onConfigSaved={loadInitialState}
    />
    <AiVsAiPane
      bind:turns={arenaTurns}
      {config}
      {authVersion}
      {pendingAdvice}
      onConfigSaved={loadInitialState}
    />
  </div>
{:else if activeView === 'files'}
  <FilesView />
{:else}
  <McpView />
{/if}
