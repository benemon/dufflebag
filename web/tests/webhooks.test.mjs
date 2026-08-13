import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let CreateWebhookForm
let DeleteConfirmation
let WebhooksView
let DeliveryTable
let TypedConfirmModalView

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ CreateWebhookForm, DeleteConfirmation, WebhooksView, DeliveryTable } =
    await vite.ssrLoadModule('/src/screens/Webhooks.tsx'))
  ;({ TypedConfirmModalView } =
    await vite.ssrLoadModule('/src/components/TypedConfirmModal.tsx'))
})

after(async () => {
  await vite.close()
})

// Fixture fields follow components.schemas.Webhook in spec/platform/openapi.yaml:
// the secret never appears, has_secret carries the posture.
const webhook = (over = {}) => ({
  id: 'e0a24d5e-0000-4000-8000-000000000000', name: 'build events',
  url: 'https://receiver.example.com/hook', description: '',
  has_secret: true, events: [], state: 'active',
  last_verification_at: '2026-08-13T00:00:00Z', last_verification_error: null,
  created_at: '2026-08-13T00:00:00Z', updated_at: '2026-08-13T00:00:00Z',
  ...over,
})

const view = (props = {}) => renderToStaticMarkup(React.createElement(WebhooksView, {
  webhooks: [], loading: false, failure: null, callerRole: 'maintainer',
  onCreate: async () => {}, onVerify: async () => {}, onDelete: async () => {},
  onDeliveries: async () => [],
  ...props,
}))

test('the create form carries the approved fields and event subscriptions', () => {
  const markup = renderToStaticMarkup(React.createElement(CreateWebhookForm, {
    callerRole: 'maintainer', onCreate: async () => {}, onCancel: () => {},
  }))
  assert.match(markup, /Create and verify/)
  assert.match(markup, /HMAC key/)
  assert.match(markup, /version\.created/)
  assert.match(markup, /bucket\.deleted/)
  assert.match(markup, /Leave every box clear to subscribe to all operations\./)
})

test('deletion demands the webhook name and only fires through confirmation', () => {
  let deletes = 0
  const confirmation = DeleteConfirmation({
    webhook: webhook(), onConfirm: () => { deletes++ }, onCancel: () => {},
  })
  assert.equal(deletes, 0)
  assert.equal(confirmation.props.expected, 'build events')
  confirmation.props.onConfirm()
  assert.equal(deletes, 1)
  const markup = renderToStaticMarkup(React.createElement(TypedConfirmModalView, {
    ...confirmation.props, confirmation: '', onConfirmationChange: () => {},
  }))
  assert.match(markup, /Delete build events\?/)
  assert.match(markup, /Type <strong>build events<\/strong> to confirm/)
  assert.match(markup, /Delete webhook/)
  assert.match(markup, /Cancel/)
})

test('webhook actions are disabled with the required role for a reader', () => {
  const markup = view({ callerRole: 'reader', webhooks: [webhook()] })
  assert.match(markup, /Requires maintainer/)
})

test('Create webhook moves between the empty state and populated header with its role gate', () => {
  const emptyReader = view({ callerRole: 'reader' })
  assert.match(emptyReader, /pf-v6-c-empty-state[\s\S]*Create webhook/)
  assert.match(emptyReader, /Requires maintainer/)
  assert.equal((emptyReader.match(/Create webhook/g) ?? []).length, 1)

  const populatedReader = view({ callerRole: 'reader', webhooks: [webhook()] })
  assert.match(populatedReader, /pf-v6-c-page__main-section[\s\S]{0,2000}Create webhook/)
  assert.doesNotMatch(populatedReader, /pf-v6-c-empty-state/)
  assert.match(populatedReader, /Requires maintainer/)
  assert.equal((populatedReader.match(/Create webhook/g) ?? []).length, 1)
})

test('pending and active states are labelled distinctly', () => {
  const active = view({ webhooks: [webhook()] })
  assert.match(active, /pf-m-success/)
  assert.match(active, /pf-v6-c-label__text">active</)
  const pending = view({ webhooks: [webhook({ state: 'pending' })] })
  assert.match(pending, /pf-m-warning/)
  assert.match(pending, /pf-v6-c-label__text">pending</)
})

test('the empty and failed states say so honestly', () => {
  assert.match(view(), /<h2[^>]*>No webhooks are configured<\/h2>/)
  assert.match(view(), /pf-v6-c-empty-state__body">Create a webhook to send signed project events\./)
  const failed = view({ failure: 'boom' })
  assert.match(failed, /Webhooks could not be loaded/)
  assert.doesNotMatch(failed, /No webhooks are configured/)
})

test('the loading listing is held by skeleton rows with honest screen-reader copy', () => {
  const loading = view({ loading: true })
  assert.match(loading, /pf-v6-c-skeleton/)
  assert.match(loading, /pf-v6-screen-reader">Loading webhooks…<\/span>/)
  assert.doesNotMatch(loading, /No webhooks are configured/)
})

test('delivery attempts render as semantic timestamps and retain the empty dash', () => {
  const markup = renderToStaticMarkup(React.createElement(DeliveryTable, { deliveries: [{
    id: 'delivery-1', event_id: 'event-1', operation: 'version.completed',
    status: 'delivered', attempt_count: 1, first_attempted_at: '2026-08-13T07:41:00Z',
    last_attempted_at: '2026-08-13T07:41:30.702958Z', response_code: 204,
    detail: null, created_at: '2026-08-13T07:41:00Z',
  }, {
    id: 'delivery-2', event_id: 'event-2', operation: 'version.created',
    status: 'pending', attempt_count: 0, first_attempted_at: null,
    last_attempted_at: null, response_code: null, detail: null,
    created_at: '2026-08-13T07:42:00Z',
  }] }))
  assert.match(markup, /<time[^>]*dateTime="2026-08-13T07:41:30.702958Z"/)
  assert.match(markup, /data-label="Last attempt"[^>]*>—</)
})

test('webhook deliveries paginate the in-memory listing', () => {
  const deliveries = Array.from({ length: 21 }, (_, index) => {
    const number = String(index + 1).padStart(2, '0')
    return {
      id: `delivery-${number}`, event_id: `event-${number}`,
      operation: `operation-${number}`, status: 'delivered', attempt_count: 1,
      first_attempted_at: '2026-08-13T07:41:00Z',
      last_attempted_at: '2026-08-13T07:41:30Z', response_code: 204,
      detail: null, created_at: '2026-08-13T07:41:00Z',
    }
  })
  const markup = renderToStaticMarkup(React.createElement(DeliveryTable, { deliveries }))
  assert.match(markup, />operation-20</)
  assert.doesNotMatch(markup, />operation-21</)
  assert.ok((markup.match(/pf-v6-c-pagination/g) ?? []).length >= 2)
})
