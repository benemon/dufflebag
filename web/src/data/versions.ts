import { useEffect, useState } from 'react'

import {
  ApiError, getBucket, getVersion, listBucketAncestry, listBuildPackages,
  listChannelAssignmentHistory, listChannels, listEnforcedBlocksByBucket, listSboms, listVersions,
  signOutIfUnauthorized,
  type ApiAncestryStatus, type ApiBucket, type ApiBucketAncestry, type ApiBuild,
  type ApiChannel, type ApiPackage, type ApiVersion, type Tenant as ApiTenant,
} from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { platformTenancyGap, type TenancyGap } from './tenant'
import { scanAttribution, type BuildFindings, type ScanAttribution } from './findings'

export type Artifact = {
  id: string
  externalIdentifier: string
  region: string
}

/**
 * A build's lifecycle state, as a closed set so the screen cannot render a
 * state nobody chose a colour for (the Drift precedent in buckets.ts).
 */
export type BuildState = 'done' | 'running' | 'failed' | 'cancelled' | 'pending'

export type Build = {
  id: string
  component: string
  platform: string
  state: BuildState
  packerRunUUID: string
  sourceExternalIdentifier: string
  labels: Record<string, string>
  artifacts: Artifact[]
  packerVersion: string
  plugins: { name: string; version: string }[]
  runnerOS: string
  arch: string
  options: {
    path: string
    variables: string[]
    variableFiles: string[]
    only: string[]
    except: string[]
    debug: boolean
    force: boolean
  }
  updated: string
  packageInventory:
    | { status: 'not-loaded' }
    | {
        status: 'parsed'
        packages: Package[]
        /**
         * Absent when no scanner is configured or this build has no current
         * successful scan. Its absence is what distinguishes "not examined"
         * from "examined and unaffected".
         */
        scan?: ScanAttribution
      }
    | { status: 'unparseable' }
}

export type Package = {
  name: string
  version: string
  purl: string
  sboms: { id: string; name: string; format: string }[]
  /** Absent when the package carries no vulnerability data at all. */
  findings?: Finding[]
}

export type Finding = {
  identifier: string
  description: string
  /** The derived fixed scale, comparable across providers. */
  criticality: string
  /** The provider's verbatim value, which is NOT comparable across providers. */
  severity: string
  fixedVersion: string
  aliases: string[]
  firstSeen: string
}

/**
 * One version of a bucket, as the screens show it.
 *
 * The name IS the completion state — "v0" until the version completes, "vN"
 * after — and only a complete version can be assigned to a channel
 * (internal/domain/registry/version.go).
 */
export type Version = {
  name: string
  fingerprint: string
  state: 'complete' | 'incomplete' | 'revoked' | 'revocation-scheduled'
  templateType: string
  /** The channels currently pointing at this version — assignment context. */
  channels: string[]
  assignments: AssignmentContext[]
  builds: Build[]
  parents: AncestryParent[]
  children: AncestryChild[]
  created: string
}

export type AssignmentContext = {
  channel: string
  assignedAt: string
  author: string | null
}

export type BucketChannel = {
  name: string
  versionName: string
  fingerprint: string
  managed: boolean
  restricted: boolean
  assignedAt: string
  author: string | null
}

export type ChannelHistoryEntry = {
  versionName: string
  fingerprint: string
  status: 'active' | 'historical'
  parentStatus: 'none' | 'current' | 'out-of-date' | 'unknown'
  author: string | null
  assignedAt: string
}

export type BucketPage = {
  name: string
  description: string
  labels: Record<string, string>
  templateTypes: string[]
  versions: Version[]
  channels: BucketChannel[]
  latestVersion: { name: string; fingerprint: string } | null
  newestVersion: {
    name: string
    fingerprint: string
    state: Version['state']
    created: string
  } | null
  platforms: string[]
  parents: AncestryParent[]
  children: AncestryChild[]
}

export type ParentFreshness =
  | { status: 'newest' }
  | { status: 'behind'; currentVersion: string }
  | { status: 'unknown' }

