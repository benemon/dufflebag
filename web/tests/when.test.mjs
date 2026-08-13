import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let When

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ When } = await vite.ssrLoadModule('/src/components/When.tsx'))
})

after(async () => {
  await vite.close()
})

const render = (props) => renderToStaticMarkup(React.createElement(When, props))

test('a timestamp keeps the full API precision on its semantic time element', () => {
  const iso = '2026-08-13T07:41:30.702958Z'
  const markup = render({ iso })
  assert.match(markup, /<time\b/)
  assert.match(markup, new RegExp(`dateTime="${iso}"`))
})

test('an empty timestamp renders the requested plain text', () => {
  assert.equal(render({ iso: '' }), '—')
  assert.equal(render({ iso: null, emptyText: 'Never' }), 'Never')
})

test('dateOnly omits time formatting', () => {
  const calls = []
  const original = Date.prototype.toLocaleString
  Date.prototype.toLocaleString = function (locale, options) {
    calls.push(options)
    return original.call(this, locale, options)
  }
  try {
    render({ iso: '2026-08-13T07:41:30.702958Z', dateOnly: true })
  } finally {
    Date.prototype.toLocaleString = original
  }
  assert.ok(calls.length > 0)
  assert.ok(calls.every((options) => options.dateStyle === 'medium'))
  assert.ok(calls.every((options) => options.timeStyle === undefined))
})
