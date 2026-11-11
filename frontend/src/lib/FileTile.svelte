<script lang="ts">
  import { scale } from 'svelte/transition'
  import { thumbURL, type FileView } from './api'
  import { size, typeLabel } from './format'

  type Props = {
    file: FileView
    /** Position in the newest batch, for the staggered entrance. */
    enter?: number
    selected: boolean
    active: boolean
    working: boolean
    onselect: (e: MouseEvent) => void
    onopen: () => void
    onremovestep: (index: number) => void
    oncontext: (e: MouseEvent) => void
  }
  let { file, enter = 0, selected, active, working, onselect, onopen, onremovestep, oncontext }: Props = $props()

  let broken = $state(false)
  $effect(() => {
    void file.rev
    broken = false
  })

  // Only formats that can hold transparency get the checkerboard.
  let alpha = $derived(/^\.(png|webp|gif|heic|heif|avif|tiff?|ico|svg)$/i.test(file.type))
  let showThumb = $derived((file.kind === 'image' || file.kind === 'video') && !broken)
  let snippet = $derived(file.kind === 'text' ? (file.info?.snippet ?? '').split('\n').slice(0, 9).join('\n') : '')
  let changed = $derived((file.steps?.length ?? 0) > 0)
  let was = $derived(file.origin === 'combined' || !changed ? '' : size(file.originalSize))
  let now = $derived(file.origin === 'combined' || changed ? size(file.size) : size(file.originalSize))
  let infoLine = $derived.by(() => {
    const now = file.info?.summary ?? ''
    const was = file.originalInfo?.summary ?? ''
    if (changed && was && now && was !== now && file.originalInfo.kind === file.info.kind) return `${was} → ${now}`
    return now
  })
  let ratio = $derived(file.origin !== 'combined' && changed && file.originalSize > 0 ? file.size / file.originalSize : 1)
  // Same-motion check for svelte transitions, which ignore the CSS media query.
  const still = typeof matchMedia !== 'undefined' && matchMedia('(prefers-reduced-motion: reduce)').matches
  const chipIn = (node: Element) => scale(node, { start: 0.85, duration: still ? 0 : 220, opacity: 0 })
  const chipOut = (node: Element) => scale(node, { start: 0.85, duration: still ? 0 : 140 })
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
  style={`--enter:${enter}`}
  onclick={onselect}
  ondblclick={onopen}
  oncontextmenu={oncontext}
  onkeydown={() => {}}
