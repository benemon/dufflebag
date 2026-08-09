import { useState, type CSSProperties, type ReactNode } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Content,
  DescriptionList, DescriptionListDescription, DescriptionListGroup, DescriptionListTerm,
  Label, PageSection, Title,
} from '@patternfly/react-core'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate, useParams } from 'react-router'

import { PlatformList } from '../components/PlatformLabel'

import {
  useChannelHistory, useEnforcedProvisioners, useVersions, type BucketChannel, type BucketPage,
  type ChannelHistoryEntry, type ParentFreshness, type Version,
} from '../data/versions'
import type { TenancyGap } from '../data/tenant'
import { FacetRail, knownCount, type FacetCount } from './RegistryFacets'

/**
 * Bucket detail — version and channel facets for one registry bucket.
 *
 * Components follow the design's bucket page (mockup 1a1, isBucket):
 *   Breadcrumb · Title · vertical Tabs · Card · DataList/Table · Label
 * The design's custom timeline rail is a deliberate non-PatternFly addition
 * and is omitted; the state Labels carry the same information.
 *
 * Read-only (ADR-0012): Packer creates versions and Terraform promotes them;
 * this screen shows the result. No promote, assign or delete affordances.
 */
export function Versions() {
  const { bucket = '' } = useParams()
  const navigate = useNavigate()
  const { data, loading, failure, gap } = useVersions(bucket)
  const enforcedProvisioners = useEnforcedProvisioners(bucket)
  return (
    <VersionsView
      bucket={bucket}
      bucketData={data}
      loading={loading}
      failure={failure}
      gap={gap}
      enforcedProvisioners={enforcedProvisioners.data}
      enforcedProvisionersLoading={enforcedProvisioners.loading}
      enforcedProvisionersFailure={enforcedProvisioners.failure}
      onBack={() => navigate('/buckets')}
      onOpenVersion={(fingerprint) =>
        navigate(`/buckets/${encodeURIComponent(bucket)}/versions/${encodeURIComponent(fingerprint)}`)}
    />
  )
}

/** The most recent rows shown before "older versions" must be asked for. */
const RECENT = 30

const cardStyle: CSSProperties = {
  '--pf-v6-c-card--BorderRadius': '3px',
  boxShadow: '0 .0625rem .125rem 0 rgba(3,3,3,.12), 0 0 .125rem 0 rgba(3,3,3,.06)',
} as CSSProperties

