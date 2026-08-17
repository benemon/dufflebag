/**
 * Calls to the compatibility plane, carrying the console's bearer token.
 *
 * The console is a client of the same API the CLI uses (ADR-0006), so there is
 * no console-only endpoint to keep in step and no second authorization model.
 * URLs are relative: same origin in production, proxied in development.
 */

import type { Role } from '../auth/permissions'

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly reason?: string,
  ) {
    super(message)
  }
}

// A 401 mid-session means the token expired under the operator, which is the
// one arrival at sign-in that deserves an explanation (duf-1cn) — so the
// reason travels with the bounce.
export function signOutIfUnauthorized(
  error: unknown, signOut: (reason: 'expired' | 'requested') => void,
): boolean {
  if (!(error instanceof ApiError) || error.status !== 401) return false
  signOut('expired')
  return true
}

const PACKER_BASE = '/packer/2023-01-01'
const RESOURCE_MANAGER_BASE = '/resource-manager/2019-12-10'

/**
 * The platform plane — ours, not HCP's (ADR-0007).
 *
 * The only surface the console reaches that Packer does not, because managing
 * principals has no compatibility equivalent. Everything else the console does
 * goes through the same endpoints the CLI uses (ADR-0006).
 */
const PLATFORM_BASE = '/api/v1'

async function request<T>(
  token: string,
  method: string,
  path: string,
  body?: unknown,
): Promise<T | null> {
  const response = await fetch(path, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  })
  if (!response.ok) {
    // The server answers not-found for a tenancy the caller may not see, so a
    // 404 means "nothing you can reach", not necessarily "does not exist".
    // 403 means the opposite: visible, but your role is insufficient (ADR-0017).
    const failure = await failureMessage(response, path)
    throw new ApiError(response.status, failure.message, failure.reason)
  }
  if (response.status === 204) return null
  return (await response.json()) as T
}

/**
 * Prefers the server's own message over a synthesised one.
 *
 * A refusal explains itself — "the principal's role does not permit this
 * operation" is worth showing; "403 from /api/v1/principals" is not. Internal
 * errors deliberately carry only a correlation id, so this surfaces that too and
 * the operator can find the detail server-side.
 */
async function failureMessage(
  response: Response,
  path: string,
): Promise<{ message: string; reason?: string }> {
  try {
    const body = (await response.json()) as { message?: string; reason?: string }
    if (body.message) return { message: body.message, reason: body.reason }
  } catch {
    // Not JSON, or empty. Fall through to the status line.
  }
  return { message: `${response.status} from ${path}` }
}

async function get<T>(token: string, path: string): Promise<T> {
  return (await request<T>(token, 'GET', path)) as T
}

/**
 * A GET that also surfaces the response headers, for the one case where the
 * frozen JSON has no room for what the server needs to say.
 */
async function getWithHeaders<T>(
  token: string,
  path: string,
): Promise<{ body: T | null; headers: Headers }> {
  const response = await fetch(path, {
    method: 'GET',
    headers: { Accept: 'application/json', Authorization: `Bearer ${token}` },
  })
  if (!response.ok) {
    const failure = await failureMessage(response, path)
    throw new ApiError(response.status, failure.message, failure.reason)
  }
  if (response.status === 204) return { body: null, headers: response.headers }
  return { body: (await response.json()) as T, headers: response.headers }
}

export type Tenant = { organizationID: string; projectID: string }

export type ApiProject = {
  id: string
  name: string
  /** Load-bearing: an unpinned CLI selects the oldest, so order matters. */
  created_at: string
}

export async function listProjects(token: string, organizationID: string): Promise<ApiProject[]> {
  const body = await get<{ projects?: ApiProject[] }>(
    token,
    `${RESOURCE_MANAGER_BASE}/projects?scope.id=${encodeURIComponent(organizationID)}`,
  )
  return body.projects ?? []
}

export type ApiBucket = {
  /** Unique identifier (ULID) — the id the platform's scope claims speak. */
  id?: string
  name: string
  description?: string
  labels?: Record<string, string>
  created_at?: string
  updated_at?: string
  latest_version?: { name?: string; fingerprint?: string } | null
  platforms?: string[] | null
  parents?: ApiAncestryLink | null
  children?: ApiAncestryLink | null
}

