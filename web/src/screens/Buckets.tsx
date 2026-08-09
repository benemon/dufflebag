import { useEffect, useState, type Ref } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Content, Gallery, GalleryItem, Label,
  Dropdown, DropdownItem, DropdownList, FormSelect, FormSelectOption, MenuToggle,
  PageSection, Pagination, TextInput, Title, Toolbar,
  ToolbarContent, ToolbarItem,
} from '@patternfly/react-core'
import type { MenuToggleElement } from '@patternfly/react-core'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import EllipsisVIcon from '@patternfly/react-icons/dist/esm/icons/ellipsis-v-icon'
import { useLocation, useNavigate } from 'react-router'

import { PlatformList } from '../components/PlatformLabel'
import { useBuckets, type AncestryLink, type Bucket } from '../data/buckets'
import { requirementReason } from '../auth/permissions'
import type { ApiPin } from '../api/client'
import type { TenancyGap } from '../data/tenant'

/**
 * Buckets — the registry landing screen.
 *
 * Pins are shared project presentation state. Pinned buckets remain in the
 * complete table; their cards are a second, focused way into the same data.
 */
export function Buckets() {
  const location = useLocation()
  const navigate = useNavigate()
  return (
    <BucketsView
      {...useBuckets(location.key)}
      openBucket={(bucket) => navigate(`/buckets/${encodeURIComponent(bucket)}`)}
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
  gap,
  openBucket,
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
  /** A platform session with no tenancy chosen yet — stated, never fetched around. */
  gap?: TenancyGap | null
  /** Navigation into a bucket's versions; a callback so the view stays router-free. */
  openBucket: (bucket: string) => void
}) {
  const [expanded, setExpanded] = useState<string | null>(null)
  const [nameFilter, setNameFilter] = useState('')
  const [platformFilter, setPlatformFilter] = useState('')
  const [statusFilter, setStatusFilter] = useState('')
  // Newest first: a registry's most recent push is what a reader came for.
  const [sort, setSort] = useState('last-push')
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const platforms = [...new Set(buckets.flatMap((bucket) => bucket.platforms))].sort()
  const filteredBuckets = buckets
    .filter((bucket) => bucket.name.toLowerCase().includes(nameFilter.trim().toLowerCase()))
    .filter((bucket) => !platformFilter || bucket.platforms.includes(platformFilter))
    .filter((bucket) => !statusFilter || bucket.newestVersion?.state === statusFilter)
    .sort(bucketComparator(sort))
  const filteredTotal = filteredBuckets.length
  const lastPage = Math.max(1, Math.ceil(filteredTotal / perPage))
  const first = (page - 1) * perPage
  const visibleBuckets = filteredBuckets.slice(first, first + perPage)
  const pinnedBuckets = pins.flatMap((pin) => {
    const bucket = buckets.find((candidate) => candidate.name === pin.bucket_name)
    return bucket ? [bucket] : []
  })

  useEffect(() => {
    if (page > lastPage) setPage(lastPage)
  }, [lastPage, page])

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
                      <Content component="p">Last updated: {bucket.lastPush}</Content>
                    </CardBody>
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
              <Alert variant="info" isInline title={gap.title}>
                <Content component="p">{gap.detail}</Content>
              </Alert>
            ) : buckets.length === 0 ? (
              <Alert variant="info" isInline title="No buckets in this project" />
            ) : (
              <>
                <Toolbar id="buckets-toolbar">
                  <ToolbarContent>
                    <ToolbarItem>
                      <TextInput
                        aria-label="Filter buckets by name"
                        placeholder="Filter by name"
                        value={nameFilter}
                        onChange={(_event, value) => {
                          setNameFilter(value)
                          setPage(1)
                        }}
                      />
                    </ToolbarItem>
                    <ToolbarItem>
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
                    </ToolbarItem>
                    <ToolbarItem>
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
                        {[...new Set(buckets.map((b) => b.newestVersion?.state ?? ''))]
                          .filter(Boolean).sort().map((state) => (
                            <FormSelectOption
                              key={state}
                              value={state}
                              label={state.charAt(0).toUpperCase() + state.slice(1).replace('-', ' ')}
                            />
                          ))}
                      </FormSelect>
                    </ToolbarItem>
                    <ToolbarItem>
                      <FormSelect
                        aria-label="Sort buckets"
                        value={sort}
                        onChange={(_event, value) => {
                          setSort(value)
                          setPage(1)
                        }}
                      >
                        <FormSelectOption value="name" label="Sort: name" />
                        <FormSelectOption value="status" label="Sort: status" />
                        <FormSelectOption value="last-push" label="Sort: last push" />
                      </FormSelect>
                    </ToolbarItem>
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
                  <Alert variant="info" isInline title="No buckets match these filters" />
                ) : <Table aria-label="Buckets" variant="compact">
                  <Thead>
                    <Tr>
                      <Th screenReaderText="Row expansion" />
                      <Th>Bucket name</Th>
                      <Th>Newest version</Th>
                      <Th>Parents</Th>
                      <Th>Children</Th>
                      <Th>Platforms</Th>
                      <Th>Last updated</Th>
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
                          {bucket.channels.length > 0 && (
                            <div aria-label={`Channels for ${bucket.name}`}>
                              {bucket.channels.map((channel, index) => (
                                <span key={channel.name}>
                                  {index > 0 && <span aria-hidden> · </span>}
                                  {channel.name} {channel.versionName}
                                </span>
                              ))}
                            </div>
                          )}
                        </Td>
                        <Td dataLabel="Newest version">
                          {bucket.newestVersion ? (
                            <Button variant="link" isInline onClick={() => openBucket(bucket.name)}>
                              {bucket.newestVersion.name}
                            </Button>
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
                        <Td dataLabel="Last updated">{bucket.lastPush}</Td>
                        <Td dataLabel="Actions">
                          <BucketActions
                            bucket={bucket.name}
                            openBucket={openBucket}
                            pinned={pins.some((pin) => pin.bucket_name === bucket.name)}
                            canPin={canPin}
                            togglePin={togglePin}
                          />
                        </Td>
                      </Tr>
                      <Tr isExpanded={expanded === bucket.name}>
                        <Td />
                        <Td dataLabel="Detail" colSpan={7}>
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
    </>
  )
}

