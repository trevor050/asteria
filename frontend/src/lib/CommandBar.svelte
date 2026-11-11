<script lang="ts">
  import { tick } from 'svelte'
  import { api, type ParamDef, type Suggestion } from './api'
  import { mod, plural, typeLabel } from './format'

  type Props = {
    targetIds: string[]
    hasFiles: boolean
    busy: boolean
    refreshKey: number
    onrun: (s: Suggestion, params: Record<string, unknown>) => void
  }
  let { targetIds, hasFiles, busy, refreshKey, onrun }: Props = $props()

  let input: HTMLInputElement | undefined = $state()
  let query = $state('')
  let suggestions: Suggestion[] = $state([])
  let highlight = $state(0)
  let open = $state(false)
  let focused = $state(false)

  // Parameter mode: a skill was chosen and needs its one value.
  let paramSkill = $state<Suggestion | null>(null)
  let paramValue = $state('')

  let param = $derived<ParamDef | null>(paramSkill?.params?.[0] ?? null)
  let visible = $derived(suggestions.slice(0, 7))
  // The engine already ranks what fits the files; surface the top few as
  // one-click actions, and the real count of everything that applies.
  let fitting = $derived(suggestions.filter((s) => !s.isMeta && s.applies > 0))
  let quick = $derived.by(() => {
    // One per kind of action first (convert, transform, compress, pdf...),
    // in the engine's order, so a mixed tray shows a spread, not four
    // flavors of the same conversion.
    const ready = fitting.filter((s) => !s.missing?.length)
    const picked: Suggestion[] = []
    const kinds = new Set<string>()
    for (const s of ready) {
      if (picked.length < 4 && !kinds.has(s.category)) {
        kinds.add(s.category)
        picked.push(s)
      }
    }
    for (const s of ready) if (picked.length < 4 && !picked.includes(s)) picked.push(s)
    return ready.filter((s) => picked.includes(s))
  })
  let showQuick = $derived(hasFiles && !busy && !query && !paramSkill && !open && quick.length > 0)

  function showAll() {
    open = true
    highlight = 0
    input?.focus()
  }

  export function focus(text?: string) {
    input?.focus()
    if (text !== undefined && !paramSkill) {
      query += text
      open = true
    }
  }

  export function reset() {
    query = ''
    paramSkill = null
    paramValue = ''
    open = false
    highlight = 0
  }

  let seq = 0
  async function search() {
    const mine = ++seq
    const res = (await api.Search(query, targetIds)) ?? []
    if (mine !== seq) return
    suggestions = res
    highlight = 0
  }

  $effect(() => {
    // Re-run whenever the query, the targets or the tray change.
    void query
    void targetIds
    void refreshKey
    const t = setTimeout(search, 25)
    return () => clearTimeout(t)
  })

  function presetValue(p: unknown): string {
    if (p && typeof p === 'object' && 'value' in (p as object)) return String((p as { value: unknown }).value)
    return String(p)
  }
  function presetLabel(p: unknown): string {
    if (p && typeof p === 'object' && 'label' in (p as object)) return String((p as { label: unknown }).label)
    return String(p)
  }

  function choose(s: Suggestion) {
    const p = s.params?.[0]
    if (p && s.value) {
      run(s, { [p.name]: s.value })
      return
    }
    if (p) {
      paramSkill = s
      paramValue = s.value || presetValue(p.default ?? '')
      open = false
      void tick().then(() => input?.select())
      return
    }
    run(s, {})
  }

  function run(s: Suggestion, params: Record<string, unknown>) {
    const had = focused
    onrun(s, params)
    reset()
    if (had) refocus()
  }

  // Entering or leaving parameter mode swaps the input element; keep focus
  // in the bar so it stays ready for the next command.
  function refocus() {
    void tick().then(() => input?.focus())
  }

  function cyclePreset(dir: 1 | -1) {
    const presets = (param?.presets ?? []).map(presetValue)
    if (!presets.length) return
    const i = presets.indexOf(paramValue)
    paramValue = presets[(i + dir + presets.length) % presets.length]
  }

  function onkeydown(e: KeyboardEvent) {
    if (paramSkill) {
      if (e.key === 'Tab') {
        e.preventDefault()
        cyclePreset(e.shiftKey ? -1 : 1)
      } else if (e.key === 'Enter') {
        e.preventDefault()
        if (param) run(paramSkill, { [param.name]: paramValue })
      } else if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        paramSkill = null
        paramValue = ''
        refocus()
      } else if (e.key === 'Backspace' && paramValue === '') {
        e.preventDefault()
        query = paramSkill.name
        paramSkill = null
        open = true
        refocus()
      }
      return
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      if (!open) {
        open = true
        return
      }
      if (!visible.length) return
      // The list opens upward, so ArrowUp moves away from the input.
      const delta = e.key === 'ArrowUp' ? 1 : -1
      highlight = (highlight + delta + visible.length) % visible.length
    } else if (e.key === 'Enter' && !e.metaKey && !e.ctrlKey) {
      e.preventDefault()
      const s = visible[highlight]
      if (s && (open || query)) choose(s)
    } else if (e.key === 'Tab' && open && visible[highlight]) {
      e.preventDefault()
      const s = visible[highlight]
      query = s.name + (s.value ? ' ' + s.value : s.params?.length ? ' ' : '')
    } else if (e.key === 'Escape' && (open || query)) {
      e.preventDefault()
      e.stopPropagation()
      if (query) query = ''
      else open = false
    }
  }

  function meta(s: Suggestion): string {
    if (s.missing?.length) return `needs ${s.missing.join(', ')}`
    if (s.isMeta || !hasFiles) return ''
    if (!s.applies) return 'no matching files'
    const out = s.outputType === 'folder' ? 'folder' : s.outputType ? typeLabel(s.outputType) : ''
    if (s.combine) return `${plural(s.applies, 'file')} → 1 ${out}`
    return out ? `${plural(s.applies, 'file')} → ${out}` : plural(s.applies, 'file')
  }

  let placeholder = $derived(
    !hasFiles
      ? `Drop files, or ${mod}O to add`
      : targetIds.length === 1
        ? 'What should happen to this file?'
        : `What should happen to ${targetIds.length} files?`,
  )