export function VersionsView({
  bucket,
  bucketData,
  versions: suppliedVersions,
  loading,
  failure,
  gap,
  enforcedProvisioners = [],
  enforcedProvisionersLoading = false,
  enforcedProvisionersFailure = null,
  onBack,
  onOpenVersion,
}: {
  bucket: string
  bucketData?: BucketPage | null
  /** Kept for focused view tests that do not need the bucket-level fetch. */
  versions?: Version[]
  loading: boolean
  failure: string | null
  /** A platform session with no tenancy chosen yet — stated, never fetched around. */
  gap?: TenancyGap | null
  enforcedProvisioners?: string[]
  enforcedProvisionersLoading?: boolean
  enforcedProvisionersFailure?: string | null
  onBack: () => void
  onOpenVersion: (fingerprint: string) => void
}) {
  const versions = bucketData?.versions ?? suppliedVersions ?? []
  const [facet, setFacet] = useState<'overview' | 'versions' | 'channels'>('overview')
  const versionsCount: FacetCount = bucketData || suppliedVersions
    ? knownCount(versions.length)
    : { status: 'unknown' }
  const channelsCount: FacetCount = bucketData
    ? knownCount(bucketData.channels.length)
    : { status: 'unknown' }

  return (
    <>
      <PageSection variant="default">
        <Breadcrumb>
          {/* "Registry" is the bucket list, exactly as the Buckets screen's own
              breadcrumb names it. */}
          <BreadcrumbItem component="button" onClick={onBack}>
            Registry
          </BreadcrumbItem>
          <BreadcrumbItem isActive>{bucket}</BreadcrumbItem>
        </Breadcrumb>
        <Title headingLevel="h1" size="2xl">
          <span style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            {bucket}
            {bucketData?.templateTypes.map((templateType) => (
              <Label key={templateType} isCompact>{templateType}</Label>
            ))}
          </span>
        </Title>
        {bucketData && (
          <>
            <Content component="p">
              {bucketData.description || 'No description has been recorded.'}
            </Content>
            {Object.keys(bucketData.labels).length > 0 && (
              <span style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 12 }}>
                {Object.entries(bucketData.labels).map(([key, value]) => (
                  <Label key={key} isCompact>{key}={value}</Label>
                ))}
              </span>
            )}
          </>
        )}
      </PageSection>

      {/* The rail sits flush against the header and left edge; only the facet
          content carries the grey well's padding. Alert states have no rail,
          so they keep the section padding. */}
      <PageSection
        variant="secondary"
        isFilled
        hasBodyWrapper={false}
        padding={{ default: loading || failure || gap ? 'padding' : 'noPadding' }}
      >
        {loading ? (
          <Content component="p">Loading versions…</Content>
        ) : failure ? (
          <Alert variant="danger" isInline title="Versions could not be loaded">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : gap ? (
          <Alert variant="info" isInline title={gap.title}>
            <Content component="p">{gap.detail}</Content>
          </Alert>
        ) : (
          <FacetRail
            active={facet}
            onSelect={setFacet}
            heading="This bucket"
            label="Bucket facets"
            unmountOnExit={bucketData !== undefined && bucketData !== null}
            facets={[
              {
                key: 'overview', label: 'Overview',
                content: bucketData
                  ? (
                      <BucketOverview
                        bucket={bucketData}
                        enforcedProvisioners={enforcedProvisioners}
                        enforcedProvisionersLoading={enforcedProvisionersLoading}
                        enforcedProvisionersFailure={enforcedProvisionersFailure}
                        onOpenVersion={onOpenVersion}
                      />
                    )
                  : <Content component="p">Bucket details have not been loaded.</Content>,
              },
              {
                key: 'versions', label: 'Versions', count: versionsCount,
                content: (
                  <VersionsFacet
                    versions={versions}
                    onOpenVersion={onOpenVersion}
                  />
                ),
              },
              {
                key: 'channels', label: 'Channels', count: channelsCount,
                content: (
                  <BucketChannelsFacet
                    bucket={bucket}
                    channels={bucketData?.channels ?? []}
                    latestVersion={bucketData?.latestVersion ?? null}
                    onOpenVersion={onOpenVersion}
                  />
                ),
              },
            ]}
          />
        )}
      </PageSection>
    </>
  )
}

