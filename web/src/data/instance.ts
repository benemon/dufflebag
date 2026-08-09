import { useEffect, useState } from 'react'

import { getInstance, signOutIfUnauthorized, type ApiInstance } from '../api/client'
import { useAuth } from '../auth/AuthContext'

export function useInstance() {
  const { state, signOut } = useAuth()
  const [instance, setInstance] = useState<ApiInstance | null>(null)
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)

  useEffect(() => {
    if (!state) {
      setInstance(null)
      setLoading(false)
      return
    }
    let cancelled = false
    setLoading(true)
    setFailure(null)
    void getInstance(state.token)
      .then((loaded) => {
        if (!cancelled) setInstance(loaded)
      })
      .catch((err: unknown) => {
        if (cancelled || signOutIfUnauthorized(err, signOut)) return
        setInstance(null)
        setFailure(err instanceof Error ? err.message : 'Could not load instance information.')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [state, signOut])

  return { instance, loading, failure }
}
