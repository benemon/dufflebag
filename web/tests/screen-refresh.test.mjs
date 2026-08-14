import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let ScreenHeader

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ ScreenHeader } = await vite.ssrLoadModule('/src/components/ScreenHeader.tsx'))
})

after(async () => { await vite.close() })

const renderHeader = (props) => renderToStaticMarkup(
  React.createElement(ScreenHeader, { title: 'Test screen', ...props }),
)

test('ScreenHeader omits refresh unless onRefresh is supplied', () => {
  assert.doesNotMatch(renderHeader({}), /aria-label="Refresh"/)
})

test('ScreenHeader renders an enabled refresh button when onRefresh is supplied', () => {
  const markup = renderHeader({ onRefresh: () => {} })
  assert.match(markup, /<button(?=[^>]*aria-label="Refresh")(?![^>]*disabled)[^>]*>/)
})

test('ScreenHeader disables refresh while refreshing', () => {
  const markup = renderHeader({ onRefresh: () => {}, refreshing: true })
  assert.match(markup, /<button(?=[^>]*aria-label="Refresh")(?=[^>]*disabled)[^>]*>/)
})

const refreshScreens = [
  'Buckets', 'Versions', 'Version', 'Audit', 'Webhooks', 'Principals', 'Encryption', 'Instance',
  'BagDrop',
]

test('every volatile screen passes onRefresh to ScreenHeader', () => {
  for (const name of refreshScreens) {
    const source = readFileSync(new URL(`../src/screens/${name}.tsx`, import.meta.url), 'utf8')
    assert.match(source, /<ScreenHeader\b[\s\S]*?\bonRefresh=\{/, `${name}: refresh wiring`)
    // A forwarding-only file means the container binding was lost and the
    // button is dead while this scan stays green — require at least one
    // binding that is not pure prop forwarding (caught in review: removing
    // the container's loader binding left 311 tests passing).
    assert.match(
      source,
      /onRefresh=\{(?!onRefresh\})(?!props\.onRefresh\})/,
      `${name}: live loader binding`,
    )
  }
})
