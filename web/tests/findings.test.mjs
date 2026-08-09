import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import { createServer } from 'vite'

let vite
let rollup, rollupVersion, coverageSummary, hasCoverageGap, scanAttribution

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
  })
  ;({ rollup, versionRollup: rollupVersion, coverageSummary, hasCoverageGap, scanAttribution } =
    await vite.ssrLoadModule('/src/data/findings.ts'))
})

after(async () => {
  await vite?.close()
})

const finding = (identifier, criticality) => ({ identifier, criticality })

const pkg = (name, version, purl, ...findings) => ({
  name,
  version,
  purl,
  ...(findings.length ? { findings } : {}),
})

test('two findings on one package count as one affected package', () => {
  const result = rollup([
    {
      buildID: 'build-1',
      packages: [
        pkg('openssl', '3.0.0', 'pkg:apk/alpine/openssl@3.0.0',
          finding('CVE-1', 'critical'), finding('CVE-2', 'critical')),
      ],
    },
  ])
  assert.equal(result.worst, 'critical')
  assert.equal(result.affectedAtWorst, 1, 'the count is of packages, not findings')
})

test('a second package at the worst severity increments the count', () => {
  const result = rollup([
    {
      buildID: 'build-1',
      packages: [
        pkg('openssl', '3.0.0', 'pkg:apk/alpine/openssl@3.0.0', finding('CVE-1', 'critical')),
        pkg('busybox', '1.36.1-r0', 'pkg:apk/alpine/busybox@1.36.1-r0', finding('CVE-2', 'critical')),
      ],
    },
  ])
  assert.equal(result.worst, 'critical')
  assert.equal(result.affectedAtWorst, 2)
})

test('lower severity packages do not join the worst-severity count', () => {
  const result = rollup([
    {
      buildID: 'build-1',
      packages: [
        pkg('openssl', '3.0.0', 'pkg:apk/alpine/openssl@3.0.0', finding('CVE-1', 'critical')),
        pkg('zlib', '1.2.13', 'pkg:apk/alpine/zlib@1.2.13', finding('CVE-2', 'medium')),
        pkg('musl', '1.2.4', 'pkg:apk/alpine/musl@1.2.4', finding('CVE-3', 'low')),
      ],
    },
  ])
  assert.equal(result.worst, 'critical')
  assert.equal(result.affectedAtWorst, 1, 'only the critical package sits at the worst level')
  assert.equal(result.affectedTotal, 3, 'but all three are affected')
})

test('a package duplicated across two SBOMs of one build counts once', () => {
  // The same identity reported by an application SBOM and a base SBOM. One
  // package is affected, not two: the duplication is a projection artefact.
  const duplicated = pkg('openssl', '3.0.0', 'pkg:apk/alpine/openssl@3.0.0',
    finding('CVE-1', 'high'))
  const result = rollup([
    { buildID: 'build-1', packages: [duplicated, { ...duplicated }] },
  ])
  assert.equal(result.worst, 'high')
  assert.equal(result.affectedAtWorst, 1)
  assert.equal(result.affectedTotal, 1)
})

test('the same package in two builds counts twice', () => {
  // Two images are affected, so two instances are affected — this is the
  // half of the rule that a naive global dedup would break.
  const affected = pkg('openssl', '3.0.0', 'pkg:apk/alpine/openssl@3.0.0',
    finding('CVE-1', 'high'))
  const result = rollup([
    { buildID: 'build-1', packages: [affected] },
    { buildID: 'build-2', packages: [{ ...affected }] },
  ])
  assert.equal(result.worst, 'high')
  assert.equal(result.affectedAtWorst, 2)
})

test('a package takes its own worst finding', () => {
  const result = rollup([
    {
      buildID: 'build-1',
      packages: [
        pkg('openssl', '3.0.0', 'pkg:apk/alpine/openssl@3.0.0',
          finding('CVE-1', 'low'), finding('CVE-2', 'high')),
      ],
    },
  ])
  assert.equal(result.worst, 'high')
  assert.equal(result.affectedAtWorst, 1)
})

