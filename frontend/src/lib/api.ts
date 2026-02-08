// Wails v3 bindings
import { App } from '../../bindings/asteria'
import { Events } from '@wailsio/runtime'

// Re-export types from generated bindings
export type { Skill, ParamDef } from '../../bindings/asteria/internal/skills/models'
export type { SessionSnapshot, WorkingFile, ExportResult, AppliedSkill } from '../../bindings/asteria/internal/session/models'
export type { SkillResult } from '../../bindings/asteria/internal/executor/models'

export const api = {
  getSession: () => App.GetSession(),
  getSkills: (query: string, inputTypes: string[]) => App.GetSkills(query, inputTypes),
  openFilesDialog: () => App.OpenFilesDialog(),
  addFiles: (paths: string[]) => App.AddFiles(paths),
  executeSkill: (fileIds: string[], skillId: string, params: Record<string, unknown>) =>
    App.ExecuteSkill(fileIds, skillId, params),
  removeSkill: (fileId: string, index: number) => App.RemoveSkill(fileId, index),
  setMode: (mode: string) => App.SetMode(mode),
  exportFiles: (fileIds: string[]) => App.ExportFiles(fileIds),
  clearAll: () => App.ClearAll()
}

// Wails v3 uses events for file drops: "common:WindowFilesDropped"
export const FILE_DROP_EVENT = 'common:WindowFilesDropped'

export const AppEvents = {
  on: (eventName: string, callback: (ev: { name: string; data: any }) => void): (() => void) => {
    return Events.On(eventName, callback)
  },
  emit: (eventName: string, data?: any): Promise<boolean> => {
    return Events.Emit(eventName, data)
  },
  off: (...eventNames: string[]) => {
    Events.Off(...(eventNames as [string, ...string[]]))
  }
}
