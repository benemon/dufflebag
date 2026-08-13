import { useEffect, useState, type Ref } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardFooter, CardTitle, Content, Gallery, GalleryItem, Label,
  Dropdown, DropdownItem, DropdownList, FormSelect, FormSelectOption, MenuToggle,
  EmptyState, EmptyStateActions, EmptyStateBody, EmptyStateFooter, Hint, HintBody,
  LabelGroup, PageSection, Pagination, SearchInput, Title, Toolbar,
  ToolbarContent, ToolbarFilter, ToolbarItem,
} from '@patternfly/react-core'
import type { MenuToggleElement } from '@patternfly/react-core'
import {
  ExpandableRowContent, SortByDirection, Table, Tbody, Td, Th, Thead, Tr,
} from '@patternfly/react-table'
import EllipsisVIcon from '@patternfly/react-icons/dist/esm/icons/ellipsis-v-icon'
import TimesIcon from '@patternfly/react-icons/dist/esm/icons/times-icon'
import { useLocation, useNavigate } from 'react-router'

import { PlatformList } from '../components/PlatformLabel'
import { DeleteBucketModal } from '../components/DeleteBucketModal'
import { TenancyGapEmptyState } from '../components/TenancyCreation'
import { When } from '../components/When'
import { useBuckets, type AncestryLink, type Bucket } from '../data/buckets'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, requirementReason, type Role } from '../auth/permissions'
import { deleteBucket, getBagDropStatus, signOutIfUnauthorized, type ApiPin } from '../api/client'
import type { TenancyGap } from '../data/tenant'
import { VersionStateLabel } from './Versions'

const BUCKET_SORT_KEYS = {
  1: 'name',
  3: 'status',
  7: 'last-push',
} as const
type BucketSortIndex = keyof typeof BUCKET_SORT_KEYS
const VERSION_STATE_ORDER: NonNullable<Bucket['newestVersion']>['state'][] = [
  'complete', 'incomplete', 'revoked', 'revocation-scheduled',
]

/**
 * Buckets — the registry landing screen.
 *
 * Pins are shared project presentation state. Pinned buckets remain in the
 * complete table; their cards are a second, focused way into the same data.
 */
