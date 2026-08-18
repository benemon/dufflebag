import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

const initializeSource = readFileSync(new URL('../src/screens/Initialize.tsx', import.meta.url), 'utf8')
const creationSource = readFileSync(
  new URL('../src/components/TenancyCreation.tsx', import.meta.url),
  'utf8',
)
const gapScreenSources = ['Versions.tsx', 'Version.tsx', 'Build.tsx'].map((screen) =>
  readFileSync(new URL(`../src/screens/${screen}`, import.meta.url), 'utf8'))

let vite
let TenancyForm
let TenancyModalView
let TenancyGapEmptyState
let CreateTenancyButton
let refreshThenSelect
let NoProjectsYet
let platformTenancyGap

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ TenancyForm } = await vite.ssrLoadModule('/src/components/TenancyForm.tsx'))
  ;({ TenancyModalView, TenancyGapEmptyState, CreateTenancyButton, refreshThenSelect } =
    await vite.ssrLoadModule('/src/components/TenancyCreation.tsx'))
  ;({ NoProjectsYet } = await vite.ssrLoadModule('/src/App.tsx'))
  ;({ platformTenancyGap } = await vite.ssrLoadModule('/src/data/tenant.ts'))
})

after(async () => { await vite.close() })

test('wizard project and tenancy modal share TenancyForm', () => {
  assert.match(initializeSource, /<TenancyForm[\s\S]*?kind="organization"/)
  assert.match(initializeSource, /<TenancyForm[\s\S]*?kind="project"/)
  assert.doesNotMatch(initializeSource, /<TextInput/)
  assert.match(creationSource, /<TenancyForm/)

  const organization = renderToStaticMarkup(React.createElement(TenancyForm, {
    kind: 'organization', formID: 'organization', submitLabel: 'Create organisation',
    submitting: false, footer: 'modal', onSubmit: async () => {}, onCancel: () => {},
  }))
  const project = renderToStaticMarkup(React.createElement(TenancyForm, {
    kind: 'project', formID: 'project', submitLabel: 'Create project',
    submitting: false, footer: 'modal', onSubmit: async () => {}, onCancel: () => {},
  }))
  assert.match(organization, /Contains projects and their principals\. The name cannot be changed later\./)
  assert.match(project, /Scopes buckets, principals and channels\. The name cannot be changed later\./)
  for (const markup of [organization, project]) {
    assert.match(markup, /<button[^>]*disabled[^>]*>/)
  }
})

const findElement = (node, predicate) => {
  if (Array.isArray(node)) {
    for (const child of node) {
      const found = findElement(child, predicate)
      if (found) return found
    }
    return null
  }
  if (!React.isValidElement(node)) return null
  if (predicate(node)) return node
  return findElement(node.props.children, predicate)
}

test('tenancy modal renders an inline danger Alert on failure', () => {
  const tree = TenancyModalView({
    kind: 'project', submitting: false, failure: 'name already exists',
    onSubmit: async () => {}, onClose: () => {},
  })
  const form = findElement(tree, (element) => element.type === TenancyForm)
  assert.ok(form)
  assert.equal(form.props.message.props.variant, 'danger')
  assert.equal(form.props.message.props.isInline, true)
  const markup = renderToStaticMarkup(form.props.message)
  assert.match(markup, /The tenancy could not be created/)
  assert.match(markup, /name already exists/)
})

test('creation buttons expose the settled role requirements', () => {
  const organization = renderToStaticMarkup(React.createElement(CreateTenancyButton, {
    kind: 'organization', callerRole: 'maintainer', onOpen: () => {}, variant: 'link',
  }))
  const project = renderToStaticMarkup(React.createElement(CreateTenancyButton, {
    kind: 'project', callerRole: 'reader', organizationID: 'org-1',
    onOpen: () => {}, variant: 'link',
  }))
  assert.match(organization, /Create organisation/)
  assert.match(organization, /Requires root/)
  assert.match(project, /Create project/)
  assert.match(project, /Requires maintainer/)
})

test('created project is selected after its refreshed listing includes it', async () => {
  const events = []
  const created = { id: 'project-new', name: 'new', created_at: '2026-08-13T00:00:00Z' }
  await refreshThenSelect(
    created,
    async () => {
      events.push('refresh')
      return [created]
    },
    (project) => events.push(`select:${project.id}`),
  )
  assert.deepEqual(events, ['refresh', 'select:project-new'])
})

test('no-project gate is an actionable EmptyState', () => {
  const markup = renderToStaticMarkup(React.createElement(NoProjectsYet, {
    callerRole: 'maintainer', organizationID: 'org-1',
  }))
  assert.match(markup, /<h2[^>]*>No projects yet<\/h2>/)
  assert.match(markup, /A project scopes buckets, principals and channels\./)
  assert.match(markup, />Create project</)
  assert.doesNotMatch(markup, /No projects are available to this principal/)
})

test('every tenancy-gap data screen uses the actionable EmptyState', () => {
  for (const source of gapScreenSources) {
    assert.match(source, /<TenancyGapEmptyState gap=\{gap\}/)
    assert.doesNotMatch(source, /title=\{gap\.title\}/)
  }
  const gap = platformTenancyGap({
    platform: false, organizationCount: 0,
    selectedOrganization: 'org-1', projectCount: 0, selectedProject: null,
  })
  const markup = renderToStaticMarkup(React.createElement(TenancyGapEmptyState, {
    gap, callerRole: 'maintainer',
  }))
  assert.match(markup, /No projects in this organisation/)
  assert.match(markup, /Buckets live inside a project/)
  assert.match(markup, />Create project</)
  assert.doesNotMatch(markup, /Requires maintainer/)
})
