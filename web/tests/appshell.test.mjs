import assert from 'node:assert/strict'
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

const labels = ['Buckets', 'Principals', 'Audit', 'Encryption', 'Bag Drop', 'Instance']

const view = (role) => renderToStaticMarkup(React.createElement(
  MemoryRouter,
  { initialEntries: ['/buckets'] },
  React.createElement(AppShellView, {
    pathname: '/buckets',
    visibleItems: visibleNavItems(role),
  }, React.createElement('main', null, 'Screen')),
))

test('the shell renders only navigation the caller can use', () => {
  for (const [role, expected] of [
    [null, ['Buckets', 'Bag Drop', 'Instance']],
    ['reader', ['Buckets', 'Bag Drop', 'Instance']],
    ['builder', ['Buckets', 'Bag Drop', 'Instance']],
    ['publisher', ['Buckets', 'Bag Drop', 'Instance']],
    ['maintainer', ['Buckets', 'Principals', 'Bag Drop', 'Instance']],
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
