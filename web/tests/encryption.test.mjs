import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let ApiError
let encryptionRefusalHint
let loadEncryption
let rewrapEncryption
let rotateEncryption
let EncryptionView
let EncryptionAlerts
let RotationConfirmation
let RewrapConfirmation
let encryptionIsUnconfigured
let TypedConfirmModalView

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ ApiError } = await vite.ssrLoadModule('/src/api/client.ts'))
  ;({
    encryptionRefusalHint, loadEncryption, rewrapEncryption, rotateEncryption,
  } = await vite.ssrLoadModule('/src/data/encryption.ts'))
  ;({
    EncryptionView, EncryptionAlerts, RotationConfirmation, RewrapConfirmation,
    encryptionIsUnconfigured,
  } = await vite.ssrLoadModule('/src/screens/Encryption.tsx'))
  ;({ TypedConfirmModalView } =
    await vite.ssrLoadModule('/src/components/TypedConfirmModal.tsx'))
})

after(async () => {
  await vite.close()
})

const entry = (over = {}) => ({
  purpose: 'payload',
  version: 2,
  kek_ref: 'vault:v2:dufflebag',
  wrapped_at: '2026-08-05T09:30:00Z',
  ...over,
})

const view = (over = {}) => renderToStaticMarkup(React.createElement(EncryptionView, {
  encryption: { state: 'ok', keyring: [] },
  loading: false,
  failure: null,
  reload: async () => {},
  token: 'root-token',
  callerRole: 'root',
  ...over,
}))

