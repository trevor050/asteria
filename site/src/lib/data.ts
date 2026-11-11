// Build-time data for the site. Everything here is read from the repository
// itself (docs/formats.json, skills/core, addons/manifest.json and the Go
// type lists), so numbers and lists can never drift from the app.
import fs from 'node:fs'
import path from 'node:path'
import { rank as rankSkills, type Data as SearchData } from '../scripts/search'

const ROOT = path.resolve(process.cwd(), '..')
const read = (p: string) => fs.readFileSync(path.join(ROOT, p), 'utf8')
const readJSON = <T = any>(p: string): T => JSON.parse(read(p))

export type Platform = 'darwin-arm64' | 'linux-amd64' | 'windows-amd64' | string

export interface Addon {
  id: string
  name: string
  unlocks: string
  license: string
  homepage: string
  publisher: string
  /** systems where Asteria opens the official download page instead of installing it */
  manualOn: ('Mac' | 'Windows' | 'Linux')[]
}

export interface FormatInfo {
  ext: string
  name: string
  families: string[]
  read: boolean
  write: boolean
  builtIn: boolean
}

export interface Pair {
  slug: string
  from: string
  to: string
  fromName: string
  toName: string
  family: string
  /** null when at least one way to do it is built in */
  addon: Addon | null
  /** the skill a person would reach for, then the rest */
  skill: { id: string; name: string }
  others: { id: string; name: string }[]
  verifiedOn: Platform[]
  verifiedBy: Record<string, string>
  /** what to type in the command bar */
  typed: string
}

export interface SiteSkill {
  id: string
  name: string
  aliases: string[]
  description: string
  category: string
  icon: string
  priority: number
  /** expanded extensions (no dot), or ['*'] */
  accepts: string[]
  output: string
  param: { label: string; unit: string; default: string; hint: string } | null
  addon: string | null
}

// ---------------------------------------------------------------- formats

const formatsDoc = readJSON('docs/formats.json')
export const summary: {
  fileTypesRead: number
  fileTypesReadBuiltIn: number
  fileTypesWritten: number
  conversionPairs: number
  conversions: number
} = formatsDoc.summary
export const platformsTested: Record<string, { tested: string; engines: Record<string, string> }> =
  formatsDoc.platforms
export const formats: FormatInfo[] = formatsDoc.formats
export const formatByExt = new Map(formats.map((f) => [f.ext, f]))

// ---------------------------------------------------------------- add-ons

const manifest = readJSON('addons/manifest.json')
export const addons: Addon[] = manifest.engines.map((e: any) => ({
  id: e.id,
  name: e.name,
  unlocks: e.unlocks,
  license: e.license,
  homepage: e.homepage,
  publisher: e.publisher,
  manualOn: (
    [
      ['Mac', 'darwin-arm64'],
      ['Windows', 'windows-amd64'],
      ['Linux', 'linux-amd64'],
    ] as const
  )
    .filter(([, k]) => e.platforms?.[k]?.manual)
    .map(([os]) => os),
}))
export const addonById = new Map(addons.map((a) => [a.id, a]))
// Map the programs an add-on provides (ffmpeg, soffice, 7zz...) to the add-on.
const addonByBin = new Map<string, string>()
for (const e of manifest.engines) {
  addonByBin.set(e.id, e.id)
  for (const p of Object.values<any>(e.platforms ?? {})) {
    for (const bin of Object.keys(p?.bins ?? {})) addonByBin.set(bin, e.id)
  }
}
for (const [alias, real] of [
  ['7z', '7zz'],
  ['7za', '7zz'],
  ['gswin64c', 'gs'],
]) {
  if (addonByBin.has(real) && !addonByBin.has(alias)) addonByBin.set(alias, addonByBin.get(real)!)
}

// ---------------------------------------------------------------- platforms

export const platformLabel = (p: Platform) =>
  (
    ({
      'darwin-arm64': 'macOS on Apple silicon',
      'darwin-amd64': 'macOS on Intel',
      'linux-amd64': 'Linux',
      'linux-arm64': 'Linux on ARM',
      'windows-amd64': 'Windows',
    }) as Record<string, string>
  )[p] ?? p

// ---------------------------------------------------------------- pairs

const slugExt = (e: string) => e.replace(/\./g, '-')
export const pairSlug = (from: string, to: string) => `${slugExt(from)}-to-${slugExt(to)}`

