import {
  Alert, Card, CardBody, CardTitle, Content, Gallery, Label,
  PageSection, Title,
} from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'

import { useChannels, type Channel } from '../data/channels'
import type { Drift } from '../data/buckets'
import type { TenancyGap } from '../data/tenant'

/**
 * Channels — promotion state across the project.
 *
 * Components follow the labelled design (variant 1a1, isChannels):
 *   PageSection variant=light · Title · TextContent
 *   Alert variant=info isInline
 *   Gallery · Card · Table variant=compact · Label
 *
 * Read-only. Promotion has two sources — the server assigns the managed
 * 'latest' channel itself on version completion, and Terraform assigns the
 * rest via hcp_packer_channel_assignment (ADR-0011). The UI writes neither;
 * the managed label on a channel row says which source owns it.
 */
export function Channels() {
  return <ChannelsView {...useChannels()} />
}

export function ChannelsView({
  byChannel,
  loading,
  failure,
  gap,
}: {
  byChannel: Channel[]
  loading: boolean
  failure: string | null
  /** A platform session with no tenancy chosen yet — stated, never fetched around. */
  gap?: TenancyGap | null
}) {
  const byBucket = new Map<string, { channel: Channel; assignment: Channel['assignments'][number] }[]>()
  for (const channel of byChannel) {
    for (const assignment of channel.assignments) {
      const rows = byBucket.get(assignment.bucket) ?? []
      rows.push({ channel, assignment })
      byBucket.set(assignment.bucket, rows)
    }
  }

  return (
    <>
      <PageSection variant="default">
        <Title headingLevel="h1" size="2xl">
          Channels
        </Title>
        <Content component="p">
          A channel belongs to one bucket and points at one version in that bucket. They are
          listed under the bucket that owns them.
        </Content>
      </PageSection>

      <PageSection variant="secondary" isFilled>
        {loading ? (
          <Content component="p">Loading channels…</Content>
        ) : failure ? (
          <Alert variant="danger" isInline title="Channels could not be loaded">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : gap ? (
          <Alert variant="info" isInline title={gap.title}>
            <Content component="p">{gap.detail}</Content>
          </Alert>
        ) : byChannel.length === 0 ? (
          <Alert variant="info" isInline title="No channels in this project" />
        ) : (
          <Gallery hasGutter minWidths={{ default: '420px' }}>
            {[...byBucket.entries()].map(([bucket, rows]) => (
              <BucketChannelsCard key={bucket} bucket={bucket} rows={rows} />
            ))}
          </Gallery>
        )}
      </PageSection>
    </>
  )
}

function BucketChannelsCard({
  bucket,
  rows,
}: {
  bucket: string
  rows: { channel: Channel; assignment: Channel['assignments'][number] }[]
}) {
  return (
    <Card isCompact>
      <CardTitle>
        {bucket}
        <Content component="small" style={{ marginLeft: 8 }}>
          {rows.length} {rows.length === 1 ? 'channel' : 'channels'}
        </Content>
      </CardTitle>
      <CardBody>
        <Table aria-label={`${bucket} channels`} variant="compact">
          <Thead>
            <Tr>
              <Th>Channel</Th>
              <Th>Version</Th>
              <Th>State</Th>
            </Tr>
          </Thead>
          <Tbody>
            {rows.map(({ channel, assignment }) => (
              <Tr key={channel.name}>
                <Td dataLabel="Channel">
                  {channel.name}
                  {channel.managed && <Label isCompact color="blue" style={{ marginLeft: 8 }}>managed</Label>}
                </Td>
                <Td dataLabel="Version">
                  {assignment.versionName} {assignment.fingerprint && <code>{assignment.fingerprint}</code>}
                </Td>
                <Td dataLabel="State">
                  <AssignmentState drift={assignment.drift} />
                </Td>
              </Tr>
            ))}
          </Tbody>
        </Table>
      </CardBody>
    </Card>
  )
}

function AssignmentState({ drift }: { drift: Drift }) {
  switch (drift.kind) {
    case 'current':
      return <Label isCompact color="green">current</Label>
    case 'absent':
      return <Label isCompact color="grey">no {drift.channel}</Label>
    case 'behind':
      return (
        <Label isCompact color={drift.versions >= 10 ? 'orange' : 'yellow'}>
          {drift.versions} behind
        </Label>
      )
  }
}
