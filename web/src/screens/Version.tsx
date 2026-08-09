import { useState } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle,
  ClipboardCopyButton, CodeBlock, CodeBlockAction, CodeBlockCode, Content,
  DescriptionList, DescriptionListDescription, DescriptionListGroup,
  DescriptionListTerm, Label, PageSection, Title,
} from '@patternfly/react-core'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate, useParams } from 'react-router'

import {
  useVersion, useVersionFindings, type AncestryChild, type BucketChannel, type Build,
  type BuildState, type Version as VersionData, type VersionDetail,
} from '../data/versions'
import { VersionSecurityCard } from '../components/VersionSecurity'
import type { BuildFindings } from '../data/findings'
import { VersionStateLabel } from './Versions'
import type { TenancyGap } from '../data/tenant'
import { FacetRail, knownCount } from './RegistryFacets'

/**
 * Version — one version's lineage, operations and expandable build list.
 *
 * Components follow the design's version page (mockup 1a1, isVersion):
 *   Breadcrumb · Title · LabelGroup · DescriptionList · Card · Table
 * Read-only (ADR-0012): promotion is Terraform's job; nothing here writes.
 */
export function Version() {
  const { bucket = '', fingerprint = '' } = useParams()
  const navigate = useNavigate()
  const { data, loading, failure, gap } = useVersion(bucket, fingerprint)
  const { data: findings } = useVersionFindings(
    bucket,
    fingerprint,
    (data?.version.builds ?? []).map((build) => ({
      id: build.id, platform: build.platform, component: build.component,
    })),
  )
  return (
    <VersionView
      bucket={bucket}
      detail={data}
      findings={findings}
      loading={loading}
      failure={failure}
      gap={gap}
      onBackToRegistry={() => navigate('/buckets')}
      onBackToBucket={() => navigate(`/buckets/${encodeURIComponent(bucket)}`)}
      onOpenBuild={(build) => navigate(
        `/buckets/${encodeURIComponent(bucket)}/versions/${encodeURIComponent(fingerprint)}` +
        `/builds/${encodeURIComponent(build)}`,
      )}
      onOpenVersion={(relatedBucket, relatedFingerprint) => navigate(
        `/buckets/${encodeURIComponent(relatedBucket)}/versions/${encodeURIComponent(relatedFingerprint)}`,
      )}
    />
  )
}

