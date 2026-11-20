<script lang="ts">
  import { apiFetch, shortTime, withTokenQuery } from '../lib/api';

  export interface LogItem {
    stream: string;
    record: Record<string, any>;
  }

  let {
    logs = $bindable([]),
    allowedHosts = [],
    onConfigSaved
  }: {
    logs: LogItem[];
    allowedHosts: string[];
    onConfigSaved: () => void;
  } = $props();

  let streamFilter = $state('all');
  let textFilter = $state('');
  let tailLimit = $state(80);
  let showAllowlist = $state(false);
  let allowlistText = $state('');
  let savingAllow = $state(false);
  let bodyEl: HTMLDivElement | undefined;
  let initialScrolled = $state(false);

  $effect(() => {
    allowlistText = allowedHosts.join('\n');
  });

  $effect(() => {
    void logs.length;
    if (!bodyEl) return;
    if (!initialScrolled && logs.length > 0) {
      initialScrolled = true;
      queueMicrotask(() => {
        if (bodyEl) bodyEl.scrollTop = bodyEl.scrollHeight;
      });
      return;
    }
    const nearBottom = bodyEl.scrollHeight - bodyEl.scrollTop - bodyEl.clientHeight < 80;
    if (nearBottom) {
      queueMicrotask(() => {
        if (bodyEl) bodyEl.scrollTop = bodyEl.scrollHeight;
      });
    }
  });

  const filteredLogs = $derived(
    [...logs]
      .sort((a, b) => String(a.record.ts || '').localeCompare(String(b.record.ts || '')))
      .filter((x) => {
        if (streamFilter !== 'all' && x.stream !== streamFilter) return false;
        if (!textFilter.trim()) return true;
        const q = textFilter.toLowerCase();
        return JSON.stringify(x.record).toLowerCase().includes(q);
      })
  );

  async function loadMoreLogs() {
    const prevHeight = bodyEl?.scrollHeight || 0;
    tailLimit = Math.min(tailLimit + 100, 500);
    const res = await apiFetch<{ items: LogItem[] }>(
      `/admin/logs/recent?stream=${encodeURIComponent(streamFilter)}&tail=${tailLimit}`
    );
    logs = res.items || [];
    queueMicrotask(() => {
      if (bodyEl) {
        bodyEl.scrollTop = bodyEl.scrollHeight - prevHeight;
      }
    });
  }

  async function saveAllowlist() {
    savingAllow = true;
    try {
      const hosts = allowlistText
        .split('\n')
        .map((s) => s.trim())
        .filter(Boolean);
      await apiFetch('/admin/config', {
        method: 'PATCH',
        body: JSON.stringify({ allowedHosts: hosts })
      });
      showAllowlist = false;
      onConfigSaved();
    } finally {
      savingAllow = false;
    }
  }

  function formatDetail(rec: Record<string, any>): string {
    const copy: Record<string, any> = { ...rec };
    delete copy.ts;
    delete copy.event;
    return JSON.stringify(copy);
  }
</script>

<section class="pane">
  <div class="pane-head">
    <span>Logs ＆ Egress</span>
    <div class="pane-controls">
      <select bind:value={streamFilter}>
        <option value="all">all streams</option>
        <option value="api-audit">api-audit.bin</option>
        <option value="egress">egress.bin</option>
        <option value="control">control.bin</option>
      </select>
      <input bind:value={textFilter} placeholder="filter..." style="width:110px;" />
      <button class="ghost" onclick={() => (showAllowlist = !showAllowlist)}>
        許可ドメイン ({allowedHosts.length}) ▾
      </button>
      <a
        href={withTokenQuery(`/admin/logs/export?stream=${streamFilter === 'all' ? 'api-audit' : streamFilter}&format=jsonl&tail=${tailLimit}`)}
        target="_blank"
        rel="noreferrer"
      >
        <button class="ghost" type="button">JSONL</button>
      </a>
    </div>
  </div>

  {#if showAllowlist}
    <div class="inline-drawer">
      <div class="row" style="justify-content:space-between; margin-bottom:4px;">
        <span class="sub">allowedHosts（1行1ドメイン · Egress :18888 に即時反映）</span>
        <button onclick={saveAllowlist} disabled={savingAllow}>保存して閉じる</button>
      </div>
      <textarea class="mono" rows="4" style="width:100%;" bind:value={allowlistText}></textarea>
    </div>
  {/if}

  <div class="pane-body" bind:this={bodyEl}>
    {#if tailLimit < 500}
      <div class="row" style="justify-content:center; margin-bottom:6px;">
        <button class="ghost" onclick={loadMoreLogs}>過去のログを読み込む</button>
      </div>
    {/if}
    <table>
      <thead>
        <tr><th>Time</th><th>Stream</th><th>Event</th><th>Detail</th></tr>
      </thead>
      <tbody>
        {#each filteredLogs as item}
          {@const ev = String(item.record.event || '')}
          <tr>
            <td class="sub">{shortTime(item.record.ts)}</td>
            <td><code>{item.stream}</code></td>
            <td>
              <span class="tag {ev.includes('block') || ev.includes('err') || ev.includes('unhealthy') ? 'err' : ev.includes('patch') || ev.includes('heal') ? 'warn' : 'ok'}">
                {ev}
              </span>
            </td>
            <td class="sub" style="word-break:break-all;">{formatDetail(item.record)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</section>
