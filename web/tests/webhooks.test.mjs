import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let CreateWebhookForm
let DeleteConfirmation
let WebhooksView

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ CreateWebhookForm, DeleteConfirmation, WebhooksView } =
    await vite.ssrLoadModule('/src/screens/Webhooks.tsx'))
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

test('deletion demands an explicit inline confirmation', () => {
  const markup = renderToStaticMarkup(React.createElement(DeleteConfirmation, {
    webhook: webhook(), onConfirm: () => {}, onCancel: () => {},
  }))
  assert.match(markup, /Delete build events\?/)
  assert.match(markup, /Delete webhook/)
  assert.match(markup, /Cancel/)
})

test('webhook actions are disabled with the required role for a reader', () => {
  const markup = view({ callerRole: 'reader', webhooks: [webhook()] })
  assert.match(markup, /Requires maintainer/)
})

test('pending and active states are labelled distinctly', () => {
  const active = view({ webhooks: [webhook()] })
  assert.match(active, /active/)
  const pending = view({ webhooks: [webhook({ state: 'pending' })] })
  assert.match(pending, /pending/)
})

test('the empty and failed states say so honestly', () => {
  assert.match(view(), /No webhooks are configured/)
  const failed = view({ failure: 'boom' })
  assert.match(failed, /Webhooks could not be loaded/)
  assert.doesNotMatch(failed, /No webhooks are configured/)
})
