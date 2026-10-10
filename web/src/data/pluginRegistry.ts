import { platformDelete, platformGet, platformPost, platformPut, platformPutForm } from '../api/client'

export type PluginRegistry = {
  enabled: boolean
  exposed: boolean
}

function path(organizationID: string, action = ''): string {
  const base = `/organizations/${encodeURIComponent(organizationID)}/plugin-registry`
  return action === '' ? base : `${base}/${action}`
}

export function getPluginRegistry(token: string, organizationID: string): Promise<PluginRegistry> {
  return platformGet<PluginRegistry>(token, path(organizationID))
}

export function enablePluginRegistry(token: string, organizationID: string): Promise<PluginRegistry> {
  return platformPost<PluginRegistry>(token, path(organizationID, 'enable'))
}

export function exposePluginRegistry(token: string, organizationID: string): Promise<PluginRegistry> {
  return platformPost<PluginRegistry>(token, path(organizationID, 'expose'))
}

export function unexposePluginRegistry(token: string, organizationID: string): Promise<PluginRegistry> {
  return platformPost<PluginRegistry>(token, path(organizationID, 'unexpose'))
}

export async function disablePluginRegistry(token: string, organizationID: string): Promise<void> {
  await platformPost<null>(token, path(organizationID, 'disable'))
}

export type PluginSource = { kind: 'upload' | 'releases-hashicorp' | 'github'; repository?: string }

export type PluginUpdateCheck = { enabled: boolean; checked_at?: string; error?: string; latest?: string; latest_tag?: string }

export type Plugin = {
  name: string
  source: PluginSource
  published_versions: number
  newest_version?: string
  update_check: PluginUpdateCheck
  update_available: boolean
}

export type PluginPlatform = { os: string; arch: string }

export type PluginVersion = {
  version: string
  revoked: boolean
  created_at: string
  listed_platforms: PluginPlatform[]
  stored_platforms: PluginPlatform[]
}

export type PluginVersions = { name: string; source: PluginSource; versions: PluginVersion[] }

export type TemplateStanza = { source: string; version: string; hcl: string }

export type PublishedPluginVersion = { version: PluginVersion; stanza: TemplateStanza }

export async function listPlugins(token: string, organizationID: string): Promise<Plugin[]> {
  const body = await platformGet<{ plugins?: Plugin[] }>(token, path(organizationID, 'plugins'))
  return body.plugins ?? []
}

export function listPluginVersions(token: string, organizationID: string, name: string): Promise<PluginVersions> {
  return platformGet<PluginVersions>(token, path(organizationID, `plugins/${encodeURIComponent(name)}/versions`))
}

export function publishPluginVersion(
  token: string, organizationID: string, plan: ReadyUpload,
): Promise<PublishedPluginVersion> {
  const form = new FormData()
  for (const { field, file } of plan.files) form.append(field, file, file.name)
  return platformPutForm<PublishedPluginVersion>(
    token,
    path(organizationID, `plugins/${encodeURIComponent(plan.name)}/versions/${encodeURIComponent(plan.version)}`),
    form,
  )
}

export function sourceLabel(source: PluginSource): string {
  return { upload: 'Local', 'releases-hashicorp': 'HashiCorp', github: 'GitHub' }[source.kind]
}

export function templateStanza(host: string, organization: string, name: string, version: string): string {
  const source = `${host}/plugins/${organization}/${name}`
  return `packer {\n  required_plugins {\n    ${name} = {\n      source  = "${source}"\n      version = "${version}"\n    }\n  }\n}\n`
}

type NamedFile = { name: string; size: number }

export type ReadyUpload<F extends NamedFile = File> = {
  kind: 'ready'
  name: string
  version: string
  files: { field: 'sha256sums' | 'sha256sums_sig' | 'manifest' | 'zips'; file: F; platform?: string }[]
}

export type RefusedUpload = { label: string; reason: string }

export type UploadPlan<F extends NamedFile = File> = { versions: ReadyUpload<F>[]; refused: RefusedUpload[] }

