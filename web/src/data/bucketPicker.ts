import { useCallback, useEffect, useRef, useState } from 'react'

import {
  listBuckets, listPins, signOutIfUnauthorized,
  type ApiBucket, type ApiPin, type Tenant,
} from '../api/client'
import { useAuth } from '../auth/AuthContext'

export type BucketPickerListing = {
  buckets: ApiBucket[]
  pins: ApiPin[]
}

export async function loadBucketPicker(
  token: string,
  tenant: Tenant,
): Promise<BucketPickerListing> {
  const [buckets, pins] = await Promise.all([
    listBuckets(token, tenant),
    listPins(token, tenant),
  ])
  return { buckets, pins }
}

export function useBucketPicker() {
  const { state, selectedOrganization, selectedProject, signOut } = useAuth()
  const [buckets, setBuckets] = useState<ApiBucket[]>([])
  const [pins, setPins] = useState<ApiPin[]>([])
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)
  const identity = state && selectedOrganization && selectedProject
    ? `${state.token}\u0000${selectedOrganization}\u0000${selectedProject}`
    : ''
  const identityRef = useRef(identity)
  identityRef.current = identity

  const refresh = useCallback(async (): Promise<ApiBucket[] | null> => {
    if (!state || !selectedOrganization || !selectedProject) return null
    const requestIdentity = `${state.token}\u0000${selectedOrganization}\u0000${selectedProject}`
    try {
      const listed = await loadBucketPicker(state.token, {
        organizationID: selectedOrganization,
        projectID: selectedProject,
      })
      if (identityRef.current === requestIdentity) {
        setBuckets(listed.buckets)
        setPins(listed.pins)
        setFailure(null)
      }
      return listed.buckets
    } catch (err: unknown) {
      if (signOutIfUnauthorized(err, signOut)) return null
      if (identityRef.current === requestIdentity) {
        setFailure(err instanceof Error ? err.message : 'Could not load buckets.')
      }
      return null
    }
  }, [state, selectedOrganization, selectedProject, signOut])

  useEffect(() => {
    setBuckets([])
    setPins([])
    setFailure(null)
    if (!identity) {
      setLoading(false)
      return
    }
    setLoading(true)
    void refresh().finally(() => {
      if (identityRef.current === identity) setLoading(false)
    })
  }, [identity, refresh])

  return { buckets, pins, loading, failure, refresh }
}
