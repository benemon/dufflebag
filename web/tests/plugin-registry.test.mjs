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
let PluginRegistrySettingsView
let PluginErrorCard
let TypedConfirmModalView
let disablePluginRegistry
let enablePluginRegistry
let exposePluginRegistry
let getPluginRegistry
let pluginRegistryErrorMessage
let unexposePluginRegistry
let planPluginUploads
let templateStanza
let PluginDetailView
let PluginUploadView
let listPlugins
let publishPluginVersion
let deletePluginVersion
let PluginHashicorpView
let PluginImportJobView
let PluginGithubView
let pluginChanges
let syncPlugin
let syncCatalogue
let setPluginUpdateCheck
let createGithubImport
let createPluginImport

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
  ;({ PluginErrorCard } = await vite.ssrLoadModule('/src/components/PluginLoadState.tsx'))
  ;({
    disablePluginRegistry, enablePluginRegistry, exposePluginRegistry, getPluginRegistry,
    unexposePluginRegistry, planPluginUploads, templateStanza, listPlugins, publishPluginVersion,
    deletePluginVersion, pluginChanges, syncPlugin, syncCatalogue, setPluginUpdateCheck, createPluginImport, createGithubImport,
  } = await vite.ssrLoadModule('/src/data/pluginRegistry.ts'))
  ;({ PluginDetailView } = await vite.ssrLoadModule('/src/screens/PluginDetail.tsx'))
  ;({ PluginUploadView } = await vite.ssrLoadModule('/src/screens/PluginUpload.tsx'))
  ;({ PluginHashicorpView } = await vite.ssrLoadModule('/src/screens/PluginHashicorp.tsx'))
  ;({ PluginImportJobView } = await vite.ssrLoadModule('/src/screens/PluginImportJob.tsx'))
  ;({ PluginGithubView } = await vite.ssrLoadModule('/src/screens/PluginGithub.tsx'))
  ;({ PluginRegistrySettingsView } =
    await vite.ssrLoadModule('/src/screens/PluginRegistrySettings.tsx'))
})

after(async () => { await vite.close() })

const props = (over = {}) => ({
  organizationName: 'acme', callerRole: 'maintainer', host: 'dufflebag.example.com',
  registry: { enabled: false, exposed: false }, plugins: [], loading: false, failure: null,
  onBackToRegistry: () => {},
  onOpenPlugin: () => {}, onUpload: () => {}, onBrowse: () => {}, onImportGithub: () => {},
  onOpenImport: () => {}, onSyncSelected: async () => [],
  onRefresh: async () => {}, onEnable: async () => {}, ...over,
})

const render = (over = {}) => renderToStaticMarkup(
  React.createElement(PluginRegistryView, props(over)),
)

