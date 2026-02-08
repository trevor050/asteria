<script lang="ts">
  import { onMount } from 'svelte'
  import { Clipboard } from '@wailsio/runtime'
  import { api, AppEvents, FILE_DROP_EVENT } from './lib/api'
  import type { ParamDef, SessionSnapshot, Skill, SkillResult, WorkingFile } from './lib/api'

  type SessionSnapshotExt = SessionSnapshot & { accentColor?: string }

  let query = ''
  let skills: Skill[] = []
  let files: WorkingFile[] = []
  let session: SessionSnapshotExt = {
    mode: 'batch' as any,
    outputFolder: '',
    namingPattern: '{name}_{skill}.{ext}',
    accentColor: '99,102,241'
  }

  const applyAccent = (color?: string) => {
    const c = typeof color === 'string' && color.trim() ? color.trim() : '99,102,241'
    const rgb = parseColorToRgb(c)
    if (!rgb) return
    const hover = darkenRgb(rgb.r, rgb.g, rgb.b, 0.10)

    document.documentElement.style.setProperty('--accent', `rgb(${rgb.r}, ${rgb.g}, ${rgb.b})`)
    document.documentElement.style.setProperty('--accent-hover', `rgb(${hover.r}, ${hover.g}, ${hover.b})`)
    document.documentElement.style.setProperty('--accent-light', `rgba(${rgb.r}, ${rgb.g}, ${rgb.b}, 0.16)`)
  }

  const parseColorToRgb = (input: string): { r: number; g: number; b: number } | null => {
    const s = input.trim()

    if (s.startsWith('#')) {
      const raw = s.replace('#', '').trim()
      const full = raw.length === 3 ? raw.split('').map((c) => c + c).join('') : raw
      if (!/^[0-9a-fA-F]{6}$/.test(full)) return null
      const n = parseInt(full, 16)
      return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255 }
    }

    const rgbFn = s.match(/^rgb\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*\)$/i)
    if (rgbFn) {
      return clampRgb(parseInt(rgbFn[1], 10), parseInt(rgbFn[2], 10), parseInt(rgbFn[3], 10))
    }

    const triplet = s.match(/^(\d{1,3})\s*[,\s]\s*(\d{1,3})\s*[,\s]\s*(\d{1,3})$/)
    if (triplet) {
      return clampRgb(parseInt(triplet[1], 10), parseInt(triplet[2], 10), parseInt(triplet[3], 10))
    }

    return null
  }

  const clampRgb = (r: number, g: number, b: number): { r: number; g: number; b: number } | null => {
    if ([r, g, b].some((v) => Number.isNaN(v) || v < 0 || v > 255)) return null
    return { r, g, b }
  }

  const darkenRgb = (r: number, g: number, b: number, amount: number) => {
    const f = 1 - Math.min(1, Math.max(0, amount))
    return {
      r: Math.round(r * f),
      g: Math.round(g * f),
      b: Math.round(b * f)
    }
  }

  const isPresetObject = (value: any): value is { label?: any; value?: any } => {
    return (
      !!value &&
      typeof value === 'object' &&
      (Object.prototype.hasOwnProperty.call(value, 'value') || Object.prototype.hasOwnProperty.call(value, 'label'))
    )
  }

  const presetValue = (value: any): string => {
    if (isPresetObject(value)) return String(value.value ?? value.label ?? '')
    return String(value)
  }

  const presetLabel = (value: any): string => {
    if (isPresetObject(value)) return String(value.label ?? value.value ?? '')
    return String(value)
  }

  const presetSwatchCss = (value: any): string => {
    const rgb = parseColorToRgb(presetValue(value))
    if (!rgb) return ''
    return `rgb(${rgb.r}, ${rgb.g}, ${rgb.b})`
  }

  const withBusy = async <T,>(text: string, total: number, fn: () => Promise<T>): Promise<T | undefined> => {
    if (isBusy) return
    isBusy = true
    busyText = text
    busyTotal = total
    try {
      return await fn()
    } finally {
      isBusy = false
      busyText = ''
      busyTotal = 0
    }
  }

  let activeFileId: string | null = null
  let selectedFileIds: Set<string> = new Set()
  let highlightIndex = 0
  let isParamMode = false
  let activeSkill: Skill | null = null
  let activeParam: ParamDef | null = null
  let paramValue = ''
  let isBusy = false
  let busyText = ''
  let busyTotal = 0
  let toast = ''
  let commandInput: HTMLInputElement | null = null
  let showDropdown = false

  // Context menu state
  let contextMenu: { x: number; y: number; fileId: string } | null = null

  $: selectionCount = selectedFileIds.size > 0 ? selectedFileIds.size : activeFileId ? 1 : 0

  const showToast = (message: string) => {
    toast = message
    setTimeout(() => (toast = ''), 2400)
  }

  const loadSession = async () => {
    try {
      session = await api.getSession()
    } catch (e) {
      console.error('Failed to load session:', e)
    }
  }

  const inputTypes = (): string[] => {
    if (session.mode === 'per_file' && activeFileId) {
      const file = files.find((item) => item.id === activeFileId)
      return file ? [file.currentExtension] : []
    }
    const types = new Set(files.map((file) => file.currentExtension))
    return Array.from(types)
  }

  const refreshSkills = async () => {
    try {
      skills = await api.getSkills(query, inputTypes())
      highlightIndex = 0
    } catch (e) {
      console.error('Failed to refresh skills:', e)
    }
  }

  const openPicker = async () => {
    try {
      const picked = await api.openFilesDialog()
      if (picked && picked.length) {
        await addFiles(picked)
      }
    } catch (error) {
      console.error('Failed to open file picker:', error)
    }
  }

  const addFiles = async (paths: string[]) => {
    try {
      const added = await api.addFiles(paths)
      if (added && added.length) {
        files = [...files, ...added]
        if (!activeFileId) {
          activeFileId = added[0].id
        }
        await refreshSkills()
      }
    } catch (e) {
      console.error('Failed to add files:', e)
    }
  }

  const updateFiles = (updated: WorkingFile[]) => {
    if (!updated || updated.length === 0) return
    const map = new Map(files.map((file) => [file.id, file]))
    updated.forEach((file) => map.set(file.id, file))
    files = Array.from(map.values())
  }

  const resetCommand = () => {
    query = ''
    isParamMode = false
    activeSkill = null
    activeParam = null
    paramValue = ''
    showDropdown = false
    refreshSkills()
  }

  const startParamMode = (skill: Skill) => {
    activeSkill = skill
    activeParam = skill.params[0]
    if (activeParam) {
      paramValue = String(activeParam.default ?? '')
      isParamMode = true
      showDropdown = false
    } else {
      isParamMode = false
    }
  }

  const selectSkill = async (skill: Skill) => {
    if (skill.params && skill.params.length > 0) {
      startParamMode(skill)
      return
    }
    await applySkill(skill, {})
  }

  const getTargetFileIds = (): string[] => {
    // If we have selected files, use those
    if (selectedFileIds.size > 0) {
      return Array.from(selectedFileIds)
    }
    if (session.mode === 'per_file' && activeFileId) {
      return [activeFileId]
    }
    return files.map((file) => file.id)
  }

  const getSelectionIds = (): string[] => {
    if (selectedFileIds.size > 0) {
      return Array.from(selectedFileIds)
    }
    if (activeFileId) {
      return [activeFileId]
    }
    return []
  }

  const getSelectionFiles = (): WorkingFile[] => {
    const ids = getSelectionIds()
    if (!ids.length) return []
    const idSet = new Set(ids)
    return files.filter((file) => idSet.has(file.id))
  }

  const syncActiveToSelection = (selection: Set<string>) => {
    if (selection.size === 0) return
    if (!activeFileId || !selection.has(activeFileId)) {
      const first = files.find((file) => selection.has(file.id))
      if (first) {
        activeFileId = first.id
      }
    }
  }

  const handleFileClick = (fileId: string, event: MouseEvent) => {
    if (event.metaKey || event.ctrlKey) {
      // Cmd/Ctrl+click: toggle selection
      const newSelection = new Set(selectedFileIds)
      if (newSelection.size === 0 && activeFileId) {
        newSelection.add(activeFileId)
      }
      if (newSelection.has(fileId)) {
        newSelection.delete(fileId)
      } else {
        newSelection.add(fileId)
      }
      selectedFileIds = newSelection
      activeFileId = fileId
    } else if (event.shiftKey && activeFileId) {
      // Shift+click: range selection
      const activeIndex = files.findIndex((f) => f.id === activeFileId)
      const clickedIndex = files.findIndex((f) => f.id === fileId)
      const start = Math.min(activeIndex, clickedIndex)
      const end = Math.max(activeIndex, clickedIndex)
      const newSelection = new Set<string>()
      for (let i = start; i <= end; i++) {
        newSelection.add(files[i].id)
      }
      selectedFileIds = newSelection
      activeFileId = fileId
    } else {
      // Normal click: select single, clear others
      selectedFileIds = new Set()
      activeFileId = fileId
    }
  }

  const handleContextMenu = (fileId: string, event: MouseEvent) => {
    event.preventDefault()
    // If right-clicking on unselected file, select it
    if (!selectedFileIds.has(fileId)) {
      selectedFileIds = new Set([fileId])
      activeFileId = fileId
    }
    contextMenu = { x: event.clientX, y: event.clientY, fileId }
  }

  const closeContextMenu = () => {
    contextMenu = null
  }

  const contextExportSelected = async () => {
    closeContextMenu()
    const ids = getSelectionIds()
    if (ids.length === 0) return
    try {
      const outputs = await api.exportFiles(ids)
      if (outputs && outputs.length) {
        showToast(`Exported ${outputs.length} file${outputs.length > 1 ? 's' : ''}`)
      }
    } catch (error) {
      showToast('Export failed')
    }
  }

  const contextRemoveSelected = () => {
    closeContextMenu()
    const ids = getSelectionIds()
    files = files.filter((f) => !ids.includes(f.id))
    selectedFileIds = new Set()
    if (activeFileId && ids.includes(activeFileId)) {
      activeFileId = files.length > 0 ? files[0].id : null
    }
  }

  const contextSelectAll = () => {
    closeContextMenu()
    const selection = new Set(files.map((f) => f.id))
    selectedFileIds = selection
    syncActiveToSelection(selection)
  }

  const contextClearSelection = () => {
    closeContextMenu()
    selectedFileIds = new Set()
  }

  const contextInvertSelection = () => {
    closeContextMenu()
    if (!files.length) return
    const current = new Set(getSelectionIds())
    const next = new Set<string>()
    files.forEach((file) => {
      if (!current.has(file.id)) {
        next.add(file.id)
      }
    })
    selectedFileIds = next
    syncActiveToSelection(next)
  }

  const contextCopyNames = async () => {
    closeContextMenu()
    const selection = getSelectionFiles()
    if (!selection.length) return
    const text = selection.map((file) => `${file.name}${file.currentExtension}`).join('\n')
    try {
      await Clipboard.SetText(text)
      showToast(`Copied ${selection.length} name${selection.length > 1 ? 's' : ''}`)
    } catch (error) {
      showToast('Copy failed')
    }
  }

  const contextCopyPaths = async () => {
    closeContextMenu()
    const selection = getSelectionFiles()
    if (!selection.length) return
    const text = selection.map((file) => file.originalPath || file.workingPath).join('\n')
    try {
      await Clipboard.SetText(text)
      showToast(`Copied ${selection.length} path${selection.length > 1 ? 's' : ''}`)
    } catch (error) {
      showToast('Copy failed')
    }
  }

  const applySkill = async (skill: Skill, params: Record<string, unknown>) => {
    const targets = getTargetFileIds()
    await withBusy(`Applying ${skill.name}`, targets.length, async () => {
      try {
        const result = await api.executeSkill(targets, skill.id, params)
        handleSkillResult(result, skill)
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error)
        showToast(message || 'Something went wrong')
        console.error(error)
      } finally {
        resetCommand()
      }
    })
  }

  const handleSkillResult = (result: SkillResult, skill: Skill) => {
    if (result?.session) {
      session = result.session as SessionSnapshotExt
      applyAccent(session.accentColor)
    }
    if (result?.updatedFiles?.length) {
      updateFiles(result.updatedFiles)
    }
    if (skill.id === 'clear_all') {
      files = []
      activeFileId = null
    }
    if (result?.message) {
      showToast(result.message)
    }
  }

  const confirmParam = async () => {
    if (!activeSkill || !activeParam) return
    const value = parseParam(activeParam, paramValue)
    await applySkill(activeSkill, { [activeParam.name]: value })
  }

  const parseParam = (param: ParamDef, raw: string): unknown => {
    if (param.type === 'int') {
      return parseInt(raw, 10)
    }
    if (param.type === 'float') {
      return parseFloat(raw)
    }
    return raw
  }

  const cyclePreset = () => {
    if (!activeParam?.presets?.length) return
    const presets = activeParam.presets.map(presetValue)
    const currentIndex = presets.indexOf(paramValue)
    const nextIndex = currentIndex >= 0 ? (currentIndex + 1) % presets.length : 0
    paramValue = presets[nextIndex]
  }

  const onCommandKeydown = async (event: KeyboardEvent) => {
    if (event.key === 'Tab' && isParamMode) {
      event.preventDefault()
      cyclePreset()
      return
    }
    if (event.key === 'Escape') {
      event.preventDefault()
      if (isParamMode) {
        resetCommand()
      } else {
        showDropdown = false
        query = ''
      }
      return
    }
    if (event.key === 'Enter') {
      event.preventDefault()
      if (isParamMode) {
        await confirmParam()
        return
      }
      const selected = skills[highlightIndex]
      if (selected) {
        await selectSkill(selected)
      }
    }
    if (!isParamMode && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
      event.preventDefault()
      if (!skills.length) return
      const delta = event.key === 'ArrowDown' ? 1 : -1
      highlightIndex = (highlightIndex + delta + skills.length) % skills.length
    }
  }

  const onQueryInput = async () => {
    showDropdown = true
    await refreshSkills()
  }

  const onInputFocus = () => {
    showDropdown = true
  }

  const onInputBlur = () => {
    setTimeout(() => {
      showDropdown = false
    }, 150)
  }

  const removeSkill = async (fileId: string, index: number) => {
    if (isBusy) return
    isBusy = true
    try {
      const updated = await api.removeSkill(fileId, index)
      updateFiles([updated])
      await refreshSkills()
    } finally {
      isBusy = false
    }
  }

  const toggleMode = async (mode: 'batch' | 'per_file') => {
    try {
      const snapshot = await api.setMode(mode)
      session = snapshot as SessionSnapshotExt
      applyAccent(session.accentColor)
      if (mode === 'per_file' && !activeFileId && files.length) {
        activeFileId = files[0].id
      }
      await refreshSkills()
    } catch (error) {
      console.error('Failed to toggle mode:', error)
    }
  }

  const exportAll = async () => {
    if (!files.length) return
    const targets = getTargetFileIds()
    await withBusy('Exporting', targets.length, async () => {
      try {
        const outputs = await api.exportFiles(targets)
        if (outputs && outputs.length) {
          showToast(`Exported ${outputs.length} file${outputs.length > 1 ? 's' : ''}`)
        }
      } catch (error) {
        showToast('Export failed')
        console.error(error)
      }
    })
  }

  const clearAll = async () => {
    await withBusy('Clearing', files.length, async () => {
      try {
        await api.clearAll()
        files = []
        activeFileId = null
        await refreshSkills()
      } catch (e) {
        console.error('Failed to clear:', e)
      }
    })
  }

  const displaySkillName = (skillId: string): string => {
    const found = skills.find((item) => item.id === skillId)
    if (found) return found.name
    return skillId.replace(/_/g, ' ')
  }

  const truncatePath = (path: string): string => {
    if (!path) return 'Same as original'
    const parts = path.split('/')
    if (parts.length <= 3) return path
    return '.../' + parts.slice(-2).join('/')
  }

  onMount(async () => {
    await loadSession()
    applyAccent(session.accentColor)
    await refreshSkills()

    const unsubSkillsUpdated = AppEvents.on('asteria:skills-updated', () => {
      void refreshSkills()
    })

    // Listen for native file drop events (Wails runtime). Keep compatible with
    // both the built-in event name and our backend-emitted event.
    const onDrop = (ev: { name: string; data: any }) => {
      const payload = ev?.data
      const paths = payload?.filenames || payload?.files || payload?.paths || payload
      if (Array.isArray(paths)) {
        void addFiles(paths)
      }
    }
    const unsubFileDrop = AppEvents.on(FILE_DROP_EVENT, onDrop)
    const unsubFileDropCompat = AppEvents.on('asteria:file-drop', onDrop)

    const onKey = (event: KeyboardEvent) => {
      if (event.metaKey && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        commandInput?.focus()
        showDropdown = true
      }
      if (event.metaKey && event.key.toLowerCase() === 'o') {
        event.preventDefault()
        openPicker()
      }
      if (event.metaKey && event.key.toLowerCase() === 'a' && files.length > 0) {
        event.preventDefault()
        selectedFileIds = new Set(files.map((f) => f.id))
      }
      if (event.key === 'Escape') {
        closeContextMenu()
        selectedFileIds = new Set()
      }
    }
    
    const onClickOutside = () => {
      closeContextMenu()
    }

    window.addEventListener('keydown', onKey)
    window.addEventListener('click', onClickOutside)
    
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('click', onClickOutside)
      unsubFileDrop()
      unsubFileDropCompat()
      unsubSkillsUpdated()
    }
  })
