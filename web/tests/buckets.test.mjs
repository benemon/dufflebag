import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let BucketsView
let bucketComparator
let pinBucketAction
let deleteBucketAction
let DeleteBucketModalView
let TypedConfirmModalView
let loadBuckets
let toggleBucketPin
let deleteBucket
let ApiError
let BucketPickerView
let bucketPickerOptions
let CreateBucketButton
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
  ;({ BucketsView, bucketComparator, pinBucketAction, deleteBucketAction } =
    await vite.ssrLoadModule('/src/screens/Buckets.tsx'))
  ;({ DeleteBucketModalView } =
    await vite.ssrLoadModule('/src/components/DeleteBucketModal.tsx'))
  ;({ TypedConfirmModalView } =
    await vite.ssrLoadModule('/src/components/TypedConfirmModal.tsx'))
  ;({ loadBuckets, toggleBucketPin } = await vite.ssrLoadModule('/src/data/buckets.ts'))
  ;({ deleteBucket, ApiError, createBucket } = await vite.ssrLoadModule('/src/api/client.ts'))
  ;({ BucketPickerView, bucketPickerOptions } =
    await vite.ssrLoadModule('/src/shell/TenantSwitcher.tsx'))
  ;({ CreateBucketButton } = await vite.ssrLoadModule('/src/components/BucketCreation.tsx'))
  ;({ loadBucketPicker } = await vite.ssrLoadModule('/src/data/bucketPicker.ts'))
  ;({ refreshThenSelect } = await vite.ssrLoadModule('/src/components/TenancyCreation.tsx'))
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
const bucketScreenSource = readFileSync(new URL('../src/screens/Buckets.tsx', import.meta.url), 'utf8')

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
  assert.match(markup, /aria-label="Parents ancestry scope"/)
  assert.match(markup, /aria-label="Children ancestry scope"/)
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
  lastPushAt: '2026-08-09T10:00:00Z',
})

test('bucket header sorts replace the sort selector and keep newest-push first', () => {
  const buckets = [
    { ...galleryBucket('older'), lastPushAt: '2026-08-08T10:00:00Z' },
    { ...galleryBucket('newer'), lastPushAt: '2026-08-10T10:00:00Z' },
  ]
  const markup = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets, total: buckets.length, loading: false, failure: null, openBucket: () => {},
  }))
  assert.doesNotMatch(markup, /Sort buckets|Sort: name|Sort: status|Sort: last push/)
  assert.match(markup, /aria-label="Filter buckets by name"/)
  assert.match(markup, /aria-sort="descending"[\s\S]{0,600}Last updated/)
  assert.ok(markup.indexOf('>newer<') < markup.indexOf('>older<'))
})

test('status sorting follows the rendered state scale, not the alphabet', () => {
  const stateBucket = (state) => ({
    ...galleryBucket(state),
    newestVersion: { name: 'v1', fingerprint: `${state}-v1`, state },
  })
  const ordered = [
    stateBucket('revocation-scheduled'),
    stateBucket('revoked'),
    stateBucket('incomplete'),
    stateBucket('complete'),
  ].sort(bucketComparator('status'))
  assert.deepEqual(ordered.map((bucket) => bucket.newestVersion.state), [
    'complete', 'incomplete', 'revoked', 'revocation-scheduled',
  ])
})

test('platform and state filters own chips and share one complete reset', () => {
  assert.match(bucketScreenSource, /categoryName="Platform"[\s\S]{0,120}labels=\{platformFilter/)
  assert.match(bucketScreenSource, /categoryName="State"[\s\S]{0,120}labels=\{statusFilter/)
  assert.match(
    bucketScreenSource,
    /const clearAllFilters = \(\) => \{\s*setNameFilter\(''\)\s*setPlatformFilter\(''\)\s*setStatusFilter\(''\)\s*setPage\(1\)/,
  )
  assert.match(bucketScreenSource, /<Toolbar id="buckets-toolbar" clearAllFilters=\{clearAllFilters\}>/)
  assert.match(bucketScreenSource, /onClick=\{clearAllFilters\}/)
})

test('the Channels column bounds labels after three while retaining their versions', () => {
  assert.match(bucketScreenSource, /<LabelGroup[\s\S]{0,120}numLabels=\{3\}/)
  const channels = ['latest', 'production', 'staging', 'canary'].map((name, index) => ({
    name, versionName: `v${index + 1}`, fingerprint: `fp-${index + 1}`,
    managed: false, restricted: false,
  }))
  const markup = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets: [{ ...galleryBucket('images'), channels }],
    total: 1, loading: false, failure: null, openBucket: () => {},
  }))
  assert.match(markup, />Channels</)
  assert.match(markup, /aria-label="Channels for images"/)
  assert.match(markup, />latest v1</)
  assert.match(markup, />production v2</)
  assert.match(markup, />staging v3</)
  assert.match(markup, /1 more/)
  assert.doesNotMatch(markup, />canary v4</)
  const nameCell = markup.match(/<td[^>]*data-label="Bucket"[\s\S]*?<\/td>/)?.[0] ?? ''
  assert.match(nameCell, />images</)
  assert.doesNotMatch(nameCell, /latest|production|staging|canary/)
})

