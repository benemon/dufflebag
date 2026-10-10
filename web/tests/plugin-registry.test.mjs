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
let planCatalogueSync
let catalogueRows
let pendingItems
let retryPluginImport
let githubRateLimit
let CatalogueSyncConfirmationView
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
    PluginRegistryView, CatalogueSyncConfirmationView,
    pluginRegistryErrorMessage,
  } = await vite.ssrLoadModule('/src/screens/Plugins.tsx'))
  ;({ TypedConfirmModalView } =
    await vite.ssrLoadModule('/src/components/TypedConfirmModal.tsx'))
  ;({ PluginErrorCard } = await vite.ssrLoadModule('/src/components/PluginLoadState.tsx'))
  ;({
    disablePluginRegistry, enablePluginRegistry, exposePluginRegistry, getPluginRegistry,
    unexposePluginRegistry, planPluginUploads, templateStanza, listPlugins, publishPluginVersion,
    deletePluginVersion, pluginChanges, syncPlugin, syncCatalogue, setPluginUpdateCheck, planCatalogueSync, catalogueRows, pendingItems, retryPluginImport, githubRateLimit, createPluginImport, createGithubImport,
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
  onOpenImport: () => {}, onPlanSync: async () => [], onSyncSelected: async () => [],
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
        onEdit: () => {}, onDiscard: () => {}, onOlderUpstream: () => {}, hasOlderUpstream: false, onSync: () => {}, onRemove: () => {},
      })),
    },
    {
      name: 'job', crumbs: ['Registry', 'Plugins', 'Plugin', 'Job 01KZF3QW'], loading: 'Loading job…', error: 'Job could not be loaded',
      view: (loading, failure) => renderToStaticMarkup(React.createElement(PluginImportJobView, {
        ...callbacks, id: '01KZF3QW7N', job: null, registry: null, loading, failure,
        organizationName: 'acme', host: 'dufflebag.example.com', onOpen: () => {},
              callerRole: 'publisher', busy: false, actionFailure: null, onRetry: () => {},
})),
    },
    {
      name: 'hashicorp', crumbs: ['Registry', 'Plugins', 'Browse HashiCorp'], loading: 'Reading releases.hashicorp.com…', error: "HashiCorp's plugin list could not be read",
      view: (loading, failure) => renderToStaticMarkup(React.createElement(PluginHashicorpView, {
        ...callbacks, organizationName: 'acme', plugins: null, selected: null, versions: null, heldVersions: [], hasMore: false, preselected: [],
        loading, failure, busy: false, onChoose: () => {}, onMore: () => {}, onImport: () => {},
      })),
    },
    {
      name: 'github', crumbs: ['Registry', 'Plugins', 'Import from GitHub'], loading: 'Loading GitHub release…', error: 'GitHub release could not be loaded',
      view: (loading, failure) => renderToStaticMarkup(React.createElement(PluginGithubView, {
        ...callbacks, link: '', release: null, preselected: [], loading, failure, busy: false,
        onLinkChange: () => {}, onResolve: () => {}, onImport: () => {},
              organizationName: 'acme', mirroredVersions: null,
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
    assert.match(failed, new RegExp(screen.error.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replaceAll("'", '&#x27;')), `${screen.name}: error title`)
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
    busy: false, actionFailure: null, editing: false, upstream: [], summary: null, onEdit: () => {}, onDiscard: () => {}, onOlderUpstream: () => {}, hasOlderUpstream: false, onSync: () => {}, onRemove: () => {},
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
  assert.match(html, /<td[^>]*data-label="darwin_arm64"[^>]*><span title="Upstream, not mirrored">○<\/span><\/td>/)
  assert.match(html, /<td[^>]*data-label="linux_arm64"[^>]*><span title="Mirrored"[^>]*>●<\/span><\/td>/)
  assert.match(html, /<td[^>]*data-label="windows_386"[^>]*><span title="Not published upstream">–<\/span><\/td>/)
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
    busy: false, actionFailure: null, editing: false, upstream: [], summary: null, onEdit: () => {}, onDiscard: () => {}, onOlderUpstream: () => {}, hasOlderUpstream: false, onSync: () => {}, onRemove: () => {},
    detail: { name: 'git', source: { kind: 'upload' }, versions: [{
      version: '0.6.3', revoked, created_at: '2026-10-09T00:00:00Z', listed_platforms: [], stored_platforms: [],
    }] },
  }))
  const served = view('publisher', false)
  assert.match(served, />Edit versions</)
  assert.match(served, /aria-label="Kebab toggle"/, 'the row menu carries Remove version')
  assert.match(served, />Available</)
  assert.match(served, /Not available for uploaded plugins\. To add a version, upload its files\./)
  assert.match(served, />Upload version</)
  assert.doesNotMatch(served, /Check daily for a newer stable version/)
  const revoked = view('publisher', true)
  assert.match(revoked, />Revoked</)
  assert.match(revoked, /No version is available: every version is revoked\./)
  const reader = view('reader', false)
  assert.doesNotMatch(reader, />Edit versions</)
  assert.doesNotMatch(reader, /aria-label="Kebab toggle"/)
  assert.doesNotMatch(reader, />Upload version</)
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
    busy: false, actionFailure: null, editing: true, upstream: amazonUpstream, summary: null,
    onEdit: () => {}, onDiscard: () => {}, onOlderUpstream: () => {}, hasOlderUpstream: false, onSync: () => {}, onRemove: () => {},
    detail: { name: 'amazon', source: { kind: 'releases-hashicorp', repository: 'packer-plugin-amazon' }, versions: amazonVersions },
  }))
  assert.match(html, /aria-label="Add linux_arm64 to 1\.8\.2"/)
  assert.match(html, /aria-label="Add darwin_arm64 to 1\.8\.2"/)
  assert.doesNotMatch(html, /aria-label="Add linux_amd64 to 1\.8\.2"/, 'a mirrored platform is a status, not a control')
  assert.doesNotMatch(html, /aria-label="Add linux_arm64 to 1\.8\.1"/, 'a revoked version takes no platforms until restored')
  assert.match(html, /aria-label="Add 1\.8\.3"/)
  assert.doesNotMatch(html, /aria-label="Add 1\.8\.2"/, 'a mirrored version is not offered again')
  assert.match(html, /aria-label="Serve 1\.8\.1"/)
  assert.match(html, /Pending changes/)
  assert.match(html, /None yet\. Nothing changes until you sync\./)
  assert.match(html, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Sync</)
  assert.match(html, />Discard</)
  assert.match(html, /Applied as one job\./)
  assert.match(html, /Editing: tick a version to add or restore it, untick to revoke/)
  assert.doesNotMatch(html, />Edit versions</)
  assert.match(html, /<th[^>]*colSpan="4"[^>]*>linux</, 'architecture columns are grouped by OS')
  assert.match(html, /Not mirrored/)
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