</script>

<div class="app-shell">
  <!-- Titlebar - draggable region -->
  <header class="titlebar" style="--wails-draggable: drag">
    <span class="titlebar-title">Asteria</span>
    <div class="mode-toggle" style="--wails-draggable: no-drag">
      <button 
        class="mode-btn" 
        class:active={session.mode === 'batch'} 
        on:click={() => toggleMode('batch')}
      >
        Batch
      </button>
      <button 
        class="mode-btn" 
        class:active={session.mode === 'per_file'} 
        on:click={() => toggleMode('per_file')}
      >
        Per-file
      </button>
    </div>
  </header>

  <!-- Content area - files preview (now on TOP) -->
  <section class="content-area" data-file-drop-target>
    {#if !files.length}
      <div class="empty-state">
        <div class="empty-icon">+</div>
        <div class="empty-title">Drop files here</div>
        <p class="empty-subtitle">or press <kbd>⌘O</kbd> to browse</p>
      </div>
    {:else}
      <div class="file-grid">
        {#each files as file}
          <button
            class="file-card"
            class:active={session.mode === 'per_file' && activeFileId === file.id}
            class:selected={selectedFileIds.has(file.id)}
            on:click={(e) => handleFileClick(file.id, e)}
            on:contextmenu={(e) => handleContextMenu(file.id, e)}
          >
            <div class="file-preview">
              {#if file.previewDataUrl}
                <img src={file.previewDataUrl} alt={file.name} />
              {:else}
                <span class="file-preview-fallback">{file.currentExtension.replace('.', '')}</span>
              {/if}
            </div>
            <div class="file-name">{file.name}{file.currentExtension}</div>
            {#if file.appliedSkills && file.appliedSkills.length > 0}
              <div class="skill-tags">
                {#each file.appliedSkills as applied, index}
                  <span class="skill-tag">
                    {displaySkillName(applied.skillId)}
                    <button class="skill-remove" on:click|stopPropagation={() => removeSkill(file.id, index)}>×</button>
                  </span>
                {/each}
              </div>
            {/if}
          </button>
        {/each}
      </div>
    {/if}
  </section>

  <!-- Command bar section (now at BOTTOM, above footer) -->
  <section class="command-section">
    {#if isBusy && busyText}
      <div class="busy-indicator" aria-live="polite">
        <div class="busy-row">
          <span class="busy-text">{busyText}{busyTotal > 1 ? ` (${busyTotal})` : ''}</span>
          <span class="busy-dot"></span>
        </div>
        <div class="busy-bar"><div class="busy-bar-inner"></div></div>
      </div>
    {/if}
    {#if isParamMode && activeSkill}
      <!-- Parameter mode - completely redesigned -->
      <div class="param-panel">
        <div class="param-header">
          <span class="param-skill-name">{activeSkill.name}</span>
          <button class="param-cancel" on:click={resetCommand}>
            Cancel <kbd>Esc</kbd>
          </button>
        </div>
        <div class="param-body">
          <label class="param-label">{activeParam?.label ?? 'Value'}</label>
          <div class="param-input-row">
            <input
              type="text"
              class="param-input"
              bind:value={paramValue}
              bind:this={commandInput}
              on:keydown={onCommandKeydown}
              autocomplete="off"
              autofocus
            />
            <span class="param-unit">{activeParam?.unit ?? ''}</span>
            <button class="param-apply" on:click={confirmParam}>
              Apply <kbd>↵</kbd>
            </button>
          </div>
          {#if activeParam?.presets?.length}
            <div class="param-presets">
              <span class="presets-label">Quick:</span>
              {#each activeParam.presets as preset}
                <button 
                  class="preset-chip" 
                  class:active={paramValue === presetValue(preset)}
                  on:click={() => (paramValue = presetValue(preset))}
                >
                  {#if activeSkill.id === 'set_accent_color'}
                    <span class="preset-swatch" style={`--swatch:${presetSwatchCss(preset)}`}></span>
                    {presetLabel(preset)}
                  {:else}
                    {presetLabel(preset)}{activeParam.unit ?? ''}
                  {/if}
                </button>
              {/each}
            </div>
          {/if}
        </div>
      </div>
    {:else}
      <!-- Normal command input -->
      <div class="command-input-wrapper">
        <span class="command-icon">⌘</span>
        <input
          type="text"
          class="command-input"
          bind:value={query}
          bind:this={commandInput}
          placeholder={files.length ? 'Search skills...' : 'Drop files or ⌘O to add'}
          on:keydown={onCommandKeydown}
          on:input={onQueryInput}
          on:focus={onInputFocus}
          on:blur={onInputBlur}
          autocomplete="off"
        />
        {#if isBusy}
          <div class="spinner"></div>
        {/if}

        <!-- Dropdown suggestions (opens upward now) -->
        {#if showDropdown && skills.length > 0}
          <div class="suggestions-dropdown">
            {#each skills.slice(0, 6) as skill, index}
              <button
                class="suggestion-item"
                class:active={index === highlightIndex}
                on:mousedown|preventDefault={() => selectSkill(skill)}
              >
                <span class="suggestion-name">{skill.name}</span>
                <span class="suggestion-desc">{skill.description}</span>
              </button>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
  </section>

  <!-- Footer bar -->
  <footer class="footer-bar">
    <div class="footer-left">
      <span class="footer-label">Output</span>
      <span class="footer-value">{truncatePath(session.outputFolder)}</span>
    </div>
    <div class="footer-actions">
      <button class="btn-ghost" on:click={clearAll} disabled={!files.length}>Clear</button>
      <button class="btn-primary" disabled={!files.length} on:click={exportAll}>
        Export{selectedFileIds.size > 0 ? ` (${selectedFileIds.size})` : files.length ? ` (${files.length})` : ''}
      </button>
    </div>
  </footer>

  {#if toast}
    <div class="toast">{toast}</div>
  {/if}

  <!-- Context menu -->
  {#if contextMenu}
    <div 
      class="context-menu" 
      style="left: {contextMenu.x}px; top: {contextMenu.y}px;"
      on:click|stopPropagation
    >
      <div class="context-menu-label">{selectionCount} selected</div>
      <button class="context-menu-item" on:click={contextSelectAll}>
        Select All <kbd>⌘A</kbd>
      </button>
      <button class="context-menu-item" on:click={contextClearSelection}>
        Clear Selection
      </button>
      <button class="context-menu-item" on:click={contextInvertSelection}>
        Invert Selection
      </button>
      <div class="context-menu-separator"></div>
      <button class="context-menu-item" on:click={contextCopyNames}>
        Copy Names
      </button>
      <button class="context-menu-item" on:click={contextCopyPaths}>
        Copy Paths
      </button>
      <div class="context-menu-separator"></div>
      <button class="context-menu-item" on:click={contextExportSelected}>
        Export {selectionCount > 1 ? `(${selectionCount})` : ''}
      </button>
      <div class="context-menu-separator"></div>
      <button class="context-menu-item danger" on:click={contextRemoveSelected}>
        Remove {selectionCount > 1 ? `(${selectionCount})` : ''}
      </button>
    </div>
  {/if}
</div>
