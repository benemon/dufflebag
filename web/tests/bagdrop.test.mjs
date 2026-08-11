import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let enableBagDrop
let AssociationSelectorView
let BagDropStatusTableView
let BagDropView
let BucketRemovalConfirmation
let DestinationActionFailure
let DestinationFormView
let DestinationZone

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ enableBagDrop } = await vite.ssrLoadModule('/src/api/client.ts'))
  ;({
    AssociationSelectorView, BagDropStatusTableView, BagDropView, BucketRemovalConfirmation,
    DestinationActionFailure, DestinationFormView, DestinationZone,
  } = await vite.ssrLoadModule('/src/screens/BagDrop.tsx'))
})

after(async () => {
  await vite.close()
})

// Fixture fields follow components.schemas.BagDropVerificationResult in
// spec/platform/openapi.yaml: outcome is required; reason and message are optional.
const verification = (over = {}) => ({
  outcome: 'failed', reason: 'credential_refused', message: 'Destination rejected the credential.',
  ...over,
})

// Fixture fields follow components.schemas.BagDropConfig in
// spec/platform/openapi.yaml, including the authored credential_protection posture.
const config = (over = {}) => ({
  adapter: 'hcp-packer',
  hcp_packer: {
    organization_id: 'destination-org', project_id: 'destination-project', client_id: 'client-id',
  },
  secret_set: true, credential_protection: 'keyring', enabled: false,
  last_verification: null,
  created_at: '2026-08-11T09:00:00Z', updated_at: '2026-08-11T09:00:00Z',
  ...over,
})

// Fixture fields follow components.schemas.BagDropAssociation in
// spec/platform/openapi.yaml; every nullable timestamp/error field is present.
const association = (over = {}) => ({
  bucket_name: 'images', state: 'active', sync_status: 'synced',
  created_at: '2026-08-11T09:00:00Z', updated_at: '2026-08-11T09:05:00Z',
  first_attempted_at: '2026-08-11T09:01:00Z',
  last_attempt_at: '2026-08-11T09:05:00Z', last_synced_at: '2026-08-11T09:05:00Z',
  last_sync_error: null,
  ...over,
})

// Fixture fields follow components.schemas.BagDropStatus in
// spec/platform/openapi.yaml: configured and associations are the required reader surface.
const status = (over = {}) => ({
  configured: true, adapter: 'hcp-packer', enabled: false,
  last_verification: null, associations: [association()], ...over,
})

const draft = (over = {}) => ({
  adapter: 'hcp-packer', endpoint: '', caChain: '', organizationID: 'destination-org',
  projectID: 'destination-project', clientID: 'client-id', clientSecret: '', ...over,
})

const viewProps = (over = {}) => ({
  callerRole: 'maintainer', canConfigure: true,
  config: config(), configLoading: false, configFailure: null,
  onSave: async () => config(), onVerify: async () => verification({ outcome: 'resolved' }),
  onEnable: async () => ({ kind: 'enabled', config: config({ enabled: true }) }),
  onDisable: async () => config(), onDelete: async () => {},
  buckets: [{ name: 'images' }, { name: 'workers' }], associations: [association()],
  associationsLoading: false, associationsFailure: null,
  onAssociate: async () => {}, onUnassociate: async () => {},
  status: status(), statusLoading: false, statusFailure: null, onReconcile: async () => {},
  ...over,
})

const renderView = (over = {}) => renderToStaticMarkup(
  React.createElement(BagDropView, viewProps(over)),
)

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

test('reader renders the status zone only while maintainer renders all three zones', () => {
  const reader = renderView({ callerRole: 'reader', canConfigure: false })
  assert.doesNotMatch(reader, /aria-label="Destination"/)
  assert.doesNotMatch(reader, /aria-label="Mirrored buckets"/)
  assert.match(reader, /aria-label="Status"/)

  const maintainer = renderView()
  assert.match(maintainer, /aria-label="Destination"/)
  assert.match(maintainer, /aria-label="Mirrored buckets"/)
  assert.match(maintainer, /aria-label="Status"/)
})

test('Verify is disabled while dirty and enabled after Save restores the saved draft', () => {
  let saved = false
  const props = {
    config: config(), draft: draft({ organizationID: 'edited-org' }), busy: null,
    onDraftChange: () => {}, onSave: () => { saved = true }, onVerify: () => {},
    onEnable: () => {}, onDisable: () => {}, onDelete: () => {},
  }
  const dirtyView = DestinationFormView({ ...props, dirty: true })
  const dirtyVerify = findElement(dirtyView, (element) => element.props.children === 'Verify')
  assert.equal(dirtyVerify.props.isDisabled, true)
  findElement(dirtyView, (element) => element.props.children === 'Save').props.onClick()
  assert.equal(saved, true)

  const savedView = DestinationFormView({ ...props, draft: draft(), dirty: false })
  const savedVerify = findElement(savedView, (element) => element.props.children === 'Verify')
  assert.equal(savedVerify.props.isDisabled, false)
})

const selectorProps = (over = {}) => ({
  buckets: [{ name: 'images' }, { name: 'workers' }], associations: [association()],
  selectedAvailable: null, selectedMirrored: 'images', confirming: null, busy: false,
  onSelectAvailable: () => {}, onSelectMirrored: () => {}, onAssociate: () => {},
  onRequestRemoval: () => {}, onCancelRemoval: () => {}, onConfirmRemoval: () => {},
  ...over,
})

