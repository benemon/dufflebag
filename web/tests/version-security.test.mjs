import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let VersionSecurityCard

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ VersionSecurityCard } = await vite.ssrLoadModule('/src/components/VersionSecurity.tsx'))
})

after(async () => {
  await vite?.close()
})

const render = (props) =>
  renderToStaticMarkup(
    React.createElement(VersionSecurityCard, { onOpenBuild: () => {}, outOfScanSet: false, ...props }),
  )

const scan = { adapter: 'osv', observedAt: '2026-08-07T12:00:00Z', databaseRevision: 'unreported', submitted: 205 }

const withLibcurl = (buildID, platform) => ({
  buildID, platform, component: `${platform}.distro`, scanned: 205, scan,
  packages: [{
    name: 'libcurl', version: '7.61.1', purl: 'pkg:rpm/libcurl@7.61.1',
    findings: [{ identifier: 'CVE-2023-38545', criticality: 'critical' }],
  }],
})

test('the headline counts a shared finding once and the builds say where it lives', () => {
  const html = render({
    builds: [withLibcurl('b1', 'docker'), withLibcurl('b2', 'aws'), withLibcurl('b3', 'azure')],
  })
  assert.match(html, /1 finding across 1 package/, 'three builds, one problem')
  assert.match(html, /Last scanned: 2026-08-07/, 'a plain date, not a timestamp')
  assert.doesNotMatch(html, /21:23|\.\d\d\dZ/, 'the full timestamp is noise')
  assert.doesNotMatch(html, /database revision/, 'adapter detail is a root-level concern')
  assert.match(html, /docker/)
  assert.match(html, /aws/)
  assert.match(html, /azure/)
})

test('every build tile links to that build', () => {
  const html = render({ builds: [withLibcurl('b1', 'docker'), withLibcurl('b2', 'aws')] })
  assert.match(html, /data-build-link="b1"/)
  assert.match(html, /data-build-link="b2"/)
})

// The mockup rendered "clean" for a build with nothing found. The ruling says
// the word never appears, so it must not be copied in.
test('neither "clean" nor "stale" renders in any state', () => {
  const states = [
    { builds: [withLibcurl('b1', 'docker')] },
    { builds: [{ buildID: 'b1', platform: 'docker', component: 'docker.distro', scanned: 205, scan, packages: [] }] },
    { builds: [withLibcurl('b1', 'docker')], outOfScanSet: true },
  ]
  for (const props of states) {
    const html = render(props).toLowerCase()
    assert.doesNotMatch(html, /\bclean\b/)
    assert.doesNotMatch(html, /\bstale\b/)
  }
})

test('a build with nothing found reports what it scanned', () => {
  const html = render({
    builds: [{ buildID: 'b1', platform: 'docker', component: 'docker.distro', scanned: 205, scan, packages: [] }],
  })
  assert.match(html, /no findings/)
  assert.match(html, /205 scanned/)
  assert.match(html, /Last scanned: 2026-08-07/, 'the zero state still says when')
})

test('an unscanned version says so rather than reporting zero', () => {
  const html = render({
    builds: [{ buildID: 'b1', platform: 'docker', component: 'docker.distro', scanned: 0, packages: [] }],
  })
  assert.match(html, /data-state="never-scanned"/)
  assert.match(html, /Not scanned/)
  assert.doesNotMatch(html, /No known findings/)
})

// The bug this card was moved to fix: a version carrying a channel was
// reported as no longer maintained because the Build screen's projection did
// not populate channels.
test('a version selected by a channel is not marked unmaintained', () => {
  const html = render({ builds: [withLibcurl('b1', 'docker')], outOfScanSet: false })
  assert.doesNotMatch(html, /not being updated/)
  assert.doesNotMatch(html, /dfbg-findings-unmaintained/)
})

test('a version no channel selects keeps its figures under the unmaintained class', () => {
  const html = render({ builds: [withLibcurl('b1', 'docker')], outOfScanSet: true })
  assert.match(html, /dfbg-findings-unmaintained/)
  assert.match(html, /critical/, 'the prior figures are retained')
  assert.match(html, /not being updated/)
})

// The identifier means nothing to a reader; the build's own name does.
test('a build tile shows the build name, not its identifier', () => {
  const html = render({ builds: [withLibcurl('01KZF1QRA3BQ9K913VA8DP23RN', 'docker')] })
  assert.match(html, /docker\.distro/)
  assert.doesNotMatch(html, />01KZF1QRA3BQ9K913VA8DP23RN</, 'the ULID is not the label')
})

// Coverage counts are the difference between "nothing found" and "not looked
// at", so they appear when something was not examined — and only then.
test('coverage appears only when something was not examined', () => {
  const full = render({ builds: [withLibcurl('b1', 'docker')] })
  assert.doesNotMatch(full, /Coverage:/, 'full coverage needs no line')
  const gap = render({
    builds: [{ ...withLibcurl('b1', 'docker'), scan: { ...scan, unsupported: 12 } }],
  })
  assert.match(gap, /Coverage:/)
  assert.match(gap, /does not cover/)
})
