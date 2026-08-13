import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let BucketsView
let loadBuckets
let selectTenantProject
let ApiError
let signOutIfUnauthorized
let ProjectLoadFailure
let SignOutButton

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ BucketsView } = await vite.ssrLoadModule('/src/screens/Buckets.tsx'))
  ;({ loadBuckets } = await vite.ssrLoadModule('/src/data/buckets.ts'))
  ;({ selectTenantProject } = await vite.ssrLoadModule('/src/data/tenant.ts'))
  ;({ ApiError, signOutIfUnauthorized } = await vite.ssrLoadModule('/src/api/client.ts'))
  ;({ ProjectLoadFailure } = await vite.ssrLoadModule('/src/App.tsx'))
  ;({ SignOutButton } = await vite.ssrLoadModule('/src/shell/AppShell.tsx'))
})

after(async () => {
  await vite.close()
})

test('bucket and channel failures are visible instead of empty successful data', () => {
  const bucketMarkup = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets: [],
    total: 0,
    loading: false,
    failure: '500 from /packer/buckets',
  }))
  assert.match(bucketMarkup, /Buckets could not be loaded/)
  assert.match(bucketMarkup, /500 from \/packer\/buckets/)
  assert.doesNotMatch(bucketMarkup, /No buckets yet/)

  const projectMarkup = renderToStaticMarkup(React.createElement(ProjectLoadFailure, {
    failure: '503 from /resource-manager/projects',
  }))
  assert.match(projectMarkup, /Projects could not be loaded/)
  assert.match(projectMarkup, /503 from \/resource-manager\/projects/)
})

test('nested version failure rejects the whole bucket load', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async (input) => {
    const path = String(input)
    if (path.endsWith('/buckets')) return json({ buckets: [{ name: 'images' }] })
    if (path.endsWith('/versions')) return json({}, 500)
    if (path.endsWith('/channels')) return json({ channels: [] })
    throw new Error(`unexpected request ${path}`)
  }
  try {
    await assert.rejects(
      loadBuckets('token', { organizationID: 'org', projectID: 'project' }),
      /500 from .*\/versions/,
    )
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('top-level failure rejects rather than becoming an empty registry', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async () => json({}, 403)
  try {
    await assert.rejects(
      loadBuckets('token', { organizationID: 'org', projectID: 'project' }),
      /403 from .*\/buckets/,
    )
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('each channel gets its own drift state', async () => {
  const originalFetch = globalThis.fetch
  // Channel fixtures derive from the producer: renderChannel in
  // internal/compat/hcp2023/handler.go nests the assignment as a full
  // `version` object and marks the server-maintained "latest" with
  // managed:true — no flat version_fingerprint field exists on the wire
  // (models/hashicorp_cloud_packer20230101_channel.go, Appendix A A.2).
  // Version fixtures likewise: renderVersion always emits `status`, and a
  // completed version carries VERSION_ACTIVE — the field drift measures by.
  globalThis.fetch = async (input) => {
    const path = String(input)
    if (path.endsWith('/buckets')) return json({ buckets: [{
      name: 'images', description: 'Golden base images', labels: { team: 'platform' },
      platforms: ['docker'],
      parents: { href: '/ancestry?type=ANCESTRY_TYPE_PARENTS', status: 'OUT_OF_DATE' },
      children: { href: '/ancestry?type=ANCESTRY_TYPE_CHILDREN', status: 'UP_TO_DATE' },
    }] })
    if (path.endsWith('/versions')) {
      return json({ versions: [
        { name: 'v2', fingerprint: 'new', status: 'VERSION_ACTIVE', template_type: 'HCL2', created_at: '2026-07-31T00:00:00Z' },
        { name: 'v1', fingerprint: 'old', status: 'VERSION_ACTIVE', template_type: 'HCL2', created_at: '2026-07-30T00:00:00Z' },
      ] })
    }
    if (path.endsWith('/channels')) {
      return json({ channels: [
        { name: 'latest', version: { name: 'v2', fingerprint: 'new' }, managed: true, restricted: true },
        { name: 'production', version: { name: 'v2', fingerprint: 'new' } },
        { name: 'staging', version: { name: 'v1', fingerprint: 'old' } },
      ] })
    }
    throw new Error(`unexpected request ${path}`)
  }
  try {
    const [bucket] = await loadBuckets('token', { organizationID: 'org', projectID: 'project' })
    assert.deepEqual(bucket.channels.map(({ name, managed, drift }) => ({ name, managed, drift })), [
      { name: 'latest', managed: true, drift: { kind: 'current' } },
      { name: 'production', managed: false, drift: { kind: 'current' } },
      { name: 'staging', managed: false, drift: { kind: 'behind', versions: 1 } },
    ])
    assert.deepEqual(bucket.parents, {
      href: '/ancestry?type=ANCESTRY_TYPE_PARENTS', status: 'OUT_OF_DATE',
    })
    assert.deepEqual(bucket.children, {
      href: '/ancestry?type=ANCESTRY_TYPE_CHILDREN', status: 'UP_TO_DATE',
    })
    const markup = renderToStaticMarkup(React.createElement(BucketsView, {
      buckets: [bucket], total: 1, loading: false, failure: null, openBucket: () => {},
    }))
    assert.match(markup, />out of date</)
    assert.match(markup, />up to date</)
    assert.match(markup, /Golden base images/)
    assert.match(markup, /team=platform/)
    assert.match(markup, />HCL2</)
    assert.match(markup, />restricted</)
    assert.match(markup, />newest</)
  } finally {
    globalThis.fetch = originalFetch
  }
})

// The adversarial-review scenario, verbatim: ListVersions orders sequence
// DESC and PostgreSQL sorts an incomplete version's NULL sequence FIRST, so
// versions[0] is the unnumbered v0 — not the version latest holds. Drift must
// measure against the newest COMPLETE version (renderVersion's status rule,
// isComplete in versions.ts), or a bucket whose latest correctly holds the
// newest complete version reports behind while a build is merely in flight.
test('an incomplete v0 at the head of the listing does not put latest behind', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async (input) => {
    const path = String(input)
    if (path.endsWith('/buckets')) return json({ buckets: [{ name: 'images' }] })
    if (path.endsWith('/versions')) {
      return json({ versions: [
        { name: 'v0', fingerprint: 'fp-wip', status: 'VERSION_RUNNING', created_at: '2026-07-31T11:00:00Z' },
        { name: 'v1', fingerprint: 'fp-done', status: 'VERSION_ACTIVE', created_at: '2026-07-31T10:00:00Z' },
      ] })
    }
    if (path.endsWith('/channels')) {
      return json({ channels: [
        { name: 'latest', version: { name: 'v1', fingerprint: 'fp-done' }, managed: true, restricted: true },
      ] })
    }
    throw new Error(`unexpected request ${path}`)
  }
  try {
    const [bucket] = await loadBuckets('token', { organizationID: 'org', projectID: 'project' })
    assert.deepEqual(bucket.drift, { kind: 'current' })
    assert.deepEqual(bucket.newestVersion, {
      name: 'v0', fingerprint: 'fp-wip', state: 'incomplete',
    })
    assert.deepEqual(bucket.channels.map(({ name, drift }) => ({ name, drift })), [
      { name: 'latest', drift: { kind: 'current' } },
    ])
  } finally {
    globalThis.fetch = originalFetch
  }
})


test('empty results are distinct and unsupported actions are absent', () => {
  const emptyMarkup = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets: [],
    total: 0,
    loading: false,
    failure: null,
  }))
  assert.match(emptyMarkup, /<h2[^>]*>No buckets yet<\/h2>/)
  assert.match(emptyMarkup, /class="pf-v6-c-empty-state__body"/)
  assert.match(emptyMarkup, /Connect a client/)

  const buckets = Array.from({ length: 21 }, (_, index) => ({
    name: `bucket-${String(index + 1).padStart(2, '0')}`,
    description: '',
    labels: {},
    templateTypes: [],
    versionCount: 0,
    channels: [],
    drift: { kind: 'absent', channel: 'channels' },
    platforms: [],
    lastPushAt: '',
  }))
  const firstPage = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets,
    total: buckets.length,
    loading: false,
    failure: null,
  }))
  // The bucket name now renders inside the drill-down button (duf-424), so
  // the pagination boundary is asserted on the text itself.
  assert.match(firstPage, />bucket-20</)
  assert.doesNotMatch(firstPage, />bucket-21</)
  assert.match(firstPage, /Filter buckets by name/)
  assert.match(firstPage, /Sort: status/)
  assert.match(firstPage, /Actions for bucket-01/)
  for (const unsupported of [
    'Pin to project', 'Copy Terraform reference', 'View ancestry', 'View SBOMs',
  ]) {
    assert.doesNotMatch(firstPage, new RegExp(unsupported))
  }
})