export type AncestryParent = {
  bucket: string
  versionName: string
  fingerprint: string
  freshness: ParentFreshness
  /** Channels currently on the parent version; only the version screen's load fetches them. */
  channels?: string[]
  /** This bucket's own version in the relation — the one built FROM the parent. */
  localVersionName?: string
}

export type AncestryChild = {
  bucket: string
  versionName: string
  fingerprint: string
  /** Channels currently on the child version; only the version screen's load fetches them. */
  channels?: string[]
  /** This bucket's own version in the relation — the one the child was built FROM. */
  localVersionName?: string
}

export type VersionDetail = {
  version: Version
  channels: BucketChannel[]
}

export type SbomRef = {
  id: string
  name: string
  format: string
}

export type BuildDetail = {
  version: Version
  build: Build
  /** The build's stored SBOMs, from the same listing any client would use. */
  sboms: SbomRef[]
}

/**
 * Versions of one bucket, projected from the compatibility-plane API.
 *
 * Shapes are domain-shaped, not wire-shaped: generated models stay behind the
 * client (docs/architecture.md: wire models are never domain models).
 */
export function useVersions(bucket: string) {
  return useVersionData<BucketPage | null>(
    null,
    (token, tenant) => loadBucketPage(token, tenant, bucket),
    bucket,
  )
}

/** Enforced provisioners are independent so their failure stays local to the Overview row. */
export function useEnforcedProvisioners(bucket: string) {
  return useVersionData<string[]>(
    [],
    (token, tenant) => loadEnforcedProvisioners(token, tenant, bucket),
    `enforced-provisioners/${bucket}`,
  )
}

/** One version by fingerprint, for the drill-down page. */
export function useVersion(bucket: string, fingerprint: string) {
  return useVersionData<VersionDetail | null>(
    null,
    (token, tenant) => loadVersionDetail(token, tenant, bucket, fingerprint),
    bucket + '/' + fingerprint,
    (detail) => detail?.version.builds.some(buildIsInProgress) ?? false,
  )
}

/** One build by id, including its package-inventory status. */
export function useBuild(bucket: string, fingerprint: string, build: string) {
  return useVersionData<BuildDetail | null>(
    null,
    (token, tenant) => loadBuildDetail(token, tenant, bucket, fingerprint, build),
    bucket + '/' + fingerprint + '/' + build,
    (detail) => detail ? buildIsInProgress(detail.build) : false,
  )
}

/** Assignment history is fetched only while its channel row is expanded. */
export function useChannelHistory(bucket: string, channel: string, currentFingerprint: string) {
  return useVersionData<ChannelHistoryEntry[]>(
    [],
    (token, tenant) => loadChannelHistory(
      token, tenant, bucket, channel, currentFingerprint,
    ),
    `${bucket}/${channel}/${currentFingerprint}`,
  )
}

/**
 * The shared fetch-and-state skeleton of the two versions screens: same
 * tenancy gate, same discovery folding, same honest failure handling as
 * useBuckets (duf-tkw).
 */
