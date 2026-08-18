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
  // Keyed on the TENANCY, deliberately not the token: a session renewal mints
  // a new token every ~14 minutes, and keying on it blanked every picker
  // instance mid-interaction while the same tenancy reloaded for no reason.
  // The state ref keeps that promise: the stable refresh callback always reads
  // the current token for its requests without making renewal an effect key.
  const identity = state && selectedOrganization && selectedProject
    ? `${selectedOrganization}\u0000${selectedProject}`
    : ''
  const stateRef = useRef(state)
  stateRef.current = state
  const identityRef = useRef(identity)
  identityRef.current = identity

  const refresh = useCallback(async (): Promise<ApiBucket[] | null> => {
    const session = stateRef.current
    if (!session || !selectedOrganization || !selectedProject) return null
    const requestIdentity = `${selectedOrganization}\u0000${selectedProject}`
    try {
      const listed = await loadBucketPicker(session.token, {
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
  }, [selectedOrganization, selectedProject, signOut])

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
