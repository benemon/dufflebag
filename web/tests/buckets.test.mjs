import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let BucketsView
let loadBuckets

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
