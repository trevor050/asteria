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
    settings?.outputFolder ? settings.outputFolder.split(/[\\/]/).filter(Boolean).slice(-2).join('/') : 'Next to originals',
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

<div class="shell" class:mac={st?.platform === 'darwin'}>
  <header class="titlebar">
    <span class="app">Asteria</span>
    {#if files.length}
      <div class="mode" role="radiogroup" aria-label="Apply skills to">
        <button type="button" role="radio" aria-checked={!perFile} class:on={!perFile} onclick={() => setMode('batch')}>
          {selected.length ? 'Selected' : 'All files'}
        </button>
        <button type="button" role="radio" aria-checked={perFile} class:on={perFile} onclick={() => setMode('per_file')}>Focused file</button>
      </div>
    {/if}
  </header>

  <main class="tray" data-file-drop-target bind:this={trayEl} aria-label="Files">
    {#if !files.length}
      <div class="empty">
        <h1>Drop files here</h1>
        <p class="lead">or <button type="button" class="link" onclick={pickFiles}>choose files</button> ({mod}O), then type what you want:</p>
        <ul class="examples">
          <li><kbd>heic to jpg</kbd><span>iPhone photos that open everywhere</span></li>
          <li><kbd>resize 50%</kbd><span>or 1920, or 1920x1080</span></li>
          <li><kbd>merge pdfs</kbd><span>in the order shown</span></li>
          <li><kbd>trim 0:05-0:30</kbd><span>audio and video</span></li>
          <li><kbd>csv to json</kbd><span>and YAML, Markdown</span></li>
          <li><kbd>zip</kbd><span>or unzip</span></li>
        </ul>
        <p class="local">Nothing is uploaded. Every conversion runs on this computer, and your originals are never changed.</p>
        {#if ffmpeg && !ffmpeg.found}
          <p class="note">Audio and video skills need ffmpeg: <code>{ffmpeg.hint}</code></p>
        {/if}
      </div>
    {:else}
      <div class="grid" role="listbox" aria-multiselectable="true" aria-label="Files in the tray">
        {#each files as file (file.id)}
          <FileTile
            {file}
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

  <section class="command">
    {#if busy && progress}
      <div class="progress" aria-live="polite">
        <div class="track"><div class="fill" style={`width:${Math.round((progress.fraction || 0) * 100)}%`}></div></div>
        <span class="label">
          {progress.label}{#if progress.total > 1}&ensp;{progress.done} of {progress.total}{/if}{#if progress.current}&ensp;<span class="cur">{progress.current}</span>{/if}
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
      </div>
    {/if}
    <CommandBar bind:this={bar} targetIds={targets} hasFiles={files.length > 0} {busy} {refreshKey} onrun={run} />
  </section>

  <footer class="footer">
    <div class="out">
      <span class="muted">Export to</span>
      <button type="button" class="link" onclick={pickOutput} title={settings?.outputFolder || 'Each export is saved beside its original'}>{outputLabel}</button>
      {#if settings?.outputFolder}
        <button type="button" class="x" onclick={resetOutput} aria-label="Export next to originals instead">×</button>
      {/if}
    </div>
    <div class="right">
      {#if files.length}
        <span class="muted count">{plural(files.length, 'file')}{selected.length ? `, ${selected.length} selected` : ''}</span>
        <button type="button" class="ghost" onclick={() => removeFromTray(files.map((f) => f.id))} disabled={busy}>Clear</button>
      {/if}
      <button type="button" class="primary" onclick={exportFiles} disabled={!exportable.length || busy}>
        Export{exportable.length ? ` ${exportable.length}` : ''}
        <kbd>{mod}↵</kbd>
      </button>
    </div>
  </footer>

  {#if toasts.length}
    <div class="toasts" aria-live="polite">
      {#each toasts as t (t.id)}
        <div class="toast" class:error={t.tone === 'error'}>
          <div class="text">
            {t.text}
            {#if t.detail}<pre>{t.detail}</pre>{/if}
          </div>
          {#each t.actions ?? [] as a}
            <button
              type="button"
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
    <div class="menu" role="menu" style={`left:${menu.x}px;top:${menu.y}px`}>
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
      <div class="dialog" role="alertdialog" aria-modal="true" aria-labelledby="trust-title">
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
    height: 100vh;
    display: grid;
    grid-template-rows: auto 1fr auto auto;
  }
  .titlebar {
    --wails-draggable: drag;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    height: 44px;
    padding: 0 14px 0 16px;
    user-select: none;
  }
  .mac .titlebar {
    padding-left: 84px;
  }
  .app {
    font-weight: 600;
    letter-spacing: -0.01em;
  }
  .mode {
    --wails-draggable: no-drag;
    display: flex;
    padding: 2px;
    border-radius: 9px;
    background: var(--chip);
  }
  .mode button {
    font: inherit;
    font-size: 12.5px;
    border: 0;
    background: transparent;
    color: var(--muted);
    padding: 4px 10px;
    border-radius: 7px;
    cursor: pointer;
  }
  .mode button.on {
    background: var(--surface);
    color: var(--ink);
    box-shadow: 0 1px 2px rgb(0 0 0 / 0.08);
  }
  .tray {
    overflow: auto;
    padding: 4px 12px 12px;
    min-height: 0;
    border-radius: 14px;
    margin: 0 8px;
    transition: background 0.12s;
  }
  .tray:global(.file-drop-target-active) {
    background: rgb(var(--accent-rgb) / 0.08);
    box-shadow: inset 0 0 0 2px rgb(var(--accent-rgb) / 0.45);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(176px, 1fr));
    gap: 6px;
    align-items: start;
  }
  .empty {
    min-height: 100%;
    display: flex;
    flex-direction: column;
    justify-content: center;
    max-width: 520px;
    margin: 0 auto;
    padding: 24px 8px;
  }
  .empty h1 {
    margin: 0;
    font-size: clamp(30px, 6vw, 44px);
    font-weight: 600;
    letter-spacing: -0.035em;
    line-height: 1.05;
  }
  .lead {
    margin: 10px 0 18px;
    color: var(--muted);
    font-size: 15px;
  }
  .examples {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: 2px;
  }
  .examples li {
    display: grid;
    grid-template-columns: 9.5rem 1fr;
    align-items: baseline;
    gap: 12px;
    padding: 5px 0;
    font-size: 14px;
  }
  .examples kbd {
    justify-self: start;
    font-size: 13.5px;
  }
  .examples span {
    color: var(--muted);
  }
  .local {
    margin: 22px 0 0;
    font-size: 13px;
    color: var(--muted);
  }
  .note {
    margin: 8px 0 0;
    font-size: 13px;
    color: var(--warn);
  }
  .command {
    width: 100%;
    max-width: 892px;
    margin: 0 auto;
    padding: 8px 16px 10px;
  }
  .progress {
    display: grid;
    grid-template-columns: 1fr auto auto;
    align-items: center;
    gap: 4px 12px;
    margin-bottom: 8px;
    font-size: 12.5px;
  }
  .track {
    grid-column: 1 / -1;
    height: 3px;
    border-radius: 2px;
    background: var(--chip);
    overflow: hidden;
  }
  .fill {
    height: 100%;
    background: rgb(var(--accent-rgb));
    transition: width 0.15s linear;
  }
  .label {
    color: var(--muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-variant-numeric: tabular-nums;
  }
  .cur {
    color: var(--ink);
  }
  .queued {
    color: var(--muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 260px;
  }
  .cancel {
    font: inherit;
    font-size: 12px;
    border: 0;
    background: transparent;
    color: var(--muted);
    cursor: pointer;
  }
  .footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 8px 16px 12px;
    font-size: 13px;
    min-width: 0;
  }
  .out {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .out .link {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 260px;
  }
  .right {
    display: flex;
    align-items: center;
    gap: 10px;
    flex: none;
  }
  .muted {
    color: var(--muted);
  }
  .link {
    font: inherit;
    border: 0;
    padding: 0;
    background: none;
    color: rgb(var(--accent-ink-rgb));
    cursor: pointer;
    text-decoration: underline;
    text-decoration-color: rgb(var(--accent-rgb) / 0.35);
    text-underline-offset: 3px;
  }
  .x {
    border: 0;
    background: transparent;
    color: var(--muted);
    cursor: pointer;
    font-size: 16px;
    line-height: 1;
    padding: 2px 4px;
    border-radius: 4px;
  }
  .primary,
  .ghost {
    font: inherit;
    font-size: 13px;
    font-weight: 600;
    border-radius: 9px;
    padding: 7px 12px;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .primary {
    border: 0;
    background: rgb(var(--accent-rgb));
    color: white;
  }
  .primary kbd {
    background: rgb(255 255 255 / 0.2);
    color: white;
    border: 0;
  }
  .primary:disabled {
    background: var(--chip);
    color: var(--muted);
    cursor: default;
  }
  .primary:disabled kbd {
    background: transparent;
    color: var(--muted);
  }
  .ghost {
    border: 1px solid var(--line);
    background: transparent;
    color: var(--ink);
    font-weight: 500;
  }
  .ghost:disabled {
    opacity: 0.5;
  }
  .toasts {
    position: fixed;
    right: 14px;
    bottom: 120px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    z-index: 40;
    max-width: min(420px, calc(100vw - 28px));
  }
  .toast {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 10px 10px 10px 14px;
    border-radius: 11px;
    background: var(--popover);
    border: 1px solid var(--line);
    box-shadow: var(--shadow-pop);
    font-size: 13px;
    animation: pop 0.14s ease-out;
  }
  @keyframes pop {
    from {
      opacity: 0;
      transform: translateY(6px);
    }
  }
  .toast.error {
    border-color: rgb(var(--danger-rgb) / 0.4);
  }
  .toast .text {
    flex: 1;
    min-width: 0;
    padding-top: 2px;
  }
  .toast pre {
    margin: 4px 0 0;
    font: inherit;
    font-size: 12px;
    color: var(--muted);
    white-space: pre-wrap;
    word-break: break-word;
  }
  .toast button:not(.x) {
    font: inherit;
    font-size: 12.5px;
    font-weight: 600;
    border: 0;
    background: rgb(var(--accent-rgb) / 0.12);
    color: rgb(var(--accent-ink-rgb));
    border-radius: 7px;
    padding: 4px 9px;
    cursor: pointer;
    white-space: nowrap;
  }
  .menu {
    position: fixed;
    z-index: 45;
    min-width: 210px;
    padding: 5px;
    background: var(--popover);
    border: 1px solid var(--line);
    border-radius: 10px;
    box-shadow: var(--shadow-pop);
    display: flex;
    flex-direction: column;
  }
  .menu button {
    font: inherit;
    font-size: 13px;
    text-align: left;
    border: 0;
    background: transparent;
    color: var(--ink);
    padding: 6px 9px;
    border-radius: 6px;
    display: flex;
    justify-content: space-between;
    gap: 16px;
    cursor: default;
  }
  .menu button:hover {
    background: rgb(var(--accent-rgb) / 0.12);
  }
  .menu .danger {
    color: rgb(var(--danger-rgb));
  }
  .menu hr {
    border: 0;
    border-top: 1px solid var(--line);
    margin: 4px 2px;
  }
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 60;
    background: rgb(0 0 0 / 0.38);
    display: grid;
    place-items: center;
    padding: 20px;
  }
  .dialog {
    width: min(440px, 100%);
    background: var(--surface);
    border-radius: 14px;
    border: 1px solid var(--line);
    box-shadow: var(--shadow-pop);
    padding: 20px;
    font-size: 14px;
  }
  .dialog h2 {
    margin: 0 0 8px;
    font-size: 17px;
  }
  .dialog p {
    margin: 0 0 10px;
    color: var(--muted);
  }
  .dialog ul {
    margin: 0 0 12px;
    padding-left: 18px;
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 16px;
  }
  @media (max-width: 640px) {
    .grid {
      grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
    }
    .count,
    .footer kbd {
      display: none;
    }
    .examples li {
      grid-template-columns: 1fr;
      gap: 0;
    }
    .examples span {
      font-size: 13px;
      margin-bottom: 4px;
    }
  }
</style>
