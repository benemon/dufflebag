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
let planPluginUploads
let templateStanza
let PluginDetailView
let PluginUploadView
let listPlugins
let publishPluginVersion
let revokePluginVersion
let restorePluginVersion
let deletePluginVersion
let PluginHashicorpView
let PluginImportJobView
let PluginGithubView
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
  ;({
    disablePluginRegistry, enablePluginRegistry, exposePluginRegistry, getPluginRegistry,
    unexposePluginRegistry, planPluginUploads, templateStanza, listPlugins, publishPluginVersion,
    revokePluginVersion, restorePluginVersion, deletePluginVersion, createPluginImport, createGithubImport,
  } = await vite.ssrLoadModule('/src/data/pluginRegistry.ts'))
  ;({ PluginDetailView } = await vite.ssrLoadModule('/src/screens/PluginDetail.tsx'))
  ;({ PluginUploadView } = await vite.ssrLoadModule('/src/screens/PluginUpload.tsx'))
  ;({ PluginHashicorpView } = await vite.ssrLoadModule('/src/screens/PluginHashicorp.tsx'))
  ;({ PluginImportJobView } = await vite.ssrLoadModule('/src/screens/PluginImportJob.tsx'))
  ;({ PluginGithubView } = await vite.ssrLoadModule('/src/screens/PluginGithub.tsx'))
})

after(async () => { await vite.close() })

const props = (over = {}) => ({
  organizationName: 'acme', callerRole: 'maintainer', host: 'dufflebag.example.com',
  registry: { enabled: false, exposed: false }, plugins: [], loading: false, failure: null,
  onOpenPlugin: () => {}, onUpload: () => {}, onBrowse: () => {}, onImportGithub: () => {},
  defaultPlatforms: ['linux_amd64', 'linux_arm64', 'darwin_arm64'], onSetDefaultPlatforms: async () => {},
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
    busy: false, actionFailure: null, onRevoke: () => {}, onRestore: () => {}, onRemove: () => {},
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
  assert.match(done, /Packer can&#x27;t resolve this template stanza until the registry is exposed/)
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

test('version rows offer revoke or restore and removal to publishers only', () => {
  const view = (callerRole, revoked) => renderToStaticMarkup(React.createElement(PluginDetailView, {
    name: 'git', organizationName: 'acme', host: 'dufflebag.example.com', callerRole,
    registry: { enabled: true, exposed: true }, loading: false, failure: null, onRefresh: () => {}, onUpload: () => {},
    busy: false, actionFailure: null, onRevoke: () => {}, onRestore: () => {}, onRemove: () => {},
    detail: { name: 'git', source: { kind: 'upload' }, versions: [{
      version: '0.6.3', revoked, created_at: '2026-10-09T00:00:00Z', listed_platforms: [], stored_platforms: [],
    }] },
  }))
  const served = view('publisher', false)
  assert.match(served, />Revoke</)
  assert.match(served, />Remove version</)
  const revoked = view('publisher', true)
  assert.match(revoked, />Restore</)
  assert.match(revoked, />Revoked</)
  assert.doesNotMatch(revoked, /Template stanza/)
  const reader = view('reader', false)
  assert.doesNotMatch(reader, />Revoke</)
  assert.doesNotMatch(reader, />Remove version</)
})

test('revoke, restore and remove use the version paths', async () => {
  const originalFetch = globalThis.fetch
  const requests = []
  globalThis.fetch = async (path, options) => {
    requests.push(`${options.method} ${path}`)
    return new Response(null, { status: 204 })
  }
  try {
    await revokePluginVersion('token', 'org', 'git', '0.6.3')
    await restorePluginVersion('token', 'org', 'git', '0.6.3')
    await deletePluginVersion('token', 'org', 'git', '0.6.3')
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.deepEqual(requests, [
    'POST /api/v1/organizations/org/plugin-registry/plugins/git/versions/0.6.3/revoke',
    'POST /api/v1/organizations/org/plugin-registry/plugins/git/versions/0.6.3/restore',
    'DELETE /api/v1/organizations/org/plugin-registry/plugins/git/versions/0.6.3',
  ])
})

test('the registry settings show the default platforms and the catalogue offers HashiCorp to publishers', () => {
  const html = render({ registry: { enabled: true, exposed: false } })
  assert.match(html, /value="linux_amd64, linux_arm64, darwin_arm64"/)
  assert.match(html, /Save default platforms/)
  assert.match(html, /Browse HashiCorp/)
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
      platforms: ['linux_amd64', 'windows_386'], state: 'partially_succeeded', created_at: '2026-10-09T00:00:00Z',
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
  assert.match(html, /Packer can&#x27;t resolve this template stanza until the registry is exposed/)
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