test('moving out opens the destructive confirmation and Cancel never calls DELETE', () => {
  let confirming = null
  let deletes = 0
  const selector = AssociationSelectorView(selectorProps({
    onRequestRemoval: (name) => { confirming = name },
    onConfirmRemoval: () => { deletes++ },
  }))
  findElement(
    selector, (element) => element.props['aria-label'] === 'Stop mirroring selected bucket',
  ).props.onClick()
  assert.equal(confirming, 'images')
  assert.equal(deletes, 0)

  const warning = BucketRemovalConfirmation({
    bucketName: confirming,
    onCancel: () => { confirming = null },
    onConfirm: () => { deletes++ },
  })
  findElement(warning, (element) => element.props.children === 'Cancel').props.onClick()
  assert.equal(confirming, null)
  assert.equal(deletes, 0)

  const confirmed = BucketRemovalConfirmation({
    bucketName: 'images', onCancel: () => {}, onConfirm: () => { deletes++ },
  })
  findElement(
    confirmed, (element) => element.props.variant === 'danger',
  ).props.onClick()
  assert.equal(deletes, 1)
})

test('pending_removal stays in the mirrored pane as Removing and can be resumed', () => {
  let resumed = null
  const pending = association({ state: 'pending_removal', sync_status: 'removing' })
  const tree = AssociationSelectorView(selectorProps({
    associations: [pending], selectedMirrored: 'images', onAssociate: (name) => { resumed = name },
  }))
  const markup = renderToStaticMarkup(tree)
  assert.match(markup, /Mirrored buckets/)
  assert.match(markup, /images/)
  assert.match(markup, /Removing/)
  findElement(tree, (element) => element.props['aria-label'] === 'Resume selected bucket').props.onClick()
  assert.equal(resumed, 'images')
})

test('env_key shows the persistent credential warning while keyring does not', () => {
  const props = {
    configLoading: false, configFailure: null,
    onSave: async () => config(), onVerify: async () => verification(),
    onEnable: async () => ({ kind: 'enabled', config: config() }),
    onDisable: async () => config(), onDelete: async () => {},
  }
  const environment = renderToStaticMarkup(React.createElement(
    DestinationZone, { ...props, config: config({ credential_protection: 'env_key' }) },
  ))
  assert.match(environment, /sealed with an environment key/)
  assert.match(environment, /protected in database dumps, but not against host compromise/)

  const keyring = renderToStaticMarkup(React.createElement(
    DestinationZone, { ...props, config: config({ credential_protection: 'keyring' }) },
  ))
  assert.doesNotMatch(keyring, /sealed with an environment key/)
})

test('only rows carrying last_sync_error have a visible error expander', () => {
  const markup = renderToStaticMarkup(React.createElement(BagDropStatusTableView, {
    associations: [
      association({ bucket_name: 'clean' }),
      association({ bucket_name: 'failed', last_sync_error: 'destination returned HTTP 500' }),
    ],
    expanded: null, onToggle: () => {},
  }))
  assert.equal(markup.split('<button').length - 1, 1)
  assert.match(markup, /<button[^>]*aria-label="Details"/)
  assert.match(markup, /<button[\s\S]*?<svg/)
})

test('Bag Drop zones render honest loading, error, and empty states', () => {
  const loading = renderView({
    configLoading: true, associationsLoading: true, statusLoading: true,
  })
  assert.match(loading, /Loading Bag Drop configuration/)
  assert.match(loading, /Loading mirrored buckets/)
  assert.match(loading, /Loading Bag Drop status/)

  const failed = renderView({
    configFailure: 'configuration refused', associationsFailure: 'bucket listing refused',
    statusFailure: 'status refused',
  })
  assert.match(failed, /Bag Drop configuration could not be loaded/)
  assert.match(failed, /Mirrored buckets could not be loaded/)
  assert.match(failed, /Bag Drop status could not be loaded/)
  assert.match(failed, /configuration refused/)
  assert.match(failed, /bucket listing refused/)
  assert.match(failed, /status refused/)

  const empty = renderView({
    config: null, associations: [], status: { configured: false, associations: [] },
  })
  assert.match(empty, /Configure a destination before mirroring buckets/)
  assert.match(empty, /Bag Drop is not configured/)
  assert.match(empty, /No buckets are being mirrored from this project/)
})

test('enable 409 preserves and renders its verification failure inline', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async () => new Response(JSON.stringify({
    message: 'destination did not resolve; configuration remains disabled',
    verification: verification(),
  }), { status: 409, headers: { 'Content-Type': 'application/json' } })
  try {
    const result = await enableBagDrop('token', {
      organizationID: 'source-org', projectID: 'source-project',
    })
    assert.equal(result.kind, 'refused')
    const markup = renderToStaticMarkup(React.createElement(DestinationActionFailure, {
      message: result.message, verification: result.verification ?? null,
    }))
    assert.match(markup, /configuration remains disabled/)
    assert.match(markup, /credential_refused/)
    assert.match(markup, /Destination rejected the credential/)
  } finally {
    globalThis.fetch = originalFetch
  }
})
