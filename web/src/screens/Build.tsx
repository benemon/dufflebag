import { useState, type CSSProperties } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Card, CardBody, CardTitle, CodeBlock,
  CodeBlockCode, Content, DescriptionList, DescriptionListDescription,
  DescriptionListGroup, DescriptionListTerm, FormSelect, FormSelectOption, Label,
  PageSection, Pagination, TextInput, Title, Toolbar, ToolbarContent, ToolbarItem, Truncate,
} from '@patternfly/react-core'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import DownloadIcon from '@patternfly/react-icons/dist/esm/icons/download-icon'
import { useNavigate, useParams } from 'react-router'

import { PlatformLabel } from '../components/PlatformLabel'
import { SkeletonRows } from '../components/Loading'
import { TenancyGapEmptyState } from '../components/TenancyCreation'
import { downloadSbom, signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import type { Role } from '../auth/permissions'
import { useBuild, type Build, type BuildDetail, type Package, type SbomRef } from '../data/versions'
import type { TenancyGap } from '../data/tenant'
import { BuildStateLabel, packageSummary, pluginSummary } from './Version'
import { FacetRail, knownCount, type FacetCount } from './RegistryFacets'
import { FindingCounts, PackageFindingsTable } from '../components/Findings'
import { CopyableIdentifier } from '../components/CopyableIdentifier'
import { SEVERITY_ORDER } from '../data/findings'

const darkCodeStyle: CSSProperties = {
  '--pf-v6-c-code-block--BackgroundColor': 'var(--pf-t--color--gray--95)',
  '--pf-v6-c-code-block--BorderWidth': '0',
} as CSSProperties

/** Build metadata, labels, command reconstruction and artifact inventory. */
export function Build() {
  const { bucket = '', fingerprint = '', build = '' } = useParams()
  const navigate = useNavigate()
  const { data, loading, failure, gap } = useBuild(bucket, fingerprint, build)
  const { state, self, selectedOrganization, selectedProject, signOut } = useAuth()
  const fetchSbom = async (sbom: SbomRef): Promise<ArrayBuffer> => {
    if (!state || !selectedOrganization || !selectedProject) {
      throw new Error('No session.')
    }
    try {
      return await downloadSbom(
        state.token,
        { organizationID: selectedOrganization, projectID: selectedProject },
        bucket, fingerprint, build, sbom.name,
      )
    } catch (err: unknown) {
      // The same convention as every load path: a dead session signs out
      // rather than leaving a button that fails identically on every click.
      signOutIfUnauthorized(err, signOut)
      throw err
    }
  }
  const versionPath =
    `/buckets/${encodeURIComponent(bucket)}/versions/${encodeURIComponent(fingerprint)}`
  return (
    <BuildView
      bucket={bucket}
      detail={data}
      loading={loading}
      failure={failure}
      gap={gap}
      callerRole={self?.role ?? null}
      onBackToRegistry={() => navigate('/buckets')}
      onBackToBucket={() => navigate(`/buckets/${encodeURIComponent(bucket)}`)}
      onBackToVersion={() => navigate(versionPath)}
      fetchSbom={fetchSbom}
    />
  )
}

export function BuildView({
  bucket,
  detail,
  loading,
  failure,
  gap,
  callerRole = null,
  onBackToRegistry,
  onBackToBucket,
  onBackToVersion,
  fetchSbom = () => Promise.reject(new Error('No session.')),
}: {
  bucket: string
  detail: BuildDetail | null
  loading: boolean
  failure: string | null
  gap?: TenancyGap | null
  callerRole?: Role | null
  onBackToRegistry: () => void
  onBackToBucket: () => void
  onBackToVersion: () => void
  /** Fetches one stored SBOM's bytes; the view saves them as a file. */
  fetchSbom?: (sbom: SbomRef) => Promise<ArrayBuffer>
}) {
  const [facet, setFacet] = useState<'overview' | 'artifacts' | 'packages'>('overview')
  const build = detail?.build
  return (
    <>
      <PageSection variant="default">
        <Breadcrumb>
          <BreadcrumbItem component="button" onClick={onBackToRegistry}>Registry</BreadcrumbItem>
          <BreadcrumbItem component="button" onClick={onBackToBucket}>{bucket}</BreadcrumbItem>
          <BreadcrumbItem component="button" onClick={onBackToVersion}>
            {detail?.version.name ?? '…'}
          </BreadcrumbItem>
          <BreadcrumbItem isActive>{build?.component ?? '…'}</BreadcrumbItem>
        </Breadcrumb>
        {build && (
          <>
            <Title headingLevel="h1" size="2xl">
              <span style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
                {build.component}
                <BuildStateLabel state={build.state} />
              </span>
            </Title>
            <Content component="p">
              {countLabel(build.artifacts.length, 'artifact')} · {packageSummary(build)}
            </Content>
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
        padding={{ default: !loading && !failure && !gap && build ? 'noPadding' : 'padding' }}
      >
        {loading ? (
          <SkeletonRows screenreaderText="Loading build…" />
        ) : failure ? (
          <Alert variant="danger" isInline title="Build could not be loaded">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : gap ? (
          <TenancyGapEmptyState gap={gap} callerRole={callerRole} />
        ) : build ? (
          <FacetRail
            active={facet}
            onSelect={setFacet}
            heading="This build"
            label="Build facets"
            facets={[
              {
                key: 'overview', label: 'Overview',
                content: (
                  <BuildOverview
                    build={build}
                    sboms={detail?.sboms ?? []}
                    fetchSbom={fetchSbom}
                  />
                ),
              },
              {
                key: 'artifacts', label: 'Artifacts', count: knownCount(build.artifacts.length),
                content: <ArtifactsCard build={build} />,
              },
              {
                key: 'packages', label: 'Packages', count: packageFacetCount(build),
                content: <PackagesCard build={build} />,
              },
            ]}
          />
        ) : null}
      </PageSection>
    </>
  )
}

function BuildOverview({
  build,
  sboms,
  fetchSbom,
}: {
  build: Build
  sboms: SbomRef[]
  fetchSbom: (sbom: SbomRef) => Promise<ArrayBuffer>
}) {
  const command = packerBuildCommand(build)
  return (
    <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      <div style={{ flex: '1 1 440px', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 24 }}>
        <SbomCard sboms={sboms} fetchSbom={fetchSbom} />
        <Card>
          <CardTitle>Build options</CardTitle>
          <CardBody>
            {command ? (
              <>
                <CodeBlock style={darkCodeStyle}>
                  <CodeBlockCode style={{ color: 'var(--pf-t--color--gray--20)', whiteSpace: 'pre' }}>
                    {command}
                  </CodeBlockCode>
                </CodeBlock>
                <Content component="p" style={{ color: 'var(--pf-t--global--text--color--subtle)', marginTop: 8 }}>
                  Variable values are masked.
                </Content>
              </>
            ) : (
              <Content component="p">Packer did not report build options.</Content>
            )}
          </CardBody>
        </Card>

        <Card>
          <CardTitle>Packer runner environment</CardTitle>
          <CardBody>
            <DescriptionList isHorizontal isCompact>
              <EnvironmentField label="Packer" value={build.packerVersion} />
              <EnvironmentField label="Plugin" value={pluginSummary(build)} />
              <EnvironmentField label="Packer runner OS" value={build.runnerOS} />
              <EnvironmentField label="Arch" value={build.arch} />
              <DescriptionListGroup>
                <DescriptionListTerm>Run UUID</DescriptionListTerm>
                <DescriptionListDescription>
                  {build.packerRunUUID ? (
                    <CopyableIdentifier value={build.packerRunUUID} label="Build Run UUID" />
                  ) : '—'}
                </DescriptionListDescription>
              </DescriptionListGroup>
            </DescriptionList>
          </CardBody>
        </Card>
      </div>

      <Card style={{ flex: '0 1 380px', minWidth: 300 }}>
        <CardTitle>Build labels</CardTitle>
        <CardBody>
          {Object.keys(build.labels).length === 0 ? (
            <Content component="p">No labels were reported for this build.</Content>
          ) : (
            <DescriptionList isCompact>
              {Object.entries(build.labels).map(([key, value]) => (
                <DescriptionListGroup
                  key={key}
                  style={{ padding: '8px 11px', background: 'var(--pf-t--global--background--color--200)', borderRadius: 3 }}
                >
                  <DescriptionListTerm style={{ fontFamily: 'Red Hat Mono, monospace' }}>
                    {key}
                  </DescriptionListTerm>
                  <DescriptionListDescription
                    style={{ fontFamily: 'Red Hat Mono, monospace', wordBreak: 'break-all' }}
                  >
                    {value}
                  </DescriptionListDescription>
                </DescriptionListGroup>
              ))}
            </DescriptionList>
          )}
        </CardBody>
      </Card>
    </div>
  )
}

function EnvironmentField({ label, value }: { label: string; value: string }) {
  return (
    <DescriptionListGroup>
      <DescriptionListTerm>{label}</DescriptionListTerm>
      <DescriptionListDescription>{value || '—'}</DescriptionListDescription>
    </DescriptionListGroup>
  )
}

function ArtifactsCard({ build }: { build: Build }) {
  return (
    <Card>
      <CardTitle>Artifacts</CardTitle>
      <CardBody>
        {build.artifacts.length === 0 ? (
          <Content component="p">No artifacts were reported for this build.</Content>
        ) : (
          <Table aria-label={`${build.component} artifacts`} variant="compact">
            <Thead>
              <Tr>
                <Th>Platform</Th>
                <Th>External ID</Th>
                <Th>Region</Th>
              </Tr>
            </Thead>
            <Tbody>
              {build.artifacts.map((artifact) => (
                <Tr key={artifact.id}>
                  <Td dataLabel="Platform">{build.platform ? <PlatformLabel platform={build.platform} /> : '—'}</Td>
                  <Td dataLabel="External ID"><code>{artifact.externalIdentifier || '—'}</code></Td>
                  <Td dataLabel="Region">{artifact.region || '—'}</Td>
                </Tr>
              ))}
            </Tbody>
          </Table>
        )}
      </CardBody>
    </Card>
  )
}

function PackagesCard({ build }: { build: Build }) {
  const [query, setQuery] = useState('')
  // Keyed by purl: the identity the findings themselves are keyed by.
  const [expanded, setExpanded] = useState<string | null>(null)
  // '' = every package, 'affected' = only those with findings, or one band.
  const [findingFilter, setFindingFilter] = useState('')
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  if (build.packageInventory.status === 'unparseable') {
    return (
      <Card>
        <CardTitle>Packages</CardTitle>
        <CardBody>
          <Alert variant="warning" isInline title="Package inventory is unavailable">
            At least one client-supplied SBOM could not be parsed, so the package count is unknown.
          </Alert>
        </CardBody>
      </Card>
    )
  }
  if (build.packageInventory.status === 'not-loaded') {
    return (
      <Card>
        <CardTitle>Packages</CardTitle>
        <CardBody><Content component="p">Package inventory has not been loaded.</Content></CardBody>
      </Card>
    )
  }

  const normalized = query.trim().toLowerCase()
  const all = build.packageInventory.packages
  const affected = all.filter((pkg) => (pkg.findings?.length ?? 0) > 0)
  const packages = all.filter((pkg) => {
    if (!pkg.name.toLowerCase().includes(normalized)) return false
    const findings = pkg.findings ?? []
    if (findingFilter === '') return true
    if (findingFilter === 'affected') return findings.length > 0
    if (findingFilter === 'none') return findings.length === 0
    return findings.some((f) => (f.criticality ?? '').toLowerCase() === findingFilter)
  })
  // Only bands actually present are offered: a filter for a severity nothing
  // carries would return an empty table and teach the reader nothing.
  const bands = SEVERITY_ORDER.filter((band) =>
    affected.some((pkg) => (pkg.findings ?? []).some((f) => (f.criticality ?? '').toLowerCase() === band)),
  ).reverse()
  const lastPage = Math.max(1, Math.ceil(packages.length / perPage))
  const currentPage = Math.min(page, lastPage)
  const first = (currentPage - 1) * perPage
  const visiblePackages = packages.slice(first, first + perPage)
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
      <CardTitle>Packages</CardTitle>
      <CardBody>
        <Content component="p" style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
          Reported by client-supplied SBOMs; dufflebag has not verified this inventory.
          {' '}{all.length} {all.length === 1 ? 'package' : 'packages'} · {affected.length} with findings.
        </Content>
        <Toolbar id="packages-toolbar">
          <ToolbarContent>
            <ToolbarItem>
              <TextInput
                aria-label="Filter packages by name"
                placeholder="Filter by name"
                value={query}
                onChange={(_event, value) => {
                  setQuery(value)
                  setPage(1)
                }}
              />
            </ToolbarItem>
            <ToolbarItem>
              <FormSelect
                aria-label="Filter packages by findings"
                value={findingFilter}
                onChange={(_event, value) => {
                  setFindingFilter(value)
                  setPage(1)
                }}
              >
                <FormSelectOption value="" label="All packages" />
                <FormSelectOption value="affected" label={`With findings (${affected.length})`} />
                <FormSelectOption value="none" label={`Without findings (${all.length - affected.length})`} />
                {bands.map((band) => (
                  <FormSelectOption key={band} value={band} label={`Severity: ${band}`} />
                ))}
              </FormSelect>
            </ToolbarItem>
            <ToolbarItem>
              <Content component="p">{packages.length} of {all.length}</Content>
            </ToolbarItem>
            <ToolbarItem variant="pagination" align={{ default: 'alignEnd' }}>
              <Pagination
                itemCount={packages.length}
                page={currentPage}
                perPage={perPage}
                onSetPage={setCurrentPage}
                onPerPageSelect={selectPerPage}
                isCompact
              />
            </ToolbarItem>
          </ToolbarContent>
        </Toolbar>
        {packages.length === 0 ? (
          <Content component="p" style={{ marginTop: 14 }}>No package matches that search.</Content>
        ) : (
          <>
            <PackageTable
              packages={visiblePackages}
              expanded={expanded}
              onToggle={setExpanded}
            />
            <Pagination
              itemCount={packages.length}
              page={currentPage}
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

function PackageTable({
  packages, expanded, onToggle,
}: {
  packages: Package[]
  expanded: string | null
  onToggle: (purl: string | null) => void
}) {
  return (
    <Table aria-label="Packages" variant="compact" isStickyHeader style={{ marginTop: 14 }}>
      <Thead>
        <Tr>
          <Th>Name</Th>
          <Th>Version</Th>
          <Th>SBOM</Th>
          <Th width={20}>Findings</Th>
        </Tr>
      </Thead>
      {packages.map((pkg, rowIndex) => {
        // The expander lives ON the findings cell rather than in a leading
        // column, so a package with nothing found keeps its alignment and
        // gains no affordance at all.
        const findings = pkg.findings ?? []
        const isExpanded = expanded === pkg.purl && findings.length > 0
        return (
          <Tbody key={`${pkg.purl}/${pkg.name}/${pkg.version}`} isExpanded={isExpanded}>
            <Tr>
              <Td dataLabel="Name"><code>{pkg.name}</code></Td>
              <Td dataLabel="Version">{pkg.version}</Td>
              <Td dataLabel="SBOM">{sbomNames(pkg)}</Td>
              {findings.length > 0 ? (
                <Td
                  dataLabel="Findings"
                  compoundExpand={{
                    isExpanded,
                    onToggle: () => onToggle(isExpanded ? null : pkg.purl),
                    rowIndex,
                    columnIndex: 3,
                  }}
                >
                  <FindingCounts findings={findings} isExpanded={isExpanded} />
                </Td>
              ) : (
                <Td dataLabel="Findings"><FindingCounts findings={findings} /></Td>
              )}
            </Tr>
            {isExpanded && (
              <Tr isExpanded>
                <Td dataLabel="Findings" colSpan={4} noPadding>
                  <ExpandableRowContent>
                    <PackageFindingsTable findings={findings} />
                  </ExpandableRowContent>
                </Td>
              </Tr>
            )}
          </Tbody>
        )
      })}
    </Table>
  )
}

export const PackageTableForTest = PackageTable
export const PackagesCardForTest = PackagesCard

function sbomNames(pkg: Package): string {
  if (pkg.sboms.length === 0) return '—'
  return pkg.sboms.map((sbom) => sbom.name || sbom.id || sbom.format).filter(Boolean).join(', ') || '—'
}

function packageFacetCount(build: Build): FacetCount {
  return build.packageInventory.status === 'parsed'
    ? knownCount(build.packageInventory.packages.length)
    : { status: 'unknown' }
}

/** The command HCP reconstructs from metadata; Packer supplies names, never variable values. */
export function packerBuildCommand(build: Build): string | null {
  if (!build.options.path) return null
  const args = [
    ...build.options.variables.map((name) => `-var=${JSON.stringify(`${name}=***`)}`),
    ...build.options.variableFiles.map((path) => `-var-file=${path}`),
    ...build.options.only.map((value) => `-only=${value}`),
    ...build.options.except.map((value) => `-except=${value}`),
    ...(build.options.debug ? ['-debug'] : []),
    ...(build.options.force ? ['-force'] : []),
    build.options.path,
  ]
  return args.length === 1
    ? `packer build ${args[0]}`
    : `packer build \\\n  ${args.join(' \\\n  ')}`
}

function countLabel(count: number, singular: string): string {
  return `${count} ${singular}${count === 1 ? '' : 's'}`
}

/**
 * One row per stored document, in the Security card's row idiom: the row is
 * the affordance and the trailing glyph says what it does — a download, not a
 * drill-down. Absent entirely when the build stored none. The saved file is
 * the DOCUMENT under "<name>.json", exactly as live HCP serves it (probed
 * 2026-08-08).
 */
function SbomCard({
  sboms,
  fetchSbom,
}: {
  sboms: SbomRef[]
  fetchSbom: (sbom: SbomRef) => Promise<ArrayBuffer>
}) {
  const [failure, setFailure] = useState<string | null>(null)
  if (sboms.length === 0) return null

  const save = async (sbom: SbomRef) => {
    setFailure(null)
    try {
      const bytes = await fetchSbom(sbom)
      const url = URL.createObjectURL(new Blob([bytes], { type: 'application/json' }))
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = sbomFileName(sbom)
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (err: unknown) {
      setFailure(err instanceof Error ? err.message : 'The SBOM could not be downloaded.')
    }
  }

  return (
    <Card>
      <CardTitle>SBOM</CardTitle>
      <CardBody>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {sboms.map((sbom) => (
            <button
              key={sbom.id || sbom.name}
              type="button"
              onClick={() => void save(sbom)}
              aria-label={`Download ${sbomFileName(sbom)}`}
              style={{
                font: 'inherit', textAlign: 'left', cursor: 'pointer', background: 'none',
                width: '100%',
                display: 'flex', alignItems: 'center', gap: 14, padding: '11px 14px',
                border: '1px solid var(--pf-t--global--border--color--default)', borderRadius: 3, color: 'inherit',
              }}
            >
              <span style={{ flex: '0 1 auto', minWidth: 0 }}>
                <Truncate content={sbomFileName(sbom)} />
              </span>
              <Label isCompact>{sbom.format}</Label>
              <span style={{ marginLeft: 'auto', display: 'flex' }} aria-hidden="true">
                <DownloadIcon />
              </span>
            </button>
          ))}
        </div>
        {failure ? (
          <Alert
            variant="danger"
            isInline
            title="SBOM could not be downloaded"
            style={{ marginTop: 12 }}
          >
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
      </CardBody>
    </Card>
  )
}

/** "<name>.json" — the document, exactly as live HCP names it (whatever the format). */
export function sbomFileName(sbom: SbomRef): string {
  return `${sbom.name}.json`
}
