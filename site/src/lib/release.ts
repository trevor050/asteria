// Resolves the latest published release at build time. Every asset link is
// checked with a real request before it is used; anything that cannot be
// checked falls back to the releases page and is marked as such.
export const REPO = 'trevor050/asteria'
export const RELEASES_URL = `https://github.com/${REPO}/releases`
export const REPO_URL = `https://github.com/${REPO}`

export type OS = 'mac' | 'windows' | 'linux'

export interface Download {
  os: OS
  label: string
  file: string
  url: string
  size: number | null
  /** true when this is the releases page, not a checked file link */
  fallback: boolean
}

export interface Release {
  tag: string | null
  version: string | null
  url: string
  published: string | null
  mac: Download[]
  windows: Download[]
  linux: Download[]
  checksums: Download | null
  /** true when no release could be resolved: every link is the releases page */
  fallback: boolean
}

const patterns: { os: OS | 'sums'; label: string; re: RegExp }[] = [
  { os: 'mac', label: 'Mac app (.dmg)', re: /-macos-universal\.dmg$/i },
  { os: 'windows', label: 'Installer (.exe)', re: /-windows-x64-setup\.exe$/i },
  { os: 'windows', label: 'No install needed (.exe)', re: /-windows-x64-portable\.exe$/i },
  { os: 'linux', label: 'Ubuntu and Debian (.deb)', re: /-linux-amd64\.deb$/i },
  { os: 'linux', label: 'AppImage', re: /-linux-x86_64\.AppImage$/i },
  { os: 'linux', label: 'Archive (.tar.gz)', re: /-linux-amd64\.tar\.gz$/i },
  { os: 'sums', label: 'SHA-256 checksums', re: /^SHA256SUMS\.txt$/i },
]

const headers: Record<string, string> = {
  Accept: 'application/vnd.github+json',
  'User-Agent': 'asteria-site-build',
  'X-GitHub-Api-Version': '2022-11-28',
}
if (process.env.GITHUB_TOKEN) headers.Authorization = `Bearer ${process.env.GITHUB_TOKEN}`

async function resolves(url: string): Promise<boolean> {
  try {
    // A ranged GET follows GitHub's redirect to the file host without
    // downloading the whole file (HEAD is not always honoured there).
    const res = await fetch(url, { method: 'GET', headers: { Range: 'bytes=0-0', 'User-Agent': headers['User-Agent'] }, redirect: 'follow' })
    await res.body?.cancel()
    return res.status === 200 || res.status === 206
  } catch {
    return false
  }
}

function fallbackRelease(): Release {
  const page = (os: OS, label: string): Download => ({ os, label, file: '', url: RELEASES_URL, size: null, fallback: true })
  return {
    tag: null,
    version: null,
    url: RELEASES_URL,
    published: null,
    mac: [page('mac', 'Mac (Apple silicon and Intel)')],
    windows: [page('windows', 'Windows installer')],
    linux: [page('linux', 'Linux packages')],
    checksums: null,
    fallback: true,
  }
}

let cached: Promise<Release> | null = null

export function getRelease(): Promise<Release> {
  cached ??= load()
  return cached
}

async function load(): Promise<Release> {
  if (process.env.ASTERIA_SITE_OFFLINE === '1') {
    console.warn('[release] ASTERIA_SITE_OFFLINE=1: using the releases page for every download')
    return fallbackRelease()
  }
  let data: any
  try {
    const res = await fetch(`https://api.github.com/repos/${REPO}/releases/latest`, { headers })
    if (!res.ok) {
      console.warn(`[release] GitHub API answered ${res.status}; download links fall back to ${RELEASES_URL}`)
      return fallbackRelease()
    }
    data = await res.json()
  } catch (err) {
    console.warn(`[release] could not reach GitHub (${(err as Error).message}); download links fall back to ${RELEASES_URL}`)
    return fallbackRelease()
  }

  const release: Release = {
    tag: data.tag_name,
    version: String(data.tag_name).replace(/^v/, ''),
    url: data.html_url,
    published: data.published_at,
    mac: [],
    windows: [],
    linux: [],
    checksums: null,
    fallback: false,
  }

  const checks = await Promise.all(
    (data.assets ?? []).map(async (a: any) => {
      const p = patterns.find((x) => x.re.test(a.name))
      if (!p) return null
      const ok = await resolves(a.browser_download_url)
      if (!ok) console.warn(`[release] ${a.name} did not resolve; it is left out`)
      return ok ? { p, a } : null
    }),
  )
  for (const hit of checks) {
    if (!hit) continue
    const { p, a } = hit
    const d: Download = {
      os: p.os === 'sums' ? 'linux' : p.os,
      label: p.label,
      file: a.name,
      url: a.browser_download_url,
      size: a.size ?? null,
      fallback: false,
    }
    if (p.os === 'sums') release.checksums = d
    else release[p.os].push(d)
  }
  const order = (os: OS) => patterns.filter((p) => p.os === os).map((p) => p.label)
  for (const os of ['mac', 'windows', 'linux'] as const) {
    release[os].sort((x, y) => order(os).indexOf(x.label) - order(os).indexOf(y.label))
    if (!release[os].length) {
      console.warn(`[release] no checked ${os} file in ${release.tag}; linking the release page instead`)
      release[os].push({ os, label: 'Release page', file: '', url: release.url, size: null, fallback: true })
    }
  }
  console.log(
    `[release] ${release.tag}: ${[...release.mac, ...release.windows, ...release.linux].filter((d) => !d.fallback).length} checked downloads${release.checksums ? ' + checksums' : ''}`,
  )
  return release
}

export const formatSize = (bytes: number | null) =>
  bytes == null ? '' : bytes >= 1e6 ? `${(bytes / 1e6).toFixed(bytes >= 1e8 ? 0 : 1)} MB` : `${Math.round(bytes / 1e3)} KB`
