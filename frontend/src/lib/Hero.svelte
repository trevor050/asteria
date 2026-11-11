<script lang="ts">
  import { scale } from 'svelte/transition'
  import Icon from './Icon.svelte'
  import { skillMeta } from './skillmeta.svelte'
  import { fileURL, thumbURL, type FileView, type Info } from './api'
  import { size, typeLabel } from './format'
  import { motionReduced } from './theme'
  import PdfPage from './PdfPage.svelte'

  type Props = {
    file: FileView
    /** Position and count when several files are in the tray. */
    index: number
    count: number
    working: boolean
    onprev: () => void
    onnext: () => void
    onopen: () => void
    onremovestep: (index: number) => void
    /** Live preview (data URL) of a skill being chosen, shown in place. */
    preview?: string
  }
  let { file, index, count, working, onprev, onnext, onopen, onremovestep, preview = '' }: Props = $props()

  // The frame shrink-wraps the media; its limits come from the window, so
  // the file, its details and the bar stay one compact, centered group.
  let heroW = $state(0)
  let winH = $state(0)
  let frameW = $derived(Math.min(heroW, 920))
  let frameH = $derived(Math.max(140, Math.min(720, winH - ((file.steps?.length ?? 0) > 0 ? 390 : 330))))
  let showOriginal = $state(false)
  let broken = $state(false)
  $effect(() => {
    void file.id
    showOriginal = false
  })
  $effect(() => {
    void file.rev
    broken = false
  })

  let changed = $derived((file.steps?.length ?? 0) > 0)
  let canCompare = $derived(changed && file.origin !== 'combined')
  let rev = $derived(showOriginal ? 'original' : file.rev)
  let info: Info = $derived(showOriginal ? file.originalInfo : file.info)
  let kind = $derived(showOriginal ? file.originalInfo?.kind || file.kind : file.kind)
  let type = $derived(showOriginal ? (file.originalPath.match(/\.[^./\\]+$/)?.[0] ?? file.type) : file.type)

  // Fit the media into the frame at its true aspect ratio. Small images are
  // never blown up into a blur: tiny ones scale by whole pixels, crisp.
  let box = $derived.by(() => {
    const w = info?.width || 0
    const h = info?.height || 0
    if (!w || !h || !frameW || !frameH) return null
    // Vector art scales cleanly, so a small SVG fills a good part of the
    // frame instead of sitting tiny in the middle.
    const vector = (showOriginal ? file.originalPath : file.type)?.toLowerCase().endsWith('.svg')
    let s = Math.min(frameW / w, frameH / h, vector ? Math.max(1, 360 / Math.max(w, h)) : 1)
    let pixelated = false
    if (!vector && s === 1 && Math.max(w, h) < 96) {
      s = Math.max(1, Math.min(4, Math.floor(160 / Math.max(w, h))))
      pixelated = s > 1
    }
    return { w: Math.round(w * s), h: Math.round(h * s), pixelated, natural: Math.max(w, h) }
  })
  let thumbSize = $derived(
    box ? Math.min(2400, box.pixelated ? box.natural : Math.ceil(Math.max(box.w, box.h) * Math.min(2, devicePixelRatio || 1))) : 1600,
  )
  let alpha = $derived(/^\.(png|webp|gif|heic|heif|avif|tiff?|ico|svg)$/i.test(type))

  let csv = $derived.by(() => {
    if (!/^\.(csv|tsv)$/i.test(type) || !info?.snippet) return null
    const sep = type.toLowerCase() === '.tsv' ? '\t' : ','
    return info.snippet
      .split('\n')
      .filter((l) => l.trim())
      .slice(0, 14)
      .map((l) => splitRow(l, sep).slice(0, 8))
  })
  function splitRow(line: string, sep: string): string[] {
    const out: string[] = []
    let cur = ''
    let q = false
    for (const ch of line) {
      if (ch === '"') q = !q
      else if (ch === sep && !q) {
        out.push(cur)
        cur = ''
      } else cur += ch
    }
    out.push(cur)
    return out
  }

  let was = $derived(file.origin === 'combined' || !changed ? '' : size(file.originalSize))
  let now = $derived(file.origin === 'combined' || changed ? size(file.size) : size(file.originalSize))
  let smaller = $derived(changed && file.origin !== 'combined' && file.originalSize > 0 && file.size / file.originalSize < 0.98)
  let infoLine = $derived.by(() => {
    const n = file.info?.summary ?? ''
    const o = file.originalInfo?.summary ?? ''
    if (changed && o && n && o !== n && file.originalInfo.kind === file.info.kind) return `${o} → ${n}`
    return n
  })
  let duration = $derived.by(() => {
    const d = info?.duration ?? 0
    if (!d) return ''
    const m = Math.floor(d / 60)
    const s = Math.round(d % 60)
    return `${m}:${String(s).padStart(2, '0')}`
  })

  const chipIn = (node: Element) => scale(node, { start: 0.85, duration: motionReduced() ? 0 : 220, opacity: 0 })
  const chipOut = (node: Element) => scale(node, { start: 0.85, duration: motionReduced() ? 0 : 140 })
