import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let InstanceView, BuildCard, ScannerCard, clientEnvironment, shellValue

before(async () => {
  vite = await createServer({
    root: process.cwd(), logLevel: 'silent',
    server: { middlewareMode: true }, appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ InstanceView, BuildCard, ScannerCard, clientEnvironment, shellValue } =
    await vite.ssrLoadModule('/src/screens/Instance.tsx'))
})

after(async () => { await vite.close() })

const view = (over = {}) => renderToStaticMarkup(React.createElement(InstanceView, {
  host: 'dufflebag.example.com:8443', secure: true,
  organizationID: 'org-1', projectID: 'proj-1',
  instance: {
    version: '1.2.3', commit: 'abc123',
    api_versions: ['/packer/2023-01-01', '/resource-manager/2019-12-10', '/api/v1'],
    initialized_at: '2026-08-05T09:00:00Z', store: true,
    object_storage: 'ok', encryption: 'degraded', audit: 'enabled',
    scanner: { configured: true, adapter: 'osv' },
  },
  loading: false, failure: null, ...over,
}))

// HCP_API_ADDRESS is a host, HCP_AUTH_URL is a URL that must be https. Emitting
// them the other way round produces a block that silently does not work.
test('the environment block distinguishes the host from the URL', () => {
  const env = clientEnvironment({
    host: 'example:8443', organizationID: 'org-1', projectID: 'proj-1',
  })
  assert.match(env, /HCP_API_ADDRESS=example:8443/)
  assert.doesNotMatch(env, /HCP_API_ADDRESS=https/)
  assert.match(env, /HCP_AUTH_URL=https:\/\/example:8443/)
})

// An organization-scoped principal has no project. Emitting one would point the
// client at a tenancy it may not be entitled to.
test('an identifier the console does not have is omitted, not invented', () => {
  const env = clientEnvironment({ host: 'h', organizationID: 'org-1', projectID: null })
  assert.match(env, /HCP_ORGANIZATION_ID=org-1/)
  assert.doesNotMatch(env, /HCP_PROJECT_ID/)
  // The credential is genuinely unknown here and is marked as a placeholder.
  assert.match(env, /HCP_CLIENT_SECRET=<client secret>/)
})

// A bucket-scoped session can publish into exactly one bucket, and Packer reads
// the fallback bucket name from HCP_PACKER_BUCKET_NAME (packer v1.16.0
// internal/hcp/env/variables.go, HCPPackerBucket). Wider tenancies never emit
// it: pinning a client to a bucket the credential is not bound to invents a
// constraint the server does not hold.
test('a bucket-scoped session environment names its bucket; others never do', () => {
  const scoped = clientEnvironment({
    host: 'h', organizationID: 'org-1', projectID: 'proj-1', bucketName: 'base-images',
  })
  assert.match(scoped, /export HCP_PACKER_BUCKET_NAME=base-images/)
  const above = clientEnvironment({ host: 'h', organizationID: 'org-1', projectID: 'proj-1' })
  assert.doesNotMatch(above, /HCP_PACKER_BUCKET_NAME/)
  // Every other session shape is byte-identical to the block before the row
  // existed.
  assert.equal(above, clientEnvironment({
    host: 'h', organizationID: 'org-1', projectID: 'proj-1', bucketName: null,
  }))
  // The rendered screen pins both shapes.
  assert.match(view({ bucketName: 'base-images' }), /HCP_PACKER_BUCKET_NAME=base-images/)
  assert.doesNotMatch(view(), /HCP_PACKER_BUCKET_NAME/)
})

// The block is made to be pasted into a shell, and bucket names are arbitrary
// strings (the compat plane deliberately imposes no character class). An
// unquoted name with spaces exports the wrong value; one with metacharacters
// runs them on the operator's machine.
test('a hostile bucket name is quoted, a plain one stays bare', () => {
  assert.equal(shellValue('base-images'), 'base-images')
  assert.equal(shellValue('base images'), "'base images'")
  assert.equal(shellValue('x; rm -rf /'), "'x; rm -rf /'")
  assert.equal(shellValue("it's"), `'it'\\''s'`)
  const env = clientEnvironment({
    host: 'h', organizationID: 'org-1', projectID: 'proj-1', bucketName: 'pw; do-evil',
  })
  assert.match(env, /export HCP_PACKER_BUCKET_NAME='pw; do-evil'/)
})