const hashicorpPlugins = [
  { product: 'packer-plugin-amazon', name: 'amazon', mirrored_versions: 1 },
  { product: 'packer-plugin-docker', name: 'docker', mirrored_versions: 0, held_by: { kind: 'github', repository: 'acme-infra/packer-plugin-docker' } },
  { product: 'packer-plugin-vsphere', name: 'vsphere', mirrored_versions: 0 },
]
const amazonReleases = [
  { version: '1.8.3', created_at: '2026-10-07T08:42:46Z', prerelease: false, state: 'supported', changelog: 'https://github.com/hashicorp/packer-plugin-amazon/blob/main/CHANGELOG.md', platforms: ['linux_amd64', 'linux_arm64'], mirrored: false },
  { version: '1.8.2', created_at: '2026-07-13T08:13:00Z', prerelease: false, platforms: ['linux_amd64'], mirrored: true },
  { version: '1.9.0-beta.1', created_at: '2026-10-08T00:00:00Z', prerelease: true, platforms: ['linux_amd64'], mirrored: false },
]
const hashicorpView = (props) => renderToStaticMarkup(React.createElement(PluginHashicorpView, {
  organizationName: 'acme', plugins: hashicorpPlugins, selected: hashicorpPlugins[0], versions: amazonReleases,
  heldVersions: [{ version: '1.8.2', revoked: false, created_at: '2026-07-13T08:13:00Z', listed_platforms: [], stored_platforms: [{ os: 'linux', arch: 'amd64' }] }],
  hasMore: true, preselected: ['linux_amd64'], loading: false, failure: null, busy: false,
  onBackToRegistry: () => {}, onBackToPlugins: () => {}, onRefresh: () => {}, onChoose: () => {}, onMore: () => {}, onImport: () => {},
  ...props,
}))