function BucketDetail({ bucket }: { bucket: Bucket }) {
  return (
    <>
      <Content component="p">{bucket.description || 'No description has been recorded.'}</Content>
      <div style={{ display: 'flex', gap: 32, flexWrap: 'wrap', marginTop: 12 }}>
        <div style={{ flex: '1 1 300px' }}>
          <Content component="h3" style={detailHeadingStyle}>Channels</Content>
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
              <span style={{ color: '#4d4d4d', fontSize: 13 }}>
                {channelGap(channel.fingerprint, bucket)}
              </span>
            </div>
          ))}
        </div>
        <div style={{ flex: '0 1 260px' }}>
          <Content component="h3" style={detailHeadingStyle}>Labels</Content>
          <span style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            {Object.entries(bucket.labels).length === 0 ? 'None' :
              Object.entries(bucket.labels).map(([key, value]) => (
                <Label key={key} isCompact>{key}={value}</Label>
              ))}
          </span>
          <Content component="h3" style={{ ...detailHeadingStyle, marginTop: 14 }}>
            Template type
          </Content>
          {bucket.templateTypes.length === 0 ? '—' : bucket.templateTypes.map((templateType) => (
            <Label key={templateType} isCompact>{templateType}</Label>
          ))}
        </div>
      </div>
    </>
  )
}

const detailHeadingStyle = {
  color: '#4d4d4d', fontSize: 12, letterSpacing: '.04em', textTransform: 'uppercase' as const,
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
}: {
  bucket: string
  openBucket: (bucket: string) => void
  pinned: boolean
  canPin: boolean
  togglePin: (bucketName: string, pinned: boolean) => Promise<void>
}) {
  const [open, setOpen] = useState(false)
  const pinAction = pinBucketAction(canPin)
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
      </DropdownList>
    </Dropdown>
  )
}

export function pinBucketAction(canPin: boolean): { disabled: boolean; label: string } {
  return {
    disabled: !canPin,
    label: `Pin bucket${canPin ? '' : ` — ${requirementReason('pinBuckets')}`}`,
  }
}

function bucketComparator(sort: string): (a: Bucket, b: Bucket) => number {
  switch (sort) {
    case 'status':
      return (a, b) => (a.newestVersion?.state ?? '').localeCompare(b.newestVersion?.state ?? '') ||
        a.name.localeCompare(b.name)
    case 'last-push':
      // The full timestamp, never the truncated display value: everything
      // pushed on one day would otherwise compare equal and fall to the name.
      return (a, b) => b.lastPushAt.localeCompare(a.lastPushAt) || a.name.localeCompare(b.name)
    default:
      return (a, b) => a.name.localeCompare(b.name)
  }
}