function useVersionData<T>(
  empty: T,
  load: (token: string, tenant: ApiTenant) => Promise<T>,
  key: string,
  reloadWhile?: (loaded: T) => boolean,
): { data: T; loading: boolean; failure: string | null; gap: TenancyGap | null } {
  const {
    state, selectedOrganization, selectedProject, signOut,
    organizations, organizationsLoading, organizationFailure,
    permittedProjects, projectsLoading, projectFailure,
  } = useAuth()
  const [data, setData] = useState<T>(empty)
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)

  useEffect(() => {
    // Same gate as useBuckets: for a platform session there is no organisation
    // claim to fall back on, and fabricating one would query a tenancy nobody
    // chose (duf-tkw).
    if (!state || !selectedOrganization || !selectedProject) {
      setData(empty)
      setLoading(false)
      setFailure(null)
      return
    }
    let cancelled = false
    setData(empty)
    setLoading(true)
    setFailure(null)
    const tenant = { organizationID: selectedOrganization, projectID: selectedProject }

    let retry: ReturnType<typeof setTimeout> | undefined
    const refresh = () => void load(state.token, tenant)
      .then((loaded) => {
        if (!cancelled) {
          setData(loaded)
          setFailure(null)
          if (reloadWhile?.(loaded)) retry = setTimeout(refresh, 500)
        }
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (signOutIfUnauthorized(err, signOut)) return
        setData(empty)
        setFailure(err instanceof Error ? err.message : 'Could not load versions.')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    refresh()
    return () => {
      cancelled = true
      if (retry) clearTimeout(retry)
    }
    // `key` names what `load` closes over; `load`, `empty`, and `reloadWhile`
    // themselves are fresh values every render and must not retrigger the effect.
  }, [state, selectedOrganization, selectedProject, signOut, key])

  const platform = state !== null && state.claims.organizationID === null
  const aboveProjects = state !== null && state.claims.projectID === null
  const discovering = aboveProjects && (organizationsLoading || projectsLoading)
  const discoveryFailure = aboveProjects ? (organizationFailure ?? projectFailure) : null
  return {
    data,
    loading: loading || discovering,
    failure: failure ?? discoveryFailure,
    gap:
      aboveProjects && !discovering && !discoveryFailure
        ? platformTenancyGap({
            platform,
            organizationCount: organizations.length,
            selectedOrganization,
            projectCount: permittedProjects.length,
            selectedProject: selectedProject || null,
          })
        : null,
  }
}

function buildIsInProgress(build: Build): boolean {
  return build.state === 'pending' || build.state === 'running'
}

export async function loadVersions(
  token: string,
  tenant: ApiTenant,
  bucket: string,
): Promise<Version[]> {
  // The versions and the bucket's channels together: the channel pill on each
  // row is assignment context, from the same ListChannels any client would use.
  const [versions, channels] = await Promise.all([
    listVersions(token, tenant, bucket),
    listChannels(token, tenant, bucket),
  ])
  return versions.map((version) => toVersion(version, channels))
}

export async function loadEnforcedProvisioners(
  token: string,
  tenant: ApiTenant,
  bucket: string,
): Promise<string[]> {
  const blocks = await listEnforcedBlocksByBucket(token, tenant, bucket)
  return blocks.map((block) => block.name?.trim() || block.id?.trim() || 'Unknown provisioner')
}

export async function loadBucketPage(
  token: string,
  tenant: ApiTenant,
  bucketName: string,
): Promise<BucketPage> {
  const [bucket, versions, channels] = await Promise.all([
    getBucket(token, tenant, bucketName),
    listVersions(token, tenant, bucketName),
    listChannels(token, tenant, bucketName),
  ])
  const projected = versions.map((version) => toVersion(version, channels))
  await Promise.all(projected.map(async (version) => {
    const relations = await loadVersionRelations(token, tenant, bucketName, version.fingerprint)
    version.parents = relations.parents
    version.children = relations.children
  }))
  // The bucket card states the WHOLE bucket's ancestry, so it aggregates the
  // per-version relations fetched above rather than calling the bucket-level
  // listing — whose parents arm the server scopes to the newest complete
  // version, which is exactly the reading that hid older-version ancestry
  // (duf-okej.11). No extra requests: every version's relations are already
  // here, each carrying the local version it belongs to.
  const parents = dedupeRelations(projected.flatMap((version) => version.parents ?? []))
  const children = dedupeRelations(projected.flatMap((version) => version.children ?? []))
  const newest = projected[0] ?? null
  return {
    ...toBucketHeader(bucket),
    templateTypes: [...new Set(projected.map((version) => version.templateType).filter(Boolean))].sort(),
    versions: projected,
    channels: channels.map(toBucketChannel),
    newestVersion: newest ? {
      name: newest.name,
      fingerprint: newest.fingerprint,
      state: newest.state,
      created: newest.created,
    } : null,
    platforms: bucket.platforms ?? [],
    parents,
    children,
  }
}

/** One entry per (related version, local version) edge, however many builds recorded it. */
function dedupeRelations<T extends { bucket: string; fingerprint: string; localVersionName?: string }>(
  relations: T[],
): T[] {
  const seen = new Set<string>()
  return relations.filter((relation) => {
    const key = `${relation.bucket}/${relation.fingerprint}/${relation.localVersionName ?? ''}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

export async function loadChannelHistory(
  token: string,
  tenant: ApiTenant,
  bucket: string,
  channel: string,
  currentFingerprint: string,
): Promise<ChannelHistoryEntry[]> {
  const history = await listChannelAssignmentHistory(token, tenant, bucket, channel)
  return history.map((assignment, index) => ({
    versionName: assignment.version?.name ?? '—',
    fingerprint: assignment.version?.fingerprint ?? '',
    status:
      index === 0 && !!currentFingerprint &&
      assignment.version?.fingerprint === currentFingerprint
        ? 'active'
        : 'historical',
    parentStatus: assignmentParentStatus(assignment.version),
    author: knownAuthor(assignment.author_id),
    assignedAt: formatCreated(assignment.assigned_at),
  }))
}

export async function loadVersion(
  token: string,
  tenant: ApiTenant,
  bucket: string,
  fingerprint: string,
): Promise<Version> {
  const [version, channels] = await Promise.all([
    getVersion(token, tenant, bucket, fingerprint),
    listChannels(token, tenant, bucket),
  ])
  return toVersion(version, channels)
}

export async function loadVersionDetail(
  token: string,
  tenant: ApiTenant,
  bucket: string,
  fingerprint: string,
): Promise<VersionDetail> {
  const [version, channels, relations] = await Promise.all([
    getVersion(token, tenant, bucket, fingerprint),
    listChannels(token, tenant, bucket),
    loadVersionRelations(token, tenant, bucket, fingerprint),
  ])
  const related = await relatedBucketChannels(token, tenant, relations, bucket, channels)
  const projected = toVersion(version, channels)
  projected.parents = relations.parents.map((parent) => withRelationChannels(parent, related))
  projected.children = relations.children.map((child) => withRelationChannels(child, related))
  projected.builds = await Promise.all(projected.builds.map(async (build) => ({
    ...build,
    packageInventory: await loadPackageInventory(token, tenant, bucket, fingerprint, build.id),
  })))
  return {
    version: projected,
    channels: channels.map(toBucketChannel),
  }
}

async function loadVersionRelations(
  token: string,
  tenant: ApiTenant,
  bucket: string,
  fingerprint: string,
): Promise<{ parents: AncestryParent[]; children: AncestryChild[] }> {
  const [parents, children] = await Promise.all([
    listBucketAncestry(token, tenant, bucket, 'ANCESTRY_TYPE_PARENTS', fingerprint),
    listBucketAncestry(token, tenant, bucket, 'ANCESTRY_TYPE_CHILDREN', fingerprint),
  ])
  return {
    parents: parents.map(toAncestryParent).filter((parent): parent is AncestryParent => parent !== null),
    children: children.map(toAncestryChild).filter((child): child is AncestryChild => child !== null),
  }
}

/**
 * Channel lists for every bucket the relations point at, one ListChannels per
 * bucket, reusing this bucket's already-fetched list. A related bucket whose
 * channels cannot be read (a deleted ancestor) yields null so its entries stay
 * channel-less instead of failing the whole screen.
 */
async function relatedBucketChannels(
  token: string,
  tenant: ApiTenant,
  relations: { parents: AncestryParent[]; children: AncestryChild[] },
  bucket: string,
  channels: ApiChannel[],
): Promise<Map<string, ApiChannel[] | null>> {
  const known = new Map<string, ApiChannel[] | null>([[bucket, channels]])
  const missing = [...new Set(
    [...relations.parents, ...relations.children].map((entry) => entry.bucket),
  )].filter((name) => !known.has(name))
  await Promise.all(missing.map(async (name) => {
    known.set(name, await listChannels(token, tenant, name).catch((err: unknown) => {
      // A dead session must still reach the sign-out path; only an unreadable
      // bucket (deleted ancestor answering 404) degrades to channel-less rows.
      if (err instanceof ApiError && err.status === 401) throw err
      return null
    }))
  }))
  return known
}

function withRelationChannels<T extends { bucket: string; fingerprint: string }>(
  entry: T,
  channelsByBucket: Map<string, ApiChannel[] | null>,
): T {
  const channels = channelsByBucket.get(entry.bucket)
  if (!channels) return entry
  return {
    ...entry,
    channels: channels
      .filter((channel) => channel.version?.fingerprint === entry.fingerprint)
      .map((channel) => channel.name),
  }
}

export async function loadBuildDetail(
  token: string,
  tenant: ApiTenant,
  bucket: string,
  fingerprint: string,
  buildID: string,
): Promise<BuildDetail> {
  const [version, packageInventory] = await Promise.all([
    getVersion(token, tenant, bucket, fingerprint),
    loadPackageInventory(token, tenant, bucket, fingerprint, buildID),
  ])
  const projected = toVersion(version, [])
  const build = projected.builds.find((candidate) => candidate.id === buildID)
  if (!build) throw new Error('Build not found.')
  // Fetched only once the build settles: an in-progress build screen re-polls
  // this whole loader every half second, and hammering the SBOM listing for a
  // document set that becomes interesting at completion buys nothing. The
  // card appears when the build does finish.
  const sboms = buildIsInProgress(build)
    ? []
    : await listSboms(token, tenant, bucket, fingerprint, buildID)
  return {
    version: projected,
    build: { ...build, packageInventory },
    sboms: sboms.map((sbom) => ({
      id: sbom.id ?? '', name: sbom.name ?? '', format: sbom.format ?? '',
    })),
  }
}

async function loadPackageInventory(
  token: string,
  tenant: ApiTenant,
  bucket: string,
  fingerprint: string,
  build: string,
): Promise<Build['packageInventory']> {
  try {
    const { packages, headers } = await listBuildPackages(token, tenant, bucket, fingerprint, build)
    const scan = scanAttribution(headers)
    return { status: 'parsed', packages: packages.map(toPackage), ...(scan ? { scan } : {}) }
  } catch (err: unknown) {
    // This is the endpoint's deliberate distinction: 422 means at least one
    // client-supplied SBOM could not be parsed. Calling that zero packages
    // would turn an unknown inventory into an inspected-and-empty one.
    if (err instanceof ApiError && err.status === 422) return { status: 'unparseable' }
    throw err
  }
}

/**
 * Whether a version completed, from the wire's own vocabulary: renderVersion
 * (internal/compat/hcp2023/handler.go) sets VERSION_ACTIVE if and only if the
 * version completed and is not revoked; a revoked or revocation-scheduled
 * version keeps its number but reports the revocation status instead, so for
 * those two completion is carried only by the name — the wire's own collapse,
 * v0 meaning incomplete (the rule packer's IsVersionComplete reads).
 *
 * The ONE completeness rule for every screen — drift in buckets.ts measures
 * against it too, so it must not be re-derived in a second place.
 */
export function isComplete(version: ApiVersion): boolean {
  if (version.status === 'VERSION_ACTIVE') return true
  return (version.status === 'VERSION_REVOKED' || version.status === 'VERSION_REVOCATION_SCHEDULED')
    && version.name !== 'v0'
}

/** The console's one projection of the wire status into a row state. */
export function versionState(version: ApiVersion): Version['state'] {
  switch (version.status) {
    case 'VERSION_REVOKED':
      return 'revoked'
    case 'VERSION_REVOCATION_SCHEDULED':
      return 'revocation-scheduled'
    default:
      return isComplete(version) ? 'complete' : 'incomplete'
  }
}

/** Projects the API's vocabulary into what the screens display. */
function toVersion(
  version: ApiVersion,
  channels: ApiChannel[],
): Version {
  const assignedChannels = channels.filter(
    (channel) => !!version.fingerprint && channel.version?.fingerprint === version.fingerprint,
  )
  return {
    name: version.name ?? '',
    fingerprint: version.fingerprint ?? '',
    state: versionState(version),
    templateType: version.template_type ?? '',
    // The channel's assignment is the nested version object renderChannel
    // emits; the guard keeps a fingerprint-less version from "matching" every
    // unassigned channel.
    channels: assignedChannels.map((channel) => channel.name),
    assignments: assignedChannels.map((channel) => ({
      channel: channel.name,
      assignedAt: formatCreated(channel.updated_at),
      author: knownAuthor(channel.author_id),
    })),
    builds: (version.builds ?? []).map(toBuild),
    parents: [],
    children: [],
    created: formatCreated(version.created_at),
  }
}

function toBucketHeader(
  bucket: ApiBucket,
): Pick<BucketPage, 'name' | 'description' | 'labels' | 'latestVersion'> {
  return {
    name: bucket.name,
    description: bucket.description ?? '',
    labels: bucket.labels ?? {},
    latestVersion: bucket.latest_version?.fingerprint
      ? {
          name: bucket.latest_version.name ?? '—',
          fingerprint: bucket.latest_version.fingerprint,
        }
      : null,
  }
}

function toBucketChannel(channel: ApiChannel): BucketChannel {
  const assigned = !!channel.version?.fingerprint
  return {
    name: channel.name,
    versionName: channel.version?.name ?? '—',
    fingerprint: channel.version?.fingerprint ?? '',
    managed: channel.managed ?? false,
    restricted: channel.restricted ?? false,
    assignedAt: assigned ? formatCreated(channel.updated_at) : '—',
    author: assigned ? knownAuthor(channel.author_id) : null,
  }
}

function knownAuthor(author?: string): string | null {
  return author?.trim() || null
}

function assignmentParentStatus(version?: ApiVersion | null): ChannelHistoryEntry['parentStatus'] {
  if (!version?.parents) return 'none'
  switch (version.parents.status) {
    case 'UP_TO_DATE':
      return 'current'
    case 'OUT_OF_DATE':
      return 'out-of-date'
    case 'UNDETERMINED':
    case undefined:
      return 'unknown'
  }
}

function toAncestryParent(relation: ApiBucketAncestry): AncestryParent | null {
  if (!relation.parent?.bucket_name || !relation.parent.version_fingerprint) return null
  return {
    bucket: relation.parent.bucket_name,
    versionName: relation.parent.version_name ?? '—',
    fingerprint: relation.parent.version_fingerprint,
    freshness: parentFreshness(relation.status, relation.parent.channel_version?.name),
    localVersionName: relation.child?.version_name,
  }
}

function parentFreshness(status: ApiAncestryStatus | undefined, currentVersion?: string): ParentFreshness {
  if (status === 'UP_TO_DATE') return { status: 'newest' }
  if (status === 'OUT_OF_DATE' && currentVersion) {
    return { status: 'behind', currentVersion }
  }
  return { status: 'unknown' }
}

function toAncestryChild(relation: ApiBucketAncestry): AncestryChild | null {
  if (!relation.child?.bucket_name || !relation.child.version_fingerprint) return null
  return {
    bucket: relation.child.bucket_name,
    versionName: relation.child.version_name ?? '—',
    fingerprint: relation.child.version_fingerprint,
    localVersionName: relation.parent?.version_name,
  }
}

function toPackage(pkg: ApiPackage): Package {
  const findings = toFindings(pkg)
  return {
    name: pkg.name ?? '—',
    version: pkg.version ?? '—',
    purl: pkg.purl ?? '',
    sboms: (pkg.sboms ?? []).map((sbom) => ({
      id: sbom.id ?? '',
      name: sbom.name ?? '',
      format: sbom.format ?? '',
    })),
    ...(findings.length ? { findings } : {}),
  }
}

/**
 * Flattens the frozen nested shape into rows the table renders. Aliases and
 * related ids arrive as the wire's comma-separated strings.
 */
function toFindings(pkg: ApiPackage): Finding[] {
  const findings: Finding[] = []
  for (const detail of pkg.vuln_details ?? []) {
    for (const vulnerability of detail.vulnerabilities ?? []) {
      findings.push({
        identifier: vulnerability.identifier ?? '',
        description: vulnerability.description ?? '',
        criticality: (vulnerability.criticality ?? 'unknown').toLowerCase(),
        severity: vulnerability.severity ?? '',
        fixedVersion: vulnerability.fixed_version ?? '',
        // Aliases travel in the frozen wire's refers_to; related carries a
        // different relation (adjacent advisories, e.g. CGA container ids)
        // and must not masquerade as an alias (duf-o0ou.7 smoke finding).
        aliases: (vulnerability.refers_to ?? '')
          .split(',')
          .map((alias) => alias.trim())
          .filter(Boolean),
        firstSeen: vulnerability.first_seen_at ?? '',
      })
    }
  }
  return findings
}

/** "2026-07-27 09:14 UTC" — the timestamps the server emits are always UTC. */
function formatCreated(iso?: string): string {
  if (!iso) return '—'
  return `${iso.slice(0, 10)} ${iso.slice(11, 16)} UTC`
}

function toBuild(build: ApiBuild): Build {
  const packer = build.metadata?.packer
  const options = packer?.options
  return {
    id: build.id ?? '',
    component: build.component_type ?? '',
    platform: build.platform ?? '',
    state: buildState(build.status),
    packerRunUUID: build.packer_run_uuid ?? '',
    sourceExternalIdentifier: build.source_external_identifier ?? '',
    labels: build.labels ?? {},
    artifacts: (build.artifacts ?? []).map((artifact) => ({
      id: artifact.id ?? '',
      externalIdentifier: artifact.external_identifier ?? '',
      region: artifact.region ?? '',
    })),
    packerVersion: packer?.version ?? '',
    plugins: (packer?.plugins ?? []).map((plugin) => ({
      name: plugin.name ?? '',
      version: plugin.version ?? '',
    })),
    runnerOS: packer?.os?.type ?? '',
    arch: packer?.os?.details?.arch ?? packer?.os?.arch ?? '',
    options: {
      path: options?.path ?? '',
      variables: options?.vars?.filter((name): name is string => typeof name === 'string') ?? [],
      variableFiles:
        options?.['var-files']?.filter((path): path is string => typeof path === 'string') ?? [],
      only: optionValues(options?.only),
      except: optionValues(options?.except),
      debug: options?.debug === true,
      force: options?.force === true,
    },
    updated: formatCreated(build.updated_at),
    packageInventory: { status: 'not-loaded' },
  }
}

function optionValues(value?: string | string[]): string[] {
  if (typeof value === 'string') return value ? [value] : []
  return value?.filter((item): item is string => typeof item === 'string' && item !== '') ?? []
}

function buildState(status?: string): BuildState {
  switch (status) {
    case 'BUILD_DONE':
      return 'done'
    case 'BUILD_RUNNING':
      return 'running'
    case 'BUILD_FAILED':
      return 'failed'
    case 'BUILD_CANCELLED':
      return 'cancelled'
    default:
      // BUILD_UNSET: created but not yet reporting, which is how the server
      // stores a build whose status was never sent (domainBuildStatus).
      return 'pending'
  }
}

/**
 * Package inventories for every build of a version, so the version can report
 * findings once rather than per build.
 *
 * One request set per build: the compatibility plane exposes packages per
 * build and has no per-version aggregate, so this is the honest cost of a
 * version-level answer.
 */
export function useVersionFindings(
  bucket: string, fingerprint: string,
  builds: { id: string; platform: string; component: string }[],
) {
  const key = builds.map((build) => build.id).join(',')
  return useVersionData<BuildFindings[]>(
    [],
    async (token, tenant) =>
      Promise.all(
        builds.map(async (build) => {
          const { packages, headers } = await listBuildPackages(
            token, tenant, bucket, fingerprint, build.id,
          )
          const scan = scanAttribution(headers)
          return {
            buildID: build.id,
            platform: build.platform || 'unknown',
            component: build.component,
            packages: packages.map(toPackage),
            scanned: packages.length,
            ...(scan ? { scan } : {}),
          }
        }),
      ),
    `${bucket}/${fingerprint}/${key}`,
  )
}
