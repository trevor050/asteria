// Structured data shared by every page. The author is the same person that
// trevorrosato.com describes, so his node keeps that site's @id.
import { summary } from './data'
import { REPO_URL, type Release } from './release'

export const SITE = 'https://asteria.trevorrosato.com'
export const AUTHOR = { name: 'Trevor Rosato', url: 'https://trevorrosato.com/' } as const
export const PERSON_ID = 'https://trevorrosato.com/#person'
export const WEBSITE_ID = `${SITE}/#website`
export const APP_ID = `${SITE}/#app`

export const appDescription = `Asteria is a free file converter for Mac, Windows and Linux. Drop a file, type what you want, get the result. ${summary.conversionPairs} tested conversions, and nothing is ever uploaded.`

interface GraphInput {
  canonical: string
  title: string
  description: string
  release: Release
  /** absolute URL of a real screenshot of the app */
  screenshot: string
  /** extra nodes for this page, such as a BreadcrumbList */
  nodes?: Record<string, unknown>[]
  noindex?: boolean
}

export function buildGraph({ canonical, title, description, release, screenshot, nodes = [], noindex = false }: GraphInput) {
  const person = { '@type': 'Person', '@id': PERSON_ID, name: AUTHOR.name, url: AUTHOR.url }
  const website = {
    '@type': 'WebSite',
    '@id': WEBSITE_ID,
    url: `${SITE}/`,
    name: 'Asteria',
    description: appDescription,
    inLanguage: 'en',
    publisher: { '@id': PERSON_ID },
    about: { '@id': APP_ID },
  }
  const app: Record<string, unknown> = {
    '@type': 'SoftwareApplication',
    '@id': APP_ID,
    name: 'Asteria',
    url: `${SITE}/`,
    description: appDescription,
    applicationCategory: 'UtilitiesApplication',
    operatingSystem: 'macOS, Windows, Linux',
    offers: { '@type': 'Offer', price: '0', priceCurrency: 'USD' },
    isAccessibleForFree: true,
    license: 'https://opensource.org/licenses/MIT',
    downloadUrl: release.url,
    ...(release.version && !release.fallback ? { softwareVersion: release.version } : {}),
    screenshot,
    image: `${SITE}/og.png`,
    sameAs: [REPO_URL],
    author: { '@id': PERSON_ID },
    creator: { '@id': PERSON_ID },
  }
  const graph: Record<string, unknown>[] = [website, app, person]
  if (!noindex) {
    const breadcrumb = nodes.find((n) => n['@type'] === 'BreadcrumbList')
    graph.push({
      '@type': 'WebPage',
      '@id': `${canonical}#webpage`,
      url: canonical,
      name: title,
      description,
      inLanguage: 'en',
      isPartOf: { '@id': WEBSITE_ID },
      about: { '@id': APP_ID },
      author: { '@id': PERSON_ID },
      ...(breadcrumb?.['@id'] ? { breadcrumb: { '@id': breadcrumb['@id'] } } : {}),
    })
  }
  graph.push(...nodes)
  return { '@context': 'https://schema.org', '@graph': graph }
}
