<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { apiFetch, shortTime } from '../lib/api';

  interface DreamEvent {
    ts: string;
    event: string;
    turn?: number;
    scoreBefore?: number;
    scoreNow?: number;
    pickedIds?: string[];
    pickedHeadings?: string[];
    reason?: string;
  }

  interface DreamStatus {
    hasData: boolean;
    runId: string;
    enabled: boolean;
    intervalMin: number;
    recallProb: number;
    startedAt: string;
    lastEventAt: string;
    lastWriteAt: string;
    nextWriteAt: string;
    writes: number;
    recalls: number;
    followThroughs: number;
    guardSkips: number;
    postRecallLifts: number;
    latestScore?: number;
    targetMet: boolean;
    memory: {
      exists: boolean;
      path: string;
      bytes: number;
      totalEntries: number;
      tiers: Record<string, number>;
      bloatWarning: boolean;
    };
    recentEvents: DreamEvent[];
  }

  interface HealthTarget {
    name: string;
    healthy: boolean;
    consecutiveFailures: number;
    healCount: number;
    lastCheck: string;
    lastError?: string;
  }

  interface Job {
    id: string;
    kind: string;
    command: string;
    status: string;
    exitCode: number;
    durationMs: number;
    stdout?: string;
    stderr?: string;
  }

  interface QueueItem {
    id: string;
    source: string;
    model?: string;
    status: string;
    queuedAt: string;
    startedAt?: string;
  }

  let {
    targets = [],
    jobs = [],
    strataQueue = [],
    autoHeal = $bindable(true),
    healthIntervalSec = $bindable(10),
    onConfigSaved
  }: {
    targets: HealthTarget[];
    jobs: Job[];
    strataQueue?: QueueItem[];
    autoHeal: boolean;
    healthIntervalSec: number;
    onConfigSaved: () => void;
  } = $props();

  let selectedJobId = $state('');
  let saving = $state(false);
  let dream = $state<DreamStatus | null>(null);
  let dreamTimer: ReturnType<typeof setInterval> | null = null;

  async function loadDream() {
    try {
      dream = await apiFetch<DreamStatus>('/admin/dream/status');
    } catch {
      // ignore transient errors
    }
  }

  function formatBytes(n: number): string {
    if (n >= 1024) return `${(n / 1024).toFixed(1)} KB`;
    return `${n} B`;
  }

  onMount(() => {
    loadDream();
    dreamTimer = setInterval(loadDream, 5000);
  });

  onDestroy(() => {
    if (dreamTimer) clearInterval(dreamTimer);
  });

  const activeJob = $derived(
    jobs.find((j) => j.id === selectedJobId) || jobs[0] || null
  );

  async function saveHealthConfig() {
    saving = true;
    try {
      await apiFetch('/admin/config', {
        method: 'PATCH',
        body: JSON.stringify({
          autoHealEnabled: autoHeal,
          healthIntervalSec: Number(healthIntervalSec)
        })
      });
      onConfigSaved();
    } finally {
      saving = false;
    }
  }
</script>

