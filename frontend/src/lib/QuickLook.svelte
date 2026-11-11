<script lang="ts">
  import { fileURL, thumbURL, type FileView } from './api'
  import { size, typeLabel } from './format'

  type Props = {
    file: FileView
    onclose: () => void
    onstep: (delta: 1 | -1) => void
    onreveal: (path: string) => void
  }
  let { file, onclose, onstep, onreveal }: Props = $props()

  let dialog: HTMLDivElement | undefined = $state()
  let showOriginal = $state(false)
  $effect(() => {
    void file.id
    showOriginal = false
    dialog?.focus()
  })

  let rev = $derived(showOriginal ? 'original' : file.rev)
  let kind = $derived(showOriginal ? file.originalInfo?.kind || file.kind : file.kind)
  let info = $derived(showOriginal ? file.originalInfo : file.info)
  let canCompare = $derived((file.steps?.length ?? 0) > 0 && file.origin !== 'combined')
  let originalName = $derived(file.originalPath.split(/[\\/]/).pop() ?? file.displayName)
  let originalType = $derived(originalName.includes('.') ? '.' + originalName.split('.').pop() : '')

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' || e.key === ' ') {
      e.preventDefault()
      e.stopPropagation()
      onclose()
    } else if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
      e.preventDefault()
      onstep(1)
    } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
      e.preventDefault()
      onstep(-1)
    } else if (e.key.toLowerCase() === 'o' && canCompare) {
      e.preventDefault()
      showOriginal = !showOriginal
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onclick={onclose}>
  <div
    class="sheet"
    role="dialog"
    aria-modal="true"
    aria-label={'Preview of ' + file.displayName}
    tabindex="-1"
    bind:this={dialog}
    {onkeydown}
    onclick={(e) => e.stopPropagation()}
  >
    <header>
      <div class="title">
        <h2>{showOriginal ? originalName : file.displayName}</h2>
        <p>
          {typeLabel(showOriginal ? originalType : file.type)}&ensp;{size(showOriginal ? file.originalSize : file.size)}{#if info?.summary}&ensp;{info.summary}{/if}
          {#if (file.steps?.length ?? 0) && !showOriginal}&ensp;<span class="steps">{(file.steps ?? []).map((s) => s.label).join(' → ')}</span>{/if}
        </p>
      </div>
      <div class="actions">
        {#if canCompare}
          <button type="button" class:on={showOriginal} onclick={() => (showOriginal = !showOriginal)} title="Toggle original (O)">
            {showOriginal ? 'Showing original' : 'Compare with original'}
          </button>
        {/if}
        {#if file.originalPath}
          <button type="button" onclick={() => onreveal(file.originalPath)}>Show original in folder</button>
        {/if}
        <button type="button" class="close" onclick={onclose} aria-label="Close preview">×</button>
      </div>
    </header>

    <div class="stage">
      {#key rev + file.id}
        {#if kind === 'image'}
          <img src={thumbURL(file.id, rev, 1600)} alt={file.displayName} />
        {:else if kind === 'video'}
          <!-- svelte-ignore a11y_media_has_caption -->
          <video src={fileURL(file.id, rev)} controls autoplay muted playsinline></video>
        {:else if kind === 'audio'}
          <audio src={fileURL(file.id, rev)} controls></audio>
        {:else if kind === 'pdf'}
          <iframe src={fileURL(file.id, rev)} title={file.displayName}></iframe>
        {:else if kind === 'folder'}
          <ul class="entries">
            {#each info?.entries ?? [] as e}
              <li class:dir={e.dir}><span>{e.path}{e.dir ? '/' : ''}</span>{#if !e.dir}<span class="size">{size(e.size)}</span>{/if}</li>
            {/each}
            {#if (info?.count ?? 0) > (info?.entries?.length ?? 0)}
              <li class="more">and more ({info.count} files in total)</li>
            {/if}
          </ul>
        {:else if info?.snippet}
          <pre>{info.snippet}</pre>
        {:else}
          <p class="none">No preview for this file type. Export it to open it in another app.</p>
        {/if}
      {/key}
    </div>
    <footer><kbd>←</kbd><kbd>→</kbd> other files{#if canCompare}&ensp;<kbd>O</kbd> original{/if}&ensp;<kbd>Space</kbd> close</footer>
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 50;
    background: rgb(0 0 0 / 0.38);
    display: grid;
    place-items: center;
    padding: 24px;
    animation: fade 0.12s ease-out;
  }
  @keyframes fade {
    from {
      opacity: 0;
    }
  }
  .sheet {
    width: min(1100px, 100%);
    height: min(780px, 100%);
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border-radius: 14px;
    border: 1px solid var(--line);
    box-shadow: var(--shadow-pop);
    outline: none;
    overflow: hidden;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 16px;
    padding: 14px 16px 12px 18px;
    border-bottom: 1px solid var(--line);
  }
  .title {
    min-width: 0;
  }
  h2 {
    margin: 0;
    font-size: 16px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  header p {
    margin: 2px 0 0;
    color: var(--muted);
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
  .steps {
    color: var(--ink);
  }
  .actions {
    display: flex;
    gap: 6px;
    flex: none;
    align-items: center;
  }
  .actions button {
    font: inherit;
    font-size: 13px;
    padding: 5px 10px;
    border-radius: 8px;
    border: 1px solid var(--line);
    background: transparent;
    color: var(--ink);
    cursor: pointer;
  }
  .actions button.on {
    background: rgb(var(--accent-rgb) / 0.12);
    border-color: rgb(var(--accent-rgb) / 0.5);
  }
  .actions .close {
    border: 0;
    font-size: 20px;
    width: 30px;
    padding: 2px 0;
  }
  .stage {
    position: relative;
    flex: 1;
    min-height: 0;
    display: grid;
    place-items: center;
    background: var(--well);
    overflow: auto;
  }
  img,
  video {
    position: absolute;
    inset: 12px;
    width: calc(100% - 24px);
    height: calc(100% - 24px);
    object-fit: contain;
  }
  iframe {
    width: 100%;
    height: 100%;
    border: 0;
    background: white;
  }
  pre {
    align-self: stretch;
    justify-self: stretch;
    margin: 0;
    padding: 18px;
    font: 13px/1.55 ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
    white-space: pre-wrap;
    word-break: break-word;
    color: var(--ink);
  }
  .entries {
    align-self: stretch;
    justify-self: stretch;
    list-style: none;
    margin: 0;
    padding: 10px 18px;
    font-size: 13px;
  }
  .entries li {
    display: flex;
    justify-content: space-between;
    padding: 4px 0;
    border-bottom: 1px solid var(--line);
  }
  .entries .dir {
    color: var(--muted);
  }
  .entries .size,
  .entries .more {
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .none {
    color: var(--muted);
  }
  footer {
    padding: 8px 18px;
    font-size: 12px;
    color: var(--muted);
    border-top: 1px solid var(--line);
  }
</style>
