#!/usr/bin/env node
// Checks the add-on manifest (addons/manifest.json) against upstream.
//
//   node scripts/addons/check.mjs                 every pinned URL still resolves over HTTPS,
//                                                 has the pinned size, and the project's own
//                                                 published checksum still equals the pin
//   node scripts/addons/check.mjs --deep [id...]  also downloads each file and hashes it
//                                                 (large: LibreOffice and Calibre are ~300 MB each)
//   node scripts/addons/check.mjs --pin <id> <platform> <url> --version <v>
//                                                 points an entry at a new upstream build: reads the
//                                                 size and the published SHA-256 and rewrites the entry
//
// Published checksums come from where each project publishes them: the
// GitHub release asset digest, a "<file>.sha256" file next to the download
// (The Document Foundation, martin-riedl.de), or a SHA-1 list (Potrace).
// Set GITHUB_TOKEN to avoid GitHub's anonymous rate limit.

import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const manifestPath = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'addons', 'manifest.json');
const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
const args = process.argv.slice(2);
const deep = args.includes('--deep');

const ghHeaders = { 'User-Agent': 'asteria-addons-check', Accept: 'application/vnd.github+json' };
if (process.env.GITHUB_TOKEN) ghHeaders.Authorization = `Bearer ${process.env.GITHUB_TOKEN}`;

const releaseCache = new Map();
async function githubDigest(url) {
  const m = url.match(/^https:\/\/github\.com\/([^/]+)\/([^/]+)\/releases\/download\/([^/]+)\/([^/]+)$/);
  if (!m) return null;
  const [, owner, repo, tag, name] = m;
  const key = `${owner}/${repo}@${tag}`;
  if (!releaseCache.has(key)) {
    const r = await fetch(`https://api.github.com/repos/${owner}/${repo}/releases/tags/${encodeURIComponent(tag)}`, { headers: ghHeaders });
    if (!r.ok) throw new Error(`GitHub API ${r.status} for ${key}`);
    releaseCache.set(key, await r.json());
  }
  const asset = releaseCache.get(key).assets.find((a) => a.name === decodeURIComponent(name));
  if (!asset) throw new Error(`asset ${name} not in release ${key}`);
  if (!asset.digest?.startsWith('sha256:')) throw new Error(`release ${key} has no digest for ${name}`);
  return { sha256: asset.digest.slice(7), size: asset.size, source: 'GitHub release asset digest' };
}

async function sidecarDigest(url) {
  const r = await fetch(url + '.sha256', { headers: { 'User-Agent': 'asteria-addons-check' } });
  if (!r.ok) return null;
  const m = (await r.text()).match(/\b[0-9a-f]{64}\b/i);
  return m ? { sha256: m[0].toLowerCase(), source: `${url.split('/').pop()}.sha256` } : null;
}

async function published(a) {
  return (await githubDigest(a.url)) ?? (await sidecarDigest(a.url));
}

async function head(url) {
  const r = await fetch(url, { method: 'HEAD', redirect: 'follow', headers: { 'User-Agent': 'asteria-addons-check' } });
  if (!r.url.startsWith('https://')) throw new Error(`redirected to non-HTTPS ${r.url}`);
  return { ok: r.ok, status: r.status, size: Number(r.headers.get('content-length') || 0) };
}

async function hashURL(url) {
  const r = await fetch(url, { redirect: 'follow', headers: { 'User-Agent': 'asteria-addons-check' } });
  if (!r.ok) throw new Error(`GET ${r.status}`);
  const s256 = createHash('sha256');
  const s1 = createHash('sha1');
  let n = 0;
  for await (const chunk of r.body) {
    s256.update(chunk);
    s1.update(chunk);
    n += chunk.length;
  }
  return { sha256: s256.digest('hex'), sha1: s1.digest('hex'), size: n };
}

async function check() {
  const only = args.filter((a) => !a.startsWith('--'));
  let failed = 0;
  const done = new Map(); // url -> result line, for builds shared by two platforms
  for (const e of manifest.engines) {
    if (only.length && !only.includes(e.id)) continue;
    for (const [plat, a] of Object.entries(e.platforms)) {
      const label = `${e.id} ${plat}`.padEnd(26);
      if (a.manual) {
        const r = await head(a.manual.page).catch((err) => ({ ok: false, status: err.message }));
        console.log(`${label} manual  ${r.ok ? 'page ok' : `PAGE ${r.status}`}  ${a.manual.page}`);
        if (!r.ok) failed++;
        continue;
      }
      if (done.has(a.url)) {
        console.log(`${label} ${done.get(a.url)}`);
        continue;
      }
      const problems = [];
      try {
        const h = await head(a.url);
        if (!h.ok) problems.push(`HTTP ${h.status}`);
        else if (h.size && h.size !== a.size) problems.push(`size ${h.size} != pinned ${a.size}`);
        if (a.sha1_published) {
          // Potrace publishes SHA-1 only; the list's URL is in checksum_source.
          const page = (a.checksum_source || '').match(/https:\/\/\S+?(?=[)\s,;]|$)/)?.[0];
          const text = page ? await (await fetch(page)).text() : '';
          if (!text.includes(a.sha1_published)) problems.push('published SHA-1 not found on its page');
        } else {
          const p = await published(a);
          if (!p) problems.push('no published checksum found');
          else if (p.sha256 !== a.sha256) problems.push(`published sha256 ${p.sha256} != pinned`);
        }
        if (deep && !problems.length) {
          const got = await hashURL(a.url);
          if (got.sha256 !== a.sha256) problems.push(`downloaded sha256 ${got.sha256} != pinned`);
          if (a.sha1_published && got.sha1 !== a.sha1_published) problems.push('downloaded SHA-1 != published');
          if (got.size !== a.size) problems.push(`downloaded ${got.size} bytes != pinned ${a.size}`);
        }
      } catch (err) {
        problems.push(err.message);
      }
      const line = problems.length ? `FAIL    ${problems.join('; ')}` : `ok      ${a.version}${deep ? ' (downloaded and hashed)' : ''}`;
      done.set(a.url, line);
      console.log(`${label} ${line}`);
      if (problems.length) failed++;
    }
  }
  if (failed) {
    console.error(`\n${failed} entr${failed === 1 ? 'y' : 'ies'} need attention.`);
    process.exit(1);
  }
}

async function pin() {
  const i = args.indexOf('--pin');
  const [id, plat, url] = args.slice(i + 1, i + 4);
  const version = args[args.indexOf('--version') + 1];
  if (!id || !plat || !url?.startsWith('https://') || args.indexOf('--version') < 0) {
    console.error('usage: --pin <id> <platform> <https-url> --version <version>');
    process.exit(2);
  }
  const e = manifest.engines.find((x) => x.id === id);
  if (!e) throw new Error(`no engine ${id}`);
  const p = await published({ url });
  if (!p) throw new Error(`${url}: no published SHA-256 (GitHub digest or .sha256 file); pin it by hand after checking the project's own checksum`);
  const h = await head(url);
  const size = p.size || h.size;
  const prev = e.platforms[plat] ?? {};
  e.platforms[plat] = { ...prev, version, url, size, sha256: p.sha256, checksum_source: p.source };
  delete e.platforms[plat].manual;
  writeFileSync(manifestPath, JSON.stringify(manifest, null, 2) + '\n');
  console.log(`${id} ${plat}: ${version} ${size} bytes sha256 ${p.sha256} (${p.source})`);
  console.log('Check bins, format and unpacked_mb by hand, then run the Go add-on tests.');
}

(args.includes('--pin') ? pin() : check()).catch((err) => {
  console.error(err.message);
  process.exit(1);
});
