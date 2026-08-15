import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import { createServer } from 'vite'

let vite
let refreshDelay
const pollingSource = readFileSync(new URL('../src/data/polling.ts', import.meta.url), 'utf8')

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
  })
  ;({ refreshDelay } = await vite.ssrLoadModule('/src/data/polling.ts'))
})

after(async () => { await vite.close() })

test('MUTATION_POLL_CADENCE chooses hot and settled refresh delays', () => {
  assert.equal(refreshDelay(true, true), 5_000)
  assert.equal(refreshDelay(false, true), 30_000)
})

test('MUTATION_POLL_VISIBILITY pauses refresh while the document is hidden', () => {
  assert.equal(refreshDelay(true, false), null)
  assert.equal(refreshDelay(false, false), null)
})

test('the hook refreshes on return to visible and owns timer cleanup', () => {
  assert.match(pollingSource, /addEventListener\('visibilitychange', visibilityChanged\)/)
  assert.match(
    pollingSource,
    /visibilityChanged = \(\) => \{[\s\S]*?visibilityState !== 'hidden'\) refresh\.current\(\)/,
  )
  assert.match(
    pollingSource,
    /return \(\) => \{[\s\S]*?removeEventListener\('visibilitychange'[\s\S]*?clearInterval\(timer\)/,
  )
})
