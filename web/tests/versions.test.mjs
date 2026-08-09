import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let VersionsView
let VersionsFacet
let BucketChannelsFacet
let AssignmentHistoryTable
let EnforcedProvisionersRow
let VersionView
let BuildView
let loadVersions
let loadVersion
let loadBucketPage
let loadEnforcedProvisioners
let loadChannelHistory
let loadVersionDetail
let loadBuildDetail
let terraformConsumeSnippet
let terraformPromotionSnippet
let packerBuildCommand
let sbomFileName
let channelVersionGap
let parentFreshnessText
let FacetRail
let facetCountText
let platformTenancyGap
const versionDataSource = readFileSync(new URL('../src/data/versions.ts', import.meta.url), 'utf8')

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ VersionsView, VersionsFacet, BucketChannelsFacet, AssignmentHistoryTable,
    EnforcedProvisionersRow, channelVersionGap, parentFreshnessText } =
    await vite.ssrLoadModule('/src/screens/Versions.tsx'))
  ;({ VersionView, terraformConsumeSnippet, terraformPromotionSnippet } =
    await vite.ssrLoadModule('/src/screens/Version.tsx'))
  ;({ BuildView, packerBuildCommand, sbomFileName } = await vite.ssrLoadModule('/src/screens/Build.tsx'))
  ;({ loadVersions, loadVersion, loadBucketPage, loadEnforcedProvisioners, loadChannelHistory,
    loadVersionDetail, loadBuildDetail } =
    await vite.ssrLoadModule('/src/data/versions.ts'))
  ;({ platformTenancyGap } = await vite.ssrLoadModule('/src/data/tenant.ts'))
  ;({ FacetRail, facetCountText } = await vite.ssrLoadModule('/src/screens/RegistryFacets.tsx'))
})

after(async () => {
  await vite.close()
})

// Fixtures derived from the SERVER's rendering, not imagined: renderVersion and
// renderBuild in internal/compat/hcp2023/handler.go, field names and omitempty
// behaviour from internal/compat/hcp2023/models/
// hashicorp_cloud_packer20230101_{version,build,artifact}.go. A complete
// version leaves "v0" and gains VERSION_ACTIVE together; an incomplete one is
// "v0" VERSION_RUNNING. `builds` and `artifacts` carry no omitempty, so they
// are always present, [] when empty.
const completeVersion = {
  id: '01K1CJ4X8GVQZJ0R1T2U3V4W5X',
  name: 'v1',
  bucket_name: 'images',
  fingerprint: 'fp-complete',
  template_type: 'HCL2',
  status: 'VERSION_ACTIVE',
  builds: [
    {
      id: '01K1CJ4X8H0000000000000001',
      version_id: '01K1CJ4X8GVQZJ0R1T2U3V4W5X',
      component_type: 'docker.ubuntu',
      status: 'BUILD_DONE',
      platform: 'docker',
      packer_run_uuid: 'run-1',
      labels: { ImageDigest: 'sha256:label-value' },
      metadata: { packer: {
        version: '1.16.0',
        options: {
          path: './image.pkr.hcl', vars: ['base_image', 'run_label'],
          'var-files': ['./production.pkrvars.hcl'],
          only: ['docker.ubuntu'], debug: true, force: true,
        },
        os: { type: 'linux', details: { arch: 'amd64', version: '6.12' } },
        plugins: [{ name: 'docker', version: '1.1.4' }],
      } },
      artifacts: [
        {
          id: '01K1CJ4X8H0000000000000002',
          external_identifier: 'sha256:abc123',
          region: 'us-east-1',
          created_at: '2026-07-31T10:00:00.000Z',
        },
      ],
      created_at: '2026-07-31T10:00:00.000Z',
      updated_at: '2026-07-31T10:05:00.000Z',
    },
  ],
  created_at: '2026-07-31T10:00:00.000Z',
  updated_at: '2026-07-31T10:05:00.000Z',
}

const incompleteVersion = {
  id: '01K1CJ4X8H0000000000000003',
  name: 'v0',
  bucket_name: 'images',
  fingerprint: 'fp-incomplete',
  template_type: 'HCL2',
  status: 'VERSION_RUNNING',
  builds: [],
  created_at: '2026-07-31T11:00:00.000Z',
  updated_at: '2026-07-31T11:00:00.000Z',
}

const withFetch = async (routes, run) => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async (input) => {
    const path = String(input)
    for (const [suffix, respond] of Object.entries(routes)) {
      if (path.endsWith(suffix)) return respond()
    }
    throw new Error(`unexpected request ${path}`)
  }
  try {
    return await run()
  } finally {
    globalThis.fetch = originalFetch
  }
}

const json = (body, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })

const listMarkup = (versions, extra = {}) =>
  renderToStaticMarkup(React.createElement(VersionsView, {
    bucket: 'images',
    versions,
    loading: false,
    failure: null,
    onBack: () => {},
    onOpenVersion: () => {},
    ...extra,
  }))

const detailMarkup = (version, extra = {}) =>
  renderToStaticMarkup(React.createElement(VersionView, {
    bucket: 'images',
    version,
    loading: false,
    failure: null,
    onBackToRegistry: () => {},
    onBackToBucket: () => {},
    ...extra,
  }))