export function VersionView({
  bucket,
  detail,
  version: suppliedVersion,
  findings = [],
  loading,
  failure,
  gap,
  onBackToRegistry,
  onBackToBucket,
  onOpenBuild = () => {},
  onOpenVersion = () => {},
}: {
  bucket: string
  detail?: VersionDetail | null
  /** Kept for focused view tests that do not need the ancestry fetch. */
  version?: VersionData | null
  /** Per-build inventories, fetched by the container like every other read. */
  findings?: BuildFindings[]
  loading: boolean
  failure: string | null
  /** A platform session with no tenancy chosen yet — stated, never fetched around. */
  gap?: TenancyGap | null
  onBackToRegistry: () => void
  onBackToBucket: () => void
  onOpenBuild?: (build: string) => void
  onOpenVersion?: (bucket: string, fingerprint: string) => void
}) {
  const version = detail?.version ?? suppliedVersion ?? null
  const [facet, setFacet] = useState<'overview' | 'builds'>('overview')
  return (
    <>
      <PageSection variant="default">
        <Breadcrumb>
          <BreadcrumbItem component="button" onClick={onBackToRegistry}>
            Registry
          </BreadcrumbItem>
          <BreadcrumbItem component="button" onClick={onBackToBucket}>
            {bucket}
          </BreadcrumbItem>
          <BreadcrumbItem isActive>{version?.name ?? '…'}</BreadcrumbItem>
        </Breadcrumb>
        {version && (
          <>
            <Title headingLevel="h1" size="2xl">
              <span style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
                {version.name}
                <VersionStateLabel state={version.state} />
                {version.channels.map((channel) => (
                  <Label key={channel} isCompact>{channel}</Label>
                ))}
                {version.templateType && <Label isCompact>{version.templateType}</Label>}
              </span>
            </Title>
            <DescriptionList isHorizontal style={{ marginTop: 16 }}>
              <DescriptionListGroup>
                <DescriptionListTerm>Fingerprint</DescriptionListTerm>
                <DescriptionListDescription>
                  <code className="registry-fingerprint">{version.fingerprint}</code>
                </DescriptionListDescription>
              </DescriptionListGroup>
              <DescriptionListGroup>
                <DescriptionListTerm>Created</DescriptionListTerm>
                <DescriptionListDescription>{version.created}</DescriptionListDescription>
              </DescriptionListGroup>
            </DescriptionList>
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
        padding={{ default: !loading && !failure && !gap && version ? 'noPadding' : 'padding' }}
      >
        {loading ? (
          <Content component="p">Loading version…</Content>
        ) : failure ? (
          <Alert variant="danger" isInline title="Version could not be loaded">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : gap ? (
          <Alert variant="info" isInline title={gap.title}>
            <Content component="p">{gap.detail}</Content>
          </Alert>
        ) : version ? (
          <FacetRail
            active={facet}
            onSelect={setFacet}
            heading="This version"
            label="Version facets"
            facets={[
              {
                key: 'overview', label: 'Overview',
                content: (
                  <VersionOverview
                    bucket={bucket}
                    version={version}
                    detail={detail ?? null}
                    findings={findings}
                    onOpenBuild={onOpenBuild}
                    onOpenVersion={onOpenVersion}
                  />
                ),
              },
              {
                key: 'builds', label: 'Builds', count: knownCount(version.builds.length),
                content: (
                  <Card style={{ flex: '1 1 520px' }}>
                    <CardTitle>Builds</CardTitle>
                    <CardBody>
                      {version.builds.length === 0 ? (
                        <Content component="p">No builds have been reported for this version.</Content>
                      ) : (
                        <BuildTable builds={version.builds} onOpenBuild={onOpenBuild} />
                      )}
                    </CardBody>
                  </Card>
                ),
              },
            ]}
          />
        ) : null}
      </PageSection>
    </>
  )
}

function VersionOverview({
  bucket,
  version,
  detail,
  findings,
  onOpenBuild,
  onOpenVersion,
}: {
  bucket: string
  version: VersionData
  detail: VersionDetail | null
  findings: BuildFindings[]
  onOpenBuild: (build: string) => void
  onOpenVersion: (bucket: string, fingerprint: string) => void
}) {
  return (
    <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <div style={{ flex: '1 1 520px', display: 'flex', flexDirection: 'column', gap: 24 }}>
        {detail && findings.length > 0 && (
          <VersionSecurityCard
            builds={findings}
            onOpenBuild={onOpenBuild}
            // The channels this version actually carries, so a version a
            // channel selects is never reported as unmaintained.
            outOfScanSet={version.channels.length === 0}
          />
        )}
        {detail && <LineageCard version={version} onOpenVersion={onOpenVersion} />}
      </div>
      {detail && (
        <div style={{ flex: '0 1 440px', display: 'flex', flexDirection: 'column', gap: 24 }}>
          <ConsumeCard bucket={bucket} version={version} />
          <OperationsCard bucket={bucket} version={version} channels={detail.channels} />
        </div>
      )}
    </div>
  )
}

function LineageCard({
  version,
  onOpenVersion,
}: {
  version: VersionData
  onOpenVersion: (bucket: string, fingerprint: string) => void
}) {
  return (
    <Card>
      <CardTitle>Lineage</CardTitle>
      <CardBody style={{ display: 'flex', gap: 32, flexWrap: 'wrap' }}>
        <LineageSide heading="Parents" entries={version.parents ?? []} onOpenVersion={onOpenVersion} />
        <LineageSide heading="Children" entries={version.children ?? []} onOpenVersion={onOpenVersion} />
      </CardBody>
    </Card>
  )
}

/**
 * One side of the lineage card. Entries read "bucket vN" and link to that
 * version's screen, which carries the fingerprint a reader would copy.
 */
function LineageSide({
  heading,
  entries,
  onOpenVersion,
}: {
  heading: string
  entries: AncestryChild[]
  onOpenVersion: (bucket: string, fingerprint: string) => void
}) {
  return (
    <div style={{ flex: '1 1 220px' }}>
      <Content component="h3" style={subheadingStyle}>{heading}</Content>
      {entries.length === 0 ? <Content component="p">None.</Content> :
        entries.map((entry) => (
          <div key={`${entry.bucket}/${entry.fingerprint}`} style={{ padding: '5px 0' }}>
            <Button variant="link" isInline onClick={() => onOpenVersion(entry.bucket, entry.fingerprint)}>
              {entry.bucket} {entry.versionName}
            </Button>
            {(entry.channels ?? []).length > 0 && (
              <div style={{ color: '#4d4d4d', fontSize: 13 }}>{(entry.channels ?? []).join(', ')}</div>
            )}
          </div>
        ))}
    </div>
  )
}

const subheadingStyle = {
  color: '#4d4d4d', fontSize: 12, letterSpacing: '.04em', textTransform: 'uppercase' as const,
}

function ConsumeCard({ bucket, version }: { bucket: string; version: VersionData }) {
  const snippet = terraformConsumeSnippet(bucket, version)
  return (
    <Card>
      <CardTitle>Consume this version</CardTitle>
      <CardBody>
        {snippet ? (
          <TerraformCode snippet={snippet} label="Copy consume configuration" />
        ) : (
          <Alert variant="info" isInline title="No Terraform lookup can identify this version">
            <Content component="p">
              <code>hcp_packer_version</code> resolves a channel, and no channel currently points
              at this version. An artifact block appears once an artifact has been reported.
            </Content>
          </Alert>
        )}
        {version.channels.length > 0 && (
          <Content component="p">
            The version lookup follows {version.channels[0]}. The artifact lookup pins the exact
            fingerprint shown above.
          </Content>
        )}
      </CardBody>
    </Card>
  )
}

function OperationsCard({
  bucket,
  version,
  channels,
}: {
  bucket: string
  version: VersionData
  channels: BucketChannel[]
}) {
  const promotable = channels.filter((channel) => !channel.managed)
  if (promotable.length === 0) return null
  return (
    <Card>
      <CardTitle>Operations</CardTitle>
      <CardBody>
        {promotable.map((channel) => (
          <div key={channel.name} style={{ marginTop: 16 }}>
            <Content component="p">
              {channel.fingerprint === version.fingerprint ? 'Keep' : 'Promote'} {version.name} on{' '}
              <strong>{channel.name}</strong>
            </Content>
            <TerraformCode
              snippet={terraformPromotionSnippet(bucket, channel.name, version.fingerprint)}
              label={`Copy ${channel.name} assignment`}
            />
          </div>
        ))}
      </CardBody>
    </Card>
  )
}

function TerraformCode({ snippet, label }: { snippet: string; label: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <CodeBlock
      actions={(
        <CodeBlockAction>
          <ClipboardCopyButton
            id={`copy-${label.replace(/\s+/g, '-').toLowerCase()}`}
            variant="plain"
            aria-label={label}
            onClick={() => {
              void navigator.clipboard.writeText(snippet)
              setCopied(true)
            }}
            exitDelay={copied ? 1500 : 600}
            onTooltipHidden={() => setCopied(false)}
          >
            {copied ? 'Copied' : 'Copy'}
          </ClipboardCopyButton>
        </CodeBlockAction>
      )}
    >
      <CodeBlockCode>{snippet}</CodeBlockCode>
    </CodeBlock>
  )
}

export function terraformConsumeSnippet(bucket: string, version: VersionData): string | null {
  const channel = version.channels[0]
  const artifact = version.builds.flatMap((build) =>
    build.artifacts.map((item) => ({ ...item, platform: build.platform })),
  )[0]
  if (!channel && !artifact) return null

  const label = terraformLabel(bucket)
  const blocks: string[] = []
  if (channel) {
    blocks.push(`data "hcp_packer_version" "${label}" {\n` +
      `  bucket_name  = ${JSON.stringify(bucket)}\n` +
      `  channel_name = ${JSON.stringify(channel)}\n` +
      '}')
  }
  if (artifact) {
    blocks.push(`data "hcp_packer_artifact" "${label}" {\n` +
      `  bucket_name         = ${JSON.stringify(bucket)}\n` +
      `  version_fingerprint = ${JSON.stringify(version.fingerprint)}\n` +
      `  platform            = ${JSON.stringify(artifact.platform)}\n` +
      `  region              = ${JSON.stringify(artifact.region)}\n` +
      '}')
  }
  return blocks.join('\n\n')
}

export function terraformPromotionSnippet(
  bucket: string,
  channel: string,
  fingerprint: string,
): string {
  return `resource "hcp_packer_channel_assignment" "${terraformLabel(channel)}" {\n` +
    `  bucket_name         = ${JSON.stringify(bucket)}\n` +
    `  channel_name        = ${JSON.stringify(channel)}\n` +
    `  version_fingerprint = ${JSON.stringify(fingerprint)}\n` +
    '}'
}

function terraformLabel(value: string): string {
  const label = value.toLowerCase().replace(/[^a-z0-9_]/g, '_') || 'version'
  return /^[0-9]/.test(label) ? `v_${label}` : label
}

function BuildTable({ builds, onOpenBuild }: { builds: Build[]; onOpenBuild: (id: string) => void }) {
  const [expanded, setExpanded] = useState<string | null>(null)
  return (
    <Table aria-label="Builds" variant="compact">
      <Thead>
        <Tr>
          <Th screenReaderText="Row expansion" />
          <Th>Name</Th>
          <Th>Status</Th>
          <Th>Packer runner OS</Th>
          <Th>Arch</Th>
          <Th>Updated</Th>
        </Tr>
      </Thead>
      {builds.map((build, index) => (
        <Tbody key={build.id} isExpanded={expanded === build.id}>
          <Tr>
            <Td
              expand={{
                rowIndex: index,
                isExpanded: expanded === build.id,
                onToggle: () => setExpanded(expanded === build.id ? null : build.id),
                expandId: `build-${build.id}`,
              }}
            />
            <Td dataLabel="Name">
              <Button variant="link" isInline onClick={() => onOpenBuild(build.id)}>
                {build.component || '—'}
              </Button>
            </Td>
            <Td dataLabel="Status"><BuildStateLabel state={build.state} /></Td>
            <Td dataLabel="Packer runner OS">{build.runnerOS || '—'}</Td>
            <Td dataLabel="Arch">{build.arch || '—'}</Td>
            <Td dataLabel="Updated">{build.updated}</Td>
          </Tr>
          <Tr isExpanded={expanded === build.id}>
            <Td />
            <Td dataLabel="Build detail" colSpan={5}>
              <ExpandableRowContent>
                <DescriptionList isHorizontal isCompact>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Packer</DescriptionListTerm>
                    <DescriptionListDescription>{build.packerVersion || '—'}</DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Plugin</DescriptionListTerm>
                    <DescriptionListDescription>{pluginSummary(build)}</DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Artifacts</DescriptionListTerm>
                    <DescriptionListDescription>
                      {countLabel(build.artifacts.length, 'artifact')}
                    </DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Packages</DescriptionListTerm>
                    <DescriptionListDescription>{packageSummary(build)}</DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Run UUID</DescriptionListTerm>
                    <DescriptionListDescription><code>{build.packerRunUUID || '—'}</code></DescriptionListDescription>
                  </DescriptionListGroup>
                </DescriptionList>
                <Button variant="link" isInline onClick={() => onOpenBuild(build.id)}>
                  Open {build.component || 'build'} →
                </Button>
              </ExpandableRowContent>
            </Td>
          </Tr>
        </Tbody>
      ))}
    </Table>
  )
}

export function BuildStateLabel({ state }: { state: BuildState }) {
  switch (state) {
    case 'done':
      return <Label isCompact color="green">done</Label>
    case 'running':
      return <Label isCompact color="blue">running</Label>
    case 'failed':
      return <Label isCompact color="red">failed</Label>
    case 'cancelled':
      return <Label isCompact color="orange">cancelled</Label>
    case 'pending':
      return <Label isCompact color="grey">pending</Label>
  }
}

export function pluginSummary(build: Build): string {
  if (build.plugins.length === 0) return '—'
  return build.plugins
    .map((plugin) => [plugin.name, plugin.version].filter(Boolean).join(' '))
    .filter(Boolean)
    .join(', ') || '—'
}

export function packageSummary(build: Build): string {
  switch (build.packageInventory.status) {
    case 'parsed':
      return countLabel(build.packageInventory.packages.length, 'package')
    case 'unparseable':
      return 'SBOM unparseable'
    case 'not-loaded':
      return '—'
  }
}

function countLabel(count: number, singular: string): string {
  return `${count} ${singular}${count === 1 ? '' : 's'}`
}
