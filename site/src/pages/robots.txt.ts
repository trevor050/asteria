import type { APIRoute } from 'astro'

// Search engines and AI assistants are welcome to read and cite every page.
// The main crawlers are named so that each one has an explicit rule.
const crawlers = [
  'GPTBot',
  'OAI-SearchBot',
  'ChatGPT-User',
  'ClaudeBot',
  'Claude-SearchBot',
  'Claude-User',
  'PerplexityBot',
  'Perplexity-User',
  'Google-Extended',
  'Applebot-Extended',
  'CCBot',
  'Bingbot',
  'Amazonbot',
  'meta-externalagent',
  'DuckAssistBot',
  'MistralAI-User',
  'cohere-ai',
  'Bytespider',
  'YouBot',
  'Diffbot',
]

export const GET: APIRoute = ({ site }) => {
  const groups = ['*', ...crawlers].map((ua) => `User-agent: ${ua}\nAllow: /`).join('\n\n')
  return new Response(`${groups}\n\nSitemap: ${new URL('/sitemap.xml', site).href}\n`, {
    headers: { 'Content-Type': 'text/plain; charset=utf-8' },
  })
}