export function Buckets() {
  const location = useLocation()
  const navigate = useNavigate()
  const [refresh, setRefresh] = useState(0)
  const bucketData = useBuckets(`${location.key}:${refresh}`)
  const { state, self, selectedOrganization, selectedProject, signOut } = useAuth()
  const tenant = state && selectedOrganization && selectedProject
    ? { organizationID: selectedOrganization, projectID: selectedProject }
    : null
  return (
    <BucketsView
      {...bucketData}
      callerRole={self?.role ?? null}
      openBucket={(bucket) => navigate(`/buckets/${encodeURIComponent(bucket)}`)}
      openInstance={() => navigate('/instance')}
      onDeleteBucket={async (bucket) => {
        if (!state || !tenant) throw new Error('No session.')
        try {
          await deleteBucket(state.token, tenant, bucket)
          setRefresh((current) => current + 1)
        } catch (err: unknown) {
          signOutIfUnauthorized(err, signOut)
          throw err
        }
      }}
      onCheckMirrored={async (bucket) => {
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

export function BucketsView({
  buckets,
  total,
  loading,
  failure,
  pins = [],
  pinsLoading = false,
  pinsFailure = null,
  canPin = false,
  togglePin = async () => {},
  callerRole = null,
  gap,
  openBucket,
  openInstance = () => {},
  onDeleteBucket = () => Promise.reject(new Error('No session.')),
  onCheckMirrored,
}: {
  buckets: Bucket[]
  total: number
  loading: boolean
  failure: string | null
  pins?: ApiPin[]
  pinsLoading?: boolean
  pinsFailure?: string | null
  canPin?: boolean
  togglePin?: (bucketName: string, pinned: boolean) => Promise<void>
  callerRole?: Role | null
  /** A platform session with no tenancy chosen yet — stated, never fetched around. */
  gap?: TenancyGap | null
  /** Navigation into a bucket's versions; a callback so the view stays router-free. */
  openBucket: (bucket: string) => void
  /** Navigation to the client connection details; a callback so the view stays router-free. */
  openInstance?: () => void
  onDeleteBucket?: (bucket: string) => Promise<void>
  onCheckMirrored?: (bucket: string) => Promise<boolean>
}) {
  const [expanded, setExpanded] = useState<string | null>(null)
  const [nameFilter, setNameFilter] = useState('')
  const [platformFilter, setPlatformFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState('')
  // Newest first: a registry's most recent push is what a reader came for.
  const [activeSortIndex, setActiveSortIndex] = useState<BucketSortIndex>(7)
  const [activeSortDirection, setActiveSortDirection] = useState(SortByDirection.desc)
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const [deletingBucket, setDeletingBucket] = useState<string | null>(null)
  const [showConnectionHint, setShowConnectionHint] = useState(true)
  const platforms = [...new Set(buckets.flatMap((bucket) => bucket.platforms))].sort()
  const states = VERSION_STATE_ORDER.filter((state) =>
    buckets.some((bucket) => bucket.newestVersion?.state === state),
  )
  const sortKey = BUCKET_SORT_KEYS[activeSortIndex]
  const comparator = bucketComparator(sortKey)
  const naturalDirection = sortKey === 'last-push' ? SortByDirection.desc : SortByDirection.asc
  const filteredBuckets = buckets
    .filter((bucket) => bucket.name.toLowerCase().includes(nameFilter.trim().toLowerCase()))
    .filter((bucket) => !platformFilter || bucket.platforms.includes(platformFilter))
    .filter((bucket) => !statusFilter || bucket.newestVersion?.state === statusFilter)
    .sort((a, b) => activeSortDirection === naturalDirection
      ? comparator(a, b)
      : -comparator(a, b))
  const filteredTotal = filteredBuckets.length
  const lastPage = Math.max(1, Math.ceil(filteredTotal / perPage))
  const first = (page - 1) * perPage
  const visibleBuckets = filteredBuckets.slice(first, first + perPage)
  const pinnedBuckets = pins.flatMap((pin) => {
    const bucket = buckets.find((candidate) => candidate.name === pin.bucket_name)
    return bucket ? [bucket] : []
  })
  const awaitingFirstCompletedVersion = buckets.length > 0 && buckets.every(
    (bucket) => !bucket.newestVersion || bucket.newestVersion.state === 'incomplete',
  )

  useEffect(() => {
    if (page > lastPage) setPage(lastPage)
  }, [lastPage, page])

  const clearAllFilters = () => {
    setNameFilter('')
    setPlatformFilter('')
    setStatusFilter('')
    setPage(1)
  }
  const getSortParams = (columnIndex: BucketSortIndex) => ({
    sortBy: { index: activeSortIndex, direction: activeSortDirection },
    onSort: (_event: React.MouseEvent, index: number, direction: SortByDirection) => {
      setActiveSortIndex(index as BucketSortIndex)
      setActiveSortDirection(direction)
      setPage(1)
    },
    columnIndex,
  })

  return (
    <>
      <PageSection variant="default" padding={{ default: 'padding' }}>
        <Breadcrumb>
          <BreadcrumbItem to="#" isActive>
          Project registry
          </BreadcrumbItem>
        </Breadcrumb>
        <Title headingLevel="h1" size="2xl">
          Buckets
        </Title>
        {!loading && !failure && !gap && (
          <Content component="p">
            {total} {total === 1 ? 'bucket' : 'buckets'} ·{' '}
            {buckets.reduce((count, bucket) => count + bucket.versionCount, 0)} versions ·{' '}
            {buckets.reduce((count, bucket) => count + bucket.channels.length, 0)} channels
          </Content>
        )}
      </PageSection>

      <PageSection variant="secondary" isFilled>
        {pinsFailure && (
          <Alert variant="warning" isInline title="Pinned buckets could not be loaded">
            <Content component="p">{pinsFailure}</Content>
          </Alert>
        )}
        {pinsLoading && !loading && <Content component="p">Loading pinned buckets…</Content>}
        {!loading && pinnedBuckets.length > 0 && (
          <section aria-label="Pinned buckets" style={{ marginBottom: 24 }}>
            <Title headingLevel="h2" size="lg" style={{ marginBottom: 12 }}>Pinned buckets</Title>
            <Gallery hasGutter minWidths={{ default: '260px' }}>
              {pinnedBuckets.map((bucket) => (
                <GalleryItem key={bucket.name}>
                  <Card isCompact>
                    <CardTitle>
                      <Button variant="link" isInline onClick={() => openBucket(bucket.name)}>
                        {bucket.name}
                      </Button>
                    </CardTitle>
                    <CardBody>
                      <Content component="p">
                        Newest version: {bucket.newestVersion?.name ?? '—'}
                      </Content>
                      <PlatformList platforms={bucket.platforms} />
                      <Content component="p">Last updated: <When iso={bucket.lastPushAt} /></Content>
                    </CardBody>
                    {canPin ? (
                      <CardFooter>
                        <Button
                          variant="link" isInline aria-label={`Unpin ${bucket.name}`}
                          onClick={() => { void togglePin(bucket.name, true) }}
                        >
                          Unpin
                        </Button>
                      </CardFooter>
                    ) : null}
                  </Card>
                </GalleryItem>
              ))}
            </Gallery>
          </section>
        )}
        <Card>
          <CardTitle>All buckets</CardTitle>
          <CardBody>
            {loading ? (
              <Content component="p">Loading buckets…</Content>
            ) : failure ? (
              <Alert variant="danger" isInline title="Buckets could not be loaded">
                <Content component="p">{failure}</Content>
              </Alert>
            ) : gap ? (
              <TenancyGapEmptyState gap={gap} callerRole={callerRole} />
            ) : buckets.length === 0 ? (
              <EmptyState titleText="No buckets yet" headingLevel="h2">
                <EmptyStateBody>
                  Buckets appear when Packer publishes a version to this project.
                </EmptyStateBody>
                <EmptyStateFooter>
                  <EmptyStateActions>
                    <Button variant="primary" onClick={openInstance}>Connect a client</Button>
                  </EmptyStateActions>
                  <EmptyStateActions>
                    <Button
                      component="a"
                      href="https://developer.hashicorp.com/packer/docs/hcp"
                      target="_blank"
                      variant="link"
                    >
                      Packer HCP docs
                    </Button>
                  </EmptyStateActions>
                </EmptyStateFooter>
              </EmptyState>
            ) : (
              <>
                {showConnectionHint && awaitingFirstCompletedVersion ? (
                  <Hint
                    actions={(
                      <Button
                        variant="plain"
                        aria-label="Dismiss client connection hint"
                        icon={<TimesIcon />}
                        onClick={() => setShowConnectionHint(false)}
                      />
                    )}
                  >
                    <HintBody>
                      Waiting on a first build? The Instance screen has the client environment block
                      that points Packer here.{' '}
                      <Button variant="link" isInline onClick={openInstance}>Open Instance</Button>
                    </HintBody>
                  </Hint>
                ) : null}
                <Toolbar id="buckets-toolbar" clearAllFilters={clearAllFilters}>
                  <ToolbarContent>
                    <ToolbarItem>
                      <SearchInput
                        aria-label="Filter buckets by name"
                        placeholder="Filter by name"
                        value={nameFilter}
                        onChange={(_event, value) => {
                          setNameFilter(value)
                          setPage(1)
                        }}
                        onClear={() => {
                          setNameFilter('')
                          setPage(1)
                        }}
                      />
                    </ToolbarItem>
                    <ToolbarFilter
                      categoryName="Platform"
                      labels={platformFilter ? [platformFilter] : []}
                      deleteLabel={() => {
                        setPlatformFilter('')
                        setPage(1)
                      }}
                    >
                      <FormSelect
                        aria-label="Filter buckets by platform"
                        value={platformFilter}
                        onChange={(_event, value) => {
                          setPlatformFilter(value)
                          setPage(1)
                        }}
                      >
                        <FormSelectOption value="" label="All platforms" />
                        {platforms.map((platform) => (
                          <FormSelectOption key={platform} value={platform} label={platform} />
                        ))}
                      </FormSelect>
                    </ToolbarFilter>
                    <ToolbarFilter
                      categoryName="State"
                      labels={statusFilter ? [statusFilter.replace('-', ' ')] : []}
                      deleteLabel={() => {
                        setStatusFilter('')
                        setPage(1)
                      }}
                    >
                      <FormSelect
                        aria-label="Filter buckets by status"
                        value={statusFilter}
                        onChange={(_event, value) => {
                          setStatusFilter(value)
                          setPage(1)
                        }}
                      >
                        <FormSelectOption value="" label="All states" />
                        {/* Only states the listing actually contains: a filter
                            for a state nothing carries returns an empty table
                            and reads as a fault. */}
                        {states.map((state) => (
                          <FormSelectOption
                            key={state}
                            value={state}
                            label={state.charAt(0).toUpperCase() + state.slice(1).replace('-', ' ')}
                          />
                        ))}
                      </FormSelect>
                    </ToolbarFilter>
                    <ToolbarItem variant="pagination" align={{ default: 'alignEnd' }}>
                      <Pagination
                        itemCount={filteredTotal}
                        page={page}
                        perPage={perPage}
                        onSetPage={(_e, p) => setPage(p)}
                        onPerPageSelect={(_e, pp) => {
                          setPerPage(pp)
                          setPage(1)
                        }}
                        isCompact
                      />
                    </ToolbarItem>
                  </ToolbarContent>
                </Toolbar>

                {filteredTotal === 0 ? (
                  <EmptyState titleText="No buckets match these filters" headingLevel="h2">
                    <EmptyStateBody>
                      No buckets match the filters currently applied.
                    </EmptyStateBody>
                    <EmptyStateFooter>
                      <EmptyStateActions>
                        <Button
                          variant="primary"
                          onClick={clearAllFilters}
                        >
                          Clear all filters
                        </Button>
                      </EmptyStateActions>
                    </EmptyStateFooter>
                  </EmptyState>
                ) : <Table aria-label="Buckets" variant="compact">
                  <Thead>
                    <Tr>
                      <Th screenReaderText="Row expansion" />
                      <Th sort={getSortParams(1)}>Bucket name</Th>
                      <Th>Channels</Th>
                      <Th sort={getSortParams(3)}>Newest version</Th>
                      <Th>Parents</Th>
                      <Th>Children</Th>
                      <Th>Platforms</Th>
                      <Th sort={getSortParams(7)}>Last updated</Th>
                      <Th screenReaderText="Actions" />
                    </Tr>
                  </Thead>
                  {visibleBuckets.map((bucket, rowIndex) => (
                    <Tbody key={bucket.name} isExpanded={expanded === bucket.name}>
                      <Tr>
                        <Td
                          expand={{
                            rowIndex: first + rowIndex,
                            isExpanded: expanded === bucket.name,
                            onToggle: () =>
                              setExpanded(expanded === bucket.name ? null : bucket.name),
                            expandId: `bucket-${bucket.name}`,
                          }}
                        />
                        <Td dataLabel="Bucket">
                          <div>
                            <Button
                              variant="link"
                              isInline
                              onClick={() => openBucket(bucket.name)}
                            >
                              {bucket.name}
                            </Button>
                          </div>
                        </Td>
                        <Td dataLabel="Channels">
                          {bucket.channels.length === 0 ? '—' : (
                            <LabelGroup
                              aria-label={`Channels for ${bucket.name}`}
                              numLabels={3}
                            >
                              {bucket.channels.map((channel) => (
                                <Label key={channel.name} isCompact>
                                  {channel.name} {channel.versionName}
                                </Label>
                              ))}
                            </LabelGroup>
                          )}
                        </Td>
                        <Td dataLabel="Newest version">
                          {bucket.newestVersion ? (
                            <>
                              <Button variant="link" isInline onClick={() => openBucket(bucket.name)}>
                                {bucket.newestVersion.name}
                              </Button>{' '}
                              <VersionStateLabel state={bucket.newestVersion.state} />
                            </>
                          ) : '—'}
                        </Td>
                        <Td dataLabel="Parents">
                          <RelationshipLabel
                            relation={bucket.parents}
                            inOlderVersions={bucket.parentsInOlderVersions}
                          />
                        </Td>
                        <Td dataLabel="Children">
                          <RelationshipLabel
                            relation={bucket.children}
                            inOlderVersions={bucket.childrenInOlderVersions}
                          />
                        </Td>
                        <Td dataLabel="Platforms"><PlatformList platforms={bucket.platforms} /></Td>
                        <Td dataLabel="Last updated"><When iso={bucket.lastPushAt} /></Td>
                        <Td dataLabel="Actions">
                          <BucketActions
                            bucket={bucket.name}
                            openBucket={openBucket}
                            pinned={pins.some((pin) => pin.bucket_name === bucket.name)}
                            canPin={canPin}
                            togglePin={togglePin}
                            callerRole={callerRole}
                            onDelete={() => setDeletingBucket(bucket.name)}
                          />
                        </Td>
                      </Tr>
                      <Tr isExpanded={expanded === bucket.name}>
                        <Td />
                        <Td dataLabel="Detail" colSpan={8}>
                          <ExpandableRowContent>
                            <BucketDetail bucket={bucket} />
                          </ExpandableRowContent>
                        </Td>
                      </Tr>
                    </Tbody>
                  ))}
                </Table>}
              </>
            )}
          </CardBody>
        </Card>
      </PageSection>
      {deletingBucket ? (
        <DeleteBucketModal
          bucket={deletingBucket}
          callerRole={callerRole}
          onConfirm={() => onDeleteBucket(deletingBucket)}
          onClose={() => setDeletingBucket(null)}
          checkMirrored={
            onCheckMirrored ? () => onCheckMirrored(deletingBucket) : undefined
          }
        />
      ) : null}
    </>
  )
}

function BucketDetail({ bucket }: { bucket: Bucket }) {
  return (
    <>
      <Content component="p">{bucket.description || 'No description has been recorded.'}</Content>
      <div style={{ display: 'flex', gap: 32, flexWrap: 'wrap', marginTop: 12 }}>
        <div style={{ flex: '1 1 300px' }}>
          <Title headingLevel="h3" size="md">Channels</Title>
          {bucket.channels.length === 0 ? (
            <Content component="p">None</Content>
          ) : bucket.channels.map((channel) => (
            <div
              key={channel.name}
              style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '5px 0' }}
            >
              <span style={{ minWidth: 110 }}>
                {channel.name}{' '}
                {channel.restricted && <Label isCompact color="grey">restricted</Label>}
              </span>
              <span>{channel.versionName}</span>
              <span style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
                {channelGap(channel.fingerprint, bucket)}
              </span>
            </div>
          ))}
        </div>
        <div style={{ flex: '0 1 260px' }}>
          <Title headingLevel="h3" size="md">Labels</Title>
          <span style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            {Object.entries(bucket.labels).length === 0 ? 'None' :
              Object.entries(bucket.labels).map(([key, value]) => (
                <Label key={key} isCompact>{key}={value}</Label>
              ))}
          </span>
          <Title headingLevel="h3" size="md" style={{ marginTop: 14 }}>
            Template type
          </Title>
          {bucket.templateTypes.length === 0 ? '—' : bucket.templateTypes.map((templateType) => (
            <Label key={templateType} isCompact>{templateType}</Label>
          ))}
        </div>
      </div>
    </>
  )
}

function channelGap(fingerprint: string, bucket: Bucket): string {
  if (!bucket.newestVersion) return '—'
  return fingerprint && fingerprint === bucket.newestVersion.fingerprint
    ? 'newest'
    : `newest ${bucket.newestVersion.name}`
}

/**
 * The cell has three states, and all three render (duf-okej.11): a status pill
 * for the newest version's ancestry, "other versions" when only versions outside
 * the status link's scope carry any, and "—" only when the whole bucket has none. Without
 * the middle state a dash reads as "nothing is built from this", which is false.
 */
function RelationshipLabel({
  relation,
  inOlderVersions,
}: {
  relation: AncestryLink | null
  inOlderVersions: boolean
}) {
  if (!relation) {
    if (inOlderVersions) return <Label isCompact color="grey">other versions</Label>
    return <>—</>
  }
  switch (relation.status) {
    case 'UP_TO_DATE':
      return <Label isCompact color="green">up to date</Label>
    case 'OUT_OF_DATE':
      return <Label isCompact color="yellow">out of date</Label>
    case 'UNDETERMINED':
      return <Label isCompact color="grey">unknown</Label>
  }
}

function BucketActions({
  bucket,
  openBucket,
  pinned,
  canPin,
  togglePin,
  callerRole,
  onDelete,
}: {
  bucket: string
  openBucket: (bucket: string) => void
  pinned: boolean
  canPin: boolean
  togglePin: (bucketName: string, pinned: boolean) => Promise<void>
  callerRole: Role | null
  onDelete: () => void
}) {
  const [open, setOpen] = useState(false)
  const pinAction = pinBucketAction(canPin)
  const deleteAction = deleteBucketAction(callerRole)
  return (
    <Dropdown
      isOpen={open}
      onOpenChange={setOpen}
      onSelect={() => setOpen(false)}
      toggle={(ref: Ref<MenuToggleElement>) => (
        <MenuToggle
          ref={ref}
          variant="plain"
          aria-label={`Actions for ${bucket}`}
          isExpanded={open}
          onClick={() => setOpen(!open)}
        >
          <EllipsisVIcon />
        </MenuToggle>
      )}
    >
      <DropdownList>
        <DropdownItem onClick={() => openBucket(bucket)}>Open bucket</DropdownItem>
        <DropdownItem
          isSelected={pinned}
          isDisabled={pinAction.disabled}
          onClick={() => { void togglePin(bucket, pinned) }}
        >
          {pinAction.label}
        </DropdownItem>
        <DropdownItem
          isDanger
          isDisabled={deleteAction.disabled}
          onClick={onDelete}
        >
          {deleteAction.label}
        </DropdownItem>
      </DropdownList>
    </Dropdown>
  )
}

export function deleteBucketAction(callerRole: Role | null): { disabled: boolean; label: string } {
  const allowed = permitsAction(callerRole, 'deleteBuckets')
  return {
    disabled: !allowed,
    label: `Delete bucket…${allowed ? '' : ` — ${requirementReason('deleteBuckets')}`}`,
  }
}

export function pinBucketAction(canPin: boolean): { disabled: boolean; label: string } {
  return {
    disabled: !canPin,
    label: `Pin bucket${canPin ? '' : ` — ${requirementReason('pinBuckets')}`}`,
  }
}

export function bucketComparator(sort: string): (a: Bucket, b: Bucket) => number {
  switch (sort) {
    case 'status':
      return (a, b) => (a.newestVersion
        ? VERSION_STATE_ORDER.indexOf(a.newestVersion.state) : -1) -
        (b.newestVersion ? VERSION_STATE_ORDER.indexOf(b.newestVersion.state) : -1) ||
        a.name.localeCompare(b.name)
    case 'last-push':
      // The full timestamp, never the truncated display value: everything
      // pushed on one day would otherwise compare equal and fall to the name.
      return (a, b) => b.lastPushAt.localeCompare(a.lastPushAt) || a.name.localeCompare(b.name)
    default:
      return (a, b) => a.name.localeCompare(b.name)
  }
}
