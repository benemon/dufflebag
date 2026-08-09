import { useCallback, useEffect, useState } from 'react'

import {
  deletePin, listBuckets, listChannels, listPins, listVersions, setPin, signOutIfUnauthorized,
  type ApiAncestryLink, type ApiAncestryStatus, type ApiBucket, type ApiChannel,
  type ApiPin, type ApiVersion, type Tenant as ApiTenant,
} from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction } from '../auth/permissions'
import { platformTenancyGap } from './tenant'
import { isComplete, versionState, type Version } from './versions'

/** A channel and the version it currently points at. */
export type ChannelRef = {
  name: string
  versionName: string
  fingerprint: string
  /** Server-maintained (the per-bucket "latest"); rendered, never mutable here. */
  managed: boolean
  restricted: boolean
  drift: Drift
}

export type AncestryLink = {
  href: string
  status: ApiAncestryStatus
}

/**
 * How far a bucket's channels have fallen behind its newest complete version.
 *
 * A closed set rather than a string, so the table cannot render a drift state
 * nobody chose a colour for.
 */
export type Drift =
  | { kind: 'current' }
  | { kind: 'behind'; versions: number }
  | { kind: 'absent'; channel: string }

export type Bucket = {
  name: string
  description: string
  labels: Record<string, string>
  templateTypes: string[]
  versionCount: number
  newestVersion: {
    name: string
    fingerprint: string
    state: Version['state']
  } | null
  parents: AncestryLink | null
  children: AncestryLink | null
  /**
   * Ancestry carried only by versions other than the one the links follow — the third state
   * of the table cells (duf-okej.11): the links above follow the newest
   * version by design (duf-okej.3), and "—" there would read as "none at all".
   */
  parentsInOlderVersions: boolean
  childrenInOlderVersions: boolean
  channels: ChannelRef[]
  drift: Drift
  platforms: string[]
  lastPush: string
  lastPushAt: string
}

/**
 * Bucket data projected from the compatibility-plane API.
 *
 * Shapes are domain-shaped, not wire-shaped: generated models stay behind the
 * client (docs/architecture.md: wire models are never domain models).
 */

