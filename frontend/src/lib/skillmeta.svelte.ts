import { api } from './api'

/** Icon and family per skill id, for places that only know a step's skill. */
export const skillMeta: Record<string, { icon: string; category: string }> = $state({})

export async function loadSkillMeta() {
  const all = (await api.ListSkills()) ?? []
  for (const s of all) skillMeta[s.id] = { icon: s.icon, category: s.category }
}
