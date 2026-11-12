// Plain-text summaries of the site for language models (/llms.txt and
// /llms-full.txt, https://llmstxt.org). Every fact comes from the repository:
// docs/formats.json, addons/manifest.json, the README and this site's copy.
import { summary, addons, allPairs, familyOrder, verifiedPlatformsText, type Pair } from './data'
import { REPO_URL, RELEASES_URL, getRelease } from './release'
import { AUTHOR, SITE } from './seo'

const list = (xs: readonly string[]) => (xs.length > 1 ? `${xs.slice(0, -1).join(', ')} and ${xs.at(-1)}` : xs[0])

async function intro(): Promise<string> {
  const r = await getRelease()
  const released = r.published
    ? new Date(r.published).toLocaleDateString('en-US', { year: 'numeric', month: 'long', day: 'numeric', timeZone: 'UTC' })
    : null
  const version = !r.fallback && r.version ? `- Latest release: ${r.version}${released ? `, published ${released}` : ''} (${r.url})\n` : ''
  return `# Asteria

> Asteria is a free, open-source desktop file converter for Mac, Windows and Linux. Drop files in, type what you want done to them (for example "heic to jpg", "resize 50%", "merge pdfs" or "compress video"), preview the result and save it. It works on your own computer, works offline, and never uploads your files.

Asteria is made by [${AUTHOR.name}](${AUTHOR.url}).

## Key facts

- Price: free. No account, no ads, no trial and no limit on how many files you convert.
- License: MIT. Source code: ${REPO_URL}
- Systems: macOS 12 or newer (Apple silicon and Intel); Windows 10 or 11, 64-bit; Ubuntu 24.04, Debian 13 or newer, 64-bit.
${version}- Formats: opens ${summary.fileTypesRead} kinds of files, ${summary.fileTypesReadBuiltIn} of them with nothing extra installed. ${summary.conversionPairs} conversions between file types, each run on a real sample file with the result checked by a separate tool, on ${verifiedPlatformsText()}.
- Privacy: files are never sent anywhere. No accounts, analytics or telemetry. The only time Asteria goes online is to download a free add-on the user said yes to, from that add-on's official download host. A test in the repository checks that converting a file never opens a network connection.
- Originals are never changed: results are saved as new files, next to the originals or in a folder the user picks, and never overwrite an existing file.
- Beyond conversions: more than 100 picture skills (crop, straighten, levels, duotone, dither, vignette, watermark, collage, favicon and app-icon sets), video edits (trim, speed, crop, rotate, mute, loudness, subtitles, contact sheets), PDF tools (merge, split, extract pages, compress), and helpers for any file (zip, compress, checksum, identify the real file type).
- Add-ons: audio and video, office documents, ebooks and a few other formats use free, separate programs (${list(addons.map((a) => a.name))}). Asteria offers to download one the first time it is needed, shows its size first, checks the download against a pinned checksum, and needs no administrator password. Programs already on the computer are used as they are.
- Custom skills: skills are JSON files; users can add their own.
- The builds are not code-signed yet, so macOS and Windows show a warning the first time. On a Mac, use Open Anyway in System Settings, Privacy & Security. On Windows, click More info, then Run anyway.

## Get it

- [Download Asteria](${SITE}/#download): installers for Mac (.dmg), Windows (installer or portable .exe) and Linux (.deb, AppImage, .tar.gz), with first-launch help for each system.
- [Releases on GitHub](${RELEASES_URL})

## Pages

- [Home](${SITE}/): what Asteria does and how it works
- [Every conversion](${SITE}/convert): all ${summary.conversionPairs} tested conversions, with what each one needs
- [Source code on GitHub](${REPO_URL}): README, build instructions and the skill format
- [License (MIT)](${REPO_URL}/blob/master/LICENSE)
`
}

export async function llmsTxt(): Promise<string> {
  return `${await intro()}
## Optional

- [Full text for language models](${SITE}/llms-full.txt): everything above, plus every add-on and every tested conversion with its page
`
}

const needs = (p: Pair) => (p.addon ? `needs the ${p.addon.name} add-on` : 'built in')

export async function llmsFullTxt(): Promise<string> {
  const families = [...new Set([...familyOrder, ...allPairs.map((p) => p.family)])]
  const sections = families
    .map((family) => {
      const pairs = allPairs.filter((p) => p.family === family).sort((a, b) => a.slug.localeCompare(b.slug))
      if (!pairs.length) return ''
      const lines = pairs.map((p) => {
        const name = `${p.from.toUpperCase()} to ${p.to === 'folder' ? 'a folder of files' : p.to.toUpperCase()}`
        const label = p.to === 'folder' ? name : `[${name}](${SITE}/convert/${p.slug})`
        return `- ${label}: ${p.fromName} to ${p.to === 'folder' ? 'a folder of files' : p.toName}, ${needs(p)}`
      })
      return `### ${family}\n\n${lines.join('\n')}\n`
    })
    .filter(Boolean)
    .join('\n')

  const addonLines = addons
    .map((a) => {
      const manual = a.manualOn.length ? ` On ${list(a.manualOn)}, Asteria opens the official download page instead of installing it.` : ''
      return `- ${a.name} (${a.homepage}), from ${a.publisher}, license ${a.license}: ${a.unlocks}.${manual}`
    })
    .join('\n')

  return `${await intro()}
## How it works

- Everything is a skill. Converting, resizing, exporting and choosing the output folder all happen in one command bar. A value after the name is used directly: "resize 50%", "jpg 80", "trim 0:05-0:30".
- After files are dropped in, the skills that fit those exact files are shown above the bar. Skills that need an add-on are marked and ask before downloading anything.
- Skills with a number (saturation, exposure, blur, vignette and others) get a slider, and the picture previews the change live. Nothing is applied until Enter is pressed.
- Steps stack and can be undone. Removing a step from the middle replays the rest.
- Save with Cmd+Enter on a Mac, Ctrl+Enter on Windows and Linux.

## Add-ons

Asteria does not bundle these programs. Each one is downloaded unmodified from its project's official release channel, only when the user asks, and can be removed again in Settings, Add-ons.

${addonLines}

## Every tested conversion

Each conversion below was run on a real sample file, and the output was opened and checked by a separate tool. "Built in" means it works with nothing extra installed.

${sections}`
}
