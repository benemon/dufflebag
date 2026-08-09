import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let ApiError
let createAuditTarget
let auditRefusalHint
let AuditView
let AuditTargetTable
let CreateAuditTargetForm
let LastTargetConfirmation
let auditTargetLimitReached
let lastTargetRemovalNeedsConfirmation
let formatBytes
let loginDestination

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ ApiError } = await vite.ssrLoadModule('/src/api/client.ts'))
  ;({ createAuditTarget, auditRefusalHint } = await vite.ssrLoadModule('/src/data/audit.ts'))
  ;({
    AuditView, AuditTargetTable, CreateAuditTargetForm, LastTargetConfirmation,
    auditTargetLimitReached, lastTargetRemovalNeedsConfirmation, formatBytes,
  } = await vite.ssrLoadModule('/src/screens/Audit.tsx'))
  ;({ loginDestination } = await vite.ssrLoadModule('/src/screens/Login.tsx'))
})

after(async () => {
  await vite.close()
})

const target = (over = {}) => ({
  id: '11111111-2222-4333-8444-555555555555',
  path: '/var/log/dufflebag/audit.log',
  created_at: '2026-08-03T09:00:00Z',
  status: 'healthy',
  since: null,
  consecutive_failures: 0,
  cumulative_failures: 0,
  last_failure_at: null,
  last_reopened_at: null,
  measurement: {
    state: 'available', current_file_size_bytes: 0, filesystem_free_bytes: 16 * 1024 * 1024,
  },
  ...over,
})

// Exact GET /api/v1/self field names from the platform handler's JSON response.
const self = (role) => ({ principal_id: 'p-caller', name: `${role} caller`, role })

const view = (over = {}) => renderToStaticMarkup(React.createElement(AuditView, {
  targets: [], loading: false, failure: null, reload: async () => {}, token: 'root-token',
  callerRole: 'root',
  ...over,
}))

test('audit mutations are disabled with a root requirement for a reader', () => {
  const reader = view({ callerRole: self('reader').role, targets: [target()] })
  assert.match(reader, /Requires root/)
  for (const label of ['Add target', 'Remove']) {
    const end = reader.indexOf(label)
    assert.ok(end >= 0, `${label} is absent`)
    assert.match(reader.slice(Math.max(0, end - 500), end), /disabled/)
  }

  const root = view({ callerRole: self('root').role, targets: [target()] })
  const add = root.indexOf('>Add target<')
  const remove = root.indexOf('>Remove<')
  assert.doesNotMatch(root.slice(Math.max(0, add - 300), add), /disabled/)
  assert.doesNotMatch(root.slice(Math.max(0, remove - 300), remove), /disabled/)
})

test('audit screen has honest loading, empty, and root-refusal states', () => {
  assert.match(view({ loading: true }), /Loading audit targets/)
  assert.match(view(), /No audit targets are configured/)
  assert.match(view(), /not recording audit events to a file/)

  const refused = view({ failure: 'Only a platform root can view or change audit targets.' })
  assert.match(refused, /Audit targets could not be loaded/)
  assert.match(refused, /Only a platform root/)
  assert.doesNotMatch(refused, /No audit targets are configured/)
})

test('three targets couple the disabled Add control to its one visible explanation', () => {
  const three = [target(), target({ id: '2', path: '/audit/two.log' }), target({ id: '3', path: '/audit/three.log' })]
  assert.equal(auditTargetLimitReached(three, false), true)
  const full = view({ targets: three })
  assert.match(full, /<button[^>]*disabled[^>]*>[\s\S]{0,160}Add target/)
  assert.match(full, /Three audit targets are already configured/)
  assert.match(full, /disabled while all three slots are occupied/)

  const room = view({ targets: three.slice(0, 2) })
  assert.doesNotMatch(room, /Three audit targets are already configured/)
  assert.doesNotMatch(room, /disabled while all three slots are occupied/)
  assert.equal(auditTargetLimitReached(three, true), false)
})