test('browsing HashiCorp lists the published plugins with their standing and a selected plugin\'s releases', () => {
  const html = hashicorpView({})
  assert.match(html, /Plugins[\s\S]{0,200}?· 3 published/)
  assert.match(html, /Held from GitHub \(acme-infra\/packer-plugin-docker\)/)
  assert.match(html, />1 mirrored</)
  assert.match(html, />Not mirrored</)
  assert.match(html, /github\.com\/hashicorp\/packer-plugin-amazon/)
  assert.match(html, /Show prereleases/)
  assert.match(html, /Platform: any/)
  assert.match(html, /2 of 3 versions · prereleases hidden/)
  assert.match(html, /<th[^>]*>Lifecycle<\/th><th[^>]*>Platforms<\/th><th[^>]*>Changelog<\/th><th[^>]*>In dufflebag</)
  assert.match(html, /<code>1\.8\.3<\/code>/)
  assert.match(html, />supported</)
  assert.match(html, />2 platforms</)
  assert.match(html, /href="https:\/\/github\.com\/hashicorp\/packer-plugin-amazon\/blob\/main\/CHANGELOG\.md"[^>]*>Changelog ↗</)
  assert.match(html, />Available<[\s\S]{0,300}?1 platforms/, 'a mirrored version shows its standing and stored platform count')
  assert.match(html, /id="version-1\.8\.2"[^>]*disabled/, 'a mirrored version cannot be chosen again')
  assert.doesNotMatch(html, /1\.9\.0-beta\.1/)
  assert.match(html, />Show older releases</)
  assert.match(html, /Platforms to import/)
  assert.match(html, /Preselected: the architectures amazon already has\./)
  assert.match(html, /id="platform-linux_amd64"[^>]*checked/)
  assert.match(html, />existing</)
  assert.match(html, /<button[^>]*disabled[^>]*>[\s\S]{0,200}?Import</)
  assert.match(html, /Select at least one version/)
  assert.match(html, />Cancel</)
})

test('a held plugin explains the hold and offers no import; a first import preselects the organization defaults', () => {
  const held = hashicorpView({ selected: hashicorpPlugins[1], versions: [amazonReleases[0]], heldVersions: [], preselected: [] })
  assert.match(held, /docker is held from GitHub/)
  assert.match(held, /In acme, docker comes from GitHub \(acme-infra\/packer-plugin-docker\)\. A name belongs to one source per organization, so HashiCorp’s docker can’t be imported\. The name is freed once every docker version is removed; revoked versions still count\./)
  assert.match(held, />Not importable</)
  assert.match(held, /id="version-1\.8\.3"[^>]*disabled/)
  assert.doesNotMatch(held, /Platforms to import/)
  const first = hashicorpView({ selected: hashicorpPlugins[2], versions: [amazonReleases[0]], heldVersions: [], preselected: ['linux_amd64', 'darwin_arm64'] })
  assert.match(first, /Preselected: acme’s default platforms \(Registry settings\), since this is a first import\./)
  assert.match(first, />org default</)
  assert.match(first, /id="platform-darwin_arm64"[^>]*checked/)
})

const jobViewProps = {
  id: 'job', registry: { enabled: true, exposed: true }, loading: false, failure: null, organizationName: 'acme', host: 'dufflebag.example.com',
  callerRole: 'publisher', busy: false, actionFailure: null,
  onBackToRegistry: () => {}, onBackToPlugins: () => {}, onRefresh: () => {}, onOpen: () => {}, onRetry: () => {},
}
const importJob = (state, outcomes, extra = {}) => ({
  id: '01KZF3QW7N00000000000000000', source: 'releases-hashicorp', product: 'packer-plugin-amazon', versions: ['1.8.3', '1.8.2', '1.8.1'],
  platforms: ['linux_amd64', 'windows_386'], changes: [], state, created_at: '2026-10-09T14:02:00Z',
  created_by: 'alice', origin: 'import', batch_index: 1, batch_size: 1, queued_ahead: 0, outcomes, ...extra,
})