function BucketOverview({
  bucket,
  enforcedProvisioners,
  enforcedProvisionersLoading,
  enforcedProvisionersFailure,
  onOpenVersion,
}: {
  bucket: BucketPage
  enforcedProvisioners: string[]
  enforcedProvisionersLoading: boolean
  enforcedProvisionersFailure: string | null
  onOpenVersion: (fingerprint: string) => void
}) {
  const newest = bucket.newestVersion
  return (
    <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <div style={{ flex: '1 1 440px', display: 'flex', flexDirection: 'column', gap: 24 }}>
        <Card style={cardStyle}>
          <CardTitle>Bucket details</CardTitle>
          <CardBody>
            <DescriptionList isHorizontal isCompact>
              <DescriptionListGroup>
                <DescriptionListTerm>Newest version</DescriptionListTerm>
                <DescriptionListDescription>
                  {newest ? (
                    <Button variant="link" isInline onClick={() => onOpenVersion(newest.fingerprint)}>
                      {newest.name}
                    </Button>
                  ) : '—'}
                </DescriptionListDescription>
              </DescriptionListGroup>
              <DescriptionListGroup>
                <DescriptionListTerm>Status</DescriptionListTerm>
                <DescriptionListDescription>
                  {newest ? <VersionStateLabel state={newest.state} /> : '—'}
                </DescriptionListDescription>
              </DescriptionListGroup>
              <DescriptionListGroup>
                <DescriptionListTerm>Published</DescriptionListTerm>
                <DescriptionListDescription>{newest?.created ?? '—'}</DescriptionListDescription>
              </DescriptionListGroup>
              <DescriptionListGroup>
                <DescriptionListTerm>Platforms</DescriptionListTerm>
                <DescriptionListDescription><PlatformList platforms={bucket.platforms} /></DescriptionListDescription>
              </DescriptionListGroup>
              <DescriptionListGroup>
                <DescriptionListTerm>Fingerprint</DescriptionListTerm>
                <DescriptionListDescription>
                  <code className="registry-fingerprint">{newest?.fingerprint ?? '—'}</code>
                </DescriptionListDescription>
              </DescriptionListGroup>
              <EnforcedProvisionersRow
                provisioners={enforcedProvisioners}
                loading={enforcedProvisionersLoading}
                failure={enforcedProvisionersFailure}
              />
            </DescriptionList>
          </CardBody>
        </Card>
        <Card style={cardStyle}>
          <CardTitle>Ancestry</CardTitle>
          <CardBody style={{ display: 'flex', gap: 32, flexWrap: 'wrap' }}>
            <div style={{ flex: '1 1 220px' }}>
              <Content component="h3" style={subheadingStyle}>Parents</Content>
              {bucket.parents.length === 0 ? <Content component="p">None</Content> :
                bucket.parents.map((parent) => (
                  <div key={`${parent.bucket}/${parent.fingerprint}/${parent.localVersionName ?? ''}`} style={{ padding: '5px 0' }}>
                    <strong>{parent.bucket} {parent.versionName}</strong>
                    <div style={{ color: parentColor(parent.freshness), fontSize: 13 }}>
                      {parentFreshnessText(parent.freshness)}
                    </div>
                    <LocalVersionNote
                      prefix="parent of"
                      versionName={parent.localVersionName}
                      latestName={bucket.latestVersion?.name}
                    />
                  </div>
                ))}
            </div>
            <div style={{ flex: '1 1 220px' }}>
              <Content component="h3" style={subheadingStyle}>Children</Content>
              {bucket.children.length === 0 ? <Content component="p">None</Content> :
                bucket.children.map((child) => (
                  <div key={`${child.bucket}/${child.fingerprint}/${child.localVersionName ?? ''}`} style={{ padding: '5px 0' }}>
                    <strong>{child.bucket} {child.versionName}</strong>
                    <LocalVersionNote
                      prefix="built from"
                      versionName={child.localVersionName}
                      latestName={bucket.latestVersion?.name}
                    />
                  </div>
                ))}
            </div>
          </CardBody>
        </Card>
      </div>
    </div>
  )
}

export function EnforcedProvisionersRow({
  provisioners,
  loading,
  failure,
}: {
  provisioners: string[]
  loading: boolean
  failure: string | null
}) {
  return (
    <DescriptionListGroup>
      <DescriptionListTerm>Enforced provisioners</DescriptionListTerm>
      <DescriptionListDescription>
        {loading
          ? 'Loading…'
          : failure
            ? `Could not be loaded: ${failure}`
            : provisioners.length > 0
              ? provisioners.join(', ')
              : 'None configured'}
      </DescriptionListDescription>
    </DescriptionListGroup>
  )
}

const subheadingStyle = {
  color: '#4d4d4d', fontSize: 12, letterSpacing: '.04em', textTransform: 'uppercase' as const,
}

/**
 * Which of THIS bucket's versions the relation belongs to, with "latest" said
 * explicitly (duf-okej.11) — the registry table's status columns follow the
 * latest version only, and this card is where there is room to say so.
 */
function LocalVersionNote({
  prefix,
  versionName,
  latestName,
}: {
  prefix: string
  versionName?: string
  latestName?: string
}) {
  if (!versionName) return null
  return (
    <div style={{ color: '#4d4d4d', fontSize: 13 }}>
      {prefix} {versionName}{versionName === latestName ? ' (latest)' : ''}
    </div>
  )
}

