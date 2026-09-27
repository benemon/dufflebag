import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router'
import { createServer } from 'vite'

let vite
let BuildView, PackagesCard, VulnerabilitiesCard, BuildTable, versionBuildFacetPath
let projectBuildFindings, loadVersionFindings, runsDisagree

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
  ;({ projectBuildFindings } = await vite.ssrLoadModule('/src/data/advisories.ts'))
  ;({ loadVersionFindings, runsDisagree } = await vite.ssrLoadModule('/src/data/versions.ts'))
})

after(async () => {
  await vite?.close()
})

// The handler's own response body, written by
// internal/platform/v1/build_findings_test.go (DUFFLEBAG_UPDATE_FIXTURES=1);
// the Go test fails when this file drifts from the handler.
const buildFindingsFixture = JSON.parse(readFileSync(
  new URL('./fixtures/build-findings.json', import.meta.url), 'utf8',
))

// Wire advisories shaped like the fixture's, for cases the fixture does not carry.
const wireAdvisory = (identifier, severity, packages, over = {}) => ({
  identifier, severity, summary: '', aliases: [`ALIAS-${identifier}`],
  published: '2026-04-03T03:28:56Z', fixed_versions: ['4.1.4'],
  packages: packages.map((name) => ({
    name, version: 'v4.1.1', purl: `pkg:golang/${name}@v4.1.1`, sbom_id: 'sbom-a', fixed_version: '4.1.4', reported: '',
  })),
  ...over,
})
const wire = (advisories, over = {}) => ({
  ...buildFindingsFixture, advisories, packages_total: 6,
  packages_affected: new Set(advisories.flatMap((a) => a.packages.map((p) => p.purl))).size,
  ...over,
})

const finding = (identifier, criticality, over = {}) => ({
  identifier,
  criticality,
  severity: '',
  fixedVersion: '4.1.4',
  aliases: [`ALIAS-${identifier}`],
  firstSeen: '2026-08-01T00:00:00Z',
  published: '2026-04-03T03:28:56Z',
  ...over,
})

// These package shapes are projected from the recorded OSV bodies at
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

const observedAt = '2026-09-27T14:00:00Z'
const parsedBuild = (packages) => build({
  status: 'parsed', packages,
  scan: { adapter: 'osv', observedAt, submitted: packages.length },
})

const renderVulnerabilities = (findings, over = {}) => renderToStaticMarkup(React.createElement(
  VulnerabilitiesCard,
  {
    findings,
    buildPath: '/buckets/images/versions/fp/builds/b1/vulnerabilities',
    ...over,
  },
))

test('the fixture projects one row per advisory with a package entry per reporting SBOM', () => {
  const data = projectBuildFindings(buildFindingsFixture)
  assert.equal(data.advisories.length, 2)
  const shared = data.advisories.find((a) => a.identifier === 'GHSA-78h2-9frx-2jm8')
  assert.equal(shared.hits.length, 2)
  assert.equal(shared.packages, 1, 'one identity in two SBOMs counts once')
  assert.equal(shared.hits[0].reported, 'CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H')
  assert.deepEqual(data.run, { id: 'run-a', observedAt: '2026-09-27T14:00:00Z' })
  assert.equal(data.packagesTotal, 3)
  assert.equal(data.packagesAffected, 2)
  const html = renderVulnerabilities(data)
  assert.match(html, />1 package</)
  assert.match(html, /2 advisories · 2 of 3 packages affected/)
})

test('two identities sharing an advisory render one row with two affected packages', () => {
  const data = projectBuildFindings(wire([wireAdvisory('SHARED-1', 'high', ['go-jose', 'go-jose-fork'])]))
  assert.equal(data.advisories.length, 1)
  assert.equal(data.advisories[0].packages, 2)
  assert.match(renderVulnerabilities(data), />2 packages</)
})

test('severity strip counts remain build totals under an active severity filter', () => {
  const data = projectBuildFindings(wire([
    wireAdvisory('CRITICAL-1', 'critical', ['critical-package']),
    wireAdvisory('HIGH-1', 'high', ['high-package']),
  ]))
  const html = renderVulnerabilities(data, { severityFilter: ['high'] })
  const criticalItem = html.match(/<button[^>]*id="severity-critical"[\s\S]*?<\/button>/)?.[0] ?? ''
  assert.match(criticalItem, /critical/)
  assert.match(criticalItem, />1<\/span>/)
  assert.match(html, /aria-pressed="true" id="severity-high"/)
})

