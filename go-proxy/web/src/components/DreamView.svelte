<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
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

  interface MemoryEntry {
    id: string;
    tier: string;
    title: string;
    body: string;
    recallCount: number;
  }

  interface VerifyPoint {
    ts: string;
    score: number;
    targetMet: boolean;
  }

  interface JudgeResult {
    available: boolean;
    model?: string;
    verdict?: string;
    summary?: string;
    recalls?: Array<{ turn: number; status: string; reason: string }>;
    memory?: Array<{ id: string; action: string; reason: string }>;
    error?: string;
  }

  interface EvalSummary {
    ts: string;
    runId: string;
    dreamEnabled: boolean;
    hasVerifier: boolean;
    totalTurns: number;
    writes: number;
    recalls: number;
    overheadRatio: number;
    finalScore?: number;
    targetMet: boolean;
    verdict: string;
    reasons?: string[];
    judge?: JudgeResult | null;
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
      entries: MemoryEntry[];
      bloatWarning: boolean;
    };
    recentEvents: DreamEvent[];
    verifyPoints: VerifyPoint[];
    latestEval?: EvalSummary | null;
    recentEvals: EvalSummary[];
  }

  let data = $state<DreamStatus | null>(null);
  let activeTier = $state('ALL');
  let timer: ReturnType<typeof setInterval> | null = null;

  async function refresh() {
    try {
      data = await apiFetch<DreamStatus>('/admin/dream/status');
    } catch {
      // ignore transient errors
    }
  }

  function formatBytes(n: number): string {
    return n >= 1024 ? `${(n / 1024).toFixed(1)} KB` : `${n} B`;
  }

  function verdictClass(v?: string): string {
    if (v === 'HELPFUL' || v === 'USED' || v === 'KEEP') return 'ok';
    if (v === 'HARMFUL' || v === 'NOISY' || v === 'DISTRACTED' || v === 'PRUNE') return 'err';
    return 'warn';
  }

  const judgeMemMap = $derived.by(() => {
    const map = new Map<string, { action: string; reason: string }>();
    for (const m of data?.latestEval?.judge?.memory || []) {
      map.set(m.id, { action: m.action, reason: m.reason });
    }
    return map;
  });

  const judgeRecallMap = $derived.by(() => {
    const map = new Map<number, { status: string; reason: string }>();
    for (const r of data?.latestEval?.judge?.recalls || []) {
      map.set(r.turn, { status: r.status, reason: r.reason });
    }
    return map;
  });

  const filteredEntries = $derived.by(() => {
    const all = data?.memory?.entries || [];
    if (activeTier === 'ALL') return all;
    return all.filter((e) => e.tier.toLowerCase() === activeTier.toLowerCase());
  });

  const svgPolyline = $derived.by(() => {
    const pts = data?.verifyPoints || [];
    if (pts.length === 0) return '';
    const n = pts.length;
    return pts
      .map((p, i) => {
        const x = n === 1 ? 380 : 48 + (i / (n - 1)) * 672;
        const y = 136 - (Math.max(0, Math.min(100, p.score)) / 100) * 116;
        return `${x.toFixed(1)},${y.toFixed(1)}`;
      })
      .join(' ');
  });

  onMount(() => {
    refresh();
    timer = setInterval(refresh, 5000);
  });

  onDestroy(() => {
    if (timer) clearInterval(timer);
  });
</script>

