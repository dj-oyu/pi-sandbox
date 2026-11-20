<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { EditorState, Compartment } from '@codemirror/state';
  import { EditorView, lineNumbers, highlightActiveLineGutter, hoverTooltip, keymap } from '@codemirror/view';
  import { foldGutter, foldKeymap, syntaxHighlighting, defaultHighlightStyle } from '@codemirror/language';
  import { search, searchKeymap } from '@codemirror/search';
  import { oneDark } from '@codemirror/theme-one-dark';
  import { javascript } from '@codemirror/lang-javascript';
  import { python } from '@codemirror/lang-python';
  import { go } from '@codemirror/lang-go';
  import { rust } from '@codemirror/lang-rust';
  import { json } from '@codemirror/lang-json';
  import { markdown } from '@codemirror/lang-markdown';
  import { html } from '@codemirror/lang-html';
  import { css } from '@codemirror/lang-css';
  import { apiFetch } from '../lib/api';
  import { renderMarkdown } from '../lib/markdown';
  import FileTreeNode, { type TreeEntry } from './FileTreeNode.svelte';

  let rootEntries = $state<TreeEntry[]>([]);
  let includeHidden = $state(false);
  let filterText = $state('');
  let activePath = $state('');
  let rawContent = $state('');
  let showRawMd = $state(false);
  let copied = $state(false);
  let lspNote = $state('');
  let mobileHoverMd = $state('');
  let treeCollapsed = $state(false);
  let lastCursorPos = 0;
  let editorHost: HTMLDivElement | undefined;

  let showGH = $state(false);
  let ghStatus = $state<{ isRepo?: boolean; branch?: string; originUrl?: string; dirtyCount?: number; ghUser?: string } | null>(null);
  let repoName = $state('');
  let repoVis = $state<'private' | 'public'>('private');
  let prTitle = $state('');
  let prBase = $state('main');
  let ghBusy = $state(false);
  let ghMsg = $state('');

  let view: EditorView | null = null;
  const langConf = new Compartment();

  const isMarkdown = $derived(activePath.toLowerCase().endsWith('.md'));

  function langExtForPath(p: string) {
    const lower = p.toLowerCase();
    if (lower.endsWith('.ts') || lower.endsWith('.tsx')) return javascript({ typescript: true, jsx: lower.endsWith('x') });
    if (lower.endsWith('.js') || lower.endsWith('.jsx') || lower.endsWith('.mjs') || lower.endsWith('.cjs')) return javascript({ jsx: lower.endsWith('x') });
    if (lower.endsWith('.py') || lower.endsWith('.pyi')) return python();
    if (lower.endsWith('.go')) return go();
    if (lower.endsWith('.rs')) return rust();
    if (lower.endsWith('.json')) return json();
    if (lower.endsWith('.md')) return markdown();
    if (lower.endsWith('.html') || lower.endsWith('.svelte')) return html();
    if (lower.endsWith('.css')) return css();
    return [];
  }

  function posToLsp(doc: EditorState['doc'], pos: number) {
    const lineObj = doc.lineAt(pos);
    return {
      line: lineObj.number - 1,
      character: pos - lineObj.from
    };
  }

  function lspToPos(doc: EditorState['doc'], line: number, character: number): number {
    const lineNum = Math.min(Math.max(1, line + 1), doc.lines);
    const lineObj = doc.line(lineNum);
    return Math.min(lineObj.from + Math.max(0, character), lineObj.to);
  }

  async function jumpToDefinitionAt(pos: number) {
    if (!view || !activePath) return;
    const { line, character } = posToLsp(view.state.doc, pos);
    lspNote = '定義を検索中...';
    try {
      const res = await apiFetch<{
        locations: Array<{ path?: string; uri?: string; line: number; character: number; external?: boolean }>;
        error?: string;
      }>('/admin/lsp/definition', {
        method: 'POST',
        body: JSON.stringify({ path: activePath, line, character })
      });
      if (res.error) {
        lspNote = `LSP: ${res.error}`;
        return;
      }
      const loc = res.locations?.[0];
      if (!loc) {
        lspNote = '定義が見つかりませんでした';
        return;
      }
      if (loc.external || !loc.path) {
        lspNote = `外部定義: ${loc.uri || '(external)'}:${loc.line + 1}`;
        return;
      }
      await openFile(loc.path, loc.line, loc.character);
      lspNote = `ジャンプ: ${loc.path}:${loc.line + 1}`;
    } catch (e: any) {
      lspNote = `LSP error: ${e.message}`;
    }
  }

  const lspHover = hoverTooltip(async (v, pos) => {
    if (!activePath) return null;
    const { line, character } = posToLsp(v.state.doc, pos);
    try {
      const res = await apiFetch<{ found: boolean; contents: string }>('/admin/lsp/hover', {
        method: 'POST',
        body: JSON.stringify({ path: activePath, line, character })
      });
      if (!res.found || !res.contents) return null;
      return {
        pos,
        above: true,
        create() {
          const dom = document.createElement('div');
          dom.className = 'md lsp-hover';
          dom.style.cssText =
            'max-width:520px;max-height:240px;overflow:auto;padding:2px 4px;color:#e2e8f0;font-size:11.5px;';
          dom.innerHTML = renderMarkdown(res.contents);
          return { dom };
        }
      };
    } catch {
      return null;
    }
  }, { hideOnChange: true });

  function initEditor() {
    if (!editorHost || view) return;
    const state = EditorState.create({
      doc: '// 左のツリーからファイルを選択してください',
      extensions: [
        EditorState.readOnly.of(true),
        EditorView.editable.of(false),
        lineNumbers(),
        highlightActiveLineGutter(),
        foldGutter(),
        search(),
        keymap.of([...searchKeymap, ...foldKeymap]),
        oneDark,
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
        langConf.of([]),
        lspHover,
        EditorView.domEventHandlers({
          click(e, v) {
            const pos = v.posAtCoords({ x: e.clientX, y: e.clientY });
            if (pos !== null) {
              lastCursorPos = pos;
              v.dispatch({ selection: { anchor: pos } });
            }
            if (e.ctrlKey || e.metaKey) {
              if (pos !== null) {
                e.preventDefault();
                jumpToDefinitionAt(pos);
              }
            }
          },
          pointerup(e, v) {
            const pos = v.posAtCoords({ x: e.clientX, y: e.clientY });
            if (pos !== null) lastCursorPos = pos;
          },
          touchend(e, v) {
            const t = e.changedTouches?.[0];
            if (t) {
              const pos = v.posAtCoords({ x: t.clientX, y: t.clientY });
              if (pos !== null) lastCursorPos = pos;
            }
          }
        }),
        EditorView.theme({
          '&': { height: '100%', backgroundColor: '#090d16', fontSize: '12px' },
          '.cm-scroller': { fontFamily: 'var(--mono)' },
          '.cm-content': { paddingBottom: 'calc(56px + env(safe-area-inset-bottom, 0px))' },
          '.cm-gutters': { backgroundColor: '#0f1522', borderRight: '1px solid #243047' },
          '.cm-tooltip': { backgroundColor: '#0b101b', border: '1px solid #38bdf8', borderRadius: '4px' }
        })
      ]
    });
    view = new EditorView({ state, parent: editorHost });
  }

  export async function loadTree() {
    try {
      const res = await apiFetch<{ entries: TreeEntry[] }>(
        `/admin/fs/tree?hidden=${includeHidden ? '1' : '0'}`
      );
      rootEntries = res.entries || [];
    } catch {
      rootEntries = [];
    }
  }

  export async function openFile(path: string, targetLine?: number, targetChar?: number) {
    const res = await apiFetch<{ path: string; size: number; truncated: boolean; content: string }>(
      `/admin/fs/raw?path=${encodeURIComponent(path)}`
    );
    activePath = res.path;
    rawContent = res.content;
    showRawMd = false;
    mobileHoverMd = '';
    lspNote = '';
    lastCursorPos = 0;
    if (typeof window !== 'undefined' && window.innerWidth <= 1000) {
      treeCollapsed = true;
    }
    if (!view) initEditor();
    if (view) {
      view.dispatch({
        changes: { from: 0, to: view.state.doc.length, insert: res.content },
        effects: langConf.reconfigure(langExtForPath(res.path))
      });
      if (typeof targetLine === 'number') {
        const pos = lspToPos(view.state.doc, targetLine, targetChar || 0);
        view.dispatch({
          selection: { anchor: pos },
          effects: EditorView.scrollIntoView(pos, { y: 'center' })
        });
      }
    }
  }

  async function copyPiPath() {
    if (!activePath) return;
    const piPath = `/workspace/${activePath}`;
    try {
      await navigator.clipboard.writeText(piPath);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      lspNote = piPath;
    }
  }

  function captureDomSelectionPos(): number {
    if (!view) return lastCursorPos;
    try {
      const sel = window.getSelection();
      if (sel && sel.rangeCount > 0 && sel.anchorNode && view.contentDOM.contains(sel.anchorNode)) {
        const pos = view.posAtDOM(sel.anchorNode, sel.anchorOffset);
        if (typeof pos === 'number' && pos >= 0 && pos <= view.state.doc.length) {
          lastCursorPos = pos;
          return pos;
        }
      }
    } catch {
      // ignore DOM mapping errors
    }
    const cmHead = view.state.selection.main.head;
    if (cmHead > 0) {
      lastCursorPos = cmHead;
    }
    return Math.min(lastCursorPos, view.state.doc.length);
  }

  async function inspectHoverAtCursor() {
    if (!view || !activePath) return;
    const pos = captureDomSelectionPos();
    const { line, character } = posToLsp(view.state.doc, pos);
    mobileHoverMd = `型情報を取得中... (L${line + 1}:${character + 1})`;
    try {
      const res = await apiFetch<{ found: boolean; contents: string; error?: string }>('/admin/lsp/hover', {
        method: 'POST',
        body: JSON.stringify({ path: activePath, line, character })
      });
      if (res.error) {
        mobileHoverMd = `LSP error: ${res.error}`;
      } else {
        mobileHoverMd = res.found && res.contents ? res.contents : `型情報なし (L${line + 1}:${character + 1})`;
      }
    } catch (e: any) {
      mobileHoverMd = `LSP error: ${e?.message || e}`;
    }
  }

  function jumpAtCursor() {
    if (!view || !activePath) return;
    const pos = captureDomSelectionPos();
    jumpToDefinitionAt(pos);
  }

  async function loadGHStatus() {
    try {
      ghStatus = await apiFetch('/admin/github/status');
    } catch (e: any) {
      ghMsg = e.message;
    }
  }

  async function createGHRepo() {
    if (!repoName.trim() || ghBusy) return;
    ghBusy = true;
    ghMsg = '';
    try {
      const res = await apiFetch<{ output: string; status: any }>('/admin/github/create-repo', {
        method: 'POST',
        body: JSON.stringify({ name: repoName.trim(), visibility: repoVis })
      });
      ghStatus = res.status;
      ghMsg = res.output || 'リポジトリ作成＆Push 完了';
    } catch (e: any) {
      ghMsg = `Error: ${e.message}`;
    } finally {
      ghBusy = false;
    }
  }

  async function pushGH() {
    if (ghBusy) return;
    ghBusy = true;
    ghMsg = '';
    try {
      const res = await apiFetch<{ output: string; status: any }>('/admin/github/push', { method: 'POST' });
      ghStatus = res.status;
      ghMsg = res.output || 'Push 完了';
    } catch (e: any) {
      ghMsg = `Error: ${e.message}`;
    } finally {
      ghBusy = false;
    }
  }

  async function createGHPR() {
    if (!prTitle.trim() || ghBusy) return;
    ghBusy = true;
    ghMsg = '';
    try {
      const res = await apiFetch<{ output: string }>('/admin/github/pr', {
        method: 'POST',
        body: JSON.stringify({ title: prTitle.trim(), base: prBase.trim(), body: '' })
      });
      ghMsg = res.output || 'PR 作成完了';
      prTitle = '';
    } catch (e: any) {
      ghMsg = `Error: ${e.message}`;
    } finally {
      ghBusy = false;
    }
  }

  function handleDocSelectionChange() {
    captureDomSelectionPos();
  }

  onMount(() => {
    initEditor();
    loadTree();
    document.addEventListener('selectionchange', handleDocSelectionChange);
  });

  onDestroy(() => {
    document.removeEventListener('selectionchange', handleDocSelectionChange);
    view?.destroy();
    view = null;
  });