test('an import job shows each version\'s outcome and failed platforms, with a retry for the failed ones', () => {
  const job = importJob('partially_succeeded', [
    { version: '1.8.3', outcome: 'imported', platforms: [{ platform: 'linux_amd64', outcome: 'imported' }, { platform: 'windows_386', outcome: 'failed', error: 'not published upstream' }] },
    { version: '1.8.2', outcome: 'already_mirrored' },
    { version: '1.8.1', outcome: 'failed', error: 'SHA256SUMS signature does not verify against the HashiCorp key' },
  ], { finished_at: '2026-10-09T14:04:00Z' })
  const html = renderToStaticMarkup(React.createElement(PluginImportJobView, { ...jobViewProps, job, registry: { enabled: true, exposed: false } }))
  assert.match(html, /Import amazon/)
  assert.match(html, /Partially succeeded/)
  assert.match(html, /Started by alice · [^·]+ · finished [^·]+ · from Browse HashiCorp \(packer-plugin-amazon\)/)
  assert.match(html, />Job 01KZF3QW</)
  assert.match(html, /1 of 3 versions applied/)
  assert.match(html, /1\.8\.3 didn’t get windows_386; 1\.8\.1 didn’t get applied\. Other versions are live\. Retrying runs only the failed versions\./)
  assert.match(html, />Retry failed versions</)
  assert.match(html, /Outcome[\s\S]{0,400}?1 of 3 versions applied\. 2 versions failed\./)
  assert.match(html, />Imported</)
  assert.match(html, />Already mirrored</)
  assert.match(html, /<code>windows_386<\/code>[\s\S]{0,400}?not published upstream[\s\S]{0,400}?>Failed</)
  assert.match(html, /<code>all<\/code>[\s\S]{0,400}?does not verify against the HashiCorp key/)
  assert.match(html, /1 of 2</, 'a version with a failed platform counts its imported ones')
  assert.match(html, /version = &quot;1\.8\.3&quot;/)
  assert.match(html, /Pins the newest version this job made available\./)
  assert.match(html, /Packer can’t resolve the template stanza on this page until the registry is exposed/)
  assert.match(html, /href="\/plugin-registry\/settings"[\s\S]{0,300}?Registry settings</)
  assert.ok(html.indexOf('Registry settings') < html.indexOf('Import outcomes'))
  const reader = renderToStaticMarkup(React.createElement(PluginImportJobView, { ...jobViewProps, job, callerRole: 'reader' }))
  assert.doesNotMatch(reader, />Retry failed versions</)
})

test('a job page states its outcome per state: queued, running, failed, succeeded', () => {
  const render = (job) => renderToStaticMarkup(React.createElement(PluginImportJobView, { ...jobViewProps, job }))
  const queued = render(importJob('queued', [], { queued_ahead: 1, origin: 'catalogue', batch_index: 1, batch_size: 3 }))
  assert.match(queued, /Waiting for 1 job ahead of it\./)
  assert.match(queued, /from Sync selected \(1 of 3 jobs\)/)
  assert.match(queued, />Waiting</)
  assert.match(queued, /Appears when the job imports something\./)
  assert.doesNotMatch(queued, /Open amazon/)
  const running = render(importJob('running', [{ version: '1.8.3', outcome: 'imported', platforms: [{ platform: 'linux_amd64', outcome: 'imported' }, { platform: 'windows_386', outcome: 'imported' }] }]))
  assert.match(running, /1 of 3 versions done\./)
  assert.match(running, /aria-label="Job progress"/)
  assert.match(running, />Importing</)
  assert.match(running, />Waiting</)
  const failed = render(importJob('failed', [{ version: '1.8.3', outcome: 'failed', error: 'releases.hashicorp.com unreachable: i/o timeout' }], { versions: ['1.8.3'], finished_at: '2026-10-09T14:03:00Z' }))
  assert.match(failed, />Job failed</)
  assert.match(failed, /i\/o timeout Nothing was changed\./)
  assert.match(failed, />Retry job</)
  assert.match(failed, /No stanza: nothing was imported\./)
  const succeeded = render(importJob('succeeded', [
    { version: '1.8.3', outcome: 'imported', platforms: [{ platform: 'linux_amd64', outcome: 'imported' }, { platform: 'windows_386', outcome: 'imported' }] },
    { version: '1.8.2', outcome: 'imported', platforms: [{ platform: 'linux_amd64', outcome: 'imported' }, { platform: 'windows_386', outcome: 'already_mirrored' }] },
    { version: '1.8.1', outcome: 'already_mirrored' },
  ], { finished_at: '2026-10-09T14:03:00Z' }))
  assert.match(succeeded, /3 versions\. 3 files imported, 2 already mirrored\./)
  assert.match(succeeded, /Identical to the stored file; nothing written\./)
  assert.doesNotMatch(succeeded, /Retry/)
})

