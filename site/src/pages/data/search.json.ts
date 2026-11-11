// The data the page's command bar and finder search, built from the repo.
// Fetched once, lazily, from this same site.
import type { APIRoute } from 'astro'
import { allPairs, skills, formats, addons } from '../../lib/data'
import { icons, familyIcons } from '../../../../frontend/src/lib/icons'

export const GET: APIRoute = () => {
  const usedIcons: Record<string, string> = {}
  const iconFor = (s: { icon: string; category: string }) => {
    const key = icons[s.icon] ? s.icon : familyIcons[s.category] ?? 'file'
    if (icons[key]) usedIcons[key] = icons[key]
    return key
  }
  const body = {
    skills: skills.map((s) => ({
      id: s.id,
      n: s.name,
      al: s.aliases,
      d: s.description,
      c: s.category,
      i: iconFor(s),
      p: s.priority,
      acc: s.accepts,
      out: s.output,
      prm: s.param ? [s.param.label, s.param.unit, s.param.default] : null,
      ad: s.addon,
    })),
    pairs: allPairs.map((p) => [p.from, p.to, p.slug, p.addon?.id ?? null, p.skill.id, p.typed, p.to === 'folder' ? 0 : 1]),
    names: Object.fromEntries(formats.map((f) => [f.ext, f.name])),
    addons: Object.fromEntries(addons.map((a) => [a.id, a.name])),
    icons: usedIcons,
  }
  return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
}
