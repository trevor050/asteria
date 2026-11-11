// Captures the README screenshots, the demo GIF and a set of per-state
// screenshots from the server-mode build, driven by a real Chromium.
//
//   go build -tags server,nodynamic -o bin/asteria-server .   (from the repo root)
//   cd scripts/capture && npm install && node capture.mjs [readme|states|all]
//
// Demo media: four NASA photos (public domain, downloaded once into .media),
// a short video made from one of them with ffmpeg, and fixtures from
// testdata/fixtures. ffmpeg must be installed. The app runs with an isolated
// HOME so it never touches your real settings.

import { spawn, execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, rmSync, copyFileSync, writeFileSync, readdirSync } from 'node:fs'
import { join, dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright-core'

const here = dirname(fileURLToPath(import.meta.url))
const root = resolve(here, '../..')
const media = join(here, '.media')
const home = join(here, '.home')
const out = join(here, 'out')
const docs = join(root, 'docs')
const port = 34217
const url = `http://127.0.0.1:${port}/`
const mode = process.argv[2] ?? 'all'

const photos = [
  ['GSFC_20171208_Archive_e000393', 'Sun corona.jpg'],
  ['PIA12348', 'Galactic center.jpg'],
  ['as08-14-2383', 'Earthrise.jpg'],
  ['as17-148-22727', 'Blue Marble.jpg'],
]

async function prepareMedia() {
  mkdirSync(media, { recursive: true })
  for (const [id, name] of photos) {
    const dest = join(media, name)
    if (existsSync(dest)) continue
    const res = await fetch(`https://images-assets.nasa.gov/image/${id}/${id}~orig.jpg`)
    if (!res.ok) throw new Error(`download ${id}: ${res.status}`)
    writeFileSync(dest, Buffer.from(await res.arrayBuffer()))
  }
  const video = join(media, 'Earthrise flyby.mp4')
  if (!existsSync(video)) {
    execFileSync('ffmpeg', [
      '-v', 'error', '-y', '-loop', '1', '-i', join(media, 'Earthrise.jpg'),
      '-vf', "scale=2400:-1,zoompan=z='1+0.0015*on':x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)':d=360:s=1920x1080:fps=30",
      '-t', '12', '-c:v', 'libx264', '-pix_fmt', 'yuv420p', '-crf', '14', video,
    ])
  }
  const fixtures = join(root, 'testdata/fixtures')
  copyFileSync(join(fixtures, 'three-pages.pdf'), join(media, 'Mission brief.pdf'))
  copyFileSync(join(fixtures, 'three-pages.pdf'), join(media, 'Appendix.pdf'))
  copyFileSync(join(fixtures, 'people.json'), join(media, 'crew.json'))
}

function cleanExports() {
  // Exports land next to the originals; keep the media folder pristine.
  for (const f of readdirSync(media)) if (/\(\d+\)|\.webp$|\.png$/.test(f)) rmSync(join(media, f))
}

async function startServer() {
  rmSync(home, { recursive: true, force: true })
  mkdirSync(home, { recursive: true })
  const bin = process.env.ASTERIA_BIN ?? join(root, 'bin/asteria-server')
  if (!existsSync(bin)) throw new Error('build bin/asteria-server first (see the header of this file)')
  const proc = spawn(bin, [], { env: { ...process.env, HOME: home }, stdio: 'ignore' })
  for (let i = 0; i < 100; i++) {
    try {
      if ((await fetch(url)).ok) return proc
    } catch {}
    await new Promise((r) => setTimeout(r, 100))
  }
  proc.kill()
  throw new Error('server did not start')
}

const files = () => [
  ...photos.map(([, n]) => join(media, n)),
  join(media, 'Mission brief.pdf'),
  join(media, 'Appendix.pdf'),
  join(media, 'Earthrise flyby.mp4'),
  join(media, 'crew.json'),
]

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

async function open(browser, { theme = 'dark', w = 1000, h = 680, scale = 2, noFfmpeg = false } = {}) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: scale, colorScheme: theme })
  const page = await ctx.newPage()
  if (noFfmpeg) await mockMissingFfmpeg(page)
  await page.goto(url)
  await page.waitForFunction(() => typeof window.__asteriaAdd === 'function')
  await sleep(400)
  return { ctx, page }
}