export function useBuckets(refreshKey = '') {
  const {
    state, self, selectedOrganization, selectedProject, signOut,
    organizations, organizationsLoading, organizationFailure,
    permittedProjects, projectsLoading, projectFailure,
  } = useAuth()
  const [buckets, setBuckets] = useState<Bucket[]>([])
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)
  const [pins, setPins] = useState<ApiPin[]>([])
  const [pinsLoading, setPinsLoading] = useState(false)
  const [pinsFailure, setPinsFailure] = useState<string | null>(null)

  useEffect(() => {
    // The organisation comes from the session's selection — for a platform
    // session there is no organisation claim to fall back on, and fabricating
    // one would query a tenancy nobody chose (duf-tkw).
    if (!state || !selectedOrganization || !selectedProject) {
      setBuckets([])
      setLoading(false)
      setFailure(null)
      return
    }
    let cancelled = false
    setBuckets([])
    setLoading(true)
    setFailure(null)
    const tenant = { organizationID: selectedOrganization, projectID: selectedProject }

    // Buckets first, then each bucket's versions and channels concurrently.
    // The compatibility plane is frozen in HCP's shape and has no aggregate
    // endpoint; inventing one would create a console-only API (ADR-0001).
    void loadBuckets(state.token, tenant)
      .then((detailed) => {
        if (!cancelled) {
          setBuckets(detailed)
          setFailure(null)
        }
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (signOutIfUnauthorized(err, signOut)) return
        setBuckets([])
        setFailure(err instanceof Error ? err.message : 'Could not load buckets.')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [state, selectedOrganization, selectedProject, signOut, refreshKey])

  useEffect(() => {
    // A tenancy gap is not a project. Do not issue a pins request until both
    // selections exist, matching the bucket fetch above.
    if (!state || !selectedOrganization || !selectedProject) {
      setPins([])
      setPinsLoading(false)
      setPinsFailure(null)
      return
    }
    let cancelled = false
    setPins([])
    setPinsLoading(true)
    setPinsFailure(null)
    const tenant = { organizationID: selectedOrganization, projectID: selectedProject }
    void listPins(state.token, tenant)
      .then((listed) => {
        if (!cancelled) setPins(listed)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (signOutIfUnauthorized(err, signOut)) return
        setPinsFailure(err instanceof Error ? err.message : 'Could not load pinned buckets.')
      })
      .finally(() => {
        if (!cancelled) setPinsLoading(false)
      })
    return () => { cancelled = true }
  }, [state, selectedOrganization, selectedProject, signOut, refreshKey])

  const togglePin = useCallback(async (bucketName: string, pinned: boolean) => {
    if (!state || !selectedOrganization || !selectedProject) return
    const tenant = { organizationID: selectedOrganization, projectID: selectedProject }
    setPinsFailure(null)
    try {
      const updated = await toggleBucketPin(state.token, tenant, bucketName, pinned)
      if (!updated) {
        setPins((current) => current.filter((pin) => pin.bucket_name !== bucketName))
      } else {
        setPins((current) => [...current.filter((item) => item.bucket_name !== bucketName), updated]
          .sort((a, b) => a.pinned_at.localeCompare(b.pinned_at) ||
            a.bucket_name.localeCompare(b.bucket_name)))
      }
    } catch (err: unknown) {
      if (signOutIfUnauthorized(err, signOut)) return
      setPinsFailure(err instanceof Error ? err.message : 'Could not update the pin.')
    }
  }, [state, selectedOrganization, selectedProject, signOut])

  // A session standing above a project folds its tenancy discovery into this
  // screen's states: discovery in flight is loading, a failed listing is a
  // failure, and a settled listing with nothing chosen (or nothing to choose)
  // is a gap the screen states plainly rather than a silent empty table
  // (duf-tkw). The blank project row (duf-4qr) means an organisation-scoped
  // session can stand above a project too, so the gate is the project claim,
  // not platform scope.
  const platform = state !== null && state.claims.organizationID === null
  const aboveProjects = state !== null && state.claims.projectID === null
  const discovering = aboveProjects && (organizationsLoading || projectsLoading)
  const discoveryFailure = aboveProjects ? (organizationFailure ?? projectFailure) : null
  return {
    buckets,
    total: buckets.length,
    loading: loading || discovering,
    failure: failure ?? discoveryFailure,
    pins,
    pinsLoading,
    pinsFailure,
    canPin: permitsAction(self?.role ?? null, 'pinBuckets'),
    togglePin,
    gap:
      aboveProjects && !discovering && !discoveryFailure
        ? platformTenancyGap({
            platform,
            organizationCount: organizations.length,
            selectedOrganization,
            projectCount: permittedProjects.length,
            // The blank row stores '' so the auto-select cannot undo it; the
            // gap helper only asks whether a project is in effect.
            selectedProject: selectedProject || null,
          })
        : null,
  }
}

export async function toggleBucketPin(
  token: string, tenant: ApiTenant, bucketName: string, pinned: boolean,
): Promise<ApiPin | null> {
  if (pinned) {
    await deletePin(token, tenant, bucketName)
    return null
  }
  return setPin(token, tenant, bucketName)
}

export async function loadBuckets(token: string, tenant: ApiTenant): Promise<Bucket[]> {
  const apiBuckets = await listBuckets(token, tenant)
  return Promise.all(
    apiBuckets.map(async (bucket) => {
      const [versions, channels] = await Promise.all([
        listVersions(token, tenant, bucket.name),
        listChannels(token, tenant, bucket.name),
      ])
      return toBucket(bucket, versions, channels)
    }),
  )
}

/** Projects the API's vocabulary into what the screen displays. */
function toBucket(bucket: ApiBucket, versions: ApiVersion[], channels: ApiChannel[]): Bucket {
  const newest = versions[0]
  const lastPushAt = bucket.updated_at ?? newest?.created_at ?? ''
  return {
    name: bucket.name,
    description: bucket.description ?? '',
    labels: bucket.labels ?? {},
    templateTypes: [...new Set(versions.map((version) => version.template_type ?? '').filter(Boolean))].sort(),
    versionCount: versions.length,
    newestVersion: newest ? {
      name: newest.name ?? '—',
      fingerprint: newest.fingerprint ?? '',
      state: versionState(newest),
    } : null,
    parents: ancestryLink(bucket.parents),
    children: ancestryLink(bucket.children),
    // Derived from the versions this screen already loads: every version
    // carries its own parents summary and has_descendants on the wire.
    parentsInOlderVersions: !bucket.parents && versions.some((version) => !!version.parents),
    childrenInOlderVersions: !bucket.children && versions.some((version) => !!version.has_descendants),
    channels: channels.map((channel) => ({
      name: channel.name,
      versionName: channel.version?.name ?? '—',
      fingerprint: channel.version?.fingerprint ?? '',
      managed: channel.managed ?? false,
      restricted: channel.restricted ?? false,
      drift: driftOf([channel], versions),
    })),
    // Drift compares channels against the newest COMPLETE version; with no
    // channels assigned there is nothing to be adrift FROM, which is not the
    // same as being up to date.
    drift: driftOf(channels, versions),
    platforms: [...(bucket.platforms ?? [])].sort(),
    lastPush: lastPushAt ? lastPushAt.slice(0, 10) : '—',
    lastPushAt,
  }
}

function ancestryLink(link?: ApiAncestryLink | null): AncestryLink | null {
  if (!link?.href || !link.status) return null
  return { href: link.href, status: link.status }
}

function driftOf(channels: ApiChannel[], versions: ApiVersion[]): Drift {
  // ListVersions orders sequence DESC and an incomplete version has no
  // sequence yet — PostgreSQL sorts its NULL first — so versions[0] can be an
  // unnumbered v0. Only a complete version can hold a channel
  // (internal/domain/registry/version.go), so drift measures against, and in
  // units of, the COMPLETE versions only: a channel holding the newest
  // complete version is current, whatever incomplete runs sit above it.
  const complete = versions.filter(isComplete)
  const newest = complete[0]
  if (!newest) return { kind: 'absent', channel: 'versions' }
  if (channels.length === 0) return { kind: 'absent', channel: 'channels' }

  // A channel with no version assigned is a different problem from a channel
  // pointing at an old one, and the screen distinguishes them.
  const unassigned = channels.find((channel) => !channel.version?.fingerprint)
  if (unassigned) return { kind: 'absent', channel: unassigned.name }

  const stale = channels.filter((channel) => channel.version?.fingerprint !== newest.fingerprint)
  if (stale.length === 0) return { kind: 'current' }

  // How far behind, measured in versions, so the screen can say "3 behind"
  // rather than merely "behind".
  const behindBy = stale.reduce((furthest, channel) => {
    const index = complete.findIndex((v) => v.fingerprint === channel.version?.fingerprint)
    return index < 0 ? furthest : Math.max(furthest, index)
  }, 1)
  return { kind: 'behind', versions: behindBy }
}