export function VersionsFacet({
  versions,
  onOpenVersion,
}: {
  versions: Version[]
  onOpenVersion: (fingerprint: string) => void
}) {
  // The design windows the spine ("16 older versions · show all") rather than
  // paginating it; older rows stay one click away.
  const [showAll, setShowAll] = useState(false)
  const [expanded, setExpanded] = useState<string | null>(null)
  const visibleVersions = showAll ? versions : versions.slice(0, RECENT)
  const older = versions.length - visibleVersions.length

  return (
    <Card style={cardStyle}>
        <CardTitle>Versions</CardTitle>
        <CardBody>
          {versions.length === 0 ? (
            <Alert variant="info" isInline title="No versions in this bucket" />
          ) : (
            <>
              <Table aria-label="Versions" variant="compact">
                <Thead>
                  <Tr>
                    <Th screenReaderText="Row expansion" />
                    <Th>Version</Th>
                    <Th>Status</Th>
                    <Th>Channels</Th>
                    <Th>Published</Th>
                  </Tr>
                </Thead>
                {visibleVersions.map((version, index) => (
                  <Tbody key={version.fingerprint} isExpanded={expanded === version.fingerprint}>
                    <Tr>
                      <Td expand={{
                        rowIndex: index,
                        isExpanded: expanded === version.fingerprint,
                        onToggle: () => setExpanded(
                          expanded === version.fingerprint ? null : version.fingerprint,
                        ),
                        expandId: `version-${version.fingerprint}`,
                      }} />
                      <Td dataLabel="Version">
                        <Button variant="link" isInline onClick={() => onOpenVersion(version.fingerprint)}>
                          {version.name}
                        </Button>
                      </Td>
                      <Td dataLabel="Status"><VersionStateLabel state={version.state} /></Td>
                      <Td dataLabel="Channels">
                        {version.channels.length === 0 ? '—' : version.channels.map((channel) => (
                          <Label key={channel} isCompact>{channel}</Label>
                        ))}
                      </Td>
                      <Td dataLabel="Published">
                        {version.created}
                        {version.assignments.map((assignment) => (
                          <div key={assignment.channel} style={{ color: '#4d4d4d', fontSize: 13 }}>
                            {assignment.author ? `by ${assignment.author}` : 'actor unavailable'}
                          </div>
                        ))}
                      </Td>
                    </Tr>
                    <Tr isExpanded={expanded === version.fingerprint}>
                      <Td />
                      <Td dataLabel="Version detail" colSpan={4}>
                        <ExpandableRowContent>
                          <DescriptionList isHorizontal isCompact style={{ maxWidth: 720 }}>
                            <DescriptionListGroup>
                              <DescriptionListTerm>Fingerprint</DescriptionListTerm>
                              <DescriptionListDescription>
                                <code className="registry-fingerprint">{version.fingerprint}</code>
                              </DescriptionListDescription>
                            </DescriptionListGroup>
                            <DescriptionListGroup>
                              <DescriptionListTerm>Builds</DescriptionListTerm>
                              <DescriptionListDescription>
                                {countLabel(version.builds.length, 'build')} ·{' '}
                                {countLabel(artifactCount(version), 'artifact')}
                              </DescriptionListDescription>
                            </DescriptionListGroup>
                            <DescriptionListGroup>
                              <DescriptionListTerm>Parents</DescriptionListTerm>
                              <DescriptionListDescription>{parentNames(version)}</DescriptionListDescription>
                            </DescriptionListGroup>
                            <DescriptionListGroup>
                              <DescriptionListTerm>Parent status</DescriptionListTerm>
                              <DescriptionListDescription>{parentStatuses(version)}</DescriptionListDescription>
                            </DescriptionListGroup>
                            <DescriptionListGroup>
                              <DescriptionListTerm>Children</DescriptionListTerm>
                              <DescriptionListDescription>{childNames(version)}</DescriptionListDescription>
                            </DescriptionListGroup>
                          </DescriptionList>
                          <Button variant="link" isInline onClick={() => onOpenVersion(version.fingerprint)}>
                            Open {version.name} →
                          </Button>
                        </ExpandableRowContent>
                      </Td>
                    </Tr>
                  </Tbody>
                ))}
              </Table>
              {older > 0 && (
                <Button variant="link" isInline onClick={() => setShowAll(true)}>
                  {older} older {older === 1 ? 'version' : 'versions'} · show all
                </Button>
              )}
            </>
          )}
        </CardBody>
    </Card>
  )
}

