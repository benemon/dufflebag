import { useCallback, useEffect, useState } from 'react'

import {
  ApiError, platformGet, platformPost, signOutIfUnauthorized,
} from '../api/client'
import { useAuth } from '../auth/AuthContext'

export type EncryptionState = 'unconfigured' | 'ok' | 'degraded'

export type KeyringEntry = {
  purpose: 'payload' | 'integrity' | 'token_signing' | 'audit_hmac'
  version: number
  kek_ref: string
  wrapped_at: string
}

export type Encryption = {
  state: EncryptionState
  keyring: KeyringEntry[]
  kek_latest?: string
}

export async function loadEncryption(token: string): Promise<Encryption> {
  return platformGet<Encryption>(token, '/encryption')
}

export async function rewrapEncryption(token: string): Promise<Encryption> {
  return platformPost<Encryption>(token, '/encryption/rewrap')
}

export async function rotateEncryption(token: string): Promise<Encryption> {
  return platformPost<Encryption>(token, '/encryption/rotate')
}

/** Safe, actionable reasons returned only to an authenticated platform root. */
export function encryptionRefusalHint(error: unknown): string | null {
  if (!(error instanceof ApiError)) return null
  if (error.status === 403) {
    return 'Only a platform root can view or change encryption state.'
  }
  if (error.status === 409) {
    return error.message.trim() ||
      'Encryption is not configured, or another rotation changed the keyring. Reload before retrying.'
  }
  if (error.status === 502) {
    return 'The key service refused or was unreachable. The keyring was not changed.'
  }
  return null
}

export function useEncryption() {
  const { state, self, signOut } = useAuth()
  const [encryption, setEncryption] = useState<Encryption | null>(null)
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)

  const reload = useCallback(async () => {
    if (!state) {
      setEncryption(null)
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      setEncryption(await loadEncryption(state.token))
      setFailure(null)
    } catch (err: unknown) {
      if (signOutIfUnauthorized(err, signOut)) return
      setEncryption(null)
      setFailure(
        encryptionRefusalHint(err) ??
          (err instanceof Error ? err.message : 'Could not load encryption state.'),
      )
    } finally {
      setLoading(false)
    }
  }, [state, signOut])

  useEffect(() => {
    void reload()
  }, [reload])

  return {
    encryption, loading, failure, reload,
    token: state?.token ?? null,
    callerRole: self?.role ?? null,
  }
}
