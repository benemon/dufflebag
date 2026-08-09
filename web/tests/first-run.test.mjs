import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let ServicePrincipalForm
let Initialize
let createOrganization
let createProject

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ ServicePrincipalForm } = await vite.ssrLoadModule('/src/screens/Login.tsx'))
  ;({ Initialize } = await vite.ssrLoadModule('/src/screens/Initialize.tsx'))
  ;({ createOrganization, createProject } = await vite.ssrLoadModule('/src/api/client.ts'))
})

after(async () => {
  await vite.close()
})

const form = (initial) => renderToStaticMarkup(React.createElement(ServicePrincipalForm, {
  initial, onSignIn: async () => {}, onInitialize: () => {},
}))

// duf-9rr. There is no recovery from losing the bootstrap secret: /init refuses
// a claimed instance, the hash is one-way, and the last root cannot be deleted.
// The form pre-fills the secret, so first run can be completed without ever
// copying it — and the token is memory-only, so the first reload is fatal.
//
// Signing in is not the dangerous step. Reloading afterwards is. The gate sits
// here because this is the last moment the secret is on screen.
test('first-run sign-in is gated on acknowledging the secret is stored', () => {
  const firstRun = form({ client_id: 'client-1', client_secret: 'PLAINTEXT' })
  assert.match(firstRun, /I have stored this client secret/)
  assert.match(firstRun, /direct database access/)
  // Unticked, so the submit button must be disabled.
  assert.match(firstRun, /<button[^>]*disabled[^>]*>[\s\S]{0,120}Log in/)
})

// An ordinary sign-in carries no secret the operator could lose, so the gate
// would be noise. Asking every time trains people to click past it.
test('an ordinary sign-in is not gated', () => {
  const ordinary = form(null)
  assert.doesNotMatch(ordinary, /I have stored this client secret/)
})

test('the first-run screen presents the prototype three-step structure', () => {
  const wizard = renderToStaticMarkup(React.createElement(Initialize, { onDone: () => {} }))
  assert.match(wizard, /Three steps: mint the administrative principal/)
  assert.match(wizard, /Initialization progress/)
  assert.match(wizard, />Initialize</)
  assert.match(wizard, />Organization</)
  assert.match(wizard, />Project</)
  assert.match(wizard, /Initialize this instance/)
  assert.match(wizard, /creates only the first root principal/)
})

test('tenancy steps use the authenticated platform endpoint shapes', async () => {
  const originalFetch = globalThis.fetch
  const calls = []
  globalThis.fetch = async (path, options) => {
    calls.push({ path, options })
    const body = String(path).endsWith('/projects')
      ? { id: 'project-1', name: 'registry', created_at: '2026-08-01T00:00:01Z' }
      : { id: 'organization-1', name: 'orbital', created_at: '2026-08-01T00:00:00Z' }
    return new Response(JSON.stringify(body), {
      status: 201,
      headers: { 'Content-Type': 'application/json' },
    })
  }
  try {
    await createOrganization('root-token', 'orbital')
    await createProject('root-token', 'organization-1', 'registry')
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.deepEqual(calls.map(({ path, options }) => ({
    path,
    method: options.method,
    authorization: options.headers.Authorization,
    body: options.body,
  })), [
    {
      path: '/api/v1/organizations',
      method: 'POST',
      authorization: 'Bearer root-token',
      body: JSON.stringify({ name: 'orbital' }),
    },
    {
      path: '/api/v1/organizations/organization-1/projects',
      method: 'POST',
      authorization: 'Bearer root-token',
      body: JSON.stringify({ name: 'registry' }),
    },
  ])
})