test('screen renders loading, error, disabled reader, and disabled maintainer states honestly', () => {
  assert.match(render({ loading: true, registry: null }), /Loading plugins…/)

  const failed = render({ failure: 'database unavailable', registry: null })
  assert.match(failed, /Plugins could not be loaded/)
  assert.match(failed, /database unavailable/)
  assert.match(failed, />Retry</)

  const reader = render({ callerRole: 'reader' })
  assert.match(reader, /The plugin registry isn&#x27;t enabled/)
  assert.match(reader, /Enable it to mirror Packer plugins for acme/)
  assert.match(reader, /packer init resolves them from dufflebag instead of the internet/)
  assert.doesNotMatch(reader, /Enable the registry/)

  const maintainer = render()
  assert.match(maintainer, /Enable the registry/)
})

test('all six plugin screens pin their breadcrumb, loading card, and retryable error card', () => {
  const callbacks = { onBackToRegistry: () => {}, onBackToPlugins: () => {}, onRefresh: () => {} }
  const screens = [
    {
      name: 'catalogue', crumbs: ['Registry', 'Plugins'], loading: 'Loading plugins…', error: 'Plugins could not be loaded',
      view: (loading, failure) => render({ loading, failure, registry: null }),
    },
    {
      name: 'detail', crumbs: ['Registry', 'Plugins', 'amazon'], loading: 'Loading amazon…', error: 'amazon could not be loaded',
      view: (loading, failure) => renderToStaticMarkup(React.createElement(PluginDetailView, {
        ...callbacks, name: 'amazon', organizationName: 'acme', host: 'dufflebag.example.com', callerRole: 'publisher',
        registry: null, detail: null, loading, failure, onUpload: () => {}, busy: false,
        actionFailure: null, editing: false, upstream: [], summary: null, onToggleUpdates: () => {},
        onEdit: () => {}, onCancelEdit: () => {}, onSync: () => {}, onRemove: () => {},
      })),
    },
    {
      name: 'job', crumbs: ['Registry', 'Plugins', 'Plugin', 'Job 01KZF3QW7N'], loading: 'Loading job…', error: 'Job could not be loaded',
      view: (loading, failure) => renderToStaticMarkup(React.createElement(PluginImportJobView, {
        ...callbacks, id: '01KZF3QW7N', job: null, registry: null, loading, failure,
        organizationName: 'acme', host: 'dufflebag.example.com', onOpen: () => {},
      })),
    },
    {
      name: 'hashicorp', crumbs: ['Registry', 'Plugins', 'Browse HashiCorp'], loading: 'Reading releases.hashicorp.com…', error: "HashiCorp's plugin list could not be read",
      view: (loading, failure) => renderToStaticMarkup(React.createElement(PluginHashicorpView, {
        ...callbacks, plugins: null, selected: null, versions: null, hasMore: false, preselected: [],
        loading, failure, busy: false, onChoose: () => {}, onMore: () => {}, onImport: () => {},
      })),
    },
    {
      name: 'github', crumbs: ['Registry', 'Plugins', 'Import from GitHub'], loading: 'Loading GitHub release…', error: 'GitHub release could not be loaded',
      view: (loading, failure) => renderToStaticMarkup(React.createElement(PluginGithubView, {
        ...callbacks, link: '', release: null, preselected: [], loading, failure, busy: false,
        onLinkChange: () => {}, onResolve: () => {}, onImport: () => {},
      })),
    },
    {
      name: 'upload', crumbs: ['Registry', 'Plugins', 'Upload'], loading: 'Loading upload…', error: 'Upload could not be loaded',
      view: (loading, failure) => renderToStaticMarkup(React.createElement(PluginUploadView, {
        ...callbacks, registry: null, plan: null, outcomes: {}, busy: false, loading, failure,
        onChoose: () => {}, onOpen: () => {}, onSubmit: () => {},
      })),
    },
  ]

  for (const screen of screens) {
    const loading = screen.view(true, null)
    assert.match(loading, new RegExp(screen.crumbs.join('[\\s\\S]*')), `${screen.name}: breadcrumbs`)
    assert.match(loading, new RegExp(screen.loading.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')), `${screen.name}: loading`)
    assert.match(loading, /aria-busy="true"/, `${screen.name}: loading card`)

    const failed = screen.view(false, 'upstream unavailable')
    assert.match(failed, new RegExp(screen.error.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace("'", '&#x27;')), `${screen.name}: error title`)
    assert.match(failed, /font-family:monospace/, `${screen.name}: monospace error`)
    assert.match(failed, />Retry</, `${screen.name}: retry`)
  }
})

test('the shared Retry action invokes the screen reload callback', () => {
  let calls = 0
  const card = PluginErrorCard({ title: 'Failed', error: 'bad gateway', onRetry: () => { calls += 1 } })
  const body = card.props.children
  const retry = body.props.children[1]
  retry.props.onClick()
  assert.equal(calls, 1)
})

test('the catalogue links unexposed state to settings and carries no lifecycle settings or exposed alert', () => {
  const enabled = render({ registry: { enabled: true, exposed: false } })
  assert.match(enabled, /The registry is enabled but not exposed/)
  assert.match(enabled, /Plugins below are stored and can be managed, but Packer can’t reach them until the registry is exposed/)
  assert.match(enabled, /href="\/plugin-registry\/settings"[\s\S]{0,300}?Registry settings</)
  assert.match(enabled, /No plugins mirrored yet/)
  assert.match(enabled, /Upload plugin files/)
  assert.doesNotMatch(enabled, />Expose</)
  assert.doesNotMatch(enabled, /Disable registry/)

  const exposed = render({ registry: { enabled: true, exposed: true } })
  assert.doesNotMatch(exposed, /The registry is exposed/)
  assert.doesNotMatch(exposed, /Registry settings/)

  const reader = render({
    callerRole: 'reader', registry: { enabled: true, exposed: false },
  })
  assert.match(reader, /Registry settings/)
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
  assert.match(source, /const organizationRoute = pathname === '\/plugin-registry' \|\| pathname\.startsWith\('\/plugin-registry\/'\)/)
  assert.match(source, /!platform && !organizationRoute && projectsLoading/)
  assert.match(source, /!platform && !organizationRoute && !selectedProject/)
})

// Names are verbatim from producers captured 2026-10-09: packer-plugin-amazon 1.8.2 on
// releases.hashicorp.com and packer-plugin-git v0.6.3 on GitHub
// (internal/domain/plugin/testdata holds their SHA256SUMS files).
const file = (name, size = 1) => ({ name, size })

test('upload planning groups files from both publishing shapes into one upload per version', () => {
  const plan = planPluginUploads([
    file('packer-plugin-amazon_1.8.2_SHA256SUMS'), file('packer-plugin-amazon_1.8.2_SHA256SUMS.sig'),
    file('packer-plugin-amazon_1.8.2_manifest.json'), file('packer-plugin-amazon_1.8.2_linux_arm64.zip'),
    file('packer-plugin-git_v0.6.3_SHA256SUMS'), file('packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip'),
    file('packer-plugin-git_v0.6.3_x5.0_darwin_arm64.zip'),
    file('packer-plugin-git_v0.6.2_SHA256SUMS'), file('packer-plugin-git_v0.6.2_x5.0_linux_arm64.zip'),
  ])
  assert.deepEqual(plan.refused, [])
  assert.deepEqual(plan.versions.map((v) => `${v.name} ${v.version}`), ['amazon 1.8.2', 'git 0.6.3', 'git 0.6.2'])
  const [amazon, git] = plan.versions
  assert.deepEqual(amazon.files.map((f) => f.field), ['sha256sums', 'sha256sums_sig', 'manifest', 'zips'])
  assert.equal(amazon.files[3].platform, 'linux_arm64')
  assert.deepEqual(git.files.filter((f) => f.field === 'zips').map((f) => f.platform), ['linux_arm64', 'darwin_arm64'])
})

test('upload planning refuses incomplete versions and unknown files before sending', () => {
  const plan = planPluginUploads([
    file('packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip'),
    file('packer-plugin-git_v0.6.2_SHA256SUMS'),
    file('README.md'),
    file('packer-plugin-amazon_1.8.2_SHA256SUMS'), file('packer-plugin-amazon_1.8.2_linux_arm64.zip'),
  ])
  assert.deepEqual(plan.versions.map((v) => `${v.name} ${v.version}`), ['amazon 1.8.2'])
  assert.deepEqual(plan.refused, [
    { label: 'README.md', reason: 'not a SHA256SUMS file, signature, manifest or plugin zip' },
    { label: 'git 0.6.3', reason: 'no SHA256SUMS file' },
    { label: 'git 0.6.2', reason: 'no plugin zip' },
  ])
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
    busy: false, actionFailure: null, editing: false, upstream: [], onEdit: () => {}, onCancelEdit: () => {}, onSync: () => {}, onRemove: () => {},
    detail: { name: 'git', source: { kind: 'upload' }, versions: [{
      version: '0.6.3', revoked: false, created_at: '2026-10-09T00:00:00Z',
      listed_platforms: [{ os: 'linux', arch: 'arm64' }, { os: 'darwin', arch: 'arm64' }],
      stored_platforms: [{ os: 'linux', arch: 'arm64' }],
    }] },
  }))
  const html = detail({ enabled: true, exposed: false })
  assert.match(html, /source  = &quot;dufflebag\.example\.com\/plugins\/acme\/git&quot;/)
  assert.match(html, /version = &quot;0\.6\.3&quot;/)
  assert.match(html, /Packer can’t resolve the template stanza on this page until the registry is exposed/)
  assert.match(html, /href="\/plugin-registry\/settings"[\s\S]{0,300}?Registry settings</)
  assert.ok(html.indexOf('Registry settings') < html.indexOf('Template stanza'))
  assert.match(html, /<td[^>]*data-label="darwin_arm64"[^>]*>○<\/td>/)
  assert.match(html, /<td[^>]*data-label="linux_arm64"[^>]*>●<\/td>/)
  assert.match(html, /Upload version/)
  assert.doesNotMatch(detail({ enabled: true, exposed: true }), /until the registry is exposed/)
})