test('a complete version projects complete and a v0 projects incomplete', async () => {
  const versions = await withFetch(
    {
      // ListVersions answers newest first (registry_handler.go listVersions).
      '/versions': () => json({ versions: [incompleteVersion, completeVersion] }),
      '/channels': () => json({ channels: [] }),
    },
    () => loadVersions('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  assert.deepEqual(
    versions.map(({ fingerprint, name, state }) => ({ fingerprint, name, state })),
    [
      { fingerprint: 'fp-incomplete', name: 'v0', state: 'incomplete' },
      { fingerprint: 'fp-complete', name: 'v1', state: 'complete' },
    ],
  )
})

test('a revoked version projects and renders its revocation state, not incomplete', async () => {
  const versions = await withFetch(
    {
      '/versions': () => json({ versions: [
        { ...completeVersion, status: 'VERSION_REVOKED' },
        { ...completeVersion, fingerprint: 'fp-scheduled', status: 'VERSION_REVOCATION_SCHEDULED' },
      ] }),
      '/channels': () => json({ channels: [] }),
    },
    () => loadVersions('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  // A revoked version keeps its number: completion is carried by the name.
  assert.deepEqual(
    versions.map(({ name, state }) => ({ name, state })),
    [
      { name: 'v1', state: 'revoked' },
      { name: 'v1', state: 'revocation-scheduled' },
    ],
  )
  const markup = listMarkup(versions)
  assert.match(markup, />revoked</)
  assert.match(markup, />revocation scheduled</)
  assert.doesNotMatch(markup, />incomplete</)
})

test('an incomplete version renders as incomplete, not as broken', async () => {
  const versions = await withFetch(
    {
      '/versions': () => json({ versions: [incompleteVersion, completeVersion] }),
      '/channels': () => json({ channels: [] }),
    },
    () => loadVersions('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  const markup = listMarkup(versions)
  assert.match(markup, /class="registry-facet-heading">This bucket<\/div>/)
  assert.match(markup, />v0</)
  assert.match(markup, />incomplete</)
  assert.match(markup, />v1</)
  assert.match(markup, />complete</)
  assert.match(markup, /0 builds · 0 artifacts/)
  assert.doesNotMatch(markup, /could not be loaded/)
})

const facetVersion = {
  name: 'v1', fingerprint: 'fp-complete', state: 'complete', templateType: 'HCL2',
  channels: [], assignments: [], created: '2026-08-01 14:02 UTC',
  builds: [{ artifacts: [] }], parents: [], children: [],
}

test('the Versions facet is one full-width card without a redundant summary', () => {
  const markup = renderToStaticMarkup(React.createElement(VersionsFacet, {
    versions: [facetVersion],
    onOpenVersion: () => {},
  }))
  assert.match(markup, /^<div [^>]*class="pf-v6-c-card\b/)
  assert.equal((markup.match(/class="pf-v6-c-card\b/g) ?? []).length, 1)
  assert.match(markup, /class="pf-v6-c-card__title-text">Versions<\/div>/)
  assert.doesNotMatch(markup, /Version summary|>Complete<|>Incomplete<|>Artifacts</)
})

test('long fingerprints remain complete and opt into wrapping', () => {
  const fingerprint = `dufflebag-sbom-demo-${'1'.repeat(80)}`
  const markup = renderToStaticMarkup(React.createElement(VersionsFacet, {
    versions: [{ ...facetVersion, fingerprint }],
    onOpenVersion: () => {},
  }))
  assert.match(markup, new RegExp(
    `<code class="registry-fingerprint">${fingerprint}<\\/code>`,
  ))
})

test('an Overview facet with no count renders an empty count span', () => {
  assert.equal(facetCountText(), '')
})

test('enforced provisioners row distinguishes configured, empty, loading, and failure states', () => {
  const markup = (props) => renderToStaticMarkup(React.createElement(EnforcedProvisionersRow, props))

  const configured = markup({
    provisioners: ['required-plugins', 'eb-02'], loading: false, failure: null,
  })
  assert.match(configured, />Enforced provisioners</)
  assert.match(configured, />required-plugins, eb-02</)
  assert.doesNotMatch(configured, /None configured|Could not be loaded|Loading…/)

  const empty = markup({ provisioners: [], loading: false, failure: null })
  assert.match(empty, />None configured</)
  assert.doesNotMatch(empty, />—</)

  const loading = markup({ provisioners: [], loading: true, failure: null })
  assert.match(loading, />Loading…</)
  assert.doesNotMatch(loading, /None configured/)

  const failed = markup({ provisioners: [], loading: false, failure: 'reader request failed' })
  assert.match(failed, />Could not be loaded: reader request failed</)
  assert.doesNotMatch(failed, /None configured/)
})

test('enforced provisioners load from the bucket endpoint and prefer names over identifiers', async () => {
  const provisioners = await withFetch(
    {
      '/enforced_blocks/bucket/images': () => json({ enforced_block_detail: [
        { id: 'eb-01', name: 'required-plugins' },
        { id: 'eb-02' },
      ] }),
    },
    () => loadEnforcedProvisioners(
      'token', { organizationID: 'org', projectID: 'project' }, 'images',
    ),
  )
  assert.deepEqual(provisioners, ['required-plugins', 'eb-02'])
})

test('an unknown facet count renders an empty count span', () => {
  assert.equal(facetCountText({ status: 'unknown' }), '')
})

test('a known empty facet count renders zero', () => {
  assert.equal(facetCountText({ status: 'known', value: 0 }), '0')
})

const facetRailMarkup = () =>
  renderToStaticMarkup(React.createElement(FacetRail, {
    active: 'packages',
    heading: 'This build',
    label: 'Build facets',
    facets: [
      { key: 'overview', label: 'Overview', content: 'Overview content' },
      {
        key: 'artifacts', label: 'Artifacts', count: { status: 'known', value: 0 },
        content: 'Artifact content',
      },
      {
        key: 'packages', label: 'Packages', count: { status: 'unknown' },
        content: 'Package content',
      },
    ],
    onSelect: () => {},
  }))

test('the facet rail keeps its natural mixed-case heading in the markup', () => {
  const markup = facetRailMarkup()
  assert.match(markup, /class="registry-facet-heading">This build<\/div>/)
})

test('the facet rail renders labels and counts in separate spans', () => {
  const markup = facetRailMarkup()
  assert.match(markup, /aria-current="false"[^>]*><span class="registry-facet-label">Overview<\/span><span class="registry-facet-count"><\/span>/)
  assert.match(markup, /<span class="registry-facet-label">Artifacts<\/span><span class="registry-facet-count">0<\/span>/)
  assert.doesNotMatch(markup, /Overview \(|Artifacts \(|Packages \(/)
})

test('the facet rail marks only its active item as current', () => {
  const markup = facetRailMarkup()
  assert.match(markup, /aria-current="false"[^>]*><span class="registry-facet-label">Overview/)
  assert.match(markup, /aria-current="true"[^>]*><span class="registry-facet-label">Packages/)
})

test('build and artifact details come through to the version page', async () => {
  const version = await withFetch(
    {
      '/versions/fp-complete': () => json({ version: completeVersion }),
      '/channels': () => json({ channels: [] }),
    },
    () => loadVersion('token', { organizationID: 'org', projectID: 'project' }, 'images', 'fp-complete'),
  )
  assert.deepEqual(version.builds, [
    {
      id: '01K1CJ4X8H0000000000000001',
      component: 'docker.ubuntu',
      platform: 'docker',
      state: 'done',
      packerRunUUID: 'run-1',
      sourceExternalIdentifier: '',
      labels: { ImageDigest: 'sha256:label-value' },
      artifacts: [
        {
          id: '01K1CJ4X8H0000000000000002',
          externalIdentifier: 'sha256:abc123',
          region: 'us-east-1',
        },
      ],
      packerVersion: '1.16.0',
      plugins: [{ name: 'docker', version: '1.1.4' }],
      runnerOS: 'linux',
      arch: 'amd64',
      options: {
        path: './image.pkr.hcl', variables: ['base_image', 'run_label'],
        variableFiles: ['./production.pkrvars.hcl'],
        only: ['docker.ubuntu'], except: [], debug: true, force: true,
      },
      updated: '2026-07-31 10:05 UTC',
      packageInventory: { status: 'not-loaded' },
    },
  ])
  const markup = detailMarkup(version)
  assert.match(markup, /class="registry-facet-heading">This version<\/div>/)
  assert.match(markup, /docker\.ubuntu/)
  assert.match(markup, />done</)
  assert.match(markup, /run-1/)
  assert.match(markup, /linux/)
  assert.match(markup, /amd64/)
  assert.match(markup, /docker 1\.1\.4/)
})

test('the build list renders metadata columns and expandable package detail', () => {
  const version = {
    ...completeVersion,
    name: 'v1',
    fingerprint: 'fp-complete',
    state: 'complete',
    templateType: 'HCL2',
    channels: [],
    assignments: [],
    parents: [],
    children: [],
    created: '2026-07-31 10:00 UTC',
    builds: [{
      id: 'build-list-id', component: 'docker.ubuntu', platform: 'docker', state: 'done',
      packerRunUUID: 'run-list', sourceExternalIdentifier: '', labels: {}, artifacts: [],
      packerVersion: '1.16.0', plugins: [{ name: 'docker', version: '1.1.4' }],
      runnerOS: 'linux', arch: 'amd64', updated: '2026-07-31 10:05 UTC',
      options: {
        path: './image.pkr.hcl', variables: [], variableFiles: [],
        only: [], except: [], debug: false, force: false,
      },
      packageInventory: {
        status: 'parsed',
        packages: [
          { name: 'openssl', version: '3', purl: '', sboms: [] },
          { name: 'zlib', version: '1', purl: '', sboms: [] },
          { name: 'curl', version: '8', purl: '', sboms: [] },
        ],
      },
    }],
  }
  const markup = detailMarkup(version)
  assert.match(markup, />Packer runner OS</)
  for (const expected of [
    'Name', 'Status', 'Arch', 'Updated', 'docker.ubuntu',
    'done', 'linux', 'amd64', '1.16.0', 'docker 1.1.4', '0 artifacts',
    '3 packages', 'run-list',
  ]) assert.match(markup, new RegExp(expected))
})

test('build overview reconstructs masked options and the Artifacts facet names Platform', async () => {
  const detail = await withFetch(
    {
      '/versions/fp-complete': () => json({ version: completeVersion }),
      '/packages?pagination.page_size=100': () => json({
        packages: [{ name: 'openssl', version: '3.0.11' }],
        pagination: {},
      }),
      '/sboms': () => json({ sboms: [
        { id: 'sb-1', name: 'fp-complete', format: 'SPDX' },
      ] }),
    },
    () => loadBuildDetail(
      'token', { organizationID: 'org', projectID: 'project' }, 'images',
      'fp-complete', completeVersion.builds[0].id,
    ),
  )
  assert.equal(detail.build.packageInventory.packages.length, 1)
  assert.equal(
    packerBuildCommand(detail.build),
    'packer build \\\n  -var="base_image=***" \\\n  -var="run_label=***" \\\n  -var-file=./production.pkrvars.hcl \\\n  -only=docker.ubuntu \\\n  -debug \\\n  -force \\\n  ./image.pkr.hcl',
  )

  const markup = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images', detail, loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  assert.match(markup, /class="registry-facet-heading">This build<\/div>/)
  assert.match(markup, /Build options/)
  assert.match(markup, /Variable values are masked/)
  assert.match(markup, /Packer runner environment/)
  assert.match(markup, /Build labels/)
  assert.match(markup, /base_image=\*\*\*/)
  assert.match(markup, /ImageDigest/)
  assert.match(markup, />Platform</)
  assert.match(markup, />External ID</)
  assert.match(markup, /sha256:abc123/)
  assert.match(markup, />Packages<\/span><span class="registry-facet-count">1<\/span>/)
  assert.match(markup, /openssl/)
  assert.match(markup, /Reported by client-supplied SBOMs/)
  assert.doesNotMatch(markup, />Artifact</)

  // The stored SBOM offers its download on the overview (duf-cse): one row
  // per document in the Security card's row idiom, saved as the DOCUMENT
  // under "<name>.json" exactly as live HCP serves it (probed 2026-08-08).
  assert.deepEqual(detail.sboms, [{ id: 'sb-1', name: 'fp-complete', format: 'SPDX' }])
  assert.match(markup, />SBOM</)
  assert.match(markup, /aria-label="Download fp-complete\.json"/)
  assert.match(markup, />SPDX</)
  assert.doesNotMatch(markup, /zstd/)
  assert.doesNotMatch(markup, /pf-v6-c-menu-toggle/)
  assert.equal(sbomFileName(detail.sboms[0]), 'fp-complete.json')
  // The wire names every format the same way — no format-suffix invention.
  assert.equal(sbomFileName({ id: 'x', name: 'inv', format: 'CYCLONEDX' }), 'inv.json')

  // Several documents render one row each.
  const multi = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images',
    detail: { ...detail, sboms: [
      { id: 'sb-1', name: 'fp-complete', format: 'SPDX' },
      { id: 'sb-2', name: 'inventory', format: 'CYCLONEDX' },
    ] },
    loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  assert.match(multi, /aria-label="Download fp-complete\.json"/)
  assert.match(multi, /aria-label="Download inventory\.json"/)
  assert.match(multi, />CYCLONEDX</)

  // A build with no stored SBOMs offers no card at all: a control that
  // cannot succeed should not look available.
  const bare = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images', detail: { ...detail, sboms: [] }, loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  assert.doesNotMatch(bare, /aria-label="Download /)
  assert.doesNotMatch(bare, /pf-v6-c-card__title[^>]*>SBOM</)
})

test('an unparseable SBOM is not rendered as a zero package inventory', async () => {
  const load = (packageResponse) => withFetch(
    {
      '/versions/fp-complete': () => json({ version: completeVersion }),
      '/packages?pagination.page_size=100': packageResponse,
      '/sboms': () => json({ sboms: [] }),
    },
    () => loadBuildDetail(
      'token', { organizationID: 'org', projectID: 'project' }, 'images',
      'fp-complete', completeVersion.builds[0].id,
    ),
  )
  const unparseable = await load(() => json({
    message: 'package inventory is unparseable for SBOMs ["broken-report"]',
  }, 422))
  const unparseableMarkup = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images', detail: unparseable, loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  assert.match(unparseableMarkup, /SBOM unparseable/)
  assert.doesNotMatch(unparseableMarkup, /0 packages/)

  const empty = await load(() => json({ packages: [], pagination: {} }))
  const emptyMarkup = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images', detail: empty, loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  assert.match(emptyMarkup, /0 packages/)
  assert.doesNotMatch(emptyMarkup, /SBOM unparseable/)
})

test('a build load failure renders as a failure rather than an empty overview', () => {
  const markup = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images', detail: null, loading: false, failure: 'build not found',
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  assert.match(markup, /Build could not be loaded/)
  assert.match(markup, /build not found/)
  assert.doesNotMatch(markup, /Build options/)
})

test('package count follows every page rather than treating the first page as the total', async () => {
  const detail = await withFetch(
    {
      '/packages?pagination.page_size=100&pagination.next_page_token=next': () => json({
        packages: [{ name: 'zlib' }], pagination: {},
      }),
      '/packages?pagination.page_size=100': () => json({
        packages: [{ name: 'openssl' }], pagination: { next_page_token: 'next' },
      }),
      '/versions/fp-complete': () => json({ version: completeVersion }),
      '/sboms': () => json({ sboms: [] }),
    },
    () => loadBuildDetail(
      'token', { organizationID: 'org', projectID: 'project' }, 'images',
      'fp-complete', completeVersion.builds[0].id,
    ),
  )
  assert.equal(detail.build.packageInventory.status, 'parsed')
  assert.deepEqual(detail.build.packageInventory.packages.map(({ name }) => name), ['openssl', 'zlib'])
})

test('package inventory refetches while a build is in progress', () => {
  assert.match(
    versionDataSource,
    /useVersionData<VersionDetail \| null>\([\s\S]*?some\(buildIsInProgress\)/,
  )
  assert.match(
    versionDataSource,
    /useVersionData<BuildDetail \| null>\([\s\S]*?buildIsInProgress\(detail\.build\)/,
  )
  assert.match(
    versionDataSource,
    /reloadWhile\?\.\(loaded\)\) retry = setTimeout\(refresh, 500\)/,
  )
  assert.match(versionDataSource, /if \(retry\) clearTimeout\(retry\)/)
})

test('an incomplete version page states the absence of builds plainly', async () => {
  const version = await withFetch(
    {
      '/versions/fp-incomplete': () => json({ version: incompleteVersion }),
      '/channels': () => json({ channels: [] }),
    },
    () => loadVersion('token', { organizationID: 'org', projectID: 'project' }, 'images', 'fp-incomplete'),
  )
  const markup = detailMarkup(version)
  assert.match(markup, />v0</)
  assert.match(markup, />incomplete</)
  assert.match(markup, /No builds have been reported for this version/)
  assert.doesNotMatch(markup, /could not be loaded/)
})

test('channel assignment context names the channels pointing at each version', async () => {
  const versions = await withFetch(
    {
      '/versions': () => json({ versions: [incompleteVersion, completeVersion] }),
      // renderChannel (internal/compat/hcp2023/handler.go) nests the
      // assignment as a full version object — there is no flat
      // version_fingerprint on the wire.
      '/channels': () => json({ channels: [
        { name: 'production', version: { name: 'v1', fingerprint: 'fp-complete' } },
        { name: 'staging', version: { name: 'v1', fingerprint: 'fp-complete' } },
        // An unassigned channel points at nothing, so it appears nowhere here.
        { name: 'dev' },
        // The managed latest is unassigned until a completion; it must not
        // attach itself to any version here.
        { name: 'latest', managed: true, restricted: true },
      ] }),
    },
    () => loadVersions('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  assert.deepEqual(
    versions.map(({ fingerprint, channels }) => ({ fingerprint, channels })),
    [
      { fingerprint: 'fp-incomplete', channels: [] },
      { fingerprint: 'fp-complete', channels: ['production', 'staging'] },
    ],
  )
  const markup = listMarkup(versions)
  assert.match(markup, />production</)
  assert.match(markup, />staging</)
})

test('a version row expands to fingerprint, build summary, parent freshness, and children', () => {
  const version = {
    name: 'v14', fingerprint: 'child-fingerprint', state: 'complete', templateType: 'HCL2',
    channels: ['production'], assignments: [], created: '2026-08-01 14:02 UTC',
    builds: [{ artifacts: [{ id: 'artifact-1' }, { id: 'artifact-2' }] }],
    parents: [{
      bucket: 'base-images', versionName: 'v22', fingerprint: 'parent-fingerprint',
      channel: 'latest', freshness: { status: 'behind', currentVersion: 'v24' },
    }],
    children: [{
      bucket: 'derived-images', versionName: 'v3', fingerprint: 'descendant-fingerprint',
    }],
  }
  const markup = listMarkup([version])
  for (const expected of [
    'child-fingerprint', '1 build · 2 artifacts', 'base-images v22',
    'bucket now at v24', 'derived-images v3', 'Open v14',
  ]) assert.match(markup, new RegExp(expected))
})

test('collapsed bucket detail uses channel metadata and does not fetch assignment history', async () => {
  const bucket = await withFetch(
    {
      '/buckets/images': () => json({ bucket: {
        name: 'images', description: 'Golden images', labels: { team: 'platform' },
        latest_version: { name: 'v1', fingerprint: 'fp-complete' },
      } }),
      '/versions': () => json({ versions: [completeVersion] }),
      '/channels': () => json({ channels: [
        {
          name: 'production', updated_at: '2026-08-01T09:10:00.000Z',
          author_id: 'principal-real-author',
          version: { name: 'v1', fingerprint: 'fp-complete' },
        },
      ] }),
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-complete': () =>
        json({ relations: [] }),
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-complete': () =>
        json({ relations: [] }),
    },
    () => loadBucketPage('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  assert.equal(bucket.description, 'Golden images')
  assert.deepEqual(bucket.labels, { team: 'platform' })
  assert.deepEqual(bucket.templateTypes, ['HCL2'])
  assert.deepEqual(bucket.latestVersion, { name: 'v1', fingerprint: 'fp-complete' })
  assert.deepEqual(bucket.versions[0].assignments, [{
    channel: 'production',
    assignedAt: '2026-08-01 09:10 UTC',
    author: 'principal-real-author',
  }])
  assert.equal(bucket.channels[0].author, 'principal-real-author')
})

test('bucket status and rail counts come from the loaded newest version and facets', async () => {
  const bucket = await withFetch(
    {
      '/buckets/images': () => json({ bucket: {
        name: 'images', description: 'Golden images', platforms: ['docker'],
        latest_version: { name: 'v1', fingerprint: 'fp-complete' },
      } }),
      '/versions': () => json({ versions: [incompleteVersion, completeVersion] }),
      '/channels': () => json({ channels: [{
        name: 'latest', managed: true,
        version: { name: 'v1', fingerprint: 'fp-complete' },
      }] }),
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-incomplete': () =>
        json({ relations: [] }),
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-incomplete': () =>
        json({ relations: [] }),
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-complete': () =>
        json({ relations: [] }),
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-complete': () =>
        json({ relations: [] }),
    },
    () => loadBucketPage('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  assert.deepEqual(bucket.latestVersion, { name: 'v1', fingerprint: 'fp-complete' })
  assert.deepEqual(bucket.newestVersion, {
    name: 'v0', fingerprint: 'fp-incomplete', state: 'incomplete',
    created: '2026-07-31 11:00 UTC',
  })
  const markup = renderToStaticMarkup(React.createElement(VersionsView, {
    bucket: 'images', bucketData: bucket, loading: false, failure: null,
    onBack: () => {}, onOpenVersion: () => {},
  }))
  assert.match(markup, />Versions<\/span><span class="registry-facet-count">2<\/span>/)
  assert.match(markup, />Channels<\/span><span class="registry-facet-count">1<\/span>/)
  assert.match(markup, /aria-current="true"><span[^>]*>Overview/)
  assert.match(markup, /Bucket details[\s\S]*Newest version[\s\S]*v0[\s\S]*Status[\s\S]*incomplete/)
})

test('assignment history marks only the current row active and preserves unknown authorship', async () => {
  const history = await withFetch(
    {
      '/channels/production/history': () => json({ history: [
        {
          assigned_at: '2026-08-02T09:10:00.000Z',
          author_id: 'principal-current',
          version: {
            name: 'v2', fingerprint: 'fp-current',
            parents: { status: 'UP_TO_DATE' },
          },
        },
        {
          assigned_at: '2026-08-01T09:10:00.000Z',
          author_id: '',
          version: {
            name: 'v2', fingerprint: 'fp-current',
            parents: { status: 'UP_TO_DATE' },
          },
        },
        {
          assigned_at: '2026-07-31T09:10:00.000Z',
          author_id: 'principal-old',
          version: {
            name: 'v1', fingerprint: 'fp-old',
            parents: { status: 'OUT_OF_DATE' },
          },
        },
      ] }),
    },
    () => loadChannelHistory(
      'token', { organizationID: 'org', projectID: 'project' },
      'images', 'production', 'fp-current',
    ),
  )
  assert.deepEqual(history.map(({ status, author, parentStatus }) => ({
    status, author, parentStatus,
  })), [
    { status: 'active', author: 'principal-current', parentStatus: 'current' },
    { status: 'historical', author: null, parentStatus: 'current' },
    { status: 'historical', author: 'principal-old', parentStatus: 'out-of-date' },
  ])

  const markup = renderToStaticMarkup(React.createElement(AssignmentHistoryTable, {
    channel: 'production', history,
  }))
  assert.equal((markup.match(/>Active</g) ?? []).length, 1)
  assert.equal((markup.match(/>Historical</g) ?? []).length, 2)
  assert.match(markup, />Unknown</)
  assert.doesNotMatch(markup, /<td[^>]*data-label="Assigned by"[^>]*><\/td>/)
})

test('managed and unmanaged channels name their different managers', () => {
  const channel = (overrides) => ({
    name: 'production', versionName: 'v2', fingerprint: 'fp-current',
    managed: false, restricted: false, assignedAt: '2026-08-02 09:10 UTC',
    author: 'principal-current', ...overrides,
  })
  const markup = renderToStaticMarkup(React.createElement(BucketChannelsFacet, {
    bucket: 'images',
    channels: [
      channel({ name: 'latest', managed: true, restricted: true, author: 'Dufflebag' }),
      channel({ author: null }),
    ],
    latestVersion: { name: 'v2', fingerprint: 'fp-current' },
    onOpenVersion: () => {},
  }))
  for (const heading of ['Channel', 'Assigned version', 'Assigned by', 'Assigned time']) {
    assert.match(markup, new RegExp(`>${heading}<`))
  }
  assert.match(markup, /Dufflebag, on version completion/)
  assert.match(markup, /hcp_packer_channel_assignment/)
  assert.match(markup, />Unknown</)
})

test('channel gap names the newest comparison and says current plainly', () => {
  const newest = { name: 'v5', fingerprint: 'fp-5' }
  assert.equal(
    channelVersionGap({ versionName: 'v5', fingerprint: 'fp-5' }, newest),
    'This channel points to the newest version in the bucket (v5).',
  )
  assert.equal(
    channelVersionGap({ versionName: 'v3', fingerprint: 'fp-3' }, newest),
    '2 complete versions behind the newest version (v5).',
  )
})

test('version detail renders persisted parent links and Terraform from real identifiers', async () => {
  const detail = await withFetch(
    {
      '/versions/fp-complete': () => json({ version: completeVersion }),
      // The specific bucket routes must precede the generic '/channels' one:
      // withFetch matches by suffix in insertion order.
      '/buckets/base-images/channels': () => json({ channels: [
        { name: 'stable', version: { name: 'v7', fingerprint: 'parent-fingerprint' } },
        { name: 'staging', version: { name: 'v7', fingerprint: 'parent-fingerprint' } },
        { name: 'retired', version: { name: 'v2', fingerprint: 'unrelated' } },
      ] }),
      '/buckets/derived-images/channels': () => json({ channels: [
        { name: 'canary', version: { name: 'v3', fingerprint: 'child-fingerprint' } },
      ] }),
      '/channels': () => json({ channels: [
        {
          name: 'latest', managed: true,
          version: { name: 'v1', fingerprint: 'fp-complete' },
        },
        {
          name: 'production', managed: false,
          version: { name: 'v0', fingerprint: 'fp-previous' },
        },
      ] }),
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-complete': () => json({
        relations: [{
          status: 'OUT_OF_DATE',
          parent: {
            bucket_name: 'base-images', version_name: 'v7',
            version_fingerprint: 'parent-fingerprint', channel_name: 'production',
            channel_version: { name: 'v8', fingerprint: 'new-parent' },
          },
          child: { bucket_name: 'images', version_name: 'v1', version_fingerprint: 'fp-complete' },
        }],
      }),
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-complete': () => json({
        relations: [{
          parent: {
            bucket_name: 'images', version_name: 'v1',
            version_fingerprint: 'fp-complete',
          },
          child: {
            bucket_name: 'derived-images', version_name: 'v3',
            version_fingerprint: 'child-fingerprint',
          },
        }],
      }),
      '/packages?pagination.page_size=100': () => json({ packages: [], pagination: {} }),
    },
    () => loadVersionDetail(
      'token', { organizationID: 'org', projectID: 'project' }, 'images', 'fp-complete',
    ),
  )

  const markup = renderToStaticMarkup(React.createElement(VersionView, {
    bucket: 'images', detail, loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {},
  }))
  // The lineage card is the design's two-column ancestry shape (duf-dus4):
  // "bucket vN" links with the related version's current channels beneath —
  // no fingerprints, which live on the linked version's own screen.
  assert.match(markup, /Lineage/)
  assert.match(markup, /Parents/)
  assert.match(markup, /Children/)
  assert.match(markup, /base-images v7/)
  assert.match(markup, /stable, staging/)
  assert.match(markup, /derived-images v3/)
  assert.match(markup, /canary/)
  assert.doesNotMatch(markup, /retired/)
  assert.doesNotMatch(markup, /parent-fingerprint|child-fingerprint/)
  assert.doesNotMatch(markup, /resolved through/)
  assert.doesNotMatch(markup, /Descendants|Revoking/)
  assert.deepEqual(detail.version.parents[0].freshness, {
    status: 'behind', currentVersion: 'v8',
  })
  assert.deepEqual(detail.version.parents[0].channels, ['stable', 'staging'])
  assert.deepEqual(detail.version.children[0].channels, ['canary'])
  assert.equal(parentFreshnessText({ status: 'newest' }), 'newest')
  assert.equal(
    parentFreshnessText({ status: 'behind', currentVersion: 'v24' }),
    'bucket now at v24',
  )

  // An empty side is stated, never omitted: both columns read "None." when a
  // version has no recorded relations.
  const emptyLineage = renderToStaticMarkup(React.createElement(VersionView, {
    bucket: 'images',
    detail: { ...detail, version: { ...detail.version, parents: [], children: [] } },
    loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {},
  }))
  assert.equal(emptyLineage.split('None.').length - 1, 2)

  // The copy control must not re-render the snippet: each Terraform block
  // appears exactly once in the card (duf-yxa — ClipboardCopy rendered its
  // children as a second, unformatted copy of the code).
  assert.equal(markup.split('hcp_packer_version').length - 1, 1)
  assert.equal(markup.split('hcp_packer_artifact').length - 1, 1)

  const consume = terraformConsumeSnippet('images', detail.version)
  assert.match(consume, /data "hcp_packer_version" "images"/)
  assert.match(consume, /channel_name = "latest"/)
  assert.match(consume, /data "hcp_packer_artifact" "images"/)
  assert.match(consume, /version_fingerprint = "fp-complete"/)
  assert.match(consume, /platform            = "docker"/)
  assert.match(consume, /region              = "us-east-1"/)

  const promotion = terraformPromotionSnippet('images', 'production', 'fp-complete')
  assert.match(promotion, /resource "hcp_packer_channel_assignment" "production"/)
  assert.match(promotion, /bucket_name         = "images"/)
  assert.match(promotion, /channel_name        = "production"/)
  assert.match(promotion, /version_fingerprint = "fp-complete"/)
  assert.doesNotMatch(promotion, /latest/)
})

test('a versions failure is visible instead of empty successful data', () => {
  const markup = renderToStaticMarkup(React.createElement(VersionsView, {
    bucket: 'images',
    versions: [],
    loading: false,
    failure: '500 from /packer/buckets/images/versions',
    onBack: () => {},
    onOpenVersion: () => {},
  }))
  assert.match(markup, /Versions could not be loaded/)
  assert.match(markup, /500 from \/packer\/buckets\/images\/versions/)
  assert.doesNotMatch(markup, /No versions in this bucket/)

  const detail = renderToStaticMarkup(React.createElement(VersionView, {
    bucket: 'images',
    version: null,
    loading: false,
    failure: 'version not found',
    onBackToRegistry: () => {},
    onBackToBucket: () => {},
  }))
  assert.match(detail, /Version could not be loaded/)
  assert.match(detail, /version not found/)
})

test('a nested channels failure rejects the whole load', async () => {
  await withFetch(
    {
      '/versions': () => json({ versions: [completeVersion] }),
      '/channels': () => json({}, 500),
    },
    () =>
      assert.rejects(
        loadVersions('token', { organizationID: 'org', projectID: 'project' }, 'images'),
        /500 from .*\/channels/,
      ),
  )
})

test('the list windows old versions instead of hiding them', () => {
  const versions = Array.from({ length: 33 }, (_, index) => ({
    name: `v${33 - index}`,
    fingerprint: `fp-${33 - index}`,
    state: 'complete',
    templateType: 'HCL2',
    channels: [],
    assignments: [],
    builds: [],
    parents: [],
    children: [],
    created: '2026-07-31 10:00 UTC',
  }))
  const markup = listMarkup(versions)
  assert.match(markup, />v33</)
  assert.match(markup, />v4</)
  assert.doesNotMatch(markup, />v3</)
  assert.match(markup, /3 older versions · show all/)
})

test('empty and gap states are distinct, and write affordances are absent', async () => {
  const emptyMarkup = listMarkup([])
  assert.match(emptyMarkup, /No versions in this bucket/)

  const gap = platformTenancyGap({
    platform: true, organizationCount: 1,
    selectedOrganization: null, projectCount: 0, selectedProject: null,
  })
  const gapMarkup = listMarkup([], { gap })
  assert.match(gapMarkup, /Choose an organisation/)
  assert.doesNotMatch(gapMarkup, /No versions in this bucket/)

  // Read-only per ADR-0012: promotion, assignment and deletion belong to
  // Terraform and Packer, not these screens.
  const version = await withFetch(
    {
      '/versions/fp-complete': () => json({ version: completeVersion }),
      // renderChannel (internal/compat/hcp2023/handler.go) nests the
      // assignment as a full version object — there is no flat
      // version_fingerprint on the wire, and a fixture carrying one passes
      // against a shape the producer can never emit.
      '/channels': () => json({ channels: [
        { name: 'production', version: { name: 'v1', fingerprint: 'fp-complete' } },
      ] }),
    },
    () => loadVersion('token', { organizationID: 'org', projectID: 'project' }, 'images', 'fp-complete'),
  )
  // The association actually projects and renders — the channel pill names
  // production on both screens — so a read-only page is still an informative
  // one, not merely one with the write words missing.
  assert.deepEqual(version.channels, ['production'])
  for (const markup of [listMarkup([version]), detailMarkup(version)]) {
    assert.match(markup, />production</)
    for (const unsupported of [
      'Promote', 'Assign', 'Delete', 'Revoke', 'Create version', 'Schedule',
    ]) {
      assert.doesNotMatch(markup, new RegExp(unsupported))
    }
  }
})

test('the bucket ancestry card aggregates every version and names the local version', async () => {
  const newestComplete = { ...completeVersion, fingerprint: 'fp-new', name: 'v2' }
  const olderComplete = { ...completeVersion, fingerprint: 'fp-old', name: 'v1' }
  const bucket = await withFetch(
    {
      '/buckets/images': () => json({ bucket: {
        name: 'images', latest_version: { name: 'v2', fingerprint: 'fp-new' },
      } }),
      '/versions': () => json({ versions: [newestComplete, olderComplete] }),
      '/channels': () => json({ channels: [] }),
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-new': () =>
        json({ relations: [] }),
      // The newest version has a child of its own — its note must say (latest).
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-new': () =>
        json({ relations: [{
          parent: { bucket_name: 'images', version_name: 'v2', version_fingerprint: 'fp-new' },
          child: { bucket_name: 'derived', version_name: 'v9', version_fingerprint: 'fp-nine' },
        }] }),
      // The OLDER version carries the parent link the newest-scoped bucket
      // listing hides (duf-okej.11) — and a duplicate edge from a second build
      // collapses to one entry.
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-old': () =>
        json({ relations: [{
          status: 'UP_TO_DATE',
          parent: {
            bucket_name: 'base-images', version_name: 'v7',
            version_fingerprint: 'fp-base', channel_name: 'production',
            channel_version: { name: 'v7', fingerprint: 'fp-base' },
          },
          child: { bucket_name: 'images', version_name: 'v1', version_fingerprint: 'fp-old' },
        }] }),
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-old': () =>
        json({ relations: [
          {
            parent: { bucket_name: 'images', version_name: 'v1', version_fingerprint: 'fp-old' },
            child: { bucket_name: 'derived', version_name: 'v3', version_fingerprint: 'fp-child' },
          },
          {
            parent: { bucket_name: 'images', version_name: 'v1', version_fingerprint: 'fp-old' },
            child: { bucket_name: 'derived', version_name: 'v3', version_fingerprint: 'fp-child' },
          },
        ] }),
    },
    () => loadBucketPage('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  assert.deepEqual(
    bucket.parents.map(({ bucket: name, versionName, localVersionName }) =>
      ({ name, versionName, localVersionName })),
    [{ name: 'base-images', versionName: 'v7', localVersionName: 'v1' }],
  )
  assert.deepEqual(
    bucket.children.map(({ bucket: name, versionName, localVersionName }) =>
      ({ name, versionName, localVersionName })),
    [
      { name: 'derived', versionName: 'v9', localVersionName: 'v2' },
      { name: 'derived', versionName: 'v3', localVersionName: 'v1' },
    ],
  )

  const markup = renderToStaticMarkup(React.createElement(VersionsView, {
    bucket: 'images', bucketData: bucket, loading: false, failure: null,
    onBack: () => {}, onOpenVersion: () => {},
  }))
  assert.match(markup, /base-images v7/)
  assert.match(markup, /parent of v1/)
  assert.match(markup, /built from v2 \(latest\)/)
  assert.match(markup, /built from v1/)
  assert.doesNotMatch(markup, /parent of v1 \(latest\)/)
})
