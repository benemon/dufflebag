import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import { createServer } from 'vite'

let vite
let ApiError
let deleteBucket

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
  })
  ;({ ApiError, deleteBucket } = await vite.ssrLoadModule('/src/api/client.ts'))
})

after(async () => { await vite.close() })

const tenant = { organizationID: 'org', projectID: 'project' }

const withResponse = async (response, run) => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async () => response
  try {
    await run()
  } finally {
    globalThis.fetch = originalFetch
  }
}

test('gateway failures explain that the action outcome is unknown', async () => {
  await withResponse(new Response('', {
    status: 504,
    headers: { 'Content-Type': 'text/plain' },
  }), async () => {
    await assert.rejects(deleteBucket('token', tenant, 'images'), (error) => {
      assert.ok(error instanceof ApiError)
      assert.equal(error.status, 504)
      assert.equal(
        error.message,
        '504: dufflebag did not answer before the gateway gave up. Reload to see whether the action took effect.',
      )
      return true
    })
  })
})

test('server messages take precedence over status fallbacks', async () => {
  await withResponse(new Response(JSON.stringify({ message: 'publisher role required' }), {
    status: 403,
    headers: { 'Content-Type': 'application/json' },
  }), async () => {
    await assert.rejects(deleteBucket('token', tenant, 'images'), (error) => {
      assert.ok(error instanceof ApiError)
      assert.equal(error.status, 403)
      assert.equal(error.message, 'publisher role required')
      return true
    })
  })
})
