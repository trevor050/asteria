// Detects the visitor's system and points every [data-dl] button at its build.
export type OS = 'mac' | 'windows' | 'linux' | 'other'

export function detectOS(): OS {
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } }
  const p = (nav.userAgentData?.platform || nav.platform || '').toLowerCase()
  const ua = navigator.userAgent.toLowerCase()
  if (/iphone|ipad|ipod|android/.test(ua)) return 'other'
  if (p.includes('mac') || ua.includes('mac os')) return 'mac'
  if (p.includes('win') || ua.includes('windows')) return 'windows'
  if (p.includes('linux') || ua.includes('linux') || ua.includes('x11')) return 'linux'
  return 'other'
}

export const osName: Record<Exclude<OS, 'other'>, string> = { mac: 'Mac', windows: 'Windows', linux: 'Linux' }

export function wireDownloads(root: ParentNode = document) {
  const os = detectOS()
  if (os === 'other') return
  for (const a of root.querySelectorAll<HTMLAnchorElement>('a[data-dl]')) {
    const url = a.dataset[os]
    if (!url) continue
    // A fallback link goes to the release page; keep the in-page section,
    // which explains it, as the target instead.
    if (a.dataset.direct && !a.dataset[`${os}Fallback`]) a.href = url
    const label = a.querySelector('[data-dl-label]')
    if (label) label.textContent = `Download for ${osName[os]}`
  }
}
