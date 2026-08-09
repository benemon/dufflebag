import { useCallback, useEffect, useState } from 'react'

import {
  ApiError, platformDelete, platformGet, platformPost, signOutIfUnauthorized,
} from '../api/client'
import { useAuth } from '../auth/AuthContext'

export type AuditTargetMeasurement =
  | {
      state: 'available'
      current_file_size_bytes: number
      filesystem_free_bytes: number
    }
  | { state: 'unavailable' }

export type AuditTarget = {
  id: string
  path: string
  created_at: string
  status: 'healthy' | 'failing'
  since: string | null
  consecutive_failures: number
  cumulative_failures: number
  last_failure_at: string | null
  last_reopened_at: string | null
  measurement: AuditTargetMeasurement
}

export async function loadAuditTargets(token: string): Promise<AuditTarget[]> {
  const body = await platformGet<{ targets?: AuditTarget[] }>(token, '/audit/targets')
  return body.targets ?? []
}

export async function createAuditTarget(token: string, path: string): Promise<AuditTarget> {
  return platformPost<AuditTarget>(token, '/audit/targets', { path })
}

export async function deleteAuditTarget(token: string, id: string): Promise<void> {
  await platformDelete(token, `/audit/targets/${encodeURIComponent(id)}`)
}

/** Safe, actionable reasons returned only to an authenticated platform root. */
export function auditRefusalHint(error: unknown): string | null {
  if (!(error instanceof ApiError)) return null
  if (error.status === 403) {
    return 'Only a platform root can view or change audit targets.'
  }
  if (error.status !== 400) return error.status === 409 ? error.message : null
  switch (error.reason) {
    case 'not-a-regular-file':
      return 'The path must name a regular file, not a directory, device, or pipe.'
    case 'permission-denied':
      return 'The Dufflebag process does not have permission to open that path.'
    case 'symlink-refused':
      return 'Symlinks are refused for audit targets. Enter the regular file path directly.'
    case 'world-writable-parent':
      return 'The parent directory is world-writable. Restrict it before adding this target.'
    case 'path-unavailable':
      return 'The path or its parent is unavailable to the Dufflebag process.'
    default:
      return error.message
  }
}

export function useAuditTargets() {
  const { state, self, signOut } = useAuth()
  const [targets, setTargets] = useState<AuditTarget[]>([])
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)

  const reload = useCallback(async () => {
    if (!state) {
      setTargets([])
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      setTargets(await loadAuditTargets(state.token))
      setFailure(null)
    } catch (err: unknown) {
      if (signOutIfUnauthorized(err, signOut)) return
      setTargets([])
      setFailure(
        auditRefusalHint(err) ?? (err instanceof Error ? err.message : 'Could not load audit targets.'),
      )
    } finally {
      setLoading(false)
    }
  }, [state, signOut])

  useEffect(() => {
    void reload()
  }, [reload])

  return {
    targets, loading, failure, reload,
    token: state?.token ?? null,
    callerRole: self?.role ?? null,
  }
}
