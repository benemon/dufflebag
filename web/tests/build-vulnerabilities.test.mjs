import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router'
import { createServer } from 'vite'

let vite
let BuildView, PackagesCard, VulnerabilitiesCard, BuildTable, versionBuildFacetPath
let deriveBuildAdvisories, loadVersionFindings

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ BuildView, PackagesCard, VulnerabilitiesCard } =
    await vite.ssrLoadModule('/src/screens/Build.tsx'))
  ;({ BuildTable, versionBuildFacetPath } = await vite.ssrLoadModule('/src/screens/Version.tsx'))
  ;({ deriveBuildAdvisories } = await vite.ssrLoadModule('/src/data/advisories.ts'))
  ;({ loadVersionFindings } = await vite.ssrLoadModule('/src/data/versions.ts'))
})

after(async () => {
  await vite?.close()
})

const finding = (identifier, criticality, over = {}) => ({
  identifier,
  description: 'recorded advisory',
  criticality,
  severity: '',
  fixedVersion: '4.1.4',
  aliases: [`ALIAS-${identifier}`],
  firstSeen: '2026-08-01T00:00:00Z',
  published: '2026-04-03T03:28:56Z',
  ...over,
})

// These package/finding shapes are projected from the recorded OSV bodies at
// internal/scan/testdata/osv/detail-{GHSA-78h2-9frx-2jm8,GO-2026-4945}.json,
// which are also the bodies served by the browser smoke's OSV stub.
const pkg = (name, findings = [], over = {}) => ({
  name,
  version: 'v4.1.1',
  purl: `pkg:golang/${name}@v4.1.1`,
  sboms: [{ id: `sbom-${name}`, name: 'scanner-state', format: 'CYCLONEDX' }],
  ...(findings.length ? { findings } : {}),
  ...over,
})

const build = (packageInventory) => ({
  id: 'b1', component: 'docker.test', platform: 'docker', state: 'done',
  packerRunUUID: '', sourceExternalIdentifier: '', labels: {}, artifacts: [],
  packerVersion: '', plugins: [], runnerOS: '', arch: '',
  options: {
    path: '', variables: [], variableFiles: [], only: [], except: [], debug: false, force: false,
  },
  updated: '2026-08-07T12:00:00Z', packageInventory,
})

const observedAt = '2026-08-07T12:00:00Z'
const parsedBuild = (packages) => build({
  status: 'parsed', packages,
  scan: { adapter: 'osv', observedAt, submitted: packages.length },
})

const renderVulnerabilities = (over = {}) => renderToStaticMarkup(React.createElement(
  VulnerabilitiesCard,
  {
    build: parsedBuild([pkg('go-jose', [finding('GHSA-78h2-9frx-2jm8', 'high')])]),
    scannerConfigured: true,
    scanned: true,
    buildPath: '/buckets/images/versions/fp/builds/b1/vulnerabilities',
    ...over,
  },
))

test('two packages sharing an advisory derive one row with two affected packages', () => {
  const packages = [
    pkg('go-jose', [finding('GHSA-78h2-9frx-2jm8', 'high')]),
    pkg('go-jose-fork', [finding('GHSA-78h2-9frx-2jm8', 'high')]),
  ]
  const data = deriveBuildAdvisories(packages)
  assert.equal(data.advisories.length, 1)
  const html = renderVulnerabilities({ build: parsedBuild(packages) })
  assert.equal((html.match(/GHSA-78h2-9frx-2jm8/g) ?? []).length >= 1, true)
  assert.match(html, />2 packages</)
})

test('severity strip counts remain build totals under an active severity filter', () => {
  const packages = [
    pkg('critical-package', [finding('CRITICAL-1', 'critical')]),
    pkg('high-package', [finding('HIGH-1', 'high')]),
  ]
  const html = renderVulnerabilities({
    build: parsedBuild(packages), severityFilter: ['high'],
  })
  const criticalItem = html.match(/<button[^>]*id="severity-critical"[\s\S]*?<\/button>/)?.[0] ?? ''
  assert.match(criticalItem, /critical/)
  assert.match(criticalItem, />1<\/span>/)
  assert.match(html, /aria-pressed="true" id="severity-high"/)
})

