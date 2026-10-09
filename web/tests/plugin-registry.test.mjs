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
let planPluginUpload
let templateStanza
let PluginDetailView
let PluginUploadView
let listPlugins
let publishPluginVersion

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
    unexposePluginRegistry, planPluginUpload, templateStanza, listPlugins, publishPluginVersion,
  } = await vite.ssrLoadModule('/src/data/pluginRegistry.ts'))
  ;({ PluginDetailView } = await vite.ssrLoadModule('/src/screens/PluginDetail.tsx'))
  ;({ PluginUploadView } = await vite.ssrLoadModule('/src/screens/PluginUpload.tsx'))
})

after(async () => { await vite.close() })

const props = (over = {}) => ({
  organizationName: 'acme', callerRole: 'maintainer', host: 'dufflebag.example.com',
  registry: { enabled: false, exposed: false }, plugins: [], loading: false, failure: null,
  onOpenPlugin: () => {}, onUpload: () => {},
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
  assert.match(enabled, /No plugins mirrored yet/)
  assert.match(enabled, /Upload plugin files/)
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
  assert.match(reader, /No plugins mirrored yet/)
  assert.doesNotMatch(reader, /Upload plugin files/)
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
  assert.match(source, /const organizationRoute = pathname === '\/plugins' \|\| pathname\.startsWith\('\/plugins\/'\)/)
  assert.match(source, /!platform && !organizationRoute && projectsLoading/)
  assert.match(source, /!platform && !organizationRoute && !selectedProject/)
})

// Names are verbatim from producers captured 2026-10-09: packer-plugin-amazon 1.8.2 on
// releases.hashicorp.com and packer-plugin-git v0.6.3 on GitHub
// (internal/domain/plugin/testdata holds their SHA256SUMS files).
const file = (name, size = 1) => ({ name, size })

test('upload planning groups both publishing shapes into one version', () => {
  const amazon = planPluginUpload([
    file('packer-plugin-amazon_1.8.2_SHA256SUMS'), file('packer-plugin-amazon_1.8.2_SHA256SUMS.sig'),
    file('packer-plugin-amazon_1.8.2_manifest.json'), file('packer-plugin-amazon_1.8.2_linux_arm64.zip'),
  ])
  assert.equal(amazon.kind, 'ready')
  assert.equal(amazon.name, 'amazon')
  assert.equal(amazon.version, '1.8.2')
  assert.deepEqual(amazon.files.map((f) => f.field), ['sha256sums', 'sha256sums_sig', 'manifest', 'zips'])
  assert.equal(amazon.files[3].platform, 'linux_arm64')

  const git = planPluginUpload([
    file('packer-plugin-git_v0.6.3_SHA256SUMS'), file('packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip'),
    file('packer-plugin-git_v0.6.3_x5.0_darwin_arm64.zip'),
  ])
  assert.equal(git.kind, 'ready')
  assert.equal(git.version, '0.6.3')
  assert.deepEqual(git.files.filter((f) => f.field === 'zips').map((f) => f.platform), ['linux_arm64', 'darwin_arm64'])
})

test('upload planning refuses what the server would refuse, before sending', () => {
  const refused = (files, reason) => {
    const plan = planPluginUpload(files)
    assert.equal(plan.kind, 'refused')
    assert.match(plan.reason, reason)
  }
  refused([file('packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip')], /SHA256SUMS file/)
  refused([file('packer-plugin-git_v0.6.3_SHA256SUMS')], /at least one plugin zip/)
  refused([file('packer-plugin-git_v0.6.3_SHA256SUMS'), file('packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip'),
    file('packer-plugin-git_v0.6.2_x5.0_linux_amd64.zip')], /span versions 0\.6\.3, 0\.6\.2/)
  refused([file('packer-plugin-git_v0.6.3_SHA256SUMS'), file('README.md')], /README\.md is not/)
  refused([file('packer-plugin-amazon_1.8.2_SHA256SUMS'), file('packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip')], /more than one plugin/)
})

