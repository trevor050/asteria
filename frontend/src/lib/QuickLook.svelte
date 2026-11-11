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
    class="sheet glass"
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
          {#if (file.steps?.length ?? 0) && !showOriginal}&ensp;<span class="steps">{(file.steps ?? []).map((s) => s.label).join('  →  ')}</span>{/if}
        </p>
      </div>
      <div class="actions">
        {#if canCompare}
          <div class="compare" role="radiogroup" aria-label="Show">
            <button type="button" role="radio" aria-checked={showOriginal} class:on={showOriginal} onclick={() => (showOriginal = true)} title="Toggle with O"
              >Original</button
            >
            <button type="button" role="radio" aria-checked={!showOriginal} class:on={!showOriginal} onclick={() => (showOriginal = false)} title="Toggle with O"
              >Result</button
            >
          </div>
        {/if}
        {#if file.originalPath}
          <button type="button" class="reveal" onclick={() => onreveal(file.originalPath)}>Show original in folder</button>
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
    background: var(--scrim);
    display: grid;
    place-items: center;
    padding: 28px;
    animation: fade 0.18s ease-out both;
  }
  @keyframes fade {
    from {
      opacity: 0;
    }
  }
  .sheet {
    --glass-bg: var(--glass-strong);
    width: min(1100px, 100%);
    height: min(780px, 100%);
    display: flex;
    flex-direction: column;
    border-radius: 28px;
    --glass-shadow: var(--lg-shadow-deep);
    outline: none;
    overflow: hidden;
    animation: sheet-in 0.34s var(--ease-spring) both;
  }
  @keyframes sheet-in {
    from {
      opacity: 0;
      transform: translateY(10px) scale(0.975);
    }
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 16px;
    padding: 14px 14px 12px 22px;
  }
  .title {
    min-width: 0;
  }
  h2 {
    margin: 0;
    font-size: 16px;
    font-weight: 400;
    letter-spacing: -0.01em;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  header p {
    margin: 3px 0 0;
    color: var(--muted);
    font-size: 12.5px;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .steps {
    color: var(--ink-2);
    white-space: pre;
  }
  .actions {
    display: flex;
    gap: 8px;
    flex: none;
    align-items: center;
  }
  .actions button {
    font-size: 12.5px;
    border: 0;
    background: transparent;
    color: var(--ink-2);
    cursor: pointer;
    padding: 5px 10px;
    border-radius: 999px;
    transition:
      background 0.15s,
      color 0.15s;
  }
  .actions button:hover {
    color: var(--ink);
    background: var(--hover);
  }
  .compare {
    display: flex;
    gap: 2px;
    padding: 2px;
    border-radius: 999px;
    background: var(--chip);
  }
  .compare button.on,
  .compare button.on:hover {
    background: var(--seg-on);
    color: var(--ink);
    box-shadow: var(--seg-on-shadow);
  }
  .actions .close {
    font-size: 19px;
    line-height: 1;
    width: 32px;
    height: 32px;
    padding: 0;
    border-radius: 50%;
  }
  .stage {
    position: relative;
    flex: 1;
    min-height: 0;
    margin: 0 10px;
    border-radius: 12px;
    display: grid;
    place-items: center;
    background: var(--well);
    overflow: auto;
  }
  img,
  video {
    position: absolute;
    inset: 14px;
    width: calc(100% - 28px);
    height: calc(100% - 28px);
    object-fit: contain;
    animation: fade 0.2s ease-out both;
  }
  iframe {
    width: 100%;
    height: 100%;
    border: 0;
    background: white;
  }
  audio {
    width: min(480px, 80%);
  }
  pre {
    align-self: stretch;
    justify-self: stretch;
    margin: 0;
    padding: 20px 22px;
    font: 12.5px/1.6 var(--mono);
    white-space: pre-wrap;
    word-break: break-word;
    color: var(--ink);
  }
  .entries {
    align-self: stretch;
    justify-self: stretch;
    list-style: none;
    margin: 0;
    padding: 10px 22px;
    font-size: 13px;
  }
  .entries li {
    display: flex;
    justify-content: space-between;
    gap: 16px;
    padding: 6px 0;
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
    font-weight: 300;
  }
  footer {
    padding: 10px 22px 12px;
    font-size: 12px;
    color: var(--muted);
  }
  footer kbd {
    margin-right: 2px;
  }
  @media (max-width: 640px) {
    .scrim {
      padding: 12px;
    }
    header {
      flex-wrap: wrap;
    }
  }
</style>
