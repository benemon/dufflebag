import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let BucketPickerView
let selectTenantProject
let ApiError
let signOutIfUnauthorized
let ProjectLoadFailure
let SignOutButton

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ BucketPickerView } = await vite.ssrLoadModule('/src/shell/TenantSwitcher.tsx'))
  ;({ selectTenantProject } = await vite.ssrLoadModule('/src/data/tenant.ts'))
  ;({ ApiError, signOutIfUnauthorized } = await vite.ssrLoadModule('/src/api/client.ts'))
  ;({ ProjectLoadFailure } = await vite.ssrLoadModule('/src/App.tsx'))
  ;({ SignOutButton } = await vite.ssrLoadModule('/src/shell/AppShell.tsx'))
})

after(async () => { await vite.close() })

test('bucket and project failures are visible instead of successful empty states', () => {
  const bucketMarkup = renderToStaticMarkup(React.createElement(BucketPickerView, {
    buckets: [], pins: [], scoped: true, loading: false,
    failure: '500 from /packer/buckets', callerRole: 'reader',
    onRefresh: async () => [], onSelect: () => {}, onCreate: async () => {},
  }))
  assert.match(bucketMarkup, /Buckets could not be loaded/)
  assert.doesNotMatch(bucketMarkup, /No buckets exist|tenant-bucket/)

  const projectMarkup = renderToStaticMarkup(React.createElement(ProjectLoadFailure, {
    failure: '503 from /resource-manager/projects',
  }))
  assert.match(projectMarkup, /Projects could not be loaded/)
  assert.match(projectMarkup, /503 from \/resource-manager\/projects/)
})

test('tenant selection passes the project UUID, not its display name', () => {
  let selected = ''
  selectTenantProject({
    id: 'org/project-id',
    organization: 'org',
    projectID: 'project-id',
    project: 'friendly project name',
  }, (projectID) => { selected = projectID })
  assert.equal(selected, 'project-id')
})

test('a 401 triggers the shared sign-out path', () => {
  let signedOut = 0
  assert.equal(signOutIfUnauthorized(new ApiError(401, 'expired'), () => { signedOut++ }), true)
  assert.equal(signedOut, 1)
  assert.equal(signOutIfUnauthorized(new ApiError(500, 'failed'), () => { signedOut++ }), false)
  assert.equal(signedOut, 1)
})

test('the visible sign-out control invokes sign out', () => {
  let signedOut = 0
  const button = SignOutButton({ signOut: () => { signedOut++ } })
  assert.equal(button.props.children, 'Sign out')
  button.props.onClick()
  assert.equal(signedOut, 1)
})
