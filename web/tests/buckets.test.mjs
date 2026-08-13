import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let BucketsView
let pinBucketAction
let deleteBucketAction
let DeleteBucketModalView
let TypedConfirmModalView
let loadBuckets
let toggleBucketPin
let deleteBucket
let ApiError

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ BucketsView, pinBucketAction, deleteBucketAction } =
    await vite.ssrLoadModule('/src/screens/Buckets.tsx'))
  ;({ DeleteBucketModalView } =
    await vite.ssrLoadModule('/src/components/DeleteBucketModal.tsx'))
  ;({ TypedConfirmModalView } =
    await vite.ssrLoadModule('/src/components/TypedConfirmModal.tsx'))
  ;({ loadBuckets, toggleBucketPin } = await vite.ssrLoadModule('/src/data/buckets.ts'))
  ;({ deleteBucket, ApiError } = await vite.ssrLoadModule('/src/api/client.ts'))
})

after(async () => {
  await vite.close()
})

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

const json = (body) =>
  new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })

const findElement = (node, predicate) => {
  if (Array.isArray(node)) {
    for (const child of node) {
      const found = findElement(child, predicate)
      if (found) return found
    }
    return null
  }
  if (!React.isValidElement(node)) return null
  if (predicate(node)) return node
  return findElement(node.props.children, predicate)
}

const renderTyped = (element) => renderToStaticMarkup(React.createElement(
  TypedConfirmModalView,
  { ...element.props, confirmation: '', onConfirmationChange: () => {} },
))

// Fixtures follow the server's rendering (renderVersion/renderBucket in
// internal/compat/hcp2023/handler.go): a version carries has_descendants and a
// parents {href,status} summary; a bucket's own parents/children links follow
// its NEWEST version only (duf-okej.3).
test('ancestry carried only by older versions renders as its own state, not as none', async () => {
  const buckets = await withFetch(
    {
      '/buckets': () => json({ buckets: [
        // Newest version has no ancestry; an older one does — the cell that
        // used to lie (duf-okej.11).
        { name: 'stale', created_at: '2026-08-01T10:00:00.000Z' },
        // No ancestry anywhere: the dash stays honest.
        { name: 'bare', created_at: '2026-08-01T10:00:00.000Z' },
        // The newest version carries ancestry: the status pill as before.
        {
          name: 'live', created_at: '2026-08-01T10:00:00.000Z',
          parents: { href: '/ancestry?type=ANCESTRY_TYPE_PARENTS', status: 'UP_TO_DATE' },
        },
      ] }),
      '/buckets/stale/versions': () => json({ versions: [
        { name: 'v2', fingerprint: 'stale-2', status: 'VERSION_ACTIVE', builds: [] },
        {
          name: 'v1', fingerprint: 'stale-1', status: 'VERSION_ACTIVE', builds: [],
          has_descendants: true, parents: { status: 'UP_TO_DATE' },
        },
      ] }),
      '/buckets/bare/versions': () => json({ versions: [
        { name: 'v1', fingerprint: 'bare-1', status: 'VERSION_ACTIVE', builds: [] },
      ] }),
      '/buckets/live/versions': () => json({ versions: [
        {
          name: 'v1', fingerprint: 'live-1', status: 'VERSION_ACTIVE', builds: [],
          parents: { status: 'UP_TO_DATE' },
        },
      ] }),
      '/channels': () => json({ channels: [] }),
    },
    () => loadBuckets('token', { organizationID: 'org', projectID: 'project' }),
  )

  const byName = Object.fromEntries(buckets.map((bucket) => [bucket.name, bucket]))
  assert.equal(byName.stale.parents, null)
  assert.equal(byName.stale.parentsInOlderVersions, true)
  assert.equal(byName.stale.childrenInOlderVersions, true)
  assert.equal(byName.bare.parentsInOlderVersions, false)
  assert.equal(byName.bare.childrenInOlderVersions, false)
  assert.notEqual(byName.live.parents, null)
  // The newest version carries the ancestry, so the third state must NOT fire.
  assert.equal(byName.live.parentsInOlderVersions, false)

  const markup = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets, total: buckets.length, loading: false, failure: null,
    openBucket: () => {},
  }))
  assert.equal(markup.split('>other versions<').length - 1, 2)
  assert.match(markup, />up to date</)
})

const galleryBucket = (name) => ({
  name,
  description: '',
  labels: {},
  templateTypes: ['HCL2'],
  versionCount: 1,
  newestVersion: { name: 'v1', fingerprint: `${name}-v1`, state: 'complete' },
  parents: null,
  children: null,
  parentsInOlderVersions: false,
  childrenInOlderVersions: false,
  channels: [],
  drift: { kind: 'current' },
  platforms: ['linux/amd64'],
  lastPush: '2026-08-09',
  lastPushAt: '2026-08-09T10:00:00Z',
})

test('pinned bucket gallery renders joined cards and disappears when empty', () => {
  const buckets = [galleryBucket('images'), galleryBucket('workers')]
  const pinned = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets, total: 2, loading: false, failure: null,
    pins: [{ bucket_name: 'images', pinned_at: '2026-08-09T10:00:00Z' }],
    openBucket: () => {}, canPin: true,
  }))
  assert.match(pinned, /aria-label="Pinned buckets"/)
  assert.match(pinned, />images</)
  assert.match(pinned, /Newest version:.*v1/)
  assert.match(pinned, /aria-label="Buckets"/)
  assert.match(pinned, /aria-label="Unpin images"/)

  const empty = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets, total: 2, loading: false, failure: null, pins: [],
    openBucket: () => {}, canPin: true,
  }))
  assert.doesNotMatch(empty, /aria-label="Pinned buckets"/)
})

