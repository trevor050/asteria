// Client-side search over the app's real skills and verified conversions.
// The ranking follows the app's own tiers (internal/skills/ranking.go,
// matchTier): name exact > name prefix > name words > alias exact > alias
// prefix > alias words > fuzzy > typo > description, with skills that fit the
// dropped files first and priority breaking ties.

export interface Skill {
  id: string
  n: string
  al: string[]
  d: string
  c: string
  i: string
  p: number
  acc: string[]
  out: string
  prm: [string, string, string] | null
  ad: string | null
}
/** [from, to, slug, addonId, skillId, typed, hasPage] */
export type PairRow = [string, string, string, string | null, string, string, 0 | 1]

export interface Data {
  skills: Skill[]
  pairs: PairRow[]
  names: Record<string, string>
  addons: Record<string, string>
  icons: Record<string, string>
}

let loading: Promise<Data> | null = null
export function loadData(): Promise<Data> {
  loading ??= fetch('/data/search.json').then((r) => {
    if (!r.ok) throw new Error(`search data: ${r.status}`)
    return r.json()
  })
  return loading
}

const T = {
  none: 0,
  description: 1,
  typo: 2,
  fuzzy: 3,
  aliasWords: 4,
  aliasPrefix: 5,
  aliasExact: 6,
  nameWords: 7,
  namePrefix: 8,
  nameExact: 9,
}

const words = (s: string) => s.toLowerCase().split(/[^\p{L}\p{N}]+/u).filter(Boolean)
const wordPrefixes = (q: string[], target: string[]) =>
  q.length > 0 && q.every((w) => target.some((t) => t.startsWith(w)))

function subsequence(name: string, q: string): boolean {
  if (q.length < 2) return false
  let qi = 0
  let starts = 0
  for (let i = 0; i < name.length && qi < q.length; i++) {
    if (name[i] !== q[qi]) continue
    if (i === 0 || !/\p{L}/u.test(name[i - 1])) starts++
    qi++
  }
  return qi === q.length && starts > 0
}

function lev(a: string, b: string): number {
  const d = Array.from({ length: b.length + 1 }, (_, i) => i)
  for (let i = 1; i <= a.length; i++) {
    let prev = d[0]
    d[0] = i
    for (let j = 1; j <= b.length; j++) {
      const t = d[j]
      d[j] = Math.min(d[j] + 1, d[j - 1] + 1, prev + (a[i - 1] === b[j - 1] ? 0 : 1))
      prev = t
    }
  }
  return d[b.length]
}
const typo = (q: string[], target: string[]) =>
  q.length > 0 && q.every((w) => (w.length < 4 ? target.some((t) => t.startsWith(w)) : target.some((t) => lev(w, t.slice(0, w.length + 1)) <= 1)))

export function matchTier(s: Skill, text: string): [number, string] {
  const q = text.trim().toLowerCase()
  if (!q) return [T.none, '']
  const name = s.n.toLowerCase()
  const qw = words(q)
  if (name === q) return [T.nameExact, '']
  if (name.startsWith(q)) return [T.namePrefix, '']
  if (wordPrefixes(qw, words(s.n))) return [T.nameWords, '']
  let best = T.none
  let which = ''
  for (const al of s.al) {
    let t = T.none
    if (al === q) t = T.aliasExact
    else if (al.startsWith(q)) t = T.aliasPrefix
    else if (wordPrefixes(qw, words(al))) t = T.aliasWords
    else if (al.includes(q)) t = T.fuzzy
    if (t > best) {
      best = t
      which = al
    }
  }
  if (best >= T.aliasWords) return [best, which]
  if (name.includes(q) || subsequence(name, q.replace(/ /g, ''))) return [T.fuzzy, '']
  if (best === T.fuzzy) return [T.fuzzy, which]
  if (typo(qw, words(s.n))) return [T.typo, '']
  if (q.length >= 4 && s.d.toLowerCase().includes(q)) return [T.description, '']
  return [T.none, '']
}

/** "resize 50%" -> ["resize", "50%"] (the app's SplitInline). */
export function splitInline(q: string): [string, string] {
  const toks = q.trim().split(/\s+/).filter(Boolean)
  let k = toks.length
  while (k > 1 && /\d/.test(toks[k - 1])) k--
  if (k === toks.length || k === 0) return [q.trim(), '']
  return [toks.slice(0, k).join(' '), toks.slice(k).join(' ')]
}