test('only the last target removal requires confirmation and states the consequence', () => {
  assert.equal(lastTargetRemovalNeedsConfirmation([target()]), true)
  assert.equal(lastTargetRemovalNeedsConfirmation([target(), target({ id: '2' })]), false)
  const confirmation = renderToStaticMarkup(React.createElement(LastTargetConfirmation, {
    target: target(), callerRole: 'root', onConfirm: () => {}, onCancel: () => {},
  }))
  assert.match(confirmation, /Remove the last audit target/)
  assert.match(confirmation, /stops this instance recording audit events entirely until/)
  assert.match(confirmation, /Remove last target/)
})

test('recovered health still renders cumulative and last-failure history', () => {
  const recovered = target({
    status: 'healthy',
    cumulative_failures: 7,
    last_failure_at: '2026-08-03T10:30:00Z',
  })
  const markup = renderToStaticMarkup(React.createElement(AuditTargetTable, {
    targets: [recovered], callerRole: 'root', onRemove: () => {},
  }))
  assert.match(markup, /Cumulative failures<\/td><td[^>]*>7<\/td>/)
  assert.match(markup, /Last failure/)
  assert.match(markup, /2026-08-03T10:30:00Z/)
  assert.match(markup, /Consecutive failures<\/td><td[^>]*>0<\/td>/)
})

test('storage columns distinguish an empty current file from an unavailable measurement', () => {
  const markup = renderToStaticMarkup(React.createElement(AuditTargetTable, {
    targets: [
      target({ last_reopened_at: '2026-08-04T12:30:00Z' }),
      target({
        id: '2', path: '/audit/unmeasurable.log',
        measurement: { state: 'unavailable' },
      }),
    ],
    callerRole: 'root',
    onRemove: () => {},
  }))
  assert.match(markup, /Current file/)
  assert.match(markup, /Space remaining/)
  assert.match(markup, /title="0 bytes">0 B/)
  assert.match(markup, /title="16777216 bytes">16 MiB/)
  assert.match(markup, /2026-08-04T12:30:00Z/)
  assert.match(markup, /Unavailable/)
  assert.match(markup, /Never/)
})

test('byte formatting keeps file and filesystem values readable', () => {
  assert.equal(formatBytes(0), '0 B')
  assert.equal(formatBytes(1536), '1.5 KiB')
  assert.equal(formatBytes(16 * 1024 * 1024), '16 MiB')
})

test('the create form is inline and starts disabled for an empty path', () => {
  const markup = renderToStaticMarkup(React.createElement(CreateAuditTargetForm, {
    callerRole: 'root', onCreate: async () => {}, onCancel: () => {},
  }))
  assert.match(markup, /New audit target/)
  assert.match(markup, /audit-target-path/)
  assert.match(markup, /<button[^>]*disabled[^>]*>[\s\S]{0,160}Add target/)
})

test('create refusal categories become safe operator guidance', () => {
  assert.match(
    auditRefusalHint(new ApiError(400, 'audit target path was refused', 'symlink-refused')),
    /Symlinks are refused/,
  )
  assert.match(
    auditRefusalHint(new ApiError(400, 'audit target path was refused', 'permission-denied')),
    /does not have permission/,
  )
  assert.match(
    auditRefusalHint(new ApiError(400, 'audit target path was refused', 'world-writable-parent')),
    /world-writable/,
  )
})

test('the API client preserves an audit create refusal reason', async () => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async () => new Response(JSON.stringify({
    message: 'audit target path was refused', reason: 'not-a-regular-file',
  }), { status: 400, headers: { 'Content-Type': 'application/json' } })
  try {
    await assert.rejects(
      createAuditTarget('root-token', '/dev/null'),
      (error) => error instanceof ApiError &&
        error.status === 400 && error.reason === 'not-a-regular-file',
    )
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('degraded audit at boot goes to sign-in, not the database-failure screen', () => {
  assert.equal(loginDestination({ initialized: true, database: true, audit: 'degraded' }), 'sign-in')
  assert.equal(loginDestination({ initialized: false, database: true, audit: 'disabled' }), 'initialize')
  assert.equal(loginDestination({ initialized: false, database: false, audit: 'disabled' }), 'database-failure')
})
