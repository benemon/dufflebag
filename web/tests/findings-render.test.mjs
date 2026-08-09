import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let FindingCounts, PackageFindingsTable

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ FindingCounts, PackageFindingsTable } =
    await vite.ssrLoadModule('/src/components/Findings.tsx'))
})

after(async () => {
  await vite?.close()
})

const render = (element) => renderToStaticMarkup(element)

const affected = {
  name: 'openssl',
  version: '3.0.0',
  purl: 'pkg:apk/alpine/openssl@3.0.0',
  sboms: [],
  findings: [
    {
      identifier: 'CVE-2026-0001',
      description: 'a flaw',
      criticality: 'critical',
      severity: 'CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H',
      fixedVersion: '3.0.1',
      aliases: ['GHSA-aaaa-bbbb-cccc'],
      firstSeen: '2026-08-01T00:00:00Z',
    },
  ],
}

const unaffected = { name: 'zlib', version: '1.2.13', purl: 'pkg:apk/alpine/zlib@1.2.13', sboms: [] }

const scan = {
  adapter: 'osv',
  engine: 'https://api.osv.dev',
  databaseRevision: 'unreported',
  observedAt: '2026-08-07T12:00:00Z',
  submitted: 40,
}

test('the findings cell shows counts per severity, not prose', () => {
  const html = render(React.createElement(FindingCounts, { findings: [
    { identifier: 'A', criticality: 'critical' },
    { identifier: 'B', criticality: 'critical' },
    { identifier: 'C', criticality: 'medium' },
  ] }))
  assert.match(html, /2 critical/)
  assert.match(html, /1 medium/)
  // The cell must NOT carry the vector: that is what made it wrap off screen.
  assert.doesNotMatch(html, /CVSS/)
})

test('worst severity leads the findings cell', () => {
  const html = render(React.createElement(FindingCounts, { findings: [
    { identifier: 'A', criticality: 'low' },
    { identifier: 'B', criticality: 'critical' },
  ] }))
  assert.ok(html.indexOf('critical') < html.indexOf('low'), 'critical must precede low')
})

test('a package with no findings shows a dash and nothing to open', () => {
  const html = render(React.createElement(FindingCounts, { findings: [] }))
  assert.match(html, /data-findings="none"/)
  assert.match(html, /—/)
})

test('the expanded region gives the CVSS vector its own column', () => {
  const html = render(React.createElement(PackageFindingsTable, { findings: affected.findings }))
  assert.match(html, /CVE-2026-0001/)
  assert.match(html, /critical/)
  assert.match(html, /CVSS:3.1/, 'the provider value is shown verbatim')
  assert.match(html, /3\.0\.1/)
  assert.match(html, /GHSA-aaaa-bbbb-cccc/)
  assert.match(html, /data-findings-table="true"/, 'the detail is a table, not prose')
})

test('the expanded region orders findings worst first', () => {
  const html = render(React.createElement(PackageFindingsTable, {
    findings: [
      { ...affected.findings[0], identifier: 'LOW-1', criticality: 'low' },
      { ...affected.findings[0], identifier: 'CRIT-1', criticality: 'critical' },
    ],
  }))
  assert.ok(html.indexOf('CRIT-1') < html.indexOf('LOW-1'))
})

test('a finding with no fix says so rather than showing nothing', () => {
  const html = render(React.createElement(PackageFindingsTable, {
    findings: [{ ...affected.findings[0], fixedVersion: '', aliases: [] }],
  }))
  assert.match(html, /No fix available/)
})

// Several fixed versions are stored as a verbatim set rather than reduced to a
// semantic minimum, so they must not be joined into one wrapping string.
test('multiple fixed versions render one per line', () => {
  const html = render(React.createElement(PackageFindingsTable, {
    findings: [{ ...affected.findings[0], fixedVersion: '3.7, 2.9.1' }],
  }))
  assert.match(html, /3\.7/)
  assert.match(html, /2\.9\.1/)
  assert.doesNotMatch(html, /3\.7, 2\.9\.1/, 'versions must not be one joined string')
})

test('only affected rows get an expander', async () => {
  const { PackageTableForTest } = await vite.ssrLoadModule('/src/screens/Build.tsx')
  const html = renderToStaticMarkup(
    React.createElement(PackageTableForTest, {
      packages: [
        { name: 'openssl', version: '3.0.0', purl: 'pkg:a/openssl@3.0.0', sboms: [],
          findings: [{ identifier: 'CVE-1', criticality: 'high', severity: '', fixedVersion: '', aliases: [] }] },
        { name: 'zlib', version: '1.2.13', purl: 'pkg:a/zlib@1.2.13', sboms: [] },
      ],
      expanded: null,
      onToggle: () => {},
    }),
  )
  const rows = html.split('<tr').slice(1)
  const affectedRow = rows.find((r) => r.includes('openssl'))
  const cleanRow = rows.find((r) => r.includes('zlib'))
  assert.match(affectedRow, /pf-c-table__compound-expansion-toggle|button/,
    'the affected row must offer an expander')
  assert.doesNotMatch(cleanRow, /button/, 'a clean row must offer no expander at all')
})

// The filter a reader reaches for first: 205 packages, four of them affected.
test('the packages toolbar can select only rows with findings', async () => {
  const { PackagesCardForTest } = await vite.ssrLoadModule('/src/screens/Build.tsx')
  const build = {
    id: 'build-1',
    packageInventory: {
      status: 'parsed',
      scan: { adapter: 'osv', observedAt: '2026-08-07T12:00:00Z', submitted: 2 },
      packages: [
        { name: 'openssl', version: '3.0.0', purl: 'pkg:a/openssl@3.0.0', sboms: [],
          findings: [{ identifier: 'CVE-1', criticality: 'high', severity: '', fixedVersion: '', aliases: [] }] },
        { name: 'zlib', version: '1.2.13', purl: 'pkg:a/zlib@1.2.13', sboms: [] },
      ],
    },
  }
  const html = renderToStaticMarkup(
    React.createElement(PackagesCardForTest, { build, outOfScanSet: false }),
  )
  assert.match(html, /With findings \(1\)/, 'the affected count is offered as a filter')
  assert.match(html, /Without findings \(1\)/)
  assert.match(html, /Severity: high/, 'only bands actually present are offered')
  assert.doesNotMatch(html, /Severity: critical/, 'a band nothing carries must not be offered')
})

// PatternFly's compound expansion makes the cell interactive but draws no
// marker, so the chips alone read as static. The caret is the affordance.
test('an expandable findings cell carries a caret', () => {
  const findings = [{ identifier: 'CVE-1', criticality: 'high' }]
  const closed = render(React.createElement(FindingCounts, { findings, isExpanded: false }))
  const open = render(React.createElement(FindingCounts, { findings, isExpanded: true }))
  assert.match(closed, /data-caret="closed"/)
  assert.match(open, /data-caret="open"/)
})

test('a cell that cannot expand carries no caret', () => {
  const html = render(React.createElement(FindingCounts, {
    findings: [{ identifier: 'CVE-1', criticality: 'high' }],
  }))
  assert.doesNotMatch(html, /data-caret/, 'a non-expandable cell must not imply it opens')
})