// Shows the "needs ffmpeg" state on a machine that has ffmpeg, for design
// review only (never used for README images).
async function mockMissingFfmpeg(page) {
  await page.route('**/wails/runtime*', async (route) => {
    const res = await route.fetch()
    let body = await res.text()
    try {
      const data = JSON.parse(body)
      if (data && Array.isArray(data.tools)) data.tools = data.tools.map((t) => (t.name === 'ffmpeg' ? { ...t, found: false } : t))
      if (Array.isArray(data)) for (const s of data) if (s && s.category === 'media') s.missing = ['ffmpeg']
      body = JSON.stringify(data)
    } catch {}
    await route.fulfill({ response: res, body })
  })
}

async function add(page, paths) {
  await page.evaluate((p) => window.__asteriaAdd(p), paths)
  await sleep(1200)
}

async function command(page, text, { wait = true } = {}) {
  await page.keyboard.press('Meta+k')
  await page.keyboard.type(text, { delay: 40 })
  await sleep(250)
  await page.keyboard.press('Enter')
  if (wait) await idle(page)
}

async function idle(page) {
  await sleep(300)
  await page.waitForFunction(() => !document.querySelector('[aria-live] .track, .progress'), null, { timeout: 60000 })
  await sleep(500)
}

// The native runtime adds this class to the drop target while files are
// dragged over the window; a headless browser cannot start an OS drag.
async function dragOver(page, on) {
  await page.evaluate((on) => document.querySelector('[data-file-drop-target]').classList.toggle('file-drop-target-active', on), on)
  await sleep(450)
}

async function shot(page, name) {
  mkdirSync(out, { recursive: true })
  await page.screenshot({ path: join(out, `${name}.png`) })
  console.log('  ', name)
}

async function states(browser, label = '') {
  const sizes = { s: [640, 480], m: [1000, 680], l: [1440, 900] }
  const only = (process.env.SIZES ?? 'sml').split('')
  const themes = (process.env.THEMES ?? 'dark,light').split(',')
  for (const theme of themes) {
    for (const [sz, [w, h]] of Object.entries(sizes).filter(([k]) => only.includes(k))) {
      const { ctx, page } = await open(browser, { theme, w, h, scale: 1 })
      const p = `${label}${theme}-${sz}-`
      await shot(page, p + '1-empty')
      await dragOver(page, true)
      await shot(page, p + '1b-drag-over-empty')
      await dragOver(page, false)
      await add(page, files())
      await page.keyboard.press('Escape')
      await sleep(300)
      await shot(page, p + '2-files')
      await dragOver(page, true)
      await shot(page, p + '2b-drag-over-files')
      await dragOver(page, false)
      await page.keyboard.press('Meta+k')
      await page.keyboard.press('ArrowUp')
      await sleep(300)
      await shot(page, p + '3-suggestions-empty-query')
      await page.keyboard.type('conv', { delay: 30 })
      await sleep(400)
      await shot(page, p + '4-suggestions-typed')
      await page.keyboard.press('Escape')
      await page.keyboard.press('Escape')
      await command(page, 'resize 1920')
      await command(page, 'webp 80')
      await page.keyboard.press('Escape')
      await sleep(300)
      await shot(page, p + '5-applied')
      await page.keyboard.press('Meta+k')
      await page.keyboard.type('compress', { delay: 30 })
      await sleep(300)
      await page.keyboard.press('Enter')
      await sleep(400)
      await shot(page, p + '6-param')
      await page.keyboard.press('Escape')
      await page.keyboard.press('Escape')
      if (sz === 'm') {
        await command(page, 'compress video 30', { wait: false })
        await sleep(900)
        await shot(page, p + '7-progress')
        await idle(page)
        await command(page, 'extract pages 9', { wait: true })
        await sleep(200)
        await shot(page, p + '8-error')
        await page.locator('[data-id]').first().click({ button: 'right' })
        await sleep(250)
        await shot(page, p + '9-menu')
        await page.keyboard.press('Escape')
        await page.locator('[data-id]').first().click()
        await page.keyboard.press(' ')
        await sleep(900)
        await shot(page, p + '10-quicklook')
        await page.keyboard.press('o')
        await sleep(900)
        await shot(page, p + '11-quicklook-original')
        await page.keyboard.press('Escape')
        await page.keyboard.press('Meta+Enter')
        await idle(page)
        await shot(page, p + '12-exported')
        cleanExports()
      }
      await ctx.close()
      // Fresh server state between sizes.
      await restart()
    }
  }
  const { ctx, page } = await open(browser, { theme: 'dark', w: 1000, h: 680, scale: 1, noFfmpeg: true })
  await shot(page, `${label}dark-m-13-no-ffmpeg-empty`)
  await add(page, files())
  await page.keyboard.press('Meta+k')
  await page.keyboard.type('mp4', { delay: 30 })
  await sleep(400)
  await shot(page, `${label}dark-m-14-no-ffmpeg-suggestions`)
  await ctx.close()
}

