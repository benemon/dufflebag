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
  assert.deepEqual(
    allowedActions('publisher'),
    ['pinBuckets', 'revokeVersions', 'manageChannels', 'deleteBuckets'],
  )
  assert.deepEqual(
    allowedActions('maintainer'),
    [
      'pinBuckets', 'revokeVersions', 'manageChannels', 'deleteBuckets',
      'managePrincipals', 'configureBagDrop',
    ],
  )
  assert.deepEqual(allowedActions('root'), [
    'pinBuckets', 'revokeVersions', 'manageChannels', 'deleteBuckets', 'configureAudit',
    'manageEncryption', 'managePrincipals', 'configureBagDrop',
  ])
  assert.deepEqual(allowedActions(null), [])
})

test('pinBuckets permission mapping requires builder', () => {
  assert.equal(allowedActions('reader').includes('pinBuckets'), false)
  assert.equal(allowedActions('builder').includes('pinBuckets'), true)
})

test('revokeVersions permission mapping requires publisher', () => {
  assert.equal(allowedActions('reader').includes('revokeVersions'), false)
  assert.equal(allowedActions('builder').includes('revokeVersions'), false)
  assert.equal(allowedActions('publisher').includes('revokeVersions'), true)
  assert.equal(allowedActions('maintainer').includes('revokeVersions'), true)
})

test('manageChannels permission mapping requires publisher', () => {
  assert.equal(allowedActions('reader').includes('manageChannels'), false)
  assert.equal(allowedActions('builder').includes('manageChannels'), false)
  assert.equal(allowedActions('publisher').includes('manageChannels'), true)
  assert.equal(allowedActions('maintainer').includes('manageChannels'), true)
})

test('deleteBuckets permission mapping requires publisher', () => {
  assert.equal(allowedActions('reader').includes('deleteBuckets'), false)
  assert.equal(allowedActions('builder').includes('deleteBuckets'), false)
  assert.equal(allowedActions('publisher').includes('deleteBuckets'), true)
  assert.equal(allowedActions('maintainer').includes('deleteBuckets'), true)
})

test('each role gets the navigation snapshot declared by the server', () => {
  for (const [role, expected] of [
    [null, ['buckets', 'bagdrop', 'instance']],
    ['reader', ['buckets', 'bagdrop', 'instance']],
    ['builder', ['buckets', 'bagdrop', 'instance']],
    ['publisher', ['buckets', 'bagdrop', 'instance']],
    ['maintainer', ['buckets', 'principals', 'bagdrop', 'instance']],
    ['root', ['buckets', 'principals', 'audit', 'encryption', 'bagdrop', 'instance']],
  ]) {
    assert.deepEqual(visibleNavItems(role), expected, String(role))
  }
})
