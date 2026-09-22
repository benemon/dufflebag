import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let VersionSecurityCard
let projectVersionSecuritySummary
const themeSource = readFileSync(new URL('../src/theme.css', import.meta.url), 'utf8')

// generatedClient and absentScans transcribe the responses constructed by
// internal/platform/v1/findings_summary_test.go:71 and :32. mixedBuilds and
// scannerNotConfigured are spec-derived from spec/platform/openapi.yaml:1590-1664;
// they add summary presence, inventory, counts, timestamps, attribution and coverage cases.
const fixtures = JSON.parse(readFileSync(
  new URL('./fixtures/findings-summary.json', import.meta.url), 'utf8',
))

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ VersionSecurityCard } = await vite.ssrLoadModule('/src/components/VersionSecurity.tsx'))
  ;({ projectVersionSecuritySummary } = await vite.ssrLoadModule('/src/data/versions.ts'))
})

after(async () => {
  await vite?.close()
})

const project = (wire) => projectVersionSecuritySummary(structuredClone(wire))
const render = (wire, props = {}) => renderToStaticMarkup(
  React.createElement(VersionSecurityCard, {
    summary: project(wire), onOpenBuild: () => {}, outOfScanSet: false, ...props,
  }),
)

test('the headline uses the version summary so a shared finding is counted once', () => {
  const html = render(fixtures.mixedBuilds)
  assert.match(html, /1 finding across 1 package/, 'two build summaries, one version problem')
  assert.doesNotMatch(html, /2 findings across/)
  assert.match(html, /Last scanned:/)
  assert.match(html, /<time[^>]*dateTime="2026-09-22T10:00:00Z"/)
  assert.doesNotMatch(html, /database revision/, 'adapter detail is a root-level concern')
})

test('every build tile links to that build', () => {
  const html = render(fixtures.mixedBuilds)
  for (const build of fixtures.mixedBuilds.builds) {
    assert.match(html, new RegExp(`data-build-link="${build.build_id}"`))
  }
})

test('by-build rows use clickable DataList items and AngleRightIcon', () => {
  const html = render(fixtures.generatedClient)
  assert.match(html, /pf-v6-c-data-list/)
  assert.match(html, /pf-v6-c-data-list__item pf-m-clickable[^>]*data-build-link="build-a"/)
  assert.match(html, /<svg[^>]*aria-hidden="true"/)
  assert.doesNotMatch(html, /›/)
})

test('neither "clean" nor "stale" renders in any state', () => {
  for (const [wire, outOfScanSet] of [
    [fixtures.generatedClient, false],
    [fixtures.absentScans, false],
    [fixtures.mixedBuilds, true],
  ]) {
    const html = render(wire, { outOfScanSet }).toLowerCase()
    assert.doesNotMatch(html, /\bclean\b/)
    assert.doesNotMatch(html, /\bstale\b/)
  }
})

test('a build with nothing found reports what it scanned', () => {
  const html = render(fixtures.mixedBuilds)
  assert.match(html, /no findings/)
  assert.match(html, /12 scanned/)
})

test('an unparseable build is labelled', () => {
  const html = render(fixtures.mixedBuilds)
  assert.match(html, /azure-arm\.broken/)
  assert.match(html, /SBOM unparseable/)
})

test('the headline states how many builds it covers when some are not yet scanned', () => {
  const partial = render(fixtures.mixedBuilds)
  assert.match(partial, /Covers 3 of 5 builds; the rest are not yet scanned\./)
  const complete = render(fixtures.generatedClient)
  assert.doesNotMatch(complete, /Covers \d+ of \d+ builds/)
})

test('an unscanned build is labelled "not scanned"', () => {
  const html = render(fixtures.mixedBuilds)
  assert.match(html, /googlecompute\.pending/)
  assert.match(html, /not scanned/)
  assert.doesNotMatch(html, /0 scanned/)
})

test('scanner absence and a pending first scan have distinct empty states', () => {
  const absent = render(fixtures.scannerNotConfigured)
  assert.match(absent, /data-state="never-scanned"/)
  assert.match(absent, /Not scanned\. No vulnerability source is configured for this deployment\./)

  const pending = render(fixtures.absentScans)
  assert.match(pending, /data-state="not-yet-scanned"/)
  assert.match(
    pending,
    /Not yet scanned\. Findings appear once the scanner has examined a build of this version\./,
  )
  assert.doesNotMatch(pending, /No vulnerability source/)
})

test('a version selected by a channel is not marked unmaintained', () => {
  const html = render(fixtures.generatedClient)
  assert.doesNotMatch(html, /not being updated/)
  assert.doesNotMatch(html, /dfbg-findings-unmaintained/)
})

test('a version no channel selects keeps its figures under the unmaintained class', () => {
  const html = render(fixtures.generatedClient, { outOfScanSet: true })
  assert.match(html, /dfbg-findings-unmaintained/)
  assert.match(html, /critical/, 'the prior figures are retained')
  assert.match(html, /pf-m-outline[\s\S]{0,500}>not updated</)
  assert.match(html, /not being updated/)
  const rule = themeSource.match(/\.dfbg-findings-unmaintained\s*\{[^}]*\}/)?.[0] ?? ''
  assert.doesNotMatch(rule, /filter\s*:/)
  assert.doesNotMatch(rule, /opacity\s*:/)
  assert.match(rule, /border-left:\s*2px solid/)
})

test('a build tile shows the build name, not its identifier', () => {
  const wire = structuredClone(fixtures.generatedClient)
  wire.builds[0].build_id = '01KZF1QRA3BQ9K913VA8DP23RN'
  wire.builds[0].component = 'docker.distro'
  const html = render(wire)
  assert.match(html, /docker\.distro/)
  assert.doesNotMatch(html, />01KZF1QRA3BQ9K913VA8DP23RN</, 'the ULID is not the label')
})

test('a build tile keeps its full component in PatternFly truncation', () => {
  const wire = structuredClone(fixtures.generatedClient)
  const component = 'docker.ubuntu-a-provider-component-that-does-not-fit'
  wire.builds[0].component = component
  const html = render(wire)
  assert.match(html, /pf-v6-c-truncate/)
  assert.match(html, new RegExp(component))
  assert.doesNotMatch(html, /title=/)
})

test('coverage appears only when something was not examined', () => {
  const full = render(fixtures.generatedClient)
  assert.doesNotMatch(full, /Coverage:/, 'full coverage needs no line')
  const gap = structuredClone(fixtures.generatedClient)
  gap.builds[0].summary.coverage.unsupported = 12
  const html = render(gap)
  assert.match(html, /Coverage:/)
  assert.match(html, /does not cover/)
})
