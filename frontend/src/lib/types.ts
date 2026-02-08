export type ParamDef = {
  name: string
  type: string
  label: string
  default: unknown
  presets?: unknown[]
  options?: string[]
  min?: number
  max?: number
  unit?: string
}

export type Skill = {
  id: string
  name: string
  aliases: string[]
  category: string
  description: string
  inputTypes: string[]
  outputType: string
  params: ParamDef[]
  driver: string
  isMeta: boolean
  dangerLevel: number
}

export type AppliedSkill = {
  skillId: string
  params: Record<string, unknown>
  appliedAt: string
}

export type WorkingFile = {
  id: string
  name: string
  extension: string
  currentExtension: string
  originalPath: string
  workingPath: string
  size: number
  previewDataUrl: string
  appliedSkills: AppliedSkill[]
}

export type SessionSnapshot = {
  mode: string
  outputFolder: string
  namingPattern: string
}

export type SkillResult = {
  updatedFiles: WorkingFile[]
  session: SessionSnapshot
  message?: string
}

export type ExportResult = {
  fileId: string
  outputPath: string
}