export type ApiAncestryStatus = 'UNDETERMINED' | 'UP_TO_DATE' | 'OUT_OF_DATE'

export type ApiAncestryLink = {
  href?: string
  status?: ApiAncestryStatus
}

export async function listBuckets(token: string, tenant: Tenant): Promise<ApiBucket[]> {
  const body = await get<{ buckets?: ApiBucket[] }>(token, `${packerPath(tenant)}/buckets`)
  return body.buckets ?? []
}

export async function createBucket(
  token: string,
  tenant: Tenant,
  name: string,
): Promise<ApiBucket> {
  const body = await request<{ bucket?: ApiBucket }>(
    token,
    'PUT',
    `${packerPath(tenant)}/buckets`,
    { name },
  )
  return body?.bucket ?? { name }
}

export async function getBucket(token: string, tenant: Tenant, bucket: string): Promise<ApiBucket> {
  const body = await get<{ bucket?: ApiBucket }>(
    token,
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}`,
  )
  return body.bucket ?? { name: bucket }
}

export async function deleteBucket(
  token: string,
  tenant: Tenant,
  bucket: string,
): Promise<void> {
  await request(
    token,
    'DELETE',
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}`,
  )
}

export type ApiEnforcedBlockDetail = {
  id?: string
  name?: string
}

export async function listEnforcedBlocksByBucket(
  token: string,
  tenant: Tenant,
  bucket: string,
): Promise<ApiEnforcedBlockDetail[]> {
  const body = await get<{ enforced_block_detail?: ApiEnforcedBlockDetail[] }>(
    token,
    `${packerPath(tenant)}/enforced_blocks/bucket/${encodeURIComponent(bucket)}`,
  )
  return body.enforced_block_detail ?? []
}

export type ApiBuild = {
  id?: string
  component_type?: string
  platform?: string
  status?: string
  packer_run_uuid?: string
  source_external_identifier?: string
  labels?: Record<string, string>
  artifacts?: { id?: string; external_identifier?: string; region?: string }[] | null
  metadata?: {
    packer?: {
      version?: string
      options?: {
        path?: string
        vars?: string[] | null
        'var-files'?: string[] | null
        only?: string | string[] | null
        except?: string | string[] | null
        debug?: boolean
        force?: boolean
      } | null
      os?: {
        type?: string
        details?: { arch?: string; version?: string }
        /** Older and hand-authored clients have reported these flat. */
        arch?: string
        version?: string
      }
      plugins?: { name?: string; version?: string }[]
    }
  } | null
  updated_at?: string
}

export type ApiVersion = {
  id?: string
  name?: string
  fingerprint?: string
  status?: string
  template_type?: string
  created_at?: string
  builds?: ApiBuild[] | null
  has_descendants?: boolean
  parents?: {
    status?: 'UNDETERMINED' | 'UP_TO_DATE' | 'OUT_OF_DATE'
  } | null
}

export async function listVersions(
  token: string,
  tenant: Tenant,
  bucket: string,
): Promise<ApiVersion[]> {
  const body = await get<{ versions?: ApiVersion[] }>(
    token,
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}/versions`,
  )
  return body.versions ?? []
}

export async function getVersion(
  token: string,
  tenant: Tenant,
  bucket: string,
  fingerprint: string,
): Promise<ApiVersion> {
  const body = await get<{ version?: ApiVersion }>(
    token,
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}/versions/${encodeURIComponent(fingerprint)}`,
  )
  return body.version ?? {}
}

export type RevokeVersionOptions = {
  revoke_at: string
  revocation_message?: string
  skip_descendants_revocation?: true
  disable_rollback_channels?: true
}

export async function revokeVersion(
  token: string,
  tenant: Tenant,
  bucket: string,
  fingerprint: string,
  options: RevokeVersionOptions,
): Promise<void> {
  const message = options.revocation_message?.trim()
  await request(
    token,
    'PATCH',
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}` +
      `/versions/${encodeURIComponent(fingerprint)}`,
    {
      revoke_at: options.revoke_at,
      ...(message ? { revocation_message: message } : {}),
      ...(options.skip_descendants_revocation ? { skip_descendants_revocation: true } : {}),
      ...(options.disable_rollback_channels ? { disable_rollback_channels: true } : {}),
    },
  )
}

export async function restoreVersion(
  token: string,
  tenant: Tenant,
  bucket: string,
  fingerprint: string,
): Promise<void> {
  await request(
    token,
    'PATCH',
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}` +
      `/versions/${encodeURIComponent(fingerprint)}`,
    { restore: true },
  )
}

