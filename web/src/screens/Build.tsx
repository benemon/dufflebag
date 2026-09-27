import { useEffect, useRef, useState, type CSSProperties } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Card, CardBody, CardTitle, CodeBlock,
  CodeBlockCode, Content, DataList, DataListCell, DataListItem, DataListItemCells,
  DataListItemRow, DescriptionList, DescriptionListDescription, DescriptionListGroup,
  DescriptionListTerm, FormSelect, FormSelectOption, Label, PageSection, Pagination,
  SearchInput, Spinner, TextInput, Title, ToggleGroup, ToggleGroupItem, Toolbar,
  ToolbarContent, ToolbarFilter, ToolbarItem, Truncate,
} from '@patternfly/react-core'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import DownloadIcon from '@patternfly/react-icons/dist/esm/icons/download-icon'
import { useNavigate, useParams, useSearchParams } from 'react-router'

import { PlatformLabel } from '../components/PlatformLabel'
import { SkeletonRows } from '../components/Loading'
import { ScreenHeader } from '../components/ScreenHeader'
import { TenancyGapEmptyState } from '../components/TenancyCreation'
import { downloadSbom, signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import type { Role } from '../auth/permissions'
import {
  buildIsInProgress, packageInventoryFromFindings, runsDisagree, useBuild, useBuildFindings,
  useVersionFindings, useVersionSecuritySummary, type Build, type BuildDetail, type InventoryProgress,
  type Package, type SbomRef, type VersionSecuritySummary,
} from '../data/versions'
import { useAutoRefresh } from '../data/polling'
import type { TenancyGap } from '../data/tenant'
import { BuildStateLabel, pluginSummary } from './Version'
import { FacetRail, knownCount, type FacetCount } from './RegistryFacets'
import { SEVERITY_COLOUR } from '../components/Findings'
import { CopyableIdentifier } from '../components/CopyableIdentifier'
import { SEVERITY_ORDER, severityCounts, type Severity } from '../data/findings'
import {
  packageIdentity, type Advisory, type BuildFindingsData, type VulnerablePackage,
} from '../data/advisories'
import { When } from '../components/When'

const darkCodeStyle: CSSProperties = {
  '--pf-v6-c-code-block--BackgroundColor': 'var(--pf-t--color--gray--95)',
  '--pf-v6-c-code-block--BorderWidth': '0',
} as CSSProperties

export type BuildFacet = 'overview' | 'artifacts' | 'packages' | 'vulnerabilities'

export function buildFacet(value: string | undefined): BuildFacet {
  switch (value) {
    case 'artifacts':
    case 'packages':
    case 'vulnerabilities':
      return value
    default:
      return 'overview'
  }
}

export function buildFacetPath(
  bucket: string, fingerprint: string, build: string, facet: BuildFacet,
): string {
  return `/buckets/${encodeURIComponent(bucket)}/versions/${encodeURIComponent(fingerprint)}` +
    `/builds/${encodeURIComponent(build)}/${facet}`
}

/** Build metadata, labels, command reconstruction and artifact inventory. */
export function Build() {
  const { bucket = '', fingerprint = '', build = '', facet: facetParam } = useParams()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { data, loading, refreshing: detailRefreshing, failure, gap, reload } =
    useBuild(bucket, fingerprint, build)
  const buildInProgress = data ? buildIsInProgress(data.build) : false
  useAutoRefresh({ hot: buildInProgress, onRefresh: reload })
  const inventoryBuilds = data
    ? (buildInProgress ? [] : [data.build])
    : []
  const inventory = useVersionFindings(bucket, fingerprint, inventoryBuilds)
  const security = useVersionSecuritySummary(bucket, fingerprint)
  const findings = useBuildFindings(bucket, fingerprint, build)
  const inventoryRun = inventory.data[0]?.scan?.runID
  const findingsRun = findings.data?.run?.id
  const reconciled = useRef('')
  useEffect(() => {
    const pair = `${inventoryRun}/${findingsRun}`
    if (!runsDisagree(inventoryRun, findingsRun) || reconciled.current === pair) return
    reconciled.current = pair
    inventory.reload()
    findings.reload()
  }, [inventoryRun, findingsRun, inventory, findings])
  const detail = data ? {
    ...data,
    build: {
      ...data.build,
      packageInventory: packageInventoryFromFindings(inventory.data[0]),
    },
  } : null
  const refreshing = detailRefreshing || inventory.loading || security.refreshing || findings.refreshing
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
  const selectedFacet = buildFacet(facetParam)
  const severityFilter = (searchParams.get('severity') ?? '').split(',').filter(
    (value): value is Severity => SEVERITY_ORDER.includes(value as Severity),
  )
  const selectFacet = (next: BuildFacet) => {
    const nextSearch = new URLSearchParams()
    const packageFilter = searchParams.get('package')
    if ((next === 'packages' || next === 'vulnerabilities') && packageFilter) {
      nextSearch.set('package', packageFilter)
    }
    if (next === 'vulnerabilities' && severityFilter.length > 0) {
      nextSearch.set('severity', severityFilter.join(','))
    }
    const query = nextSearch.toString()
    navigate(buildFacetPath(bucket, fingerprint, build, next) + (query ? `?${query}` : ''), {
      replace: true,
    })
  }
  const updateFilters = (packageFilter: string, severities: Severity[]) => {
    const next = new URLSearchParams()
    if (packageFilter) next.set('package', packageFilter)
    if (severities.length > 0) next.set('severity', severities.join(','))
    setSearchParams(next, { replace: true })
  }
  return (
    <BuildView
      bucket={bucket}
      detail={detail}
      inventoryLoading={inventory.loading}
      inventoryFailure={inventory.failure}
      inventoryProgress={inventory.progress}
      securitySummary={security.data}
      findings={findings.data}
      findingsLoading={findings.loading}
      findingsFailure={findings.failure}
      facet={selectedFacet}
      packageFilter={searchParams.get('package') ?? ''}
      severityFilter={severityFilter}
      loading={loading}
      refreshing={refreshing}
      failure={failure}
      gap={gap}
      callerRole={self?.role ?? null}
      onBackToRegistry={() => navigate('/')}
      onBackToBucket={() => navigate(`/buckets/${encodeURIComponent(bucket)}`)}
      onBackToVersion={() => navigate(versionPath)}
      onSelectFacet={selectFacet}
      onUpdateFilters={updateFilters}
      onRefresh={() => {
        reload()
        inventory.reload()
        security.reload()
        findings.reload()
      }}
      fetchSbom={fetchSbom}
    />
  )
}

export function BuildView({
  bucket,
  detail,
  inventoryLoading = false,
  inventoryFailure = null,
  inventoryProgress = { packages: 0 },
  securitySummary = null,
  findings = null,
  findingsLoading = false,
  findingsFailure = null,
  facet: suppliedFacet,
  packageFilter = '',
  severityFilter = [],
  loading,
  refreshing = false,
  failure,
  gap,
  callerRole = null,
  onBackToRegistry,
  onBackToBucket,
  onBackToVersion,
  onSelectFacet = () => {},
  onUpdateFilters = () => {},
  onRefresh = () => {},
  fetchSbom = () => Promise.reject(new Error('No session.')),
}: {
  bucket: string
  detail: BuildDetail | null
  inventoryLoading?: boolean
  inventoryFailure?: string | null
  inventoryProgress?: InventoryProgress
  securitySummary?: VersionSecuritySummary | null
  findings?: BuildFindingsData | null
  findingsLoading?: boolean
  findingsFailure?: string | null
  facet?: BuildFacet
  packageFilter?: string
  severityFilter?: Severity[]
  loading: boolean
  refreshing?: boolean
  failure: string | null
  gap?: TenancyGap | null
  callerRole?: Role | null
  onBackToRegistry: () => void
  onBackToBucket: () => void
  onBackToVersion: () => void
  onSelectFacet?: (facet: BuildFacet) => void
  onUpdateFilters?: (packageFilter: string, severities: Severity[]) => void
  onRefresh?: () => void
  /** Fetches one stored SBOM's bytes; the view saves them as a file. */
  fetchSbom?: (sbom: SbomRef) => Promise<ArrayBuffer>
}) {
  const { facet: routeFacet } = useParams()
  const facet = suppliedFacet ?? buildFacet(routeFacet)
  const build = detail?.build
  const securityBuild = securitySummary?.builds.find((candidate) => candidate.buildID === build?.id)
  const scanned = Boolean(securityBuild?.summary)
  return (
    <>
      <ScreenHeader
        onRefresh={onRefresh}
        refreshing={refreshing}
        breadcrumbs={<Breadcrumb>
          <BreadcrumbItem component="button" onClick={onBackToRegistry}>Registry</BreadcrumbItem>
          <BreadcrumbItem component="button" onClick={onBackToBucket}>{bucket}</BreadcrumbItem>
          <BreadcrumbItem component="button" onClick={onBackToVersion}>
            {detail?.version.name ?? '…'}
          </BreadcrumbItem>
          <BreadcrumbItem isActive>{build?.component ?? '…'}</BreadcrumbItem>
        </Breadcrumb>}
        title={build ? (
          <span style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
            {build.component}
            <BuildStateLabel state={build.state} />
          </span>
        ) : null}
        description={build
          ? `${countLabel(build.artifacts.length, 'artifact')} · ${
            inventoryLoading
              ? 'reading packages…'
              : inventoryFailure ? 'packages unavailable' : packageSummary(build)
          }`
          : null}
      />

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
            onSelect={onSelectFacet}
            heading="This build"
            label="Build facets"
            unmountOnExit
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
                key: 'packages', label: 'Packages', count: packageFacetCount(build, inventoryLoading),
                content: (
                  <PackagesCard
                    build={build}
                    inventoryLoading={inventoryLoading}
                    inventoryFailure={inventoryFailure}
                    inventoryProgress={inventoryProgress}
                    scanned={scanned}
                    packageFilter={packageFilter}
                    vulnerabilityPath={buildFacetPath(
                      bucket, detail!.version.fingerprint, build.id, 'vulnerabilities',
                    )}
                    onPackageFilterChange={(identity) => onUpdateFilters(identity, severityFilter)}
                  />
                ),
              },
              {
                key: 'vulnerabilities', label: 'Vulnerabilities',
                count: vulnerabilityFacetCount(findings),
                content: (
                  <VulnerabilitiesCard
                    findings={findings}
                    loading={findingsLoading}
                    failure={findingsFailure}
                    packageFilter={packageFilter}
                    severityFilter={severityFilter}
                    buildPath={buildFacetPath(
                      bucket, detail!.version.fingerprint, build.id, 'vulnerabilities',
                    )}
                    onFiltersChange={onUpdateFilters}
                  />
                ),
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
  // The Run UUID is a top-level build field, not metadata, and this card is
  // its only home — a metadata-less build with a run UUID keeps the card. The
  // card absents itself only when there is genuinely nothing to show.
  const hasPackerEnvironment = Boolean(
    build.packerVersion || build.plugins.length || build.runnerOS || build.arch ||
    build.options.path || build.options.variables.length || build.options.variableFiles.length ||
    build.options.only.length || build.options.except.length ||
    build.options.debug || build.options.force || build.packerRunUUID,
  )
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

        {hasPackerEnvironment && (
          <Card>
            <CardTitle>Packer runner environment</CardTitle>
            <CardBody>
              <DescriptionList isHorizontal isCompact>
                <EnvironmentField label="Packer" value={build.packerVersion} />
                <EnvironmentField label="Plugin" value={pluginSummary(build)} />
                <EnvironmentField label="Packer runner OS" value={build.runnerOS} />
                <EnvironmentField label="Arch" value={build.arch} />
                {build.options.path && <EnvironmentField label="Template" value={build.options.path} />}
                {build.options.debug && <EnvironmentField label="Debug" value="true" />}
                {build.options.force && <EnvironmentField label="Force" value="true" />}
                {build.options.only.length > 0 && (
                  <EnvironmentField label="Only" value={build.options.only.join(', ')} />
                )}
                {build.options.except.length > 0 && (
                  <EnvironmentField label="Except" value={build.options.except.join(', ')} />
                )}
                {build.options.variableFiles.length > 0 && (
                  <EnvironmentField label="Var files" value={build.options.variableFiles.join(', ')} />
                )}
                {build.options.variables.length > 0 && (
                  <EnvironmentField label="Vars" value={build.options.variables.join(', ')} />
                )}
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
        )}
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

export function ArtifactsCard({ build }: { build: Build }) {
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

export function PackagesCard({
  build,
  inventoryLoading = false,
  inventoryFailure = null,
  inventoryProgress = { packages: 0 },
  scanned = false,
  packageFilter = '',
  vulnerabilityPath = '',
  onPackageFilterChange = () => {},
}: {
  build: Build
  inventoryLoading?: boolean
  inventoryFailure?: string | null
  inventoryProgress?: InventoryProgress
  scanned?: boolean
  packageFilter?: string
  vulnerabilityPath?: string
  onPackageFilterChange?: (identity: string) => void
}) {
  const [query, setQuery] = useState('')
  // '' = every package, 'affected' = only those with findings, or one band.
  const [findingFilter, setFindingFilter] = useState('')
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  if (inventoryLoading) {
    return (
      <Card>
        <CardTitle>Packages</CardTitle>
        <CardBody>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Spinner isInline aria-label="Reading package inventory…" />
            <Content component="p" aria-live="polite" style={{ margin: 0 }}>
              Reading package inventory… {countLabel(inventoryProgress.packages, 'package')} read so far.
            </Content>
          </div>
        </CardBody>
      </Card>
    )
  }
  if (inventoryFailure) {
    return (
      <Card>
        <CardTitle>Packages</CardTitle>
        <CardBody>
          <Alert variant="danger" isInline title="Package inventory could not be loaded">
            <Content component="p">{inventoryFailure}</Content>
          </Alert>
        </CardBody>
      </Card>
    )
  }
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
    if (packageFilter && packageIdentity(pkg) !== packageFilter) return false
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
  }
  const selectPerPage = (_event: unknown, nextPerPage: number) => {
    setPerPage(nextPerPage)
    setPage(1)
  }

  return (
    <Card>
      <CardTitle>Packages</CardTitle>
      <CardBody>
        <Content component="p" style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
          Reported by client-supplied SBOMs; dufflebag has not verified this inventory.
          {' '}{all.length} {all.length === 1 ? 'package' : 'packages'} · {affected.length} with findings.
        </Content>
        {scanned && build.packageInventory.scan?.observedAt ? (
          <AsOf observedAt={build.packageInventory.scan.observedAt} />
        ) : null}
        <Toolbar
          id="packages-toolbar"
          clearAllFilters={() => {
            setQuery('')
            setFindingFilter('')
            onPackageFilterChange('')
            setPage(1)
          }}
        >
          <ToolbarContent>
            <ToolbarFilter
              categoryName="Package"
              labels={packageFilter ? [packageFilter] : []}
              deleteLabel={() => {
                onPackageFilterChange('')
                setPage(1)
              }}
              showToolbarItem={false}
            >
              <span />
            </ToolbarFilter>
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
              vulnerabilityPath={vulnerabilityPath}
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
  packages, vulnerabilityPath,
}: {
  packages: Package[]
  vulnerabilityPath: string
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
      {packages.map((pkg) => (
          <Tbody key={`${pkg.purl}/${pkg.name}/${pkg.version}`}>
            <Tr>
              <Td dataLabel="Name"><code>{pkg.name}</code></Td>
              <Td dataLabel="Version">{pkg.version}</Td>
              <Td dataLabel="SBOM">{sbomNames(pkg)}</Td>
              <Td dataLabel="Findings">
                <PackageFindingLinks pkg={pkg} vulnerabilityPath={vulnerabilityPath} />
              </Td>
            </Tr>
          </Tbody>
      ))}
    </Table>
  )
}

function PackageFindingLinks({ pkg, vulnerabilityPath }: { pkg: Package; vulnerabilityPath: string }) {
  const counts = severityCounts(pkg.findings ?? [])
  if (counts.length === 0) {
    return <span style={{ color: 'var(--pf-t--global--text--color--disabled)' }} data-findings="none">—</span>
  }
  const href = withSearch(vulnerabilityPath, { package: packageIdentity(pkg) })
  return (
    <span style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
      {counts.map(({ severity, count }) => (
        <Label
          key={severity}
          href={href}
          color={SEVERITY_COLOUR[severity] ?? 'grey'}
          isCompact
        >
          {count} {severity}
        </Label>
      ))}
      <span style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>›</span>
    </span>
  )
}

export const PackageTableForTest = PackageTable
export const PackagesCardForTest = PackagesCard

export function VulnerabilitiesCard({
  findings,
  loading = false,
  failure = null,
  packageFilter = '',
  severityFilter = [],
  buildPath = '',
  onFiltersChange = () => {},
}: {
  findings: BuildFindingsData | null
  loading?: boolean
  failure?: string | null
  packageFilter?: string
  severityFilter?: Severity[]
  buildPath?: string
  onFiltersChange?: (packageFilter: string, severities: Severity[]) => void
}) {
  const [query, setQuery] = useState('')
  const [expanded, setExpanded] = useState<string | null>(null)
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  if (failure) {
    return (
      <Card>
        <CardTitle>Vulnerabilities</CardTitle>
        <CardBody>
          <Alert data-state="failed" variant="danger" isInline title="Findings could not be loaded">
            <Content component="p">{failure}</Content>
          </Alert>
        </CardBody>
      </Card>
    )
  }
  if (loading || !findings) {
    return (
      <Card>
        <CardTitle>Vulnerabilities</CardTitle>
        <CardBody>
          <div data-state="loading" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Spinner isInline aria-label="Reading findings…" />
            <Content component="p" aria-live="polite" style={{ margin: 0 }}>Reading findings…</Content>
          </div>
        </CardBody>
      </Card>
    )
  }
  if (findings.inventory === 'unparseable') {
    return (
      <Card>
        <CardTitle>Vulnerabilities</CardTitle>
        <CardBody><Content component="p" data-state="unparseable">SBOM unparseable</Content></CardBody>
      </Card>
    )
  }
  if (!findings.scannerConfigured) {
    return (
      <Card>
        <CardTitle>Vulnerabilities</CardTitle>
        <CardBody>
          <Content component="p" data-state="never-scanned">
            Not scanned. No vulnerability source is configured for this deployment.
          </Content>
        </CardBody>
      </Card>
    )
  }
  if (!findings.scanned || !findings.run) {
    return (
      <Card>
        <CardTitle>Vulnerabilities</CardTitle>
        <CardBody>
          <Content component="p" data-state="not-yet-scanned">
            Not yet scanned. Findings appear once the scanner has examined this build.
          </Content>
        </CardBody>
      </Card>
    )
  }

  const data = findings
  const normalized = query.trim().toLowerCase()
  const advisories = data.advisories.filter((advisory) => {
    if (packageFilter && !advisory.hits.some((hit) => hit.packageIdentity === packageFilter)) return false
    if (severityFilter.length > 0 && !severityFilter.includes(advisory.severity)) return false
    return !normalized || advisory.identifier.toLowerCase().includes(normalized) ||
      advisory.aliases.some((alias) => alias.toLowerCase().includes(normalized))
  })
  const lastPage = Math.max(1, Math.ceil(advisories.length / perPage))
  const currentPage = Math.min(page, lastPage)
  const first = (currentPage - 1) * perPage
  const visible = advisories.slice(first, first + perPage)
  const packagePath = buildPath.replace(/\/vulnerabilities$/, '/packages')
  const setSeverities = (next: Severity[]) => {
    onFiltersChange(packageFilter, next)
    setPage(1)
    setExpanded(null)
  }

  return (
    <Card>
      <CardTitle>Vulnerabilities</CardTitle>
      <CardBody>
        <Content component="p" style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
          Reported by client-supplied SBOMs; dufflebag has not verified this inventory.{' '}
          {data.advisories.length === 0
            ? `${countLabel(data.packagesTotal, 'package')} · 0 advisories.`
            : `${data.advisories.length} ${data.advisories.length === 1 ? 'advisory' : 'advisories'} · ${data.packagesAffected} of ${data.packagesTotal} packages affected.`}
        </Content>
        <AsOf observedAt={findings.run.observedAt} />
        {findings.latestAttempt?.status === 'failed' ? (
          <Content component="p" data-rescan="failed" style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
            The latest rescan failed on <When iso={findings.latestAttempt.observedAt} dateOnly />; these
            findings are from the scan above.
          </Content>
        ) : null}
        <ToggleGroup aria-label="Filter by severity" isCompact style={{ marginTop: 16 }}>
          {[...SEVERITY_ORDER].reverse().map((band) => (
            <ToggleGroupItem
              key={band}
              buttonId={`severity-${band}`}
              text={(
                <span style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                  <Label color={SEVERITY_COLOUR[band] ?? 'grey'} isCompact>{band}</Label>
                  {data.counts[band]}
                </span>
              )}
              isDisabled={data.counts[band] === 0}
              isSelected={severityFilter.includes(band)}
              onChange={(_event, selected) => setSeverities(
                selected
                  ? [...severityFilter, band]
                  : severityFilter.filter((severity) => severity !== band),
              )}
            />
          ))}
        </ToggleGroup>

        {data.topVulnerablePackages.length > 0 ? (
          <TopVulnerablePackages rows={data.topVulnerablePackages} packagePath={packagePath} />
        ) : null}

        {data.advisories.length === 0 ? (
          <Content component="p" data-state="zero-findings" style={{ marginTop: 18 }}>
            No findings in {countLabel(data.packagesTotal, 'package')}
          </Content>
        ) : (
          <div data-state="findings">
            <Toolbar
              id="vulnerabilities-toolbar"
              clearAllFilters={() => {
                setQuery('')
                onFiltersChange('', [])
                setPage(1)
                setExpanded(null)
              }}
            >
              <ToolbarContent>
                <ToolbarItem>
                  <SearchInput
                    aria-label="Filter by advisory or alias"
                    placeholder="Filter by advisory or alias"
                    value={query}
                    onChange={(_event, value) => {
                      setQuery(value)
                      setPage(1)
                    }}
                    onClear={() => {
                      setQuery('')
                      setPage(1)
                    }}
                  />
                </ToolbarItem>
                <ToolbarFilter
                  categoryName="Severity"
                  labels={severityFilter}
                  deleteLabel={(_category, label) => setSeverities(
                    severityFilter.filter((severity) => severity !== label),
                  )}
                >
                  <FormSelect
                    aria-label="Filter advisories by severity"
                    value={severityFilter.length === 1 ? severityFilter[0] : ''}
                    onChange={(_event, value) => setSeverities(value ? [value as Severity] : [])}
                  >
                    <FormSelectOption value="" label="All severities" />
                    {[...SEVERITY_ORDER].reverse().map((band) => (
                      <FormSelectOption key={band} value={band} label={band} />
                    ))}
                  </FormSelect>
                </ToolbarFilter>
                <ToolbarFilter
                  categoryName="Package"
                  labels={packageFilter ? [packageFilter] : []}
                  deleteLabel={() => {
                    onFiltersChange('', severityFilter)
                    setPage(1)
                  }}
                  showToolbarItem={false}
                >
                  <span />
                </ToolbarFilter>
                <ToolbarItem><Content component="p">{advisories.length} of {data.advisories.length}</Content></ToolbarItem>
                <ToolbarItem variant="pagination" align={{ default: 'alignEnd' }}>
                  <Pagination
                    itemCount={advisories.length}
                    page={currentPage}
                    perPage={perPage}
                    onSetPage={(_event, next) => {
                      setPage(next)
                      setExpanded(null)
                    }}
                    onPerPageSelect={(_event, next) => {
                      setPerPage(next)
                      setPage(1)
                      setExpanded(null)
                    }}
                    isCompact
                  />
                </ToolbarItem>
              </ToolbarContent>
            </Toolbar>
            {advisories.length === 0 ? (
              <Content component="p" style={{ marginTop: 14 }}>No advisories match these filters.</Content>
            ) : (
              <>
                <AdvisoryTable
                  advisories={visible}
                  expanded={expanded}
                  onToggle={setExpanded}
                  packagePath={packagePath}
                />
                <Pagination
                  itemCount={advisories.length}
                  page={currentPage}
                  perPage={perPage}
                  onSetPage={(_event, next) => {
                    setPage(next)
                    setExpanded(null)
                  }}
                  onPerPageSelect={(_event, next) => {
                    setPerPage(next)
                    setPage(1)
                    setExpanded(null)
                  }}
                  variant="bottom"
                  dropDirection="up"
                />
              </>
            )}
          </div>
        )}
      </CardBody>
    </Card>
  )
}

function AsOf({ observedAt }: { observedAt: string }) {
  return (
    <Content component="small" style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
      As of <When iso={observedAt} utc />
    </Content>
  )
}

function TopVulnerablePackages({
  rows, packagePath,
}: {
  rows: VulnerablePackage[]
  packagePath: string
}) {
  return (
    <div style={{ marginTop: 20 }}>
      <Title headingLevel="h3" size="md">Top vulnerable packages</Title>
      <Table aria-label="Top vulnerable packages" variant="compact" borders={false}>
        <Thead><Tr><Th>Name</Th><Th>Version</Th><Th>Findings</Th></Tr></Thead>
        <Tbody>
          {rows.map((pkg) => (
            <Tr key={pkg.identity}>
              <Td dataLabel="Name">
                <a href={withSearch(packagePath, { package: pkg.identity })}>{pkg.name}</a>
              </Td>
              <Td dataLabel="Version">{pkg.version}</Td>
              <Td dataLabel="Findings">
                <span style={{ display: 'flex', gap: 6 }}>
                  {pkg.critical > 0 ? <Label color="red" isCompact>{pkg.critical} critical</Label> : null}
                  {pkg.high > 0 ? <Label color="orange" isCompact>{pkg.high} high</Label> : null}
                </span>
              </Td>
            </Tr>
          ))}
        </Tbody>
      </Table>
    </div>
  )
}

function AdvisoryTable({
  advisories, expanded, onToggle, packagePath,
}: {
  advisories: Advisory[]
  expanded: string | null
  onToggle: (identifier: string | null) => void
  packagePath: string
}) {
  return (
    <Table aria-label="Advisories" variant="compact" style={{ marginTop: 14 }}>
      <Thead>
        <Tr>
          <Th>Advisory</Th>
          <Th>Severity</Th>
          <Th>Affected packages</Th>
          <Th>Published</Th>
          <Th>Fixed in</Th>
        </Tr>
      </Thead>
      {advisories.map((advisory, rowIndex) => {
        const isExpanded = expanded === advisory.identifier
        return (
          <Tbody key={advisory.identifier} isExpanded={isExpanded}>
            <Tr>
              <Td dataLabel="Advisory">{advisoryCell(advisory)}</Td>
              <Td dataLabel="Severity">{severityCell(advisory)}</Td>
              <Td
                dataLabel="Affected packages"
                compoundExpand={{
                  isExpanded,
                  onToggle: () => onToggle(isExpanded ? null : advisory.identifier),
                  rowIndex,
                  columnIndex: 2,
                }}
              >
                {countLabel(advisory.packages, 'package')}
              </Td>
              <Td dataLabel="Published">{publishedCell(advisory)}</Td>
              <Td dataLabel="Fixed in">{fixedInCell(advisory)}</Td>
            </Tr>
            {isExpanded ? (
              <Tr isExpanded>
                <Td dataLabel="Affected packages" colSpan={5} noPadding>
                  <ExpandableRowContent>
                    <AffectedPackagesTable advisory={advisory} packagePath={packagePath} />
                  </ExpandableRowContent>
                </Td>
              </Tr>
            ) : null}
          </Tbody>
        )
      })}
    </Table>
  )
}

function advisoryCell(advisory: Advisory) {
  return (
    <span style={{ display: 'block', minWidth: 0 }}>
      <code><Truncate content={advisory.identifier} /></code>
      {advisory.aliases.length > 0 ? (
        <code style={{ display: 'block', color: 'var(--pf-t--global--text--color--subtle)' }}>
          <Truncate content={advisory.aliases.join(', ')} />
        </code>
      ) : null}
    </span>
  )
}

function severityCell(advisory: Advisory) {
  return <Label color={SEVERITY_COLOUR[advisory.severity] ?? 'grey'} isCompact>{advisory.severity}</Label>
}

function publishedCell(advisory: Advisory) {
  return advisory.published ? <When iso={advisory.published} dateOnly /> : '—'
}

function fixedInCell(advisory: Advisory) {
  const versions = advisoryFixedVersions(advisory)
  if (versions.length === 0) {
    return <span style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>No fix available</span>
  }
  return <span><code>{versions[0]}</code>{versions.length > 1 ? ` +${versions.length - 1} more` : ''}</span>
}

function AffectedPackagesTable({ advisory, packagePath }: { advisory: Advisory; packagePath: string }) {
  return (
    <Table aria-label={`Affected packages for ${advisory.identifier}`} variant="compact" borders={false}>
      <Thead><Tr><Th>Name</Th><Th>Version</Th><Th>SBOM</Th><Th>Reported</Th><Th>Fixed in</Th></Tr></Thead>
      <Tbody>
        {advisory.hits.map((hit) => (
          <Tr key={[hit.packageIdentity, hit.sbomID, hit.fixedVersion].join('/') }>
            <Td dataLabel="Name">
              <a href={withSearch(packagePath, { package: hit.packageIdentity })}>{hit.name}</a>
            </Td>
            <Td dataLabel="Version">{hit.version}</Td>
            <Td dataLabel="SBOM">{hit.sbomID || '—'}</Td>
            <Td dataLabel="Reported" modifier="truncate">
              <code style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
                {hit.reported ? <Truncate content={hit.reported} /> : '—'}
              </code>
            </Td>
            <Td dataLabel="Fixed in">{hit.fixedVersion || 'No fix available'}</Td>
          </Tr>
        ))}
      </Tbody>
    </Table>
  )
}

function advisoryFixedVersions(advisory: Advisory): string[] {
  return [...new Set(advisory.hits.flatMap((hit) =>
    hit.fixedVersion.split(',').map((version) => version.trim()).filter(Boolean),
  ))]
}

function withSearch(path: string, values: Record<string, string>): string {
  const search = new URLSearchParams(values)
  return `${path}?${search}`
}

function sbomNames(pkg: Package): string {
  if (pkg.sboms.length === 0) return '—'
  return pkg.sboms.map((sbom) => sbom.name || sbom.id || sbom.format).filter(Boolean).join(', ') || '—'
}

function packageFacetCount(build: Build, inventoryLoading = false): FacetCount {
  return !inventoryLoading && build.packageInventory.status === 'parsed'
    ? knownCount(build.packageInventory.packages.length)
    : { status: 'unknown' }
}

function vulnerabilityFacetCount(findings: BuildFindingsData | null): FacetCount {
  return findings?.scanned && findings.inventory === 'parsed'
    ? knownCount(findings.advisories.length)
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

function packageSummary(build: Build): string {
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
        <DataList
          aria-label="SBOM downloads"
          onSelectDataListItem={(_event, sbomID) => {
            const sbom = sboms.find((candidate) => candidate.id === sbomID)
            if (sbom) void save(sbom)
          }}
        >
          {sboms.map((sbom) => (
            <DataListItem
              key={sbom.id || sbom.name}
              id={sbom.id}
              aria-labelledby={`sbom-${sbom.id}`}
              aria-label={`Download ${sbomFileName(sbom)}`}
            >
              <DataListItemRow>
                <DataListItemCells dataListCells={[
                  <DataListCell key="name">
                    <span id={`sbom-${sbom.id}`}>
                      <Truncate content={sbomFileName(sbom)} />
                    </span>
                  </DataListCell>,
                  <DataListCell key="format">
                    <Label isCompact>{sbom.format}</Label>
                  </DataListCell>,
                  <DataListCell key="download" isIcon alignRight>
                    <DownloadIcon aria-hidden />
                  </DataListCell>,
                ]} />
              </DataListItemRow>
            </DataListItem>
          ))}
        </DataList>
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
