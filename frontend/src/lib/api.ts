import { Events } from '@wailsio/runtime'
import { App } from '../../bindings/asteria'

export type { AddResult, AddonView, Result, State, Suggestion, ToolStatus, TrustRequest } from '../../bindings/asteria/models'
export type { View as FileView, Step, ExportResult } from '../../bindings/asteria/internal/session/models'
export type { Info } from '../../bindings/asteria/internal/inspect/models'
export type { Outcome, Problem } from '../../bindings/asteria/internal/engine/models'

/** Sent on the asteria:progress event while an operation runs. */
export type Progress = { label: string; done: number; total: number; fraction: number; current: string }
export type { Settings } from '../../bindings/asteria/internal/storage/models'
export type { ParamDef } from '../../bindings/asteria/internal/skills/models'

export const api = App

export const events = {
  progress: 'asteria:progress',
  filesAdded: 'asteria:files-added',
  skillsChanged: 'asteria:skills-changed',
  addonProgress: 'asteria:addon-progress',
} as const

export function on<T>(name: string, fn: (data: T) => void): () => void {
  return Events.On(name, (ev: { data: T }) => fn(ev.data))
}

/** URL of an image thumbnail for a file's current (or given) step. */
export function thumbURL(id: string, rev: string, size = 480): string {
  return `/asteria/thumb/${encodeURIComponent(id)}/${encodeURIComponent(rev)}?s=${size}`
}

/** URL that streams the file's output (video, audio, PDF). */
export function fileURL(id: string, rev: string): string {
  return `/asteria/file/${encodeURIComponent(id)}/${encodeURIComponent(rev)}`
}

// Exposed for automated UI tests (server-mode builds driven by Playwright).
// Page scripts can already call every binding, so this adds no capability.
;(window as unknown as { __asteria: typeof App }).__asteria = App