export async function deleteVersion(
  token: string,
  tenant: Tenant,
  bucket: string,
  fingerprint: string,
): Promise<void> {
  await request(
    token,
    'DELETE',
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}` +
      `/versions/${encodeURIComponent(fingerprint)}`,
  )
}

export type ApiVulnerability = {
  identifier?: string
  description?: string
  criticality?: string
  severity?: string
  fixed_version?: string
  related?: string
  refers_to?: string
  first_seen_at?: string
  published_at?: string
  withdrawn_at?: string
}

export type ApiVulnDetails = {
  last_scanned_at?: string
  vulnerabilities?: ApiVulnerability[] | null
}

export type ApiPackage = {
  name?: string
  version?: string
  purl?: string
  sboms?: { id?: string; name?: string; format?: string }[] | null
  /**
   * ABSENT, not empty, when the deployment has no scanner or the build has no
   * current successful scan. An empty array would claim the package was
   * examined and found unaffected.
   */
  vuln_details?: ApiVulnDetails[] | null
}

/**
 * All packages reported for one build.
 *
 * The compatibility endpoint pages at 100 by default. Package count is a UI
 * fact, so stopping at the first page would silently turn "at least 100" into
 * an exact count. Follow the server's own opaque tokens until it says there is
 * no next page.
 */
export async function listBuildPackages(
  token: string,
  tenant: Tenant,
  bucket: string,
  fingerprint: string,
  build: string,
): Promise<{ packages: ApiPackage[]; headers: Headers }> {
  const packages: ApiPackage[] = []
  let headers = new Headers()
  let next = ''
  do {
    const query = new URLSearchParams({ 'pagination.page_size': '100' })
    if (next) query.set('pagination.next_page_token', next)
    const response = await getWithHeaders<{
      packages?: ApiPackage[]
      pagination?: { next_page_token?: string } | null
    }>(
      token,
      `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}` +
        `/versions/${encodeURIComponent(fingerprint)}/builds/${encodeURIComponent(build)}` +
        `/packages?${query}`,
    )
    // Scan attribution rides on the response headers because the frozen
    // package JSON has nowhere to carry it. Every page reports the same run,
    // so the first page's headers stand for the inventory.
    if (!next) headers = response.headers
    packages.push(...(response.body?.packages ?? []))
    next = response.body?.pagination?.next_page_token ?? ''
  } while (next)
  return { packages, headers }
}

/**
 * Shape from the producer: renderChannel in internal/compat/hcp2023/handler.go
 * emits the assignment as a full nested `version` object (omitted when
 * unassigned) plus `managed` for the server-maintained "latest" — there is no
 * flat version_fingerprint field on the wire
 * (models/hashicorp_cloud_packer20230101_channel.go).
 */
export type ApiChannel = {
  name: string
  version?: { name?: string; fingerprint?: string } | null
  author_id?: string
  managed?: boolean
  restricted?: boolean
  updated_at?: string
}

export type ApiChannelAssignment = {
  assigned_at?: string
  author_id?: string
  version?: ApiVersion | null
}

export async function listChannels(
  token: string,
  tenant: Tenant,
  bucket: string,
): Promise<ApiChannel[]> {
  const body = await get<{ channels?: ApiChannel[] }>(
    token,
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}/channels`,
  )
  return body.channels ?? []
}

export async function createChannel(
  token: string,
  tenant: Tenant,
  bucket: string,
  options: { name: string; restricted?: boolean; fingerprint?: string },
): Promise<void> {
  await request(
    token,
    'POST',
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}/channels`,
    {
      name: options.name,
      ...(options.restricted === undefined ? {} : { restricted: options.restricted }),
      ...(options.fingerprint === undefined
        ? {}
        : { version_fingerprint: options.fingerprint }),
    },
  )
}

export async function deleteChannel(
  token: string,
  tenant: Tenant,
  bucket: string,
  channel: string,
): Promise<void> {
  await request(
    token,
    'DELETE',
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}` +
      `/channels/${encodeURIComponent(channel)}`,
  )
}

