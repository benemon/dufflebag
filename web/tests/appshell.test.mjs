import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter, Route, Routes, useParams } from 'react-router'
import { createServer } from 'vite'

let vite
let AppShellView
let ThemeToggleButton
let visibleNavItems
let BucketPickerView

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ AppShellView, ThemeToggleButton } = await vite.ssrLoadModule('/src/shell/AppShell.tsx'))
  ;({ BucketPickerView } = await vite.ssrLoadModule('/src/shell/TenantSwitcher.tsx'))
  ;({ visibleNavItems } = await vite.ssrLoadModule('/src/auth/permissions.ts'))
})

after(async () => {
  await vite.close()
})

const labels = ['Registry', 'Principals', 'Audit', 'Encryption', 'Bag Drop', 'Webhooks', 'Instance']
const shellSource = readFileSync(new URL('../src/shell/AppShell.tsx', import.meta.url), 'utf8')
const tenantSwitcherSource = readFileSync(
  new URL('../src/shell/TenantSwitcher.tsx', import.meta.url),
  'utf8',
)
const appSource = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8')
const screenHeaderSource = readFileSync(
  new URL('../src/components/ScreenHeader.tsx', import.meta.url),
  'utf8',
)
const headerScreenSources = [
  'Principals', 'Audit', 'Webhooks', 'Versions', 'Version', 'Instance', 'Encryption',
  'BagDrop',
].map((name) => [
  name,
  readFileSync(new URL(`../src/screens/${name}.tsx`, import.meta.url), 'utf8'),
])

const view = (role, pathname = '/', over = {}) => renderToStaticMarkup(React.createElement(
  MemoryRouter,
  { initialEntries: [pathname] },
  React.createElement(AppShellView, {
    pathname,
    visibleItems: visibleNavItems(role),
    ...over,
  }, React.createElement('main', null, 'Screen')),
))