test('the pinned card offers Unpin only with pin authority', () => {
  const buckets = [galleryBucket('images')]
  const pins = [{ bucket_name: 'images', pinned_at: '2026-08-09T10:00:00Z' }]
  const reader = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets, total: 1, loading: false, failure: null, pins,
    openBucket: () => {}, canPin: false,
  }))
  assert.match(reader, /aria-label="Pinned buckets"/)
  assert.doesNotMatch(reader, /aria-label="Unpin images"/)
})

test('reader sees the kebab pin item disabled with its builder reason', () => {
  assert.deepEqual(pinBucketAction(false), {
    disabled: true,
    label: 'Pin bucket — Requires builder',
  })
  assert.deepEqual(pinBucketAction(true), { disabled: false, label: 'Pin bucket' })
})

test('bucket-list deletion is live for publisher and disabled with a reason below publisher', () => {
  for (const role of [null, 'reader', 'builder']) {
    assert.deepEqual(deleteBucketAction(role), {
      disabled: true,
      label: 'Delete bucket… — Requires publisher',
    })
  }
  assert.deepEqual(deleteBucketAction('publisher'), {
    disabled: false,
    label: 'Delete bucket…',
  })
})

test('bucket deletion sends DELETE to the exact compat-plane path', async () => {
  const originalFetch = globalThis.fetch
  const calls = []
  globalThis.fetch = async (input, init) => {
    calls.push({ path: String(input), init })
    return new Response(null, { status: 204 })
  }
  try {
    await deleteBucket(
      'bearer',
      { organizationID: 'org one', projectID: 'project/one' },
      'images/base',
    )
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.equal(calls.length, 1)
  assert.equal(calls[0].init.method, 'DELETE')
  assert.equal(calls[0].init.headers.Authorization, 'Bearer bearer')
  assert.equal(
    calls[0].path,
    '/packer/2023-01-01/organizations/org%20one/projects/project%2Fone/buckets/images%2Fbase',
  )
  assert.equal('body' in calls[0].init, false)
})

test('bucket deletion waits for the typed confirmation and names its expected string', async () => {
  let deletes = 0
  const modal = DeleteBucketModalView({
    bucket: 'images', callerRole: 'publisher', submitting: false, failure: null,
    onConfirm: async () => { deletes++ }, onClose: () => {},
  })
  assert.equal(deletes, 0)
  assert.equal(modal.props.expected, 'images')
  await modal.props.onConfirm()
  assert.equal(deletes, 1)
})

test('bucket deletion names every consequence and renders a server error verbatim', () => {
  const message = new ApiError(409, 'bucket is protected by registry policy').message
  const markup = renderTyped(DeleteBucketModalView({
    bucket: 'images', callerRole: 'publisher', submitting: false, failure: message,
    onConfirm: async () => {}, onClose: () => {},
  }))
  assert.match(markup, /Delete images/)
  assert.match(markup, /Type <strong>images<\/strong> to confirm/)
  assert.match(
    markup,
    /Deleting images permanently removes the bucket and all its versions, builds, artifacts, channels and history\./,
  )
  assert.match(markup, /The action was refused/)
  assert.match(markup, /bucket is protected by registry policy/)
})

test('bucket deletion warns when Bag Drop mirrors the bucket, and only then', () => {
  const mirrored = renderTyped(DeleteBucketModalView({
    bucket: 'images', callerRole: 'publisher', submitting: false, failure: null,
    mirrored: true, onConfirm: async () => {}, onClose: () => {},
  }))
  assert.match(mirrored, /This bucket is mirrored by Bag Drop/)
  assert.match(mirrored, /also deletes the destination copy at the next reconcile/)

  const plain = renderTyped(DeleteBucketModalView({
    bucket: 'images', callerRole: 'publisher', submitting: false, failure: null,
    onConfirm: async () => {}, onClose: () => {},
  }))
  assert.doesNotMatch(plain, /mirrored by Bag Drop/)
})

test('kebab pin toggle drives setPin and deletePin contract calls', async () => {
  const originalFetch = globalThis.fetch
  const requests = []
  globalThis.fetch = async (path, options) => {
    requests.push([String(path), options.method])
    if (options.method === 'DELETE') return new Response(null, { status: 204 })
    return json({ bucket_name: 'images', pinned_at: '2026-08-09T10:00:00Z', pinned_by: 'builder' })
  }
  try {
    const tenant = { organizationID: 'org', projectID: 'project' }
    await toggleBucketPin('token', tenant, 'images', false)
    await toggleBucketPin('token', tenant, 'images', true)
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.deepEqual(requests, [
    ['/api/v1/organizations/org/projects/project/pins/images', 'PUT'],
    ['/api/v1/organizations/org/projects/project/pins/images', 'DELETE'],
  ])
})

test('pins failure is non-blocking and keeps the bucket table', () => {
  const markup = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets: [galleryBucket('images')], total: 1, loading: false, failure: null,
    pins: [], pinsFailure: 'pin service unavailable', openBucket: () => {}, canPin: true,
  }))
  assert.match(markup, /Pinned buckets could not be loaded/)
  assert.match(markup, /pin service unavailable/)
  assert.match(markup, /aria-label="Buckets"/)
})
