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
  let editorHost: HTMLDivElement | undefined;

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
            if (e.ctrlKey || e.metaKey) {
              const pos = v.posAtCoords({ x: e.clientX, y: e.clientY });
              if (pos !== null) {
                e.preventDefault();
                jumpToDefinitionAt(pos);
              }
            }
          }
        }),
        EditorView.theme({
          '&': { height: '100%', backgroundColor: '#090d16', fontSize: '12px' },
          '.cm-scroller': { fontFamily: 'var(--mono)' },
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

  async function inspectHoverAtCursor() {
    if (!view || !activePath) return;
    const pos = view.state.selection.main.head;
    const { line, character } = posToLsp(view.state.doc, pos);
    const res = await apiFetch<{ found: boolean; contents: string }>('/admin/lsp/hover', {
      method: 'POST',
      body: JSON.stringify({ path: activePath, line, character })
    });
    mobileHoverMd = res.found && res.contents ? res.contents : '型情報なし';
  }

  function jumpAtCursor() {
    if (!view || !activePath) return;
    jumpToDefinitionAt(view.state.selection.main.head);
  }

  onMount(() => {
    initEditor();
    loadTree();
  });

  onDestroy(() => {
    view?.destroy();
    view = null;
  });
</script>

<div class="files-view">
  <section class="pane">
    <div class="pane-head">
      <span>Workspace Files</span>
      <div class="pane-controls">
        <label class="row" style="gap:4px;">
          <input type="checkbox" bind:checked={includeHidden} onchange={loadTree} /> .git/deps
        </label>
        <button class="ghost" onclick={loadTree}>更新</button>
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

  <section class="pane">
    <div class="pane-head">
      <span class="mono">{activePath || '(未選択)'}</span>
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
            <button class="ghost" onclick={inspectHoverAtCursor} title="カーソル位置の型情報">型</button>
            <button class="ghost" onclick={jumpAtCursor} title="カーソル位置の定義へジャンプ">定義</button>
          {/if}
          <button class="ghost" onclick={copyPiPath} title="/workspace/パスをコピー">
            {copied ? 'Copied!' : 'Pathコピー'}
          </button>
        {/if}
      </div>
    </div>
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
