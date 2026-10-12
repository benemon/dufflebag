import platforms from './platforms.json'
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

export type PluginUpdateCheck = { enabled: boolean; checked_at?: string; error?: string; latest?: string; latest_tag?: string; next_check_at?: string }

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

export type RefusedUpload<F extends NamedFile = File> = { label: string; reason: string; files: F[] }

export type UploadPlan<F extends NamedFile = File> = { versions: ReadyUpload<F>[]; refused: RefusedUpload<F>[] }

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
  const refused: RefusedUpload<F>[] = []
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
      refused.push({ label: file.name, reason: 'not a SHA256SUMS file, signature, manifest or plugin zip', files: [file] })
    }
  }
  const versions: ReadyUpload<F>[] = []
  for (const upload of groups.values()) {
    const label = `${upload.name} ${upload.version}`
    const sumsFiles = upload.files.filter((f) => f.field === 'sha256sums').length
    if (sumsFiles !== 1) {
      refused.push({ label, reason: sumsFiles === 0 ? 'no SHA256SUMS file' : 'more than one SHA256SUMS file', files: upload.files.map((f) => f.file) })
    } else if (!upload.files.some((f) => f.field === 'zips')) {
      refused.push({ label, reason: 'no plugin zip', files: upload.files.map((f) => f.file) })
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

export async function revokePluginVersion(token: string, organizationID: string, name: string, version: string): Promise<void> {
  await platformPost<null>(token, `${versionPath(organizationID, name, version)}/revoke`)
}

export async function restorePluginVersion(token: string, organizationID: string, name: string, version: string): Promise<void> {
  await platformPost<null>(token, `${versionPath(organizationID, name, version)}/restore`)
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
  plugin: string
  versions: string[]
  platforms: string[]
  changes: PluginChange[]
  state: 'queued' | 'running' | 'succeeded' | 'partially_succeeded' | 'failed'
  created_by: string
  origin: 'import' | 'plugin' | 'catalogue' | 'upload'
  batch_index: number
  batch_size: number
  queued_ahead: number
  retry_of?: string
  created_at: string
  started_at?: string
  finished_at?: string
  outcomes: ImportVersionOutcome[]
}

export type JobStateFilter = PluginImport['state'] | 'unfinished'

export type PluginImportFilter = { states?: JobStateFilter[]; plugins?: string[]; limit?: number; offset?: number }

export function listPluginImports(token: string, organizationID: string, filter: PluginImportFilter = {}): Promise<{ imports: PluginImport[]; total: number }> {
  const query = new URLSearchParams()
  if (filter.states?.length) query.set('state', filter.states.join(','))
  if (filter.plugins?.length) query.set('plugin', filter.plugins.join(','))
  if (filter.limit !== undefined) query.set('limit', String(filter.limit))
  if (filter.offset) query.set('offset', String(filter.offset))
  const suffix = query.size ? `?${query}` : ''
  return platformGet(token, path(organizationID, `imports${suffix}`))
}

export async function deletePluginImport(token: string, organizationID: string, id: string): Promise<void> {
  await platformDelete(token, path(organizationID, `imports/${encodeURIComponent(id)}`))
}

// What a job does, in the words of the Jobs table: "1.0.0 · 3 platforms",
// "2 additions", "3 versions".
export function jobWhat(job: PluginImport): string {
  if (job.changes.length) return `${job.changes.length} ${job.changes.length === 1 ? 'addition' : 'additions'}`
  if (job.origin === 'upload') return `${job.versions.length} ${job.versions.length === 1 ? 'version' : 'versions'}`
  return `${job.versions.join(', ')} · ${job.platforms.length} ${job.platforms.length === 1 ? 'platform' : 'platforms'}`
}

// The plugin's unfinished job that runs first; a listing is newest first.
export function activeJobFor(unfinished: PluginImport[], plugin: string): PluginImport | undefined {
  return [...unfinished].reverse().find((job) => job.plugin === plugin)
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
    return syncPlugin(token, organizationID, job.product, changes, job.id)
  }
  const failedVersions = job.versions.filter((_, index) => failed(index))
  const versions = failedVersions.length ? failedVersions : job.versions
  if (job.source === 'github') return createGithubImport(token, organizationID, job.product, versions[0] ?? '', job.platforms, job.id)
  return createPluginImport(token, organizationID, job.product, versions, job.platforms, job.id)
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
  token: string, organizationID: string, product: string, versions: string[], platforms: string[], retryOf?: string,
): Promise<PluginImport> {
  return platformPost<PluginImport>(token, path(organizationID, 'imports'), { source: 'releases-hashicorp', product, versions, platforms, ...(retryOf ? { retry_of: retryOf } : {}) })
}

export function getPluginImport(token: string, organizationID: string, id: string): Promise<PluginImport> {
  return platformGet<PluginImport>(token, path(organizationID, `imports/${encodeURIComponent(id)}`))
}

export type PluginPrune = {
  enabled: boolean
  age_days: number
  schedule: string
  next_run_at?: string
  last_run_at?: string
  last_run_cleared: number
  last_error: string
  would_clear: number
  unfinished: number
  kept: number
  oldest_finished_at?: string
}

export type PluginPruneRequest = { enabled: boolean; age_days: number; schedule: string }

export const pruneAges = [1, 7, 14, 30, 60, 90, 180, 365]

export const pruneSchedules: { key: 'hourly' | 'daily' | 'weekly'; text: string; expression: string; words: string }[] = [
  { key: 'hourly', text: 'Hourly', expression: '0 * * * *', words: 'every hour, on the hour' },
  { key: 'daily', text: 'Daily', expression: '0 3 * * *', words: 'every day at 03:00 UTC' },
  { key: 'weekly', text: 'Weekly', expression: '0 3 * * 0', words: 'every Sunday at 03:00 UTC' },
]

export function scheduleWords(schedule: string): string {
  return pruneSchedules.find((preset) => preset.expression === schedule)?.words ?? `on the schedule ${schedule} (UTC)`
}

// A five-field cron expression's first problem, field by field, or null.
// The server parses the expression itself; this keeps Save honest before it is pressed.
export function cronProblem(schedule: string): string | null {
  const fields = schedule.trim().split(/\s+/).filter(Boolean)
  if (fields.length !== 5) return `A schedule has 5 fields (minute, hour, day of month, month, day of week); this has ${fields.length}.`
  const names: [string, number, number][] = [['minute', 0, 59], ['hour', 0, 23], ['day of month', 1, 31], ['month', 1, 12], ['day of week', 0, 7]]
  for (const [index, field] of fields.entries()) {
    const [name, low, high] = names[index] as [string, number, number]
    for (const part of field.split(',')) {
      const match = /^(\*|(\d+)(?:-(\d+))?)(?:\/(\d+))?$/.exec(part)
      if (!match) return `The ${name} field "${part}" is not a number, range, list or step.`
      const numbers = [match[2], match[3]].filter((v) => v !== undefined).map(Number)
      if (numbers.some((n) => n < low || n > high)) return `The ${name} field must be ${low} to ${high}.`
      if (match[4] !== undefined && Number(match[4]) === 0) return `The ${name} field's step cannot be 0.`
    }
  }
  return null
}

export function prunePreview(prune: PluginPrune, enabled: boolean, ageDays: number): string {
  const jobs = (n: number) => `${n} ${n === 1 ? 'job' : 'jobs'}`
  if (!enabled) {
    const oldest = prune.oldest_finished_at ? `, the oldest finished ${new Date(prune.oldest_finished_at).toLocaleDateString()}` : ''
    return `Nothing is pruned while this is off. ${jobs(prune.kept)} ${prune.kept === 1 ? 'is' : 'are'} kept${oldest}.`
  }
  const cutoff = new Date(Date.now() - ageDays * 86400000).toLocaleDateString()
  const untouched = `${jobs(prune.unfinished)} queued or running ${prune.unfinished === 1 ? 'is' : 'are'} never touched.`
  if (ageDays !== prune.age_days) return `Save to see what ${ageDays} days would clear now. ${untouched}`
  if (prune.would_clear === 0) return `If it ran now, it would clear nothing: no finished job is older than ${ageDays} days. ${untouched}`
  return `If it ran now, it would clear ${jobs(prune.would_clear)} that finished before ${cutoff}. ${untouched}`
}

export function getPluginPrune(token: string, organizationID: string): Promise<PluginPrune> {
  return platformGet<PluginPrune>(token, path(organizationID, 'prune'))
}

export function setPluginPrune(token: string, organizationID: string, request: PluginPruneRequest): Promise<PluginPrune> {
  return platformPut<PluginPrune>(token, path(organizationID, 'prune'), request)
}

export function runPluginPrune(token: string, organizationID: string): Promise<PluginPrune> {
  return platformPost<PluginPrune>(token, path(organizationID, 'prune/run'))
}

export async function getDefaultPlatforms(token: string, organizationID: string): Promise<string[]> {
  return (await platformGet<{ platforms: string[] }>(token, path(organizationID, 'default-platforms'))).platforms
}

export async function setDefaultPlatforms(token: string, organizationID: string, platforms: string[]): Promise<string[]> {
  return (await platformPut<{ platforms: string[] }>(token, path(organizationID, 'default-platforms'), { platforms })).platforms
}

export const terminalImportStates = new Set(['succeeded', 'partially_succeeded', 'failed'])

export function jobName(job: PluginImport): string {
  return job.product.replace(/^.*packer-plugin-/, '')
}

export function jobTitle(job: PluginImport): string {
  const verb = job.origin === 'upload' ? 'Upload' : job.changes.length ? 'Sync' : 'Import'
  return `${verb} ${jobName(job)}`
}

export function jobOrigin(job: PluginImport): string {
  switch (job.origin) {
    case 'plugin': return `from the plugin page, ${job.changes.length} ${job.changes.length === 1 ? 'change' : 'changes'}`
    case 'catalogue': return `from Sync selected (${job.batch_index} of ${job.batch_size} ${job.batch_size === 1 ? 'job' : 'jobs'})`
    case 'upload': return 'from Upload'
    default: return job.source === 'github' ? `from Import from GitHub (${job.product})` : `from Browse HashiCorp (${job.product})`
  }
}

export function firstError(outcomes: ImportVersionOutcome[]): string | undefined {
  for (const outcome of outcomes) {
    if (outcome.error) return outcome.error
    const platform = outcome.platforms?.find((p) => p.error)
    if (platform?.error) return `${platform.platform}: ${platform.error}`
  }
  return undefined
}

export type GithubRelease = {
  repository: string
  name: string
  tag: string
  version: string
  prerelease: boolean
  published_at?: string
  platforms: string[]
  has_checksum: boolean
  checksum_asset?: string
  held_by?: PluginSource
}

// GitHub's unauthenticated limit; the server passes the reset time through.
export function githubRateLimit(message: string): { resetsAt: Date } | null {
  if (!/rate limit/i.test(message)) return null
  const at = /(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)/.exec(message)
  return { resetsAt: at?.[1] ? new Date(at[1]) : new Date(Date.now() + 3_600_000) }
}

export function resolveGithubRelease(token: string, organizationID: string, releaseURL: string): Promise<GithubRelease> {
  return platformPost<GithubRelease>(token, path(organizationID, 'catalogue/github/resolve'), { release_url: releaseURL })
}

export function createGithubImport(
  token: string, organizationID: string, repository: string, tag: string, platforms: string[], retryOf?: string,
): Promise<PluginImport> {
  return platformPost<PluginImport>(token, path(organizationID, 'imports'), { source: 'github', product: repository, versions: [tag], platforms, ...(retryOf ? { retry_of: retryOf } : {}) })
}

export function syncPlugin(token: string, organizationID: string, name: string, changes: PluginChange[], retryOf?: string): Promise<PluginImport> {
  return platformPost<PluginImport>(token, path(organizationID, `plugins/${encodeURIComponent(name)}/sync`), { changes, ...(retryOf ? { retry_of: retryOf } : {}) })
}

const platformKey = (p: PluginPlatform) => `${p.os}_${p.arch}`

// The sync carries additions only: platforms keyed by version. A revoked
// version takes none; an unmirrored upstream version needs at least one.
export function pluginChanges(versions: PluginVersion[], upstream: HashicorpPluginVersion[], added: Record<string, string[]>): PluginChange[] {
  const changes: PluginChange[] = []
  const mirrored = new Set(versions.map((v) => v.version))
  for (const version of upstream) {
    const platforms = added[version.version] ?? []
    if (!mirrored.has(version.version) && platforms.length) changes.push({ version: version.version, action: 'add', platforms })
  }
  for (const version of versions) {
    const stored = new Set(version.stored_platforms.map(platformKey))
    const platforms = (added[version.version] ?? []).filter((p) => !stored.has(p))
    if (!version.revoked && platforms.length) changes.push({ version: version.version, action: 'add', platforms })
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

// The platforms HashiCorp builds Packer plugins for, from the snapshot that
// make platforms refreshes (spec/platforms.json, rendered by make generate).
export const PLUGIN_ARCHITECTURES: readonly string[] = platforms.platforms
export const PLUGIN_OS_GROUPS: readonly (readonly [string, readonly string[]])[] = groupPlatforms(PLUGIN_ARCHITECTURES)

export function groupPlatforms(all: readonly string[]): (readonly [string, readonly string[]])[] {
  const groups = new Map<string, string[]>()
  for (const platform of all) {
    const [os = '', arch = ''] = platform.split('_', 2)
    groups.set(os, [...(groups.get(os) ?? []), arch])
  }
  return [...groups.entries()].map(([os, arches]) => [os, arches] as const)
}

export type PendingItem = { text: string; version: string }

// The pending tray's chips, one per change, worded as the design does.
export function pendingItems(changes: PluginChange[], mirrored: Set<string>): PendingItem[] {
  return changes.map((change) => {
    const platforms = change.platforms ?? []
    return mirrored.has(change.version)
      ? { text: `Add ${platforms.length === 1 ? platforms[0] : `${platforms.length} platforms`} to ${change.version}`, version: change.version }
      : { text: `Add ${change.version} · ${platforms.length} ${platforms.length === 1 ? 'platform' : 'platforms'}`, version: change.version }
  })
}

// Newest first: numeric segments compare as numbers, a prerelease sorts below
// its release, and anything unparseable falls back to string order.
export function compareVersionsDesc(a: string, b: string): number {
  const split = (v: string) => {
    const [core = '', pre] = v.replace(/^v/, '').split('-', 2)
    return { nums: core.split('.').map((n) => Number.parseInt(n, 10)), pre }
  }
  const x = split(a), y = split(b)
  for (let i = 0; i < Math.max(x.nums.length, y.nums.length); i++) {
    const l = x.nums[i] ?? 0, r = y.nums[i] ?? 0
    if (Number.isNaN(l) || Number.isNaN(r)) return b.localeCompare(a)
    if (l !== r) return r - l
  }
  if (x.pre === undefined && y.pre !== undefined) return -1
  if (x.pre !== undefined && y.pre === undefined) return 1
  return (y.pre ?? '').localeCompare(x.pre ?? '')
}
