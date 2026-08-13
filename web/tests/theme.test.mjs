import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let applyTheme
let DARK_THEME_CLASS
let InstanceView
let resolveTheme
let watchSystemTheme

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ applyTheme, DARK_THEME_CLASS, resolveTheme, watchSystemTheme } =
    await vite.ssrLoadModule('/src/theme/theme.ts'))
  ;({ InstanceView } = await vite.ssrLoadModule('/src/screens/Instance.tsx'))
})

after(async () => {
  await vite.close()
})

test('a stored theme overrides the system and an absent override follows it', () => {
  assert.equal(resolveTheme('light', true), 'light')
  assert.equal(resolveTheme('dark', false), 'dark')
  assert.equal(resolveTheme(null, true), 'dark')
  assert.equal(resolveTheme(null, false), 'light')
  assert.equal(resolveTheme('unexpected', true), 'dark')
})

test('the initial theme is applied in the document head before the app loads', () => {
  const index = readFileSync(new URL('../index.html', import.meta.url), 'utf8')
  const boot = index.indexOf("localStorage.getItem('dufflebag-theme')")
  assert.ok(boot > 0)
  assert.ok(boot < index.indexOf('</head>'))
  assert.ok(boot < index.indexOf('/src/main.tsx'))
  assert.match(index, /matchMedia\('\(prefers-color-scheme: dark\)'\)/)
  assert.match(index, /classList\.toggle\('pf-v6-theme-dark', dark\)/)
})

test('applying a theme toggles the single PatternFly dark class', () => {
  const calls = []
  const root = { classList: { toggle: (...args) => calls.push(args) } }
  applyTheme('dark', root)
  applyTheme('light', root)
  assert.deepEqual(calls, [
    ['pf-v6-theme-dark', true],
    ['pf-v6-theme-dark', false],
  ])
})

test('system preference changes apply only while no override is stored', () => {
  let listener
  let removed
  let stored = null
  const themes = []
  const media = {
    addEventListener: (name, callback) => {
      assert.equal(name, 'change')
      listener = callback
    },
    removeEventListener: (name, callback) => {
      assert.equal(name, 'change')
      removed = callback
    },
  }
  const stop = watchSystemTheme(media, () => stored, (theme) => themes.push(theme))
  listener({ matches: true })
  listener({ matches: false })
  stored = 'dark'
  listener({ matches: false })
  assert.deepEqual(themes, ['dark', 'light'])
  stop()
  assert.equal(removed, listener)
})

test('a representative screen renders beneath the PatternFly dark class', () => {
  const markup = renderToStaticMarkup(React.createElement(
    'div',
    { className: DARK_THEME_CLASS },
    React.createElement(InstanceView, {
      host: 'registry.example',
      secure: true,
      organizationID: 'org-id',
      projectID: 'project-id',
      instance: null,
      loading: true,
      failure: null,
    }),
  ))
  assert.match(markup, /^<div class="pf-v6-theme-dark">/)
  assert.match(markup, />Instance<\/h1>/)
  assert.match(markup, /Loading build information…/)
})
