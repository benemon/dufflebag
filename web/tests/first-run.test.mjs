import assert from 'node:assert/strict'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let AuthContext
let Login
let ServicePrincipalForm
let Initialize
let StoreCredentials
let StoreCredentialsFooter
let credentialsFileContent
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
  ;({ AuthContext } = await vite.ssrLoadModule('/src/auth/AuthContext.tsx'))
  ;({ Login, ServicePrincipalForm } = await vite.ssrLoadModule('/src/screens/Login.tsx'))
  ;({
    Initialize,
    StoreCredentials,
    StoreCredentialsFooter,
    credentialsFileContent,
  } = await vite.ssrLoadModule('/src/screens/Initialize.tsx'))
  ;({ createOrganization, createProject } = await vite.ssrLoadModule('/src/api/client.ts'))
})

after(async () => {
  await vite.close()
})

const form = (initial) => renderToStaticMarkup(React.createElement(ServicePrincipalForm, {
  initial, onSignIn: async () => {}, onInitialize: () => {},
}))

const credentials = {
  client_id: 'client-1',
  client_secret: 'PLAINTEXT',
  recovery_shares: ['share-one', 'share-two'],
  recovery_threshold: 2,
}

const storeCredentials = () => renderToStaticMarkup(React.createElement(StoreCredentials, {
  credentials,
  stored: false,
  onStoredChange: () => {},
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

test('the first-run screen uses the bootstrap shell and four-step wizard', () => {
  const wizard = renderToStaticMarkup(React.createElement(Initialize, {
    host: 'dufflebag.test',
    onDone: () => {},
  }))
  assert.match(wizard, /pf-v6-c-login dfbg-bootstrap/)
  assert.match(wizard, />dufflebag</)
  assert.match(wizard, /dufflebag\.test/)
  assert.match(wizard, /Mint the administrative principal, store its credentials/)
  assert.match(wizard, /Initialization progress/)
  assert.match(wizard, />Initialize</)
  assert.match(wizard, />Store credentials</)
  assert.match(wizard, />Organization</)
  assert.match(wizard, />Project</)
  assert.match(wizard, /Initialize this instance/)
  assert.match(wizard, /creates only the first root principal/)
  assert.doesNotMatch(wizard, />Next<|>Back<|>Cancel</)
})

test('sign-in and bootstrap use the lowercase wordmark', () => {
  const signIn = renderToStaticMarkup(React.createElement(
    AuthContext.Provider,
    { value: { signIn: async () => {}, sessionEnded: false } },
    React.createElement(Login),
  ))
  const bootstrap = renderToStaticMarkup(React.createElement(Initialize, {
    host: 'dufflebag.test',
    onDone: () => {},
  }))
  assert.match(signIn, />dufflebag</)
  assert.match(bootstrap, />dufflebag</)
  assert.doesNotMatch(`${signIn}${bootstrap}`, />Dufflebag</)
})

test('the wizard rail is forward-only', () => {
  const wizard = renderToStaticMarkup(React.createElement(Initialize, {
    host: 'dufflebag.test',
    onDone: () => {},
  }))
  assert.doesNotMatch(wizard, /<button(?=[^>]*id="initialize-step")(?=[^>]*disabled)[^>]*>/)
  for (const id of ['credentials-step', 'organization-step', 'project-step']) {
    assert.match(wizard, new RegExp(`<button(?=[^>]*id="${id}")(?=[^>]*disabled)[^>]*>`))
  }
})

test('Store credentials Continue is gated on acknowledgement', () => {
  const footer = renderToStaticMarkup(React.createElement(StoreCredentialsFooter, {
    stored: false,
    submitting: false,
    onContinue: () => {},
  }))
  assert.match(footer, /<button[^>]*disabled[^>]*>[\s\S]{0,160}Continue to organization/)
})

test('Store credentials warning precedes credential values', () => {
  const markup = storeCredentials()
  const warning = markup.indexOf('Shown once. Store these before continuing.')
  const clientID = markup.indexOf(credentials.client_id)
  assert.notEqual(warning, -1)
  assert.notEqual(clientID, -1)
  assert.ok(warning < clientID, 'the shown-once warning must precede the credential values')
})

test('recovery shares are expanded by default', () => {
  const markup = storeCredentials()
  assert.equal((markup.match(/aria-expanded="true"/g) ?? []).length, credentials.recovery_shares.length)
  for (const share of credentials.recovery_shares) assert.match(markup, new RegExp(share))
})

test('credential download contains every shown secret and recovery share', () => {
  const contents = credentialsFileContent(credentials)
  assert.match(contents, /^# dufflebag administrative credentials$/m)
  assert.match(contents, /^# Store this file like the secret it contains\.$/m)
  assert.match(contents, /^# The recovery share must be stored offline, separately from the credentials\.$/m)
  assert.match(contents, /^# Recovery: POST \/sys\/recovery with the share mints a fresh root principal\.$/m)
  assert.match(contents, new RegExp(`^client_id: ${credentials.client_id}$`, 'm'))
  assert.match(contents, new RegExp(`^client_secret: ${credentials.client_secret}$`, 'm'))
  credentials.recovery_shares.forEach((share, index) => {
    assert.match(contents, new RegExp(`^recovery_share_${index + 1}: ${share}$`, 'm'))
  })
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