export const accepts = (s: Skill, ext: string) => s.acc.includes('*') || s.acc.includes(ext)

export interface Hit {
  skill: Skill
  tier: number
  alias: string
  value: string
  /** how many of the dropped files it fits */
  applies: number
}

export function rank(data: Data, query: string, exts: string[]): Hit[] {
  const q = query.trim()
  let [text, value] = splitInline(q)
  let hits = rankText(data, text, value, exts)
  if (!hits.length && !value) {
    const m = q.toLowerCase().match(/^([a-z]+)(\d[\d.%x:,\-]*)$/)
    if (m) hits = rankText(data, m[1], m[2], exts)
  }
  if (!value && q.includes(' ') && (hits[0]?.tier ?? 0) < T.aliasWords) {
    const toks = q.split(/\s+/)
    for (let k = toks.length - 1; k >= 1; k--) {
      const head = toks.slice(0, k).join(' ')
      if (data.skills.some((s) => s.prm && matchTier(s, head)[0] >= T.aliasWords)) {
        return rankText(data, head, toks.slice(k).join(' '), exts)
      }
    }
  }
  return hits
}

function rankText(data: Data, text: string, value: string, exts: string[]): Hit[] {
  const scored: (Hit & { score: number })[] = []
  let strongest = 0
  const tiers = data.skills.map((s) => {
    const t = matchTier(s, text)
    strongest = Math.max(strongest, t[0])
    return t
  })
  data.skills.forEach((s, i) => {
    const [tier, alias] = tiers[i]
    if (tier === T.none) return
    if (strongest >= T.aliasWords && tier < T.fuzzy) return
    const applies = exts.filter((e) => accepts(s, e)).length
    const usable = !exts.length || applies > 0
    const noOp = exts.length > 0 && !!s.out && exts.every((e) => e === s.out || (s.out === 'jpg' && e === 'jpeg'))
    let score = tier * 1000 + Math.min(20, Math.max(0, s.p)) * 3
    if (usable && !noOp) score += 1e5
    scored.push({ skill: s, tier, alias, value: value && s.prm ? value : '', applies, score })
  })
  scored.sort((a, b) => b.score - a.score || a.skill.n.localeCompare(b.skill.n))
  return scored
}

/** Quick actions for dropped files, like the app's quick row: specific skills first, one per category. */
export function quick(data: Data, exts: string[], max = 4): Skill[] {
  const fitting = data.skills.filter((s) => exts.some((e) => accepts(s, e)))
  const specific = fitting.filter((s) => !s.acc.includes('*') && !(s.out && exts.every((e) => e === s.out)))
  const generic = fitting.filter((s) => s.acc.includes('*'))
  specific.sort((a, b) => b.p - a.p)
  const out: Skill[] = []
  const cats = new Set<string>()
  for (const s of specific) {
    if (out.length >= max) break
    if (cats.has(s.c)) continue
    cats.add(s.c)
    out.push(s)
  }
  for (const s of [...specific, ...generic]) {
    if (out.length >= max) break
    if (!out.includes(s)) out.push(s)
  }
  return out
}

export function extOf(name: string): string {
  const lower = name.toLowerCase()
  for (const c of ['.tar.gz', '.tar.xz', '.tar.zst', '.tar.bz2']) if (lower.endsWith(c)) return c.slice(1)
  const i = lower.lastIndexOf('.')
  return i > 0 ? lower.slice(i + 1) : ''
}

export function findPair(data: Data, from: string, to: string): PairRow | undefined {
  const norm = (e: string) => e.replace(/^\./, '').toLowerCase()
  const f = norm(from)
  const t = norm(to)
  return data.pairs.find((p) => p[0] === f && p[1] === t) ?? (t === 'jpeg' ? data.pairs.find((p) => p[0] === f && p[1] === 'jpg') : undefined)
}

export const formatBytes = (n: number) =>
  n >= 1e9 ? `${(n / 1e9).toFixed(1)} GB` : n >= 1e6 ? `${(n / 1e6).toFixed(1)} MB` : n >= 1e3 ? `${Math.round(n / 1e3)} KB` : `${n} bytes`

export const esc = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`)
