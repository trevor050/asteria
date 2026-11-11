<script lang="ts">
  import { onMount, tick } from 'svelte'
  import { Clipboard } from '@wailsio/runtime'
  import { api, events, on } from './lib/api'
  import type { AddResult, FileView, Progress, Result, State, Suggestion, TrustRequest } from './lib/api'
  import CommandBar from './lib/CommandBar.svelte'
  import FileTile from './lib/FileTile.svelte'
  import QuickLook from './lib/QuickLook.svelte'
  import { isTyping, mod, plural } from './lib/format'

  type Toast = { id: number; text: string; tone: 'info' | 'error'; detail?: string; actions?: { label: string; run: () => void }[] }

  let st = $state<State | null>(null)
  let files: FileView[] = $state([])
  let selected: string[] = $state([])
  let activeId = $state<string | null>(null)
  let busy = $state(false)
  let working: string[] = $state([])
  let progress = $state<Progress | null>(null)
  let toasts: Toast[] = $state([])
  let quickId = $state<string | null>(null)
  let trust = $state<{ req: TrustRequest; s: Suggestion; params: Record<string, unknown> } | null>(null)
  let menu = $state<{ x: number; y: number } | null>(null)
  let refreshKey = $state(0)
  // Skills typed while another one is running run next, in order.
  let queue: { s: Suggestion; params: Record<string, unknown> }[] = $state([])
  let bar: CommandBar | undefined = $state()
  let trayEl: HTMLElement | undefined = $state()
  // Presentation only: where the newest batch starts (for the staggered
  // entrance), how many skills exist (shown while dragging files in), and a
  // short glow after an operation finishes.
  let addedFrom = $state(0)
  let skillCount = $state(0)
  let settled = $state(false)
  let settledTimer: ReturnType<typeof setTimeout> | undefined

  let settings = $derived(st?.settings)
  let perFile = $derived(settings?.mode === 'per_file')
  let targets = $derived.by(() => {
    if (perFile) return activeId ? [activeId] : files.slice(0, 1).map((f) => f.id)
    if (selected.length) return files.filter((f) => selected.includes(f.id)).map((f) => f.id)
    return files.map((f) => f.id)
  })
  let exportable = $derived(files.filter((f) => targets.includes(f.id) && ((f.steps?.length ?? 0) > 0 || f.origin === 'combined')))
  let quickFile = $derived(files.find((f) => f.id === quickId) ?? null)
  let ffmpeg = $derived(st?.tools?.find((t) => t.name === 'ffmpeg'))
  let outputLabel = $derived(
    settings?.outputFolder ? settings.outputFolder.split(/[\\/]/).filter(Boolean).slice(-2).join('/') : 'next to the originals',
  )

  // ---- toasts --------------------------------------------------------------

  let toastSeq = 0
  function toast(text: string, opts: Partial<Omit<Toast, 'id' | 'text'>> = {}) {
    const t: Toast = { id: ++toastSeq, text, tone: opts.tone ?? 'info', detail: opts.detail, actions: opts.actions }
    toasts = [...toasts.slice(-2), t]
    const ttl = t.tone === 'error' ? 9000 : t.actions?.length ? 7000 : 3500
    setTimeout(() => dismiss(t.id), ttl)
  }
  function dismiss(id: number) {
    toasts = toasts.filter((t) => t.id !== id)
  }

  // ---- accent --------------------------------------------------------------

  function applyAccent(raw?: string) {
    const m = (raw ?? '').match(/^\s*(\d{1,3})\s*[, ]\s*(\d{1,3})\s*[, ]\s*(\d{1,3})\s*$/)
    const hex = (raw ?? '').match(/^#?([0-9a-f]{6})$/i)
    let rgb = [99, 102, 241]
    if (m) rgb = [m[1], m[2], m[3]].map((v) => Math.min(255, Number(v)))
    else if (hex) rgb = [0, 2, 4].map((i) => parseInt(hex[1].slice(i, i + 2), 16))
    document.documentElement.style.setProperty('--accent-rgb', rgb.join(' '))
  }

  // ---- files ---------------------------------------------------------------

  function onAdded(res: AddResult) {
    const added = res.added ?? []
    if (added.length) {
      addedFrom = files.length
      files = [...files, ...added]
      if (!activeId) activeId = added[0].id
      refreshKey++
    }
    if (res.errors?.length) {
      toast(`Couldn't add ${plural(res.errors.length, 'file')}`, { tone: 'error', detail: res.errors.slice(0, 3).join('\n') })
    }
    if (res.duplicates) toast(`${plural(res.duplicates, 'file')} already in the tray`)
    if (res.truncated) toast('Stopped at 5,000 files from that folder', { tone: 'error' })
    void tick().then(() => bar?.focus())
  }

  async function pickFiles() {
    try {
      onAdded(await api.PickFiles())
    } catch (e) {
      toast(String(e), { tone: 'error' })
    }
  }

  async function syncFiles() {
    const s = await api.GetState()
    st = s
    files = s.files ?? []
    const ids = new Set(files.map((f) => f.id))
    selected = selected.filter((id) => ids.has(id))
    if (activeId && !ids.has(activeId)) activeId = files[0]?.id ?? null
    if (quickId && !ids.has(quickId)) quickId = null
    refreshKey++
  }

  async function removeFromTray(ids: string[]) {
    if (!ids.length || busy) return
    await api.RemoveFiles(ids)
    files = files.filter((f) => !ids.includes(f.id))
    selected = selected.filter((id) => !ids.includes(id))
    if (activeId && ids.includes(activeId)) activeId = files[0]?.id ?? null
    refreshKey++
  }

  // ---- running skills -------------------------------------------------------

  async function handle(res: Result, s?: Suggestion, params?: Record<string, unknown>) {
    const out = res.outcome
    if (res.error) {
      if (res.errorKind === 'trust' && res.trust && s) {
        trust = { req: res.trust, s, params: params ?? {} }
        return
      }
      if (res.errorKind === 'missing-tool' && res.tool) {
        const hint = res.tool.hint
        toast(`${s?.name ?? 'This skill'} needs ${res.tool.name}`, {
          tone: 'error',
          detail: hint,
          actions: [{ label: 'Copy command', run: () => void Clipboard.SetText(hint) }],
        })
        return
      }
      toast(res.error, { tone: 'error' })
      return
    }
    if (res.settings && st) {
      st = { ...st, settings: res.settings }
      applyAccent(res.settings.accentColor)
    }
    if (out.added?.length || out.removed?.length) {
      await syncFiles()
      if (out.added?.length) activeId = out.added[0].id
    } else if (out.updated?.length) {
      const map = new Map(out.updated.map((f) => [f.id, f]))
      files = files.map((f) => map.get(f.id) ?? f)
      refreshKey++
    }
    if (res.exported?.length) {
      const first = res.exported[0].outputPath
      toast(out.message, { actions: [{ label: 'Show in folder', run: () => void api.Reveal(first) }] })
    } else if (out.message) {
      const undoable = s && !s.isMeta && (out.updated?.length || out.added?.length) && !out.cancelled
      toast(out.message, undoable ? { actions: [{ label: 'Undo', run: undo }] } : {})
    }
    if (out.failed?.length) {
      toast(`${plural(out.failed.length, 'file')} failed`, {
        tone: 'error',
        detail: out.failed
          .slice(0, 3)
          .map((p) => `${p.name}: ${p.reason}`)
          .join('\n'),
      })
    }
  }

  async function busyRun(ids: string[], fn: () => Promise<void>, label = '') {
    if (busy) {
      toast('Still working on the last one. Press Esc to cancel it.')
      return
    }
    busy = true
    working = ids
    progress = label ? { label, done: 0, total: ids.length, fraction: 0, current: '' } : null
    try {
      await fn()
    } catch (e) {
      toast(String(e), { tone: 'error' })
    } finally {
      busy = false
      working = []
      progress = null
      settled = true
      clearTimeout(settledTimer)
      settledTimer = setTimeout(() => (settled = false), 1600)
    }
    const next = queue.shift()
    if (next) {
      queue = queue
      run(next.s, next.params)
    }
  }

  function run(s: Suggestion, params: Record<string, unknown>) {
    if (!s.isMeta && !files.length) {
      toast('Add some files first: drop them here or press ' + mod + 'O')
      return
    }
    if (busy) {
      queue = [...queue, { s, params }]
      toast(`Queued ${s.name}${Object.values(params)[0] !== undefined ? ' ' + Object.values(params)[0] : ''}`)
      return
    }
    const ids = targets
    void busyRun(s.isMeta ? [] : ids, async () => {
      const res = await api.Apply(ids, s.id, params)
      await handle(res, s, params)
    }, s.isMeta ? '' : s.name)
  }

  async function allowAndRun() {
    if (!trust) return
    const t = trust
    trust = null
    await api.TrustSkill(t.req.skillId)
    run(t.s, t.params)
  }

  function removeStep(fileId: string, index: number) {
    void busyRun([fileId], async () => handle(await api.RemoveStep(fileId, index)), 'Replaying')
  }

  function undo() {
    void busyRun([], async () => handle(await api.Undo()))
  }

  function exportFiles() {
    if (!exportable.length) {
      toast('Nothing to export yet: apply a skill first')
      return
    }
    const ids = exportable.map((f) => f.id)
    void busyRun([], async () => handle(await api.Export(ids)), 'Exporting')
  }

  async function pickOutput() {
    await handle(await api.PickOutputFolder())
  }

  async function resetOutput() {
    await handle(await api.UpdateSettings('outputFolder', ''))
  }

  async function setMode(mode: 'batch' | 'per_file') {
    await handle(await api.UpdateSettings('mode', mode))
    refreshKey++
  }

  // ---- selection -----------------------------------------------------------

  function select(id: string, e: MouseEvent) {
    menu = null
    if (e.metaKey || e.ctrlKey) {
      const base = selected.length ? selected : activeId && activeId !== id ? [activeId] : []
      selected = base.includes(id) ? base.filter((x) => x !== id) : [...base, id]
    } else if (e.shiftKey && activeId) {
      const a = files.findIndex((f) => f.id === activeId)
      const b = files.findIndex((f) => f.id === id)
      const [lo, hi] = a < b ? [a, b] : [b, a]
      selected = files.slice(lo, hi + 1).map((f) => f.id)
    } else {
      selected = []
    }
    activeId = id
  }

  function moveActive(delta: number) {
    if (!files.length) return
    const i = Math.max(0, files.findIndex((f) => f.id === activeId))
    const next = files[Math.min(files.length - 1, Math.max(0, i + delta))]
    activeId = next.id
    if (quickId) quickId = next.id
    trayEl?.querySelector(`[data-id="${next.id}"]`)?.scrollIntoView({ block: 'nearest' })
  }

  function columns(): number {
    const tile = trayEl?.querySelector('.grid > *') as HTMLElement | null
    if (!tile || !trayEl) return 1
    return Math.max(1, Math.round((trayEl.querySelector('.grid') as HTMLElement).clientWidth / tile.offsetWidth))
  }

  function openMenu(id: string, e: MouseEvent) {
    e.preventDefault()
    if (!selected.includes(id)) {
      selected = []
      activeId = id
    }
    menu = { x: Math.min(e.clientX, window.innerWidth - 220), y: Math.min(e.clientY, window.innerHeight - 260) }
  }

  let menuIds = $derived(selected.length ? selected : activeId ? [activeId] : [])

  async function copyPaths() {
    const paths = files.filter((f) => menuIds.includes(f.id)).map((f) => f.originalPath || f.displayName)
    await Clipboard.SetText(paths.join('\n'))
    toast(`Copied ${plural(paths.length, 'path')}`)
    menu = null
  }

  async function countSkills() {
    const all = (await api.Search('', [])) ?? []
    skillCount = all.filter((s) => !s.isMeta).length
  }

  let phase = $derived(busy ? 'working' : settled ? 'settled' : files.length ? 'ready' : 'idle')

  // ---- keyboard ------------------------------------------------------------

  function onkeydown(e: KeyboardEvent) {
    const cmd = e.metaKey || e.ctrlKey
    const typing = isTyping(e.target)
    const key = e.key.toLowerCase()
    if (trust || quickId) return
    if (cmd && key === 'o') {
      e.preventDefault()
      void pickFiles()
    } else if (cmd && key === 'k') {
      e.preventDefault()
      bar?.focus()
    } else if (cmd && e.key === 'Enter') {
      e.preventDefault()
      exportFiles()
    } else if (cmd && key === 'z' && !e.shiftKey && !(typing && (e.target as HTMLInputElement).value)) {
      e.preventDefault()
      undo()
    } else if (cmd && key === 'a' && !(typing && (e.target as HTMLInputElement).value)) {
      e.preventDefault()
      selected = files.map((f) => f.id)
    } else if (cmd && key === 'y' && activeId) {
      e.preventDefault()
      quickId = activeId
    } else if (e.key === 'Escape') {
      if (busy) {
        e.preventDefault()
        queue = []
        void api.Cancel()
      } else if (menu) {
        menu = null
      } else if (selected.length) {
        selected = []
      }
    } else if (!typing) {
      if (e.key === ' ' && activeId) {
        e.preventDefault()
        quickId = activeId
      } else if (e.key === 'ArrowRight') moveActive(1)
      else if (e.key === 'ArrowLeft') moveActive(-1)
      else if (e.key === 'ArrowDown') moveActive(columns())
      else if (e.key === 'ArrowUp') moveActive(-columns())
      else if ((e.key === 'Backspace' || e.key === 'Delete') && menuIds.length) {
        e.preventDefault()
        void removeFromTray(menuIds)
      } else if (e.key.length === 1 && !cmd && !e.altKey) {
        e.preventDefault()
        bar?.focus(e.key)
      }
    }
  }

  onMount(() => {
    const offs = [
      on<Progress>(events.progress, (p) => {
        if (busy) progress = p
      }),
      on<AddResult>(events.filesAdded, onAdded),
      on<string[] | null>(events.skillsChanged, (problems) => {
        refreshKey++
        void countSkills()
        if (problems?.length) toast(`${plural(problems.length, 'skill')} didn't load`, { tone: 'error', detail: problems.join('\n') })
        else toast('Skills reloaded')
      }),
    ]
    void (async () => {
      await syncFiles()
      applyAccent(st?.settings.accentColor)
      if (st?.problems?.length) {
        toast(`${plural(st.problems.length, 'custom skill')} didn't load`, { tone: 'error', detail: st.problems.join('\n') })
      }
      bar?.focus()
      void countSkills()
    })()
    // Test hook: add paths exactly like a drop does (used by the screenshot
    // and GIF scripts against the server-mode build).
    ;(window as unknown as { __asteriaAdd: (p: string[]) => Promise<void> }).__asteriaAdd = async (paths) =>
      onAdded(await api.AddPaths(paths))
    const closeMenu = () => (menu = null)
    window.addEventListener('click', closeMenu)
    return () => {
      offs.forEach((off) => off())
      window.removeEventListener('click', closeMenu)
    }
  })
</script>

<svelte:window {onkeydown} />

<div class="shell" class:mac={st?.platform === 'darwin'} data-phase={phase}>
  <div class="aura" aria-hidden="true">
    <div class="blob teal"></div>
    <div class="blob amber"></div>
    <div class="blob slate"></div>
  </div>

  <header class="titlebar">
    {#if files.length}
      <div class="tally">
        <span>{plural(files.length, 'file')}{selected.length ? `, ${selected.length} selected` : ''}</span>
        <button type="button" class="quiet" onclick={() => removeFromTray(files.map((f) => f.id))} disabled={busy}>Clear</button>
      </div>
      <div class="mode" role="radiogroup" aria-label="Apply skills to">
        <button type="button" role="radio" aria-checked={!perFile} class:on={!perFile} onclick={() => setMode('batch')}>
          {selected.length ? 'Selected' : 'All files'}
        </button>
        <button type="button" role="radio" aria-checked={perFile} class:on={perFile} onclick={() => setMode('per_file')}>Focused file</button>
      </div>
    {/if}
  </header>

  <div class="stage">
    <main class="tray" data-file-drop-target bind:this={trayEl} aria-label="Files">
      {#if !files.length}
        <div class="empty">
          <h1>Drop files to begin</h1>
          <p class="lead">
            or <button type="button" class="link" onclick={pickFiles}>choose files</button> <kbd>{mod}O</kbd>, then type what you want
          </p>
          <dl class="examples">
            <div><dt>heic to jpg</dt><dd>iPhone photos that open everywhere</dd></div>
            <div><dt>resize 50%</dt><dd>or 1920, or 1920x1080</dd></div>
            <div><dt>merge pdfs</dt><dd>in the order shown</dd></div>
            <div><dt>trim 0:05-0:30</dt><dd>audio and video</dd></div>
            <div><dt>csv to json</dt><dd>and YAML, Markdown</dd></div>
            <div><dt>zip</dt><dd>or unzip</dd></div>
          </dl>
          <p class="local">Nothing is uploaded. Every conversion runs on this computer, and your originals are never changed.</p>
          {#if ffmpeg && !ffmpeg.found}
            <p class="note">Audio and video skills need ffmpeg: <code>{ffmpeg.hint}</code></p>
          {/if}
        </div>
      {:else}
        <div class="grid" role="listbox" aria-multiselectable="true" aria-label="Files in the tray">
          {#each files as file, i (file.id)}
            <FileTile
              {file}
              enter={Math.max(0, Math.min(i - addedFrom, 14))}
              selected={selected.includes(file.id)}
              active={activeId === file.id && (perFile || files.length > 1)}
              working={working.includes(file.id)}
              onselect={(e) => select(file.id, e)}
              onopen={() => (quickId = file.id)}
              onremovestep={(i) => removeStep(file.id, i)}
              oncontext={(e) => openMenu(file.id, e)}
            />
          {/each}
        </div>
      {/if}
    </main>
    <div class="drop-hint" aria-hidden="true">
      <p class="drop-title">Let go to add</p>
      {#if skillCount}<p class="drop-sub">{skillCount} skills ready, all running on this computer</p>{/if}
    </div>
  </div>

  <section class="dock">
    {#if busy && progress}
      <div class="progress" aria-live="polite">
        <span class="label">
          {progress.label}{#if progress.total > 1}<span class="count">{progress.done} of {progress.total}</span>{/if}{#if progress.current}<span class="cur">{progress.current}</span>{/if}
        </span>
        {#if queue.length}<span class="queued">then {queue.map((q) => q.s.name).join(', ')}</span>{/if}
        <button
          type="button"
          class="cancel"
          onclick={() => {
            queue = []
            void api.Cancel()
          }}>Cancel <kbd>Esc</kbd></button
        >
        <div class="track"><div class="fill" style={`transform:scaleX(${Math.max(0.02, progress.fraction || 0)})`}></div></div>
      </div>
    {/if}
    <div class="row">
      <CommandBar bind:this={bar} targetIds={targets} hasFiles={files.length > 0} {busy} {refreshKey} onrun={run} />
      <button
        type="button"
        class="export"
        class:ready={exportable.length > 0 && !busy}
        onclick={exportFiles}
        disabled={!exportable.length || busy}
        title="Export changed files"
      >
        <span>Export{exportable.length ? ` ${exportable.length}` : ''}</span>
        <kbd>{mod}↵</kbd>
      </button>
    </div>
    <div class="dest">
      <span>Export to</span>
      <button type="button" class="quiet" onclick={pickOutput} title={settings?.outputFolder || 'Each export is saved beside its original'}>{outputLabel}</button>
      {#if settings?.outputFolder}
        <button type="button" class="x" onclick={resetOutput} aria-label="Export next to originals instead">×</button>
      {/if}
    </div>
  </section>

  {#if toasts.length}
    <div class="toasts" aria-live="polite">
      {#each toasts as t (t.id)}
        <div class="toast glass" class:error={t.tone === 'error'}>
          <div class="text">
            {t.text}
            {#if t.detail}<pre>{t.detail}</pre>{/if}
          </div>
          {#each t.actions ?? [] as a}
            <button
              type="button"
              class="act"
              onclick={() => {
                a.run()
                dismiss(t.id)
              }}>{a.label}</button
            >
          {/each}
          <button type="button" class="x" aria-label="Dismiss" onclick={() => dismiss(t.id)}>×</button>
        </div>
      {/each}
    </div>
  {/if}

  {#if menu}
    <div class="menu glass" role="menu" style={`left:${menu.x}px;top:${menu.y}px`}>
      <button type="button" role="menuitem" onclick={() => ((quickId = activeId), (menu = null))}>Preview <kbd>Space</kbd></button>
      <button
        type="button"
        role="menuitem"
        onclick={() => {
          const f = files.find((x) => x.id === activeId)
          if (f?.originalPath) void api.Reveal(f.originalPath)
          menu = null
        }}>Show original in folder</button
      >
      <button type="button" role="menuitem" onclick={copyPaths}>Copy {menuIds.length > 1 ? 'paths' : 'path'}</button>
      <hr />
      <button type="button" role="menuitem" onclick={() => ((selected = files.map((f) => f.id)), (menu = null))}>Select all <kbd>{mod}A</kbd></button>
      <button type="button" role="menuitem" onclick={() => ((selected = files.filter((f) => !menuIds.includes(f.id)).map((f) => f.id)), (menu = null))}>Invert selection</button>
      <hr />
      <button type="button" role="menuitem" class="danger" onclick={() => ((menu = null), removeFromTray(menuIds))}>
        Remove {menuIds.length > 1 ? plural(menuIds.length, 'file') : ''} from tray <kbd>⌫</kbd>
      </button>
    </div>
  {/if}

  {#if quickFile}
    <QuickLook
      file={quickFile}
      onclose={() => {
        quickId = null
        bar?.focus()
      }}
      onstep={(d) => moveActive(d)}
      onreveal={(p) => void api.Reveal(p)}
    />
  {/if}

  {#if trust}
    <div class="scrim">
      <div class="dialog glass" role="alertdialog" aria-modal="true" aria-labelledby="trust-title">
        <h2 id="trust-title">Allow “{trust.req.skillName}”?</h2>
        <p>This skill comes from your skills folder, not from Asteria. It asks for:</p>
        <ul>
          {#each trust.req.permissions ?? [] as p}
            <li>
              <code>{p}</code>
              {p === 'tools.exec.any' ? 'run any program' : p === 'network' ? 'use the network' : p === 'files.anywhere' ? 'read and write files outside the tray' : 'system access'}
            </li>
          {/each}
        </ul>
        {#if trust.req.command}<p>It runs <code>{trust.req.command}</code>. Only allow skills you wrote or trust.</p>{/if}
        <div class="buttons">
          <button type="button" class="ghost" onclick={() => (trust = null)}>Cancel</button>
          <button type="button" class="primary" onclick={allowAndRun}>Allow and run</button>
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  .shell {
    position: relative;
    height: 100vh;
    display: grid;
    grid-template-rows: auto 1fr auto;
    isolation: isolate;
  }

  /* ---- ambient wash ------------------------------------------------------
     Three soft radial glows behind the top of the window. They only ever
     change opacity and transform, so they cost a composite, not a repaint. */
  .aura {
    position: fixed;
    inset: 0;
    z-index: -1;
    pointer-events: none;
    overflow: hidden;
    opacity: var(--aura-strength);
    transition: opacity 0.9s var(--ease-out);
  }
  .blob {
    position: absolute;
    border-radius: 50%;
    transition: opacity 1.4s var(--ease-out);
    will-change: transform;
  }
  .teal {
    width: 72vw;
    height: 78vh;
    left: -22vw;
    top: -40vh;
    background: radial-gradient(closest-side, rgb(var(--aura-teal)), rgb(var(--aura-teal) / 0.45) 45%, rgb(var(--aura-teal) / 0.12) 75%, transparent);
    animation: drift-a 42s ease-in-out infinite alternate;
  }
  .amber {
    width: 66vw;
    height: 74vh;
    right: -20vw;
    top: -36vh;
    background: radial-gradient(closest-side, rgb(var(--aura-amber)), rgb(var(--aura-amber) / 0.42) 45%, rgb(var(--aura-amber) / 0.1) 75%, transparent);
    animation: drift-b 50s ease-in-out infinite alternate;
  }
  .slate {
    width: 84vw;
    height: 62vh;
    left: 8vw;
    top: -44vh;
    background: radial-gradient(closest-side, rgb(var(--aura-slate)), rgb(var(--aura-slate) / 0.4) 50%, transparent);
    animation: drift-c 58s ease-in-out infinite alternate;
  }
  @keyframes drift-a {
    to {
      transform: translate3d(9vw, 5vh, 0) scale(1.08);
    }
  }
  @keyframes drift-b {
    to {
      transform: translate3d(-8vw, 7vh, 0) scale(0.94);
    }
  }
  @keyframes drift-c {
    to {
      transform: translate3d(-5vw, 4vh, 0) scale(1.1);
    }
  }
  /* The wash answers what the app is doing. */
  [data-phase='idle'] .teal {
    opacity: 0.95;
  }
  [data-phase='ready'] .teal,
  [data-phase='ready'] .amber {
    opacity: 0.7;
  }
  [data-phase='working'] .amber {
    opacity: 1;
  }
  [data-phase='working'] .teal {
    opacity: 0.55;
  }
  [data-phase='settled'] .teal {
    opacity: 1;
  }
  [data-phase='settled'] .amber {
    opacity: 0.5;
  }
  .shell:has(.tray:global(.file-drop-target-active)) .aura {
    opacity: calc(var(--aura-strength) * 1.8);
  }

  /* ---- title bar ---------------------------------------------------------- */
  .titlebar {
    --wails-draggable: drag;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    height: 46px;
    padding: 0 14px 0 20px;
    font-size: 12.5px;
    color: var(--muted);
  }
  .mac .titlebar {
    padding-left: 88px;
  }
  .tally {
    --wails-draggable: no-drag;
    display: flex;
    align-items: baseline;
    gap: 12px;
    font-variant-numeric: tabular-nums;
    animation: rise 0.4s var(--ease-out) both;
  }
  .mode {
    --wails-draggable: no-drag;
    display: flex;
    gap: 2px;
    margin-left: auto;
    animation: rise 0.4s var(--ease-out) both;
  }
  .mode button {
    border: 0;
    background: transparent;
    color: var(--muted);
    font-size: 12.5px;
    padding: 4px 11px;
    border-radius: 999px;
    cursor: pointer;
    transition:
      background 0.2s var(--ease-out),
      color 0.2s;
  }
  .mode button:hover {
    color: var(--ink-2);
  }
  .mode button.on {
    background: var(--chip);
    color: var(--ink);
  }

  /* ---- tray --------------------------------------------------------------- */
  .stage {
    position: relative;
    min-height: 0;
  }
  .tray {
    height: 100%;
    overflow: auto;
    padding: 8px 22px 28px;
    -webkit-mask-image: linear-gradient(to bottom, transparent, #000 10px, #000 calc(100% - 26px), transparent);
    mask-image: linear-gradient(to bottom, transparent, #000 10px, #000 calc(100% - 26px), transparent);
    transition:
      opacity 0.25s var(--ease-out),
      filter 0.25s var(--ease-out);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(178px, 1fr));
    gap: 20px 14px;
    align-items: start;
  }

  .drop-hint {
    position: absolute;
    inset: 0;
    display: grid;
    place-content: center;
    text-align: center;
    pointer-events: none;
    opacity: 0;
    transform: scale(0.985);
    transition:
      opacity 0.22s var(--ease-out),
      transform 0.35s var(--ease-spring);
  }
  .drop-hint::before {
    content: '';
    position: absolute;
    inset: 12% 18%;
    z-index: -1;
    border-radius: 50%;
    background: radial-gradient(closest-side, var(--accent-soft), transparent);
  }
  .drop-title {
    margin: 0;
    font-size: clamp(28px, 5vw, 40px);
    font-weight: 200;
    letter-spacing: -0.025em;
    color: var(--ink);
  }
  .drop-sub {
    margin: 8px 0 0;
    font-size: 14px;
    font-weight: 300;
    color: var(--ink-2);
  }
  .stage:has(.tray:global(.file-drop-target-active)) .drop-hint {
    opacity: 1;
    transform: none;
  }
  .stage:has(.tray:global(.file-drop-target-active)) .tray {
    opacity: 0.18;
  }

  /* ---- empty state -------------------------------------------------------- */
  .empty {
    min-height: 100%;
    display: flex;
    flex-direction: column;
    justify-content: center;
    max-width: 500px;
    margin: 0 auto;
    padding: 24px 4px 40px;
  }
  .empty > * {
    animation: rise 0.7s var(--ease-out) both;
  }
  .empty > :nth-child(2) {
    animation-delay: 60ms;
  }
  .empty > :nth-child(3) {
    animation-delay: 120ms;
  }
  .empty > :nth-child(n + 4) {
    animation-delay: 180ms;
  }
  .empty h1 {
    margin: 0;
    font-size: clamp(32px, 6vw, 46px);
    font-weight: 200;
    letter-spacing: -0.035em;
    line-height: 1.05;
  }
  .lead {
    margin: 14px 0 30px;
    color: var(--ink-2);
    font-size: 15px;
    font-weight: 300;
  }
  .lead kbd {
    margin: 0 2px;
  }
  .examples {
    margin: 0;
    display: grid;
    gap: 0;
  }
  .examples div {
    display: grid;
    grid-template-columns: 9.5rem 1fr;
    align-items: baseline;
    gap: 16px;
    padding: 7px 0;
    border-top: 1px solid var(--line);
  }
  .examples div:last-child {
    border-bottom: 1px solid var(--line);
  }
  .examples dt {
    font-size: 13.5px;
    font-weight: 400;
    color: var(--ink);
  }
  .examples dd {
    margin: 0;
    color: var(--muted);
    font-size: 13px;
  }
  .local {
    margin: 26px 0 0;
    font-size: 12.5px;
    color: var(--muted);
    max-width: 44ch;
  }
  .note {
    margin: 10px 0 0;
    font-size: 12.5px;
    color: var(--warn);
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(6px);
    }
  }

  /* ---- dock --------------------------------------------------------------- */
  .dock {
    position: relative;
    width: 100%;
    max-width: 920px;
    margin: 0 auto;
    padding: 4px 20px 10px;
  }
  .row {
    display: flex;
    align-items: flex-end;
    gap: 10px;
  }
  .export {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 10px;
    height: 52px;
    padding: 0 16px 0 20px;
    border-radius: 16px;
    border: 1px solid var(--line);
    background: transparent;
    color: var(--muted);
    font-size: 14px;
    font-weight: 450;
    cursor: pointer;
    transition:
      background 0.3s var(--ease-out),
      color 0.3s var(--ease-out),
      border-color 0.3s,
      transform 0.25s var(--ease-spring);
  }
  .export kbd {
    background: transparent;
    color: inherit;
    opacity: 0.7;
    padding: 0;
  }
  .export.ready {
    background: var(--primary-bg);
    border-color: transparent;
    color: var(--primary-ink);
  }
  .export.ready kbd {
    opacity: 0.55;
  }
  .export.ready:hover {
    transform: translateY(-1px);
  }
  .export.ready:active {
    transform: scale(0.97);
  }
  .export:disabled {
    cursor: default;
  }
  .dest {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    margin-top: 7px;
    padding-left: 18px;
    font-size: 12px;
    color: var(--muted);
  }
  .dest .quiet {
    color: var(--ink-2);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 300px;
  }

  .progress {
    position: relative;
    display: flex;
    align-items: baseline;
    gap: 12px;
    padding: 0 6px 10px 18px;
    font-size: 12.5px;
    color: var(--muted);
    animation: rise 0.25s var(--ease-out) both;
  }
  .progress .label {
    flex: 1;
    min-width: 0;
    color: var(--ink);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-variant-numeric: tabular-nums;
  }
  .progress .count,
  .progress .cur {
    margin-left: 10px;
    color: var(--muted);
  }
  .queued {
    color: var(--muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 240px;
  }
  .cancel {
    border: 0;
    background: transparent;
    color: var(--ink-2);
    font-size: 12.5px;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 2px 4px;
    border-radius: 6px;
  }
  .cancel:hover {
    color: var(--ink);
  }
  .track {
    position: absolute;
    left: 18px;
    right: 6px;
    bottom: 4px;
    height: 1px;
    background: var(--line);
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: var(--accent);
    box-shadow: 0 0 8px var(--accent);
    transform-origin: left;
    transition: transform 0.25s var(--ease-out);
  }

  /* ---- shared quiet controls --------------------------------------------- */
  .quiet,
  .link {
    border: 0;
    padding: 0;
    background: none;
    cursor: pointer;
  }
  .quiet {
    color: var(--ink-2);
    transition: color 0.15s;
  }
  .quiet:hover {
    color: var(--ink);
  }
  .quiet:disabled {
    opacity: 0.45;
    cursor: default;
  }
  .link {
    color: var(--ink);
    text-decoration: underline;
    text-decoration-color: var(--line-strong);
    text-decoration-thickness: 1px;
    text-underline-offset: 4px;
    transition: text-decoration-color 0.2s;
  }
  .link:hover {
    text-decoration-color: var(--accent);
  }
  .x {
    border: 0;
    background: transparent;
    color: var(--muted);
    cursor: pointer;
    font-size: 15px;
    line-height: 1;
    padding: 2px 5px;
    border-radius: 6px;
  }
  .x:hover {
    color: var(--ink);
    background: var(--hover);
  }
  .primary,
  .ghost {
    font-size: 13px;
    font-weight: 450;
    border-radius: 10px;
    padding: 8px 14px;
    cursor: pointer;
  }
  .primary {
    border: 0;
    background: var(--primary-bg);
    color: var(--primary-ink);
  }
  .ghost {
    border: 1px solid var(--line-strong);
    background: transparent;
    color: var(--ink);
  }

  /* ---- toasts ------------------------------------------------------------- */
  .toasts {
    position: fixed;
    right: 16px;
    top: 50px;
    display: flex;
    flex-direction: column;
    align-items: flex-end;
    gap: 8px;
    z-index: 40;
    max-width: min(400px, calc(100vw - 36px));
  }
  .toast {
    --glass-bg: var(--glass-strong);
    display: flex;
    align-items: flex-start;
    gap: 6px;
    padding: 10px 8px 10px 14px;
    border-radius: 14px;
    border: 1px solid var(--line);
    box-shadow: var(--shadow-pop);
    font-size: 13px;
    animation: toast-in 0.32s var(--ease-spring) both;
  }
  @keyframes toast-in {
    from {
      opacity: 0;
      transform: translateY(-6px) scale(0.97);
    }
  }
  .toast .text {
    flex: 1;
    min-width: 0;
    padding: 2px 4px 2px 0;
  }
  .toast.error .text::before {
    content: '';
    display: inline-block;
    width: 6px;
    height: 6px;
    margin: 0 8px 1px 0;
    border-radius: 50%;
    background: var(--danger);
    vertical-align: middle;
  }
  .toast pre {
    margin: 5px 0 0;
    font-family: var(--mono);
    font-size: 11.5px;
    color: var(--muted);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .toast .act {
    font-size: 12.5px;
    font-weight: 450;
    border: 0;
    background: var(--chip);
    color: var(--ink);
    border-radius: 8px;
    padding: 4px 10px;
    cursor: pointer;
    white-space: nowrap;
    transition: background 0.15s;
  }
  .toast .act:hover {
    background: var(--line-strong);
  }

  /* ---- context menu ------------------------------------------------------- */
  .menu {
    --glass-bg: var(--glass-strong);
    position: fixed;
    z-index: 45;
    min-width: 214px;
    padding: 5px;
    border: 1px solid var(--line);
    border-radius: 12px;
    box-shadow: var(--shadow-pop);
    display: flex;
    flex-direction: column;
    transform-origin: top left;
    animation: menu-in 0.16s var(--ease-out) both;
  }
  @keyframes menu-in {
    from {
      opacity: 0;
      transform: scale(0.97);
    }
  }
  .menu button {
    font-size: 13px;
    text-align: left;
    border: 0;
    background: transparent;
    color: var(--ink);
    padding: 6px 10px;
    border-radius: 7px;
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 16px;
    cursor: default;
  }
  .menu button:hover {
    background: var(--chip);
  }
  .menu .danger {
    color: var(--danger);
  }
  .menu hr {
    border: 0;
    border-top: 1px solid var(--line);
    margin: 4px 6px;
  }

  /* ---- trust dialog ------------------------------------------------------- */
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 60;
    background: var(--scrim);
    display: grid;
    place-items: center;
    padding: 20px;
    animation: fade 0.18s ease-out both;
  }
  @keyframes fade {
    from {
      opacity: 0;
    }
  }
  .dialog {
    --glass-bg: var(--glass-strong);
    width: min(440px, 100%);
    border-radius: 18px;
    border: 1px solid var(--line);
    box-shadow: var(--shadow-pop);
    padding: 22px;
    font-size: 14px;
    animation: toast-in 0.3s var(--ease-spring) both;
  }
  .dialog h2 {
    margin: 0 0 8px;
    font-size: 18px;
    font-weight: 400;
    letter-spacing: -0.01em;
  }
  .dialog p {
    margin: 0 0 10px;
    color: var(--ink-2);
  }
  .dialog ul {
    margin: 0 0 12px;
    padding-left: 18px;
    color: var(--ink-2);
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 18px;
  }

  @media (max-width: 640px) {
    .tray {
      padding: 6px 14px 24px;
    }
    .grid {
      grid-template-columns: repeat(auto-fill, minmax(138px, 1fr));
      gap: 16px 10px;
    }
    .dock {
      padding: 4px 12px 8px;
    }
    .export {
      padding: 0 14px;
    }
    .export kbd {
      display: none;
    }
    .examples div {
      grid-template-columns: 7.5rem 1fr;
      gap: 12px;
    }
    .examples dd {
      font-size: 12.5px;
    }
    .dest {
      padding-left: 14px;
    }
  }
  @media (max-height: 560px) {
    .empty {
      padding-top: 8px;
      padding-bottom: 16px;
    }
    .lead {
      margin-bottom: 16px;
    }
    .examples div {
      padding: 4px 0;
    }
    .local {
      display: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .blob {
      animation: none;
    }
  }
</style>
