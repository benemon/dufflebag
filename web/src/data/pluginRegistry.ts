import { platformDelete, platformGet, platformPost, platformPutForm } from '../api/client'

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

export type UploadPlan<F extends NamedFile = File> = ReadyUpload<F> | { kind: 'refused'; reason: string }

// Names follow the two shapes Packer's getter accepts: goreleaser
// (packer-plugin-NAME_vVERSION_xAPI_OS_ARCH.zip) and release-site
// (packer-plugin-NAME_VERSION_OS_ARCH.zip). The server verifies everything;
// this only groups files so the operator sees what will be sent.
const sumsName = /^packer-plugin-([a-z0-9-]+)_v?([0-9][^_]*)_SHA256SUMS$/
const zipName = /^packer-plugin-([a-z0-9-]+)_v?([0-9][^_]*?)(?:_x[0-9]+\.[0-9]+)?_([a-z0-9]+)_([a-z0-9]+)\.zip$/
const manifestName = /^packer-plugin-([a-z0-9-]+)_v?([0-9][^_]*)_manifest\.json$/

export function planPluginUpload<F extends NamedFile>(files: F[]): UploadPlan<F> {
  const planned: ReadyUpload<F>['files'] = []
  const versions = new Set<string>()
  const names = new Set<string>()
  const note = (match: RegExpMatchArray) => {
    const [, name = '', version = ''] = match
    names.add(name)
    versions.add(version)
  }
  for (const file of files) {
    const sums = file.name.match(sumsName)
    const manifest = file.name.match(manifestName)
    const zip = file.name.match(zipName)
    if (file.name.endsWith('_SHA256SUMS.sig') && sumsName.test(file.name.slice(0, -4))) {
      planned.push({ field: 'sha256sums_sig', file })
    } else if (sums) {
      if (planned.some((p) => p.field === 'sha256sums')) return { kind: 'refused', reason: 'Choose one SHA256SUMS file.' }
      note(sums)
      planned.push({ field: 'sha256sums', file })
    } else if (manifest) {
      note(manifest)
      planned.push({ field: 'manifest', file })
    } else if (zip) {
      note(zip)
      const [, , , os = '', arch = ''] = zip
      planned.push({ field: 'zips', file, platform: `${os}_${arch}` })
    } else {
      return { kind: 'refused', reason: `${file.name} is not a SHA256SUMS file, signature, manifest or plugin zip.` }
    }
  }
  if (!planned.some((p) => p.field === 'sha256sums')) return { kind: 'refused', reason: 'Include the version’s SHA256SUMS file.' }
  if (!planned.some((p) => p.field === 'zips')) return { kind: 'refused', reason: 'Include at least one plugin zip.' }
  if (names.size > 1) return { kind: 'refused', reason: `Files name more than one plugin: ${[...names].join(', ')}.` }
  if (versions.size > 1) return { kind: 'refused', reason: `Files span versions ${[...versions].join(', ')}. Upload one version at a time.` }
  const [name = ''] = names
  const [version = ''] = versions
  return { kind: 'ready', name, version, files: planned }
}

function versionPath(organizationID: string, name: string, version: string, action = ''): string {
  const base = `plugins/${encodeURIComponent(name)}/versions/${encodeURIComponent(version)}`
  return path(organizationID, action === '' ? base : `${base}/${action}`)
}

export async function revokePluginVersion(token: string, organizationID: string, name: string, version: string): Promise<void> {
  await platformPost<null>(token, versionPath(organizationID, name, version, 'revoke'))
}

export async function restorePluginVersion(token: string, organizationID: string, name: string, version: string): Promise<void> {
  await platformPost<null>(token, versionPath(organizationID, name, version, 'restore'))
}

export async function deletePluginVersion(token: string, organizationID: string, name: string, version: string): Promise<void> {
  await platformDelete(token, versionPath(organizationID, name, version))
}
