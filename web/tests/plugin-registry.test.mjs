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
let PluginCatalogue
let PluginRegistrySettingsView
let pruneLabels
let pruneSavedMessage
let PluginBundleCard
let bundleLabels
let importLabels
let bundleImportSummary
let cronProblem
let prunePreview
let describeCron
let PluginErrorCard
let TypedConfirmModalView
let disablePluginRegistry
let enablePluginRegistry
let exposePluginRegistry
let getPluginDownloads
let getPluginRegistry
let pluginRegistryErrorMessage
let unexposePluginRegistry
let planPluginUploads
let templateStanza
let PluginDetailView
let PluginDownloadsCardView
let JobNotice
let PluginJobsFacet
let PluginChecksFacet
let checkState
let ImportRefusalAlert
let jobProgress
let importSelectionKey
let clearConfirmation
let clearOutcomeAlert
let parseJobsFilter
let pendingFirstImports
let catalogueJobLabel
let jobWhat
let activeJobFor
let listPluginImports
let versionConfirmation
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
let compareVersionsDesc
let groupPlatforms
let PLUGIN_ARCHITECTURES
let PLUGIN_OS_GROUPS
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
    PluginRegistryView, PluginCatalogue, CatalogueSyncConfirmationView,
    pluginRegistryErrorMessage, pendingFirstImports, catalogueJobLabel, parseJobsFilter,
  } = await vite.ssrLoadModule('/src/screens/Plugins.tsx'))
  ;({ PluginJobsFacet, ImportRefusalAlert, jobProgress, importSelectionKey, clearConfirmation, clearOutcomeAlert } = await vite.ssrLoadModule('/src/screens/PluginJobs.tsx'))
  ;({ PluginChecksFacet, checkState } = await vite.ssrLoadModule('/src/screens/PluginChecks.tsx'))
  ;({ TypedConfirmModalView } =
    await vite.ssrLoadModule('/src/components/TypedConfirmModal.tsx'))
  ;({ PluginErrorCard } = await vite.ssrLoadModule('/src/components/PluginLoadState.tsx'))
  ;({
    disablePluginRegistry, enablePluginRegistry, exposePluginRegistry, getPluginDownloads, getPluginRegistry,
    unexposePluginRegistry, planPluginUploads, templateStanza, listPlugins, publishPluginVersion,
    deletePluginVersion, pluginChanges, syncPlugin, syncCatalogue, setPluginUpdateCheck, planCatalogueSync, catalogueRows, pendingItems, retryPluginImport, githubRateLimit, compareVersionsDesc, groupPlatforms, PLUGIN_ARCHITECTURES, PLUGIN_OS_GROUPS, createPluginImport, createGithubImport, jobWhat, activeJobFor, listPluginImports,
  } = await vite.ssrLoadModule('/src/data/pluginRegistry.ts'))
  ;({ PluginDetailView, PluginDownloadsCardView, versionConfirmation, JobNotice } = await vite.ssrLoadModule('/src/screens/PluginDetail.tsx'))
  ;({ PluginUploadView } = await vite.ssrLoadModule('/src/screens/PluginUpload.tsx'))
  ;({ PluginHashicorpView } = await vite.ssrLoadModule('/src/screens/PluginHashicorp.tsx'))
  ;({ PluginImportJobView } = await vite.ssrLoadModule('/src/screens/PluginImportJob.tsx'))
  ;({ PluginGithubView } = await vite.ssrLoadModule('/src/screens/PluginGithub.tsx'))
  ;({ PluginRegistrySettingsView, pruneLabels, pruneSavedMessage, PluginBundleCard, bundleLabels, importLabels } =
    await vite.ssrLoadModule('/src/screens/PluginRegistrySettings.tsx'))
  ;({ cronProblem, prunePreview, describeCron, bundleImportSummary } = await vite.ssrLoadModule('/src/data/pluginRegistry.ts'))
})

after(async () => { await vite.close() })

const props = (over = {}) => ({
  organizationName: 'acme', callerRole: 'maintainer', host: 'dufflebag.example.com',
  registry: { enabled: false, exposed: false }, plugins: [], loading: false, failure: null,
  onBackToRegistry: () => {},
  onOpenPlugin: () => {}, onUpload: () => {}, onBrowse: () => {}, onImportGithub: () => {},
  onOpenImport: () => {}, onPlanSync: async () => [], onSyncSelected: async () => [],
  onRefresh: async () => {}, onEnable: async () => {},
  unfinished: [], facet: 'plugins', onFacet: () => {}, jobs: null, jobsFilter: {}, jobsPage: 1, jobsFailure: null, jobsBusy: false,
  onJobsFilter: () => {}, onJobsPage: () => {}, onRetryJob: () => {}, clearOutcome: null, onDismissClearOutcome: () => {}, onMatchingJobs: async () => [], onClearJobs: () => {},
  onToggleCheck: () => {}, ...over,
})

const render = (over = {}) => renderToStaticMarkup(
  React.createElement(PluginRegistryView, props(over)),
)
// The catalogue on its own. A PatternFly table rendered inside the facet
// rail costs seconds per row in this SSR harness (measured 2026-10-12:
// 108ms alone, 5s inside Tabs, 250s inside the full screen), so the rail is
// asserted once with an empty catalogue and the rows are asserted here.
const renderCatalogue = (over = {}) => renderToStaticMarkup(React.createElement(PluginCatalogue, {
  organizationName: 'acme', plugins: catalogue, unfinished: [], canPublish: true,
  onOpenPlugin: () => {}, onUpload: () => {}, onBrowse: () => {}, onImportGithub: () => {}, onOpenImport: () => {}, onPlanSync: async () => [], onSyncSelected: async () => [],
  ...over,
}))

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
      name: 'job', crumbs: ['Registry', 'Plugins', 'Jobs', 'Job 01KZF3QW'], loading: 'Loading job…', error: 'Job could not be loaded',
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
        ...callbacks, organizationName: 'acme', registry: null, plan: null, held: {}, outcomes: {}, busy: false, loading, failure,
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
  assert.match(enabled, /href="\/plugin-registry\/settings"[\s\S]{0,300}?Plugin registry</)
  assert.match(enabled, /No plugins mirrored yet/)
  assert.match(enabled, /Upload plugin files/)
  assert.doesNotMatch(enabled, />Expose</)
  assert.doesNotMatch(enabled, /Disable registry/)

  const exposed = render({ registry: { enabled: true, exposed: true } })
  assert.doesNotMatch(exposed, /The registry is exposed/)
  assert.doesNotMatch(exposed, /Plugin registry/)

  const reader = render({
    callerRole: 'reader', registry: { enabled: true, exposed: false },
  })
  assert.match(reader, /Plugin registry/)
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
  assert.deepEqual(plan.refused.map(({ label, reason, files }) => ({ label, reason, files: files.map((f) => f.name) })), [
    { label: 'README.md', reason: 'not a SHA256SUMS file, signature, manifest or plugin zip', files: ['README.md'] },
    { label: 'git 0.6.3', reason: 'no SHA256SUMS file', files: ['packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip'] },
    { label: 'git 0.6.2', reason: 'no plugin zip', files: ['packer-plugin-git_v0.6.2_SHA256SUMS'] },
  ])
})

test('the catalogue lists plugins with their source, newest version and count', () => {
  const html = renderCatalogue({
    plugins: [{ name: 'git', source: { kind: 'upload' }, published_versions: 2, newest_version: '0.6.3', update_available: false, update_check: { enabled: false } }],
  })
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
  assert.match(html, /href="\/plugin-registry\/settings"[\s\S]{0,300}?Plugin registry</)
  assert.ok(html.indexOf('Plugin registry') < html.indexOf('Template stanza'))
  assert.match(html, /title="darwin_arm64: Upstream"/)
  assert.match(html, /title="linux_arm64: Mirrored"/)
  assert.match(html, /title="windows_386: Not published"/)
  assert.match(html, /1 of 2</, 'coverage counts stored platforms against published ones')
  assert.match(html, /Upload version/)
  assert.doesNotMatch(detail({ enabled: true, exposed: true }), /until the registry is exposed/)
})

