<script lang="ts">
  import { apiFetch, shortTime } from '../lib/api';

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
