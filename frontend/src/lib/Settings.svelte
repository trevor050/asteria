<script lang="ts">
  import { onMount } from 'svelte'
  import { Clipboard } from '@wailsio/runtime'
  import { api, type Settings, type State } from './api'
  import { mod } from './format'

  type Props = {
    settings: Settings
    app: State
    onchange: (key: string, value: string) => void
    onpickfolder: () => void
    onopenskills: () => void
    onclose: () => void
  }
  let { settings, app, onchange, onpickfolder, onopenskills, onclose }: Props = $props()

  let panel: HTMLElement | undefined = $state()
  let accents: { label: string; value: string }[] = $state([])
  let copied = $state(false)

  function today() {
    const d = new Date()
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
  }

  // Export naming presets. Collisions always get " (1)", never an overwrite.
  const names = [
    { label: 'Same name', pattern: '{name}.{ext}', example: 'photo.jpg' },
    { label: 'Add the date', pattern: '{name} {date}.{ext}', example: `photo ${today()}.jpg` },
    { label: 'Add the last step', pattern: '{name}-{skill}.{ext}', example: 'photo-resize.jpg' },
  ]
  let customName = $derived(names.every((n) => n.pattern !== settings.namingPattern))
  let ffmpeg = $derived(app.tools?.find((t) => t.name === 'ffmpeg'))
  let accentNow = $derived(settings.accentColor.replace(/\s/g, ''))

  onMount(() => {
    panel?.focus()
    // The accent presets come from the Accent Color skill itself.
    void api.ListSkills().then((all) => {
      const p = (all ?? []).find((s) => s.id === 'set_accent_color')?.params?.[0]?.presets ?? []
      accents = p.map((x) => x as unknown as { label: string; value: string })
    })
  })

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      onclose()
    }
  }

  async function copyHint() {
    if (!ffmpeg) return
    await Clipboard.SetText(ffmpeg.hint)
    copied = true
    setTimeout(() => (copied = false), 1600)
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="catch" role="presentation" onclick={onclose}></div>
<div class="panel glass" role="dialog" aria-label="Settings" tabindex="-1" bind:this={panel} {onkeydown}>
  <header>
    <h2>Settings</h2>
    <button type="button" class="close" onclick={onclose} aria-label="Close settings" title="Close (Esc)">×</button>
  </header>

  <div class="scroll">
    <div class="group">
      <h3>Appearance</h3>
      <div class="row">
        <span class="label">Theme</span>
        <div class="seg" role="radiogroup" aria-label="Theme">
          {#each [['system', 'System'], ['light', 'Light'], ['dark', 'Dark']] as [v, l]}
            <button type="button" role="radio" aria-checked={settings.appearance === v} class:on={settings.appearance === v} onclick={() => onchange('appearance', v)}
              >{l}</button
            >
          {/each}
        </div>
      </div>
      {#if accents.length}
        <div class="row">
          <span class="label">Accent</span>
          <div class="swatches" role="radiogroup" aria-label="Accent color">
            {#each accents as a}
              <button
                type="button"
                role="radio"
                aria-checked={accentNow === a.value}
                aria-label={a.label}
                title={a.label}
                class:on={accentNow === a.value}
                style={`--sw: rgb(${a.value})`}
                onclick={() => onchange('accentColor', a.value)}
              ></button>
            {/each}
          </div>
        </div>
      {/if}
      <label class="row toggle">
        <span>
          <span class="label">Reduce motion</span>
          <span class="sub">Always on when your system asks for it</span>
        </span>
        <input type="checkbox" role="switch" checked={settings.reduceMotion} onchange={(e) => onchange('reduceMotion', String(e.currentTarget.checked))} />
      </label>
      <label class="row toggle">
        <span>
          <span class="label">Reduce transparency</span>
          <span class="sub">Solid surfaces instead of frosted glass</span>
        </span>
        <input
          type="checkbox"
          role="switch"
          checked={settings.reduceTransparency}
          onchange={(e) => onchange('reduceTransparency', String(e.currentTarget.checked))}
        />
      </label>
    </div>

    <div class="group">
      <h3>Exports</h3>
      <div class="row stack">
        <span class="label">Save to</span>
        <div class="seg wide" role="radiogroup" aria-label="Export destination">
          <button type="button" role="radio" aria-checked={!settings.outputFolder} class:on={!settings.outputFolder} onclick={() => onchange('outputFolder', '')}
            >Next to the originals</button
          >
          <button type="button" role="radio" aria-checked={!!settings.outputFolder} class:on={!!settings.outputFolder} onclick={onpickfolder}
            >A folder…</button
          >
        </div>
        {#if settings.outputFolder}
          <div class="path">
            <code title={settings.outputFolder}>{settings.outputFolder}</code>
            <button type="button" class="text" onclick={onpickfolder}>Change</button>
          </div>
        {/if}
      </div>
      <div class="row stack">
        <span class="label">File names</span>
        <div class="choices" role="radiogroup" aria-label="Export file names">
          {#each names as n}
            <button
              type="button"
              role="radio"
              aria-checked={settings.namingPattern === n.pattern}
              class:on={settings.namingPattern === n.pattern}
              onclick={() => onchange('namingPattern', n.pattern)}
            >
              <span class="dot" aria-hidden="true"></span>
              <span class="label">{n.label}</span>
              <span class="ex">{n.example}</span>
            </button>
          {/each}
          {#if customName}
            <div class="custom"><span class="dot on" aria-hidden="true"></span><span class="label">Custom</span><code>{settings.namingPattern}</code></div>
          {/if}
        </div>
        <p class="note">If a name is taken, Asteria adds (1), (2) and so on. It never overwrites a file.</p>
      </div>
    </div>

    <div class="group">
      <h3>Skills</h3>
      <div class="path">
        <code title={app.skillsDir}>{app.skillsDir}</code>
      </div>
      <div class="actions">
        <button type="button" class="btn" onclick={onopenskills}>Open folder</button>
        <button type="button" class="text" onclick={() => void api.OpenLink('skills')}>How to write a skill</button>
      </div>
      <p class="note">Each skill is one JSON file. Drop it in this folder and it loads right away.</p>
    </div>

    <div class="group">
      <h3>Audio and video</h3>
      {#if ffmpeg?.found}
        <p class="status"><span class="ok" aria-hidden="true"></span>ffmpeg found</p>
        <div class="path"><code title={ffmpeg.path}>{ffmpeg.path}</code></div>
      {:else}
        <p class="status"><span class="warn" aria-hidden="true"></span>ffmpeg not found</p>
        <p class="note">Audio and video skills need it. Install it, then reopen Asteria:</p>
        {#if ffmpeg}
          <div class="path">
            <code>{ffmpeg.hint}</code>
            <button type="button" class="text" onclick={copyHint}>{copied ? 'Copied' : 'Copy'}</button>
          </div>
        {/if}
      {/if}
    </div>

    <div class="group about">
      <p>Asteria {app.version}, free and open source.</p>
      <button type="button" class="text" onclick={() => void api.OpenLink('repository')}>Source on GitHub</button>
    </div>
  </div>
  <footer><kbd>{mod},</kbd> or <kbd>Esc</kbd> to close</footer>
</div>

<style>
  .catch {
    position: fixed;
    inset: 0;
    z-index: 54;
  }
  .panel {
    --glass-bg: var(--glass-strong);
    position: fixed;
    z-index: 55;
    top: 56px;
    right: 12px;
    bottom: 12px;
    width: min(380px, calc(100vw - 24px));
    display: flex;
    flex-direction: column;
    border: 1px solid var(--line);
    border-radius: 18px;
    box-shadow: var(--shadow-pop);
    outline: none;
    animation: panel-in 0.32s var(--ease-out) both;
  }
  @keyframes panel-in {
    from {
      opacity: 0;
      transform: translateX(14px) scale(0.99);
    }
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 16px 12px 6px 20px;
  }
  h2 {
    margin: 0;
    font-size: 17px;
    font-weight: 400;
    letter-spacing: -0.01em;
  }
  .close {
    width: 30px;
    height: 30px;
    border: 0;
    border-radius: 50%;
    background: transparent;
    color: var(--muted);
    font-size: 18px;
    line-height: 1;
    cursor: pointer;
  }
  .close:hover {
    background: var(--hover);
    color: var(--ink);
  }
  .scroll {
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 0 20px 8px;
  }
  .group {
    padding: 14px 0;
    border-top: 1px solid var(--line);
  }
  .group:first-child {
    border-top: 0;
  }
  h3 {
    margin: 0 0 10px;
    font-size: 12.5px;
    font-weight: 450;
    color: var(--muted);
  }
  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 6px 0;
  }
  .row.stack {
    flex-direction: column;
    align-items: stretch;
    gap: 8px;
  }
  .label {
    font-size: 13.5px;
    color: var(--ink);
  }
  .sub {
    display: block;
    font-size: 12px;
    color: var(--muted);
    margin-top: 1px;
  }
  .seg {
    display: flex;
    gap: 2px;
    padding: 2px;
    border-radius: 999px;
    background: var(--chip);
  }
  .seg button {
    border: 0;
    background: transparent;
    color: var(--ink-2);
    font-size: 12.5px;
    padding: 5px 12px;
    border-radius: 999px;
    cursor: pointer;
    transition:
      background 0.2s var(--ease-out),
      color 0.2s;
  }
  .seg button:hover {
    color: var(--ink);
  }
  .seg button.on {
    background: var(--glass-solid);
    color: var(--ink);
    box-shadow: 0 1px 3px rgb(0 0 0 / 0.12);
  }
  .seg.wide button {
    flex: 1;
  }
  .swatches {
    display: flex;
    gap: 8px;
  }
  .swatches button {
    width: 20px;
    height: 20px;
    padding: 0;
    border-radius: 50%;
    border: 0;
    background: color-mix(in oklab, var(--sw) 70%, var(--ink) 0%);
    box-shadow: inset 0 0 0 1px rgb(0 0 0 / 0.12);
    cursor: pointer;
    transition: transform 0.25s var(--ease-spring);
  }
  .swatches button:hover {
    transform: scale(1.12);
  }
  .swatches button.on {
    box-shadow:
      0 0 0 2px var(--glass-solid),
      0 0 0 3.5px var(--sw);
  }
  .toggle {
    cursor: pointer;
  }
  input[type='checkbox'] {
    appearance: none;
    flex: none;
    width: 34px;
    height: 20px;
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
    width: 16px;
    height: 16px;
    border-radius: 50%;
    background: var(--glass-solid);
    box-shadow: 0 1px 3px rgb(0 0 0 / 0.25);
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
  .choices {
    display: flex;
    flex-direction: column;
  }
  .choices button,
  .custom {
    display: grid;
    grid-template-columns: 14px auto 1fr;
    align-items: baseline;
    gap: 10px;
    border: 0;
    background: transparent;
    text-align: left;
    padding: 6px 8px;
    margin: 0 -8px;
    border-radius: 8px;
    cursor: pointer;
  }
  .choices button:hover {
    background: var(--hover);
  }
  .dot {
    width: 12px;
    height: 12px;
    border-radius: 50%;
    box-shadow: inset 0 0 0 1.5px var(--line-strong);
    align-self: center;
    transition: box-shadow 0.2s var(--ease-out);
  }
  .on .dot,
  .dot.on {
    box-shadow: inset 0 0 0 4px var(--accent);
  }
  .ex {
    justify-self: end;
    font-size: 12px;
    color: var(--muted);
    font-variant-numeric: tabular-nums;
  }
  .custom code {
    justify-self: end;
  }
  .note {
    margin: 4px 0 0;
    font-size: 12px;
    color: var(--muted);
    line-height: 1.5;
  }
  .path {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
  }
  .path code {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    direction: rtl;
    text-align: left;
    padding: 5px 8px;
    border-radius: 7px;
    font-size: 11.5px;
    color: var(--ink-2);
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 14px;
    margin-top: 10px;
  }
  .btn {
    border: 1px solid var(--line-strong);
    background: transparent;
    color: var(--ink);
    font-size: 12.5px;
    padding: 5px 12px;
    border-radius: 999px;
    cursor: pointer;
  }
  .btn:hover {
    background: var(--hover);
  }
  .text {
    border: 0;
    background: none;
    padding: 0;
    color: var(--ink-2);
    font-size: 12.5px;
    cursor: pointer;
    text-decoration: underline;
    text-decoration-color: var(--line-strong);
    text-underline-offset: 3px;
    white-space: nowrap;
  }
  .text:hover {
    color: var(--ink);
  }
  .status {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 8px;
    font-size: 13.5px;
  }
  .status span {
    width: 7px;
    height: 7px;
    border-radius: 50%;
  }
  .status .ok {
    background: var(--good);
  }
  .status .warn {
    background: var(--warn);
  }
  .about {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    font-size: 12.5px;
    color: var(--muted);
  }
  .about p {
    margin: 0;
  }
  footer {
    padding: 10px 20px 14px;
    font-size: 12px;
    color: var(--muted);
  }
</style>
