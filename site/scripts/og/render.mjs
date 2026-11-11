// Renders card.html to public/og.png. Needs Playwright with Chromium:
//   node scripts/og/render.mjs   (set PLAYWRIGHT_MODULE to a playwright install if it is not resolvable)
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
const here = path.dirname(fileURLToPath(import.meta.url))
const mod = process.env.PLAYWRIGHT_MODULE ?? 'playwright'
const { chromium } = await import(mod)
const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 1200, height: 630 } })
await page.goto(pathToFileURL(path.join(here, 'card.html')).href)
await page.evaluate(() => document.fonts.ready)
await page.screenshot({ path: path.join(here, '../../public/og.png') })
await browser.close()
console.log('wrote public/og.png')
