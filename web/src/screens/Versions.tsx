import { useEffect, useState, type ReactNode, type Ref } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Checkbox, Content,
  DescriptionList, DescriptionListDescription, DescriptionListGroup, DescriptionListTerm,
  Dropdown, DropdownItem, DropdownList, EmptyState, EmptyStateActions, EmptyStateBody,
  EmptyStateFooter, Form, FormGroup, FormSelect, FormSelectOption,
  Label, MenuToggle, Modal, ModalBody, ModalFooter, ModalHeader, PageSection, Pagination,
  Popover, TextInput, Title, Toolbar, ToolbarContent, ToolbarItem, Tooltip,
} from '@patternfly/react-core'
import type { MenuToggleElement } from '@patternfly/react-core'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import EllipsisVIcon from '@patternfly/react-icons/dist/esm/icons/ellipsis-v-icon'
import HelpIcon from '@patternfly/react-icons/dist/esm/icons/help-icon'
import { useNavigate, useParams } from 'react-router'

import { PlatformList } from '../components/PlatformLabel'
import { DeleteBucketModal } from '../components/DeleteBucketModal'
import { ScreenHeader } from '../components/ScreenHeader'
import { TenancyGapEmptyState } from '../components/TenancyCreation'
import { TypedConfirmModal } from '../components/TypedConfirmModal'
import { CopyableIdentifier } from '../components/CopyableIdentifier'
import { When } from '../components/When'

import {
  assignChannelVersion, createChannel, deleteBucket, deleteChannel, getBagDropStatus,
  signOutIfUnauthorized,
} from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { RoleRestrictedButton } from '../auth/RoleRestrictedButton'
import { permitsAction, requirementReason, type Role } from '../auth/permissions'
import {
  useChannelHistory, useEnforcedProvisioners, useVersions, type BucketChannel, type BucketPage,
  type ChannelHistoryEntry, type ParentFreshness, type Version,
} from '../data/versions'
import type { TenancyGap } from '../data/tenant'
import { FacetRail, knownCount, type FacetCount } from './RegistryFacets'

const ANCESTRY_SCOPE = 'Follows this bucket\'s newest version. Older versions with ancestry show as "other versions".'

/**
 * Bucket detail — version and channel facets for one registry bucket.
 *
 * Components follow the design's bucket page (mockup 1a1, isBucket):
 *   Breadcrumb · Title · vertical Tabs · Card · DataList/Table · Label
 * The design's custom timeline rail is a deliberate non-PatternFly addition
 * and is omitted; the state Labels carry the same information.
 *
 * Packer creates versions. Publishers manage channel lifecycle and assignments
 * here; Terraform remains the automation path.
 */
export function Versions() {
  const { bucket = '' } = useParams()
  const navigate = useNavigate()
  const { data, loading, failure, gap, reload } = useVersions(bucket)
  const { state, self, selectedOrganization, selectedProject, signOut } = useAuth()
  const tenant = state && selectedOrganization && selectedProject
    ? { organizationID: selectedOrganization, projectID: selectedProject }
    : null
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
      callerRole={self?.role ?? null}
      onBack={() => navigate('/buckets')}
      onOpenVersion={(fingerprint) =>
        navigate(`/buckets/${encodeURIComponent(bucket)}/versions/${encodeURIComponent(fingerprint)}`)}
      onCreateChannel={async (options) => {
        if (!state || !tenant) throw new Error('No session.')
        try {
          await createChannel(state.token, tenant, bucket, options)
          reload()
        } catch (err: unknown) {
          signOutIfUnauthorized(err, signOut)
          throw err
        }
      }}
      onAssignChannel={async (channel, fingerprint) => {
        if (!state || !tenant) throw new Error('No session.')
        try {
          await assignChannelVersion(state.token, tenant, bucket, channel, fingerprint)
          reload()
        } catch (err: unknown) {
          signOutIfUnauthorized(err, signOut)
          throw err
        }
      }}
      onDeleteChannel={async (channel) => {
        if (!state || !tenant) throw new Error('No session.')
        try {
          await deleteChannel(state.token, tenant, bucket, channel)
          reload()
        } catch (err: unknown) {
          signOutIfUnauthorized(err, signOut)
          throw err
        }
      }}
      onDeleteBucket={async () => {
        if (!state || !tenant) throw new Error('No session.')
        try {
          await deleteBucket(state.token, tenant, bucket)
          navigate('/buckets')
        } catch (err: unknown) {
          signOutIfUnauthorized(err, signOut)
          throw err
        }
      }}
      onCheckMirrored={async () => {
        if (!state || !tenant) return false
        const status = await getBagDropStatus(state.token, tenant)
        return status.associations.some(
          (association) =>
            association.bucket_name === bucket && association.state === 'active',
        )
      }}
    />
  )
}

