import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import { createServer } from 'vite'

let vite
let allowedActions
let visibleNavItems

before(async () => {
  vite = await createServer({
    root: process.cwd(), logLevel: 'silent',
    server: { middlewareMode: true }, appType: 'custom',
  })
  ;({ allowedActions, visibleNavItems } = await vite.ssrLoadModule('/src/auth/permissions.ts'))
})

after(async () => { await vite.close() })

test('each role gets the nested console action snapshot declared by the server', () => {
  assert.deepEqual(allowedActions('reader'), [])
  assert.deepEqual(allowedActions('builder'), ['pinBuckets'])
  assert.deepEqual(allowedActions('publisher'), ['pinBuckets'])
  assert.deepEqual(allowedActions('maintainer'), ['pinBuckets', 'managePrincipals'])
  assert.deepEqual(allowedActions('root'), ['pinBuckets', 'configureAudit', 'manageEncryption', 'managePrincipals'])
  assert.deepEqual(allowedActions(null), [])
})

test('pinBuckets permission mapping requires builder', () => {
  assert.equal(allowedActions('reader').includes('pinBuckets'), false)
  assert.equal(allowedActions('builder').includes('pinBuckets'), true)
})

test('each role gets the navigation snapshot declared by the server', () => {
  for (const [role, expected] of [
    [null, ['buckets', 'instance']],
    ['reader', ['buckets', 'instance']],
    ['builder', ['buckets', 'instance']],
    ['publisher', ['buckets', 'instance']],
    ['maintainer', ['buckets', 'principals', 'instance']],
    ['root', ['buckets', 'principals', 'audit', 'encryption', 'instance']],
  ]) {
    assert.deepEqual(visibleNavItems(role), expected, String(role))
  }
})