test('zero-count severity items are disabled', () => {
  const html = renderVulnerabilities(projectBuildFindings(buildFindingsFixture))
  assert.match(html, /disabled="" id="severity-critical"/)
  assert.doesNotMatch(html, /disabled="" id="severity-high"/)
})

test('top vulnerable packages sort critical before high and break ties by name', () => {
  const data = projectBuildFindings(wire([
    wireAdvisory('Z-C', 'critical', ['zeta']),
    wireAdvisory('A-C', 'critical', ['alpha']), wireAdvisory('A-H', 'high', ['alpha']),
    wireAdvisory('B-C1', 'critical', ['beta']), wireAdvisory('B-C2', 'critical', ['beta']),
    wireAdvisory('G-H', 'high', ['gamma']),
    wireAdvisory('D-H', 'high', ['delta']),
    wireAdvisory('E-H', 'high', ['epsilon']),
  ]))
  assert.deepEqual(
    data.topVulnerablePackages.map((row) => row.name),
    ['beta', 'alpha', 'zeta', 'delta', 'epsilon'],
  )
})

test('both facets render their run observedAt as the as-of instant', () => {
  const vulnerabilities = renderVulnerabilities(projectBuildFindings(buildFindingsFixture))
  const packages = renderToStaticMarkup(React.createElement(PackagesCard, {
    build: parsedBuild([pkg('go-jose', [finding('GHSA-78h2-9frx-2jm8', 'high')])]), scanned: true,
  }))
  for (const html of [vulnerabilities, packages]) {
    assert.match(html, /As of/)
    assert.match(html, new RegExp(`dateTime="${observedAt}"`, 'i'))
    assert.match(html, /UTC/)
  }
})

test('Vulnerabilities renders every fixed findings state distinctly', () => {
  const state = (html) => html.match(/data-state="([a-z-]+)"/)?.[1]
  assert.equal(state(renderVulnerabilities(null, { loading: true })), 'loading')
  assert.equal(state(renderVulnerabilities(null, { failure: 'boom' })), 'failed')
  assert.equal(state(renderVulnerabilities(projectBuildFindings(wire([], { inventory: 'unparseable', packages_total: 0 })))), 'unparseable')
  assert.equal(state(renderVulnerabilities(projectBuildFindings(wire([], { scanner_configured: false, scanned: false, run: null })))), 'never-scanned')
  assert.equal(state(renderVulnerabilities(projectBuildFindings(wire([], { scanned: false, run: null })))), 'not-yet-scanned')
  const zero = renderVulnerabilities(projectBuildFindings(wire([], { packages_total: 4, packages_affected: 0 })))
  assert.equal(state(zero), 'zero-findings')
  assert.match(zero, /No findings in 4 packages/)
  const html = renderVulnerabilities(projectBuildFindings(wire([], { scanner_configured: false, scanned: false, run: null })))
  assert.match(html, /No vulnerability source is configured/)
})

test('the Vulnerabilities package filter matches identity, not a substring', () => {
  const data = projectBuildFindings(wire([
    wireAdvisory('ONLY-LIBSSL3', 'high', []), wireAdvisory('DEV-ONLY', 'high', []),
  ]))
  data.advisories[0].hits = [{ packageIdentity: 'libssl3@3.0', name: 'libssl3', version: '3.0', sbomID: 's', fixedVersion: '', reported: '' }]
  data.advisories[1].hits = [{ packageIdentity: 'libssl3-dev@3.0', name: 'libssl3-dev', version: '3.0', sbomID: 's', fixedVersion: '', reported: '' }]
  const html = renderVulnerabilities(data, { packageFilter: 'libssl3@3.0' })
  assert.match(html, /ONLY-LIBSSL3/)
  assert.doesNotMatch(html, /DEV-ONLY/)
  assert.match(html, />1 of 2</)
})

test('an unrecognised wire severity projects as unknown', () => {
  const data = projectBuildFindings(wire([wireAdvisory('ODD', 'catastrophic', ['x'])]))
  assert.equal(data.advisories[0].severity, 'unknown')
  assert.equal(data.counts.unknown, 1)
})

test('the two build reads disagree only when both carry a run and the runs differ', () => {
  assert.equal(runsDisagree('run-a', 'run-b'), true)
  assert.equal(runsDisagree('run-a', 'run-a'), false)
  assert.equal(runsDisagree(undefined, 'run-b'), false)
  assert.equal(runsDisagree('run-a', undefined), false)
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