test('buckets default to full-timestamp newest-first order and render semantic timestamps', () => {
  const buckets = [
    { name: 'alpha', lastPushAt: '2026-08-05T09:00:00Z' },
    { name: 'zulu', lastPushAt: '2026-08-05T18:00:00Z' },
  ].map((bucket) => ({
    ...bucket,
    description: '', labels: {}, templateTypes: [], versionCount: 0, newestVersion: null,
    parents: null, children: null, channels: [], drift: { kind: 'absent', channel: 'channels' },
    platforms: [],
  }))
  const markup = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets, total: buckets.length, loading: false, failure: null,
  }))

  assert.ok(markup.indexOf('>zulu<') < markup.indexOf('>alpha<'), markup)
  assert.match(markup, /<time[^>]*dateTime="2026-08-05T09:00:00Z"/)
  assert.match(markup, /<time[^>]*dateTime="2026-08-05T18:00:00Z"/)
})

test('tenant selection passes the project UUID, not its display name', () => {
  let selected = ''
  selectTenantProject({
    id: 'org/project-id',
    organization: 'org',
    projectID: 'project-id',
    project: 'friendly project name',
  }, (projectID) => {
    selected = projectID
  })
  assert.equal(selected, 'project-id')
})

test('a 401 triggers the shared sign-out path', () => {
  let signedOut = 0
  assert.equal(signOutIfUnauthorized(new ApiError(401, 'expired'), () => {
    signedOut++
  }), true)
  assert.equal(signedOut, 1)

  assert.equal(signOutIfUnauthorized(new ApiError(500, 'failed'), () => {
    signedOut++
  }), false)
  assert.equal(signedOut, 1)
})

test('the visible sign-out control invokes sign out', () => {
  let signedOut = 0
  const button = SignOutButton({ signOut: () => {
    signedOut++
  } })
  assert.equal(button.props.children, 'Sign out')
  button.props.onClick()
  assert.equal(signedOut, 1)
})

function json(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}