test('no findings yields no worst severity', () => {
  const result = rollup([
    { buildID: 'build-1', packages: [pkg('openssl', '3.0.0', 'pkg:apk/alpine/openssl@3.0.0')] },
  ])
  assert.equal(result.worst, undefined)
  assert.equal(result.affectedAtWorst, 0)
  assert.equal(result.affectedTotal, 0)
})

test('unsupported ecosystems are reported as coverage, never as zero findings', () => {
  const attribution = { submitted: 40, unsupported: 12, unversioned: 2, invalid: 0 }
  const summary = coverageSummary(attribution)
  assert.ok(hasCoverageGap(attribution), 'a coverage gap must be detectable')
  assert.ok(
    summary.some((part) => part.includes('does not cover')),
    `coverage summary must name the uncovered ecosystems: ${JSON.stringify(summary)}`,
  )
  assert.ok(summary.some((part) => part.includes('40 queried')))

  // Full coverage is a different state, and must be distinguishable.
  const full = { submitted: 40, unsupported: 0, unversioned: 0, invalid: 0 }
  assert.equal(hasCoverageGap(full), false)
})

test('attribution is absent when no scanner is configured', () => {
  const headers = new Headers()
  assert.equal(scanAttribution(headers), undefined)
})

test('attribution is parsed from the scan headers', () => {
  const headers = new Headers({
    'Dufflebag-Scan-Adapter': 'osv',
    'Dufflebag-Scan-Engine': 'https://api.osv.dev',
    'Dufflebag-Scan-Database-Revision': 'unreported',
    'Dufflebag-Scan-Observed-At': '2026-08-07T12:00:00Z',
    'Dufflebag-Scan-Submitted': '40',
    'Dufflebag-Scan-Unsupported': '12',
  })
  const attribution = scanAttribution(headers)
  assert.equal(attribution.adapter, 'osv')
  assert.equal(attribution.engine, 'https://api.osv.dev')
  assert.equal(attribution.databaseRevision, 'unreported')
  assert.equal(attribution.submitted, 40)
  assert.equal(attribution.unsupported, 12)
})

// The rule the design corrected: summing builds double-counts anything shipped
// on every platform. One flaw in libcurl on three builds is ONE problem.
test('a finding on every build counts once for the version', () => {
  const libcurl = (build) => ({
    buildID: build, platform: build, scanned: 200,
    packages: [{
      name: 'libcurl', version: '7.61.1', purl: 'pkg:rpm/libcurl@7.61.1',
      findings: [{ identifier: 'CVE-2023-38545', criticality: 'critical' }],
    }],
  })
  const result = rollupVersion([libcurl('docker'), libcurl('aws'), libcurl('azure')])
  assert.equal(result.worst, 'critical')
  assert.equal(result.findings, 1, 'three builds, one problem')
  assert.equal(result.affectedPackages, 1)
  assert.deepEqual(result.counts, [{ severity: 'critical', count: 1 }])
  // The per-build view is what says where it lives.
  assert.equal(result.builds.length, 3)
  for (const build of result.builds) {
    assert.equal(build.worst, 'critical', `${build.platform} carries it`)
  }
})

test('the same advisory on different packages counts separately', () => {
  const result = rollupVersion([{
    buildID: 'b', platform: 'docker', scanned: 10,
    packages: [
      { name: 'libcurl', version: '1', purl: 'pkg:a/libcurl@1',
        findings: [{ identifier: 'CVE-1', criticality: 'high' }] },
      { name: 'libssl', version: '1', purl: 'pkg:a/libssl@1',
        findings: [{ identifier: 'CVE-1', criticality: 'high' }] },
    ],
  }])
  assert.equal(result.findings, 2, 'one advisory, two packages, two problems')
  assert.equal(result.affectedPackages, 2)
})

test('a build with no findings still reports what it scanned', () => {
  const result = rollupVersion([
    { buildID: 'a', platform: 'docker', scanned: 205,
      packages: [{ name: 'zlib', version: '1', purl: 'pkg:a/zlib@1' }] },
  ])
  assert.equal(result.worst, undefined)
  assert.equal(result.findings, 0)
  assert.equal(result.builds[0].scanned, 205, 'coverage is reported even with nothing found')
})