</script>

<svelte:window bind:innerHeight={winH} />

<div class="hero" class:working bind:clientWidth={heroW}>
  <div class="frame" style={`--fh:${frameH}px`}>
    {#if count > 1}
      <button type="button" class="nav prev" onclick={onprev} disabled={index === 0} aria-label="Previous file" title="Previous file (←)">
        <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M10 3.5 5.5 8l4.5 4.5" /></svg>
      </button>
      <button type="button" class="nav next" onclick={onnext} disabled={index === count - 1} aria-label="Next file" title="Next file (→)">
        <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M6 3.5 10.5 8 6 12.5" /></svg>
      </button>
    {/if}
    {#if preview}
      <div class="media">
        <img class="subject live" class:checker={alpha} src={preview} alt="Preview" style={`max-width:${frameW}px;max-height:${frameH}px`} />
        <span class="tag">Preview</span>
      </div>
    {:else}
    {#key file.id + rev}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="media" ondblclick={onopen}>
        {#if (kind === 'image' || kind === 'video') && box && !broken}
          {#if kind === 'video' && !showOriginal && rev === file.rev}
            <!-- svelte-ignore a11y_media_has_caption -->
            <video
              class="subject"
              src={fileURL(file.id, rev)}
              poster={thumbURL(file.id, rev, thumbSize)}
              controls
              preload="metadata"
              playsinline
              style={`width:${box.w}px;height:${box.h}px`}
            ></video>
          {:else}
            <img
              class="subject"
              class:checker={alpha && kind === 'image'}
              class:pixelated={box.pixelated}
              src={thumbURL(file.id, rev, thumbSize)}
              alt={file.displayName}
              decoding="async"
              style={`width:${box.w}px;height:${box.h}px`}
              onerror={() => (broken = true)}
            />
          {/if}
        {:else if kind === 'pdf'}
          <div class="subject page">
            <PdfPage url={fileURL(file.id, rev)} maxWidth={Math.min(frameW, 720)} maxHeight={frameH} pages={info?.pages} />
          </div>
        {:else if kind === 'audio'}
          <div class="subject card audio">
            <span class="big">{duration || typeLabel(type)}</span>
            <audio src={fileURL(file.id, rev)} controls preload="metadata"></audio>
          </div>
        {:else if csv}
          <div class="subject card text">
            <table>
              <tbody>
                {#each csv as row, r}
                  <tr class:head={r === 0}>{#each row as cell}<td>{cell}</td>{/each}</tr>
                {/each}
              </tbody>
            </table>
          </div>
        {:else if info?.snippet}
          <div class="subject card text"><pre>{info.snippet}</pre></div>
        {:else if kind === 'folder'}
          <div class="subject card text">
            <ul class="entries">
              {#each (info?.entries ?? []).slice(0, 40) as e}
                <li class:dir={e.dir}><span>{e.path}{e.dir ? '/' : ''}</span>{#if !e.dir}<span class="sz">{size(e.size)}</span>{/if}</li>
              {/each}
            </ul>
          </div>
        {:else}
          <div class="subject card plain">
            <span class="big">{typeLabel(type)}</span>
            {#if info?.summary}<span class="sum">{info.summary}</span>{/if}
          </div>
        {/if}
      </div>
    {/key}
    {/if}
  </div>

  <div class="details">
    {#if canCompare}
      <div class="compare" role="radiogroup" aria-label="Show">
        <button type="button" role="radio" aria-checked={showOriginal} class:on={showOriginal} onclick={() => (showOriginal = true)}>Original</button>
        <button type="button" role="radio" aria-checked={!showOriginal} class:on={!showOriginal} onclick={() => (showOriginal = false)}>Result</button>
      </div>
    {/if}
    <h2 class="name" title={file.originalPath || file.displayName}>{file.displayName}</h2>
    <p class="meta">
      <span>{typeLabel(file.type)}</span>
      <span class="sizes">{#if was}<span class="was">{was}</span><span class="arrow">→</span>{/if}<span class:now={!!was} class:smaller>{now}</span></span>
      {#if infoLine}<span>{infoLine}</span>{/if}
      {#if count > 1}<span class="pos">{index + 1} of {count}</span>{/if}
    </p>
    {#if changed}
      <div class="chips">
        {#each file.steps ?? [] as step, i (step.id)}
          <span class="chip" in:chipIn out:chipOut>
            <Icon name={skillMeta[step.skillId]?.icon} family={skillMeta[step.skillId]?.category} size={12} />
            {step.label}
            <button type="button" aria-label={'Remove ' + step.label} title="Remove this step (the rest replays)" onclick={() => onremovestep(i)}>×</button>
          </span>
        {/each}
      </div>
    {/if}
  </div>
</div>

<style>
  .hero {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 18px;
    width: 100%;
    padding: 4px 0 0;
  }
  .frame {
    position: relative;
    width: min(100%, 920px);
    display: grid;
    place-items: center;
  }
  .media {
    display: grid;
    place-items: center;
    width: 100%;
    animation: media-in 0.45s var(--ease-out) both;
  }
  @keyframes media-in {
    from {
      opacity: 0;
      transform: scale(0.985);
    }
  }
  .subject {
    view-transition-name: subject;
    display: block;
    border-radius: 10px;
    max-width: 100%;
    max-height: 100%;
  }
  img.subject,
  video.subject {
    object-fit: contain;
    box-shadow: 0 30px 60px -30px rgb(0 0 0 / 0.55);
  }
  video.subject {
    background: #000;
  }
  img.live {
    width: auto;
    height: auto;
    object-fit: contain;
  }
  .tag {
    position: absolute;
    top: 10px;
    left: 50%;
    transform: translateX(-50%);
    padding: 3px 10px;
    border-radius: 999px;
    font-size: 11.5px;
    color: var(--ink);
    background: var(--glass-strong);
    box-shadow: inset 0 0 0 1px var(--line);
    pointer-events: none;
  }
  .pixelated {
    image-rendering: pixelated;
    border-radius: 2px;
    box-shadow: none !important;
  }
  .checker {
    background-image:
      linear-gradient(45deg, var(--check) 25%, transparent 25%),
      linear-gradient(-45deg, var(--check) 25%, transparent 25%),
      linear-gradient(45deg, transparent 75%, var(--check) 75%),
      linear-gradient(-45deg, transparent 75%, var(--check) 75%);
    background-size: 16px 16px;
    background-position:
      0 0,
      0 8px,
      8px -8px,
      -8px 0;
  }
  .page {
    border-radius: 4px;
  }
  .card {
    background: var(--well);
    box-shadow: inset 0 0 0 1px var(--line);
    border-radius: 14px;
  }
  .card.plain,
  .card.audio {
    width: min(100%, 420px);
    height: min(300px, var(--fh));
    display: grid;
    place-content: center;
    justify-items: center;
    gap: 14px;
    padding: 20px;
  }
  .big {
    font-size: 44px;
    font-weight: 200;
    letter-spacing: 0.02em;
    font-variant-numeric: tabular-nums;
  }
  .sum {
    color: var(--muted);
    font-size: 13px;
  }
  audio {
    width: min(320px, 100%);
  }
  .card.text {
    width: min(100%, 680px);
    max-height: var(--fh);
    overflow: hidden;
    padding: 18px 20px;
    -webkit-mask-image: linear-gradient(to bottom, #000 75%, transparent);
    mask-image: linear-gradient(to bottom, #000 75%, transparent);
  }
  pre {
    margin: 0;
    font: 12.5px/1.6 var(--mono);
    color: var(--ink);
    white-space: pre-wrap;
    word-break: break-word;
  }
  table {
    border-collapse: collapse;
    font-size: 12.5px;
    font-variant-numeric: tabular-nums;
  }
  td {
    padding: 5px 14px 5px 0;
    border-bottom: 1px solid var(--line);
    white-space: nowrap;
    color: var(--ink-2);
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  tr.head td {
    color: var(--ink);
    font-weight: 450;
  }
  .entries {
    list-style: none;
    margin: 0;
    padding: 0;
    font-size: 13px;
  }
  .entries li {
    display: flex;
    justify-content: space-between;
    gap: 16px;
    padding: 4px 0;
    border-bottom: 1px solid var(--line);
  }
  .entries .dir,
  .entries .sz {
    color: var(--muted);
  }

  .nav {
    position: absolute;
    top: 50%;
    z-index: 2;
    width: 36px;
    height: 36px;
    margin-top: -18px;
    display: grid;
    place-items: center;
    border-radius: 50%;
    border: 1px solid var(--line);
    background: var(--glass);
    color: var(--ink-2);
    cursor: pointer;
    transition:
      background 0.15s,
      color 0.15s,
      opacity 0.2s;
  }
  .nav svg {
    width: 16px;
    height: 16px;
    fill: none;
    stroke: currentColor;
    stroke-width: 1.5;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .nav:hover:not(:disabled) {
    background: var(--chip);
    color: var(--ink);
  }
  .nav:disabled {
    opacity: 0.3;
    cursor: default;
  }
  .prev {
    left: 0;
  }
  .next {
    right: 0;
  }

  .details {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    max-width: min(100%, 680px);
    gap: 6px;
  }
  .compare {
    display: flex;
    gap: 2px;
    padding: 2px;
    margin-bottom: 4px;
    border-radius: 999px;
    background: var(--chip);
  }
  .compare button {
    border: 0;
    background: transparent;
    color: var(--ink-2);
    font-size: 12px;
    padding: 3px 11px;
    border-radius: 999px;
    cursor: pointer;
  }
  .compare button.on {
    background: var(--seg-on);
    color: var(--ink);
    box-shadow: var(--seg-on-shadow);
  }
  .name {
    margin: 0;
    font-size: 19px;
    font-weight: 300;
    letter-spacing: -0.01em;
    line-height: 1.3;
    overflow-wrap: anywhere;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .meta {
    margin: 0;
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    column-gap: 14px;
    row-gap: 2px;
    font-size: 12.5px;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
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
    justify-content: center;
    gap: 4px;
    margin-top: 4px;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 2px 2px 2px 10px;
    border-radius: 999px;
    background: var(--chip);
    color: var(--ink-2);
    font-size: 12px;
    white-space: nowrap;
  }
  .chip button {
    border: 0;
    background: transparent;
    color: var(--muted);
    width: 20px;
    height: 20px;
    border-radius: 50%;
    font-size: 13px;
    line-height: 1;
    cursor: pointer;
    padding: 0;
  }
  .chip button:hover {
    background: var(--line-strong);
    color: var(--ink);
  }
  @media (max-height: 560px) {
    .hero {
      gap: 10px;
    }
    .name {
      font-size: 16px;
      -webkit-line-clamp: 1;
      line-clamp: 1;
    }
  }
</style>