const isConvertSkill = (id: string, name: string) =>
  /^convert_to_|_to_/.test(id) || /^(Convert|.* to )/.test(name)

// Pairs counted the same way as scripts/formats/build.mjs: every from/to except
// "anything" inputs. Pages exist for file-to-file pairs (not "folder").
const rawPairs = new Map<string, any[]>()
for (const c of formatsDoc.conversions) {
  if (c.from === '*') continue
  const k = `${c.from}>${c.to}`
  if (!rawPairs.has(k)) rawPairs.set(k, [])
  rawPairs.get(k)!.push(c)
}
if (rawPairs.size !== summary.conversionPairs) {
  throw new Error(
    `site/src/lib/data.ts counted ${rawPairs.size} pairs but docs/formats.json says ${summary.conversionPairs}. Keep the counting rule in step with scripts/formats/build.mjs.`,
  )
}

// What to type in the app: the shortest phrase that the app's ranking (ported
// in scripts/search.ts) puts first for this file. The skill's name always works.
function typedFor(from: string, to: string, skill: SiteSkill | undefined, ranked: SearchData): string {
  const name = (skill?.name ?? '').toLowerCase()
  if (!skill) return name
  const plain = skill.aliases.filter((a) => !/\d/.test(a)).sort((a, b) => a.length - b.length)
  const candidates = [...[`${from} to ${to}`, to].filter((c) => skill.aliases.includes(c)), name, ...plain]
  for (const c of candidates) {
    const top = rankSkills(ranked, c, [from])[0]
    if (top?.skill.id === skill.id && !top.value) return c
  }
  return name
}


export const allPairs: Pair[] = []
for (const [, entries] of rawPairs) {
  const first = entries[0]
  const builtIns = entries.filter((e) => !e.addon)
  const pool = builtIns.length ? builtIns : entries
  const pick = pool.find((e) => isConvertSkill(e.skill, e.skillName)) ?? pool[0]
  const verifiedOn = [...new Set(entries.flatMap((e) => e.verifiedOn))].sort()
  const verifiedBy: Record<string, string> = {}
  for (const e of entries) for (const [k, v] of Object.entries<string>(e.verifiedBy ?? {})) verifiedBy[k] ??= v
  allPairs.push({
    slug: pairSlug(first.from, first.to),
    from: first.from,
    to: first.to,
    fromName: first.fromName,
    toName: first.to === 'folder' ? 'Folder of files' : first.toName,
    family: first.family,
    addon: builtIns.length ? null : addonById.get(pick.addon.id) ?? null,
    skill: { id: pick.skill, name: pick.skillName },
    others: entries
      .filter((e) => e.skill !== pick.skill)
      .map((e) => ({ id: e.skill, name: e.skillName }))
      .filter((v, i, a) => a.findIndex((x) => x.id === v.id) === i),
    verifiedOn,
    verifiedBy,
    typed: '',
  })
}

// ---------------------------------------------------------------- skills

function goList(file: string, name: string): string[] {
  const src = read(file)
  const m = src.match(new RegExp(`${name}\\s*=\\s*\\[\\]string\\{([^}]*)\\}`))
  if (!m) throw new Error(`Could not find ${name} in ${file}; update site/src/lib/data.ts`)
  return [...m[1].matchAll(/"\.?([^"]+)"/g)].map((x) => x[1].toLowerCase())
}
const groups: Record<string, string[]> = {
  '@image': goList('internal/ops/image.go', 'ImageInputExts'),
  '@video': goList('internal/ops/media.go', 'VideoExts'),
  '@audio': goList('internal/ops/media.go', 'AudioExts'),
  '@pdf': ['pdf'],
}
groups['@media'] = [...groups['@video'], ...groups['@audio']]
for (const [g, file, name] of [
  ['@archive', 'internal/ops', 'ArchiveExts'],
  ['@data', 'internal/ops', 'DataExts'],
] as const) {
  const dir = path.join(ROOT, file)
  const hit = fs
    .readdirSync(dir)
    .filter((f) => f.endsWith('.go'))
    .find((f) => new RegExp(`${name}\\s*=\\s*\\[\\]string`).test(fs.readFileSync(path.join(dir, f), 'utf8')))
  if (!hit) throw new Error(`Could not find ${name} in internal/ops`)
  groups[g] = goList(`${file}/${hit}`, name)
}