>
  <div class="preview" class:folder={file.kind === 'folder'} class:image={showThumb}>
    {#if showThumb}
      <img class:checker={file.kind === 'image' && alpha} src={thumbURL(file.id, file.rev)} alt="" loading="lazy" decoding="async" onerror={() => (broken = true)} />
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
      <span class="badge" title={'Made from ' + (file.sources ?? []).join(', ')}>New</span>
    {/if}
  </div>

  <div class="body">
    <div class="name" title={file.originalPath || file.displayName}>{file.displayName}</div>
    <div class="meta">
      <span class="sizes"
        >{#if was}<span class="was">{was}</span><span class="arrow" aria-label="to">→</span>{/if}<span class:smaller={ratio < 0.98} class:now={!!was}
          >{now}</span
        ></span
      >
      {#if infoLine}<span>{infoLine}</span>{/if}
    </div>
    {#if (file.steps?.length ?? 0)}
      <div class="chips">
        {#each file.steps ?? [] as step, i (step.id)}
          <span class="chip" in:chipIn out:chipOut>
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
    position: relative;
    display: flex;
    flex-direction: column;
    min-width: 0;
    border-radius: 16px;
    padding: 6px;
    margin: -6px;
    outline: none;
    cursor: default;
    transition: background 0.2s var(--ease-out);
    animation: tile-in 0.42s var(--ease-out) both;
    animation-delay: calc(var(--enter) * 26ms);
    /* Off-screen tiles skip layout and paint, which keeps hundreds of
       files smooth. */
    content-visibility: auto;
    contain-intrinsic-size: auto 230px;
  }
  @keyframes tile-in {
    from {
      opacity: 0;
      transform: translateY(10px) scale(0.98);
    }
  }
  .tile.selected {
    background: color-mix(in oklab, rgb(var(--accent-rgb)) 8%, transparent);
  }
  .tile.selected .preview {
    box-shadow:
      0 0 0 2px var(--bg),
      0 0 0 3.5px color-mix(in oklab, var(--accent) 45%, transparent);
  }
  .preview {
    position: relative;
    aspect-ratio: 4 / 3;
    border-radius: 11px;
    overflow: hidden;
    background: var(--well);
    box-shadow: inset 0 0 0 1px var(--line);
    display: grid;
    place-items: center;
    transition:
      box-shadow 0.25s var(--ease-out),
      transform 0.35s var(--ease-spring);
  }
  .preview.image {
    box-shadow: none;
  }
  .tile:hover .preview {
    transform: translateY(-2px);
  }
  .tile.active .preview {
    box-shadow:
      0 0 0 2px var(--bg),
      0 0 0 3.5px var(--accent);
  }
  .working .preview::after {
    content: '';
    position: absolute;
    inset: 0 -100%;
    background: linear-gradient(100deg, transparent 40%, rgb(255 255 255 / 0.09) 50%, transparent 60%);
    animation: sweep 1.3s var(--ease-out) infinite;
  }
  @keyframes sweep {
    from {
      transform: translateX(-35%);
    }
    to {
      transform: translateX(35%);
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
    padding: 10px 11px;
    font: 10px/1.5 var(--mono);
    color: var(--muted);
    white-space: pre;
    overflow: hidden;
    -webkit-mask-image: linear-gradient(to right, #000 70%, transparent);
    mask-image: linear-gradient(to right, #000 70%, transparent);
    -webkit-user-select: none;
    user-select: none;
  }
  .corner {
    position: absolute;
    right: 8px;
    bottom: 7px;
    font-size: 11px;
    font-weight: 450;
    letter-spacing: 0.04em;
    color: var(--ink-2);
  }
  .glyph {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
    padding: 8px;
    text-align: center;
  }
  .ext {
    font-size: 26px;
    font-weight: 250;
    letter-spacing: 0.06em;
    color: var(--ink);
  }
  .folder .ext::before {
    content: '';
    display: block;
    width: 36px;
    height: 27px;
    margin: 0 auto 8px;
    border-radius: 4px 4px 6px 6px;
    background: var(--line-strong);
    clip-path: polygon(0 18%, 38% 18%, 46% 0, 100% 0, 100% 100%, 0 100%);
  }
  .sum {
    font-size: 12px;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .badge {
    position: absolute;
    top: 7px;
    left: 7px;
    padding: 2px 8px;
    border-radius: 999px;
    font-size: 11px;
    font-weight: 450;
    background: var(--primary-bg);
    color: var(--primary-ink);
  }
  .body {
    padding: 9px 3px 2px;
    min-width: 0;
  }
  .name {
    font-weight: 400;
    font-size: 13.5px;
    color: var(--ink);
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
    margin-top: 2px;
  }
  .arrow {
    margin: 0 5px;
    opacity: 0.6;
  }
  .now {
    color: var(--ink-2);
  }
  .smaller {
    color: var(--good);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    margin-top: 8px;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 1px;
    padding: 2px 2px 2px 9px;
    border-radius: 999px;
    background: var(--chip);
    color: var(--ink-2);
    font-size: 11.5px;
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
    border-radius: 50%;
    font-size: 13px;
    line-height: 1;
    cursor: pointer;
    padding: 0;
    opacity: 0.55;
    transition:
      opacity 0.15s,
      background 0.15s,
      color 0.15s;
  }
  .chip:hover button,
  .chip button:focus-visible {
    opacity: 1;
  }
  .chip button:hover {
    background: var(--line-strong);
    color: var(--ink);
  }
  @media (prefers-reduced-motion: reduce) {
    .working .preview::after {
      animation: none;
      background: rgb(255 255 255 / 0.08);
    }
    .tile:hover .preview {
      transform: none;
    }
  }
</style>