test('zero-count severity items are disabled', () => {
  const html = renderVulnerabilities()
  assert.match(html, /disabled="" id="severity-critical"/)
  assert.doesNotMatch(html, /disabled="" id="severity-high"/)
})

test('top vulnerable packages sort critical before high and break ties by name', () => {
  const packages = [
    pkg('zeta', [finding('Z-C', 'critical')]),
    pkg('alpha', [finding('A-C', 'critical'), finding('A-H', 'high')]),
    pkg('beta', [finding('B-C1', 'critical'), finding('B-C2', 'critical')]),
    pkg('gamma', [finding('G-H', 'high')]),
    pkg('delta', [finding('D-H', 'high')]),
    pkg('epsilon', [finding('E-H', 'high')]),
  ]
  assert.deepEqual(
    deriveBuildAdvisories(packages).topVulnerablePackages.map((item) => item.name),
    ['beta', 'alpha', 'zeta', 'delta', 'epsilon'],
  )
})

test('both inventory cards render the scan observedAt as their as-of instant', () => {
  const inventoryBuild = parsedBuild([pkg('go-jose')])
  const vulnerabilities = renderVulnerabilities({ build: inventoryBuild })
  const packages = renderToStaticMarkup(React.createElement(PackagesCard, {
    build: inventoryBuild, scanned: true,
  }))
  for (const html of [vulnerabilities, packages]) {
    assert.match(html, /As of/)
    assert.match(html, new RegExp(`dateTime="${observedAt}"`, 'i'))
    assert.match(html, /UTC/)
  }
})

test('Vulnerabilities renders every fixed inventory and scan state distinctly', () => {
  const base = { scannerConfigured: true, scanned: true }
  const loading = renderVulnerabilities({ ...base, inventoryLoading: true, inventoryProgress: { packages: 17 } })
  assert.match(loading, /data-state="loading"/)
  assert.match(loading, /Reading package inventory… 17 packages read so far\. Large images can take a minute or more\./)

  const failed = renderVulnerabilities({ ...base, inventoryFailure: 'reader failed' })
  assert.match(failed, /data-state="failed"/)
  assert.match(failed, /Package inventory could not be loaded[\s\S]*reader failed/)

  const unparseable = renderVulnerabilities({ ...base, build: build({ status: 'unparseable' }) })
  assert.match(unparseable, /data-state="unparseable"[\s\S]*?>SBOM unparseable/)

  const noScanner = renderVulnerabilities({ scannerConfigured: false, scanned: false })
  assert.match(noScanner, /data-state="never-scanned"/)
  assert.match(noScanner, /Not scanned\. No vulnerability source is configured for this deployment\./)

  const pending = renderVulnerabilities({ scannerConfigured: true, scanned: false })
  assert.match(pending, /data-state="not-yet-scanned"/)
  assert.match(pending, /Not yet scanned\. Findings appear once the scanner has examined this build\./)

  const zero = renderVulnerabilities({ build: parsedBuild([]) })
  assert.match(zero, /data-state="zero-findings"/)
  assert.match(zero, /No findings in 0 packages/)
  assert.match(zero, /0 packages · 0 advisories\./)
})

test('the Vulnerabilities package filter matches identity, not a substring', () => {
  const packages = [
    pkg('libssl3', [finding('ONLY-LIBSSL3', 'high')], { purl: '', version: '3.0' }),
    pkg('libssl3-dev', [finding('DEV-ONLY', 'high')], { purl: '', version: '3.0' }),
  ]
  const html = renderVulnerabilities({
    build: parsedBuild(packages), packageFilter: 'libssl3@3.0',
  })
  assert.match(html, /ONLY-LIBSSL3/)
  assert.doesNotMatch(html, /DEV-ONLY/)
  assert.match(html, />1 of 2</)
})