async function perf(browser) {
  // A few hundred files: measure how long adding and scrolling takes.
  const many = join(media, 'many')
  if (!existsSync(join(many, 'frame 300.jpg'))) {
    mkdirSync(many, { recursive: true })
    execFileSync('ffmpeg', ['-v', 'error', '-y', '-f', 'lavfi', '-i', 'testsrc2=s=800x600:d=10:r=30', '-q:v', '4', join(many, 'frame %03d.jpg')])
  }
  const { ctx, page } = await open(browser, { theme: 'dark', w: 1000, h: 680, scale: 1 })
  const t0 = Date.now()
  await page.evaluate((p) => window.__asteriaAdd(p), [many])
  await page.waitForFunction(() => document.querySelectorAll('[data-id]').length >= 300, null, { timeout: 120000 })
  const added = Date.now() - t0
  const frames = await page.evaluate(async () => {
    const el = document.querySelector('main')
    const times = []
    let last = performance.now()
    for (let i = 0; i < 120; i++) {
      el.scrollTop += 60
      await new Promise((r) => requestAnimationFrame(r))
      const now = performance.now()
      times.push(now - last)
      last = now
    }
    times.sort((a, b) => a - b)
    return { median: times[60], p95: times[114], worst: times[119] }
  })
  await shot(page, 'perf-300-files')
  console.log(`perf: 300 files added and rendered in ${added} ms; scroll frame ms`, frames)
  await ctx.close()
}

let server
async function restart() {
  server?.kill()
  await sleep(300)
  server = await startServer()
}

async function readme(browser) {
  for (const theme of ['dark', 'light']) {
    await restart()
    const { ctx, page } = await open(browser, { theme, w: 1000, h: 660, scale: 2 })
    await add(page, files())
    await command(page, 'resize 1920')
    await command(page, 'webp 80')
    if (theme === 'dark') {
      // Typing: what will happen, before Enter.
      await page.locator('[data-id]').nth(6).click()
      await page.keyboard.press('Meta+k')
      await page.keyboard.type('trim 0:01-0:04', { delay: 30 })
    } else {
      // Idle: the likely next steps for these files, one click away.
      await page.locator('[data-id]').nth(0).click()
      await page.keyboard.press('Meta+k')
    }
    await page.waitForFunction(() => !document.querySelector('.toast'), null, { timeout: 15000 })
    await sleep(500)
    await page.screenshot({ path: join(docs, `screenshot-${theme}.png`) })
    console.log('  docs/screenshot-' + theme + '.png')
    await ctx.close()
  }
}

