<script lang="ts">
  import { onMount, tick } from 'svelte'
  import { slide } from 'svelte/transition'
  import { Clipboard } from '@wailsio/runtime'
  import { api, type Settings, type State } from './api'
  import { motionReduced } from './theme'

  type Props = {
    settings: Settings
    app: State
    onchange: (key: string, value: string) => void
    onpickfolder: () => void
    onopenskills: () => void
    onclose: () => void
  }
  let { settings, app, onchange, onpickfolder, onopenskills, onclose }: Props = $props()

  let sheet: HTMLElement | undefined = $state()
  let accents: { label: string; value: string }[] = $state([])
  let copied = $state(false)

  function today() {
    const d = new Date()
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
  }

  // One segmented style for every "pick one of a few".
  const themes = [
    { value: 'system', label: 'System' },
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
  ]
  // Collisions always get " (1)"; nothing is ever overwritten.
  const names = [
    { value: '{name}.{ext}', label: 'Same name', example: 'photo.jpg' },
    { value: '{name} {date}.{ext}', label: 'Add date', example: `photo ${today()}.jpg` },
    { value: '{name}-{skill}.{ext}', label: 'Add step', example: 'photo-resize.jpg' },
  ]
  let naming = $derived(names.find((n) => n.value === settings.namingPattern))
  let ffmpeg = $derived(app.tools?.find((t) => t.name === 'ffmpeg'))
  let accentNow = $derived(settings.accentColor.replace(/\s/g, ''))

  // A folder as people read it: home-relative, the folder itself in full,
  // the middle shortened when long.
  let folder = $derived.by(() => {
    const raw = settings.outputFolder ?? ''
    if (!raw) return null
    const sep = raw.includes('\\') && !raw.includes('/') ? '\\' : '/'
    const home = raw.replace(/^(\/Users\/[^/]+|\/home\/[^/]+|[A-Za-z]:\\Users\\[^\\]+)(?=[\\/]|$)/, '~')
    const parts = home.split(/[\\/]/).filter(Boolean)
    const name = parts.pop() ?? raw
    let parent = parts.length ? parts.join(sep) + sep : home.startsWith(sep) ? sep : ''
    if (home.startsWith(sep) && parts.length) parent = sep + parent
    if (parent.length > 28) parent = parent.slice(0, 10) + '…' + parent.slice(-14)
    return { parent, name, full: raw }
  })

  onMount(() => {
    // Focus the sheet itself: keyboard users Tab in, nobody sees a ring
    // they did not ask for.
    sheet?.focus()
    // The accent presets come from the Accent Color skill itself.
    void api.ListSkills().then((all) => {
      const p = (all ?? []).find((s) => s.id === 'set_accent_color')?.params?.[0]?.presets ?? []
      accents = p.map((x) => x as unknown as { label: string; value: string })
    })
  })

  // Keyboard: Esc closes, Tab stays inside the sheet, arrows move within a
  // segmented control or the swatches like native radio groups.
  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      onclose()
      return
    }
    if (e.key === 'Tab' && sheet) {
      const all = [...sheet.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled])')].filter(
        (el) => el.getAttribute('role') !== 'radio' || el.getAttribute('aria-checked') === 'true',
      )
      const i = all.indexOf(document.activeElement as HTMLElement)
      if (e.shiftKey && i <= 0) {
        e.preventDefault()
        all[all.length - 1]?.focus()
      } else if (!e.shiftKey && i === all.length - 1) {
        e.preventDefault()
        all[0]?.focus()
      }
    }
  }

  function arrows(e: KeyboardEvent, options: string[], current: string, apply: (v: string) => void) {
    const d = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0
    if (!d) return
    e.preventDefault()
    const i = Math.max(0, options.indexOf(current))
    const next = options[(i + d + options.length) % options.length]
    apply(next)
    void tick().then(() => (e.currentTarget as HTMLElement | null)?.querySelector<HTMLElement>('[aria-checked="true"]')?.focus())
  }

  function setDestination(v: string) {
    if (v === 'folder') {
      if (!settings.outputFolder) onpickfolder()
    } else if (settings.outputFolder) onchange('outputFolder', '')
  }

  async function copyHint() {
    if (!ffmpeg) return
    await Clipboard.SetText(ffmpeg.hint)
    copied = true
    setTimeout(() => (copied = false), 1600)
  }

  const reveal = (node: Element) => slide(node, { duration: motionReduced() ? 0 : 180 })
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onclick={onclose}>
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <div class="sheet glass" role="dialog" aria-modal="true" aria-labelledby="settings-title" tabindex="-1" bind:this={sheet} {onkeydown} onclick={(e) => e.stopPropagation()}>
    <header>
      <h2 id="settings-title">Settings</h2>
      <button type="button" class="close" onclick={onclose} aria-label="Close settings" title="Close (Esc)">
        <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" /></svg>
      </button>
    </header>

    <section aria-labelledby="s-appearance">
      <h3 id="s-appearance">Appearance</h3>
      <div class="row">
        <span class="label" id="l-theme">Theme</span>
        <!-- svelte-ignore a11y_interactive_supports_focus -->
        <div
          class="seg"
          role="radiogroup"
          aria-labelledby="l-theme"
          onkeydown={(e) =>
            arrows(
              e,
              themes.map((t) => t.value),
              settings.appearance,
              (v) => onchange('appearance', v),
            )}
        >
          {#each themes as t}
            <button
              type="button"
              role="radio"
              aria-checked={settings.appearance === t.value}
              tabindex={settings.appearance === t.value ? 0 : -1}
              onclick={() => onchange('appearance', t.value)}>{t.label}</button
            >
          {/each}
        </div>
      </div>
      {#if accents.length}
        <div class="row">
          <span class="label" id="l-accent">Accent</span>
          <!-- svelte-ignore a11y_interactive_supports_focus -->
          <div
            class="swatches"
            role="radiogroup"
            aria-labelledby="l-accent"
            onkeydown={(e) =>
              arrows(
                e,
                accents.map((a) => a.value),
                accentNow,
                (v) => onchange('accentColor', v),
              )}
          >
            {#each accents as a}
              <button
                type="button"
                role="radio"
                aria-checked={accentNow === a.value}
                aria-label={a.label}
                title={a.label}
                tabindex={accentNow === a.value || (!accents.some((x) => x.value === accentNow) && a === accents[0]) ? 0 : -1}
                style={`--sw: rgb(${a.value})`}
                onclick={() => onchange('accentColor', a.value)}
              ></button>
            {/each}
          </div>
        </div>
      {/if}
      <label class="row" title="Always on when your system asks for it">
        <span class="label">Reduce motion</span>
        <input type="checkbox" role="switch" checked={settings.reduceMotion} onchange={(e) => onchange('reduceMotion', String(e.currentTarget.checked))} />
      </label>
      <label class="row" title="Solid surfaces instead of frosted glass. Always on when your system asks for it">
        <span class="label">Reduce transparency</span>
        <input
          type="checkbox"
          role="switch"
          checked={settings.reduceTransparency}
          onchange={(e) => onchange('reduceTransparency', String(e.currentTarget.checked))}
        />
      </label>
    </section>

    <section aria-labelledby="s-exports">
      <h3 id="s-exports">Exports</h3>
      <div class="row">
        <span class="label" id="l-dest">Save to</span>
        <!-- svelte-ignore a11y_interactive_supports_focus -->
        <div
          class="seg"
          role="radiogroup"
          aria-labelledby="l-dest"
          onkeydown={(e) => arrows(e, ['originals', 'folder'], settings.outputFolder ? 'folder' : 'originals', setDestination)}
        >
          <button
            type="button"
            role="radio"
            aria-checked={!settings.outputFolder}
            tabindex={settings.outputFolder ? -1 : 0}
            onclick={() => setDestination('originals')}>Beside originals</button
          >
          <button type="button" role="radio" aria-checked={!!settings.outputFolder} tabindex={settings.outputFolder ? 0 : -1} onclick={() => setDestination('folder')}
            >A folder</button
          >
        </div>
      </div>
      {#if folder}
        <div class="row sub" transition:reveal>
          <span class="path" title={folder.full}><span class="parent">{folder.parent}</span><span class="name">{folder.name}</span></span>
          <button type="button" class="btn" onclick={onpickfolder}>Change…</button>
        </div>
      {/if}
      <div class="row">
        <span class="text">
          <span class="label" id="l-names">File names</span>
          <span class="help">{naming ? naming.example : settings.namingPattern}</span>
        </span>
        <!-- svelte-ignore a11y_interactive_supports_focus -->
        <div
          class="seg"
          role="radiogroup"
          aria-labelledby="l-names"
          onkeydown={(e) =>
            arrows(
              e,
              names.map((n) => n.value),
              settings.namingPattern,
              (v) => onchange('namingPattern', v),
            )}
        >
          {#each names as n}
            <button
              type="button"
              role="radio"
              aria-checked={settings.namingPattern === n.value}
              tabindex={settings.namingPattern === n.value || (!naming && n === names[0]) ? 0 : -1}
              title={`${n.example}. If a name is taken, Asteria adds (1); nothing is overwritten.`}
              onclick={() => onchange('namingPattern', n.value)}>{n.label}</button
            >
          {/each}
        </div>
      </div>
    </section>

    <section aria-labelledby="s-tools">
      <h3 id="s-tools">Skills and tools</h3>
      <div class="row">
        <span class="text">
          <span class="label">Your skills</span>
          <span class="help">One JSON file each, loaded right away</span>
        </span>
        <span class="actions">
          <button type="button" class="btn quiet" onclick={() => void api.OpenLink('skills')}>Guide</button>
          <button type="button" class="btn" onclick={onopenskills} title={app.skillsDir}>Open folder</button>
        </span>
      </div>
      <div class="row">
        <span class="text">
          <span class="label">ffmpeg</span>
          {#if ffmpeg?.found}
            <span class="help">Used for audio and video</span>
          {:else}
            <span class="help mono">{ffmpeg?.hint ?? 'Install ffmpeg for audio and video'}</span>
          {/if}
        </span>
        {#if ffmpeg?.found}
          <span class="status" title={ffmpeg.path}><span class="dot ok" aria-hidden="true"></span>Found</span>
        {:else}
          <span class="actions">
            <span class="status"><span class="dot warn" aria-hidden="true"></span>Not found</span>
            {#if ffmpeg?.hint}<button type="button" class="btn" onclick={copyHint}>{copied ? 'Copied' : 'Copy command'}</button>{/if}
          </span>
        {/if}
      </div>
    </section>

    <footer>
      <span>Asteria {app.version}, free and open source</span>
      <button type="button" class="btn quiet" onclick={() => void api.OpenLink('repository')}>Source</button>
    </footer>
  </div>
</div>

<style>
  /* One grid: 24px side padding, 40px rows, one right edge for controls,
     section gaps larger than row gaps. Three type levels: section heading,
     setting label, helper. */
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 55;
    display: grid;
    /* Centered for the sheet's full height, with the top edge fixed so the
       folder row can open and close without the sheet jumping. */
    justify-items: center;
    align-items: start;
    padding: max(24px, calc((100vh - 630px) / 2)) 24px 24px;
    background: var(--scrim);
    animation: fade 0.18s ease-out both;
  }
  @keyframes fade {
    from {
      opacity: 0;
    }
  }
  .sheet {
    --glass-bg: color-mix(in oklab, var(--glass-solid) 94%, transparent);
    --c-w: 252px;
    width: min(468px, 100%);
    max-height: calc(100vh - 48px);
    overflow-y: auto;
    overscroll-behavior: contain;
    scrollbar-width: none;
    padding: 6px 24px 8px;
    border: 1px solid var(--line);
    border-radius: 20px;
    box-shadow: var(--shadow-pop);
    outline: none;
    animation: sheet-in 0.3s var(--ease-spring) both;
  }
  .sheet::-webkit-scrollbar {
    display: none;
  }
  @keyframes sheet-in {
    from {
      opacity: 0;
      transform: translateY(8px) scale(0.98);
    }
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    height: 52px;
    margin-right: -10px;
  }
  h2 {
    margin: 0;
    font-size: 17px;
    font-weight: 400;
    letter-spacing: -0.01em;
  }
  .close {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    padding: 0;
    border: 0;
    border-radius: 50%;
    background: transparent;
    color: var(--muted);
    cursor: pointer;
  }
  .close svg {
    width: 14px;
    height: 14px;
    stroke: currentColor;
    stroke-width: 1.5;
    stroke-linecap: round;
  }
  .close:hover {
    background: var(--hover);
    color: var(--ink);
  }

  section {
    padding: 14px 0 12px;
    border-top: 1px solid var(--line);
  }
  h3 {
    margin: 0 0 2px;
    font-size: 13px;
    font-weight: 600;
    color: var(--ink);
    letter-spacing: 0;
  }
  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    min-height: 40px;
  }
  label.row {
    cursor: pointer;
  }
  .row.sub {
    min-height: 36px;
    margin-top: -2px;
  }
  .label {
    font-size: 13px;
    color: var(--ink);
  }
  .text {
    display: flex;
    flex-direction: column;
    gap: 1px;
    min-width: 0;
    padding: 4px 0;
  }
  .help {
    font-size: 11.5px;
    color: var(--muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .mono {
    font-family: var(--mono);
    font-size: 11px;
  }

  /* Segmented control: the only "pick one of a few" style. */
  .seg {
    flex: none;
    display: flex;
    width: var(--c-w);
    height: 28px;
    padding: 2px;
    border-radius: 999px;
    background: var(--chip);
  }
  .seg button {
    flex: 1;
    min-width: 0;
    padding: 0 8px;
    border: 0;
    border-radius: 999px;
    background: transparent;
    color: var(--ink-2);
    font-size: 12px;
    white-space: nowrap;
    cursor: pointer;
    transition:
      background 0.2s var(--ease-out),
      color 0.2s,
      box-shadow 0.2s;
  }
  .seg button:hover {
    color: var(--ink);
  }
  .seg button[aria-checked='true'] {
    background: var(--seg-on);
    color: var(--ink);
    box-shadow: var(--seg-on-shadow);
  }

  /* Accent: small, muted swatches; the selected one gets a thin ring in ink,
     like the app's other selections. */
  .swatches {
    display: flex;
    gap: 10px;
    width: var(--c-w);
    justify-content: flex-end;
    padding-right: 4px;
  }
  .swatches button {
    width: 16px;
    height: 16px;
    padding: 0;
    border: 0;
    border-radius: 50%;
    background: var(--sw);
    box-shadow: inset 0 0 0 1px rgb(0 0 0 / 0.12);
    cursor: pointer;
    transition: box-shadow 0.2s var(--ease-out);
  }
  .swatches button[aria-checked='true'] {
    box-shadow:
      0 0 0 2px var(--glass-solid),
      0 0 0 3.25px var(--ink-2);
  }

  /* Switches end on the same right edge as everything else. */
  input[type='checkbox'] {
    appearance: none;
    flex: none;
    width: 32px;
    height: 18px;
    margin: 0;
    border-radius: 999px;
    background: var(--line-strong);
    position: relative;
    cursor: pointer;
    transition: background 0.2s var(--ease-out);
  }
  input[type='checkbox']::after {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    background: var(--glass-solid);
    box-shadow: 0 1px 2px rgb(0 0 0 / 0.25);
    transition: transform 0.25s var(--ease-spring);
  }
  input[type='checkbox']:checked {
    background: var(--accent);
  }
  input[type='checkbox']:checked::after {
    transform: translateX(14px);
  }
  input[type='checkbox']:focus-visible {
    outline: 1.5px solid var(--accent);
    outline-offset: 2px;
  }

  .path {
    min-width: 0;
    display: flex;
    font-size: 12px;
    white-space: nowrap;
  }
  .parent {
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    flex: 0 1 auto;
  }
  .name {
    color: var(--ink);
    flex: none;
  }

  /* One quiet button style. */
  .actions {
    flex: none;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .btn {
    flex: none;
    height: 28px;
    padding: 0 12px;
    border: 1px solid var(--line-strong);
    border-radius: 999px;
    background: transparent;
    color: var(--ink);
    font-size: 12px;
    white-space: nowrap;
    cursor: pointer;
    transition:
      background 0.15s,
      border-color 0.15s;
  }
  .btn:hover {
    background: var(--hover);
  }
  .btn.quiet {
    border-color: transparent;
    color: var(--ink-2);
    margin-right: -12px;
  }
  .actions .btn.quiet {
    margin-right: 0;
  }
  .btn.quiet:hover {
    color: var(--ink);
  }
  .status {
    display: inline-flex;
    align-items: center;
    gap: 7px;
    font-size: 12px;
    color: var(--ink-2);
  }
  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
  }
  .dot.ok {
    background: var(--good);
  }
  .dot.warn {
    background: var(--warn);
  }

  footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    min-height: 44px;
    border-top: 1px solid var(--line);
    font-size: 11.5px;
    color: var(--muted);
  }

  @media (max-width: 520px) {
    .sheet {
      --c-w: 210px;
      padding: 6px 18px 8px;
    }
    .help {
      display: none;
    }
  }
</style>