export function BucketChannelsFacet({
  bucket,
  channels,
  latestVersion,
  onOpenVersion,
}: {
  bucket: string
  channels: BucketChannel[]
  latestVersion: BucketPage['latestVersion']
  onOpenVersion: (fingerprint: string) => void
}) {
  const [expanded, setExpanded] = useState<string | null>(null)
  return (
    <Card style={cardStyle}>
      <CardTitle>Channels</CardTitle>
      <CardBody style={{ padding: '16px 0 0' }}>
        {channels.length === 0 ? (
          <Alert variant="info" isInline title="No channels in this bucket" />
        ) : (
          <Table aria-label={`${bucket} channels`} variant="compact">
            <Thead>
              <Tr>
                <Th screenReaderText="Row expansion" />
                <Th>Channel</Th>
                <Th>Assigned version</Th>
                <Th>Assigned by</Th>
                <Th>Assigned time</Th>
              </Tr>
            </Thead>
            {channels.map((channel, index) => (
              <Tbody key={channel.name} isExpanded={expanded === channel.name}>
                <Tr>
                  <Td
                    expand={{
                      rowIndex: index,
                      isExpanded: expanded === channel.name,
                      onToggle: () =>
                        setExpanded(expanded === channel.name ? null : channel.name),
                      expandId: `channel-${channel.name}`,
                    }}
                  />
                  <Td dataLabel="Channel">
                    <span style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                      {channel.name}
                      {channel.restricted && <Label isCompact color="grey">restricted</Label>}
                    </span>
                  </Td>
                  <Td dataLabel="Assigned version">
                    {channel.fingerprint ? (
                      <Button variant="link" isInline onClick={() => onOpenVersion(channel.fingerprint)}>
                        {channel.versionName}
                      </Button>
                    ) : 'Unassigned'}
                  </Td>
                  <Td dataLabel="Assigned by">
                    {channel.fingerprint ? (channel.author ?? 'Unknown') : '—'}
                  </Td>
                  <Td dataLabel="Assigned time">{channel.assignedAt}</Td>
                </Tr>
                <Tr isExpanded={expanded === channel.name}>
                  <Td />
                  <Td dataLabel="Channel detail" colSpan={4}>
                    <ExpandableRowContent>
                      <DescriptionList isHorizontal isCompact style={{ maxWidth: 720 }}>
                        <DescriptionListGroup>
                          <DescriptionListTerm>Fingerprint</DescriptionListTerm>
                          <DescriptionListDescription>
                            {channel.fingerprint ? <code>{channel.fingerprint}</code> : 'Unassigned'}
                          </DescriptionListDescription>
                        </DescriptionListGroup>
                        <DescriptionListGroup>
                          <DescriptionListTerm>Gap to newest version</DescriptionListTerm>
                          <DescriptionListDescription>
                            {channelVersionGap(channel, latestVersion)}
                          </DescriptionListDescription>
                        </DescriptionListGroup>
                        <DescriptionListGroup>
                          <DescriptionListTerm>Managed by</DescriptionListTerm>
                          <DescriptionListDescription>
                            {channel.managed
                              ? 'Dufflebag, on version completion'
                              : <code>hcp_packer_channel_assignment</code>}
                          </DescriptionListDescription>
                        </DescriptionListGroup>
                      </DescriptionList>

                      {expanded === channel.name && (
                        <LazyAssignmentHistory bucket={bucket} channel={channel} />
                      )}
                    </ExpandableRowContent>
                  </Td>
                </Tr>
              </Tbody>
            ))}
          </Table>
        )}
      </CardBody>
    </Card>
  )
}

function LazyAssignmentHistory({ bucket, channel }: { bucket: string; channel: BucketChannel }) {
  const { data, loading, failure } = useChannelHistory(bucket, channel.name, channel.fingerprint)
  let body: ReactNode
  if (loading) {
    body = <Content component="p">Loading assignment history…</Content>
  } else if (failure) {
    body = (
      <Alert variant="danger" isInline title="Assignment history could not be loaded">
        <Content component="p">{failure}</Content>
      </Alert>
    )
  } else if (data.length === 0) {
    body = <Content component="p">No version assignments have been recorded.</Content>
  } else {
    body = <AssignmentHistoryTable channel={channel.name} history={data} />
  }
  return (
    <div style={{ marginTop: 16 }}>
      <Content component="h3" style={{ textTransform: 'uppercase', letterSpacing: '.04em', fontSize: 12 }}>
        Assignment history
      </Content>
      {body}
    </div>
  )
}