test('retrying a job queues only its failed part', async () => {
  const originalFetch = globalThis.fetch
  const posted = []
  globalThis.fetch = async (path, options) => {
    posted.push({ path, body: JSON.parse(options.body) })
    return new Response(JSON.stringify({ id: 'next' }), { status: 202, headers: { 'content-type': 'application/json' } })
  }
  try {
    const sync = JSON.parse(readFileSync(new URL('./fixtures/plugin-sync-job.json', import.meta.url), 'utf8'))
    await retryPluginImport('token', 'org', sync)
    assert.deepEqual(posted.at(-1).body, { changes: [
      { version: '1.8.3', action: 'add', platforms: ['windows_386'] },
      { version: '1.8.2', action: 'add', platforms: ['linux_arm64'] },
    ] }, 'a partially failed add keeps only its failed platforms; a succeeded restore is not repeated')
    assert.match(posted.at(-1).path, /plugins\/amazon\/sync$/)
    await retryPluginImport('token', 'org', importJob('partially_succeeded', [
      { version: '1.8.3', outcome: 'imported' }, { version: '1.8.2', outcome: 'failed', error: 'x' }, { version: '1.8.1', outcome: 'failed', error: 'y' },
    ]))
    assert.deepEqual(posted.at(-1).body, { source: 'releases-hashicorp', product: 'packer-plugin-amazon', versions: ['1.8.2', '1.8.1'], platforms: ['linux_amd64', 'windows_386'] })
    await retryPluginImport('token', 'org', importJob('failed', [{ version: 'v0.6.4', outcome: 'failed', error: 'x' }], { source: 'github', product: 'owner/packer-plugin-git', versions: ['v0.6.4'] }))
    assert.deepEqual(posted.at(-1).body, { source: 'github', product: 'owner/packer-plugin-git', versions: ['v0.6.4'], platforms: ['linux_amd64', 'windows_386'] })
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('a sync job shows each change in order with its outcome', () => {
  // Written by the platform handler (TestPluginSyncJobFixtureMatchesTheHandler).
  const job = JSON.parse(readFileSync(new URL('./fixtures/plugin-sync-job.json', import.meta.url), 'utf8'))
  const html = renderToStaticMarkup(React.createElement(PluginImportJobView, { ...jobViewProps, job }))
  assert.match(html, /Sync amazon/)
  assert.match(html, /Started by test · [^·]+ · finished [^·]+ · from the plugin page, 3 changes/)
  assert.match(html, /1 of 3 changes applied\. 2 changes failed\./)
  assert.match(html, /<td[^>]*>Add<\/td><td[^>]*>linux_amd64, windows_386</)
  assert.match(html, /<td[^>]*>Restore<\/td><td[^>]*>—</)
  assert.match(html, /<code>windows_386<\/code>[\s\S]{0,400}?not published upstream/)
  assert.match(html, /<code>all<\/code>[\s\S]{0,400}?digest does not match SHA256SUMS/)
  assert.match(html, />Restored</)
  assert.match(html, /Open amazon/)
  assert.match(html, />Retry failed changes</)
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

const githubView = (release, props = {}, link = 'https://github.com/ethanmdavidson/packer-plugin-git/releases/latest') =>
  renderToStaticMarkup(React.createElement(PluginGithubView, {
    organizationName: 'acme', link, release, mirroredVersions: 0, preselected: ['linux_amd64'], loading: false, failure: null, busy: false,
    onBackToRegistry: () => {}, onBackToPlugins: () => {}, onLinkChange: () => {}, onResolve: () => {}, onRefresh: () => {}, onImport: () => {},
    ...props,
  }))
const gitRelease = {
  repository: 'ethanmdavidson/packer-plugin-git', name: 'git', tag: 'v0.6.3', version: '0.6.3', prerelease: false,
  published_at: '2026-09-28T12:00:00Z', platforms: ['darwin_arm64', 'linux_amd64'], has_checksum: true, checksum_asset: 'packer-plugin-git_v0.6.3_SHA256SUMS',
}

test('a resolved GitHub release shows what was inferred, pinned to its tag', () => {
  const html = githubView(gitRelease)
  assert.match(html, /Release link/)
  assert.match(html, /Latest is pinned to an exact version when you resolve it\./)
  assert.match(html, /Inferred from the release/)
  assert.match(html, /<dt[^>]*>[\s\S]{0,120}?Repository[\s\S]{0,400}?ethanmdavidson\/packer-plugin-git/)
  assert.match(html, /Plugin name[\s\S]{0,400}?>git<[\s\S]{0,300}?from the repository name/)
  assert.match(html, /Version[\s\S]{0,400}?>0\.6\.3<[\s\S]{0,300}?from \/releases\/latest, tag v0\.6\.3/)
  assert.match(html, /Checksum file[\s\S]{0,400}?packer-plugin-git_v0\.6\.3_SHA256SUMS/)
  assert.match(html, /In dufflebag[\s\S]{0,400}?New plugin/)
  assert.match(html, /Platforms[\s\S]{0,200}?· 2 in the release · first import, so acme’s default platforms are preselected/)
  assert.match(html, /id="github-platform-linux_amd64"[^>]*checked/)
  assert.match(html, />org default</)
  assert.match(html, /Import v0\.6\.3 · 1 platform</)
  assert.match(html, />Cancel</)
  const tagged = githubView(gitRelease, { mirroredVersions: 2 }, 'https://github.com/ethanmdavidson/packer-plugin-git/releases/tag/v0.6.3')
  assert.match(tagged, /Version[\s\S]{0,400}?>0\.6\.3<[\s\S]{0,300}?tag v0\.6\.3</)
  assert.match(tagged, /2 versions mirrored/)
  assert.match(tagged, /the architectures git already has are preselected/)
  assert.match(tagged, />existing</)
})

test('a GitHub release without SHA256SUMS or under a held name cannot be imported', () => {
  const unsummed = githubView({ ...gitRelease, has_checksum: false, checksum_asset: undefined })
  assert.match(unsummed, /Not importable: v0\.6\.3 has no checksum file/)
  assert.match(unsummed, /The release has 2 binaries but no SHA256SUMS asset\. dufflebag verifies every binary against the release’s checksums, so it won’t import this release\./)
  assert.match(unsummed, /Checksum file[\s\S]{0,400}?None found/)
  assert.doesNotMatch(unsummed, /Import v0\.6\.3/)
  const held = githubView({ ...gitRelease, held_by: { kind: 'releases-hashicorp', repository: 'packer-plugin-git' } })
  assert.match(held, /git is held from HashiCorp/)
  assert.match(held, /In acme, git comes from HashiCorp \(packer-plugin-git\)\. A name belongs to one source per organization, so this GitHub release is refused\. The name is freed once every git version is removed; revoked versions still count\./)
  assert.match(held, /In dufflebag[\s\S]{0,400}?Held from HashiCorp/)
  assert.doesNotMatch(held, /Import v0\.6\.3/)
})

test('a GitHub rate limit is explained with its reset, and resolving waits', () => {
  const html = githubView(null, { failure: 'GitHub rate limit reached; it resets at 2099-01-01T00:00:00Z' })
  assert.match(html, />GitHub rate limit reached</)
  assert.match(html, /all 60 unauthenticated requests this hour are used\. The limit resets at [^,]+, in \d+ minutes\. Mirrored plugins are unaffected\./)
  assert.doesNotMatch(html, /GitHub release could not be loaded/)
  assert.match(html, /<button[^>]*disabled[^>]*>[\s\S]{0,100}?Resolve</)
  assert.deepEqual(githubRateLimit('GitHub could not be reached'), null)
  assert.equal(githubRateLimit('GitHub rate limit reached; it resets at 2026-10-09T18:00:00Z').resetsAt.toISOString(), '2026-10-09T18:00:00.000Z')
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

test('the catalogue is the designed card: toolbar filters, Updates column, kebab, pager', () => {
  const html = render({ registry: { enabled: true, exposed: true }, callerRole: 'publisher', plugins: catalogue })
  // Header carries the description and the three ways in (design 1.0).
  assert.match(html, /Packer plugins mirrored for acme\. packer init resolves them from dufflebag\.example\.com\/plugins\/acme\./)
  for (const action of ['Browse HashiCorp', 'Import from GitHub', 'Upload']) assert.match(html, new RegExp(`<button[^>]*>(?:<span[^>]*>)?${action}<`))
  // Toolbar: name filter, source filter, Update available, selection text, Sync selected, pager.
  assert.match(html, /aria-label="Filter plugins by name"/)
  assert.match(html, />All sources</)
  assert.match(html, /id="updates-only"[^>]*>/)
  assert.match(html, /Select plugins with an update/)
  assert.match(html, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Sync selected</)
  assert.match(html, /aria-label="Plugins pagination"/)
  // Columns and cells.
  assert.match(html, /<th[^>]*>Updates</)
  assert.match(html, /Update available · 1\.8\.3/)
  assert.match(html, />Up to date</)
  assert.match(html, />Checks off</)
  assert.match(html, />Not checked \(upload\)</)
  assert.match(html, /<code>ethanmdavidson\/packer-plugin-git<\/code>/)
  assert.match(html, /aria-label="Kebab toggle"/)
  const box = (name) => html.match(new RegExp(`<input[^>]*aria-label="Select ${name} to sync"[^>]*>`))[0]
  assert.doesNotMatch(box('amazon'), /disabled/)
  for (const name of ['docker', 'git', 'probe']) {
    assert.match(box(name), /disabled/, `${name} has no update to sync`)
    assert.match(box(name), /title="Only plugins with an update available can be synced"/)
  }
  // No action buttons in the toolbar, no settings card, no exposed alert.
  assert.doesNotMatch(html, /Upload plugin files/)
  assert.doesNotMatch(html, /Save default platforms/)
  assert.doesNotMatch(html, /The registry is exposed/)

  const reader = render({ registry: { enabled: true, exposed: true }, callerRole: 'reader', plugins: catalogue })
  assert.match(reader, /You have read-only access; publishers add and sync plugins\./)
  assert.match(reader, /Update available · 1\.8\.3/)
  assert.doesNotMatch(reader, /Sync selected|aria-label="Kebab toggle"|Select amazon to sync|Browse HashiCorp/)
})

test('the enabled empty catalogue offers Browse HashiCorp and two links', () => {
  const html = render({ registry: { enabled: true, exposed: true }, callerRole: 'publisher', plugins: [] })
  assert.match(html, /No plugins mirrored yet/)
  assert.match(html, /pf-m-primary[^>]*>(?:<span[^>]*>)?Browse HashiCorp</)
  assert.match(html, /pf-m-link[^>]*>(?:<span[^>]*>)?Import from a GitHub release</)
  assert.match(html, /pf-m-link[^>]*>(?:<span[^>]*>)?Upload plugin files</)
})

test("the sync confirmation lists each plugin's move and warns about skipped architectures", () => {
  const chosen = catalogue.filter((plugin) => plugin.name === 'amazon')
  const view = (plan) => renderToStaticMarkup(React.createElement(CatalogueSyncConfirmationView, {
    plugins: chosen, plan, busy: false, onCancel: () => {}, onStart: () => {},
  }))
  const planning = view(null)
  assert.match(planning, /Sync 1 plugin</)
  assert.match(planning, /This starts 1 separate job, one per plugin\./)
  assert.match(planning, /Planning the sync…/)
  assert.match(planning, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Start 1 job</, 'nothing starts before the plan is known')
  const planned = view([{ plugin: 'amazon', from: '1.8.2', to: '1.8.3', architectures: '2 of its 3 architectures', warning: "windows_386 isn't published for 1.8.3 and will be skipped" }])
  assert.match(planned, /1\.8\.2 → 1\.8\.3/)
  assert.match(planned, /2 of its 3 architectures/)
  assert.match(planned, /windows_386 isn&#x27;t published for 1\.8\.3 and will be skipped/)
  assert.doesNotMatch(planned, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Start 1 job</)
  assert.match(planned, />Cancel</)
})

test("planning a catalogue sync compares each plugin's architectures with the target release", async () => {
  const originalFetch = globalThis.fetch
  const calls = []
  globalThis.fetch = async (path) => {
    calls.push(path)
    const json = (body) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    if (path.endsWith('/plugins/amazon/versions')) return json({ name: 'amazon', source: {}, versions: [{ version: '1.8.2', revoked: false, created_at: '', listed_platforms: [], stored_platforms: [{ os: 'linux', arch: 'amd64' }, { os: 'windows', arch: '386' }] }] })
    if (path.includes('/catalogue/hashicorp/packer-plugin-amazon')) return json({ versions: [{ version: '1.8.3', created_at: '', prerelease: false, platforms: ['linux_amd64'], mirrored: false }] })
    if (path.endsWith('/plugins/git/versions')) return json({ name: 'git', source: {}, versions: [{ version: '0.6.3', revoked: false, created_at: '', listed_platforms: [], stored_platforms: [{ os: 'linux', arch: 'arm64' }] }] })
    if (path.endsWith('/catalogue/github/resolve')) return json({ repository: 'ethanmdavidson/packer-plugin-git', name: 'git', tag: 'v0.6.4', version: '0.6.4', prerelease: false, platforms: ['linux_arm64', 'darwin_arm64'], has_checksum: true })
    throw new Error(`unexpected ${path}`)
  }
  let plan
  try {
    plan = await planCatalogueSync('token', 'org', [
      catalogue.find((p) => p.name === 'amazon'),
      { name: 'git', source: { kind: 'github', repository: 'ethanmdavidson/packer-plugin-git' }, published_versions: 1, newest_version: '0.6.3', update_available: true, update_check: { enabled: true, latest: '0.6.4', latest_tag: 'v0.6.4' } },
    ])
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.deepEqual(plan, [
    { plugin: 'amazon', from: '1.8.2', to: '1.8.3', architectures: '1 of its 2 architectures', warning: "windows_386 isn't published for 1.8.3 and will be skipped" },
    { plugin: 'git', from: '0.6.3', to: '0.6.4', architectures: 'its 1 architecture', warning: '' },
  ])
  assert.ok(calls.some((c) => c.includes('/catalogue/github/resolve')), 'a GitHub plugin resolves the tag the check saw')
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

test('the catalogue filters combine: name, source and update availability', () => {
  const names = (filters) => catalogueRows(catalogue, { name: '', source: 'All sources', updatesOnly: false, ...filters }).map((p) => p.name)
  assert.deepEqual(names({}), ['amazon', 'docker', 'git', 'probe'])
  assert.deepEqual(names({ source: 'HashiCorp' }), ['amazon', 'docker'])
  assert.deepEqual(names({ source: 'GitHub' }), ['git'])
  assert.deepEqual(names({ source: 'Local' }), ['probe'])
  assert.deepEqual(names({ updatesOnly: true }), ['amazon'])
  assert.deepEqual(names({ name: ' GIT ' }), ['git'])
  assert.deepEqual(names({ source: 'HashiCorp', updatesOnly: true, name: 'doc' }), [])
})

test('the detail header carries the update pills and the update-check card its last result', () => {
  const view = (update_check, update_available) => renderToStaticMarkup(React.createElement(PluginDetailView, {
    name: 'amazon', organizationName: 'acme', host: 'dufflebag.example.com', callerRole: 'publisher',
    registry: { enabled: true, exposed: true }, loading: false, failure: null, onRefresh: () => {}, onUpload: () => {},
    busy: false, actionFailure: null, editing: false, upstream: [], hasOlderUpstream: true,
    onEdit: () => {}, onDiscard: () => {}, onOlderUpstream: () => {}, onSync: () => {}, onRemove: () => {}, onToggleUpdates: () => {},
    summary: { name: 'amazon', source: { kind: 'releases-hashicorp', repository: 'packer-plugin-amazon' }, version_count: 2, update_available, update_check },
    detail: { name: 'amazon', source: { kind: 'releases-hashicorp', repository: 'packer-plugin-amazon' }, versions: amazonVersions },
  }))
  const failed = view({ enabled: true, checked_at: '2026-10-09T08:00:00Z', latest: '1.8.3', error: 'releases.hashicorp.com: dial tcp: no such host' }, true)
  assert.match(failed, /Update available · 1\.8\.3/)
  assert.match(failed, />Last check failed</)
  assert.match(failed, /Check daily for a newer stable version/)
  assert.match(failed, /\(failed\)/)
  assert.match(failed, /no such host The update pill reflects the last successful check\./)
  assert.match(failed, /releases\.hashicorp\.com · github\.com\/hashicorp\/packer-plugin-amazon/)
  assert.match(failed, />Show older upstream versions</)
  const off = view({ enabled: false }, false)
  assert.doesNotMatch(off, /Update available/)
  assert.doesNotMatch(off, />Last check failed</)
  assert.match(off, />Checks are off</)
  assert.match(off, /<dd[^>]*>[\s\S]{0,80}None</)
})

test('pending items are worded per change as the design lists them', () => {
  assert.deepEqual(pendingItems([
    { version: '1.8.3', action: 'add', platforms: ['linux_amd64', 'linux_arm64'] },
    { version: '1.8.2', action: 'add', platforms: ['darwin_arm64'] },
    { version: '1.8.1', action: 'restore' },
    { version: '1.8.0', action: 'revoke' },
  ], new Set(['1.8.2', '1.8.1', '1.8.0'])), [
    { verb: 'Add', what: '1.8.3', detail: '2 architectures: linux_amd64, linux_arm64', version: '1.8.3' },
    { verb: 'Add', what: 'darwin_arm64', detail: 'to 1.8.2', version: '1.8.2' },
    { verb: 'Restore', what: '1.8.1', detail: 'served again', version: '1.8.1' },
    { verb: 'Revoke', what: '1.8.0', detail: 'stays stored; Packer can no longer fetch it', version: '1.8.0' },
  ])
})
