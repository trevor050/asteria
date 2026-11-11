import type { APIRoute } from 'astro'
import { pagePairs } from '../lib/data'

export const GET: APIRoute = ({ site }) => {
  const base = site!.href.replace(/\/$/, '')
  const urls = ['/', '/convert', ...pagePairs.map((p) => `/convert/${p.slug}`)]
  const body = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
${urls.map((u) => `  <url><loc>${base}${u === '/' ? '/' : u}</loc></url>`).join('\n')}
</urlset>
`
  return new Response(body, { headers: { 'Content-Type': 'application/xml' } })
}
