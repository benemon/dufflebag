import type { Tenant } from '../api/client'
import type { LoadedBuildFindings } from './versions'

/**
 * Per-build inventories kept for the session: they change only after a rescan
 * or SBOM upload, and every read costs the server a full findings pass per page.
 */
const entries = new Map<string, LoadedBuildFindings>()

export function inventoryCacheKey(
  tenant: Tenant, bucket: string, fingerprint: string, buildID: string,
): string {
  return [tenant.organizationID, tenant.projectID, bucket, fingerprint, buildID].join('\u0000')
}

export function readCachedInventory(key: string): LoadedBuildFindings | undefined {
  return entries.get(key)
}

export function writeCachedInventory(key: string, findings: LoadedBuildFindings): void {
  entries.set(key, findings)
}

/** Sign-out forgets everything: the next principal must not inherit a tenancy's inventory. */
export function clearInventoryCache(): void {
  entries.clear()
}
