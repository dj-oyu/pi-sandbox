<script lang="ts">
  import { apiFetch, formatBytes } from '../lib/api';
  import FileTreeNode from './FileTreeNode.svelte';

  export interface TreeEntry {
    name: string;
    path: string;
    isDir: boolean;
    size: number;
    modTime: string;
  }

  let {
    entry,
    depth = 0,
    activePath = '',
    includeHidden = false,
    filterText = '',
    onOpenFile
  }: {
    entry: TreeEntry;
    depth?: number;
    activePath: string;
    includeHidden: boolean;
    filterText: string;
    onOpenFile: (path: string) => void;
  } = $props();

  let expanded = $state(false);
  let loading = $state(false);
  let children = $state<TreeEntry[]>([]);
  let loaded = $state(false);

  async function toggle() {
    if (!entry.isDir) {
      onOpenFile(entry.path);
      return;
    }
    expanded = !expanded;
    if (expanded && !loaded) {
      loading = true;
      try {
        const res = await apiFetch<{ entries: TreeEntry[] }>(
          `/admin/fs/tree?dir=${encodeURIComponent(entry.path)}&hidden=${includeHidden ? '1' : '0'}`
        );
        children = res.entries || [];
        loaded = true;
      } finally {
        loading = false;
      }
    }
  }

  const visible = $derived(
    !filterText.trim() ||
      entry.isDir ||
      entry.name.toLowerCase().includes(filterText.trim().toLowerCase()) ||
      entry.path.toLowerCase().includes(filterText.trim().toLowerCase())
  );
</script>

{#if visible}
  <div
    class="tree-item {activePath === entry.path ? 'active' : ''}"
    style="padding-left: {8 + depth * 14}px;"
    onclick={toggle}
    role="button"
    tabindex="0"
    onkeydown={(e) => e.key === 'Enter' && toggle()}
  >
    <span>{entry.isDir ? (expanded ? '▾ ' : '▸ ') : '  '}{entry.name}</span>
    <span class="sub">{entry.isDir ? (loading ? '...' : '') : formatBytes(entry.size)}</span>
  </div>
{/if}

{#if entry.isDir && expanded}
  {#each children as child (child.path)}
    <FileTreeNode
      entry={child}
      depth={depth + 1}
      {activePath}
      {includeHidden}
      {filterText}
      {onOpenFile}
    />
  {/each}
{/if}