</script>

<div class="bar" class:focused class:busy class:param={!!paramSkill}>
  {#if open && !paramSkill && visible.length}
    <ul class="suggestions glass" id="suggestions" role="listbox" aria-label="Skills">
      {#each visible as s, i (s.id + s.value)}
        <li
          role="option"
          aria-selected={i === highlight}
          class:active={i === highlight}
          class:dim={!s.isMeta && hasFiles && (!s.applies || s.missing?.length)}
          style={`--i:${i}`}
          onmousedown={(e) => {
            e.preventDefault()
            choose(s)
          }}
          onmousemove={() => (highlight = i)}
        >
          <span class="name">
            {s.name}{#if s.value}<span class="value">{s.value}</span>{/if}
          </span>
          <span class="desc">{s.description}</span>
          <span class="meta" class:warn={s.missing?.length}>{meta(s)}</span>
        </li>
      {/each}
    </ul>
  {/if}

  {#if showQuick}
    <div class="quick" role="group" aria-label="Likely next steps">
      {#each quick as s, i (s.id)}
        <button
          type="button"
          style={`--i:${i}`}
          onmousedown={(e) => e.preventDefault()}
          onclick={() => choose(s)}
          title={s.description}>{s.name}</button
        >
      {/each}
      {#if fitting.length > quick.length}
        <button type="button" class="all" style={`--i:${quick.length}`} onmousedown={(e) => e.preventDefault()} onclick={showAll}
          >All {fitting.length} skills</button
        >
      {/if}
    </div>
  {/if}

  {#if paramSkill && param}
    <div class="params">
      {#if param.presets?.length}
        <div class="presets">
          {#each param.presets as p, i}
            <button
              type="button"
              class:on={paramValue === presetValue(p)}
              style={`--i:${i}`}
              onmousedown={(e) => e.preventDefault()}
              onclick={() => {
                paramValue = presetValue(p)
                input?.focus()
              }}>{presetLabel(p)}{param.unit && !presetLabel(p).endsWith(param.unit) ? param.unit : ''}</button
            >
          {/each}
        </div>
      {/if}
      <span class="keys"><kbd>Tab</kbd> cycle <kbd>↵</kbd> apply <kbd>Esc</kbd> back</span>
      {#if param.hint}<p class="hint">{param.hint}</p>{/if}
    </div>
  {/if}

  <div class="wrap">
    <span class="halo" aria-hidden="true"></span>
    <div class="field glass">
      {#if paramSkill && param}
        <span class="skill">{paramSkill.name}</span>
        <input
          bind:this={input}
          bind:value={paramValue}
          {onkeydown}
          onfocus={() => (focused = true)}
          onblur={() => (focused = false)}
          aria-label={param.label}
          placeholder={param.hint || param.label}
          spellcheck="false"
          autocomplete="off"
        />
        {#if param.unit && !paramValue.endsWith(param.unit)}<span class="unit">{param.unit}</span>{/if}
      {:else}
        <svg class="glyph" viewBox="0 0 20 20" aria-hidden="true"
          ><path d="M10 1.5c.4 3.9 1.1 6.1 2.3 7.2 1.2 1.2 3.3 1.9 6.2 2.3-2.9.4-5 1.1-6.2 2.3-1.2 1.1-1.9 3.3-2.3 6.2-.4-2.9-1.1-5.1-2.3-6.2C6.5 12.1 4.4 11.4 1.5 11c2.9-.4 5-1.1 6.2-2.3C8.9 7.6 9.6 5.4 10 1.5z" /></svg
        >
        <input
          bind:this={input}
          bind:value={query}
          {onkeydown}
          oninput={() => (open = true)}
          onfocus={() => (focused = true)}
          onblur={() => {
            focused = false
            open = false
          }}
          {placeholder}
          aria-label="Command"
          aria-expanded={open}
          aria-autocomplete="list"
          role="combobox"
          aria-controls="suggestions"
          spellcheck="false"
          autocomplete="off"
        />
      {/if}
    </div>
  </div>
</div>

<style>
  .bar {
    position: relative;
    flex: 1;
    min-width: 0;
  }

  /* ---- the field ---------------------------------------------------------- */
  .wrap {
    position: relative;
  }
  /* A soft iridescent glow, in the same colors as the ambient wash, that
     blooms behind the field when it has focus. Opacity and scale only. */
  .halo {
    position: absolute;
    inset: 2px 10px;
    border-radius: 18px;
    background: conic-gradient(
      from 200deg,
      rgb(var(--aura-teal)),
      rgb(var(--aura-slate)),
      rgb(var(--aura-amber)),
      rgb(var(--aura-teal))
    );
    filter: blur(18px);
    opacity: 0;
    transform: scale(0.94, 0.8);
    transition:
      opacity 0.45s var(--ease-out),
      transform 0.55s var(--ease-spring);
    pointer-events: none;
  }
  .focused .halo {
    opacity: 0.55;
    transform: none;
  }
  .busy .halo {
    animation: breathe 2.4s ease-in-out infinite;
  }
  @keyframes breathe {
    50% {
      opacity: 0.25;
    }
  }
  .field {
    position: relative;
    display: flex;
    align-items: center;
    gap: 12px;
    height: 52px;
    padding: 0 16px 0 18px;
    border-radius: 16px;
    border: 1px solid var(--line);
    box-shadow:
      inset 0 1px 0 var(--glass-edge),
      0 10px 30px -18px rgb(0 0 0 / 0.5);
    transition:
      border-color 0.3s var(--ease-out),
      transform 0.4s var(--ease-spring);
  }
  .focused .field {
    border-color: var(--line-strong);
  }
  .glyph {
    width: 17px;
    height: 17px;
    flex: none;
    fill: var(--muted);
    transition:
      fill 0.3s,
      transform 0.5s var(--ease-spring);
  }
  .focused .glyph {
    fill: var(--accent);
    transform: rotate(90deg);
  }
  .busy .glyph {
    animation: turn 2.4s linear infinite;
  }
  @keyframes turn {
    to {
      transform: rotate(360deg);
    }
  }
  input {
    flex: 1;
    min-width: 0;
    height: 100%;
    border: 0;
    outline: 0;
    background: transparent;
    color: var(--ink);
    font: inherit;
    font-size: 17px;
    font-weight: 300;
    letter-spacing: -0.005em;
    caret-color: var(--accent);
  }
  input::placeholder {
    color: var(--muted);
    font-weight: 300;
  }
  .skill {
    flex: none;
    padding: 4px 10px;
    border-radius: 9px;
    background: var(--accent-soft);
    color: var(--ink);
    font-size: 14px;
    font-weight: 450;
    animation: pop 0.3s var(--ease-spring) both;
  }
  .unit {
    color: var(--muted);
    font-weight: 300;
  }

  /* ---- one-click next steps ----------------------------------------------- */
  .quick {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    margin: 0 0 8px 8px;
  }
  .quick button {
    border: 0;
    background: transparent;
    color: var(--ink-2);
    font-size: 13px;
    font-weight: 400;
    padding: 5px 11px;
    border-radius: 999px;
    cursor: pointer;
    animation: rise 0.36s var(--ease-out) both;
    animation-delay: calc(var(--i) * 40ms);
    transition:
      background 0.15s,
      color 0.15s;
  }
  .quick button:hover {
    background: var(--chip);
    color: var(--ink);
  }
  .quick .all {
    color: var(--muted);
  }

  /* ---- suggestions -------------------------------------------------------- */
  .suggestions {
    --glass-bg: var(--glass-strong);
    position: absolute;
    left: 0;
    right: 0;
    bottom: calc(100% + 10px);
    margin: 0;
    padding: 6px;
    list-style: none;
    border: 1px solid var(--line);
    border-radius: 16px;
    box-shadow: var(--shadow-pop);
    display: flex;
    flex-direction: column-reverse;
    z-index: 20;
    transform-origin: 50% 100%;
    animation: open 0.2s var(--ease-out) both;
  }
  @keyframes open {
    from {
      opacity: 0;
      transform: translateY(6px) scale(0.985);
    }
  }
  li {
    display: grid;
    grid-template-columns: auto 1fr auto;
    align-items: baseline;
    gap: 14px;
    padding: 9px 12px;
    border-radius: 10px;
    cursor: default;
    transition: background 0.12s;
  }
  li.active {
    background: var(--chip);
  }
  li.dim .name {
    color: var(--muted);
  }
  .name {
    font-weight: 450;
    white-space: nowrap;
    color: var(--ink);
  }
  .value {
    margin-left: 7px;
    padding: 1px 6px;
    border-radius: 6px;
    background: var(--accent-soft);
    font-weight: 400;
    font-variant-numeric: tabular-nums;
  }
  .desc {
    color: var(--muted);
    font-size: 13px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .meta {
    color: var(--muted);
    font-size: 12px;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  li.active .meta {
    color: var(--ink-2);
  }
  .meta.warn,
  li.active .meta.warn {
    color: var(--warn);
  }

  /* ---- parameter mode ----------------------------------------------------- */
  .params {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 6px 12px;
    margin: 0 4px 10px 8px;
  }
  .presets {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
  }
  .presets button {
    font-size: 13px;
    padding: 5px 11px;
    border-radius: 999px;
    border: 1px solid var(--line);
    background: transparent;
    color: var(--ink-2);
    cursor: pointer;
    font-variant-numeric: tabular-nums;
    animation: rise 0.3s var(--ease-out) both;
    animation-delay: calc(var(--i) * 30ms);
    transition:
      background 0.15s,
      border-color 0.15s,
      color 0.15s;
  }
  .presets button:hover {
    color: var(--ink);
    border-color: var(--line-strong);
  }
  .presets button.on {
    border-color: transparent;
    background: var(--accent-soft);
    color: var(--ink);
  }
  .keys {
    color: var(--muted);
    font-size: 12px;
  }
  .keys kbd {
    margin: 0 2px 0 6px;
  }
  .hint {
    flex-basis: 100%;
    margin: 0;
    color: var(--muted);
    font-size: 12px;
  }

  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(4px);
    }
  }
  @keyframes pop {
    from {
      opacity: 0;
      transform: scale(0.9);
    }
  }
  @media (prefers-color-scheme: light) {
    .focused .halo {
      opacity: 0.4;
    }
  }
  @media (max-width: 640px) {
    .desc {
      display: none;
    }
    li {
      grid-template-columns: 1fr auto;
    }
    .keys {
      display: none;
    }
    .quick button:nth-child(4):not(.all) {
      display: none;
    }
  }
</style>
