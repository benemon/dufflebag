import { useBuckets, type Drift } from './buckets'

export type Assignment = {
  bucket: string
  versionName: string
  fingerprint: string
  drift: Drift
}

export type Channel = {
  name: string
  /** True when every bucket's channel of this name is server-maintained. */
  managed: boolean
  assignments: Assignment[]
}

/**
 * Channels derived from the bucket data.
 *
 * The compatibility plane exposes ListChannels per bucket, so the client fans
 * out per bucket and regroups the result for this screen.
 */
export function useChannels() {
  const { buckets, loading, failure, gap } = useBuckets()

  const grouped = new Map<string, { assignments: Assignment[]; managed: boolean }>()
  for (const bucket of buckets) {
    for (const channel of bucket.channels) {
      const entry = grouped.get(channel.name) ?? { assignments: [], managed: true }
      entry.assignments.push({
        bucket: bucket.name,
        versionName: channel.versionName,
        fingerprint: channel.fingerprint,
        drift: channel.drift,
      })
      // "latest" is managed in every bucket; the group is labelled managed
      // only when that holds for all of its members.
      entry.managed = entry.managed && channel.managed
      grouped.set(channel.name, entry)
    }
  }

  const order = ['production', 'staging', 'dev']
  const byChannel: Channel[] = [...grouped.entries()]
    .map(([name, { assignments, managed }]) => ({ name, managed, assignments }))
    .sort((a, b) => {
      const ai = order.indexOf(a.name)
      const bi = order.indexOf(b.name)
      return (ai < 0 ? order.length : ai) - (bi < 0 ? order.length : bi)
    })

  return { byChannel, loading, failure, gap }
}