test('the shell renders only navigation the caller can use', () => {
  for (const [role, expected] of [
    [null, ['Registry', 'Bag Drop', 'Instance']],
    ['reader', ['Registry', 'Bag Drop', 'Instance']],
    ['builder', ['Registry', 'Bag Drop', 'Instance']],
    ['publisher', ['Registry', 'Bag Drop', 'Instance']],
    ['maintainer', ['Registry', 'Principals', 'Bag Drop', 'Webhooks', 'Instance']],
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

test('bucket-scoped sessions swap Buckets for a Bucket entry pointing home', () => {
  assert.match(shellSource, /key: 'buckets', to: '\/buckets', label: 'Buckets'/)
  // Above-bucket sessions keep the listing entry untouched.
  assert.match(view('root', '/audit'), />Buckets</)

  // A bucket-scoped session is never stranded on an admin screen: the entry
  // renames to Bucket and points at the resolved bucket detail (duf-xmg5).
  const onInstance = view('builder', '/instance', {
    bucketNav: { to: '/buckets/base%20images' },
  })
  assert.match(onInstance, /<a(?=[^>]*href="\/buckets\/base%20images")[^>]*>(?:<span[^>]*>)?Bucket</)
  assert.doesNotMatch(onInstance, />Buckets</)
  // Not current while an admin screen is showing.
  assert.doesNotMatch(onInstance, /<a(?=[^>]*href="\/buckets\/base%20images")(?=[^>]*pf-m-current)[^>]*>/)

  // Current whenever the session is looking at its bucket: the detail route…
  const onDetail = view('builder', '/buckets/base%20images', {
    bucketNav: { to: '/buckets/base%20images' },
  })
  assert.match(onDetail, /<a(?=[^>]*href="\/buckets\/base%20images")(?=[^>]*class="[^"]*pf-m-current)[^>]*>/)
  // …and the landing redirect while the claim is still resolving, where the
  // entry falls back to the landing route rather than rendering a dead link.
  const resolving = view('builder', '/', { bucketNav: { to: '/' } })
  assert.match(resolving, /<a(?=[^>]*href="\/")(?=[^>]*class="[^"]*pf-m-current)[^>]*>(?:<span[^>]*>)?Bucket</)

  // NOT current on a sibling bucket route (reachable by URL, refused by the
  // server) nor on a name sharing a prefix — the current link must point at
  // the page being displayed.
  const onSibling = view('builder', '/buckets/sibling', {
    bucketNav: { to: '/buckets/base%20images' },
  })
  assert.doesNotMatch(onSibling, /pf-m-current/)
  const onPrefix = view('builder', '/buckets/base-images-nightly', {
    bucketNav: { to: '/buckets/base-images' },
  })
  assert.doesNotMatch(onPrefix, /pf-m-current/)

  // The derivation feeding bucketNav is pinned at source: the carried
  // selection must match the claim's bucket id, and the name is URL-encoded.
  assert.match(shellSource, /selectedBucket\.id === state\?\.claims\.bucketID/)
  assert.match(shellSource, /encodeURIComponent\(selectedBucket\.name\)/)
})

test('the landing routes by scope while bucket detail routes keep their paths', () => {
  // Above-bucket sessions land on the Buckets screen; a bucket-scoped session
  // lands in its one bucket (the Landing component encodes the split).
  assert.match(appSource, /<Route path="\/" element=\{<Landing \/>\}/)
  assert.match(appSource, /<Route path="\/buckets" element=\{<Buckets \/>\}/)
  assert.match(appSource, /Navigate to="\/buckets" replace/)
  assert.match(appSource, /claims.bucketID/)
  assert.match(appSource, /path="\/buckets\/:bucket"/)
  assert.match(appSource, /<Route path="versions\/:fingerprint" element=\{<Version \/>\}/)
  assert.match(appSource, /<Route path="versions\/:fingerprint\/builds\/:build" element=\{<Build \/>\}/)
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

test('typeahead pickers resync an unfiltered input while open', () => {
  // A refresh must replace a removed or renamed selection while open
  // (duf-b4wo) — firing on SELECTION change only: filter transitions must
  // neither refill a cleared search box nor race the label after a select.
  assert.match(tenantSwitcherSource, /if \(!open \|\| filterValue === ''\) setInputValue\(selectedLabel\)/)
  assert.match(tenantSwitcherSource, /\}, \[open, selectedLabel\]\)/)
})

test('the bucket picker renders its selection from the matched route parameter', () => {
  function PickerAtRoute() {
    const { bucket } = useParams()
    return React.createElement(BucketPickerView, {
      selectedBucket: bucket,
      buckets: [{ name: 'base images' }],
      pins: [], scoped: true, loading: false, failure: null, callerRole: 'builder',
      onRefresh: async () => [], onSelect: () => {}, onCreate: async () => {},
    })
  }
  const markup = renderToStaticMarkup(React.createElement(
    MemoryRouter,
    { initialEntries: ['/buckets/base%20images/versions/fp'] },
    React.createElement(
      Routes,
      null,
      React.createElement(Route, {
        path: '/buckets/:bucket/*',
        element: React.createElement(PickerAtRoute),
      }),
    ),
  ))
  assert.match(markup, /id="tenant-bucket"/)
  assert.match(markup, /value="base images"/)
})

test('the theme toggle flips its action label and persists the chosen override', () => {
  const writes = []
  const toggles = []
  let changed
  const button = ThemeToggleButton({
    theme: 'light',
    onThemeChange: (theme) => { changed = theme },
    storage: { setItem: (...args) => writes.push(args) },
    root: { classList: { toggle: (...args) => toggles.push(args) } },
  })
  assert.equal(button.props['aria-label'], 'Switch to dark theme')
  button.props.onClick()
  assert.deepEqual(writes, [['dufflebag-theme', 'dark']])
  assert.deepEqual(toggles, [['pf-v6-theme-dark', true]])
  assert.equal(changed, 'dark')

  const darkButton = ThemeToggleButton({
    theme: 'dark',
    onThemeChange: () => {},
    storage: { setItem: () => {} },
    root: { classList: { toggle: () => {} } },
  })
  assert.equal(darkButton.props['aria-label'], 'Switch to light theme')
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
  for (const name of ['Principals', 'Audit', 'Webhooks', 'Instance', 'Encryption', 'BagDrop']) {
    assert.doesNotMatch(sources[name], /<Breadcrumb/, `${name}: top-level destination`)
  }
})