export async function assignChannelVersion(
  token: string,
  tenant: Tenant,
  bucket: string,
  channel: string,
  fingerprint: string,
): Promise<void> {
  await request(
    token,
    'PATCH',
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}` +
      `/channels/${encodeURIComponent(channel)}`,
    { update_mask: 'versionFingerprint', version_fingerprint: fingerprint },
  )
}

export async function listChannelAssignmentHistory(
  token: string,
  tenant: Tenant,
  bucket: string,
  channel: string,
): Promise<ApiChannelAssignment[]> {
  const body = await get<{ history?: ApiChannelAssignment[] }>(
    token,
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}` +
      `/channels/${encodeURIComponent(channel)}/history`,
  )
  return body.history ?? []
}

export type ApiBucketAncestry = {
  status?: ApiAncestryStatus
  parent?: {
    bucket_name?: string
    channel_name?: string
    version_fingerprint?: string
    version_name?: string
    channel_version?: {
      fingerprint?: string
      name?: string
    } | null
  } | null
  child?: {
    bucket_name?: string
    version_fingerprint?: string
    version_name?: string
  } | null
}

export async function listBucketAncestry(
  token: string,
  tenant: Tenant,
  bucket: string,
  type: 'ANCESTRY_TYPE_PARENTS' | 'ANCESTRY_TYPE_CHILDREN',
  fingerprint?: string,
): Promise<ApiBucketAncestry[]> {
  const query = new URLSearchParams({ type })
  if (fingerprint) query.set('version_fingerprint', fingerprint)
  const body = await get<{ relations?: ApiBucketAncestry[] }>(
    token,
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}/ancestry?${query}`,
  )
  return body.relations ?? []
}

export type ApiSbomRef = {
  id?: string
  name?: string
  format?: string
}

export async function listSboms(
  token: string,
  tenant: Tenant,
  bucket: string,
  fingerprint: string,
  build: string,
): Promise<ApiSbomRef[]> {
  const body = await get<{ sboms?: ApiSbomRef[] }>(
    token,
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}` +
      `/versions/${encodeURIComponent(fingerprint)}/builds/${encodeURIComponent(build)}/sboms`,
  )
  return body.sboms ?? []
}

/**
 * The stored SBOM document, exactly as the provisioner uploaded it —
 * zstd-compressed bytes, not JSON. Fetched with the bearer because the
 * download route is authenticated like every other read (an anchor tag cannot
 * carry the header, so the console blobs it client-side).
 */
export async function downloadSbom(
  token: string,
  tenant: Tenant,
  bucket: string,
  fingerprint: string,
  build: string,
  name: string,
): Promise<ArrayBuffer> {
  const path =
    `${packerPath(tenant)}/buckets/${encodeURIComponent(bucket)}` +
    `/versions/${encodeURIComponent(fingerprint)}/builds/${encodeURIComponent(build)}` +
    `/sboms/${encodeURIComponent(name)}/download`
  const response = await fetch(path, { headers: { Authorization: `Bearer ${token}` } })
  if (!response.ok) {
    const failure = await failureMessage(response, path)
    throw new ApiError(response.status, failure.message, failure.reason)
  }
  return response.arrayBuffer()
}

function packerPath(tenant: Tenant): string {
  return `${PACKER_BASE}/organizations/${encodeURIComponent(tenant.organizationID)}/projects/${encodeURIComponent(tenant.projectID)}`
}

/**
 * The unauthenticated status probe. 200/501/503 all carry the same body, so the
 * body is the answer and the status is for probes that read nothing else;
 * anything unexpected throws, and the caller shows an honest failure rather
 * than guessing which screen first run should be (duf-2so).
 */
export type InstanceHealth = {
  initialized: boolean
  database: boolean
  audit: 'disabled' | 'ok' | 'partial' | 'degraded'
}

export async function instanceHealth(): Promise<InstanceHealth> {
  const response = await fetch('/sys/health')
  if (![200, 501, 503].includes(response.status)) {
    throw new ApiError(response.status, 'The instance status could not be read.')
  }
  return (await response.json()) as InstanceHealth
}

