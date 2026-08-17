/**
 * Service principal management, against the platform plane.
 *
 * The one surface the console reaches that Packer does not (ADR-0012's
 * 2026-07-31 amendment). Everything here can mint or destroy a credential, which
 * is why the shapes below are deliberate rather than convenient.
 *
 * # A plaintext secret exists in exactly one place
 *
 * The API returns it in the response to ISSUING a secret, and nowhere else — it
 * is argon2id-hashed on write and cannot be recovered, only replaced. Creating a
 * principal mints nothing and returns a plain `Principal` (duf-4ac), so issuance
 * is the single call that can hand one over. `IssuedCredential` is a SEPARATE
 * type from `Principal`, returned only by that call, and it never appears on the
 * type used to render a list. A screen cannot accidentally show a secret it was
 * not explicitly handed, and a refetch cannot bring one back.
 */

import { useCallback, useEffect, useState } from 'react'

import { ApiError, platformDelete, platformGet, platformPost, signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { ROLES, type Role } from '../auth/permissions'

export type { Role } from '../auth/permissions'

/**
 * The five nested roles (ADR-0019), ordered least to most authority.
 *
 * NOT the scope claims the design mockups model. Authority is resolved from
 * storage on every request so revocation is immediate; carrying it in the token
 * would delay revocation by up to a full token lifetime, which is the thing that
 * decision exists to prevent.
 */
/** What each role adds, for the create form. Wording follows ADR-0019's table. */
export const ROLE_DESCRIPTIONS: Record<Role, string> = {
  reader: 'Read buckets, versions, builds and channels.',
  builder: 'Adds creating buckets, versions and builds. What a Packer CLI principal needs.',
  publisher: 'Adds assigning channels — promoting a version to production.',
  maintainer: 'Adds managing principals within the tenancy.',
  root: 'Organisations, authentication and audit configuration. Platform-scoped only.',
}

/**
 * The roles that can actually be granted here, given the tenancy and the
 * granter.
 *
 * Two independent rules, both enforced server-side, mirrored so the form cannot
 * offer a combination the API will refuse:
 *
 * TENANCY. `root` is platform-scoped only and every other role is tenancy-only —
 * validBinding ties them together exactly, so "root in an organisation" is not a
 * narrower root, it is a malformed principal. Creating within a tenancy
 * therefore never offers root, and creating above one offers nothing else.
 *
 * GRANTER. No identity may grant a role more permissive than its own
 * (ADR-0019). A maintainer creating a principal cannot make it root, and the
 * form should not imply otherwise by showing an option that will be refused.
 *
 * Mirroring is for the operator's sake, not the server's. The API refuses these
 * regardless — this only stops the console offering a choice it knows is a dead
 * end.
 */
export function grantableRoles(
  scope: 'platform' | 'tenancy',
  granter: Role | null,
): Role[] {
  const permittedHere: Role[] =
    scope === 'platform' ? ['root'] : ROLES.filter((role) => role !== 'root')
  if (granter === null) return permittedHere
  const ceiling = ROLES.indexOf(granter)
  return permittedHere.filter((role) => ROLES.indexOf(role) <= ceiling)
}

export type SecretMetadata = {
  id: string
  created_at: string
  /** Null when never used, which is how a failed rotation is spotted. */
  last_used_at: string | null
  /** Null when the secret never expires. An expired secret stays listed until revoked. */
  expires_at: string | null
}

export type Principal = {
  id: string
  name: string
  client_id: string
  role: Role
  organization_id: string | null
  project_id: string | null
  bucket_id?: string | null
  created_at: string
  secrets: SecretMetadata[]
}

/**
 * A credential, returned once and never again.
 *
 * Separate from Principal on purpose: a list can never carry one, so no screen
 * can render a secret by accident.
 */
export type IssuedCredential = {
  secretID: string
  secret: string
  clientID: string
}

/** Where the session stands, per the picker. The selection IS the scope (duf-4qr). */
export type Standing = 'platform' | 'organization' | 'project' | 'bucket'

export type ScopeSelection = {
  organizationID: string | null
  projectID: string | null
  bucketID?: string | null
}

export async function loadPrincipals(
  token: string,
  selection: ScopeSelection,
): Promise<Principal[]> {
  // The listing names the selection and the server answers EXACTLY that scope,
  // never a subtree — see-where-you-stand and create-where-you-stand are the
  // same rule (duf-4qr). Nothing selected means the platform, which the server
  // reads from an unqualified request as the caller's own standing. The filter
  // is authorization-checked server-side; naming it here is scoping, not
  // authority.
  const params = new URLSearchParams()
  if (selection.organizationID) params.set('organization_id', selection.organizationID)
  if (selection.projectID) params.set('project_id', selection.projectID)
  if (selection.bucketID) params.set('bucket_id', selection.bucketID)
  const query = params.toString()
  const body = await platformGet<{ principals?: Principal[] }>(
    token,
    query === '' ? '/principals' : `/principals?${query}`,
  )
  return body.principals ?? []
}

export type CreateRequest = {
  name: string
  role: Role
  organizationID: string | null
  projectID: string | null
  bucketID?: string | null
}

/**
 * Creates a principal holding no secrets, and returns it rather than a
 * credential (duf-4ac).
 *
 * Issuing is a separate, explicit action the operator takes afterwards, through
 * the same issueSecret call used for rotation. The return type carries that:
 * there is no credential here to leak, forget to display, or accidentally log.
 */
export async function createPrincipal(
  token: string,
  request: CreateRequest,
): Promise<Principal> {
  return platformPost<Principal>(token, '/principals', {
    name: request.name,
    role: request.role,
    // Omitted rather than sent as null for a platform-scoped root: the server
    // reads an absent organisation as "platform", and only root may be held
    // there (ADR-0019).
    ...(request.organizationID ? { organization_id: request.organizationID } : {}),
    ...(request.projectID ? { project_id: request.projectID } : {}),
    ...(request.bucketID ? { bucket_id: request.bucketID } : {}),
  })
}

/**
 * Issues a secret — the first one, or the second for a rotation.
 *
 * One call for both since duf-4ac: a principal is created holding none, so this
 * is how every credential the console hands out comes into existence. Two may be
 * active at once (ADR-0004), which is what makes rotation gapless: deploy the
 * new one, wait until it has authenticated, then revoke the old. The API refuses
 * a third.
 */
export async function issueSecret(
  token: string,
  principal: Principal,
  expiresAt?: string,
): Promise<IssuedCredential> {
  const issued = await platformPost<{ id: string; secret: string }>(
    token,
    `/principals/${encodeURIComponent(principal.id)}/secrets`,
    expiresAt ? { expires_at: expiresAt } : undefined,
  )
  return { secretID: issued.id, secret: issued.secret, clientID: principal.client_id }
}

/**
 * Revokes one secret. Only a ROOT principal's last secret is refused
 * (ADR-0004, amended 2026-08-02).
 *
 * Which means a compromised credential can be revoked immediately and replaced
 * afterwards, rather than the other way round: any principal below root may be
 * left with no secrets, because a maintainer whose scope covers it can issue a
 * fresh one. Root is the exception — nothing sits above it to re-issue.
 */
export async function revokeSecret(
  token: string,
  principalID: string,
  secretID: string,
): Promise<void> {
  await platformDelete(
    token,
    `/principals/${encodeURIComponent(principalID)}/secrets/${encodeURIComponent(secretID)}`,
  )
}

export async function deletePrincipal(token: string, principalID: string): Promise<void> {
  await platformDelete(token, `/principals/${encodeURIComponent(principalID)}`)
}

/**
 * Why an action was refused, in terms the operator can act on.
 *
 * The server distinguishes 404 and 403 deliberately: a tenancy you cannot see
 * answers not-found, because confirming it exists is itself a disclosure; a role
 * you lack within something visible answers forbidden (ADR-0017). Collapsing
 * them into "something went wrong" throws away the only signal that says whether
 * to ask for a role or to stop looking.
 */
export function refusalHint(error: unknown): string | null {
  if (!(error instanceof ApiError)) return null
  switch (error.status) {
    case 403:
      return 'Your role does not permit this. A maintainer can manage principals within its own tenancy; only root can act above one.'
    case 404:
      return 'Not found, or not visible to you — the two are answered identically on purpose.'
    case 409:
      return error.message
    default:
      return null
  }
}

/** Whether this secret has ever authenticated. A rotation that never does is a failed one. */
export function everUsed(secret: SecretMetadata): boolean {
  return secret.last_used_at !== null
}

/**
 * Loads the principals at the picker's selection, and works out the caller's
 * own authority from among them.
 *
 * The selection, not the token claims, scopes both the listing and the create
 * form: create-where-you-stand and see-where-you-stand are the same rule
 * (duf-4qr). For a tenancy session the selection is initialised from the
 * claims, so nothing widens; for a root session it is how browsing works at
 * all. The server authorizes the named scope — the console's filter carries no
 * authority.
 *
 * The token deliberately carries no role (ADR-0019). AuthContext obtains the
 * current server-resolved principal from GET /api/v1/self, independently of
 * which scope happens to be listed here.
 */
export function usePrincipals() {
  const {
    state, self, signOut, selectedOrganization, selectedProject, selectedBucket,
  } = useAuth()
  const [principals, setPrincipals] = useState<Principal[]>([])
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)

  // '' is the blank project row — deliberately none — which the wire simply
  // omits, the same as null.
  const organizationID = selectedOrganization
  const projectID = selectedProject || null
  // The carried bucket only means bucket standing under a full pair.
  const bucketID = organizationID && projectID ? (selectedBucket?.id ?? null) : null
  const bucketName = organizationID && projectID ? (selectedBucket?.name ?? null) : null

  const reload = useCallback(async () => {
    if (!state) {
      setPrincipals([])
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      setPrincipals(await loadPrincipals(state.token, { organizationID, projectID, bucketID }))
      setFailure(null)
    } catch (err: unknown) {
      if (signOutIfUnauthorized(err, signOut)) return
      setPrincipals([])
      setFailure(
        refusalHint(err) ?? (err instanceof Error ? err.message : 'Could not load principals.'),
      )
    } finally {
      setLoading(false)
    }
  }, [state, organizationID, projectID, bucketID, signOut])

  useEffect(() => {
    void reload()
  }, [reload])

  return {
    principals,
    loading,
    failure,
    reload,
    selfID: self?.principal_id ?? state?.claims.sub ?? null,
    callerRole: self?.role ?? null,
    token: state?.token ?? null,
    organizationID,
    projectID,
    bucketID,
    bucketName,
  }
}
