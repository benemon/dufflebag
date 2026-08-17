import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let SkeletonRows

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ SkeletonRows } = await vite.ssrLoadModule('/src/components/Loading.tsx'))
})

after(async () => {
  await vite.close()
})

const source = (path) => readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')

const skeletonSites = new Map([
  ['screens/Versions.tsx', ['Loading versions…', 'Loading assignment history…']],
  ['screens/Version.tsx', ['Loading version…']],
  ['screens/Build.tsx', ['Loading build…']],
  ['screens/Principals.tsx', ['Loading principals…']],
  ['screens/Audit.tsx', ['Loading audit targets…']],
  ['screens/Webhooks.tsx', ['Loading webhooks…']],
  ['screens/BagDrop.tsx', [
    'Loading Bag Drop configuration…',
    'Loading mirrored buckets…',
    'Loading Bag Drop status…',
  ]],
  ['screens/Encryption.tsx', ['Loading encryption state…']],
  ['screens/Instance.tsx', [
    'Loading scanner information…',
    'Loading build information…',
  ]],
])

test('skeleton rows hold four lines and expose their exact loading sentence once', () => {
  const markup = renderToStaticMarkup(React.createElement(SkeletonRows, {
    screenreaderText: 'Loading the listing…',
  }))
  assert.equal((markup.match(/<div class="pf-v6-c-skeleton"/g) ?? []).length, 4)
  assert.equal((markup.match(/Loading the listing…/g) ?? []).length, 1)
  assert.match(markup, /pf-v6-screen-reader">Loading the listing…<\/span>/)
})

test('every settled listing wait uses skeleton rows with its exact screen-reader copy', () => {
  for (const [path, sentences] of skeletonSites) {
    const contents = source(path)
    assert.doesNotMatch(contents, /<Content component="p">Loading /, path)
    for (const sentence of sentences) {
      assert.ok(
        contents.includes(`<SkeletonRows screenreaderText="${sentence}" />`),
        `${path} does not pin ${sentence}`,
      )
    }
  }
})

test('whole-page waits pair a spinner with visible loading copy', () => {
  const app = source('App.tsx')
  for (const sentence of ['Restoring your session…', 'Loading projects…']) {
    assert.ok(app.includes(`<Spinner aria-label="${sentence}" />`))
    assert.ok(app.includes(`<Content component="p">${sentence}</Content>`))
  }

  const login = source('screens/Login.tsx')
  assert.ok(login.includes('loginSubtitle="Checking this instance…"'))
  assert.ok(login.includes('<Spinner aria-label="Checking this instance…" />'))
})