<section class="pane">
  <div class="pane-head">
    <span>Runtime Monitor（死活 ＆ 並列ジョブ）</span>
    <div class="pane-controls">
      <label class="row" style="gap:4px;">
        <input type="checkbox" bind:checked={autoHeal} /> Auto-Heal
      </label>
      <span>間隔 <input type="number" bind:value={healthIntervalSec} min="2" max="300" style="width:52px;" />s</span>
      <button class="ghost" onclick={saveHealthConfig} disabled={saving}>適用</button>
    </div>
  </div>
  <div class="pane-body">
    <div class="sec-label" style="display:flex; justify-content:space-between; align-items:center;">
      <span>DREAM HARNESS</span>
      {#if dream?.hasData}
        <span class="sub" style="font-weight:400;">
          {dream.runId}{#if dream.enabled && dream.nextWriteAt} · Next {shortTime(dream.nextWriteAt)}{/if}
        </span>
      {/if}
    </div>
    <table class="fixed-cols" style="margin-bottom: 8px;">
      <colgroup>
        <col style="width:18%;" /><col style="width:16%;" /><col style="width:24%;" /><col style="width:24%;" /><col style="width:18%;" />
      </colgroup>
      <thead>
        <tr><th>State</th><th>Writes</th><th>Recalls (Follow)</th><th>Memory</th><th>Score / Lift</th></tr>
      </thead>
      <tbody>
        {#if !dream || !dream.hasData}
          <tr><td colspan="5" class="sub">Dream ログなし（-EnableDream で有効化）</td></tr>
        {:else}
          <tr>
            <td>
              <span class="tag {dream.enabled ? 'ok' : ''}">
                {dream.enabled ? `ON ${dream.intervalMin}m` : 'OFF'}
              </span>
            </td>
            <td class="mono">{dream.writes}</td>
            <td class="mono">{dream.recalls} <span class="sub">(follow {dream.followThroughs})</span></td>
            <td class="mono">
              <span class={dream.memory.bloatWarning ? 'tag warn' : ''}>
                {dream.memory.totalEntries}件 / {formatBytes(dream.memory.bytes)}
              </span>
            </td>
            <td class="mono">
              {dream.latestScore ?? '-'}
              {#if dream.postRecallLifts > 0}
                <span class="tag ok">+{dream.postRecallLifts} lift</span>
              {/if}
            </td>
          </tr>
        {/if}
      </tbody>
    </table>
    {#if dream?.recentEvents && dream.recentEvents.length > 0}
      <div class="sub" style="margin: -4px 0 8px; padding: 4px 8px; background: #0b111e; border: 1px solid #1b2538; border-radius: 3px;">
        {#each dream.recentEvents.slice(-3).reverse() as ev}
          <div style="white-space:nowrap; overflow:hidden; text-overflow:ellipsis;">
            <code>{shortTime(ev.ts)}</code>
            <span class="tag {ev.event === 'dream_recall' ? 'ok' : ev.event === 'dream_write' ? 'warn' : ''}" style="margin:0 4px;">{ev.event}</span>
            {#if ev.pickedHeadings && ev.pickedHeadings.length > 0}
              <span>{ev.pickedHeadings.join(' / ')}</span>
            {:else if ev.reason}
              <span>{ev.reason}</span>
            {:else if ev.turn}
              <span>turn {ev.turn}</span>
            {/if}
          </div>
        {/each}
      </div>
    {/if}

    <div class="sec-label">SIDECARS</div>
    <table class="fixed-cols" style="margin-bottom: 8px;">
      <colgroup>
        <col style="width:24%;" /><col style="width:18%;" /><col style="width:24%;" /><col style="width:17%;" /><col style="width:17%;" />
      </colgroup>
      <thead>
        <tr><th>Target</th><th>State</th><th>Fails</th><th>Heals</th><th>Last Check</th></tr>
      </thead>
      <tbody>
        {#if targets.length === 0}
          <tr><td colspan="5" class="sub">登録済みターゲットなし（aws-emulator 未起動時は非表示）</td></tr>
        {:else}
          {#each targets as t}
            <tr>
              <td><code>{t.name}</code></td>
              <td>
                <span class="tag {t.healthy ? 'ok' : 'err'}">
                  {t.healthy ? 'HEALTHY' : 'UNHEALTHY'}
                </span>
              </td>
              <td>{t.consecutiveFailures}</td>
              <td>{t.healCount}</td>
              <td class="sub">{shortTime(t.lastCheck)}</td>
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>

    <div class="sec-label">STRATA GPU QUEUE</div>
    <table class="fixed-cols" style="margin-bottom: 8px;">
      <colgroup>
        <col style="width:24%;" /><col style="width:18%;" /><col style="width:24%;" /><col style="width:17%;" /><col style="width:17%;" />
      </colgroup>
      <thead>
        <tr><th>ID</th><th>Source</th><th>Status</th><th>Queued</th><th>Started</th></tr>
      </thead>
      <tbody>
        {#if strataQueue.length === 0}
          <tr><td colspan="5" class="sub">GPU キュー空（待機・推論中なし）</td></tr>
        {:else}
          {#each strataQueue as q}
            <tr>
              <td><code>{q.id}</code></td>
              <td><code>{q.source}</code></td>
              <td>
                <span class="tag {q.status === 'running' ? 'ok' : 'warn'}">{q.status.toUpperCase()}</span>
              </td>
              <td class="sub">{shortTime(q.queuedAt)}</td>
              <td class="sub">{q.startedAt ? shortTime(q.startedAt) : '-'}</td>
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>

    <div class="sec-label">PARALLEL JOBS</div>
    <table class="fixed-cols">
      <colgroup>
        <col style="width:24%;" /><col style="width:18%;" /><col style="width:24%;" /><col style="width:17%;" /><col style="width:17%;" />
      </colgroup>
      <thead>
        <tr><th>Job ID</th><th>Kind</th><th>Command</th><th>Status</th><th>Time</th></tr>
      </thead>
      <tbody>
        {#if jobs.length === 0}
          <tr><td colspan="5" class="sub">ジョブ履歴なし</td></tr>
        {:else}
          {#each jobs.slice(0, 30) as j}
            <tr onclick={() => (selectedJobId = j.id)} style="cursor:pointer;">
              <td><code>{j.id}</code></td>
              <td>{j.kind}</td>
              <td><code>{j.command}</code></td>
              <td>
                <span class="tag {j.status === 'completed' ? 'ok' : j.status === 'running' || j.status === 'queued' ? 'warn' : 'err'}">
                  {j.status === 'completed' ? `EXIT ${j.exitCode}` : j.status.toUpperCase()}
                </span>
              </td>
              <td class="sub">{j.durationMs}ms</td>
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>

    {#if activeJob}
      <pre class="term">[{activeJob.id} stdout]
{activeJob.stdout || '(empty)'}{activeJob.stderr ? `\n[stderr]\n${activeJob.stderr}` : ''}</pre>
    {/if}
  </div>
</section>
