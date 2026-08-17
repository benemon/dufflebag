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
  'Versions', 'Version', 'Build', 'Audit', 'Webhooks', 'Principals', 'Encryption', 'Instance',
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

test('registry refresh indicators never replace settled data with skeletons', () => {
  for (const name of ['Versions', 'Version', 'Build']) {
    const source = readFileSync(new URL(`../src/screens/${name}.tsx`, import.meta.url), 'utf8')
    assert.match(source, /refreshing=\{refreshing\}/, `${name}: header uses refreshing`)
    assert.match(source, /\{loading \? \([\s\S]*?<SkeletonRows\b/, `${name}: skeleton uses loading`)
    assert.doesNotMatch(source, /\{refreshing \?/, `${name}: refreshing is not a render branch`)
  }
})

test('MUTATION_BUILD_REFRESH_BINDING keeps Build wired to its live quiet reload', () => {
  const source = readFileSync(new URL('../src/screens/Build.tsx', import.meta.url), 'utf8')
  assert.match(source, /const \{ data, loading, refreshing, failure, gap, reload \} = useBuild\(/)
  assert.match(source, /<BuildView[\s\S]*?onRefresh=\{reload\}/)
  assert.match(source, /<ScreenHeader[\s\S]*?onRefresh=\{onRefresh\}/)
})

test('MUTATION_MANUAL_QUIET keeps manual registry refreshes on revision reloads', () => {
  const picker = readFileSync(new URL('../src/data/bucketPicker.ts', import.meta.url), 'utf8')
  const versions = readFileSync(new URL('../src/data/versions.ts', import.meta.url), 'utf8')
  assert.match(picker, /export function useBucketPicker\(/)
  assert.match(picker, /listBuckets\(token, tenant\)/)
  assert.match(picker, /listPins\(token, tenant\)/)
  assert.doesNotMatch(picker, /listVersions|listChannels/)
  for (const hook of ['useVersions', 'useVersion', 'useBuild']) {
    assert.match(
      versions,
      new RegExp(`export function ${hook}\\([\\s\\S]*?setRevision\\(\\(current\\) => current \\+ 1\\)`),
      `${hook}: revision reload`,
    )
  }
})