test('the downloads card shows all-version and selected-version windows, including removed versions', () => {
  // Fixture shape: spec/platform/openapi.yaml PluginDownloads and its two referenced schemas.
  const downloads = {
    today: '2026-10-13',
    all_versions: { last_7_days: 1234, last_30_days: 2345, last_365_days: 3456, all_time: 4567 },
    versions: [
      { version: '1.10.0', state: 'available', last_7_days: 101, last_30_days: 202, last_365_days: 303, all_time: 404 },
      { version: '1.9.0', state: 'revoked', last_7_days: 11, last_30_days: 22, last_365_days: 33, all_time: 44 },
      { version: '1.8.0', state: 'removed', last_7_days: 1, last_30_days: 2, last_365_days: 3, all_time: 4 },
    ],
  }
  const view = (selectedVersion) => renderToStaticMarkup(React.createElement(PluginDownloadsCardView, {
    downloads, selectedVersion, onSelect: () => {},
  }))
  const all = view(null)
  assert.match(all, />All versions</)
  assert.match(all, /Last 7 days[\s\S]*?1,234[\s\S]*?Last 30 days[\s\S]*?2,345[\s\S]*?Last 365 days[\s\S]*?3,456[\s\S]*?All time[\s\S]*?4,567/)

  const selected = view('1.10.0')
  assert.match(selected, />1\.10\.0</)
  assert.match(selected, /Last 7 days[\s\S]*?101[\s\S]*?Last 30 days[\s\S]*?202[\s\S]*?Last 365 days[\s\S]*?303[\s\S]*?All time[\s\S]*?404/)
  const removed = view('1.8.0')
  assert.match(removed, />1\.8\.0 \(removed\)</)
  assert.match(removed, /Last 7 days[\s\S]*?>1<[\s\S]*?Last 30 days[\s\S]*?>2<[\s\S]*?Last 365 days[\s\S]*?>3<[\s\S]*?All time[\s\S]*?>4</)
})

test('the plugin detail cards run Template stanza, Downloads, Update check in one row above Versions', () => {
  const html = renderToStaticMarkup(React.createElement(PluginDetailView, {
    name: 'git', organizationName: 'acme', host: 'dufflebag.example.com', callerRole: 'reader',
    registry: { enabled: true, exposed: true }, loading: false, failure: null, onRefresh: () => {}, onUpload: () => {},
    busy: false, actionFailure: null, upstream: [], summary: null, onOlderUpstream: () => {}, hasOlderUpstream: false,
    onSync: () => {}, onRevoke: () => {}, onRestore: () => {}, onRemove: () => {}, onBackToRegistry: () => {}, onBackToPlugins: () => {}, onToggleUpdates: () => {},
    downloads: { today: '2026-10-13', all_versions: { last_7_days: 0, last_30_days: 0, last_365_days: 0, all_time: 0 }, versions: [] },
    detail: { name: 'git', source: { kind: 'upload' }, versions: [{
      version: '0.6.3', revoked: false, created_at: '2026-10-09T00:00:00Z', listed_platforms: [], stored_platforms: [],
    }] },
  }))
  assert.ok(html.indexOf('Template stanza') < html.indexOf('Downloads'))
  assert.ok(html.indexOf('Downloads') < html.indexOf('Update check'))
  assert.ok(html.indexOf('Update check') < html.indexOf('>Versions<'))
  assert.equal(html.match(/pf-v6-l-grid__item/g)?.length, 3, 'the three cards share one grid row')
  assert.match(html, /Last 7 days[\s\S]*?>0</)
})