const handlerAddon: [RegExp, string][] = [
  [/^media\./, 'ffmpeg'],
  [/^engine\.libreoffice/, 'soffice'],
  [/^engine\.ocr/, 'tesseract'],
  [/^engine\.pdfimages/, 'gs'],
  [/^engine\.trace/, 'potrace'],
]

const conversionAddon = new Map<string, string | null>()
for (const c of formatsDoc.conversions) {
  if (!conversionAddon.has(c.skill)) conversionAddon.set(c.skill, c.addon?.id ?? null)
}

function skillFiles(dir: string): string[] {
  const out: string[] = []
  for (const f of fs.readdirSync(dir)) {
    const p = path.join(dir, f)
    if (fs.statSync(p).isDirectory()) out.push(...skillFiles(p))
    else if (f.endsWith('.json')) out.push(p)
  }
  return out
}

export const skills: SiteSkill[] = skillFiles(path.join(ROOT, 'skills/core'))
  .map((f) => JSON.parse(fs.readFileSync(f, 'utf8')))
  .filter((s) => s.executor?.type !== 'meta')
  .map((s) => {
    const accepts = new Set<string>()
    for (const t of s.inputTypes ?? []) {
      if (t === '*') accepts.add('*')
      else if (groups[t]) groups[t].forEach((e) => accepts.add(e))
      else if (t.startsWith('@')) throw new Error(`Unknown type group ${t} in skill ${s.id}`)
      else accepts.add(t.replace(/^\./, '').toLowerCase())
    }
    let addon: string | null = null
    if (conversionAddon.has(s.id)) addon = conversionAddon.get(s.id) ?? null
    else {
      const bins: string[] = [...(s.requires ?? [])]
      if (s.executor?.command) bins.push(s.executor.command)
      for (const [re, bin] of handlerAddon) if (re.test(s.executor?.handler ?? '')) bins.push(bin)
      for (const b of bins) {
        if (addonByBin.has(b)) {
          addon = addonByBin.get(b)!
          break
        }
      }
    }
    const p = (s.params ?? [])[0]
    return {
      id: s.id,
      name: s.name,
      aliases: (s.aliases ?? []).map((a: string) => a.toLowerCase()),
      description: s.description ?? '',
      category: s.category ?? '',
      icon: s.icon ?? '',
      priority: Number(s.priority ?? 0),
      accepts: [...accepts],
      output: (s.outputType ?? '').replace(/^\./, ''),
      param: p
        ? {
            label: p.label ?? p.name,
            unit: p.unit ?? '',
            default: p.default == null ? '' : String(p.default),
            hint: p.hint ?? '',
          }
        : null,
      addon,
    }
  })
  .sort((a, b) => a.name.localeCompare(b.name))

{
  const ranked = {
    skills: skills.map((s) => ({ id: s.id, n: s.name, al: s.aliases, d: s.description, c: s.category, i: s.icon, p: s.priority, acc: s.accepts, out: s.output, prm: s.param ? [s.param.label, s.param.unit, s.param.default] : null, ad: s.addon })),
    pairs: [],
    names: {},
    addons: {},
    icons: {},
  } as unknown as SearchData
  const byId = new Map(skills.map((s) => [s.id, s]))
  for (const p of allPairs) p.typed = typedFor(p.from, p.to, byId.get(p.skill.id), ranked)
}

/** File-to-file pairs: these get their own page. */
export const pagePairs = allPairs
  .filter((p) => p.to !== 'folder')
  .sort((a, b) => a.slug.localeCompare(b.slug))

export const pairBySlug = new Map(pagePairs.map((p) => [p.slug, p]))

export const familyOrder = [
  'Images',
  'Vector',
  'PDF',
  'Documents',
  'Ebooks',
  'Audio and video',
  'Subtitles',
  'Data and config',
  'Text and encoding',
  'Email',
  'Fonts',
  'Maps and GPS',
  '3D models',
  'Color LUTs',
  'Archives',
]

export const verifiedPlatformsText = () => {
  const names = Object.keys(platformsTested).map(platformLabel)
  return names.length > 1 ? `${names.slice(0, -1).join(', ')} and ${names.at(-1)}` : names[0]
}