<div class="mcp-view" style="overflow:auto; padding:12px 14px; gap:12px;">
  <!-- Top Status & Qwen Judge -->
  <section class="pane" style="border:1px solid var(--border); border-radius:4px; overflow:visible;">
    <div class="pane-head">
      <div class="row" style="gap:8px;">
        <span>Dream Harness</span>
        {#if data?.hasData}
          <span class="tag {data.enabled ? 'ok' : ''}">{data.enabled ? `ON · ${data.intervalMin}m` : 'OFF'}</span>
          <span class="sub">{data.runId}</span>
          {#if data.enabled && data.nextWriteAt}
            <span class="sub">Next Write: {shortTime(data.nextWriteAt)}</span>
          {/if}
        {/if}
      </div>
      <div class="pane-controls">
        {#if data?.latestEval?.verdict}
          <span class="tag {verdictClass(data.latestEval.verdict)}">VERDICT: {data.latestEval.verdict}</span>
        {/if}
        <button class="ghost" onclick={refresh}>更新</button>
      </div>
    </div>

    <div class="pane-body" style="overflow:visible;">
      {#if !data || !data.hasData}
        <div class="sub">Dream ログがまだありません。<code>run-pi.ps1 -EnableDream</code> で実行するとここに表示されます。</div>
      {:else}
        <div class="row" style="gap:16px; margin-bottom:10px;">
          <div><div class="sub">Writes</div><div class="mono" style="font-size:15px; font-weight:700;">{data.writes}</div></div>
          <div><div class="sub">Recalls</div><div class="mono" style="font-size:15px; font-weight:700;">{data.recalls}</div></div>
          <div>
            <div class="sub">Memory</div>
            <div class="mono" style="font-size:15px; font-weight:700;">
              {data.memory.totalEntries}件 <span class="sub">({formatBytes(data.memory.bytes)})</span>
              {#if data.memory.bloatWarning}<span class="tag err" style="margin-left:4px;">BLOAT</span>{/if}
            </div>
          </div>
          {#if data.verifyPoints.length > 0 || data.latestScore !== undefined}
            <div>
              <div class="sub">Verifier Score</div>
              <div class="mono" style="font-size:15px; font-weight:700;">
                {data.latestScore ?? '-'}
                {#if data.postRecallLifts > 0}<span class="tag ok" style="margin-left:4px;">+{data.postRecallLifts} lift</span>{/if}
              </div>
            </div>
          {/if}
        </div>

        {#if data.latestEval?.judge?.available}
          <div style="background:#0b111e; border:1px solid var(--border); border-left:3px solid var(--accent); border-radius:4px; padding:8px 10px;">
            <div class="row" style="justify-content:space-between; margin-bottom:4px;">
              <span class="mono" style="font-weight:700; color:var(--accent);">QWEN 1-SHOT JUDGE</span>
              <span class="sub">{data.latestEval.judge.model || 'local-qwen'}</span>
            </div>
            <div style="font-size:12.5px;">
              <span class="tag {verdictClass(data.latestEval.judge.verdict)}" style="margin-right:6px;">
                {data.latestEval.judge.verdict}
              </span>
              {data.latestEval.judge.summary}
            </div>
          </div>
        {:else}
          <div class="sub">Qwen Judge: タスク終了時に <code>eval-dream.mjs</code> が自動で 1-shot 判定を記録します。</div>
        {/if}
      {/if}
    </div>
  </section>

  <!-- Optional Verifier Score Chart + Dream Event Log -->
  <section class="pane" style="border:1px solid var(--border); border-radius:4px; overflow:visible;">
    <div class="pane-head">
      <span>Dream Timeline & Verifier</span>
      <span class="sub">
        {data?.verifyPoints?.length ? `Verifier points: ${data.verifyPoints.length}` : '汎用モード（verifier.json なし）'}
      </span>
    </div>
    <div class="pane-body" style="overflow:visible;">
      {#if data?.verifyPoints && data.verifyPoints.length > 0}
        <div style="overflow-x:auto; margin-bottom:10px;">
          <svg viewBox="0 0 760 154" style="width:100%; min-width:440px; max-height:170px;">
            <g stroke="#243047" stroke-width="1" stroke-dasharray="3 3">
              <line x1="48" y1="20" x2="720" y2="20" />
              <line x1="48" y1="78" x2="720" y2="78" />
              <line x1="48" y1="136" x2="720" y2="136" />
            </g>
            <g fill="#8494ad" font-size="11" font-family="ui-monospace, monospace" text-anchor="end">
              <text x="40" y="24">100</text>
              <text x="40" y="82">50</text>
              <text x="40" y="140">0</text>
            </g>
            <polyline fill="none" stroke="#38bdf8" stroke-width="2.5" stroke-linejoin="round" points={svgPolyline} />
            {#each data.verifyPoints as p, i}
              {@const x = data.verifyPoints.length === 1 ? 380 : 48 + (i / (data.verifyPoints.length - 1)) * 672}
              {@const y = 136 - (Math.max(0, Math.min(100, p.score)) / 100) * 116}
              <circle cx={x} cy={y} r="3.5" fill={p.targetMet ? '#22c55e' : '#38bdf8'} />
            {/each}
          </svg>
        </div>
      {/if}

      <div class="sec-label">DREAM EVENTS</div>
      {#if !data?.recentEvents || data.recentEvents.length === 0}
        <div class="sub">まだイベントがありません（次回チェックポイント待機中）。</div>
      {:else}
        <table>
          <thead>
            <tr><th style="width:76px;">Time</th><th style="width:120px;">Event</th><th style="width:60px;">Turn</th><th>Detail / Qwen Judge</th></tr>
          </thead>
          <tbody>
            {#each [...data.recentEvents].reverse() as ev}
              {@const jr = ev.turn ? judgeRecallMap.get(ev.turn) : undefined}
              <tr>
                <td class="sub">{shortTime(ev.ts)}</td>
                <td>
                  <span class="tag {ev.event === 'dream_recall' ? 'ok' : ev.event === 'dream_write' ? 'warn' : ''}">
                    {ev.event}
                  </span>
                </td>
                <td class="mono">{ev.turn ?? '-'}</td>
                <td>
                  {#if ev.pickedHeadings && ev.pickedHeadings.length > 0}
                    <div class="mono" style="font-size:11px;">{ev.pickedHeadings.join(' / ')}</div>
                  {:else if ev.reason}
                    <span class="sub">{ev.reason}</span>
                  {:else}
                    <span class="sub">checkpoint recorded</span>
                  {/if}
                  {#if jr}
                    <div style="margin-top:2px;">
                      <span class="tag {verdictClass(jr.status)}">{jr.status}</span>
                      <span class="sub" style="margin-left:4px;">{jr.reason}</span>
                    </div>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </div>
  </section>

  <!-- Memory Bank + Recent Evaluations -->
  <div class="form-grid" style="align-items:start;">
    <section class="pane" style="border:1px solid var(--border); border-radius:4px; overflow:visible;">
      <div class="pane-head">
        <span>Memory Bank (<code>{data?.memory?.path || '.pi/memory/dream.md'}</code>)</span>
        <div class="pane-controls">
          {#each ['ALL', 'Principle', 'Insight', 'Dead End', 'Note'] as t}
            <button
              class={activeTier === t ? '' : 'ghost'}
              style="padding:2px 6px; font-size:11px;"
              onclick={() => (activeTier = t)}
            >{t}</button>
          {/each}
        </div>
      </div>
      <div class="pane-body" style="overflow:visible;">
        {#if filteredEntries.length === 0}
          <div class="sub">該当するメモリエントリがありません。</div>
        {:else}
          <div style="display:flex; flex-direction:column; gap:8px;">
            {#each filteredEntries as m}
              {@const jm = judgeMemMap.get(m.id)}
              <div style="background:#0b111e; border:1px solid var(--border); border-radius:4px; padding:8px 10px;">
                <div class="row" style="justify-content:space-between; margin-bottom:4px;">
                  <div class="row" style="gap:6px;">
                    <span class="tag {m.tier === 'Dead End' ? 'err' : m.tier === 'Principle' ? 'ok' : ''}">{m.tier}</span>
                    <strong class="mono">{m.id}</strong>
                    <span>{m.title}</span>
                  </div>
                  <div class="row" style="gap:6px;">
                    <span class="sub">recalled ×{m.recallCount}</span>
                    {#if jm}
                      <span class="tag {verdictClass(jm.action)}">{jm.action}</span>
                    {/if}
                  </div>
                </div>
                <div class="sub" style="white-space:normal; line-height:1.45;">{m.body}</div>
                {#if jm}
                  <div class="sub" style="margin-top:4px; color:var(--accent);">Judge: {jm.reason}</div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </section>

    <section class="pane" style="border:1px solid var(--border); border-radius:4px; overflow:visible;">
      <div class="pane-head">
        <span>Recent Evaluations (<code>dream-evals.jsonl</code>)</span>
      </div>
      <div class="pane-body" style="overflow:visible;">
        {#if !data?.recentEvals || data.recentEvals.length === 0}
          <div class="sub">評価履歴がまだありません。</div>
        {:else}
          <table>
            <thead>
              <tr><th>Run</th><th>Verdict</th><th>Score</th><th>Summary</th></tr>
            </thead>
            <tbody>
              {#each data.recentEvals as ev}
                <tr>
                  <td class="mono" style="font-size:11px;">{ev.runId.replace(/^run-/, '')}</td>
                  <td><span class="tag {verdictClass(ev.verdict)}">{ev.verdict}</span></td>
                  <td class="mono">{ev.hasVerifier ? (ev.finalScore ?? '-') : '-'}</td>
                  <td class="sub" style="white-space:normal;">
                    {ev.judge?.available ? ev.judge.summary : (ev.reasons || []).join('; ')}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>
    </section>
  </div>
</div>