// A transient listing failure must not hand the operator a plausible-looking
// block that silently omits the bucket variable: the omission is stated, with
// a retry path through the screen's Refresh.
test('an unresolved bucket name is stated above the block, never silent', () => {
  const failed = view({ bucketNameFailure: 'listing unavailable' })
  // The apostrophe renders HTML-escaped in static markup.
  assert.match(failed, /The session&#x27;s bucket could not be resolved/)
  assert.match(failed, /listing unavailable/)
  assert.match(failed, /omits HCP_PACKER_BUCKET_NAME/)
  assert.doesNotMatch(view(), /could not be resolved/)
  // The resolution effect retries on the screen's Refresh counter.
  const instanceSource = readFileSync(
    new URL('../src/screens/Instance.tsx', import.meta.url), 'utf8',
  )
  assert.match(instanceSource, /signOut, refresh\]\)/)
})

// The SDK rejects a non-https auth URL on any network, so a console served over
// http must say the value will not work rather than hand over a broken one.
test('serving over http is called out, because the SDK will refuse the auth URL', () => {
  assert.match(view({ secure: false }), /not being served over https/)
  assert.doesNotMatch(view({ secure: true }), /not being served over https/)
})

test('the build card renders only endpoint-supplied values', () => {
  const markup = view()
  for (const supplied of [
    '1.2.3', 'abc123', '/packer/2023-01-01',
    'ok', 'degraded', 'enabled',
  ]) {
    assert.match(markup, new RegExp(supplied.replaceAll('/', '\\/')))
  }
  assert.match(markup, /Database/)
  assert.doesNotMatch(markup, /reachable/)
  assert.match(markup, /<time[^>]*dateTime="2026-08-05T09:00:00Z"/)
  assert.doesNotMatch(markup, /postgres 16/)
})

test('the database row maps an unreachable store without changing the payload shape', () => {
  const markup = renderToStaticMarkup(React.createElement(BuildCard, {
    instance: { store: false }, loading: false, failure: null,
  }))
  assert.match(markup, /Database/)
  assert.match(markup, /unreachable/)
  assert.doesNotMatch(markup, />Store</)
})

test('the build card distinguishes loading, failure, and honest absence', () => {
  const card = (props) => renderToStaticMarkup(React.createElement(BuildCard, props))
  const loading = card({ instance: null, loading: true, failure: null })
  assert.match(loading, /pf-v6-c-skeleton/)
  assert.match(loading, /pf-v6-screen-reader">Loading build information…<\/span>/)
  const failed = card({ instance: null, loading: false, failure: '503 from instance' })
  assert.match(failed, /Build information could not be loaded/)
  assert.match(failed, /503 from instance/)
  assert.doesNotMatch(failed, /API versions/)

  const empty = card({ instance: {}, loading: false, failure: null })
  assert.equal(empty.split('>—<').length - 1, 8)
  assert.doesNotMatch(empty, /0\.0\.0|postgres/)
})

test('the scanner card names the adapter and renders honest absence', () => {
  const card = (instance) => renderToStaticMarkup(React.createElement(ScannerCard, {
    instance, loading: false, failure: null,
  }))
  const configured = card({ scanner: { configured: true, adapter: 'osv' } })
  assert.match(configured, /Adapter/)
  assert.match(configured, /osv/)
  assert.doesNotMatch(configured, /not configured/)

  const absent = card({ scanner: { configured: false } })
  assert.match(absent, /Scanning is not configured/)

  const loading = renderToStaticMarkup(React.createElement(ScannerCard, {
    instance: null, loading: true, failure: null,
  }))
  assert.match(loading, /pf-v6-c-skeleton/)
  assert.match(loading, /pf-v6-screen-reader">Loading scanner information…<\/span>/)
})

// duf-6ah: the screen is the copyable block, not documentation. The prose,
// tenancy-resolution card and not-implemented card are gone; the environment
// block renders expanded so every line is visible without interaction.
test('the screen is the environment block, expanded, without documentation prose', () => {
  const markup = view()
  assert.doesNotMatch(markup, /vulnerability scanning/)
  assert.doesNotMatch(markup, /How a client resolves its tenancy/)
  assert.doesNotMatch(markup, /carries no scheme/)
  assert.match(markup, /HCP_API_ADDRESS/)
  // Every line visible without interaction, and the block rendered EXACTLY
  // once — the expansion variant rendered it twice, once truncated, which is
  // the defect duf-yxa fixed on the version page.
  assert.match(markup, /HCP_PROJECT_ID=proj-1/)
  assert.equal(markup.split('HCP_API_ADDRESS').length - 1, 1)
})