test('pinned bucket cards reuse version state and ancestry treatments and disappear when empty', () => {
  const buckets = [{
    ...galleryBucket('images'),
    parents: { href: '/parents', status: 'OUT_OF_DATE' },
    childrenInOlderVersions: true,
  }, galleryBucket('workers')]
  const pinned = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets, total: 2, loading: false, failure: null,
    pins: [{ bucket_name: 'images', pinned_at: '2026-08-09T10:00:00Z' }],
    openBucket: () => {}, canPin: true,
  }))
  const pinnedSection = pinned.match(/<section aria-label="Pinned buckets"[\s\S]*?<\/section>/)?.[0] ?? ''
  assert.match(pinnedSection, />images</)
  assert.match(pinnedSection, /Newest version:.*v1[\s\S]*>complete</)
  assert.match(pinnedSection, /Parents:[\s\S]*>out of date</)
  assert.match(pinnedSection, /Children:[\s\S]*>other versions</)
  assert.match(pinnedSection, /<time[^>]*dateTime="2026-08-09T10:00:00Z"/)
  assert.match(pinned, /aria-label="Buckets"/)
  assert.match(pinned, /aria-label="Unpin images"/)

  const empty = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets, total: 2, loading: false, failure: null, pins: [],
    openBucket: () => {}, canPin: true,
  }))
  assert.doesNotMatch(empty, /aria-label="Pinned buckets"/)
})

test('an empty project owns the connect affordance and never doubles it with the build hint', () => {
  const markup = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets: [], total: 0, loading: false, failure: null,
    openBucket: () => {}, openInstance: () => {},
  }))
  assert.match(markup, /class="pf-v6-c-empty-state"/)
  assert.match(markup, /<h2[^>]*>No buckets yet<\/h2>/)
  assert.match(markup, /pf-v6-c-empty-state__body">Buckets appear when Packer publishes a version/)
  assert.match(markup, /pf-v6-c-empty-state__actions">[\s\S]{0,500}Connect a client/)
  assert.match(markup, /href="https:\/\/developer\.hashicorp\.com\/packer\/docs"[^>]*target="_blank"/)
  assert.doesNotMatch(markup, /Waiting on a first build/)
})

test('the first-build hint appears only while no bucket has ever seen a build', () => {
  const render = (buckets) => renderToStaticMarkup(React.createElement(BucketsView, {
    buckets, total: buckets.length, loading: false, failure: null,
    openBucket: () => {}, openInstance: () => {},
  }))
  const waiting = render([
    { ...galleryBucket('empty'), versionCount: 0, newestVersion: null },
  ])
  assert.match(waiting, /class="pf-v6-c-hint"/)
  assert.match(waiting, /Waiting on a first build/)
  assert.match(waiting, /Open Instance/)
  assert.match(waiting, /aria-label="Dismiss client connection hint"/)

  // An incomplete v0 is proof Packer already connected — connection guidance
  // mid-build is stale advice (duf-r0j6).
  const building = render([
    { ...galleryBucket('empty'), versionCount: 0, newestVersion: null },
    {
      ...galleryBucket('building'),
      newestVersion: { name: 'v0', fingerprint: 'building-v0', state: 'incomplete' },
    },
  ])
  assert.doesNotMatch(building, /Waiting on a first build/)

  const completed = render([galleryBucket('ready')])
  assert.doesNotMatch(completed, /Waiting on a first build/)
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
    label: 'Pin bucket',
    tooltipProps: { content: 'Requires builder' },
  })
  assert.deepEqual(pinBucketAction(true), { disabled: false, label: 'Pin bucket' })
  assert.match(bucketScreenSource, /tooltipProps=\{pinAction\.tooltipProps\}/)
})