export function AssignmentHistoryTable({
  channel,
  history,
}: {
  channel: string
  history: ChannelHistoryEntry[]
}) {
  return (
    <Table
      aria-label={`${channel} assignment history`}
      variant="compact"
      borders
      style={{ maxWidth: 780, marginTop: 8 }}
    >
      <Thead>
        <Tr>
          <Th>Version</Th>
          <Th>Status</Th>
          <Th>Parent status</Th>
          <Th>Assigned by</Th>
          <Th>Assigned time</Th>
        </Tr>
      </Thead>
      <Tbody>
        {history.map((assignment, index) => (
          <Tr key={`${assignment.fingerprint}-${assignment.assignedAt}-${index}`}>
            <Td dataLabel="Version">{assignment.versionName}</Td>
            <Td dataLabel="Status">
              <Label isCompact color={assignment.status === 'active' ? 'green' : 'grey'}>
                {assignment.status === 'active' ? 'Active' : 'Historical'}
              </Label>
            </Td>
            <Td dataLabel="Parent status">{parentStatusText(assignment.parentStatus)}</Td>
            <Td dataLabel="Assigned by">{assignment.author ?? 'Unknown'}</Td>
            <Td dataLabel="Assigned time">{assignment.assignedAt}</Td>
          </Tr>
        ))}
      </Tbody>
    </Table>
  )
}

export function channelVersionGap(
  channel: Pick<BucketChannel, 'fingerprint' | 'versionName'>,
  newest: BucketPage['latestVersion'],
): string {
  if (!newest) return 'The bucket has no complete version to compare with.'
  if (!channel.fingerprint) return `No version is assigned; the newest version is ${newest.name}.`
  if (channel.fingerprint === newest.fingerprint) {
    return `This channel points to the newest version in the bucket (${newest.name}).`
  }
  const assignedSequence = versionSequence(channel.versionName)
  const newestSequence = versionSequence(newest.name)
  if (assignedSequence !== null && newestSequence !== null && newestSequence > assignedSequence) {
    const gap = newestSequence - assignedSequence
    return `${gap} complete ${gap === 1 ? 'version' : 'versions'} behind the newest version (${newest.name}).`
  }
  return `Assigned to ${channel.versionName}; the newest version is ${newest.name}.`
}

function versionSequence(name: string): number | null {
  const match = /^v([1-9][0-9]*)$/.exec(name)
  return match ? Number(match[1]) : null
}

function parentStatusText(status: ChannelHistoryEntry['parentStatus']): string {
  switch (status) {
    case 'none':
      return 'No parent versions'
    case 'current':
      return 'Newest in parent bucket'
    case 'out-of-date':
      return 'Parent bucket has a newer version'
    case 'unknown':
      return 'Unknown'
  }
}

function artifactCount(version: Version): number {
  return version.builds.reduce((count, build) => count + build.artifacts.length, 0)
}

function countLabel(count: number, singular: string): string {
  return `${count} ${singular}${count === 1 ? '' : 's'}`
}

function parentNames(version: Version): string {
  const parents = version.parents ?? []
  return parents.length === 0
    ? 'None'
    : parents.map((parent) => `${parent.bucket} ${parent.versionName}`).join(', ')
}

function parentStatuses(version: Version): string {
  const parents = version.parents ?? []
  return parents.length === 0
    ? 'None'
    : parents.map((parent) => parentFreshnessText(parent.freshness)).join(', ')
}

function childNames(version: Version): string {
  const children = version.children ?? []
  return children.length === 0
    ? 'None'
    : children.map((child) => `${child.bucket} ${child.versionName}`).join(', ')
}

export function parentFreshnessText(freshness: ParentFreshness): string {
  switch (freshness.status) {
    case 'newest':
      return 'newest'
    case 'behind':
      return `bucket now at ${freshness.currentVersion}`
    case 'unknown':
      return 'unknown'
  }
}

function parentColor(freshness: ParentFreshness): string {
  switch (freshness.status) {
    case 'newest':
      return '#3d7317'
    case 'behind':
      return '#795600'
    case 'unknown':
      return '#4d4d4d'
  }
}

export function VersionStateLabel({ state }: { state: Version['state'] }) {
  // Completion restates the name's meaning — v0-incomplete / vN-complete —
  // while the revocation states carry the wire status packer itself refuses on.
  switch (state) {
    case 'complete':
      return <Label isCompact color="green">complete</Label>
    case 'incomplete':
      return <Label isCompact color="grey">incomplete</Label>
    case 'revoked':
      return <Label isCompact color="red">revoked</Label>
    case 'revocation-scheduled':
      return <Label isCompact color="orange">revocation scheduled</Label>
  }
}
