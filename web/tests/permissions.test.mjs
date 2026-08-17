import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import { createServer } from 'vite'

let vite
let allowedActions
let permitsAction
let requirementReason
let visibleNavItems

before(async () => {
  vite = await createServer({
    root: process.cwd(), logLevel: 'silent',
    server: { middlewareMode: true }, appType: 'custom',
  })
  ;({ allowedActions, permitsAction, requirementReason, visibleNavItems } =
    await vite.ssrLoadModule('/src/auth/permissions.ts'))
})

after(async () => { await vite.close() })

test('each role gets the nested console action snapshot declared by the server', () => {
  assert.deepEqual(allowedActions('reader'), [])
  assert.deepEqual(allowedActions('builder'), ['pinBuckets', 'createBuckets'])
  assert.deepEqual(
    allowedActions('publisher'),
    ['pinBuckets', 'createBuckets', 'revokeVersions', 'deleteVersions', 'manageChannels', 'deleteBuckets'],
  )
  assert.deepEqual(
    allowedActions('maintainer'),
    [
      'createProjects',
      'pinBuckets', 'createBuckets', 'revokeVersions', 'deleteVersions', 'manageChannels',
      'manageRestrictedChannels', 'deleteBuckets',
      'managePrincipals', 'configureBagDrop', 'configureWebhooks',
    ],
  )
  assert.deepEqual(allowedActions('root'), [
    'createOrganizations', 'createProjects', 'pinBuckets', 'createBuckets', 'revokeVersions', 'deleteVersions',
    'manageChannels', 'manageRestrictedChannels', 'deleteBuckets', 'configureAudit',
    'manageEncryption', 'managePrincipals', 'configureBagDrop', 'configureWebhooks',
  ])
  assert.deepEqual(allowedActions(null), [])
})

test('createOrganizations permission mapping requires root', () => {
  assert.equal(allowedActions('maintainer').includes('createOrganizations'), false)
  assert.equal(allowedActions('root').includes('createOrganizations'), true)
})

test('createProjects permission mapping requires maintainer', () => {
  assert.equal(allowedActions('publisher').includes('createProjects'), false)
  assert.equal(allowedActions('maintainer').includes('createProjects'), true)
})

test('pinBuckets permission mapping requires builder', () => {
  assert.equal(allowedActions('reader').includes('pinBuckets'), false)
  assert.equal(allowedActions('builder').includes('pinBuckets'), true)
})

test('createBuckets permission mapping requires builder', () => {
  assert.equal(allowedActions('reader').includes('createBuckets'), false)
  assert.equal(allowedActions('builder').includes('createBuckets'), true)
})

test('revokeVersions permission mapping requires publisher', () => {
  assert.equal(allowedActions('reader').includes('revokeVersions'), false)
  assert.equal(allowedActions('builder').includes('revokeVersions'), false)
  assert.equal(allowedActions('publisher').includes('revokeVersions'), true)
  assert.equal(allowedActions('maintainer').includes('revokeVersions'), true)
})

test('deleteVersions permission mapping requires publisher', () => {
  assert.equal(allowedActions('reader').includes('deleteVersions'), false)
  assert.equal(allowedActions('builder').includes('deleteVersions'), false)
  assert.equal(allowedActions('publisher').includes('deleteVersions'), true)
  assert.equal(allowedActions('maintainer').includes('deleteVersions'), true)
})

test('manageChannels permission mapping requires publisher', () => {
  assert.equal(allowedActions('reader').includes('manageChannels'), false)
  assert.equal(allowedActions('builder').includes('manageChannels'), false)
  assert.equal(allowedActions('publisher').includes('manageChannels'), true)
  assert.equal(allowedActions('maintainer').includes('manageChannels'), true)
})

test('restricted-channel management requires maintainer', () => {
  assert.equal(permitsAction('publisher', 'manageRestrictedChannels'), false)
  assert.equal(permitsAction('maintainer', 'manageRestrictedChannels'), true)
  assert.equal(requirementReason('manageRestrictedChannels'), 'Requires maintainer')
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
    ['maintainer', ['buckets', 'principals', 'bagdrop', 'webhooks', 'instance']],
    ['root', ['buckets', 'principals', 'audit', 'encryption', 'bagdrop', 'webhooks', 'instance']],
  ]) {
    assert.deepEqual(visibleNavItems(role), expected, String(role))
  }
})