export type InitResponse = {
  client_id: string
  client_secret: string
  recovery_shares: string[]
  recovery_threshold: number
}

export type InitRequest = {
  recovery_share_count: number
  recovery_threshold: number
}

/**
 * Initialize an unclaimed instance. Returns credentials and recovery shares
 * shown exactly once.
 */
export async function initialize(request: InitRequest): Promise<InitResponse> {
  const response = await fetch('/sys/init', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request),
  })
  if (response.status === 409) {
    throw new ApiError(409, 'This instance has already been initialized.')
  }
  if (!response.ok) {
    throw new ApiError(response.status, 'Initialization failed.')
  }
  return (await response.json()) as InitResponse
}

/**
 * The cookie-backed session (duf-1cn): the server holds the token in an
 * httpOnly cookie scoped to /sys/session, so a reload can get it back without
 * the console ever writing a credential to web storage. All three calls are
 * fire-and-observe against the same origin; credentials: 'same-origin' is the
 * fetch default, stated here because the cookie IS the point.
 */
export async function storeSession(token: string): Promise<void> {
  await fetch('/sys/session', {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
  })
}

/** The resumable token, or null when there is no session to resume. */
export async function fetchSession(): Promise<string | null> {
  const response = await fetch('/sys/session')
  if (response.status !== 200) return null
  const body = (await response.json()) as { access_token?: unknown }
  return typeof body.access_token === 'string' && body.access_token !== '' ? body.access_token : null
}

export async function clearSession(): Promise<void> {
  await fetch('/sys/session', { method: 'DELETE' })
}

/**
 * Exchange client credentials through the same endpoint used by Packer.
 * Relative same-origin URL and Authorization-header credentials both match the
 * SDK. Failures stay generic so the browser does not enumerate client IDs.
 */
export async function requestToken(clientID: string, clientSecret: string): Promise<string> {
  const response = await fetch('/oauth2/token', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      Authorization: `Basic ${btoa(`${clientID}:${clientSecret}`)}`,
    },
    body: new URLSearchParams({
      grant_type: 'client_credentials',
      audience: 'https://api.hashicorp.cloud',
    }),
  })
  if (!response.ok) {
    throw new Error('Those credentials were not accepted.')
  }
  const body: unknown = await response.json()
  const token = (body as { access_token?: unknown }).access_token
  if (typeof token !== 'string' || token === '') {
    throw new Error('The server returned no access token.')
  }
  return token
}

export async function platformGet<T>(token: string, path: string): Promise<T> {
  return (await request<T>(token, 'GET', `${PLATFORM_BASE}${path}`)) as T
}

export type ApiPin = {
  bucket_name: string
  pinned_at: string
  pinned_by?: string
}

function pinsPath(tenant: Tenant, bucketName?: string): string {
  const path = `/organizations/${encodeURIComponent(tenant.organizationID)}` +
    `/projects/${encodeURIComponent(tenant.projectID)}/pins`
  return bucketName === undefined ? path : `${path}/${encodeURIComponent(bucketName)}`
}

export async function listPins(token: string, tenant: Tenant): Promise<ApiPin[]> {
  const body = await platformGet<{ pins?: ApiPin[] }>(token, pinsPath(tenant))
  return body.pins ?? []
}

export async function setPin(token: string, tenant: Tenant, bucketName: string): Promise<ApiPin> {
  return platformPut<ApiPin>(token, pinsPath(tenant, bucketName))
}

export async function deletePin(token: string, tenant: Tenant, bucketName: string): Promise<void> {
  await platformDelete(token, pinsPath(tenant, bucketName))
}

export type ApiBagDropAdapter = 'hcp-packer' | 'dufflebag'
export type ApiBagDropCredentialProtection = 'keyring' | 'env_key'
export type ApiBagDropVerificationOutcome = 'resolved' | 'failed'
export type ApiBagDropVerificationReason =
  'credential_refused' | 'project_not_found' | 'unreachable' | 'tls_failure'

export type ApiBagDropVerificationResult = {
  outcome: ApiBagDropVerificationOutcome
  reason?: ApiBagDropVerificationReason
  message?: string
}