test('the catalogue lists plugins with their source, newest version and count', () => {
  const html = render({
    registry: { enabled: true, exposed: true },
    plugins: [{ name: 'git', source: { kind: 'upload' }, published_versions: 2, newest_version: '0.6.3' }],
  })
  assert.match(html, /packer init resolves them from dufflebag\.example\.com\/plugins\/acme/)
  assert.match(html, />git</)
  assert.match(html, />Local</)
  assert.match(html, />0\.6\.3</)
})

test('plugin detail shows the stanza for the newest version and exactly which platforms each version serves', () => {
  const detail = (registry) => renderToStaticMarkup(React.createElement(PluginDetailView, {
    name: 'git', organizationName: 'acme', host: 'dufflebag.example.com', callerRole: 'publisher',
    registry, loading: false, failure: null, onRefresh: () => {}, onUpload: () => {},
    detail: { name: 'git', source: { kind: 'upload' }, versions: [{
      version: '0.6.3', revoked: false, created_at: '2026-10-09T00:00:00Z',
      listed_platforms: [{ os: 'linux', arch: 'arm64' }, { os: 'darwin', arch: 'arm64' }],
      stored_platforms: [{ os: 'linux', arch: 'arm64' }],
    }] },
  }))
  const html = detail({ enabled: true, exposed: false })
  assert.match(html, /source  = &quot;dufflebag\.example\.com\/plugins\/acme\/git&quot;/)
  assert.match(html, /version = &quot;0\.6\.3&quot;/)
  assert.match(html, /Packer can&#x27;t resolve this template stanza until the registry is exposed/)
  assert.match(html, /<td[^>]*data-label="darwin_arm64"[^>]*>○<\/td>/)
  assert.match(html, /<td[^>]*data-label="linux_arm64"[^>]*>●<\/td>/)
  assert.match(html, /Upload version/)
  assert.doesNotMatch(detail({ enabled: true, exposed: true }), /until the registry is exposed/)
})

test('the upload view previews the grouped version before anything is sent', () => {
  const html = renderToStaticMarkup(React.createElement(PluginUploadView, {
    registry: { enabled: true, exposed: true }, busy: false, failure: null, result: null,
    onChoose: () => {}, onOpen: () => {}, onSubmit: () => {},
    plan: planPluginUpload([file('packer-plugin-git_v0.6.3_SHA256SUMS', 900), file('packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip', 6259751)]),
  }))
  assert.match(html, /git · 0\.6\.3/)
  assert.match(html, /linux_arm64/)
  assert.match(html, /6\.3 MB/)
  assert.match(html, /Upload git 0\.6\.3/)

  const disabled = renderToStaticMarkup(React.createElement(PluginUploadView, {
    registry: { enabled: false, exposed: false }, busy: false, failure: null, result: null, plan: null,
    onChoose: () => {}, onOpen: () => {}, onSubmit: () => {},
  }))
  assert.match(disabled, /The plugin registry isn&#x27;t enabled/)
})

test('publishing sends a multipart PUT to the version path', async () => {
  const originalFetch = globalThis.fetch
  let sent
  globalThis.fetch = async (path, options) => {
    sent = { path, options }
    return new Response(JSON.stringify({ version: {}, stanza: {} }), { status: 201, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    const sums = new File(['sums'], 'packer-plugin-git_v0.6.3_SHA256SUMS')
    const zip = new File(['zip'], 'packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip')
    await publishPluginVersion('token', 'organization id', planPluginUpload([sums, zip]))
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.equal(sent.path, '/api/v1/organizations/organization%20id/plugin-registry/plugins/git/versions/0.6.3')
  assert.equal(sent.options.method, 'PUT')
  assert.deepEqual([...sent.options.body.keys()], ['sha256sums', 'zips'])
  assert.equal(sent.options.body.get('zips').name, 'packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip')
})