test('an advisory severity is the worst criticality across package hits', () => {
  const data = deriveBuildAdvisories([
    pkg('first', [finding('SHARED', 'medium')]),
    pkg('second', [finding('SHARED', 'critical')]),
  ])
  assert.equal(data.advisories[0].severity, 'critical')
  assert.equal(data.counts.critical, 1)
  assert.equal(data.counts.medium, 0)
})

test('inventory projection carries the server published_at field', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async () => new Response(JSON.stringify({
    packages: [{
      name: 'github.com/go-jose/go-jose/v4', version: 'v4.1.1',
      purl: 'pkg:golang/github.com/go-jose/go-jose/v4@v4.1.1',
      vuln_details: [{ vulnerabilities: [{
        identifier: 'GHSA-78h2-9frx-2jm8', criticality: 'high',
        published_at: '2026-04-03T03:28:56Z',
      }] }],
    }],
    pagination: {},
  }), { headers: { 'Content-Type': 'application/json' } })
  try {
    const [loaded] = await loadVersionFindings(
      'token', { organizationID: 'org', projectID: 'project' },
      'images', 'published-fixture', [{ id: 'published-build' }],
    )
    assert.equal(loaded.packages[0].findings[0].published, '2026-04-03T03:28:56Z')
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('the Build route selects Packages and unknown facets fall back to Overview', () => {
  const detail = {
    version: { name: 'v1', fingerprint: 'fp' },
    build: parsedBuild([]),
    sboms: [],
  }
  const renderRoute = (path) => renderToStaticMarkup(React.createElement(
    MemoryRouter,
    { initialEntries: [path] },
    React.createElement(
      Routes,
      null,
      React.createElement(Route, {
        path: '/buckets/:bucket/versions/:fingerprint/builds/:build/:facet?',
        element: React.createElement(BuildView, {
          bucket: 'images', detail, loading: false, failure: null,
          onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
        }),
      }),
    ),
  ))
  const packages = renderRoute('/buckets/images/versions/fp/builds/b1/packages')
  assert.match(packages, /aria-selected="true"[^>]*><span class="pf-v6-c-tabs__item-text">Packages/)
  assert.match(packages, /<h1[^>]*>Packages<\/h1>|pf-v6-c-card__title[^>]*>Packages</)

  const unknown = renderRoute('/buckets/images/versions/fp/builds/b1/not-a-facet')
  assert.match(unknown, /aria-selected="true"[^>]*><span class="pf-v6-c-tabs__item-text">Overview/)
  assert.match(unknown, /Build options/)

  const source = readFileSync(new URL('../src/screens/Build.tsx', import.meta.url), 'utf8')
  assert.match(source, /const selectedFacet = buildFacet\(facetParam\)/)
})

test('the Security build navigation target ends in Vulnerabilities', () => {
  assert.equal(
    versionBuildFacetPath('images', 'fp', 'b1', 'vulnerabilities'),
    '/buckets/images/versions/fp/builds/b1/vulnerabilities',
  )
  const source = readFileSync(new URL('../src/screens/Version.tsx', import.meta.url), 'utf8')
  assert.match(
    source,
    /onOpenSecurityBuild=\{\(build\) => navigate\([\s\S]*?'vulnerabilities'[\s\S]*?\)\}/,
  )
})

test('the Builds table Packages cell links to the Packages facet', () => {
  const item = parsedBuild([])
  const html = renderToStaticMarkup(React.createElement(BuildTable, {
    builds: [item], bucket: 'images', fingerprint: 'fp', onOpenBuild: () => {},
    securitySummary: {
      scannerConfigured: true, version: null,
      builds: [{ buildID: 'b1', inventory: 'parsed', packages: 0 }],
    },
  }))
  assert.match(html, /href="\/buckets\/images\/versions\/fp\/builds\/b1\/packages"/)
})
