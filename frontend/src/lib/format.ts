export function size(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000
    i++
  }
  return i === 0 ? `${v} B` : `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`
}

export function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

/** Short label for a file type token: ".jpg" -> "JPG", "folder" -> "Folder". */
export function typeLabel(t: string): string {
  if (t === 'folder') return 'Folder'
  return t.replace(/^\./, '').toUpperCase()
}

export const isMac = typeof navigator !== 'undefined' && /Mac/.test(navigator.platform)
export const mod = isMac ? '⌘' : 'Ctrl'

/** True when an event target is a text field the user is typing in. */
export function isTyping(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false
  return el.isContentEditable || el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT'
}
