// Appearance and accessibility overrides, applied to <html> so CSS can key
// off data attributes. The last choice is cached in localStorage so the
// window paints in the right theme before the backend answers.

export type Appearance = 'system' | 'light' | 'dark'

const root = document.documentElement
const darkQuery = matchMedia('(prefers-color-scheme: dark)')
const motionQuery = matchMedia('(prefers-reduced-motion: reduce)')
let appearance: Appearance = 'system'

function resolve() {
  const dark = appearance === 'dark' || (appearance === 'system' && darkQuery.matches)
  root.dataset.theme = dark ? 'dark' : 'light'
}

export function setAppearance(a: string | undefined) {
  appearance = a === 'light' || a === 'dark' ? a : 'system'
  resolve()
  try {
    localStorage.setItem('asteria.appearance', appearance)
  } catch {}
}

export function setOverrides(o: { reduceMotion?: boolean; reduceTransparency?: boolean }) {
  root.dataset.motion = o.reduceMotion ? 'reduce' : ''
  root.dataset.transparency = o.reduceTransparency ? 'reduce' : ''
}

// An earlier build cached a flag for a glass window; clear it so no stale state lingers.
try {
  localStorage.removeItem('asteria.glass')
} catch {}

/** True when motion should be minimal, by OS setting or the app override. */
export function motionReduced(): boolean {
  return motionQuery.matches || root.dataset.motion === 'reduce'
}

darkQuery.addEventListener('change', resolve)
try {
  setAppearance(localStorage.getItem('asteria.appearance') ?? 'system')
} catch {
  resolve()
}