// Names follow the two shapes Packer's getter accepts: goreleaser
// (packer-plugin-NAME_vVERSION_xAPI_OS_ARCH.zip) and release-site
// (packer-plugin-NAME_VERSION_OS_ARCH.zip). The server verifies everything;
// this only groups files into one upload per version so the operator sees
// what will be sent.
const sumsName = /^packer-plugin-([a-z0-9-]+)_v?([0-9][^_]*)_SHA256SUMS$/
const zipName = /^packer-plugin-([a-z0-9-]+)_v?([0-9][^_]*?)(?:_x[0-9]+\.[0-9]+)?_([a-z0-9]+)_([a-z0-9]+)\.zip$/
const manifestName = /^packer-plugin-([a-z0-9-]+)_v?([0-9][^_]*)_manifest\.json$/

export function planPluginUploads<F extends NamedFile>(files: F[]): UploadPlan<F> {
  const groups = new Map<string, ReadyUpload<F>>()
  const refused: RefusedUpload[] = []
  const group = (match: RegExpMatchArray) => {
    const [, name = '', version = ''] = match
    const key = `${name} ${version}`
    let found = groups.get(key)
    if (!found) {
      found = { kind: 'ready', name, version, files: [] }
      groups.set(key, found)
    }
    return found
  }
  for (const file of files) {
    const signed = file.name.endsWith('_SHA256SUMS.sig') ? file.name.slice(0, -4).match(sumsName) : null
    const sums = file.name.match(sumsName)
    const manifest = file.name.match(manifestName)
    const zip = file.name.match(zipName)
    if (signed) {
      group(signed).files.push({ field: 'sha256sums_sig', file })
    } else if (sums) {
      group(sums).files.push({ field: 'sha256sums', file })
    } else if (manifest) {
      group(manifest).files.push({ field: 'manifest', file })
    } else if (zip) {
      const [, , , os = '', arch = ''] = zip
      group(zip).files.push({ field: 'zips', file, platform: `${os}_${arch}` })
    } else {
      refused.push({ label: file.name, reason: 'not a SHA256SUMS file, signature, manifest or plugin zip' })
    }
  }
  const versions: ReadyUpload<F>[] = []
  for (const upload of groups.values()) {
    const label = `${upload.name} ${upload.version}`
    const sumsFiles = upload.files.filter((f) => f.field === 'sha256sums').length
    if (sumsFiles !== 1) {
      refused.push({ label, reason: sumsFiles === 0 ? 'no SHA256SUMS file' : 'more than one SHA256SUMS file' })
    } else if (!upload.files.some((f) => f.field === 'zips')) {
      refused.push({ label, reason: 'no plugin zip' })
    } else {
      versions.push(upload)
    }
  }
  return { versions, refused }
}

function versionPath(organizationID: string, name: string, version: string): string {
  return path(organizationID, `plugins/${encodeURIComponent(name)}/versions/${encodeURIComponent(version)}`)
}

export async function deletePluginVersion(token: string, organizationID: string, name: string, version: string): Promise<void> {
  await platformDelete(token, versionPath(organizationID, name, version))
}

export type HashicorpPlugin = { product: string; name: string; mirrored_versions: number; held_by?: PluginSource }

export type HashicorpPluginVersion = {
  version: string
  created_at: string
  prerelease: boolean
  state?: string
  changelog?: string
  platforms: string[]
  mirrored: boolean
}

export type ImportPlatformOutcome = { platform: string; outcome: 'imported' | 'already_mirrored' | 'failed'; error?: string }

export type PluginChange = { version: string; action: 'add' | 'revoke' | 'restore'; platforms?: string[] }

export type ImportVersionOutcome = {
  version: string
  outcome: 'imported' | 'already_mirrored' | 'revoked' | 'restored' | 'failed'
  error?: string
  platforms?: ImportPlatformOutcome[]
}

export type PluginImport = {
  id: string
  source: string
  product: string
  versions: string[]
  platforms: string[]
  changes: PluginChange[]
  state: 'queued' | 'running' | 'succeeded' | 'partially_succeeded' | 'failed'
  created_by: string
  origin: 'import' | 'plugin' | 'catalogue' | 'upload'
  batch_index: number
  batch_size: number
  queued_ahead: number
  created_at: string
  finished_at?: string
  outcomes: ImportVersionOutcome[]
}

