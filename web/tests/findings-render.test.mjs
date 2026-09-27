import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let PackageTableForTest, PackagesCardForTest

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ PackageTableForTest, PackagesCardForTest } =
    await vite.ssrLoadModule('/src/screens/Build.tsx'))
})

after(async () => {
  await vite?.close()
})

const finding = {
  identifier: 'GHSA-78h2-9frx-2jm8',
  description: 'Go JOSE Panics in JWE decryption',
  criticality: 'high',
  severity: 'CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H',
  fixedVersion: '4.1.4',
  aliases: ['CVE-2026-34986', 'GO-2026-4945'],
  firstSeen: '2026-08-01T00:00:00Z',
  published: '2026-04-03T03:28:56Z',
}

// Producer-derived from internal/scan/testdata/osv/detail-GHSA-78h2-9frx-2jm8.json,
// the same recorded body served by the browser smoke's OSV stub.
const affected = {
  name: 'github.com/go-jose/go-jose/v4',
  version: 'v4.1.1',
  purl: 'pkg:golang/github.com/go-jose/go-jose/v4@v4.1.1',
  sboms: [{ id: 'sbom-1', name: 'scanner-state', format: 'CYCLONEDX' }],
  findings: [finding],
}

const unaffected = {
  name: 'zlib', version: '1.2.13', purl: 'pkg:apk/alpine/zlib@1.2.13', sboms: [],
}

test('the Packages table has a sticky header', () => {
  const html = renderToStaticMarkup(React.createElement(PackageTableForTest, {
    packages: [affected], vulnerabilityPath: '/builds/b1/vulnerabilities',
  }))
  const table = html.match(/<table[^>]*aria-label="Packages"[^>]*>/)?.[0] ?? ''
  assert.match(table, /pf-m-sticky-header/)
})

test('Packages Findings labels link to the Vulnerabilities package filter', () => {
  const html = renderToStaticMarkup(React.createElement(PackageTableForTest, {
    packages: [affected, unaffected], vulnerabilityPath: '/builds/b1/vulnerabilities',
  }))
  assert.match(html, /href="\/builds\/b1\/vulnerabilities\?package=pkg%3Agolang%2Fgithub/)
  assert.match(html, />1 high</)
  assert.match(html, />›</)
  assert.match(html, /data-findings="none"/)
  assert.doesNotMatch(html, /compound-expansion-toggle|data-findings-table/)
})

test('the packages toolbar offers the finding filters carried by the inventory', () => {
  const build = {
    id: 'build-1',
    packageInventory: {
      status: 'parsed',
      scan: { adapter: 'osv', observedAt: '2026-08-07T12:00:00Z', submitted: 2 },
      packages: [affected, unaffected],
    },
  }
  const html = renderToStaticMarkup(React.createElement(PackagesCardForTest, { build }))
  assert.match(html, /With findings \(1\)/)
  assert.match(html, /Without findings \(1\)/)
  assert.match(html, /Severity: high/)
  assert.doesNotMatch(html, /Severity: critical/)
})

test('packages paginate the filtered in-memory rows above and below the table', () => {
  const packages = Array.from({ length: 21 }, (_, index) => {
    const number = String(index + 1).padStart(2, '0')
    return {
      name: `package-${number}`, version: '1.0.0',
      purl: `pkg:a/package-${number}@1.0.0`, sboms: [],
    }
  })
  const html = renderToStaticMarkup(React.createElement(PackagesCardForTest, {
    build: { id: 'build-1', packageInventory: { status: 'parsed', packages } },
  }))
  assert.match(html, />package-20</)
  assert.doesNotMatch(html, />package-21</)
  assert.ok((html.match(/pf-v6-c-pagination/g) ?? []).length >= 2)
})

test('the Packages package filter matches identity rather than a name substring', () => {
  const packages = [
    { name: 'libssl3', version: '3.0', purl: '', sboms: [] },
    { name: 'libssl3-dev', version: '3.0', purl: '', sboms: [] },
  ]
  const html = renderToStaticMarkup(React.createElement(PackagesCardForTest, {
    build: { id: 'build-1', packageInventory: { status: 'parsed', packages } },
    packageFilter: 'libssl3@3.0',
  }))
  assert.match(html, />libssl3</)
  assert.doesNotMatch(html, />libssl3-dev</)
  assert.match(html, />1 of 2</)
})
