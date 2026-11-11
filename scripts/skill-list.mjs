// Prints the README's skill list from skills/core, grouped by family.
//   node scripts/skill-list.mjs > /tmp/list.md
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..', 'skills', 'core')
const families = [
  ['convert', 'Convert'],
  ['transform', 'Size and shape'],
  ['sizes', 'Social and wallpaper sizes'],
  ['compress', 'Compress'],
  ['adjust', 'Color and tone'],
  ['filter', 'Effects'],
  ['compose', 'Compose and output'],
  ['info', 'Info and cleanup'],
  ['pdf', 'PDF'],
  ['media', 'Audio and video'],
  ['archive', 'Archives'],
  ['data', 'Data'],
]
const walk = (d) => readdirSync(d).flatMap((n) => (statSync(join(d, n)).isDirectory() ? walk(join(d, n)) : n.endsWith('.json') ? [join(d, n)] : []))
const skills = walk(root).map((f) => JSON.parse(readFileSync(f, 'utf8'))).filter((s) => !s.isMeta)
const video = (s) => (s.inputTypes ?? []).some((t) => t === '@video' || t === '@media')
const byFam = new Map()
for (const s of skills) byFam.set(s.category, [...(byFam.get(s.category) ?? []), s])
let total = 0
const out = ['| Family | Skills |', '| --- | --- |']
for (const [k, name] of families) {
  const list = (byFam.get(k) ?? []).sort((a, b) => a.name.localeCompare(b.name))
  if (!list.length) continue
  total += list.length
  out.push(`| ${name} (${list.length}) | ${list.map((s) => s.name + (video(s) && k !== 'media' ? '\\*' : '')).join(' · ')} |`)
}
console.log(out.join('\n'))
console.log(`\n${total} skills. \\* also works on video (needs ffmpeg).`)