</script>

<div class="files-view" class:tree-collapsed={treeCollapsed}>
  {#if !treeCollapsed}
  <section class="pane">
    <div class="pane-head">
      <span>Workspace Files</span>
      <div class="pane-controls">
        <label class="row" style="gap:4px;">
          <input type="checkbox" bind:checked={includeHidden} onchange={loadTree} /> .git/deps
        </label>
        <button class="ghost" onclick={loadTree}>更新</button>
        <button class="ghost" onclick={() => (treeCollapsed = true)} title="ツリーを折りたたむ">◀ 畳む</button>
      </div>
    </div>
    <div style="padding:6px 8px; border-bottom:1px solid var(--border);">
      <input bind:value={filterText} placeholder="ファイル名フィルタ..." style="width:100%;" />
    </div>
    <div class="pane-body" style="padding:6px 4px;">
      {#if rootEntries.length === 0}
        <div class="sub" style="padding:8px;">ファイルなし（PI_WORKSPACE を確認）</div>
      {:else}
        {#each rootEntries as entry (entry.path)}
          <FileTreeNode {entry} {activePath} {includeHidden} {filterText} onOpenFile={(p) => openFile(p)} />
        {/each}
      {/if}
    </div>
  </section>
  {/if}

  <section class="pane">
    <div class="pane-head">
      <div class="row" style="gap:6px;">
        <button class="ghost" onclick={() => (treeCollapsed = !treeCollapsed)} title="ファイルツリーを開閉">
          {treeCollapsed ? '▶ ツリー' : '◀'}
        </button>
        <span class="mono">{activePath || '(未選択)'}</span>
      </div>
      <div class="pane-controls">
        {#if lspNote}
          <span class="sub">{lspNote}</span>
        {/if}
        {#if activePath}
          {#if isMarkdown}
            <button class="ghost" onclick={() => (showRawMd = !showRawMd)}>
              {showRawMd ? 'Preview' : 'Raw'}
            </button>
          {:else}
            <button class="ghost" onpointerdown={captureDomSelectionPos} onclick={inspectHoverAtCursor} title="カーソル位置の型情報">型</button>
            <button class="ghost" onpointerdown={captureDomSelectionPos} onclick={jumpAtCursor} title="カーソル位置の定義へジャンプ">定義</button>
          {/if}
          <button class="ghost" onclick={copyPiPath} title="/workspace/パスをコピー">
            {copied ? 'Copied!' : 'Pathコピー'}
          </button>
        {/if}
        <button class="ghost" onclick={() => { showGH = !showGH; if (showGH) loadGHStatus(); }}>
          GitHub ▾
        </button>
      </div>
    </div>
    {#if showGH}
      <div class="inline-drawer">
        <div class="row" style="justify-content:space-between; margin-bottom:6px;">
          <span class="sub">
            gh: <strong>{ghStatus?.ghUser || '(checking)'}</strong> ·
            branch: <code>{ghStatus?.branch || '-'}</code> ·
            origin: <code>{ghStatus?.originUrl || '(none)'}</code>
            {#if ghStatus?.dirtyCount} · dirty: {ghStatus.dirtyCount}{/if}
          </span>
          <button class="ghost" onclick={loadGHStatus} disabled={ghBusy}>再読込</button>
        </div>
        {#if !ghStatus?.originUrl}
          <div class="row" style="margin-bottom:6px;">
            <input class="mono" bind:value={repoName} placeholder="new-repo-name" style="width:190px;" />
            <select bind:value={repoVis}>
              <option value="private">Private</option>
              <option value="public">Public</option>
            </select>
            <button onclick={createGHRepo} disabled={ghBusy || !repoName.trim()}>
              {ghBusy ? '処理中...' : 'リポジトリ作成 & Push'}
            </button>
          </div>
        {:else}
          <div class="row" style="margin-bottom:6px;">
            <button onclick={pushGH} disabled={ghBusy}>{ghBusy ? 'Push中...' : 'git push'}</button>
            <input bind:value={prTitle} placeholder="PR タイトル..." style="flex:1; min-width:160px;" />
            <input class="mono" bind:value={prBase} placeholder="base (main)" style="width:90px;" />
            <button class="ghost" onclick={createGHPR} disabled={ghBusy || !prTitle.trim()}>PR 作成</button>
          </div>
        {/if}
        {#if ghMsg}
          <div class="sub" style="word-break:break-all;">{ghMsg}</div>
        {/if}
      </div>
    {/if}
    {#if mobileHoverMd}
      <div class="inline-drawer" style="display:flex; justify-content:space-between; align-items:flex-start; gap:8px;">
        <div class="md lsp-hover" style="flex:1;">{@html renderMarkdown(mobileHoverMd)}</div>
        <button class="ghost" onclick={() => (mobileHoverMd = '')}>×</button>
      </div>
    {/if}
    <div class="pane-body" style={isMarkdown && !showRawMd ? 'padding:12px 16px; overflow:auto;' : 'padding:0; overflow:hidden;'}>
      {#if isMarkdown && !showRawMd}
        <div class="md">{@html renderMarkdown(rawContent)}</div>
      {/if}
      <div bind:this={editorHost} style="height:100%; width:100%; display:{isMarkdown && !showRawMd ? 'none' : 'block'};"></div>
    </div>
  </section>
</div>
