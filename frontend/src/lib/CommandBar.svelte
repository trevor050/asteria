<script lang="ts">
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
      queueMicrotask(() => input?.select())
      return
    }
    run(s, {})
  }

  function run(s: Suggestion, params: Record<string, unknown>) {
    onrun(s, params)
    reset()
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
      } else if (e.key === 'Backspace' && paramValue === '') {
        e.preventDefault()
        query = paramSkill.name
        paramSkill = null
        open = true
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

<div class="bar" class:focused class:busy>
  {#if open && !paramSkill && visible.length}
    <ul class="suggestions" id="suggestions" role="listbox" aria-label="Skills">
      {#each visible as s, i (s.id + s.value)}
        <li
          role="option"
          aria-selected={i === highlight}
          class:active={i === highlight}
          class:dim={!s.isMeta && hasFiles && (!s.applies || s.missing?.length)}
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

  <div class="field">
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
      <svg class="glyph" viewBox="0 0 20 20" aria-hidden="true"><path d="M10 2.5l1.6 4.4c.3.8 1 1.5 1.8 1.8l4.1 1.3-4.1 1.3c-.8.3-1.5 1-1.8 1.8L10 17.5l-1.6-4.4c-.3-.8-1-1.5-1.8-1.8L2.5 10l4.1-1.3c.8-.3 1.5-1 1.8-1.8z" /></svg>
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

  {#if paramSkill && param}
    <div class="params">
      {#if param.presets?.length}
        <div class="presets">
          {#each param.presets as p}
            <button
              type="button"
              class:on={paramValue === presetValue(p)}
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
    </div>
    {#if param.hint}<p class="hint">{param.hint}</p>{/if}
  {/if}
</div>

<style>
  .bar {
    position: relative;
  }
  .field {
    display: flex;
    align-items: center;
    gap: 10px;
    height: 48px;
    padding: 0 14px;
    border-radius: 12px;
    background: var(--field);
    border: 1px solid var(--line);
    transition:
      border-color 0.15s,
      box-shadow 0.15s;
  }
  .focused .field {
    border-color: rgb(var(--accent-rgb) / 0.55);
    box-shadow: 0 0 0 3px rgb(var(--accent-rgb) / 0.14);
  }
  .glyph {
    width: 18px;
    height: 18px;
    fill: rgb(var(--accent-rgb));
    flex: none;
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
    font-size: 16px;
    letter-spacing: -0.01em;
  }
  input::placeholder {
    color: var(--muted);
  }
  .skill {
    flex: none;
    padding: 4px 9px;
    border-radius: 7px;
    background: rgb(var(--accent-rgb) / 0.13);
    color: rgb(var(--accent-ink-rgb));
    font-weight: 600;
    font-size: 14px;
  }
  .unit {
    color: var(--muted);
  }
  .suggestions {
    position: absolute;
    left: 0;
    right: 0;
    bottom: calc(100% + 8px);
    margin: 0;
    padding: 6px;
    list-style: none;
    background: var(--popover);
    border: 1px solid var(--line);
    border-radius: 12px;
    box-shadow: var(--shadow-pop);
    display: flex;
    flex-direction: column-reverse;
    z-index: 20;
    animation: rise 0.12s ease-out;
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(4px);
    }
  }
  li {
    display: grid;
    grid-template-columns: auto 1fr auto;
    align-items: baseline;
    gap: 12px;
    padding: 9px 10px;
    border-radius: 8px;
    cursor: default;
  }
  li.active {
    background: rgb(var(--accent-rgb) / 0.12);
  }
  li.dim .name {
    color: var(--muted);
  }
  .name {
    font-weight: 600;
    white-space: nowrap;
  }
  .value {
    margin-left: 6px;
    padding: 1px 6px;
    border-radius: 5px;
    background: var(--chip);
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
  .meta.warn {
    color: var(--warn);
  }
  .params {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-top: 8px;
    flex-wrap: wrap;
  }
  .presets {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
  }
  .presets button {
    font: inherit;
    font-size: 13px;
    padding: 4px 10px;
    border-radius: 7px;
    border: 1px solid var(--line);
    background: transparent;
    color: var(--ink);
    cursor: pointer;
    font-variant-numeric: tabular-nums;
  }
  .presets button.on {
    border-color: rgb(var(--accent-rgb) / 0.6);
    background: rgb(var(--accent-rgb) / 0.12);
  }
  .keys {
    color: var(--muted);
    font-size: 12px;
  }
  .hint {
    margin: 6px 2px 0;
    color: var(--muted);
    font-size: 12px;
  }
  @media (max-width: 640px) {
    .desc {
      display: none;
    }
    li {
      grid-template-columns: 1fr auto;
    }
  }
</style>