test('encryption screen has an honest skeleton loading state', () => {
  const loading = view({ encryption: null, loading: true })
  assert.match(loading, /pf-v6-c-skeleton/)
  assert.match(loading, /pf-v6-screen-reader">Loading encryption state…<\/span>/)
  assert.doesNotMatch(loading, /Encryption keyring/)
})

test('unconfigured encryption is the one-way-door empty state without controls', () => {
  const encryption = { state: 'unconfigured', keyring: [] }
  assert.equal(encryptionIsUnconfigured(encryption), true)
  const markup = view({ encryption })
  assert.match(markup, /Encryption at rest is not configured on this instance/)
  assert.match(markup, /chosen at first boot and cannot be enabled later/)
  assert.doesNotMatch(markup, /Encryption keyring/)
  assert.doesNotMatch(markup, /Rewrap keyring/)
  assert.doesNotMatch(markup, /Rotate keys/)
})

test('root refusal renders the load failure instead of the unconfigured state', () => {
  const refused = view({
    encryption: null,
    failure: 'Only a platform root can view or change encryption state.',
  })
  assert.match(refused, /Encryption state could not be loaded/)
  assert.match(refused, /Only a platform root/)
  assert.doesNotMatch(refused, /Encryption at rest is not configured/)
  assert.doesNotMatch(refused, /Encryption keyring/)
})

test('an action refusal has its own danger alert', () => {
  const markup = renderToStaticMarkup(React.createElement(EncryptionAlerts, {
    failure: null,
    actionFailure: 'The key service refused or was unreachable. The keyring was not changed.',
  }))
  assert.match(markup, /The action was refused/)
  assert.match(markup, /The key service refused or was unreachable/)
})

test('degraded state warns about sealing while root controls remain enabled', () => {
  const markup = view({ encryption: { state: 'degraded', keyring: [entry()] } })
  assert.match(markup, /could not unwrap the keyring at its last heartbeat/)
  assert.match(markup, /will fail until the key service recovers/)
  assert.match(markup, /sealed/)
  for (const label of ['Rewrap keyring', 'Rotate keys']) {
    const end = markup.indexOf(`>${label}<`)
    assert.ok(end >= 0, `${label} is absent`)
    assert.doesNotMatch(markup.slice(Math.max(0, end - 300), end), /disabled/)
  }
})

test('the keyring collapses to one row per purpose whose values update in place', () => {
  const markup = view({
    encryption: {
      state: 'ok',
      keyring: [
        entry({ version: 1, wrapped_at: '2026-08-05T09:00:00Z' }),
        entry({ version: 2, wrapped_at: '2026-08-05T09:30:00Z' }),
        entry({ purpose: 'token_signing', version: 4, kek_ref: 'v3' }),
      ],
    },
  })
  assert.match(markup, /Encryption keyring/)
  // payload: two retained versions, one row — the active version and the
  // retained count carry what a row per version used to sprawl across.
  assert.match(markup, /payload/)
  assert.match(markup, /data-label="Active"[^>]*>2</)
  assert.match(markup, /data-label="Retained"[^>]*>2</)
  assert.match(markup, /<time[^>]*dateTime="2026-08-05T09:30:00Z"/)
  assert.doesNotMatch(markup, /2026-08-05T09:00:00Z/)
  assert.match(markup, /token_signing/)
  assert.match(markup, /data-label="Active"[^>]*>4</)
  // The semantics the columns need are stated on the page, not assumed.
  assert.match(markup, /advances only after the KEK is rotated at the key service/)
})

test('a purpose wrapped under more than one KEK version shows the mixed set', () => {
  const markup = view({
    encryption: {
      state: 'ok',
      keyring: [
        entry({ version: 1, kek_ref: 'v1' }),
        entry({ version: 2, kek_ref: 'v2' }),
      ],
    },
  })
  // One row still — the mixed KEK set is the pending-rewrap signal.
  assert.match(markup, /data-label="KEK version"[^>]*>v1, v2</)
  assert.match(markup, /data-label="Retained"[^>]*>2</)
})

test('the Latest KEK column renders the heartbeat value rather than the row KEK', () => {
  const markup = view({
    encryption: { state: 'ok', kek_latest: 'v6', keyring: [entry({ kek_ref: 'v2' })] },
  })
  assert.match(markup, /<th[^>]*>Latest KEK</)
  assert.match(markup, /data-label="KEK version"[^>]*>v2</)
  assert.match(markup, /data-label="Latest KEK"[^>]*>v6</)
})

test('the Latest KEK column renders an em dash when the heartbeat value is unknown', () => {
  const markup = view({ encryption: { state: 'ok', keyring: [entry({ kek_ref: 'v2' })] } })
  assert.match(markup, /data-label="Latest KEK"[^>]*>—</)
})

test('rewrap is disabled with the current-version reason when every entry is current', () => {
  const markup = view({
    encryption: {
      state: 'ok',
      kek_latest: 'v6',
      keyring: [entry({ kek_ref: 'v6' }), entry({ purpose: 'integrity', kek_ref: 'v6' })],
    },
  })
  const end = markup.indexOf('>Rewrap keyring<')
  assert.ok(end >= 0, 'Rewrap keyring is absent')
  assert.match(markup.slice(Math.max(0, end - 500), end), /disabled/)
  assert.match(markup, /already wrapped under the key service&#x27;s current KEK version \(v6\)\./)
})

test('rewrap remains enabled when the latest KEK is unknown', () => {
  const markup = view({ encryption: { state: 'ok', keyring: [entry({ kek_ref: 'v6' })] } })
  const end = markup.indexOf('>Rewrap keyring<')
  assert.ok(end >= 0, 'Rewrap keyring is absent')
  assert.doesNotMatch(markup.slice(Math.max(0, end - 500), end), /disabled/)
})

test('rewrap remains enabled when any keyring entry trails the latest KEK', () => {
  const markup = view({
    encryption: {
      state: 'ok',
      kek_latest: 'v6',
      keyring: [entry({ kek_ref: 'v6' }), entry({ purpose: 'integrity', kek_ref: 'v5' })],
    },
  })
  const end = markup.indexOf('>Rewrap keyring<')
  assert.ok(end >= 0, 'Rewrap keyring is absent')
  assert.doesNotMatch(markup.slice(Math.max(0, end - 500), end), /disabled/)
})

test('non-root encryption controls show the root requirement and are disabled nearby', () => {
  const markup = view({ callerRole: 'reader', encryption: { state: 'ok', keyring: [entry()] } })
  assert.match(markup, /Requires root/)
  for (const label of ['Rewrap keyring', 'Rotate keys']) {
    const end = markup.indexOf(label)
    assert.ok(end >= 0, `${label} is absent`)
    assert.match(markup.slice(Math.max(0, end - 500), end), /disabled/)
  }
})

test('rotation requires rotate and states every consequence before invoking the action', () => {
  let fired = false
  const confirmation = RotationConfirmation({
    onConfirm: () => { fired = true },
    onCancel: () => {},
  })
  assert.equal(fired, false)
  assert.equal(confirmation.props.expected, 'rotate')
  const blocked = renderToStaticMarkup(React.createElement(TypedConfirmModalView, {
    ...confirmation.props, confirmation: '', onConfirmationChange: () => {},
  }))
  assert.match(blocked, /Rotate every encryption key/)
  assert.match(blocked, /Existing data stays readable forever/)
  assert.match(blocked, /old key age out within 15 minutes/)
  assert.match(blocked, /Audit HMAC correlation across the rotation boundary changes by design/)
  assert.match(blocked, /peers adopt the new keys within about five minutes/)
  assert.match(blocked, /Type <strong>rotate<\/strong> to confirm/)
  assert.match(blocked, /<button[^>]*disabled[^>]*>[\s\S]{0,160}Rotate keys/)
  const armed = renderToStaticMarkup(React.createElement(TypedConfirmModalView, {
    ...confirmation.props, confirmation: 'rotate', onConfirmationChange: () => {},
  }))
  assert.doesNotMatch(armed, /<button[^>]*disabled[^>]*>[\s\S]{0,160}Rotate keys/)
  confirmation.props.onConfirm()
  assert.equal(fired, true)
})

test('rewrap requires rewrap and explains its atomic failure behaviour', () => {
  const confirmation = RewrapConfirmation({ onConfirm: () => {}, onCancel: () => {} })
  assert.equal(confirmation.props.expected, 'rewrap')
  const blocked = renderToStaticMarkup(React.createElement(TypedConfirmModalView, {
    ...confirmation.props, confirmation: '', onConfirmationChange: () => {},
  }))
  assert.match(blocked, /Rewrap the keyring\?/)
  assert.match(blocked, /Every keyring entry is re-wrapped under the key service&#x27;s current KEK version\./)
  assert.match(blocked, /Stored data is not re-encrypted and stays readable throughout\./)
  assert.match(blocked, /the rewrap fails and the keyring is unchanged\./)
  assert.match(blocked, /Type <strong>rewrap<\/strong> to confirm/)
  assert.match(blocked, /<button[^>]*disabled[^>]*>[\s\S]{0,160}Rewrap keyring/)
  const armed = renderToStaticMarkup(React.createElement(TypedConfirmModalView, {
    ...confirmation.props, confirmation: 'rewrap', onConfirmationChange: () => {},
  }))
  assert.doesNotMatch(armed, /<button[^>]*disabled[^>]*>[\s\S]{0,160}Rewrap keyring/)
})

test('encryption refusals map to safe operator guidance', () => {
  assert.equal(
    encryptionRefusalHint(new ApiError(403, 'forbidden')),
    'Only a platform root can view or change encryption state.',
  )
  assert.equal(
    encryptionRefusalHint(new ApiError(409, 'another rotation changed the keyring')),
    'another rotation changed the keyring',
  )
  assert.match(
    encryptionRefusalHint(new ApiError(409, '')),
    /Encryption is not configured, or another rotation changed the keyring/,
  )
  assert.equal(
    encryptionRefusalHint(new ApiError(502, 'internal key service detail')),
    'The key service refused or was unreachable. The keyring was not changed.',
  )
})

test('encryption API wrappers use the contract paths and bodyless mutations', async () => {
  const originalFetch = globalThis.fetch
  const requests = []
  globalThis.fetch = async (path, options) => {
    requests.push({ path, options })
    return new Response(JSON.stringify({ state: 'ok', keyring: [] }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }
  try {
    await loadEncryption('root-token')
    await rewrapEncryption('root-token')
    await rotateEncryption('root-token')
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.deepEqual(requests.map(({ path, options }) => [path, options.method]), [
    ['/api/v1/encryption', 'GET'],
    ['/api/v1/encryption/rewrap', 'POST'],
    ['/api/v1/encryption/rotate', 'POST'],
  ])
  assert.equal(requests[1].options.body, undefined)
  assert.equal(requests[2].options.body, undefined)
  assert.equal(requests[1].options.headers['Content-Type'], undefined)
  assert.equal(requests[2].options.headers['Content-Type'], undefined)
})
