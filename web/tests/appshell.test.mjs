import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { createServer } from 'vite'

let vite
let AppShellView
let visibleNavItems

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ AppShellView } = await vite.ssrLoadModule('/src/shell/AppShell.tsx'))
  ;({ visibleNavItems } = await vite.ssrLoadModule('/src/auth/permissions.ts'))
})

after(async () => {
  await vite.close()
})

const labels = ['Buckets', 'Principals', 'Audit', 'Encryption', 'Bag Drop', 'Webhooks', 'Instance']
const shellSource = readFileSync(new URL('../src/shell/AppShell.tsx', import.meta.url), 'utf8')
const screenHeaderSource = readFileSync(
  new URL('../src/components/ScreenHeader.tsx', import.meta.url),
  'utf8',
)
const headerScreenSources = [
  'Principals', 'Audit', 'Webhooks', 'Versions', 'Version', 'Buckets', 'Instance', 'Encryption',
  'BagDrop',
].map((name) => [
  name,
  readFileSync(new URL(`../src/screens/${name}.tsx`, import.meta.url), 'utf8'),
])

const view = (role, pathname = '/buckets') => renderToStaticMarkup(React.createElement(
  MemoryRouter,
  { initialEntries: [pathname] },
  React.createElement(AppShellView, {
    pathname,
    visibleItems: visibleNavItems(role),
  }, React.createElement('main', null, 'Screen')),
))

test('the shell renders only navigation the caller can use', () => {
  for (const [role, expected] of [
    [null, ['Buckets', 'Bag Drop', 'Instance']],
    ['reader', ['Buckets', 'Bag Drop', 'Instance']],
    ['builder', ['Buckets', 'Bag Drop', 'Instance']],
    ['publisher', ['Buckets', 'Bag Drop', 'Instance']],
    ['maintainer', ['Buckets', 'Principals', 'Bag Drop', 'Webhooks', 'Instance']],
    ['root', labels],
  ]) {
    const markup = view(role)
    for (const label of labels) {
      if (expected.includes(label)) assert.match(markup, new RegExp(label), `${role}: ${label}`)
      else assert.doesNotMatch(markup, new RegExp(label), `${role}: ${label}`)
    }
    // Administration renders for every role: reader-tier Instance lives there,
    // so the group is never empty — filtering changes its contents, not the
    // shipped grouping.
    assert.match(markup, /Administration/)
  }
})

test('router links carry PatternFly native current navigation state', () => {
  const markup = view('root', '/audit')
  assert.match(
    markup,
    /<a(?=[^>]*href="\/audit")(?=[^>]*aria-current="page")(?=[^>]*class="[^"]*pf-v6-c-nav__link pf-m-current[^"]*")[^>]*>/,
  )
  assert.doesNotMatch(markup, /class="[^"]*\bnv\b/)
  for (const destination of ['/buckets', '/principals', '/audit', '/encryption', '/bagdrop', '/webhooks', '/instance']) {
    assert.match(markup, new RegExp(`<a[^>]*href="${destination}"`), `${destination} is not a focusable link`)
  }
})

test('the masthead uses the PatternFly brand slot for the lowercase wordmark', () => {
  assert.match(shellSource, /<MastheadLogo className="app-wordmark">dufflebag<\/MastheadLogo>/)
  assert.doesNotMatch(shellSource, /<span style=\{\{ fontSize:/)
})

test('the masthead keeps the labelled tenancy pickers in one toolbar item', () => {
  assert.match(
    shellSource,
    /<ToolbarItem className="tenant-switcher-item">[\s\S]*?<TenantSwitcher \/>/,
  )
})

test('settled screen headers cannot drift back to inline header shapes', () => {
  assert.match(screenHeaderSource, /<PageSection variant="default">/)
  assert.match(screenHeaderSource, /<Title headingLevel="h1" size="2xl">/)
  assert.match(screenHeaderSource, /display: 'flex', gap: 8, alignItems: 'flex-start'/)
  for (const [name, source] of headerScreenSources) {
    assert.equal((source.match(/<ScreenHeader\b/g) ?? []).length, 1, `${name}: shared header`)
    assert.doesNotMatch(source, /<PageSection[^>]*variant="default"/, `${name}: inline header`)
  }
})

test('tenancy context stays in the masthead, not top-level screen breadcrumbs', () => {
  const sources = Object.fromEntries(headerScreenSources)
  assert.doesNotMatch(sources.Buckets, /Project registry|<Breadcrumb/)
  for (const name of ['Principals', 'Audit', 'Webhooks', 'Instance', 'Encryption', 'BagDrop']) {
    assert.doesNotMatch(sources[name], /<Breadcrumb/, `${name}: top-level destination`)
  }
})