test('bucket-list deletion is live for publisher and disabled with a reason below publisher', () => {
  for (const role of [null, 'reader', 'builder']) {
    assert.deepEqual(deleteBucketAction(role), {
      disabled: true,
      label: 'Delete bucket…',
      tooltipProps: { content: 'Requires publisher' },
    })
  }
  assert.deepEqual(deleteBucketAction('publisher'), {
    disabled: false,
    label: 'Delete bucket…',
  })
  assert.match(bucketScreenSource, /tooltipProps=\{deleteAction\.tooltipProps\}/)
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

test('a failed bucket listing is stated on the screen, never an empty table', () => {
  const markup = renderToStaticMarkup(React.createElement(BucketsView, {
    buckets: [], total: 0, loading: false,
    failure: 'organisations could not be listed',
    gap: null, callerRole: 'root',
    openBucket: () => {}, openInstance: () => {},
  }))
  assert.match(markup, /Buckets could not be loaded/)
  assert.match(markup, /organisations could not be listed/)
  assert.doesNotMatch(markup, /No buckets yet/)
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

test('a carried selection offers the blank step-up row ahead of the groups', () => {
  const withSelection = bucketPickerOptions(
    [{ name: 'base-images' }, { name: 'database-images' }],
    [{ bucket_name: 'database-images' }],
    true,
  )
  assert.deepEqual(withSelection[0], { value: '', label: '\u2014' })
  assert.equal(withSelection.filter((option) => option.value === '').length, 1)

  const withoutSelection = bucketPickerOptions(
    [{ name: 'base-images' }], [], false,
  )
  assert.ok(!withoutSelection.some((option) => option.value === ''))
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

test('a bucket-scoped session sees Create bucket refused by scope, not by role', () => {
  // The server refuses bucket creation by scope whatever the role (creating a
  // bucket changes the set of buckets rather than acting inside this one), so
  // the affordance says so instead of silently no-opping (duf-3p03). The
  // scope reason wins over the role reason: "Requires builder" would send a
  // maintainer chasing a role it already holds. SSR renders the Select closed,
  // so the trigger is asserted directly and the wiring at source level.
  const refused = renderToStaticMarkup(React.createElement(CreateBucketButton, {
    callerRole: 'maintainer',
    refusal: 'A bucket-scoped session cannot create buckets',
    onOpen: () => {},
  }))
  assert.match(refused, /A bucket-scoped session cannot create buckets/)
  assert.match(refused, /<button[^>]*\bdisabled\b/)
  assert.doesNotMatch(refused, /Requires builder/)
  // Above bucket scope the affordance stays live for builder and up.
  const live = renderToStaticMarkup(React.createElement(CreateBucketButton, {
    callerRole: 'maintainer', onOpen: () => {},
  }))
  assert.doesNotMatch(live, /<button[^>]*\bdisabled\b/)
  // The picker derives the refusal from the session's scope, nothing else.
  const switcherSource = readFileSync(
    new URL('../src/shell/TenantSwitcher.tsx', import.meta.url), 'utf8',
  )
  assert.match(
    switcherSource,
    /bucketScoped \? 'A bucket-scoped session cannot create buckets' : null/,
  )
  assert.match(switcherSource, /bucketScoped=\{state\?\.claims\.bucketID != null\}/)
})

test('the create modal is owned by the picker, not the vanishing footer', () => {
  // Any click into the portaled modal closes the Select, which unmounts its
  // footer subtree; a modal owned by the footer vanished mid-submit and its
  // failure state — the server's refusal — with it (duf-3p03). The picker view
  // owns the modal state and renders the modal beside the Select; the footer
  // carries only the trigger.
  const switcherSource = readFileSync(
    new URL('../src/shell/TenantSwitcher.tsx', import.meta.url), 'utf8',
  )
  assert.match(switcherSource, /const \[creating, setCreating\] = useState\(false\)/)
  // Every listing state renders through the ONE return that carries the
  // modal: a refresh failing mid-create must swap the picker body, never
  // tear down the operator's half-typed form.
  assert.match(switcherSource, /\{body\}\s*\{modal\}\s*<\/PickerField>/)
  assert.equal(
    (switcherSource.match(/<PickerField label="Bucket">/g) ?? []).length, 1,
    'BucketPickerView must have exactly one Bucket PickerField return',
  )
  // The trigger component itself renders no modal: its footer placement would
  // couple the modal's lifetime to the picker's open state.
  const creationSource = readFileSync(
    new URL('../src/components/BucketCreation.tsx', import.meta.url), 'utf8',
  )
  assert.doesNotMatch(creationSource, /<BucketModal /)
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