test('the upload view shows each version, then its outcome and stanza', () => {
  const plan = planPluginUploads([
    file('packer-plugin-git_v0.6.3_SHA256SUMS', 900), file('packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip', 6259751),
    file('packer-plugin-git_v0.6.2_SHA256SUMS', 900), file('packer-plugin-git_v0.6.2_x5.0_linux_arm64.zip', 6100000),
  ])
  const view = (outcomes) => renderToStaticMarkup(React.createElement(PluginUploadView, {
    registry: { enabled: true, exposed: false }, plan, outcomes, busy: false, failure: null,
    onChoose: () => {}, onOpen: () => {}, onSubmit: () => {},
  }))
  const ready = view({})
  assert.match(ready, /git · 0\.6\.3/)
  assert.match(ready, /6\.3 MB/)
  assert.match(ready, /Upload 2 versions/)
  assert.equal((ready.match(/>Ready</g) ?? []).length, 2)

  const done = view({
    'git 0.6.3': { status: 'published', stanza: { source: 'dufflebag.example.com/plugins/acme/git', version: '0.6.3', hcl: 'source  = "dufflebag.example.com/plugins/acme/git"' } },
    'git 0.6.2': { status: 'refused', message: 'git 0.6.2 already exists; versions are immutable' },
  })
  assert.match(done, />Published</)
  assert.match(done, />Refused</)
  assert.match(done, /versions are immutable/)
  assert.match(done, /Open git/)
  assert.match(done, /Packer can’t resolve the template stanza on this page until the registry is exposed/)
  assert.doesNotMatch(done, /Upload 2 versions/)

  const disabled = renderToStaticMarkup(React.createElement(PluginUploadView, {
    registry: { enabled: false, exposed: false }, plan: null, outcomes: {}, busy: false, failure: null,
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
    await publishPluginVersion('token', 'organization id', planPluginUploads([sums, zip]).versions[0])
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.equal(sent.path, '/api/v1/organizations/organization%20id/plugin-registry/plugins/git/versions/0.6.3')
  assert.equal(sent.options.method, 'PUT')
  assert.deepEqual([...sent.options.body.keys()], ['sha256sums', 'zips'])
  assert.equal(sent.options.body.get('zips').name, 'packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip')
})

test('publishers edit versions and remove them; readers do neither', () => {
  const view = (callerRole, revoked) => renderToStaticMarkup(React.createElement(PluginDetailView, {
    name: 'git', organizationName: 'acme', host: 'dufflebag.example.com', callerRole,
    registry: { enabled: true, exposed: true }, loading: false, failure: null, onRefresh: () => {}, onUpload: () => {},
    busy: false, actionFailure: null, editing: false, upstream: [], onEdit: () => {}, onCancelEdit: () => {}, onSync: () => {}, onRemove: () => {},
    detail: { name: 'git', source: { kind: 'upload' }, versions: [{
      version: '0.6.3', revoked, created_at: '2026-10-09T00:00:00Z', listed_platforms: [], stored_platforms: [],
    }] },
  }))
  const served = view('publisher', false)
  assert.match(served, />Edit versions</)
  assert.match(served, />Remove version</)
  assert.doesNotMatch(served, />Revoke</)
  const revoked = view('publisher', true)
  assert.match(revoked, />Revoked</)
  assert.doesNotMatch(revoked, /Template stanza/)
  const reader = view('reader', false)
  assert.doesNotMatch(reader, />Edit versions</)
  assert.doesNotMatch(reader, />Remove version</)
})

const amazonVersions = [
  { version: '1.8.2', revoked: false, created_at: '2026-10-01T00:00:00Z',
    listed_platforms: [{ os: 'linux', arch: 'amd64' }, { os: 'linux', arch: 'arm64' }, { os: 'darwin', arch: 'arm64' }],
    stored_platforms: [{ os: 'linux', arch: 'amd64' }] },
  { version: '1.8.1', revoked: true, created_at: '2026-09-01T00:00:00Z',
    listed_platforms: [{ os: 'linux', arch: 'amd64' }, { os: 'linux', arch: 'arm64' }],
    stored_platforms: [{ os: 'linux', arch: 'amd64' }] },
]
const amazonUpstream = [
  { version: '1.8.3', created_at: '2026-10-08T00:00:00Z', prerelease: false, platforms: ['linux_amd64', 'linux_arm64'], mirrored: false },
  { version: '1.8.2', created_at: '2026-10-01T00:00:00Z', prerelease: false, platforms: ['linux_amd64', 'linux_arm64', 'darwin_arm64'], mirrored: true },
]

test('editing turns only unmirrored cells of served versions into checkboxes and lists upstream versions', () => {
  const html = renderToStaticMarkup(React.createElement(PluginDetailView, {
    name: 'amazon', organizationName: 'acme', host: 'dufflebag.example.com', callerRole: 'publisher',
    registry: { enabled: true, exposed: true }, loading: false, failure: null, onRefresh: () => {}, onUpload: () => {},
    busy: false, actionFailure: null, editing: true, upstream: amazonUpstream,
    onEdit: () => {}, onCancelEdit: () => {}, onSync: () => {}, onRemove: () => {},
    detail: { name: 'amazon', source: { kind: 'releases-hashicorp', repository: 'packer-plugin-amazon' }, versions: amazonVersions },
  }))
  assert.match(html, /aria-label="Add linux_arm64 to 1\.8\.2"/)
  assert.match(html, /aria-label="Add darwin_arm64 to 1\.8\.2"/)
  assert.doesNotMatch(html, /aria-label="Add linux_amd64 to 1\.8\.2"/, 'a mirrored platform is a status, not a control')
  assert.doesNotMatch(html, /aria-label="Add linux_arm64 to 1\.8\.1"/, 'a revoked version takes no platforms until restored')
  assert.match(html, /aria-label="Add 1\.8\.3"/)
  assert.doesNotMatch(html, /aria-label="Add 1\.8\.2"/, 'a mirrored version is not offered again')
  assert.match(html, /aria-label="Serve 1\.8\.1"/)
  assert.match(html, /No changes yet/)
  assert.match(html, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Sync 0 changes/)
  assert.doesNotMatch(html, />Remove version</)
})

test('pending changes revoke, restore, add platforms and mirror new versions', () => {
  assert.deepEqual(pluginChanges(amazonVersions, amazonUpstream.filter((v) => !v.mirrored), { selected: {}, added: {} }), [])
  assert.deepEqual(pluginChanges(amazonVersions, amazonUpstream.filter((v) => !v.mirrored), {
    selected: { '1.8.3': true, '1.8.1': true },
    added: { '1.8.3': ['linux_amd64'], '1.8.2': ['linux_amd64', 'darwin_arm64'] },
  }), [
    { version: '1.8.3', action: 'add', platforms: ['linux_amd64'] },
    { version: '1.8.2', action: 'add', platforms: ['darwin_arm64'] },
    { version: '1.8.1', action: 'restore' },
  ])
  assert.deepEqual(pluginChanges(amazonVersions, [], { selected: { '1.8.2': false }, added: { '1.8.2': ['darwin_arm64'] } }),
    [{ version: '1.8.2', action: 'revoke' }], 'a version being revoked takes no platforms')
  assert.deepEqual(pluginChanges(amazonVersions, [], { selected: {}, added: { '1.8.1': ['linux_arm64'] } }),
    [], 'a version that stays revoked takes no platforms')
  assert.deepEqual(pluginChanges(amazonVersions, amazonUpstream.filter((v) => !v.mirrored), { selected: { '1.8.3': true }, added: { '1.8.3': [] } }),
    [], 'a new version with no platforms is not a change')
})

test('a sync posts the ordered changes to the plugin', async () => {
  const originalFetch = globalThis.fetch
  let sent
  globalThis.fetch = async (path, options) => {
    sent = { path, body: JSON.parse(options.body) }
    return new Response(JSON.stringify({ id: 'job' }), { status: 202, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    await syncPlugin('token', 'org', 'amazon', [{ version: '1.8.1', action: 'revoke' }])
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.equal(sent.path, '/api/v1/organizations/org/plugin-registry/plugins/amazon/sync')
  assert.deepEqual(sent.body, { changes: [{ version: '1.8.1', action: 'revoke' }] })
})

test('remove uses the version path', async () => {
  const originalFetch = globalThis.fetch
  const requests = []
  globalThis.fetch = async (path, options) => {
    requests.push(`${options.method} ${path}`)
    return new Response(null, { status: 204 })
  }
  try {
    await deletePluginVersion('token', 'org', 'git', '0.6.3')
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.deepEqual(requests, [
    'DELETE /api/v1/organizations/org/plugin-registry/plugins/git/versions/0.6.3',
  ])
})

test('registry settings own lifecycle controls and default platforms with maintainer gating', () => {
  const settings = (callerRole, registry) => renderToStaticMarkup(React.createElement(
    PluginRegistrySettingsView,
    {
      organizationName: 'acme', callerRole, registry,
      defaultPlatforms: ['linux_amd64', 'linux_arm64', 'darwin_arm64'],
      loading: false, failure: null, onBackToRegistry: () => {}, onBackToPlugins: () => {},
      onRefresh: () => {}, onEnable: async () => {}, onExpose: async () => {},
      onUnexpose: async () => {}, onDisable: async () => {}, onSetDefaultPlatforms: async () => {},
    },
  ))
  const unexposed = settings('maintainer', { enabled: true, exposed: false })
  assert.match(unexposed, /Registry[\s\S]*Plugins[\s\S]*Registry settings/)
  assert.match(unexposed, />Expose</)
  assert.match(unexposed, /Disable registry/)
  assert.match(unexposed, /value="linux_amd64, linux_arm64, darwin_arm64"/)
  assert.match(unexposed, /Save default platforms/)

  const exposed = settings('maintainer', { enabled: true, exposed: true })
  assert.match(exposed, />Unexpose</)
  assert.match(exposed, /Unexpose the registry first before disabling it/)
  assert.match(exposed, /<button[^>]*disabled=""[^>]*>[\s\S]*?Disable registry[\s\S]*?<\/button>/)

  const reader = settings('reader', { enabled: true, exposed: false })
  assert.match(reader, /Default platforms: linux_amd64, linux_arm64, darwin_arm64/)
  assert.doesNotMatch(reader, />Expose</)
  assert.doesNotMatch(reader, /Disable registry/)
  assert.doesNotMatch(reader, /Save default platforms/)

  const disabled = settings('maintainer', { enabled: false, exposed: false })
  assert.match(disabled, /Enable the registry/)
  assert.doesNotMatch(settings('reader', { enabled: false, exposed: false }), /Enable the registry/)

  assert.match(render({ registry: { enabled: true, exposed: false } }), /Browse HashiCorp/)
  assert.doesNotMatch(render({ callerRole: 'reader', registry: { enabled: true, exposed: false } }), /Browse HashiCorp/)
})

test('browsing HashiCorp marks held names and mirrored versions', () => {
  const html = renderToStaticMarkup(React.createElement(PluginHashicorpView, {
    plugins: [
      { product: 'packer-plugin-amazon', name: 'amazon', mirrored_versions: 1 },
      { product: 'packer-plugin-docker', name: 'docker', mirrored_versions: 0, held_by: { kind: 'github', repository: 'acme-infra/packer-plugin-docker' } },
    ],
    selected: { product: 'packer-plugin-amazon', name: 'amazon', mirrored_versions: 1 },
    versions: [
      { version: '1.8.3', created_at: '2026-10-07T08:42:46Z', prerelease: false, state: 'supported', platforms: ['linux_amd64'], mirrored: false },
      { version: '1.8.2', created_at: '2026-07-13T08:13:00Z', prerelease: false, platforms: ['linux_amd64'], mirrored: true },
      { version: '1.9.0-beta.1', created_at: '2026-10-08T00:00:00Z', prerelease: true, platforms: ['linux_amd64'], mirrored: false },
    ],
    hasMore: true, preselected: ['linux_amd64'], failure: null, busy: false,
    onChoose: () => {}, onMore: () => {}, onImport: () => {},
  }))
  assert.match(html, /Held by GitHub \(acme-infra\/packer-plugin-docker\)/)
  assert.match(html, /freed once every version is removed/)
  assert.match(html, />Mirrored</)
  assert.match(html, /1\.8\.3/)
  assert.doesNotMatch(html, /1\.9\.0-beta\.1/)
  assert.match(html, /Load older releases/)
})

test('an import job shows each version\'s outcome and failed platforms', () => {
  const html = renderToStaticMarkup(React.createElement(PluginImportJobView, {
    job: {
      id: 'job', source: 'releases-hashicorp', product: 'packer-plugin-amazon', versions: ['1.8.3', '1.8.2', '1.8.1'],
      platforms: ['linux_amd64', 'windows_386'], changes: [], state: 'partially_succeeded', created_at: '2026-10-09T00:00:00Z',
      outcomes: [
        { version: '1.8.3', outcome: 'imported', platforms: [{ platform: 'linux_amd64', outcome: 'imported' }, { platform: 'windows_386', outcome: 'failed', error: 'not published upstream' }] },
        { version: '1.8.2', outcome: 'already_mirrored' },
        { version: '1.8.1', outcome: 'failed', error: 'SHA256SUMS signature does not verify against the HashiCorp key' },
      ],
    },
    registry: { enabled: true, exposed: false }, failure: null, organizationName: 'acme', host: 'dufflebag.example.com', onOpen: () => {},
  }))
  assert.match(html, /Partially succeeded/)
  assert.match(html, /Imported/)
  assert.match(html, /Already mirrored/)
  assert.match(html, /windows_386 failed/)
  assert.match(html, /not published upstream/)
  assert.match(html, /does not verify against the HashiCorp key/)
  assert.match(html, /version = &quot;1\.8\.3&quot;/)
  assert.match(html, /Packer can’t resolve the template stanza on this page until the registry is exposed/)
  assert.match(html, /href="\/plugin-registry\/settings"[\s\S]{0,300}?Registry settings</)
  assert.ok(html.indexOf('Registry settings') < html.indexOf('Import outcomes'))
})

test('a sync job shows each change in order with its outcome', () => {
  // Written by the platform handler (TestPluginSyncJobFixtureMatchesTheHandler).
  const job = JSON.parse(readFileSync(new URL('./fixtures/plugin-sync-job.json', import.meta.url), 'utf8'))
  const html = renderToStaticMarkup(React.createElement(PluginImportJobView, {
    job, registry: { enabled: true, exposed: true }, failure: null, organizationName: 'acme', host: 'dufflebag.example.com', onOpen: () => {},
  }))
  assert.match(html, /Sync amazon/)
  assert.match(html, /3 changes, applied in order/)
  assert.match(html, /add linux_amd64, windows_386/)
  assert.match(html, /windows_386 failed/)
  assert.match(html, /digest does not match SHA256SUMS/)
  assert.match(html, />Restored</)
  assert.match(html, />restore</)
  assert.match(html, /Open amazon/)
  assert.doesNotMatch(html, /Platforms: /)
})

test('an import is queued with the selected versions and platforms', async () => {
  const originalFetch = globalThis.fetch
  let sent
  globalThis.fetch = async (path, options) => {
    sent = { path, method: options.method, body: JSON.parse(options.body) }
    return new Response(JSON.stringify({ id: 'job' }), { status: 202, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    await createPluginImport('token', 'org', 'packer-plugin-amazon', ['1.8.3'], ['linux_amd64'])
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.deepEqual(sent, {
    path: '/api/v1/organizations/org/plugin-registry/imports', method: 'POST',
    body: { source: 'releases-hashicorp', product: 'packer-plugin-amazon', versions: ['1.8.3'], platforms: ['linux_amd64'] },
  })
})

const githubView = (release, link = 'https://github.com/ethanmdavidson/packer-plugin-git/releases/latest') =>
  renderToStaticMarkup(React.createElement(PluginGithubView, {
    link, release, preselected: ['linux_amd64'], failure: null, busy: false,
    onLinkChange: () => {}, onResolve: () => {}, onImport: () => {},
  }))
const gitRelease = {
  repository: 'ethanmdavidson/packer-plugin-git', name: 'git', tag: 'v0.6.3', version: '0.6.3', prerelease: false,
  platforms: ['darwin_arm64', 'linux_amd64'], has_checksum: true,
}

test('a resolved GitHub release shows what was inferred, pinned to its tag', () => {
  const html = githubView(gitRelease)
  assert.match(html, /git · 0\.6\.3/)
  assert.match(html, /tag v0\.6\.3, the latest release now\. The import uses this tag/)
  assert.match(html, /Import git 0\.6\.3 · 1 platforms/)
})

test('a GitHub release without SHA256SUMS or under a held name cannot be imported', () => {
  const unsummed = githubView({ ...gitRelease, has_checksum: false })
  assert.match(unsummed, /no SHA256SUMS asset/)
  assert.doesNotMatch(unsummed, /Import git/)
  const held = githubView({ ...gitRelease, held_by: { kind: 'releases-hashicorp', repository: 'packer-plugin-git' } })
  assert.match(held, /git is held by another source/)
  assert.match(held, /freed once every git version is removed, and revoked versions still count/)
  assert.doesNotMatch(held, /Import git/)
})

test('a GitHub import sends the repository and the pinned tag', async () => {
  const originalFetch = globalThis.fetch
  let body
  globalThis.fetch = async (_path, options) => {
    body = JSON.parse(options.body)
    return new Response(JSON.stringify({ id: 'job' }), { status: 202, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    await createGithubImport('token', 'org', 'ethanmdavidson/packer-plugin-git', 'v0.6.3', ['linux_amd64'])
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.deepEqual(body, { source: 'github', product: 'ethanmdavidson/packer-plugin-git', versions: ['v0.6.3'], platforms: ['linux_amd64'] })
})

// Written by the platform handler (TestPluginCatalogueFixtureMatchesTheHandler).
const catalogue = JSON.parse(readFileSync(new URL('./fixtures/plugin-catalogue.json', import.meta.url), 'utf8')).plugins

test('the catalogue marks plugins with an update and offers only those for sync', () => {
  const html = render({ registry: { enabled: true, exposed: true }, callerRole: 'publisher', plugins: catalogue })
  assert.match(html, /Update available · 1\.8\.3/)
  assert.equal((html.match(/Update available/g) ?? []).length, 1, 'a failed check or a current plugin shows no pill')
  const box = (name) => html.match(new RegExp(`<input[^>]*aria-label="Select ${name} to sync"[^>]*>`))[0]
  assert.doesNotMatch(box('amazon'), /disabled/)
  for (const name of ['docker', 'git', 'probe']) assert.match(box(name), /disabled/, `${name} has no update to sync`)
  assert.match(html, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Sync selected \(0\)/)
  const reader = render({ registry: { enabled: true, exposed: true }, callerRole: 'reader', plugins: catalogue })
  assert.match(reader, /Update available · 1\.8\.3/)
  assert.doesNotMatch(reader, /Sync selected/)
})

test('plugin detail shows update checking quietly, with the last failure but no alert', () => {
  const view = (name, callerRole) => renderToStaticMarkup(React.createElement(PluginDetailView, {
    name, organizationName: 'acme', host: 'dufflebag.example.com', callerRole,
    registry: { enabled: true, exposed: true }, loading: false, failure: null, onRefresh: () => {}, onUpload: () => {},
    busy: false, actionFailure: null, editing: false, upstream: [], onEdit: () => {}, onCancelEdit: () => {}, onSync: () => {}, onRemove: () => {},
    summary: catalogue.find((plugin) => plugin.name === name), onToggleUpdates: () => {},
    detail: { name, source: catalogue.find((plugin) => plugin.name === name).source, versions: [] },
  }))
  const docker = view('docker', 'publisher')
  assert.match(docker, /Check for updates/)
  assert.match(docker, /The last check failed: releases\.hashicorp\.com could not be reached/)
  assert.doesNotMatch(docker, /pf-m-danger|pf-m-warning/, 'a failed check is a quiet state, not an alert')
  assert.match(view('amazon', 'reader'), /Update checks are on/)
  assert.match(view('amazon', 'reader'), /Update available · 1\.8\.3/)
  assert.doesNotMatch(view('probe', 'publisher'), /Check for updates/, 'an upload has no upstream')
})

test('update checks and catalogue sync use their paths', async () => {
  const originalFetch = globalThis.fetch
  const sent = []
  globalThis.fetch = async (path, options) => {
    sent.push({ path, method: options.method, body: JSON.parse(options.body) })
    return path.endsWith('/sync')
      ? new Response(JSON.stringify({ results: [{ plugin: 'amazon', version: '1.8.3', import_id: 'job' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      : new Response(null, { status: 204 })
  }
  let results
  try {
    await setPluginUpdateCheck('token', 'org', 'amazon', true)
    results = await syncCatalogue('token', 'org', ['amazon'])
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.deepEqual(sent, [
    { path: '/api/v1/organizations/org/plugin-registry/plugins/amazon/update-check', method: 'PUT', body: { enabled: true } },
    { path: '/api/v1/organizations/org/plugin-registry/sync', method: 'POST', body: { plugins: ['amazon'] } },
  ])
  assert.equal(results[0].import_id, 'job')
})
