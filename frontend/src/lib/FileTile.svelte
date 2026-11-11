<script lang="ts">
  import { thumbURL, type FileView } from './api'
  import { size, typeLabel } from './format'

  type Props = {
    file: FileView
    selected: boolean
    active: boolean
    working: boolean
    onselect: (e: MouseEvent) => void
    onopen: () => void
    onremovestep: (index: number) => void
    oncontext: (e: MouseEvent) => void
  }
  let { file, selected, active, working, onselect, onopen, onremovestep, oncontext }: Props = $props()

  let broken = $state(false)
  $effect(() => {
    void file.rev
    broken = false
  })

  let showThumb = $derived((file.kind === 'image' || file.kind === 'video') && !broken)
  let snippet = $derived(file.kind === 'text' ? (file.info?.snippet ?? '').split('\n').slice(0, 9).join('\n') : '')
  let changed = $derived((file.steps?.length ?? 0) > 0)
  let sizeLine = $derived.by(() => {
    if (file.origin === 'combined') return size(file.size)
    if (!changed) return size(file.originalSize)
    return `${size(file.originalSize)} → ${size(file.size)}`
  })
  let infoLine = $derived.by(() => {
    const now = file.info?.summary ?? ''
    const was = file.originalInfo?.summary ?? ''
    if (changed && was && now && was !== now && file.originalInfo.kind === file.info.kind) return `${was} → ${now}`
    return now
  })
  let ratio = $derived(file.origin !== 'combined' && changed && file.originalSize > 0 ? file.size / file.originalSize : 1)
</script>

<div
  class="tile"
  class:selected
  class:active
  class:working
  role="option"
  aria-selected={selected}
  tabindex="-1"
  data-id={file.id}
  onclick={onselect}
  ondblclick={onopen}
  oncontextmenu={oncontext}
  onkeydown={() => {}}
>
  <div class="preview" class:folder={file.kind === 'folder'}>
    {#if showThumb}
      <img class:checker={file.kind === 'image'} src={thumbURL(file.id, file.rev)} alt="" loading="lazy" decoding="async" onerror={() => (broken = true)} />
    {:else if snippet}
      <pre class="snip" aria-hidden="true">{snippet}</pre>
      <span class="corner">{typeLabel(file.type)}</span>
    {:else}
      <div class="glyph">
        <span class="ext">{typeLabel(file.type)}</span>
        {#if file.info?.summary}<span class="sum">{file.info.summary}</span>{/if}
      </div>
    {/if}
    {#if file.origin === 'combined'}
      <span class="badge" title={'Made from ' + (file.sources ?? []).join(', ')}>new</span>
    {/if}
  </div>

  <div class="body">
    <div class="name" title={file.originalPath || file.displayName}>{file.displayName}</div>
    <div class="meta">
      <span class:smaller={ratio < 0.98}>{sizeLine}</span>
      {#if infoLine}<span>{infoLine}</span>{/if}
    </div>
    {#if (file.steps?.length ?? 0)}
      <div class="chips">
        {#each file.steps ?? [] as step, i (step.id)}
          <span class="chip">
            {step.label}
            <button
              type="button"
              aria-label={'Remove ' + step.label}
              title="Remove this step (the rest replays)"
              onclick={(e) => {
                e.stopPropagation()
                onremovestep(i)
              }}>×</button
            >
          </span>
        {/each}
      </div>
    {/if}
  </div>
</div>

<style>
  .tile {
    display: flex;
    flex-direction: column;
    min-width: 0;
    border-radius: 12px;
    padding: 6px;
    outline: none;
    cursor: default;
    transition: background 0.12s;
  }
  .tile:hover {
    background: var(--hover);
  }
  .tile.selected {
    background: rgb(var(--accent-rgb) / 0.1);
  }
  .tile.active .preview {
    box-shadow:
      0 0 0 2px var(--bg),
      0 0 0 4px rgb(var(--accent-rgb) / 0.75);
  }
  .preview {
    position: relative;
    aspect-ratio: 4 / 3;
    border-radius: 9px;
    overflow: hidden;
    background: var(--well);
    display: grid;
    place-items: center;
  }
  .working .preview::after {
    content: '';
    position: absolute;
    inset: 0;
    background: linear-gradient(100deg, transparent 30%, rgb(255 255 255 / 0.22) 50%, transparent 70%);
    background-size: 220% 100%;
    animation: sweep 1.1s linear infinite;
  }
  @keyframes sweep {
    from {
      background-position: 120% 0;
    }
    to {
      background-position: -120% 0;
    }
  }
  img {
    width: 100%;
    height: 100%;
    object-fit: contain;
  }
  img.checker {
    background-image:
      linear-gradient(45deg, var(--check) 25%, transparent 25%),
      linear-gradient(-45deg, var(--check) 25%, transparent 25%),
      linear-gradient(45deg, transparent 75%, var(--check) 75%),
      linear-gradient(-45deg, transparent 75%, var(--check) 75%);
    background-size: 14px 14px;
    background-position:
      0 0,
      0 7px,
      7px -7px,
      -7px 0;
  }
  .snip {
    align-self: stretch;
    justify-self: stretch;
    margin: 0;
    padding: 9px 10px;
    font: 10px/1.45 ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
    color: var(--muted);
    white-space: pre;
    overflow: hidden;
    background: var(--surface);
    -webkit-user-select: none;
    user-select: none;
  }
  .corner {
    position: absolute;
    right: 6px;
    bottom: 6px;
    padding: 1px 6px;
    border-radius: 5px;
    font-size: 11px;
    font-weight: 600;
    background: var(--well);
    color: rgb(var(--accent-ink-rgb));
  }
  .glyph {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
    padding: 8px;
    text-align: center;
  }
  .ext {
    font-size: 22px;
    font-weight: 700;
    letter-spacing: -0.02em;
    color: rgb(var(--accent-ink-rgb));
  }
  .folder .ext::before {
    content: '';
    display: block;
    width: 34px;
    height: 26px;
    margin: 0 auto 6px;
    border-radius: 4px 4px 5px 5px;
    background: rgb(var(--accent-rgb) / 0.35);
    clip-path: polygon(0 18%, 38% 18%, 46% 0, 100% 0, 100% 100%, 0 100%);
  }
  .sum {
    font-size: 12px;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .badge {
    position: absolute;
    top: 6px;
    left: 6px;
    padding: 1px 7px;
    border-radius: 6px;
    font-size: 11px;
    font-weight: 600;
    background: rgb(var(--accent-rgb));
    color: white;
  }
  .body {
    padding: 7px 2px 2px;
    min-width: 0;
  }
  .name {
    font-weight: 600;
    font-size: 13.5px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    column-gap: 10px;
    font-size: 12px;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
    margin-top: 1px;
  }
  .smaller {
    color: var(--good);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-top: 6px;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    padding: 2px 3px 2px 8px;
    border-radius: 6px;
    background: var(--chip);
    font-size: 12px;
    white-space: nowrap;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .chip button {
    border: 0;
    background: transparent;
    color: var(--muted);
    width: 18px;
    height: 18px;
    border-radius: 4px;
    font-size: 14px;
    line-height: 1;
    cursor: pointer;
    padding: 0;
  }
  .chip button:hover {
    background: var(--hover-strong);
    color: var(--ink);
  }
  .chip button:focus-visible {
    outline: 2px solid rgb(var(--accent-rgb));
  }
  @media (prefers-reduced-motion: reduce) {
    .working .preview::after {
      animation: none;
      background: rgb(255 255 255 / 0.12);
    }
  }
</style>
