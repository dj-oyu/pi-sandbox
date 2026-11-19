<script lang="ts">
  import { onMount } from 'svelte';
  import { apiFetch } from '../lib/api';

  interface McpTool {
    name: string;
    description?: string;
  }

  interface McpServer {
    name: string;
    transport: 'stdio' | 'http';
    command?: string;
    args?: string[];
    env?: Record<string, string>;
    url?: string;
    enabled: boolean;
    running: boolean;
    tools?: McpTool[];
    lastError?: string;
  }

  let servers = $state<McpServer[]>([]);
  let name = $state('');
  let transport = $state<'stdio' | 'http'>('stdio');
  let command = $state('uvx');
  let argsStr = $state('');
  let envJson = $state('{}');
  let url = $state('');
  let msg = $state('');
  let probing = $state('');

  async function loadServers() {
    try {
      const res = await apiFetch<{ servers: McpServer[] }>('/admin/mcp/servers');
      servers = res.servers || [];
    } catch (e: any) {
      msg = e.message;
    }
  }

  async function addServer() {
    msg = '';
    let envObj: Record<string, string> = {};
    try {
      envObj = JSON.parse(envJson || '{}');
    } catch {
      msg = 'Env JSON が不正です';
      return;
    }
    const args = argsStr
      .split(/\s+/)
      .map((s) => s.trim())
      .filter(Boolean);
    try {
      const res = await apiFetch<{ servers: McpServer[] }>('/admin/mcp/servers', {
        method: 'POST',
        body: JSON.stringify({
          name: name.trim(),
          transport,
          command: transport === 'stdio' ? command.trim() : undefined,
          args: transport === 'stdio' ? args : undefined,
          env: transport === 'stdio' ? envObj : undefined,
          url: transport === 'http' ? url.trim() : undefined,
          enabled: true
        })
      });
      servers = res.servers || [];
      name = '';
      argsStr = '';
      url = '';
      msg = 'MCP サーバーを登録しました';
    } catch (e: any) {
      msg = e.message;
    }
  }

  async function toggleServer(s: McpServer) {
    const res = await apiFetch<{ servers: McpServer[] }>(`/admin/mcp/servers/${encodeURIComponent(s.name)}/toggle`, {
      method: 'POST',
      body: JSON.stringify({ enabled: !s.enabled })
    });
    servers = res.servers || [];
  }

  async function probeServer(s: McpServer) {
    probing = s.name;
    msg = '';
    try {
      const res = await apiFetch<{ tools: McpTool[]; servers: McpServer[] }>(
        `/admin/mcp/servers/${encodeURIComponent(s.name)}/probe`,
        { method: 'POST' }
      );
      servers = res.servers || [];
      msg = `${s.name}: ${(res.tools || []).length} tools 検出`;
    } catch (e: any) {
      msg = `${s.name} probe error: ${e.message}`;
      await loadServers();
    } finally {
      probing = '';
    }
  }

  async function deleteServer(s: McpServer) {
    const res = await apiFetch<{ servers: McpServer[] }>(`/admin/mcp/servers/${encodeURIComponent(s.name)}`, {
      method: 'DELETE'
    });
    servers = res.servers || [];
  }

  onMount(loadServers);
</script>

<div class="mcp-view">
  <div class="pane-head">
    <span>MCP Servers（ホスト接続デバイス / リモート公開ゲートウェイ）</span>
    <div class="pane-controls">
      <span class="sub">外部エージェント用エンドポイント: <code>POST /mcp/&lt;name&gt;</code> (Bearer トークン必須)</span>
      <button class="ghost" onclick={loadServers}>更新</button>
    </div>
  </div>

  <div class="inline-drawer">
    <div class="form-grid">
      <div class="field">
        <label>サーバー名 (例: m5stack-flasher)</label>
        <input class="mono" bind:value={name} placeholder="m5stack-flasher" style="width:100%;" />
      </div>
      <div class="field">
        <label>Transport</label>
        <select bind:value={transport} style="width:100%;">
          <option value="stdio">stdio（ホスト側プロセス起動）</option>
          <option value="http">http（Streamable HTTP 中継）</option>
        </select>
      </div>
      {#if transport === 'stdio'}
        <div class="field">
          <label>Command</label>
          <input class="mono" bind:value={command} placeholder="uvx / npx.cmd / python" style="width:100%;" />
        </div>
        <div class="field">
          <label>Args (スペース区切り)</label>
          <input class="mono" bind:value={argsStr} placeholder="--from m5-mcp m5-mcp --port COM5" style="width:100%;" />
        </div>
        <div class="field" style="grid-column: 1 / -1;">
          <label>Env (JSON)</label>
          <input class="mono" bind:value={envJson} style="width:100%;" />
        </div>
      {:else}
        <div class="field" style="grid-column: 1 / -1;">
          <label>MCP HTTP URL</label>
          <input class="mono" bind:value={url} placeholder="http://127.0.0.1:9000/mcp" style="width:100%;" />
        </div>
      {/if}
    </div>
    <div class="row" style="justify-content:space-between; margin-top:8px;">
      <span class="sub">{msg}</span>
      <button onclick={addServer}>MCP サーバーを追加 / 更新</button>
    </div>
  </div>

  <div class="pane-body">
    <table>
      <thead>
        <tr><th>Name</th><th>Transport</th><th>Target</th><th>Endpoint</th><th>Status</th><th>Tools</th><th>Actions</th></tr>
      </thead>
      <tbody>
        {#if servers.length === 0}
          <tr><td colspan="7" class="sub">MCP サーバー未登録（例: M5Stack フラッシュ・シリアルログ取得用 MCP を登録）</td></tr>
        {:else}
          {#each servers as s}
            <tr>
              <td><code>{s.name}</code></td>
              <td><span class="tag">{s.transport}</span></td>
              <td class="sub">
                {#if s.transport === 'stdio'}
                  {s.command} {(s.args || []).join(' ')}
                {:else}
                  {s.url}
                {/if}
              </td>
              <td><code>/mcp/{s.name}</code></td>
              <td>
                <span class="tag {s.enabled ? (s.running ? 'ok' : 'warn') : 'err'}">
                  {!s.enabled ? 'DISABLED' : s.running ? 'RUNNING' : 'READY'}
                </span>
                {#if s.lastError}
                  <div class="sub" style="color:#f87171;">{s.lastError}</div>
                {/if}
              </td>
              <td class="sub">
                {#if s.tools && s.tools.length > 0}
                  {s.tools.map((t) => t.name).join(', ')}
                {:else}
                  -
                {/if}
              </td>
              <td>
                <div class="row">
                  <button class="ghost" onclick={() => probeServer(s)} disabled={!s.enabled || probing === s.name}>
                    {probing === s.name ? '...' : 'Probe'}
                  </button>
                  <button class="ghost" onclick={() => toggleServer(s)}>
                    {s.enabled ? '無効化' : '有効化'}
                  </button>
                  <button class="danger" onclick={() => deleteServer(s)}>削除</button>
                </div>
              </td>
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
  </div>
</div>
