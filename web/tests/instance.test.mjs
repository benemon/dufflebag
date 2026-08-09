import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let InstanceView, BuildCard, ScannerCard, clientEnvironment

before(async () => {
  vite = await createServer({
    root: process.cwd(), logLevel: 'silent',
    server: { middlewareMode: true }, appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ InstanceView, BuildCard, ScannerCard, clientEnvironment } =
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

// The SDK rejects a non-https auth URL on any network, so a console served over
// http must say the value will not work rather than hand over a broken one.
test('serving over http is called out, because the SDK will refuse the auth URL', () => {
  assert.match(view({ secure: false }), /not being served over https/)
  assert.doesNotMatch(view({ secure: true }), /not being served over https/)
})

test('the build card renders only endpoint-supplied values', () => {
  const markup = view()
  for (const supplied of [
    '1.2.3', 'abc123', '/packer/2023-01-01', '2026-08-05T09:00:00Z',
    'reachable', 'ok', 'degraded', 'enabled',
  ]) {
    assert.match(markup, new RegExp(supplied.replaceAll('/', '\\/')))
  }
  assert.doesNotMatch(markup, /postgres 16/)
})

test('the build card distinguishes loading, failure, and honest absence', () => {
  const card = (props) => renderToStaticMarkup(React.createElement(BuildCard, props))
  assert.match(card({ instance: null, loading: true, failure: null }), /Loading build information/)
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