/** The default page preserves the established recent-version window. */
const RECENT = 30

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
  callerRole = null,
  onBack,
  onOpenVersion,
  onCreateChannel = () => Promise.reject(new Error('No session.')),
  onAssignChannel = () => Promise.reject(new Error('No session.')),
  onDeleteChannel = () => Promise.reject(new Error('No session.')),
  onDeleteBucket = () => Promise.reject(new Error('No session.')),
  onCheckMirrored,
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
  callerRole?: Role | null
  onBack: () => void
  onOpenVersion: (fingerprint: string) => void
  onCreateChannel?: (options: {
    name: string; restricted?: boolean; fingerprint?: string
  }) => Promise<void>
  onAssignChannel?: (channel: string, fingerprint: string) => Promise<void>
  onDeleteChannel?: (channel: string) => Promise<void>
  onDeleteBucket?: () => Promise<void>
  onCheckMirrored?: () => Promise<boolean>
}) {
  const versions = bucketData?.versions ?? suppliedVersions ?? []
  const [facet, setFacet] = useState<'overview' | 'versions' | 'channels'>('overview')
  const [deletingBucket, setDeletingBucket] = useState(false)
  const versionsCount: FacetCount = bucketData || suppliedVersions
    ? knownCount(versions.length)
    : { status: 'unknown' }
  const channelsCount: FacetCount = bucketData
    ? knownCount(bucketData.channels.length)
    : { status: 'unknown' }

  return (
    <>
      <ScreenHeader
        breadcrumbs={(
          <Breadcrumb>
            <BreadcrumbItem component="button" onClick={onBack}>
              Registry
            </BreadcrumbItem>
            <BreadcrumbItem isActive>{bucket}</BreadcrumbItem>
          </Breadcrumb>
        )}
        title={(
          <span style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
            {bucket}
            {bucketData?.templateTypes.map((templateType) => (
              <Label key={templateType} isCompact>{templateType}</Label>
            ))}
          </span>
        )}
        description={bucketData
          ? bucketData.description || 'No description has been recorded.'
          : null}
        actions={!loading && !failure && !gap ? (
          <RoleRestrictedButton
            action="deleteBuckets"
            callerRole={callerRole}
            variant="danger"
            onClick={() => setDeletingBucket(true)}
          >
            Delete bucket
          </RoleRestrictedButton>
        ) : null}
      >
        {bucketData && Object.keys(bucketData.labels).length > 0 ? (
          <span style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 12 }}>
            {Object.entries(bucketData.labels).map(([key, value]) => (
              <Label key={key} isCompact>{key}={value}</Label>
            ))}
          </span>
        ) : null}
      </ScreenHeader>

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
          <TenancyGapEmptyState gap={gap} callerRole={callerRole} />
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
                    versions={versions}
                    latestVersion={bucketData?.latestVersion ?? null}
                    callerRole={callerRole}
                    onOpenVersion={onOpenVersion}
                    onCreateChannel={onCreateChannel}
                    onAssignChannel={onAssignChannel}
                    onDeleteChannel={onDeleteChannel}
                  />
                ),
              },
            ]}
          />
        )}
      </PageSection>
      {deletingBucket ? (
        <DeleteBucketModal
          bucket={bucket}
          callerRole={callerRole}
          onConfirm={onDeleteBucket}
          onClose={() => setDeletingBucket(false)}
          checkMirrored={onCheckMirrored}
        />
      ) : null}
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
        <Card>
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
                <DescriptionListDescription><When iso={newest?.created} /></DescriptionListDescription>
              </DescriptionListGroup>
              <DescriptionListGroup>
                <DescriptionListTerm>Platforms</DescriptionListTerm>
                <DescriptionListDescription><PlatformList platforms={bucket.platforms} /></DescriptionListDescription>
              </DescriptionListGroup>
              <DescriptionListGroup>
                <DescriptionListTerm>Fingerprint</DescriptionListTerm>
                <DescriptionListDescription>
                  {newest ? (
                    <CopyableIdentifier value={newest.fingerprint} label="Newest version fingerprint" />
                  ) : '—'}
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
        <Card>
          <CardTitle>Ancestry</CardTitle>
          <CardBody style={{ display: 'flex', gap: 32, flexWrap: 'wrap' }}>
            <div style={{ flex: '1 1 220px' }}>
              <Title headingLevel="h3" size="md">
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                  Parents
                  <Popover bodyContent={ANCESTRY_SCOPE}>
                    <Button
                      variant="plain"
                      hasNoPadding
                      aria-label="Parents ancestry scope"
                      icon={<HelpIcon />}
                    />
                  </Popover>
                </span>
              </Title>
              {bucket.parents.length === 0 ? <Content component="p">None</Content> :
                bucket.parents.map((parent) => (
                  <div key={`${parent.bucket}/${parent.fingerprint}/${parent.localVersionName ?? ''}`} style={{ padding: '5px 0' }}>
                    <strong>{parent.bucket} {parent.versionName}</strong>
                    <div style={{ color: parentColor(parent.freshness) }}>
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
              <Title headingLevel="h3" size="md">
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                  Children
                  <Popover bodyContent={ANCESTRY_SCOPE}>
                    <Button
                      variant="plain"
                      hasNoPadding
                      aria-label="Children ancestry scope"
                      icon={<HelpIcon />}
                    />
                  </Popover>
                </span>
              </Title>
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
    <div style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
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
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(RECENT)
  const [expanded, setExpanded] = useState<string | null>(null)
  const lastPage = Math.max(1, Math.ceil(versions.length / perPage))
  const first = (page - 1) * perPage
  const visibleVersions = versionPage(versions, page, perPage)

  useEffect(() => {
    if (page > lastPage) setPage(lastPage)
  }, [lastPage, page])

  const setCurrentPage = (_event: unknown, nextPage: number) => {
    setPage(nextPage)
    setExpanded(null)
  }
  const selectPerPage = (_event: unknown, nextPerPage: number) => {
    setPerPage(nextPerPage)
    setPage(1)
    setExpanded(null)
  }

  return (
    <Card>
        <CardTitle>Versions</CardTitle>
        <CardBody>
          {versions.length === 0 ? (
            <EmptyState titleText="No versions in this bucket" headingLevel="h2">
              <EmptyStateBody>Publish with packer build to create one.</EmptyStateBody>
            </EmptyState>
          ) : (
            <>
              <Toolbar id="versions-toolbar">
                <ToolbarContent>
                  <ToolbarItem variant="pagination" align={{ default: 'alignEnd' }}>
                    <Pagination
                      itemCount={versions.length}
                      page={page}
                      perPage={perPage}
                      onSetPage={setCurrentPage}
                      onPerPageSelect={selectPerPage}
                      isCompact
                    />
                  </ToolbarItem>
                </ToolbarContent>
              </Toolbar>
              <Table aria-label="Versions" variant="compact" isStickyHeader>
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
                        rowIndex: first + index,
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
                        <When iso={version.created} />
                        {version.assignments.map((assignment) => (
                          <div key={assignment.channel} style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
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
                                <CopyableIdentifier value={version.fingerprint} label={`${version.name} fingerprint`} />
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
              <Pagination
                itemCount={versions.length}
                page={page}
                perPage={perPage}
                onSetPage={setCurrentPage}
                onPerPageSelect={selectPerPage}
                variant="bottom"
                dropDirection="up"
              />
            </>
          )}
        </CardBody>
    </Card>
  )
}

export function versionPage(versions: Version[], page: number, perPage: number): Version[] {
  const first = (page - 1) * perPage
  return versions.slice(first, first + perPage)
}

export function BucketChannelsFacet({
  bucket,
  channels,
  versions = [],
  latestVersion,
  callerRole = null,
  onOpenVersion,
  onCreateChannel = () => Promise.reject(new Error('No session.')),
  onAssignChannel = () => Promise.reject(new Error('No session.')),
  onDeleteChannel = () => Promise.reject(new Error('No session.')),
}: {
  bucket: string
  channels: BucketChannel[]
  versions?: Version[]
  latestVersion: BucketPage['latestVersion']
  callerRole?: Role | null
  onOpenVersion: (fingerprint: string) => void
  onCreateChannel?: (options: {
    name: string; restricted?: boolean; fingerprint?: string
  }) => Promise<void>
  onAssignChannel?: (channel: string, fingerprint: string) => Promise<void>
  onDeleteChannel?: (channel: string) => Promise<void>
}) {
  const [expanded, setExpanded] = useState<string | null>(null)
  const [action, setAction] = useState<
    { kind: 'create' } | { kind: 'assign' | 'delete'; channel: BucketChannel } | null
  >(null)
  return (
    <>
      <Card>
        <CardTitle>
          <span style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <span>Channels</span>
            {channels.length > 0 ? (
              <RoleRestrictedButton
                action="manageChannels"
                callerRole={callerRole}
                variant="primary"
                onClick={() => setAction({ kind: 'create' })}
              >
                Create channel
              </RoleRestrictedButton>
            ) : null}
          </span>
        </CardTitle>
        <CardBody style={{ padding: '16px 0 0' }}>
          {channels.length === 0 ? (
            <EmptyState titleText="No channels in this bucket" headingLevel="h2">
              <EmptyStateBody>A channel names a version consumers can resolve.</EmptyStateBody>
              <EmptyStateFooter>
                <EmptyStateActions>
                  <RoleRestrictedButton
                    action="manageChannels"
                    callerRole={callerRole}
                    variant="primary"
                    onClick={() => setAction({ kind: 'create' })}
                  >
                    Create channel
                  </RoleRestrictedButton>
                </EmptyStateActions>
              </EmptyStateFooter>
            </EmptyState>
          ) : (
            <Table aria-label={`${bucket} channels`} variant="compact">
            <Thead>
              <Tr>
                <Th screenReaderText="Row expansion" />
                <Th>Channel</Th>
                <Th>Assigned version</Th>
                <Th>Assigned by</Th>
                <Th>Assigned time</Th>
                <Th screenReaderText="Actions" />
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
                  <Td dataLabel="Assigned time"><When iso={channel.assignedAt} /></Td>
                  <Td dataLabel="Actions">
                    {channel.managed ? null : (
                      <ChannelActions
                        channel={channel}
                        callerRole={callerRole}
                        onAssign={() => setAction({ kind: 'assign', channel })}
                        onDelete={() => setAction({ kind: 'delete', channel })}
                      />
                    )}
                  </Td>
                </Tr>
                <Tr isExpanded={expanded === channel.name}>
                  <Td />
                  <Td dataLabel="Channel detail" colSpan={5}>
                    <ExpandableRowContent>
                      <DescriptionList isHorizontal isCompact style={{ maxWidth: 720 }}>
                        <DescriptionListGroup>
                          <DescriptionListTerm>Fingerprint</DescriptionListTerm>
                          <DescriptionListDescription>
                            {channel.fingerprint ? (
                              <CopyableIdentifier value={channel.fingerprint} label={`${channel.name} fingerprint`} />
                            ) : 'Unassigned'}
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
                              ? 'dufflebag, on version completion'
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
      {action?.kind === 'create' ? (
        <CreateChannelModal
          bucket={bucket}
          versions={versions}
          callerRole={callerRole}
          onConfirm={onCreateChannel}
          onClose={() => setAction(null)}
        />
      ) : null}
      {action?.kind === 'assign' ? (
        <AssignChannelModal
          bucket={bucket}
          channel={action.channel}
          versions={versions}
          callerRole={callerRole}
          onConfirm={(fingerprint) => onAssignChannel(action.channel.name, fingerprint)}
          onClose={() => setAction(null)}
        />
      ) : null}
      {action?.kind === 'delete' ? (
        <DeleteChannelModal
          bucket={bucket}
          channel={action.channel}
          callerRole={callerRole}
          onConfirm={() => onDeleteChannel(action.channel.name)}
          onClose={() => setAction(null)}
        />
      ) : null}
    </>
  )
}

function ChannelActions({
  channel, callerRole, onAssign, onDelete,
}: {
  channel: BucketChannel
  callerRole: Role | null
  onAssign: () => void
  onDelete: () => void
}) {
  const [open, setOpen] = useState(false)
  const refused = !permitsAction(callerRole, 'manageChannels')
  const reason = requirementReason('manageChannels')
  const toggle = (ref: Ref<MenuToggleElement>) => (
    <MenuToggle
      ref={ref}
      variant="plain"
      aria-label={`Actions for ${channel.name}`}
      isExpanded={open}
      isDisabled={refused}
      onClick={() => setOpen(!open)}
    >
      <EllipsisVIcon />
      {refused ? <span className="pf-v6-u-screen-reader"> — {reason}</span> : null}
    </MenuToggle>
  )
  return (
    <Dropdown
      isOpen={open}
      onOpenChange={setOpen}
      onSelect={() => setOpen(false)}
      toggle={(ref) => refused ? (
        <Tooltip content={reason}>
          <span tabIndex={0} aria-label={reason} style={{ display: 'inline-block' }}>
            {toggle(ref)}
          </span>
        </Tooltip>
      ) : toggle(ref)}
    >
      <DropdownList>
        <DropdownItem
          isDisabled={refused}
          tooltipProps={refused ? { content: reason } : undefined}
          onClick={onAssign}
        >
          Assign version…
          {refused ? <span className="pf-v6-u-screen-reader"> — {reason}</span> : null}
        </DropdownItem>
        <DropdownItem
          isDisabled={refused}
          tooltipProps={refused ? { content: reason } : undefined}
          onClick={onDelete}
        >
          Delete channel
          {refused ? <span className="pf-v6-u-screen-reader"> — {reason}</span> : null}
        </DropdownItem>
      </DropdownList>
    </Dropdown>
  )
}

function CreateChannelModal({
  bucket, versions, callerRole, onConfirm, onClose,
}: {
  bucket: string
  versions: Version[]
  callerRole: Role | null
  onConfirm: (options: {
    name: string; restricted?: boolean; fingerprint?: string
  }) => Promise<void>
  onClose: () => void
}) {
  const [name, setName] = useState('')
  const [restricted, setRestricted] = useState(false)
  const [fingerprint, setFingerprint] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const confirm = async (options: {
    name: string; restricted?: boolean; fingerprint?: string
  }) => {
    setSubmitting(true)
    setFailure(null)
    try {
      await onConfirm(options)
      onClose()
    } catch (err: unknown) {
      setFailure(err instanceof Error ? err.message : 'The action failed.')
    } finally {
      setSubmitting(false)
    }
  }
  return (
    <Modal aria-labelledby="create-channel-modal-title" isOpen onClose={onClose} variant="small">
      <CreateChannelModalView
        bucket={bucket}
        versions={versions}
        callerRole={callerRole}
        name={name}
        restricted={restricted}
        fingerprint={fingerprint}
        submitting={submitting}
        failure={failure}
        onNameChange={setName}
        onRestrictedChange={setRestricted}
        onFingerprintChange={setFingerprint}
        onConfirm={confirm}
        onClose={onClose}
      />
    </Modal>
  )
}

// Only active, complete versions are offered for assignment: a revoked or
// revocation-scheduled version is a version being pulled OUT of consumption
// (Ben's ruling, duf-awx8). The server stays the arbiter at the wire.
function completeChannelVersions(versions: Version[]): Version[] {
  return versions.filter((version) => version.state === 'complete')
}

export function CreateChannelModalView({
  bucket, versions, callerRole, name, restricted, fingerprint, submitting, failure,
  onNameChange, onRestrictedChange, onFingerprintChange, onConfirm, onClose,
}: {
  bucket: string
  versions: Version[]
  callerRole: Role | null
  name: string
  restricted: boolean
  fingerprint: string
  submitting: boolean
  failure: string | null
  onNameChange: (name: string) => void
  onRestrictedChange: (restricted: boolean) => void
  onFingerprintChange: (fingerprint: string) => void
  onConfirm: (options: {
    name: string; restricted?: boolean; fingerprint?: string
  }) => Promise<void>
  onClose: () => void
}) {
  const completeVersions = completeChannelVersions(versions)
  return (
    <>
      <ModalHeader labelId="create-channel-modal-title" title={`Create channel in ${bucket}`} />
      <ModalBody>
        {failure ? (
          <Alert variant="danger" isInline title="The action was refused">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        <Form>
          <FormGroup label="Name" isRequired fieldId="channel-name">
            <TextInput
              id="channel-name"
              value={name}
              onChange={(_event, value) => onNameChange(value)}
            />
          </FormGroup>
          <Checkbox
            id="channel-restricted"
            label="Restricted"
            description="Only explicitly authorised consumers can use this channel."
            isChecked={restricted}
            onChange={(_event, checked) => onRestrictedChange(checked)}
          />
          <FormGroup label="Initial version" fieldId="channel-initial-version">
            <FormSelect
              id="channel-initial-version"
              value={fingerprint}
              onChange={(_event, value) => onFingerprintChange(value)}
            >
              <FormSelectOption value="" label="None" />
              {completeVersions.map((version) => (
                <FormSelectOption
                  key={version.fingerprint}
                  value={version.fingerprint}
                  label={version.name}
                />
              ))}
            </FormSelect>
          </FormGroup>
        </Form>
      </ModalBody>
      <ModalFooter>
        <RoleRestrictedButton
          action="manageChannels"
          callerRole={callerRole}
          variant="primary"
          isLoading={submitting}
          isDisabled={submitting || name.trim() === ''}
          onClick={() => onConfirm({
            name: name.trim(),
            ...(restricted ? { restricted: true } : {}),
            ...(fingerprint ? { fingerprint } : {}),
          })}
        >
          Create channel
        </RoleRestrictedButton>
        <Button variant="link" isDisabled={submitting} onClick={onClose}>Cancel</Button>
      </ModalFooter>
    </>
  )
}

function AssignChannelModal({
  bucket, channel, versions, callerRole, onConfirm, onClose,
}: {
  bucket: string
  channel: BucketChannel
  versions: Version[]
  callerRole: Role | null
  onConfirm: (fingerprint: string) => Promise<void>
  onClose: () => void
}) {
  const completeVersions = completeChannelVersions(versions)
  const currentIsOffered = completeVersions.some(
    (version) => version.fingerprint === channel.fingerprint,
  )
  const [fingerprint, setFingerprint] = useState(
    currentIsOffered ? channel.fingerprint : (completeVersions[0]?.fingerprint ?? ''),
  )
  const [submitting, setSubmitting] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const confirm = async (selected: string) => {
    setSubmitting(true)
    setFailure(null)
    try {
      await onConfirm(selected)
      onClose()
    } catch (err: unknown) {
      setFailure(err instanceof Error ? err.message : 'The action failed.')
    } finally {
      setSubmitting(false)
    }
  }
  return (
    <Modal aria-labelledby="assign-channel-modal-title" isOpen onClose={onClose} variant="small">
      <AssignChannelModalView
        bucket={bucket}
        channel={channel}
        versions={versions}
        callerRole={callerRole}
        fingerprint={fingerprint}
        submitting={submitting}
        failure={failure}
        onFingerprintChange={setFingerprint}
        onConfirm={confirm}
        onClose={onClose}
      />
    </Modal>
  )
}

export function AssignChannelModalView({
  bucket, channel, versions, callerRole, fingerprint, submitting, failure,
  onFingerprintChange, onConfirm, onClose,
}: {
  bucket: string
  channel: BucketChannel
  versions: Version[]
  callerRole: Role | null
  fingerprint: string
  submitting: boolean
  failure: string | null
  onFingerprintChange: (fingerprint: string) => void
  onConfirm: (fingerprint: string) => Promise<void>
  onClose: () => void
}) {
  const completeVersions = completeChannelVersions(versions)
  return (
    <>
      <ModalHeader
        labelId="assign-channel-modal-title"
        title={`Assign ${bucket} version — ${channel.name}`}
      />
      <ModalBody>
        {failure ? (
          <Alert variant="danger" isInline title="The action was refused">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        <Form>
          <FormGroup label="Version" isRequired fieldId="channel-version">
            <FormSelect
              id="channel-version"
              value={fingerprint}
              onChange={(_event, value) => onFingerprintChange(value)}
            >
              {completeVersions.length === 0 ? (
                <FormSelectOption value="" label="No complete versions" isDisabled />
              ) : completeVersions.map((version) => {
                const current = version.fingerprint === channel.fingerprint
                return (
                  <FormSelectOption
                    key={version.fingerprint}
                    value={version.fingerprint}
                    label={`${version.name}${current ? ' (current)' : ''}`}
                    isDisabled={current}
                  />
                )
              })}
            </FormSelect>
          </FormGroup>
        </Form>
      </ModalBody>
      <ModalFooter>
        <RoleRestrictedButton
          action="manageChannels"
          callerRole={callerRole}
          variant="primary"
          isLoading={submitting}
          isDisabled={submitting || fingerprint === '' || fingerprint === channel.fingerprint}
          onClick={() => onConfirm(fingerprint)}
        >
          Assign version
        </RoleRestrictedButton>
        <Button variant="link" isDisabled={submitting} onClick={onClose}>Cancel</Button>
      </ModalFooter>
    </>
  )
}

function DeleteChannelModal({
  bucket, channel, callerRole, onConfirm, onClose,
}: {
  bucket: string
  channel: BucketChannel
  callerRole: Role | null
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  const [submitting, setSubmitting] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const confirm = async () => {
    setSubmitting(true)
    setFailure(null)
    try {
      await onConfirm()
      onClose()
    } catch (err: unknown) {
      setFailure(err instanceof Error ? err.message : 'The action failed.')
    } finally {
      setSubmitting(false)
    }
  }
  return <DeleteChannelModalView
    bucket={bucket}
    channel={channel}
    callerRole={callerRole}
    submitting={submitting}
    failure={failure}
    onConfirm={confirm}
    onClose={onClose}
  />
}

export function DeleteChannelModalView({
  bucket, channel, submitting, failure, onConfirm, onClose,
}: {
  bucket: string
  channel: BucketChannel
  callerRole: Role | null
  submitting: boolean
  failure: string | null
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  return (
    <TypedConfirmModal
      title={`Delete ${bucket} — ${channel.name}`}
      expected={channel.name}
      verb={`Delete ${channel.name}`}
      busy={submitting}
      onConfirm={onConfirm}
      onCancel={onClose}
      body={<>
        {failure ? (
          <Alert variant="danger" isInline title="The action was refused">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        <Content component="p">
          Deleting {channel.name} destroys its assignment history.
        </Content>
      </>}
    />
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
      <Title headingLevel="h3" size="md">
        Assignment history
      </Title>
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
            <Td dataLabel="Assigned time"><When iso={assignment.assignedAt} /></Td>
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
      return 'var(--pf-t--global--text--color--status--success--default)'
    case 'behind':
      return 'var(--pf-t--global--text--color--status--warning--default)'
    case 'unknown':
      return 'var(--pf-t--global--text--color--subtle)'
  }
}

export function VersionStateLabel({ state }: { state: Version['state'] }) {
  // Completion restates the name's meaning — v0-incomplete / vN-complete —
  // while the revocation states carry the wire status packer itself refuses on.
  switch (state) {
    case 'complete':
      return <Label isCompact status="success">complete</Label>
    case 'incomplete':
      return <Label isCompact status="info">incomplete</Label>
    case 'revoked':
      return <Label isCompact status="danger">revoked</Label>
    case 'revocation-scheduled':
      return <Label isCompact status="warning">revocation scheduled</Label>
  }
}