export type ApiBagDropLastVerification = ApiBagDropVerificationResult & {
  verified_at: string
}

export type ApiBagDropConfig = {
  adapter: ApiBagDropAdapter
  hcp_packer?: {
    organization_id: string
    project_id: string
    client_id: string
  }
  dufflebag?: {
    endpoint: string
    ca_chain?: string
    organization_id: string
    project_id: string
    client_id: string
  }
  secret_set: boolean
  credential_protection: ApiBagDropCredentialProtection
  enabled: boolean
  last_verification: ApiBagDropLastVerification | null
  created_at: string
  updated_at: string
}

export type ApiBagDropConfigWrite = {
  adapter: ApiBagDropAdapter
  hcp_packer?: {
    organization_id: string
    project_id: string
    client_id: string
    client_secret?: string
  }
  dufflebag?: {
    endpoint: string
    ca_chain?: string
    organization_id: string
    project_id: string
    client_id: string
    client_secret?: string
  }
}

export type ApiBagDropAssociation = {
  bucket_name: string
  state: 'active' | 'pending_removal'
  created_at: string
  updated_at: string
  first_attempted_at: string | null
  last_attempt_at: string | null
  last_synced_at: string | null
  last_sync_error: string | null
  sync_status: 'pending' | 'synced' | 'error' | 'removing'
}

export type ApiBagDropStatus = {
  configured: boolean
  adapter?: ApiBagDropAdapter
  enabled?: boolean
  last_verification?: ApiBagDropLastVerification | null
  associations: ApiBagDropAssociation[]
  reconciling: boolean
  next_pass_at: string | null
  last_pass_at: string | null
  reconcile_interval_seconds: number | null
  backoff_failures: number
}

function bagDropPath(tenant: Tenant, suffix = ''): string {
  const path = `/organizations/${encodeURIComponent(tenant.organizationID)}` +
    `/projects/${encodeURIComponent(tenant.projectID)}/bagdrop`
  return suffix === '' ? path : `${path}/${suffix}`
}

export async function getBagDropConfig(
  token: string, tenant: Tenant,
): Promise<ApiBagDropConfig> {
  return platformGet<ApiBagDropConfig>(token, bagDropPath(tenant))
}

export async function deleteBagDropConfig(token: string, tenant: Tenant): Promise<void> {
  await platformDelete(token, bagDropPath(tenant))
}

export async function listBagDropAssociations(
  token: string, tenant: Tenant,
): Promise<ApiBagDropAssociation[]> {
  const body = await platformGet<{ associations: ApiBagDropAssociation[] }>(
    token, bagDropPath(tenant, 'buckets'),
  )
  return body.associations
}

export async function setBagDropAssociation(
  token: string, tenant: Tenant, bucketName: string,
): Promise<ApiBagDropAssociation> {
  return platformPut<ApiBagDropAssociation>(
    token, bagDropPath(tenant, `buckets/${encodeURIComponent(bucketName)}`),
  )
}

export async function deleteBagDropAssociation(
  token: string, tenant: Tenant, bucketName: string,
): Promise<void> {
  await platformDelete(token, bagDropPath(tenant, `buckets/${encodeURIComponent(bucketName)}`))
}

export async function getBagDropStatus(
  token: string, tenant: Tenant,
): Promise<ApiBagDropStatus> {
  return platformGet<ApiBagDropStatus>(token, bagDropPath(tenant, 'status'))
}

export async function verifyBagDrop(
  token: string, tenant: Tenant,
): Promise<ApiBagDropVerificationResult> {
  return platformPost<ApiBagDropVerificationResult>(token, bagDropPath(tenant, 'verify'))
}

export type ApiBagDropEnableResult =
  | { kind: 'enabled'; config: ApiBagDropConfig }
  | { kind: 'refused'; message: string; verification?: ApiBagDropVerificationResult }

