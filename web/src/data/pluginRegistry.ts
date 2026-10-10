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

export type Plugin = {
  name: string
  source: PluginSource
  published_versions: number
  newest_version?: string
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

export type ImportPlatformOutcome = { platform: string; outcome: 'imported' | 'failed'; error?: string }

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
  created_at: string
  finished_at?: string
  outcomes: ImportVersionOutcome[]
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

// What the detail grid's edit mode has changed: a row's selection (absent
// means unchanged) and the platforms ticked on it.
export type PluginEdit = { selected: Record<string, boolean>; added: Record<string, string[]> }

const platformKey = (p: PluginPlatform) => `${p.os}_${p.arch}`

// A mirrored version is revoked by deselecting it and restored by selecting
// it; platforms are added only to a version that stays served, and an
// upstream version not yet mirrored is added with the platforms ticked.
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