// A job's rows are its sync changes, or the versions an import requested;
// outcomes are recorded in that order as the worker finishes each one.
export function importJobRows(job: PluginImport): { version: string; change: string; architectures: string; outcome?: ImportVersionOutcome }[] {
  const sync = job.changes.length > 0
  const rows = sync
    ? job.changes.map((change) => ({
      version: change.version,
      change: change.action === 'add' ? 'Add' : change.action === 'revoke' ? 'Revoke' : 'Restore',
      architectures: change.platforms?.length ? change.platforms.join(', ') : '—',
    }))
    : job.versions.map((version) => ({ version, change: 'Import', architectures: job.platforms.join(', ') }))
  return rows.map((row, index) => ({ ...row, outcome: job.outcomes[index] }))
}

export function importJobSummary(job: PluginImport): { text: string; progress?: number } {
  const rows = importJobRows(job)
  const unit = job.changes.length ? 'change' : 'version'
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`
  const done = job.outcomes.length
  switch (job.state) {
    case 'queued':
      return { text: job.queued_ahead ? `Waiting for ${plural(job.queued_ahead, 'job')} ahead of it.` : 'Next to run.' }
    case 'running':
      return { text: `${done} of ${rows.length} ${unit}s done.`, progress: Math.round((done / Math.max(rows.length, 1)) * 100) }
    case 'failed':
      return { text: 'Nothing was changed.' }
    default: {
      // A row with a failed platform counts as failed, as the job's state does.
      const failed = job.outcomes.filter((o) => o.outcome === 'failed' || o.platforms?.some((p) => p.outcome === 'failed')).length
      if (job.state === 'partially_succeeded') return { text: `${rows.length - failed} of ${rows.length} ${unit}s applied. ${plural(failed, unit)} failed.` }
      if (job.changes.length) return { text: `${plural(rows.length, 'change')} applied.` }
      const files = job.outcomes.flatMap((o) => o.platforms ?? [])
      const imported = files.filter((p) => p.outcome === 'imported').length
      const already = files.filter((p) => p.outcome === 'already_mirrored').length + job.outcomes.filter((o) => o.outcome === 'already_mirrored' && !o.platforms?.length).length
      return { text: `${plural(rows.length, 'version')}. ${plural(imported, 'file')} imported${already ? `, ${already} already mirrored` : ''}.` }
    }
  }
}

// Queues the failed part of a finished job again: a failed sync's failed
// changes (an add keeps only its failed platforms), or a failed import's
// failed versions.
export function retryPluginImport(token: string, organizationID: string, job: PluginImport): Promise<PluginImport> {
  const failed = (index: number) => {
    const outcome = job.outcomes[index]
    return !outcome || outcome.outcome === 'failed' || (outcome.platforms ?? []).some((p) => p.outcome === 'failed')
  }
  if (job.changes.length) {
    const changes = job.changes.flatMap((change, index) => {
      if (!failed(index)) return []
      if (change.action !== 'add') return [change]
      const failedPlatforms = (job.outcomes[index]?.platforms ?? []).filter((p) => p.outcome === 'failed').map((p) => p.platform)
      return [{ ...change, platforms: failedPlatforms.length ? failedPlatforms : change.platforms }]
    })
    return syncPlugin(token, organizationID, job.product, changes)
  }
  const failedVersions = job.versions.filter((_, index) => failed(index))
  const versions = failedVersions.length ? failedVersions : job.versions
  if (job.source === 'github') return createGithubImport(token, organizationID, job.product, versions[0] ?? '', job.platforms)
  return createPluginImport(token, organizationID, job.product, versions, job.platforms)
}

export async function listHashicorpPlugins(token: string, organizationID: string): Promise<HashicorpPlugin[]> {
  const body = await platformGet<{ plugins?: HashicorpPlugin[] }>(token, path(organizationID, 'catalogue/hashicorp'))
  return body.plugins ?? []
}

export function listHashicorpPluginVersions(
  token: string, organizationID: string, product: string, after?: string,
): Promise<{ versions: HashicorpPluginVersion[]; next?: string }> {
  const query = after ? `?after=${encodeURIComponent(after)}` : ''
  return platformGet(token, path(organizationID, `catalogue/hashicorp/${encodeURIComponent(product)}${query}`))
}

export function createPluginImport(
  token: string, organizationID: string, product: string, versions: string[], platforms: string[],
): Promise<PluginImport> {
  return platformPost<PluginImport>(token, path(organizationID, 'imports'), { source: 'releases-hashicorp', product, versions, platforms })
}

export function getPluginImport(token: string, organizationID: string, id: string): Promise<PluginImport> {
  return platformGet<PluginImport>(token, path(organizationID, `imports/${encodeURIComponent(id)}`))
}

export async function getDefaultPlatforms(token: string, organizationID: string): Promise<string[]> {
  return (await platformGet<{ platforms: string[] }>(token, path(organizationID, 'default-platforms'))).platforms
}

export async function setDefaultPlatforms(token: string, organizationID: string, platforms: string[]): Promise<string[]> {
  return (await platformPut<{ platforms: string[] }>(token, path(organizationID, 'default-platforms'), { platforms })).platforms
}

export const terminalImportStates = new Set(['succeeded', 'partially_succeeded', 'failed'])

export type GithubRelease = {
  repository: string
  name: string
  tag: string
  version: string
  prerelease: boolean
  platforms: string[]
  has_checksum: boolean
  held_by?: PluginSource
}

export function resolveGithubRelease(token: string, organizationID: string, releaseURL: string): Promise<GithubRelease> {
  return platformPost<GithubRelease>(token, path(organizationID, 'catalogue/github/resolve'), { release_url: releaseURL })
}

export function createGithubImport(
  token: string, organizationID: string, repository: string, tag: string, platforms: string[],
): Promise<PluginImport> {
  return platformPost<PluginImport>(token, path(organizationID, 'imports'), { source: 'github', product: repository, versions: [tag], platforms })
}

export function syncPlugin(token: string, organizationID: string, name: string, changes: PluginChange[]): Promise<PluginImport> {
  return platformPost<PluginImport>(token, path(organizationID, `plugins/${encodeURIComponent(name)}/sync`), { changes })
}

// A version absent from selected is unchanged.
export type PluginEdit = { selected: Record<string, boolean>; added: Record<string, string[]> }

const platformKey = (p: PluginPlatform) => `${p.os}_${p.arch}`

// A revoked version takes no platforms; an unmirrored upstream version needs at least one.
export function pluginChanges(versions: PluginVersion[], upstream: HashicorpPluginVersion[], edit: PluginEdit): PluginChange[] {
  const changes: PluginChange[] = []
  const mirrored = new Set(versions.map((v) => v.version))
  for (const version of upstream) {
    const platforms = edit.added[version.version] ?? []
    if (!mirrored.has(version.version) && edit.selected[version.version] && platforms.length) {
      changes.push({ version: version.version, action: 'add', platforms })
    }
  }
  for (const version of versions) {
    const selected = edit.selected[version.version] ?? !version.revoked
    const stored = new Set(version.stored_platforms.map(platformKey))
    const platforms = (edit.added[version.version] ?? []).filter((p) => !stored.has(p))
    if (version.revoked && selected) changes.push({ version: version.version, action: 'restore' })
    else if (!version.revoked && !selected) changes.push({ version: version.version, action: 'revoke' })
    else if (!version.revoked && platforms.length) changes.push({ version: version.version, action: 'add', platforms })
  }
  return changes
}

export async function setPluginUpdateCheck(token: string, organizationID: string, name: string, enabled: boolean): Promise<void> {
  await platformPut<null>(token, path(organizationID, `plugins/${encodeURIComponent(name)}/update-check`), { enabled })
}

export type CatalogueSyncResult = { plugin: string; version?: string; import_id?: string; refused?: string }

export async function syncCatalogue(token: string, organizationID: string, plugins: string[]): Promise<CatalogueSyncResult[]> {
  return (await platformPost<{ results: CatalogueSyncResult[] }>(token, path(organizationID, 'sync'), { plugins })).results
}

// One row of the catalogue's "Sync N plugins" confirmation: the version the
// job moves to and which of the plugin's architectures that release has.
export type CatalogueSyncPlan = {
  plugin: string
  from: string
  to: string
  architectures: string
  warning: string
}

export async function planCatalogueSync(token: string, organizationID: string, plugins: Plugin[]): Promise<CatalogueSyncPlan[]> {
  return Promise.all(plugins.map(async (plugin) => {
    const to = plugin.update_check.latest ?? ''
    const held = await listPluginVersions(token, organizationID, plugin.name)
    const existing = [...new Set(held.versions.flatMap((v) => v.stored_platforms.map((p) => `${p.os}_${p.arch}`)))].sort()
    let published: string[] = []
    if (plugin.source.kind === 'releases-hashicorp' && plugin.source.repository) {
      const page = await listHashicorpPluginVersions(token, organizationID, plugin.source.repository)
      published = page.versions.find((v) => v.version === to)?.platforms ?? []
    } else if (plugin.source.kind === 'github' && plugin.source.repository) {
      const tag = plugin.update_check.latest_tag ?? `v${to}`
      published = (await resolveGithubRelease(token, organizationID, `https://github.com/${plugin.source.repository}/releases/tag/${tag}`)).platforms
    }
    const missing = existing.filter((platform) => !published.includes(platform))
    const plural = (n: number) => `${n} architecture${n === 1 ? '' : 's'}`
    return {
      plugin: plugin.name, from: plugin.newest_version ?? '', to,
      architectures: missing.length ? `${existing.length - missing.length} of its ${plural(existing.length)}` : `its ${plural(existing.length)}`,
      warning: missing.length ? `${missing.join(', ')} ${missing.length === 1 ? "isn't" : "aren't"} published for ${to} and will be skipped` : '',
    }
  }))
}