export async function enableBagDrop(
  token: string, tenant: Tenant, config?: ApiBagDropConfigWrite,
): Promise<ApiBagDropEnableResult> {
  const path = `${PLATFORM_BASE}${bagDropPath(tenant, 'enable')}`
  const response = await fetch(path, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${token}`,
      ...(config === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    ...(config === undefined ? {} : { body: JSON.stringify(config) }),
  })
  if (response.status === 409) {
    const conflict = (await response.json()) as {
      message: string
      verification?: ApiBagDropVerificationResult
    }
    return { kind: 'refused', ...conflict }
  }
  if (!response.ok) {
    const failure = await failureMessage(response, path)
    throw new ApiError(response.status, failure.message, failure.reason)
  }
  return { kind: 'enabled', config: (await response.json()) as ApiBagDropConfig }
}

export async function disableBagDrop(
  token: string, tenant: Tenant,
): Promise<ApiBagDropConfig> {
  return platformPost<ApiBagDropConfig>(token, bagDropPath(tenant, 'disable'))
}

export async function reconcileBagDrop(
  token: string, tenant: Tenant,
): Promise<{ message: string }> {
  return platformPost<{ message: string }>(token, bagDropPath(tenant, 'reconcile'))
}

export type ApiInstance = {
  version: string
  commit: string
  api_versions: string[]
  initialized_at?: string | null
  store: boolean
  object_storage: 'unconfigured' | 'ok' | 'unreachable'
  encryption: 'unconfigured' | 'ok' | 'degraded'
  audit: 'disabled' | 'enabled'
  scanner: {
    configured: boolean
    adapter?: string
  }
}

export type ApiSelf = {
  principal_id: string
  name: string
  role: Role
  organization_id?: string | null
  project_id?: string | null
}

export async function getInstance(token: string): Promise<ApiInstance> {
  return platformGet<ApiInstance>(token, '/instance')
}

export async function getSelf(token: string): Promise<ApiSelf> {
  return platformGet<ApiSelf>(token, '/self')
}

export type ApiOrganization = {
  id: string
  name: string
  created_at: string
}

export async function getOrganization(token: string, organizationID: string): Promise<ApiOrganization> {
  return platformGet<ApiOrganization>(token, `/organizations/${encodeURIComponent(organizationID)}`)
}

/**
 * The tenancies a PLATFORM-scoped session can choose between.
 *
 * These go to the platform plane, not the resource-manager one: the compat
 * listing deliberately refuses a caller with no organisation of its own,
 * because the Packer CLI depends on that refusal (ADR-0016). Only a platform
 * root reaches this path, and the platform plane answers it with every
 * organisation on the instance.
 */
export async function listOrganizations(token: string): Promise<ApiOrganization[]> {
  const body = await platformGet<{ organizations?: ApiOrganization[] }>(token, '/organizations')
  return body.organizations ?? []
}

export async function listOrganizationProjects(
  token: string,
  organizationID: string,
): Promise<ApiProject[]> {
  const body = await platformGet<{ projects?: ApiProject[] }>(
    token,
    `/organizations/${encodeURIComponent(organizationID)}/projects`,
  )
  return body.projects ?? []
}

export async function getOrganizationProject(
  token: string,
  organizationID: string,
  projectID: string,
): Promise<ApiProject> {
  return platformGet<ApiProject>(
    token,
    `/organizations/${encodeURIComponent(organizationID)}/projects/${encodeURIComponent(projectID)}`,
  )
}

export async function createOrganization(token: string, name: string): Promise<ApiOrganization> {
  return platformPost<ApiOrganization>(token, '/organizations', { name })
}

export async function createProject(
  token: string,
  organizationID: string,
  name: string,
): Promise<ApiProject> {
  return platformPost<ApiProject>(
    token,
    `/organizations/${encodeURIComponent(organizationID)}/projects`,
    { name },
  )
}

export async function platformPost<T>(token: string, path: string, body?: unknown): Promise<T> {
  return (await request<T>(token, 'POST', `${PLATFORM_BASE}${path}`, body)) as T
}

export async function platformPut<T>(token: string, path: string, body?: unknown): Promise<T> {
  return (await request<T>(token, 'PUT', `${PLATFORM_BASE}${path}`, body)) as T
}

export async function platformPatch<T>(token: string, path: string, body: unknown): Promise<T> {
  return (await request<T>(token, 'PATCH', `${PLATFORM_BASE}${path}`, body)) as T
}

export async function platformDelete(token: string, path: string): Promise<void> {
  await request<null>(token, 'DELETE', `${PLATFORM_BASE}${path}`)
}