test('getPluginDownloads reads the per-plugin downloads path', async () => {
  const originalFetch = globalThis.fetch
  let sent
  globalThis.fetch = async (path) => {
    sent = path
    return new Response(JSON.stringify({ today: '2026-10-13', all_versions: {}, versions: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    await getPluginDownloads('token', 'organization id', 'git plugin')
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.equal(sent, '/api/v1/organizations/organization%20id/plugin-registry/plugins/git%20plugin/downloads')
})

const uploadView = (props) => renderToStaticMarkup(React.createElement(PluginUploadView, {
  organizationName: 'acme', registry: { enabled: true, exposed: false }, plan: null, held: {}, outcomes: {}, busy: false, loading: false, failure: null,
  onBackToRegistry: () => {}, onBackToPlugins: () => {}, onRefresh: () => {}, onChoose: () => {}, onOpen: () => {}, onSubmit: () => {},
  ...props,
}))

test('the upload view groups the files by version with each file\'s standing, then the outcome and stanza', () => {
  const plan = planPluginUploads([
    file('packer-plugin-git_v0.6.3_SHA256SUMS', 900), file('packer-plugin-git_v0.6.3_x5.0_linux_arm64.zip', 6259751),
    file('packer-plugin-git_v0.6.2_SHA256SUMS', 900), file('packer-plugin-git_v0.6.2_x5.0_linux_arm64.zip', 6100000),
    file('packer-plugin-git_v0.6.2_x5.0_darwin_arm64.zip', 6000000),
    file('README.md', 3000),
  ])
  const held = { git: { name: 'git', source: { kind: 'upload' }, versions: [
    { version: '0.6.2', revoked: false, created_at: '2026-10-01T00:00:00Z', listed_platforms: [], stored_platforms: [{ os: 'linux', arch: 'arm64' }] },
  ] } }
  const ready = uploadView({ plan, held })
  assert.match(ready, /Drag plugin files here/)
  assert.match(ready, /git · 2 versions/)
  assert.match(ready, /grouped from 6 files; nothing has been sent yet/)
  assert.match(ready, /<code>0\.6\.3<\/code>[\s\S]{0,400}?>New version<[\s\S]{0,300}?2 files/)
  assert.match(ready, /<code>0\.6\.2<\/code>[\s\S]{0,400}?>Available<[\s\S]{0,300}?mirrored · architectures are added, never replaced/)
  assert.match(ready, /linux_arm64\.zip<\/code><\/td><td[^>]*>linux_arm64<\/td><td[^>]*>6\.3 MB<\/td><td[^>]*>New</)
  assert.match(ready, /0\.6\.2_x5\.0_linux_arm64\.zip<\/code><\/td><td[^>]*>linux_arm64<\/td><td[^>]*>6\.1 MB<\/td><td[^>]*>Already mirrored</)
  assert.match(ready, /darwin_arm64\.zip<\/code><\/td><td[^>]*>darwin_arm64<\/td><td[^>]*>6\.0 MB<\/td><td[^>]*>New architecture</)
  assert.match(ready, /Not sent[\s\S]{0,300}?>1 file</)
  assert.match(ready, /<code>README\.md<\/code>[\s\S]{0,300}?not a SHA256SUMS file[\s\S]{0,300}?3 KB<\/td><td[^>]*>Skipped</)
  assert.match(ready, />Upload 5 files · 2 versions</)
  assert.match(ready, />Cancel</)

  const done = uploadView({ plan, held, outcomes: {
    'git 0.6.3': { status: 'published', stanza: { source: 'dufflebag.example.com/plugins/acme/git', version: '0.6.3', hcl: 'source  = "dufflebag.example.com/plugins/acme/git"' } },
    'git 0.6.2': { status: 'refused', message: 'git 0.6.2 already exists; versions are immutable' },
  } })
  assert.match(done, />Imported</)
  assert.match(done, />Refused</)
  assert.match(done, /versions are immutable/)
  assert.match(done, /5 sent/)
  assert.match(done, /Open git/)
  assert.match(done, /Packer can’t resolve the template stanza on this page until the registry is exposed/)
  assert.doesNotMatch(done, /Upload 5 files/)

  const disabled = uploadView({ registry: { enabled: false, exposed: false } })
  assert.match(disabled, /The plugin registry isn’t enabled/)
  assert.match(disabled, /href="\/plugin-registry\/settings"/)
})

test('an upload under a name held from another source is refused before anything is sent', () => {
  const plan = planPluginUploads([file('packer-plugin-amazon_v1.8.3-acme_SHA256SUMS', 900), file('packer-plugin-amazon_v1.8.3-acme_x5.0_linux_amd64.zip', 41200000)])
  const html = uploadView({ plan, held: { amazon: { name: 'amazon', source: { kind: 'releases-hashicorp', repository: 'packer-plugin-amazon' }, versions: [] } } })
  assert.match(html, /amazon is held from HashiCorp/)
  assert.match(html, /In acme, amazon comes from HashiCorp \(packer-plugin-amazon\)\. A name belongs to one source per organization, so these uploads are refused\. The name is freed once every amazon version is removed; revoked versions still count\./)
  assert.match(html, />Refused<[\s\S]{0,300}?name held from HashiCorp/)
  assert.match(html, /41\.2 MB<\/td><td[^>]*><span[^>]*>Not sent</)
  assert.match(html, /<button[^>]*disabled[^>]*>[\s\S]{0,200}?Upload 2 files · 1 version/)
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

test('revoke, restore and remove confirm in one dialog shape; only remove is destructive', () => {
  const row = { version: '0.6.3', stored: new Set(['linux_amd64', 'darwin_arm64']) }
  assert.deepEqual(versionConfirmation('revoke', 'git', row, false), {
    title: 'Revoke git 0.6.3?',
    body: 'Packer gets 404 for each of its files, so templates pinned to 0.6.3 fail packer init until it is restored. The 2 stored binaries (linux_amd64, darwin_arm64) are kept. To delete them, remove the version instead.',
    verb: 'Revoke 0.6.3', danger: false,
  })
  assert.deepEqual(versionConfirmation('restore', 'git', row, false), {
    title: 'Restore git 0.6.3?', body: 'Packer resolves 0.6.3 again from its 2 stored binaries (linux_amd64, darwin_arm64).', verb: 'Restore 0.6.3', danger: false,
  })
  assert.match(versionConfirmation('revoke', 'git', { version: '0.6.3', stored: new Set(['linux_amd64']) }, false).body, /The 1 stored binary \(linux_amd64\) is kept\. To delete it, remove the version instead\.$/)
  const remove = versionConfirmation('remove', 'git', { version: '0.6.3', stored: new Set(['linux_amd64']) }, true)
  assert.equal(remove.title, 'Remove git 0.6.3?')
  assert.equal(remove.verb, 'Remove 0.6.3')
  assert.equal(remove.danger, true)
  assert.match(remove.body, /This deletes 1 stored binary \(linux_amd64\)\. Templates pinned to 0\.6\.3 will fail packer init\. This can’t be undone\./)
  assert.match(remove.body, /It is git's last version, so git is removed and its name can be used by another source\.$/)
  assert.doesNotMatch(versionConfirmation('remove', 'git', row, false).body, /last version/)
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
  assert.match(served, />Revoke</, 'a served version offers Revoke in its row')
  assert.match(served, /No pending changes/)
  assert.match(served, /aria-label="Kebab toggle"/, 'the row menu carries Remove version')
  assert.match(served, />Available</)
  assert.match(served, /Not available for uploaded plugins\. To add a version, upload its files\./)
  assert.match(served, />Upload version</)
  assert.doesNotMatch(served, /Check daily for a newer stable version/)
  const revoked = view('publisher', true)
  assert.match(revoked, />Revoked</)
  assert.match(revoked, />Restore</)
  assert.match(revoked, /No version is available: every version is revoked\./)
  const reader = view('reader', false)
  assert.doesNotMatch(reader, />Revoke</)
  assert.doesNotMatch(reader, /Pending/)
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

test('the versions list shows held versions with a coverage strip, names newer upstream releases, and expands to its platforms', () => {
  const props = {
    name: 'amazon', organizationName: 'acme', host: 'dufflebag.example.com', callerRole: 'publisher',
    registry: { enabled: true, exposed: true }, loading: false, failure: null, onRefresh: () => {}, onUpload: () => {},
    busy: false, actionFailure: null, summary: null,
    upstream: [...amazonUpstream, { version: '1.7.9', created_at: '2026-05-01T00:00:00Z', prerelease: false, platforms: ['linux_amd64'], mirrored: false }],
    onOlderUpstream: () => {}, hasOlderUpstream: false, onSync: () => {}, onRemove: () => {}, onToggleUpdates: () => {},
    // Oldest first on the wire, so the newest-first order on screen is the sort's doing.
    detail: { name: 'amazon', source: { kind: 'releases-hashicorp', repository: 'packer-plugin-amazon' }, versions: [...amazonVersions].reverse() },
  }
  const html = renderToStaticMarkup(React.createElement(PluginDetailView, props))
  // Mirrored scope by default: the held versions, newest first, the newer upstream release in the banner, the older one not listed.
  assert.match(html, />Mirrored · 2</)
  assert.match(html, />All · 4</)
  assert.match(html, /Filter by version/)
  assert.match(html, /1 newer upstream version/)
  assert.match(html, /Add 1\.8\.3 with 1 platforms/, 'the newest stable upstream is offered with the platforms the plugin already has')
  assert.match(html, />Show all versions</)
  assert.match(html, /<code>1\.8\.2<\/code>[\s\S]*<code>1\.8\.1<\/code>/)
  assert.doesNotMatch(html, /<code>1\.8\.3<\/code>/)
  assert.doesNotMatch(html, /<code>1\.7\.9<\/code>/)
  assert.match(html, /<th[^>]*>Version<\/th><th[^>]*>Status<\/th><th[^>]*>Platforms<\/th><th[^>]*>Released<\/th>/)
  assert.match(html, /aria-label="Platform coverage"/)
  assert.match(html, /title="linux_amd64: Mirrored"/)
  assert.match(html, /title="linux_arm64: Upstream"/)
  assert.match(html, /title="darwin_amd64: Not published"/)
  assert.match(html, /1 of 3</)
  assert.match(html, />Revoke</)
  assert.match(html, />Restore</)
  assert.match(html, /Mirrored<[\s\S]{0,400}?Revoked<[\s\S]{0,400}?Will be added<[\s\S]{0,400}?Upstream<[\s\S]{0,400}?Not published</, 'the strip legend')
  assert.match(html, /No pending changes/)
  assert.match(html, /<button[^>]*disabled[^>]*>[\s\S]{0,200}?Sync</)
  // Expanded rows carry the per-platform controls: only unmirrored platforms of a served version are checkboxes.
  assert.match(html, /aria-label="Add linux_arm64 to 1\.8\.2"/)
  assert.match(html, /aria-label="Add darwin_arm64 to 1\.8\.2"/)
  assert.doesNotMatch(html, /aria-label="Add linux_amd64 to 1\.8\.2"/, 'a mirrored platform is a status, not a control')
  assert.doesNotMatch(html, /aria-label="Add linux_arm64 to 1\.8\.1"/, 'a revoked version takes no platforms until restored')
  assert.match(html, /title="Restore the version first"/)
  assert.match(html, /<strong>linux<\/strong> 1 of 2/)
  assert.match(html, /<strong>windows<\/strong> not published/)
  const reader = renderToStaticMarkup(React.createElement(PluginDetailView, { ...props, callerRole: 'reader' }))
  assert.doesNotMatch(reader, /aria-label="Add /)
  assert.doesNotMatch(reader, />Add 1\.8\.3 with/)
})

test('pending changes add platforms and mirror new versions; revoke and restore are not changes', () => {
  assert.deepEqual(pluginChanges(amazonVersions, amazonUpstream.filter((v) => !v.mirrored), {}), [])
  assert.deepEqual(pluginChanges(amazonVersions, amazonUpstream.filter((v) => !v.mirrored), {
    '1.8.3': ['linux_amd64'], '1.8.2': ['linux_amd64', 'darwin_arm64'],
  }), [
    { version: '1.8.3', action: 'add', platforms: ['linux_amd64'] },
    { version: '1.8.2', action: 'add', platforms: ['darwin_arm64'] },
  ], 'a platform the version already has is not added again')
  assert.deepEqual(pluginChanges(amazonVersions, [], { '1.8.1': ['linux_arm64'] }), [], 'a revoked version takes no platforms')
  assert.deepEqual(pluginChanges(amazonVersions, amazonUpstream.filter((v) => !v.mirrored), { '1.8.3': [] }), [], 'a new version with no platforms is not a change')
})

test('a sync posts the ordered changes to the plugin', async () => {
  const originalFetch = globalThis.fetch
  let sent
  globalThis.fetch = async (path, options) => {
    sent = { path, body: JSON.parse(options.body) }
    return new Response(JSON.stringify({ id: 'job' }), { status: 202, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    await syncPlugin('token', 'org', 'amazon', [{ version: '1.8.3', action: 'add', platforms: ['linux_amd64'] }])
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.equal(sent.path, '/api/v1/organizations/org/plugin-registry/plugins/amazon/sync')
  assert.deepEqual(sent.body, { changes: [{ version: '1.8.3', action: 'add', platforms: ['linux_amd64'] }] })
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
      organizationName: 'acme', host: 'dufflebag.example.com', callerRole, registry,
      defaultPlatforms: ['linux_amd64', 'linux_arm64', 'darwin_arm64'],
      loading: false, failure: null,
      onRefresh: () => {}, onEnable: async () => {}, onExpose: async () => {},
      onUnexpose: async () => {}, onDisable: async () => {}, onSetDefaultPlatforms: async () => {},
    },
  ))
  const unexposed = settings('maintainer', { enabled: true, exposed: false })
  assert.match(unexposed, />Plugin registry<[\s\S]*?>Registry<[\s\S]*?>Enabled<[\s\S]*?>Not exposed</, 'the administration screen leads with its status')
  assert.match(unexposed, />Default platforms</)
  assert.match(unexposed, />Expose</)
  assert.match(unexposed, /Disable registry/)
  // The defaults are ticked from the published list, grouped by OS; nothing is typed.
  for (const platform of ['linux_amd64', 'linux_arm64', 'darwin_arm64']) assert.match(unexposed, new RegExp(`id="default-platform-${platform}"[^>]*checked`), platform)
  assert.match(unexposed, /id="default-platform-windows_amd64"(?![^>]*checked)/)
  assert.doesNotMatch(unexposed, /aria-label="Default platforms"/)
  assert.match(unexposed, /<button[^>]*disabled[^>]*>[\s\S]{0,200}?Save default platforms/, 'nothing changed, nothing to save')

  const exposed = settings('maintainer', { enabled: true, exposed: true })
  // Exposed, the screen says where Packer resolves this organization's plugins from, with a vanilla stanza.
  assert.match(exposed, />Packer<[\s\S]{0,600}?dufflebag\.example\.com\/plugins\/acme</)
  assert.match(exposed, /source  = &quot;dufflebag\.example\.com\/plugins\/acme\/NAME&quot;/)
  assert.match(exposed, /version = &quot;&gt;= 1\.0\.0&quot;/)
  assert.doesNotMatch(unexposed, /plugins\/acme\/NAME/, 'nothing to configure until the registry is exposed')
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
  // The plugins are a left-hand rail like a bucket's facets: the name, a mirrored-versions badge, held marked.
  assert.match(html, /<nav[^>]*aria-label="HashiCorp plugins"[^>]*>[\s\S]{0,300}?Plugins · 3 published/)
  assert.match(html, />amazon<[\s\S]{0,300}?pf-v6-c-badge[^>]*>1</)
  assert.match(html, />docker · held</)
  assert.match(html, />vsphere</)
  assert.match(html, /id="hashicorp-releases-toolbar"/)
  assert.match(html, /aria-label="Select importable releases"/)
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
  assert.match(html, /id="select-1"[^>]*disabled/, 'a mirrored version cannot be chosen again')
  assert.doesNotMatch(html, /id="select-0"[^>]*disabled/)
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
  assert.match(held, /id="select-0"[^>]*disabled/)
  assert.doesNotMatch(held, /Platforms to import/)
  const first = hashicorpView({ selected: hashicorpPlugins[2], versions: [amazonReleases[0]], heldVersions: [], preselected: ['linux_amd64', 'darwin_arm64'] })
  assert.match(first, /Preselected: acme’s default platforms \(Administration, Plugin registry\), since this is a first import\./)
  assert.match(first, />org default</)
  assert.match(first, /id="platform-darwin_arm64"[^>]*checked/)
})

const jobViewProps = {
  id: 'job', registry: { enabled: true, exposed: true }, loading: false, failure: null, organizationName: 'acme', host: 'dufflebag.example.com',
  callerRole: 'publisher', busy: false, actionFailure: null,
  onBackToRegistry: () => {}, onBackToPlugins: () => {}, onJobs: () => {}, onRefresh: () => {}, onOpen: () => {}, onRetry: () => {},
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
  assert.match(html, /href="\/plugin-registry\/settings"[\s\S]{0,300}?Plugin registry</)
  assert.ok(html.indexOf('Plugin registry') < html.indexOf('Import outcomes'))
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
    ], retry_of: sync.id }, 'a partially failed add keeps only its failed platforms; a succeeded restore is not repeated; the retry names its job')
    assert.match(posted.at(-1).path, /plugins\/amazon\/sync$/)
    await retryPluginImport('token', 'org', importJob('partially_succeeded', [
      { version: '1.8.3', outcome: 'imported' }, { version: '1.8.2', outcome: 'failed', error: 'x' }, { version: '1.8.1', outcome: 'failed', error: 'y' },
    ]))
    assert.deepEqual(posted.at(-1).body, { source: 'releases-hashicorp', product: 'packer-plugin-amazon', versions: ['1.8.2', '1.8.1'], platforms: ['linux_amd64', 'windows_386'], retry_of: '01KZF3QW7N00000000000000000' })
    await retryPluginImport('token', 'org', importJob('failed', [{ version: 'v0.6.4', outcome: 'failed', error: 'x' }], { source: 'github', product: 'owner/packer-plugin-git', versions: ['v0.6.4'] }))
    assert.deepEqual(posted.at(-1).body, { source: 'github', product: 'owner/packer-plugin-git', versions: ['v0.6.4'], platforms: ['linux_amd64', 'windows_386'], retry_of: '01KZF3QW7N00000000000000000' })
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

test('a resolved GitHub release shows its details, pinned to its tag', () => {
  const html = githubView(gitRelease)
  assert.match(html, /Release link/)
  assert.match(html, /Latest is pinned to an exact version when you resolve it\./)
  assert.match(html, /Release details/)
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
  // Header carries the title and the three ways in, and no subtitle (design 1.0).
  const screen = render({ registry: { enabled: true, exposed: true }, callerRole: 'publisher', plugins: [] })
  assert.doesNotMatch(screen, /packer init resolves them from/)
  for (const action of ['Browse HashiCorp', 'Import from GitHub', 'Upload']) assert.match(screen, new RegExp(`<button[^>]*>(?:<span[^>]*>)?${action}<`))
  assert.doesNotMatch(render({ registry: { enabled: true, exposed: true }, callerRole: 'reader', plugins: [] }), /Browse HashiCorp/)
  const html = renderCatalogue()
  // Toolbar: name filter, source filter, Update available, Sync selected, pager. The selection count appears only once something is selected.
  assert.match(html, /aria-label="Filter plugins by name"/)
  assert.match(html, />All sources</)
  assert.match(html, /id="updates-only"[^>]*>/)
  assert.doesNotMatch(html, /Select plugins with an update<\/span>|0 selected/)
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
  // The house selectable-table cells: a select-all header over the rows with an update, PF's row checkboxes.
  assert.match(html, /<div class="pf-v6-c-card__title-text">Plugins<\/div>[\s\S]{0,600}?id="plugins-toolbar"/, 'the catalogue card carries a title and a toolbar like the other list screens')
  assert.match(html, /aria-label="Select plugins with an update"/)
  const box = (row) => html.match(new RegExp(`<input[^>]*id="select-${row}"[^>]*>`))[0]
  assert.doesNotMatch(box(0), /disabled/, 'amazon has an update to sync')
  for (const row of [1, 2, 3]) assert.match(box(row), /disabled/, `row ${row} has no update to sync`)
  assert.equal((html.match(/title="Only plugins with an update available can be synced"/g) ?? []).length, 3)
  // No action buttons in the toolbar, no settings card, no exposed alert.
  assert.doesNotMatch(html, /Upload plugin files/)
  assert.doesNotMatch(html, /Save default platforms/)
  assert.doesNotMatch(html, /The registry is exposed/)

  const reader = renderCatalogue({ canPublish: false })
  assert.match(reader, /Update available · 1\.8\.3/)
  assert.doesNotMatch(reader, /Sync selected|aria-label="Kebab toggle"|Select amazon to sync/)
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
  assert.doesNotMatch(failed, /releases\.hashicorp\.com · github\.com/, 'the header carries no source subtitle')
  assert.doesNotMatch(failed, />Show older upstream versions</, 'older upstream paging belongs to the All scope')
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
  ], new Set(['1.8.2', '1.8.1', '1.8.0'])), [
    { text: 'Add 1.8.3 · 2 platforms', version: '1.8.3' },
    { text: 'Add darwin_arm64 to 1.8.2', version: '1.8.2' },
  ])
  assert.deepEqual(pendingItems([{ version: '1.8.2', action: 'add', platforms: ['darwin_arm64', 'linux_arm64'] }], new Set(['1.8.2'])),
    [{ text: 'Add 2 platforms to 1.8.2', version: '1.8.2' }])
})

test('versions order newest first, with a prerelease below its release', () => {
  const sorted = ['1.8.1', '1.9.0-beta.1', '1.10.0', '1.9.0', 'v1.8.2', '1.8.10'].sort(compareVersionsDesc)
  assert.deepEqual(sorted, ['1.10.0', '1.9.0', '1.9.0-beta.1', '1.8.10', 'v1.8.2', '1.8.1'])
})

test('the platform list is the generated snapshot, grouped by operating system in its order', () => {
  assert.ok(PLUGIN_ARCHITECTURES.length >= 18)
  assert.ok(PLUGIN_ARCHITECTURES.includes('linux_amd64'))
  assert.deepEqual([...PLUGIN_ARCHITECTURES], [...PLUGIN_ARCHITECTURES].sort(), 'the snapshot is sorted, so the groups are too')
  assert.deepEqual(groupPlatforms(['darwin_amd64', 'darwin_arm64', 'linux_386', 'linux_amd64']), [['darwin', ['amd64', 'arm64']], ['linux', ['386', 'amd64']]])
  assert.deepEqual(PLUGIN_OS_GROUPS.flatMap(([os, arches]) => arches.map((arch) => `${os}_${arch}`)), [...PLUGIN_ARCHITECTURES])
})

// Jobs are the producer's shape: the sync fixture comes from the handler; imports follow importJob().
const queuedImport = (plugin, extra = {}) => importJob('queued', [], { id: `01KZJ${plugin.toUpperCase()}000000000000000000`.slice(0, 26), product: `packer-plugin-${plugin}`, plugin, versions: ['1.0.0'], platforms: ['linux_amd64', 'linux_arm64', 'darwin_arm64'], queued_ahead: 1, ...extra })
const runningSync = (plugin) => ({
  ...JSON.parse(readFileSync(new URL('./fixtures/plugin-sync-job.json', import.meta.url), 'utf8')),
  id: '01KZG7WQ3K000000000000000', plugin, product: plugin, state: 'running', queued_ahead: 0, finished_at: undefined,
  outcomes: [{ version: '1.8.3', outcome: 'imported' }],
})

test('job wording: what a job does, where it is in the queue, and the catalogue label', () => {
  assert.equal(jobWhat(queuedImport('kubevirt')), '1.0.0 · 3 platforms')
  assert.equal(jobWhat(importJob('queued', [])), '1.8.3, 1.8.2, 1.8.1 · 2 platforms')
  assert.equal(jobWhat(runningSync('amazon')), '3 additions')
  assert.equal(jobWhat(importJob('succeeded', [], { origin: 'upload', versions: ['1.0.0', '1.1.0'] })), '2 versions')
  assert.deepEqual(jobProgress(queuedImport('kubevirt', { queued_ahead: 0 })), { text: 'Runs next' })
  assert.deepEqual(jobProgress(queuedImport('kubevirt', { queued_ahead: 2 })), { text: '2 jobs ahead' })
  assert.deepEqual(jobProgress(runningSync('amazon')), { text: '1 of 3 additions', percent: 33 })
  assert.equal(jobProgress(importJob('succeeded', [])), null)
  assert.equal(catalogueJobLabel(queuedImport('kubevirt', { queued_ahead: 2 })), 'Import queued · 2 ahead')
  assert.equal(catalogueJobLabel(runningSync('amazon')), 'Sync running · 1 of 3')
  // The list is newest first; the plugin's job that runs first is the oldest.
  const older = queuedImport('vsphere', { id: 'older' }), newer = queuedImport('vsphere', { id: 'newer' })
  assert.equal(activeJobFor([newer, older], 'vsphere').id, 'older')
  assert.equal(activeJobFor([newer, older], 'qemu'), undefined)
  assert.equal(importSelectionKey(['1.1.0', '1.0.0'], ['linux_arm64', 'linux_amd64']), '1.0.0,1.1.0|linux_amd64,linux_arm64')
})

test('the jobs list asks the API with the facet filters and paging', async () => {
  const originalFetch = globalThis.fetch
  const seen = []
  globalThis.fetch = async (path) => {
    seen.push(path)
    return new Response(JSON.stringify({ imports: [], total: 0 }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    await listPluginImports('token', 'org', { states: ['unfinished'], limit: 100 })
    await listPluginImports('token', 'org', { states: ['failed', 'queued'], plugins: ['amazon', 'qemu'], limit: 10, offset: 20 })
    await listPluginImports('token', 'org')
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.deepEqual(seen, [
    '/api/v1/organizations/org/plugin-registry/imports?state=unfinished&limit=100',
    '/api/v1/organizations/org/plugin-registry/imports?state=failed%2Cqueued&plugin=amazon%2Cqemu&limit=10&offset=20',
    '/api/v1/organizations/org/plugin-registry/imports',
  ])
})

const jobsFacet = (over = {}) => renderToStaticMarkup(React.createElement(PluginJobsFacet, {
  jobs: [], total: 0, filter: { states: [], plugins: [] }, plugins: ['amazon', 'kubevirt'], page: 1, loading: false, failure: null, canPublish: true, busy: false,
  outcome: null, onFilter: () => {}, onPage: () => {}, onOpenJob: () => {}, onRetry: () => {}, onRefresh: () => {}, onClear: () => {}, onMatching: async () => [], onDismissOutcome: () => {},
  ...over,
}))

test('the Jobs facet lists jobs newest first with state, progress, reason and Retry gated by role', () => {
  const failed = importJob('failed', [{ version: '1.8.3', outcome: 'failed', error: 'releases.hashicorp.com unreachable: dial tcp: i/o timeout' }], { id: '01KZFAILED000000000000000', finished_at: '2026-10-09T14:03:00Z' })
  const jobs = [queuedImport('kubevirt', { queued_ahead: 1 }), runningSync('amazon'), failed]
  const html = jobsFacet({ jobs, total: 3 })
  assert.match(html, /aria-label="Jobs"/)
  assert.match(html, />Queued<[\s\S]*?>Running<[\s\S]*?>Failed</, 'rows in the order given, newest first')
  assert.match(html, />Import kubevirt<[\s\S]{0,300}?1\.0\.0 · 3 platforms · Job 01KZJKUB/)
  assert.match(html, /1 job ahead/)
  assert.match(html, />Sync amazon<[\s\S]{0,300}?3 additions/)
  assert.match(html, /aria-label="Sync amazon progress"/)
  assert.match(html, /1 of 3 additions/)
  assert.match(html, /releases\.hashicorp\.com unreachable: dial tcp: i\/o timeout/, 'a failed row carries its first error')
  assert.match(html, /from Browse HashiCorp \(packer-plugin-kubevirt\)/)
  assert.equal((html.match(/>Retry</g) ?? []).length, 1, 'Retry on the failed job only')
  assert.doesNotMatch(jobsFacet({ jobs, total: 3, callerRole: 'reader', canPublish: false }), />Retry</)
  assert.match(html, /aria-label="Jobs pagination"/)
  assert.match(html, />State<[\s\S]*?>Plugin</, 'the filters are menus, named for what they filter')
  const filtered = jobsFacet({ jobs: [], total: 0, filter: { states: ['failed', 'queued'], plugins: ['qemu'] } })
  assert.match(filtered, /No jobs match\./)
  assert.match(filtered, />State · 2<[\s\S]*?>Plugin · 1</, 'the menus count their picks')
  assert.match(filtered, />Failed<[\s\S]*?>Queued<[\s\S]*?>qemu</, 'the active filters show as chips')
  assert.doesNotMatch(filtered, /No jobs yet/)
  assert.match(jobsFacet(), /No jobs yet/)
  assert.match(jobsFacet({ loading: true }), /Loading jobs…/)
  assert.match(jobsFacet({ failure: 'GET …/imports: 502' }), /Jobs could not be loaded[\s\S]*502/)
})

test('the catalogue shows a plugin\'s unfinished job and a first import\'s row, and the rail counts unfinished jobs', () => {
  const unfinished = [queuedImport('kubevirt', { queued_ahead: 1 }), runningSync('amazon')]
  // The rail with an empty catalogue: a sync's job adds no row, so the badge is asserted without a table in the rail (see renderCatalogue).
  const rail = render({ registry: { enabled: true, exposed: true }, callerRole: 'publisher', plugins: [], unfinished: [runningSync('amazon'), runningSync('qemu')] })
  assert.match(rail, /aria-label="Plugin facets"/)
  assert.match(rail, />Plugins<[\s\S]{0,200}?>0<[\s\S]{0,400}?>Jobs<[\s\S]{0,200}?<span class="pf-v6-c-badge pf-m-unread">2</, 'Plugins counts held plugins; Jobs counts unfinished jobs, unread')
  assert.match(renderCatalogue({ plugins: [], unfinished: [queuedImport('kubevirt')] }), /kubevirt[\s\S]{0,600}?>None yet</, 'a first import on an empty registry shows its row, not the empty state')
  const html = renderCatalogue({ unfinished })
  assert.match(html, />Sync running · 1 of 3</, 'amazon\'s running sync replaces its update pill')
  assert.doesNotMatch(html, /Update available · 1\.8\.3/)
  assert.match(html, /title="A job for amazon is already running"/)
  assert.match(html, /<span style="color:var\(--pf-t--global--text--color--subtle\)">kubevirt<\/span>/, 'a first import\'s name is not a link yet')
  assert.match(html, /kubevirt[\s\S]{0,600}?>None yet</)
  assert.match(html, />Import queued · 1 ahead</)
  assert.deepEqual(pendingFirstImports(catalogue, unfinished).map((plugin) => [plugin.name, plugin.source.kind, plugin.source.repository, plugin.published_versions]),
    [['kubevirt', 'releases-hashicorp', 'packer-plugin-kubevirt', 0]])
  assert.deepEqual(pendingFirstImports(catalogue, [runningSync('amazon'), queuedImport('amazon')]), [], 'a held plugin\'s jobs add no row')
  assert.doesNotMatch(render({ registry: { enabled: true, exposed: true }, callerRole: 'publisher', plugins: [] }), /pf-m-unread/, 'the Jobs badge hides at zero')
})

test('the plugin page notice follows its job and the header links to its jobs', () => {
  const view = (over) => renderToStaticMarkup(React.createElement(PluginDetailView, {
    name: 'amazon', organizationName: 'acme', host: 'dufflebag.example.com', callerRole: 'publisher',
    registry: { enabled: true, exposed: true }, loading: false, failure: null, onRefresh: () => {}, onUpload: () => {},
    busy: false, actionFailure: null, upstream: [], summary: null, onOlderUpstream: () => {}, hasOlderUpstream: false,
    onSync: () => {}, onRevoke: () => {}, onRestore: () => {}, onRemove: () => {}, onBackToRegistry: () => {}, onBackToPlugins: () => {}, onToggleUpdates: () => {},
    detail: { name: 'amazon', source: { kind: 'releases-hashicorp', repository: 'packer-plugin-amazon' }, versions: [
      { version: '1.8.2', revoked: false, created_at: '2026-10-01T00:00:00Z', listed_platforms: [], stored_platforms: [{ os: 'linux', arch: 'amd64' }] },
    ] },
    ...over,
  }))
  const running = view({ activeJob: runningSync('amazon') })
  assert.match(running, />Jobs for amazon</)
  assert.match(running, /Sync amazon is running/)
  assert.match(running, /Job 01KZG7WQ: 3 additions\. Versions below update as each one is stored\./)
  assert.match(running, /aria-label="Job progress"/)
  assert.match(running, />Open job<[\s\S]*?>All jobs for amazon</)
  const queued = view({ activeJob: queuedImport('amazon', { queued_ahead: 2 }) })
  assert.match(queued, /Import amazon is queued/)
  assert.match(queued, /2 jobs for acme run first; nothing below changes until it runs\./)
  const done = view({ finishedJob: { ...runningSync('amazon'), state: 'succeeded', finished_at: '2026-10-12T14:06:00Z' } })
  assert.match(done, /Sync amazon succeeded/)
  assert.match(done, /The versions below are up to date\./)
  const failed = renderToStaticMarkup(React.createElement(JobNotice, {
    job: importJob('failed', [{ version: '1.8.3', outcome: 'failed', error: 'no such host' }]), organizationName: 'acme', onOpenJob: () => {}, onJobs: () => {},
  }))
  assert.match(failed, /Import amazon failed/)
  assert.match(failed, /no such host Nothing below changed\./)
  assert.doesNotMatch(view({}), /Open job/, 'no notice without a job')
})

test('a duplicate import is refused inline, naming the job, until the selection changes', () => {
  const job = queuedImport('vsphere', { queued_ahead: 2, created_by: 'alice@acme' })
  const refusal = { job, message: 'an identical job for vsphere is already queued', selection: importSelectionKey(['1.0.0'], ['linux_amd64']) }
  const alert = renderToStaticMarkup(React.createElement(ImportRefusalAlert, { refusal, onOpenJob: () => {}, onJobs: () => {} }))
  assert.match(alert, /This import is already queued/)
  assert.match(alert, /Job 01KZJVSP imports 1\.0\.0 · 3 platforms\. It was queued by alice@acme at .+? and has 2 jobs ahead of it\. Starting another would only record “already mirrored”\./)
  assert.match(alert, />Open job 01KZJVSP<[\s\S]*?>Jobs</)
  const running = renderToStaticMarkup(React.createElement(ImportRefusalAlert, { refusal: { ...refusal, job: { ...job, state: 'running', started_at: '2026-10-12T14:12:00Z' } }, onOpenJob: () => {}, onJobs: () => {} }))
  assert.match(running, /This import is already running/)
  assert.match(running, /It started at .+? and is 0 of 1 version in\./)

  // Browse HashiCorp: the alert and a disabled Import while the refused selection stands; the rail and the row name the job.
  const vsphere = { product: 'packer-plugin-vsphere', name: 'vsphere', mirrored_versions: 0 }
  const releases = [{ version: '1.0.0', created_at: '2026-09-24T00:00:00Z', prerelease: false, platforms: ['linux_amd64', 'linux_arm64', 'darwin_arm64'], mirrored: false }]
  const withJob = hashicorpView({ plugins: [vsphere], selected: vsphere, versions: releases, heldVersions: [], hasMore: false, preselected: ['linux_amd64'], unfinished: [job], refusal: null })
  assert.match(withJob, /vsphere · queued/)
  assert.match(withJob, /In job 01KZJVSP \(queued\)/)
  // The view's selection starts as the preselected platforms with nothing chosen, so a refusal for that selection shows.
  const refusedNow = hashicorpView({ plugins: [vsphere], selected: vsphere, versions: releases, heldVersions: [], hasMore: false, preselected: ['linux_amd64'], unfinished: [job],
    refusal: { ...refusal, selection: importSelectionKey([], ['linux_amd64']) } })
  assert.match(refusedNow, /This import is already queued/)
  assert.doesNotMatch(hashicorpView({ plugins: [vsphere], selected: vsphere, versions: releases, heldVersions: [], hasMore: false, preselected: ['linux_amd64'], unfinished: [job], refusal }), /This import is already queued/, 'a refusal for another selection is not shown')

  // Import from GitHub: the same alert keyed on the release tag and platforms.
  const githubJob = { ...job, source: 'github', product: 'ethanmdavidson/packer-plugin-git', plugin: 'git', versions: ['v0.6.3'] }
  const refused = githubView(gitRelease, { refusal: { job: githubJob, message: 'identical', selection: importSelectionKey(['v0.6.3'], ['linux_amd64']) } })
  assert.match(refused, /This import is already queued/)
  assert.match(refused, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Import v0\.6\.3 · 1 platform</)
  assert.doesNotMatch(githubView(gitRelease, { refusal: { job: githubJob, message: 'identical', selection: importSelectionKey(['v0.6.3'], ['darwin_arm64']) } }), /This import is already queued/)
})

test('the Jobs facet selects clearable rows and clears them through one confirmation', () => {
  const queued = queuedImport('kubevirt', { queued_ahead: 1 })
  const running = runningSync('amazon')
  const failed = importJob('failed', [{ version: '1.8.3', outcome: 'failed', error: 'x' }], { id: '01KZFAILED000000000000000' })
  const html = jobsFacet({ jobs: [queued, running, failed], total: 3 })
  assert.match(html, /aria-label="Select jobs"/, 'the bulk-select toggle')
  assert.match(html, /<input id="select-0"[^>]*>/, 'the house row checkbox, PF\'s id per row')
  assert.match(html, /title="A running job can’t be cleared"/, 'the running row says why it cannot be ticked')
  assert.match(html, /<input id="select-1"[^>]*disabled/, 'a running row cannot be ticked')
  assert.match(html, /1 of 3 additions · can’t be cleared/, 'the running row note')
  assert.match(html, /Select jobs to clear/, 'the selection summary')
  assert.match(html, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Clear jobs</, 'Clear is disabled with nothing selected')
  const reader = jobsFacet({ jobs: [queued, running, failed], total: 3, canPublish: false })
  assert.doesNotMatch(reader, /id="select-|Clear jobs|aria-label="Select jobs"/)

  assert.deepEqual(clearConfirmation([queued, failed]), {
    title: 'Clear 2 jobs?',
    parts: [
      { head: '1 queued: it won’t run', danger: true, body: 'Import kubevirt (1.0.0 · 3 platforms). It is taken out of the queue and the jobs behind move up. Nothing is mirrored for it; queue the work again to get it.' },
      { head: '1 finished: record deleted', danger: false, body: 'Import amazon. Its outcomes and link stop working. Versions it mirrored are unchanged.' },
      { head: '', danger: false, body: 'This can’t be undone. If a queued job starts running before this goes through, it isn’t cleared.' },
    ],
    verb: 'Clear 2 jobs',
  })
  assert.equal(clearConfirmation([failed]).parts.length, 2, 'no queued part without a queued job')
  assert.deepEqual(clearOutcomeAlert({ cleared: [queued, failed], running: [], failed: [] }), {
    variant: 'success', title: '2 jobs cleared',
    body: '1 queued job was taken out of the queue and won’t run: Import kubevirt. 1 finished job record was deleted; the versions it mirrored are unchanged.',
  })
  const race = clearOutcomeAlert({ cleared: [failed], running: [queued], failed: [] })
  assert.equal(race.variant, 'warning')
  assert.equal(race.title, '1 of 2 jobs cleared')
  assert.match(race.body, /Import kubevirt started running before it could be cleared, so it will run to the end\. Running jobs can’t be cleared\./)
  assert.equal(clearOutcomeAlert({ cleared: [], running: [], failed: [{ job: failed, message: '503' }] }).title, 'Nothing was cleared')
  const shown = jobsFacet({ jobs: [running], total: 1, outcome: { cleared: [failed], running: [], failed: [] } })
  assert.match(shown, /1 job cleared/)
  assert.match(jobsFacet({ outcome: { cleared: [failed], running: [], failed: [] } }), /1 job cleared[\s\S]*No jobs yet/, 'the outcome shows above the empty state too')

  assert.deepEqual(parseJobsFilter(new URLSearchParams('state=failed,queued&plugin=amazon')), { states: ['failed', 'queued'], plugins: ['amazon'] })
  assert.deepEqual(parseJobsFilter(new URLSearchParams('facet=jobs')), { states: [], plugins: [] })
})

test('a cleared job page says so and leads back to the jobs', () => {
  const html = renderToStaticMarkup(React.createElement(PluginImportJobView, { ...jobViewProps, job: null, loading: false, cleared: true }))
  assert.match(html, /This job was cleared/)
  assert.match(html, /Its record was deleted, by hand or by pruning\. Any versions it mirrored are unchanged\./)
  assert.match(html, /<button[^>]*>(?:<span[^>]*>)?Jobs</)
  assert.doesNotMatch(html, /Job could not be loaded/)
})

const pruneState = (over = {}) => ({
  enabled: false, age_days: 30, schedule: '0 3 * * *', last_run_cleared: 0, last_error: '', would_clear: 6, unfinished: 3, kept: 27,
  oldest_finished_at: '2026-06-09T10:00:00Z', ...over,
})

test('the prune schedule is validated field by field and previewed honestly', () => {
  for (const good of ['0 3 * * *', '0 * * * *', '0 3 * * 0', '*/15 0-6 1,15 * 1-5']) assert.equal(cronProblem(good), null, good)
  assert.match(cronProblem('0 3 * *'), /5 fields .* this has 4/)
  assert.match(cronProblem('0 25 * * *'), /hour field must be 0 to 23/)
  assert.match(cronProblem('*/0 * * * *'), /minute field's step cannot be 0/)
  assert.match(cronProblem('a b c d e'), /minute field "a" is not a number, range, list or step/)
  assert.match(cronProblem('0 3 32 * *'), /day of month field must be 1 to 31/)
  const off = prunePreview(pruneState(), false, 30)
  assert.match(off, /^Nothing is pruned while this is off\. 27 jobs are kept, the oldest finished /)
  assert.match(prunePreview(pruneState(), true, 30), /^If it ran now, it would clear 6 jobs that finished before .*\. 3 jobs queued or running are never touched\.$/)
  assert.match(prunePreview(pruneState({ would_clear: 0, unfinished: 1 }), true, 30), /^If it ran now, it would clear nothing: no finished job is older than 30 days\. 1 job queued or running is never touched\.$/)
  assert.match(prunePreview(pruneState(), true, 7), /^Save to see what 7 days would clear now\./)
  assert.deepEqual(pruneLabels(pruneState()), [{ text: 'Off', color: 'grey' }])
  assert.deepEqual(pruneLabels(pruneState({ enabled: true })), [{ text: 'On', color: 'green' }])
  assert.deepEqual(pruneLabels(pruneState({ enabled: true, schedule: '0 */6 * * *', last_error: 'database is locked' })), [{ text: 'On', color: 'green' }, { text: 'Last run failed', color: 'red' }])
  assert.equal(pruneSavedMessage(pruneState()), 'Pruning is off. Jobs are kept until they are cleared by hand.')
  assert.match(pruneSavedMessage(pruneState({ enabled: true, next_run_at: '2026-10-13T03:00:00Z' })), /^Finished jobs older than 30 days are cleared every day at 03:00 UTC\. Next run .+\.$/)
  assert.match(pruneSavedMessage(pruneState({ enabled: true, schedule: '0 */6 * * *' })), /cleared every 6 hours, on the hour UTC\./)
  // The schedule read back in plain English, UTC; null where cronProblem takes over.
  for (const [expression, words] of [
    ['0 3 * * *', 'Every day at 03:00 UTC'], ['0 * * * *', 'Every hour, on the hour UTC'], ['0 3 * * 0', 'Every Sunday at 03:00 UTC'],
    ['0 7 5 * 0', 'Every Sunday and on day 5 of the month at 07:00 UTC'], ['*/15 * * * *', 'Every 15 minutes UTC'],
    ['30 7 * * 1-5', 'Every Monday to Friday at 07:30 UTC'], ['0 */6 * * *', 'Every 6 hours, on the hour UTC'],
    ['0 3 1,15 * *', 'On days 1 and 15 of the month at 03:00 UTC'], ['0 3 * 3,9 *', 'Every day in March and September at 03:00 UTC'],
    ['0 9,17 * * *', 'Every day at 09:00 and 17:00 UTC'], ['* * * * *', 'Every minute UTC'], ['5 4 * * 7', 'Every Sunday at 04:05 UTC'],
    ['15 2 */10 * *', 'Every 10 days at 02:15 UTC'], ['0 3 * *', null], ['0 25 * * *', null],
  ]) assert.equal(describeCron(expression), words, expression)
})

// Fixture shape: spec/platform/openapi.yaml PluginBundle.
const bundleState = (over = {}) => ({
  state: 'succeeded', requested_by: 'alice', created_at: '2026-10-13T09:00:00Z', started_at: '2026-10-13T09:00:01Z', finished_at: '2026-10-13T09:00:40Z', error: '',
  bundle: {
    filename: 'dufflebag-plugins-acme-20261013T090040Z-0123456789ab.tar.gz', size: 431 * 1024 * 1024 + 500000,
    sha256: '0123456789ab' + 'c'.repeat(52), exported_at: '2026-10-13T09:00:40Z', plugin_count: 3, version_count: 7,
  },
  ...over,
})

test('the Plugin bundle card offers one export, shows the current bundle with its digest, and keeps it through a failure', () => {
  const card = (bundle) => renderToStaticMarkup(React.createElement(PluginBundleCard, {
    bundle, organizationID: 'org-id', onExport: async () => {}, onDownload: async () => {},
  }))
  const none = card(null)
  assert.match(none, /No bundle yet\./)
  assert.doesNotMatch(none, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Generate bundle</, 'the first generate is available at once')
  assert.match(none, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Export bundle</, 'nothing to export until a bundle has been generated')
  assert.deepEqual(bundleLabels(null), [])

  const done = card(bundleState())
  assert.match(done, /dufflebag-plugins-acme-20261013T090040Z-0123456789ab\.tar\.gz/)
  assert.match(done, /431\.5 MiB/)
  assert.match(done, /3 plugins, 7 versions/)
  assert.match(done, new RegExp('0123456789ab' + 'c'.repeat(52)), 'the full digest is on the card')
  assert.match(done, /id="bundle-sha256"/, 'the digest is copyable')
  assert.doesNotMatch(done, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Export bundle</, 'a generated bundle can be exported')
  assert.match(done, /curl -H &quot;Authorization: Bearer \$TOKEN&quot; -o dufflebag-plugins-acme-20261013T090040Z-0123456789ab\.tar\.gz [^<]*\/api\/v1\/organizations\/org-id\/plugin-registry\/bundle\/download/)
  assert.deepEqual(bundleLabels(bundleState()), [])

  const running = card(bundleState({ state: 'running', finished_at: undefined }))
  assert.match(running, /<button[^>]*disabled[^>]*>[\s\S]{0,300}Generating…</, 'one generate at a time')
  assert.deepEqual(bundleLabels(bundleState({ state: 'running' })), [{ text: 'Exporting', color: 'blue' }])
  assert.deepEqual(bundleLabels(bundleState({ state: 'queued' })), [{ text: 'Queued', color: 'blue' }])

  const failed = card(bundleState({ state: 'failed', error: 'object storage unavailable' }))
  assert.match(failed, />Last export failed</)
  assert.match(failed, /object storage unavailable/)
  assert.doesNotMatch(failed, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Export bundle</, 'the previous bundle is still offered')
  assert.doesNotMatch(failed, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Generate bundle</, 'and a new generate can be tried')

  const settings = (bundle, callerRole = 'maintainer') => renderToStaticMarkup(React.createElement(PluginRegistrySettingsView, {
    organizationName: 'acme', host: 'dufflebag.example.com', callerRole, registry: { enabled: true, exposed: true },
    defaultPlatforms: ['linux_amd64'], loading: false, failure: null, prune: null, bundle,
    onRefresh: () => {}, onEnable: async () => {}, onExpose: async () => {}, onUnexpose: async () => {}, onDisable: async () => {}, onSetDefaultPlatforms: async () => {},
  }))
  assert.match(settings({ current: null, organizationID: 'org-id' }), /aria-label="Plugin bundle"/)
  assert.doesNotMatch(settings(null), /Plugin bundle/, 'nothing loaded, no card')
  assert.doesNotMatch(settings({ current: bundleState(), organizationID: 'org-id' }, 'publisher'), /Plugin bundle/, 'publishers do not see the card')
})

// Fixture shape: spec/platform/openapi.yaml PluginBundleImport and PluginBundleVersionOutcome.
const importState = (over = {}) => ({
  state: 'partially_succeeded', requested_by: 'alice', filename: 'dufflebag-plugins-acme-20261013T090040Z-0123456789ab.tar.gz', size: 1024 * 1024,
  sha256: '0123456789ab' + 'c'.repeat(52), filename_matches: true, created_at: '2026-10-13T10:00:00Z', started_at: '2026-10-13T10:00:01Z',
  finished_at: '2026-10-13T10:00:30Z', error: '',
  bundle: { host: 'connected.example.com', organization: 'acme', exported_at: '2026-10-13T09:00:40Z', dufflebag_version: 'v0.5.0-rc5', plugin_count: 2, version_count: 3 },
  outcomes: [
    { plugin: 'amazon', version: '1.8.2', origin_kind: 'releases-hashicorp', origin_repository: 'packer-plugin-amazon', outcome: 'imported' },
    { plugin: 'git', version: '0.6.3', origin_kind: 'upload', outcome: 'skipped' },
    { plugin: 'git', version: '0.6.2', origin_kind: 'upload', outcome: 'refused', error: 'git is held by another source (github)' },
  ],
  ...over,
})

test('the Plugin bundle card imports a chosen file and reports the last import version by version', () => {
  const card = (lastImport) => renderToStaticMarkup(React.createElement(PluginBundleCard, {
    bundle: null, lastImport, organizationID: 'org-id', onExport: async () => {}, onDownload: async () => {}, onImport: async () => {},
  }))
  const none = card(null)
  assert.match(none, /id="bundle-file"[^>]*aria-label="Choose a plugin bundle"[^>]*type="file"[^>]*style="display:none"/, 'the native control is hidden behind a house button')
  assert.match(none, /<button[^>]*>[\s\S]{0,120}Choose bundle…</)
  assert.match(none, /No bundle chosen/)
  assert.match(none, /id="bundle-expected-sha256"/, 'the operator is asked for the digest the exporting card showed')
  assert.match(none, /A bundle whose bytes differ is refused before anything is stored\./)
  assert.match(none, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Import bundle</, 'nothing chosen, nothing to import')
  assert.doesNotMatch(none, /Bundle import outcomes/)
  assert.deepEqual(importLabels(null), [])

  const done = card(importState())
  assert.match(done, />Partly imported</)
  assert.match(done, /Matches the filename\./)
  assert.match(done, /acme at connected\.example\.com, [^<]*\(v0\.5\.0-rc5\); 2 plugins, 3 versions/)
  assert.match(done, /1 version imported, 1 already held, 1 refused\./)
  assert.match(done, /aria-label="Bundle import outcomes"/)
  assert.match(done, /amazon[\s\S]*?1\.8\.2[\s\S]*?HashiCorp \(packer-plugin-amazon\)[\s\S]*?Imported/)
  assert.match(done, /git[\s\S]*?0\.6\.3[\s\S]*?Local[\s\S]*?Already held/)
  assert.match(done, /Refused<small[^>]*> git is held by another source \(github\)/)
  assert.equal(bundleImportSummary(importState()), '1 version imported, 1 already held, 1 refused.')
  assert.equal(bundleImportSummary(importState({ outcomes: [{ plugin: 'a', version: '1', origin_kind: 'upload', outcome: 'mismatch' }] })), '0 versions imported, 1 held with different checksums.')

  const renamed = card(importState({ filename: 'copy.tar.gz', filename_matches: false }))
  assert.match(renamed, /The filename names a different digest\./)
  const running = card(importState({ state: 'running', finished_at: undefined, outcomes: [] }))
  assert.match(running, /<button[^>]*disabled[^>]*>[\s\S]{0,300}Importing…</)
  assert.deepEqual(importLabels(importState({ state: 'running' })), [{ text: 'Importing', color: 'blue' }])
  const failed = card(importState({ state: 'failed', error: 'the bundle is not a gzip archive: gzip: invalid header', outcomes: [], bundle: undefined }))
  assert.match(failed, />Import failed</)
  assert.match(failed, /the bundle is not a gzip archive/)
})

test('the Prune jobs card is maintainer-only and reflects the schedule, the last run and its error', () => {
  const settingsWith = (prune, callerRole = 'maintainer') => renderToStaticMarkup(React.createElement(PluginRegistrySettingsView, {
    organizationName: 'acme', host: 'dufflebag.example.com', callerRole, registry: { enabled: true, exposed: true },
    defaultPlatforms: ['linux_amd64'], loading: false, failure: null, prune,
    onRefresh: () => {}, onEnable: async () => {}, onExpose: async () => {}, onUnexpose: async () => {}, onDisable: async () => {}, onSetDefaultPlatforms: async () => {},
    onSetPrune: async () => {}, onPruneNow: async () => {},
  }))
  const off = settingsWith(pruneState())
  assert.match(off, /aria-label="Prune jobs"/)
  assert.match(off, />Off</)
  assert.match(off, /id="prune-enabled"[^>]*type="checkbox"/)
  assert.match(off, /aria-label="Age limit in days"[^>]*value="30"/)
  assert.doesNotMatch(off, /Hourly|Weekly|>Custom</, 'one field, no presets')
  assert.match(off, /id="prune-schedule"[^>]*value="0 3 \* \* \*"/)
  assert.match(off, /Every day at 03:00 UTC/, 'the expression read back in English')
  assert.match(off, /Counted from when each job finished\./)
  assert.match(off, /Nothing is pruned while this is off\. 27 jobs are kept/)
  assert.match(off, /<dd[^>]*>[\s\S]{0,80}Never</, 'no run yet')
  assert.match(off, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Save prune schedule</, 'Save waits for a change')
  assert.match(off, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Prune now</, 'Prune now needs the schedule on')
  const on = settingsWith(pruneState({ enabled: true, next_run_at: '2026-10-13T03:00:00Z', last_run_at: '2026-10-12T03:00:00Z', last_run_cleared: 2 }))
  assert.match(on, />On</)
  assert.match(on, /cleared 2/)
  assert.doesNotMatch(on, /<button[^>]*disabled[^>]*>[\s\S]{0,200}Prune now</)
  const failed = settingsWith(pruneState({ enabled: true, last_error: 'database is locked (SQLITE_BUSY)' }))
  assert.match(failed, />Last run failed</)
  assert.match(failed, /database is locked \(SQLITE_BUSY\)/)
  assert.doesNotMatch(settingsWith(null), /Prune jobs/, 'no schedule, no card')
  assert.doesNotMatch(settingsWith(pruneState(), 'publisher'), /Prune jobs/, 'publishers do not see the card')
})

test('the Checks facet lists each imported plugin\'s last check, result and next due time', () => {
  const checksFacet = (over = {}) => renderToStaticMarkup(React.createElement(PluginChecksFacet, {
    plugins: catalogue, unfinished: [], canPublish: true, busy: false, onToggle: () => {}, onOpenPlugin: () => {}, ...over,
  }))
  const states = Object.fromEntries(catalogue.map((plugin) => [plugin.name, checkState(plugin, [])]))
  assert.equal(states.amazon.kind, 'newer', 'the fixture\'s amazon has an update')
  assert.deepEqual(checkState({ name: 'x', source: { kind: 'github' }, update_available: false, update_check: { enabled: false } }, []), { kind: 'off', text: 'Checks off' })
  assert.deepEqual(checkState({ name: 'x', source: { kind: 'github' }, update_available: false, update_check: { enabled: true, error: 'GitHub rate limit reached; resets 08:41 UTC.' } }, []), { kind: 'failed', text: 'GitHub rate limit reached; resets 08:41 UTC.' })
  assert.deepEqual(checkState({ name: 'x', source: { kind: 'github' }, update_available: false, update_check: { enabled: true } }, []), { kind: 'pending', text: 'Not checked yet' })
  assert.deepEqual(checkState({ name: 'x', source: { kind: 'github' }, update_available: false, update_check: { enabled: true, checked_at: '2026-10-12T08:00:00Z' } }, []), { kind: 'ok', text: 'Up to date' })
  assert.deepEqual(checkState({ name: 'vsphere', source: { kind: 'releases-hashicorp' }, update_available: true, update_check: { enabled: true, latest: '1.4.3' } }, [queuedImport('vsphere')]), { kind: 'newer', text: 'Import queued' })
  const html = checksFacet()
  assert.match(html, /aria-label="Update checks"/)
  const checkable = catalogue.filter((plugin) => plugin.source.kind !== 'upload')
  const uploads = catalogue.length - checkable.length
  assert.match(html, new RegExp(`All · ${checkable.length}`))
  assert.match(html, /Newer found · 1/)
  assert.match(html, new RegExp(`${uploads} uploaded plugin${uploads === 1 ? ' isn’t' : 's aren’t'} checked`))
  const upload = catalogue.find((plugin) => plugin.source.kind === 'upload').name
  assert.doesNotMatch(html, new RegExp(`id="check-${upload}"`), 'an uploaded plugin has no row')
  assert.match(html, /id="check-amazon"[^>]*type="checkbox"[^>]*checked/)
  assert.match(html, />Newer · 1\.8\.3</)
  assert.match(html, /<th[^>]*>Next due</)
  const reader = checksFacet({ canPublish: false })
  assert.match(reader, /id="check-amazon"[^>]*disabled/, 'readers see the switch but cannot flip it')
  assert.match(checksFacet({ plugins: catalogue.filter((plugin) => plugin.source.kind === 'upload') }), /No plugin to check/)
  const failed = checksFacet({ plugins: [{ name: 'docker', source: { kind: 'github', repository: 'acme-infra/packer-plugin-docker' }, published_versions: 1, update_available: false, update_check: { enabled: true, checked_at: '2026-10-12T08:00:00Z', error: 'GitHub rate limit reached; resets 08:41 UTC.', next_check_at: '2026-10-13T08:00:00Z' } }] })
  assert.match(failed, />Failed<[\s\S]{0,300}?GitHub rate limit reached; resets 08:41 UTC\./)
  assert.match(failed, /Failed · 1/)
  // The rail carries the third facet.
  assert.match(render({ registry: { enabled: true, exposed: true }, plugins: [] }), />Plugins<[\s\S]*?>Jobs<[\s\S]*?>Checks</)
})