export type CatalogueSourceFilter = 'All sources' | 'HashiCorp' | 'GitHub' | 'Local'

// The catalogue's three filters, applied together.
export function catalogueRows(plugins: Plugin[], filters: { name: string; source: CatalogueSourceFilter; updatesOnly: boolean }): Plugin[] {
  const name = filters.name.trim().toLowerCase()
  return plugins
    .filter((plugin) => plugin.name.includes(name))
    .filter((plugin) => filters.source === 'All sources' || sourceLabel(plugin.source) === filters.source)
    .filter((plugin) => !filters.updatesOnly || plugin.update_available)
}

// The architectures Packer plugins are published for, grouped as the detail
// grid's column headers show them (design frame 5.0).
export const PLUGIN_OS_GROUPS: readonly (readonly [string, readonly string[]])[] = [
  ['darwin', ['amd64', 'arm64']], ['freebsd', ['386', 'amd64', 'arm']], ['linux', ['386', 'amd64', 'arm', 'arm64']],
  ['netbsd', ['386', 'amd64', 'arm']], ['openbsd', ['386', 'amd64', 'arm']], ['solaris', ['amd64']], ['windows', ['386', 'amd64']],
]
export const PLUGIN_ARCHITECTURES: readonly string[] = PLUGIN_OS_GROUPS.flatMap(([os, arches]) => arches.map((arch) => `${os}_${arch}`))

export type PendingItem = { verb: 'Add' | 'Revoke' | 'Restore'; what: string; detail: string; version: string }

// The pending-changes list as the design words it, one item per change.
export function pendingItems(changes: PluginChange[], mirrored: Set<string>): PendingItem[] {
  return changes.map((change) => {
    const platforms = change.platforms ?? []
    switch (change.action) {
      case 'revoke': return { verb: 'Revoke', what: change.version, detail: 'stays stored; Packer can no longer fetch it', version: change.version }
      case 'restore': return { verb: 'Restore', what: change.version, detail: 'served again', version: change.version }
      default: return mirrored.has(change.version)
        ? { verb: 'Add', what: platforms.join(', '), detail: `to ${change.version}`, version: change.version }
        : { verb: 'Add', what: change.version, detail: `${platforms.length} architecture${platforms.length === 1 ? '' : 's'}: ${platforms.join(', ')}`, version: change.version }
    }
  })
}
