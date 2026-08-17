import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let RegistryView
let BucketPickerView
let bucketPickerOptions
let loadBucketPicker
let createBucket
let refreshThenSelect

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ RegistryView } = await vite.ssrLoadModule('/src/screens/Registry.tsx'))
  ;({ BucketPickerView, bucketPickerOptions } =
    await vite.ssrLoadModule('/src/shell/TenantSwitcher.tsx'))
  ;({ loadBucketPicker } = await vite.ssrLoadModule('/src/data/bucketPicker.ts'))
  ;({ createBucket } = await vite.ssrLoadModule('/src/api/client.ts'))
  ;({ refreshThenSelect } = await vite.ssrLoadModule('/src/components/TenancyCreation.tsx'))
})

after(async () => { await vite.close() })

const json = (body) => new Response(JSON.stringify(body), {
  status: 200,
  headers: { 'Content-Type': 'application/json' },
})

test('the registry landing points bucket choice and creation at the masthead', () => {
  const markup = renderToStaticMarkup(React.createElement(RegistryView, {
    onConnectClient: () => {},
  }))
  assert.match(markup, /<h2[^>]*>Choose a bucket<\/h2>/)
  assert.match(markup, /Pick a bucket from the masthead picker, or create one there\./)
  assert.match(markup, />Connect a client</)
  assert.match(markup, />Packer docs</)
  assert.doesNotMatch(markup, /All buckets|Pinned buckets|Loading buckets/)
})

const pickerMarkup = (over = {}) => renderToStaticMarkup(React.createElement(BucketPickerView, {
  selectedBucket: undefined,
  buckets: [{ name: 'base-images' }, { name: 'database-images' }, { name: 'tools' }],
  pins: [{ bucket_name: 'database-images' }],
  scoped: true,
  loading: false,
  failure: null,
  callerRole: 'builder',
  onRefresh: async () => [],
  onSelect: () => {},
  onCreate: async () => {},
  ...over,
}))

test('the bucket picker shows the route absence as an em dash', () => {
  const markup = pickerMarkup()
  assert.match(markup, /id="tenant-bucket"/)
  assert.match(markup, /value="—"/)
})

test('pinned buckets form the first select group and the rest follow', () => {
  assert.deepEqual(bucketPickerOptions(
    [{ name: 'base-images' }, { name: 'database-images' }, { name: 'tools' }],
    [{ bucket_name: 'database-images' }],
  ), [
    { value: 'database-images', label: 'database-images', group: 'Pinned' },
    { value: 'base-images', label: 'base-images', group: 'Buckets' },
    { value: 'tools', label: 'tools', group: 'Buckets' },
  ])
})

test('picker loading, failure and empty listings are text states, never empty menus', () => {
  const loading = pickerMarkup({ loading: true })
  assert.match(loading, /Loading buckets…/)
  assert.doesNotMatch(loading, /tenant-bucket/)

  const failed = pickerMarkup({ failure: 'offline' })
  assert.match(failed, /Buckets could not be loaded/)
  assert.doesNotMatch(failed, /tenant-bucket/)

  const empty = pickerMarkup({ buckets: [], pins: [] })
  assert.match(empty, /No buckets exist/)
  assert.match(empty, /Create bucket/)
  assert.doesNotMatch(empty, /tenant-bucket/)
})

test('the picker listing makes no per-bucket fan-out requests', async () => {
  const originalFetch = globalThis.fetch
  const calls = []
  globalThis.fetch = async (input) => {
    const path = String(input)
    calls.push(path)
    if (path.endsWith('/buckets')) return json({ buckets: [
      { name: 'base-images' }, { name: 'database-images' },
    ] })
    if (path.endsWith('/pins')) return json({ pins: [
      { bucket_name: 'database-images', pinned_at: '2026-08-01T00:00:00Z' },
    ] })
    throw new Error(`unexpected request ${path}`)
  }
  try {
    const listed = await loadBucketPicker(
      'token',
      { organizationID: 'org', projectID: 'project' },
    )
    assert.deepEqual(listed.buckets.map((bucket) => bucket.name), ['base-images', 'database-images'])
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.equal(calls.length, 2)
  assert.equal(calls.filter((path) => path.endsWith('/buckets')).length, 1)
  assert.equal(calls.filter((path) => path.endsWith('/pins')).length, 1)
  assert.equal(calls.filter((path) => /\/buckets\/[^/]+\/(versions|channels)$/.test(path)).length, 0)
})

test('bucket creation uses the compatibility client request shape', async () => {
  const originalFetch = globalThis.fetch
  let call
  globalThis.fetch = async (input, init) => {
    call = { path: String(input), init }
    return json({ bucket: { name: 'new images' } })
  }
  try {
    const created = await createBucket(
      'bearer',
      { organizationID: 'org one', projectID: 'project/one' },
      'new images',
    )
    assert.equal(created.name, 'new images')
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.equal(call.init.method, 'PUT')
  assert.equal(call.init.headers.Authorization, 'Bearer bearer')
  assert.equal(
    call.path,
    '/packer/2023-01-01/organizations/org%20one/projects/project%2Fone/buckets',
  )
  assert.deepEqual(JSON.parse(call.init.body), { name: 'new images' })
})

test('a created bucket is selected only after the refreshed listing contains it', async () => {
  const events = []
  const created = { name: 'new-images' }
  await refreshThenSelect(
    created,
    async () => {
      events.push('refresh')
      return [created]
    },
    (bucket) => events.push(`select:${bucket.name}`),
    (bucket) => bucket.name,
  )
  assert.deepEqual(events, ['refresh', 'select:new-images'])

  await assert.rejects(
    refreshThenSelect(created, async () => [], () => assert.fail(), (bucket) => bucket.name),
    /listing could not be refreshed/,
  )
})
