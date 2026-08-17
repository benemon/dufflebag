/**
 * Reading claims out of a token the UI holds.
 *
 * DECODING IS NOT VERIFYING. Nothing here checks the signature, and it must
 * never be treated as an authorization decision — the server verifies on every
 * request (ADR-0017). This exists so the UI can show the right things and offer
 * the right tenants, not to decide what the caller may do.
 */

export type TokenClaims = {
  sub: string
  /**
   * Absent for a platform-scoped principal, mirroring the domain: Issue omits
   * the claim entirely rather than emitting a zero UUID, which would read as a
   * real organisation (token.go, ADR-0016). The bootstrap root is exactly such
   * a principal, so treating absence as malformed locked it out (duf-tkw).
   */
  organizationID: string | null
  /** Absent for an organization-scoped principal, mirroring the domain. */
  projectID: string | null
  /** Present only for a bucket-scoped principal: the bucket's id, never its name. */
  bucketID: string | null
  expiresAt: Date
}

export function decodeClaims(token: string): TokenClaims | null {
  const parts = token.split('.')
  const encoded = parts.length === 3 ? parts[1] : undefined
  if (encoded === undefined) return null
  try {
    const payload = JSON.parse(atob(encoded.replace(/-/g, '+').replace(/_/g, '/')))
    if (typeof payload.sub !== 'string' || typeof payload.exp !== 'number') return null
    // An ABSENT tenancy claim is platform scope; a PRESENT one that is not a
    // string is malformed. The distinction matters: the server never emits a
    // non-string tenancy claim, so a present non-string means something is
    // wrong rather than something is unscoped (token.go Verify draws the same
    // line between absent and unparsable).
    if ('organization_id' in payload && typeof payload.organization_id !== 'string') return null
    if ('project_id' in payload && typeof payload.project_id !== 'string') return null
    if ('bucket_id' in payload && typeof payload.bucket_id !== 'string') return null
    // An empty claim reads as absent, exactly as Verify reads it. A zero UUID
    // is NOT accepted as equivalent: the server never emits one, so seeing it
    // means something is wrong rather than something is unscoped.
    const organizationID =
      typeof payload.organization_id === 'string' && payload.organization_id !== ''
        ? payload.organization_id
        : null
    const projectID =
      typeof payload.project_id === 'string' && payload.project_id !== '' ? payload.project_id : null
    const bucketID =
      typeof payload.bucket_id === 'string' && payload.bucket_id !== '' ? payload.bucket_id : null
    // A project without an organization is malformed rather than narrow, and a
    // bucket without a project likewise, mirroring Verify (token.go).
    if (organizationID === null && projectID !== null) return null
    if (projectID === null && bucketID !== null) return null
    return {
      sub: payload.sub,
      organizationID,
      projectID,
      bucketID,
      expiresAt: new Date(payload.exp * 1000),
    }
  } catch {
    return null
  }
}

export function isExpired(claims: TokenClaims, now = new Date()): boolean {
  return !(now < claims.expiresAt)
}