async function gif(browser) {
  await restart()
  const frames = join(here, '.frames')
  rmSync(frames, { recursive: true, force: true })
  mkdirSync(frames)
  const ctx = await browser.newContext({ viewport: { width: 960, height: 640 }, colorScheme: 'dark' })
  const page = await ctx.newPage()
  // Lossless frames from the compositor, emitted only when something
  // changes; a lossy video would make every frame differ and bloat the GIF.
  const shots = []
  const cdp = await ctx.newCDPSession(page)
  cdp.on('Page.screencastFrame', async ({ data, metadata, sessionId }) => {
    shots.push({ data, t: metadata.timestamp })
    await cdp.send('Page.screencastFrameAck', { sessionId }).catch(() => {})
  })
  await page.goto(url)
  await page.waitForFunction(() => typeof window.__asteriaAdd === 'function')
  // The ambient wash drifts over ~45 s, too slowly to see in a short clip,
  // but it changes every pixel of every frame; hold it still so the GIF
  // only stores what the UI actually changes.
  await page.addStyleTag({ content: '.blob { animation-play-state: paused !important; }' })
  await sleep(300)
  await cdp.send('Page.startScreencast', { format: 'png', everyNthFrame: 1 })
  await sleep(1600)
  // Drop: files arrive and the likely next steps appear under them.
  await add(page, files().slice(0, 6))
  await sleep(1400)
  // One click: Resize opens its presets; type a value and apply.
  await page.getByRole('button', { name: 'Resize', exact: true }).click()
  await sleep(900)
  await page.keyboard.type('1920', { delay: 90 })
  await sleep(400)
  await page.keyboard.press('Enter')
  await idle(page)
  await sleep(700)
  // Typing: a value inline runs it straight away.
  await page.keyboard.type('webp 80', { delay: 90 })
  await sleep(700)
  await page.keyboard.press('Enter')
  await idle(page)
  await sleep(900)
  // Select the two PDFs and merge them.
  await page.locator('[data-id]').nth(4).click()
  await page.locator('[data-id]').nth(5).click({ modifiers: ['Meta'] })
  await sleep(400)
  await page.keyboard.press('Meta+k')
  await page.keyboard.type('merge', { delay: 90 })
  await sleep(700)
  await page.keyboard.press('Enter')
  await idle(page)
  await sleep(1000)
  // Quick Look, flip between the original and the result.
  await page.locator('[data-id]').first().click()
  await page.keyboard.press(' ')
  await sleep(1300)
  await page.keyboard.press('o')
  await sleep(1200)
  await page.keyboard.press('o')
  await sleep(900)
  await page.keyboard.press('Escape')
  await sleep(600)
  await page.keyboard.press('Meta+Enter')
  await idle(page)
  await sleep(1500)
  await cdp.send('Page.stopScreencast')
  const end = shots.at(-1).t + 1.5
  let list = 'ffconcat version 1.0\n'
  shots.forEach((f, i) => {
    const name = `f${String(i).padStart(5, '0')}.png`
    writeFileSync(join(frames, name), Buffer.from(f.data, 'base64'))
    const next = shots[i + 1]?.t ?? end
    list += `file ${name}\nduration ${Math.max(0.02, next - f.t).toFixed(3)}\n`
  })
  list += `file f${String(shots.length - 1).padStart(5, '0')}.png\n`
  writeFileSync(join(frames, 'list.txt'), list)
  await ctx.close()
  const input = ['-f', 'concat', '-i', join(frames, 'list.txt')]
  const palette = join(frames, 'palette.png')
  const filters = 'fps=12,scale=760:-1:flags=lanczos,mpdecimate'
  execFileSync('ffmpeg', ['-v', 'error', '-y', ...input, '-vf', `${filters},palettegen=max_colors=160:stats_mode=full`, palette])
  execFileSync('ffmpeg', ['-v', 'error', '-y', ...input, '-i', palette, '-lavfi', `${filters}[x];[x][1:v]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle`, '-fps_mode', 'vfr', join(docs, 'demo.gif')])
  rmSync(frames, { recursive: true, force: true })
  cleanExports()
  console.log('  docs/demo.gif')
}

// Renders build/appicon.svg to build/appicon.png (Chromium handles the SVG
// blur filters); then run \`wails3 task common:generate:icons\` or the
// command in build/Taskfile.yml to refresh the .icns and .ico.
async function icon(browser) {
  const { readFileSync } = await import('node:fs')
  const svg = readFileSync(join(root, 'build/appicon.svg'), 'utf8')
  const page = await browser.newPage({ viewport: { width: 1024, height: 1024 } })
  await page.setContent(`<style>html,body{margin:0;background:transparent}</style>${svg}`)
  await page.locator('svg').screenshot({ path: join(root, 'build/appicon.png'), omitBackground: true })
  await page.close()
  console.log('  build/appicon.png')
}

if (mode === 'icon') {
  const browser = await chromium.launch()
  await icon(browser)
  await browser.close()
  process.exit(0)
}

await prepareMedia()
server = await startServer()
const browser = await chromium.launch()
try {
  if (mode === 'states' || mode === 'all') await states(browser, process.argv[3] ?? '')
  if (mode === 'perf' || mode === 'all') await perf(browser)
  if (mode === 'readme' || mode === 'all') await readme(browser)
  if (mode === 'gif' || mode === 'all') await gif(browser)
} finally {
  await browser.close()
  server.kill()
  cleanExports()
}
