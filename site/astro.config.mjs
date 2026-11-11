// @ts-check
import { defineConfig } from 'astro/config'

// Canonical URLs, the sitemap and the social card are built for the final
// domain; the Vercel preview URL serves the same files.
export default defineConfig({
  site: 'https://asteria.trevorrosato.com',
  trailingSlash: 'never',
  build: {
    format: 'file',
    inlineStylesheets: 'auto',
  },
  devToolbar: { enabled: false },
  vite: {
    server: { fs: { allow: ['..'] } },
  },
})
