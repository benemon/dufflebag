import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let ApiError
let DisablePluginRegistryConfirmation
let PluginRegistryConfirmationView
let PluginRegistryView
let TypedConfirmModalView
let disablePluginRegistry
let enablePluginRegistry
let exposePluginRegistry
let getPluginRegistry
let pluginRegistryErrorMessage
let unexposePluginRegistry

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ ApiError } = await vite.ssrLoadModule('/src/api/client.ts'))
  ;({
    DisablePluginRegistryConfirmation, PluginRegistryConfirmationView,
    PluginRegistryView,
    pluginRegistryErrorMessage,
  } = await vite.ssrLoadModule('/src/screens/Plugins.tsx'))
  ;({ TypedConfirmModalView } =
    await vite.ssrLoadModule('/src/components/TypedConfirmModal.tsx'))
  ;({
    disablePluginRegistry, enablePluginRegistry, exposePluginRegistry, getPluginRegistry,
    unexposePluginRegistry,
  } = await vite.ssrLoadModule('/src/data/pluginRegistry.ts'))
})

after(async () => { await vite.close() })

const props = (over = {}) => ({
  organizationName: 'acme', callerRole: 'maintainer',
  registry: { enabled: false, exposed: false }, loading: false, failure: null,
  onRefresh: async () => {}, onEnable: async () => {}, onExpose: async () => {},
  onUnexpose: async () => {}, onDisable: async () => {}, ...over,
})

const render = (over = {}) => renderToStaticMarkup(
  React.createElement(PluginRegistryView, props(over)),
)

test('screen renders loading, error, disabled reader, and disabled maintainer states honestly', () => {
  assert.match(render({ loading: true, registry: null }), /Loading plugin registry/)

  const failed = render({ failure: 'database unavailable', registry: null })
  assert.match(failed, /Plugin registry could not be loaded/)
  assert.match(failed, /database unavailable/)

  const reader = render({ callerRole: 'reader' })
  assert.match(reader, /The plugin registry isn&#x27;t enabled/)
  assert.match(reader, /Enable it to mirror Packer plugins for acme/)
  assert.match(reader, /packer init resolves them from dufflebag instead of the internet/)
  assert.doesNotMatch(reader, /Enable the registry/)

  const maintainer = render()
  assert.match(maintainer, /Enable the registry/)
})

test('enabled and exposed states show the required settings and honest empty state', () => {
  const enabled = render({ registry: { enabled: true, exposed: false } })
  assert.match(enabled, /The registry is enabled but not exposed/)
  assert.match(enabled, /Packer can&#x27;t reach plugins until the registry is exposed/)
  assert.match(enabled, /No plugins yet\./)
  assert.match(enabled, />Expose</)
  assert.match(enabled, /Disable registry/)

  const exposed = render({ registry: { enabled: true, exposed: true } })
  assert.match(exposed, /The registry is exposed/)
  assert.match(exposed, />Unexpose</)
  assert.match(exposed, /Unexpose the registry first before disabling it/)
  assert.match(exposed, /<button[^>]*disabled=""[^>]*>[\s\S]*?Disable registry[\s\S]*?<\/button>/)

  const reader = render({
    callerRole: 'reader', registry: { enabled: true, exposed: false },
  })
  assert.doesNotMatch(reader, /Registry settings/)
  assert.match(reader, /No plugins yet\./)
})

test('expose, unexpose, and disable confirmations state their consequences', () => {
  const expose = renderToStaticMarkup(React.createElement(PluginRegistryConfirmationView, {
    title: 'Expose the plugin registry?',
    body: 'Plugins become anonymously readable to anyone who can reach dufflebag.',
    verb: 'Expose registry', busy: false, onConfirm: () => {}, onCancel: () => {},
  }))
  assert.match(expose, /anonymously readable to anyone who can reach dufflebag/)
  assert.match(expose, /Expose registry/)

  const unexpose = renderToStaticMarkup(React.createElement(PluginRegistryConfirmationView, {
    title: 'Unexpose the plugin registry?',
    body: 'In-flight packer init fails when the registry is unexposed.',
    verb: 'Unexpose registry', busy: false, onConfirm: () => {}, onCancel: () => {},
  }))
  assert.match(unexpose, /In-flight packer init fails/)

  const modal = DisablePluginRegistryConfirmation({
    organizationName: 'acme', busy: false, onConfirm: () => {}, onCancel: () => {},
  })
  const disabled = renderToStaticMarkup(React.createElement(TypedConfirmModalView, {
    ...modal.props, confirmation: '', onConfirmationChange: () => {},
  }))
  assert.match(disabled, /All plugin data is destroyed/)
  assert.match(disabled, /Type <strong>acme<\/strong> to confirm/)
})

test('data calls use the organization platform lifecycle paths', async () => {
  const originalFetch = globalThis.fetch
  const requests = []
  globalThis.fetch = async (path, options) => {
    requests.push([path, options.method])
    if (options.method === 'POST' && String(path).endsWith('/disable')) {
      return new Response(null, { status: 204 })
    }
    return new Response(JSON.stringify({ enabled: true, exposed: false }), {
      status: options.method === 'POST' && String(path).endsWith('/enable') ? 201 : 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }
  try {
    await getPluginRegistry('token', 'organization id')
    await enablePluginRegistry('token', 'organization id')
    await exposePluginRegistry('token', 'organization id')
    await unexposePluginRegistry('token', 'organization id')
    await disablePluginRegistry('token', 'organization id')
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.deepEqual(requests, [
    ['/api/v1/organizations/organization%20id/plugin-registry', 'GET'],
    ['/api/v1/organizations/organization%20id/plugin-registry/enable', 'POST'],
    ['/api/v1/organizations/organization%20id/plugin-registry/expose', 'POST'],
    ['/api/v1/organizations/organization%20id/plugin-registry/unexpose', 'POST'],
    ['/api/v1/organizations/organization%20id/plugin-registry/disable', 'POST'],
  ])
})

test('400 and 409 server messages are preserved for inline action errors', () => {
  assert.equal(
    pluginRegistryErrorMessage(new ApiError(400, 'invalid plugin registry state'), 'fallback'),
    'invalid plugin registry state',
  )
  assert.equal(
    pluginRegistryErrorMessage(new ApiError(409, 'plugin registry is already exposed'), 'fallback'),
    'plugin registry is already exposed',
  )
})

test('the organization-level route bypasses project loading and missing-project gates', () => {
  const source = readFileSync(new URL('../src/App.tsx', import.meta.url), 'utf8')
  assert.match(source, /const organizationRoute = useLocation\(\)\.pathname === '\/plugins'/)
  assert.match(source, /!platform && !organizationRoute && projectsLoading/)
  assert.match(source, /!platform && !organizationRoute && !selectedProject/)
})
