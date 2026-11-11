<script lang="ts">
  // Asks for an add-on the moment a file needs it, in plain words, then
  // installs it with progress and cancel. The caller continues the
  // conversion on success.
  import { onMount } from 'svelte'
  import { api, events, on, type AddonView } from './api'
  import { bytes } from './format'

  type Props = {
    addon: AddonView
    /** What the user was doing, e.g. "Convert to PDF". */
    task?: string
    onclose: () => void
    ondone: () => void
  }
  let { addon, task = '', onclose, ondone }: Props = $props()

  let phase = $state<'ask' | 'working' | 'error'>('ask')
  let step = $state('download')
  let done = $state(0)
  let total = $state(0)
  let error = $state('')
  let sheet: HTMLElement | undefined = $state()
  let manual = $derived(addon.state === 'manual' || addon.state === 'unavailable')

  onMount(() => {
    sheet?.focus()
    return on<{ engine: string; phase: string; done: number; total: number }>(events.addonProgress, (p) => {
      if (p.engine !== addon.id) return
      step = p.phase
      if (p.phase === 'download') {
        done = p.done
        total = p.total
      }
    })
  })

  async function install() {
    phase = 'working'
    step = 'download'
    done = 0
    total = addon.downloadBytes
    const res = await api.InstallAddon(addon.id)
    if (res.errorKind === 'cancelled') {
      phase = 'ask'
      return
    }
    if (res.error) {
      error = res.error
      phase = 'error'
      return
    }
    ondone()
  }

  async function recheck() {
    const all = (await api.Addons()) ?? []
    const now = all.find((x) => x.id === addon.id)
    if (now && (now.state === 'installed' || now.state === 'system')) ondone()
    else error = `Asteria still can't find ${addon.name}. If you just installed it, try again in a moment.`
  }

  function cancel() {
    void api.CancelAddon(addon.id)
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      if (phase === 'working') cancel()
      else onclose()
    }
  }

  let stepText = $derived(
    step === 'download'
      ? total
        ? `Downloading ${bytes(done)} of ${bytes(total)}`
        : 'Downloading'
      : step === 'verify'
        ? 'Checking the download'
        : step === 'unpack'
          ? 'Unpacking'
          : 'Making sure it runs',
  )
  let frac = $derived(step === 'download' ? (total ? done / total : 0) : 1)
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="scrim" role="presentation" onclick={() => phase !== 'working' && onclose()}>
  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
  <div class="sheet glass" role="dialog" aria-modal="true" aria-labelledby="addon-title" tabindex="-1" bind:this={sheet} {onkeydown} onclick={(e) => e.stopPropagation()}>
    <h2 id="addon-title">{task ? `${task} needs ${addon.name}` : `Get ${addon.name}?`}</h2>
    {#if manual}
      <p>
        {addon.name} is free, from {addon.publisher}, but Asteria can't add it for you on this computer.
        {addon.manualWhy}
      </p>
      <p class="quiet">Install it from the official page, then come back here.</p>
      {#if error}<p class="err">{error}</p>{/if}
      <div class="buttons">
        <button type="button" class="btn quiet" onclick={onclose}>Not now</button>
        <button type="button" class="btn" onclick={recheck}>I've installed it</button>
        <button type="button" class="btn primary" onclick={() => void api.OpenAddonPage(addon.id, 'download')}>Open the download page</button>
      </div>
    {:else if phase === 'ask' || phase === 'error'}
      <p>
        {addon.name} is free, from {addon.publisher}. Asteria can add it for you in one step: a
        {bytes(addon.downloadBytes)} download that takes about {bytes(addon.diskBytes)} on this computer. You only do this once, and you can remove it
        in Settings.
      </p>
      <p class="quiet">
        It downloads straight from {addon.source || `${addon.name}'s official site`}. Nothing about your files is sent anywhere.
        <button type="button" class="link" onclick={() => void api.OpenAddonPage(addon.id, 'license')}>{addon.license} license</button>
      </p>
      {#if phase === 'error'}<p class="err">{error}</p>{/if}
      <div class="buttons">
        <button type="button" class="btn quiet" onclick={onclose}>Not now</button>
        <button type="button" class="btn primary" onclick={install}>{phase === 'error' ? 'Try again' : `Get ${addon.name}`}</button>
      </div>
    {:else}
      <div class="progress" aria-live="polite">
        <div class="track"><div class="fill" style={`transform:scaleX(${Math.max(0.02, frac)})`}></div></div>
        <div class="line">
          <span>{stepText}</span>
          <button type="button" class="btn quiet" onclick={cancel}>Cancel</button>
        </div>
      </div>
    {/if}
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 65;
    display: grid;
    place-items: center;
    padding: 24px;
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
    width: min(440px, 100%);
    padding: 22px 24px 18px;
    border: 1px solid var(--line);
    border-radius: 20px;
    box-shadow: var(--shadow-pop);
    outline: none;
    animation: sheet-in 0.3s var(--ease-spring) both;
  }
  @keyframes sheet-in {
    from {
      opacity: 0;
      transform: translateY(8px) scale(0.98);
    }
  }
  h2 {
    margin: 0 0 10px;
    font-size: 17px;
    font-weight: 400;
    letter-spacing: -0.01em;
    text-wrap: balance;
  }
  p {
    margin: 0 0 10px;
    font-size: 13px;
    line-height: 1.55;
    color: var(--ink-2);
  }
  .quiet {
    font-size: 12px;
    color: var(--muted);
  }
  .err {
    color: var(--danger);
    font-size: 12.5px;
  }
  .link {
    border: 0;
    padding: 0;
    background: none;
    color: var(--ink-2);
    font-size: 12px;
    text-decoration: underline;
    text-decoration-color: var(--line-strong);
    text-underline-offset: 3px;
    cursor: pointer;
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 16px;
  }
  .btn {
    height: 30px;
    padding: 0 14px;
    border: 1px solid var(--line-strong);
    border-radius: 999px;
    background: transparent;
    color: var(--ink);
    font-size: 12.5px;
    cursor: pointer;
  }
  .btn:hover {
    background: var(--hover);
  }
  .btn.quiet {
    border-color: transparent;
    color: var(--ink-2);
  }
  .btn.primary {
    border-color: transparent;
    background: var(--seg-on);
    box-shadow: var(--seg-on-shadow);
  }
  .progress {
    margin-top: 6px;
  }
  .track {
    height: 4px;
    border-radius: 2px;
    background: var(--chip);
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: var(--accent);
    transform-origin: left;
    transition: transform 0.2s var(--ease-out);
  }
  .line {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-top: 10px;
    font-size: 12.5px;
    color: var(--ink-2);
    font-variant-numeric: tabular-nums;
  }
</style>
