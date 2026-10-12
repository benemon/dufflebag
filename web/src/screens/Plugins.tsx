import { useCallback, useEffect, useState } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Checkbox, Content, EmptyState, Flex, FlexItem,
  EmptyStateActions, EmptyStateBody, EmptyStateFooter, Label, MenuToggle, Modal, ModalBody, ModalFooter,
  ModalHeader, PageSection, Pagination, SearchInput, Select, SelectList, SelectOption, Spinner,
  Toolbar, ToolbarContent, ToolbarItem,
} from '@patternfly/react-core'
import { ActionsColumn, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate, useSearchParams } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import { TypedConfirmModal } from '../components/TypedConfirmModal'
import {
  PLUGIN_ARCHITECTURES, PLUGIN_OS_GROUPS, activeJobFor, catalogueRows, enablePluginRegistry, getPluginRegistry, listPluginImports, listPlugins,
  planCatalogueSync, retryPluginImport, sourceLabel, syncCatalogue,
  jobTitle, type CatalogueSourceFilter, type CatalogueSyncPlan, type CatalogueSyncResult, type JobStateFilter, type Plugin, type PluginImport,
  type PluginRegistry,
} from '../data/pluginRegistry'
import { useTenant } from '../data/tenant'
import { PluginJobsFacet, jobsPageSize, type JobsFilter } from './PluginJobs'
import { FacetRail, knownCount } from './RegistryFacets'

export function Plugins() {
  const { state, self, selectedOrganization, signOut } = useAuth()
  const { tenant } = useTenant()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? null
  const token = state?.token ?? ''
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const facet = searchParams.get('facet') === 'jobs' ? 'jobs' : 'plugins'
  const jobsFilter: JobsFilter = {
    state: (searchParams.get('state') as JobStateFilter | null) ?? undefined,
    plugin: searchParams.get('plugin') ?? undefined,
  }
  const jobsPage = Math.max(1, Number(searchParams.get('page')) || 1)
  const [registry, setRegistry] = useState<PluginRegistry | null>(null)
  const [plugins, setPlugins] = useState<Plugin[]>([])
  const [unfinished, setUnfinished] = useState<PluginImport[]>([])
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)
  const [jobs, setJobs] = useState<{ imports: PluginImport[]; total: number } | null>(null)
  const [jobsFailure, setJobsFailure] = useState<string | null>(null)
  const [jobsBusy, setJobsBusy] = useState(false)

  // quiet reloads (polling) keep the page as it is instead of showing the loading card.
  const reload = useCallback(async (quiet = false) => {
    if (!organizationID || token === '') {
      setRegistry(null)
      setLoading(false)
      return
    }
    if (!quiet) setLoading(true)
    try {
      const next = await getPluginRegistry(token, organizationID)
      const [nextPlugins, nextJobs] = next.enabled
        ? await Promise.all([listPlugins(token, organizationID), listPluginImports(token, organizationID, { state: 'unfinished', limit: 100 })])
        : [[], { imports: [], total: 0 }]
      setPlugins(nextPlugins)
      setUnfinished(nextJobs.imports)
      setRegistry(next)
      setFailure(null)
    } catch (error: unknown) {
      if (signOutIfUnauthorized(error, signOut)) return
      setFailure(pluginRegistryErrorMessage(error, 'Plugin registry could not be loaded.'))
    } finally {
      setLoading(false)
    }
  }, [organizationID, signOut, token])

  const loadJobs = useCallback(async (quiet = false) => {
    if (!organizationID || token === '' || facet !== 'jobs') return
    if (!quiet) setJobs(null)
    try {
      setJobs(await listPluginImports(token, organizationID, { ...jobsFilter, limit: jobsPageSize, offset: (jobsPage - 1) * jobsPageSize }))
      setJobsFailure(null)
    } catch (error: unknown) {
      if (!signOutIfUnauthorized(error, signOut)) setJobsFailure(pluginRegistryErrorMessage(error, 'Jobs could not be loaded.'))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [organizationID, token, facet, jobsFilter.state, jobsFilter.plugin, jobsPage, signOut])

  useEffect(() => { void reload() }, [reload])
  useEffect(() => { void loadJobs() }, [loadJobs])
  // Poll only while a queued or running job is on screen, in the catalogue's
  // unfinished set or on the Jobs page; the worker finishes them in order.
  useEffect(() => {
    const onScreen = unfinished.length > 0 || (jobs?.imports ?? []).some((job) => job.state === 'queued' || job.state === 'running')
    if (!onScreen) return
    const timer = setTimeout(() => { void reload(true); void loadJobs(true) }, 3000)
    return () => clearTimeout(timer)
  }, [unfinished, jobs, reload, loadJobs])

  const setQuery = (changes: Record<string, string | undefined>) => {
    const next = new URLSearchParams(searchParams)
    for (const [key, value] of Object.entries(changes)) {
      if (value === undefined) next.delete(key); else next.set(key, value)
    }
    setSearchParams(next)
  }

  if (!organizationID) {
    return <PageSection><Alert variant="info" isInline title="Select an organization to manage plugins" /></PageSection>
  }

  return (
    <PluginRegistryView
      organizationName={tenant.organization}
      callerRole={self?.role ?? null}
      registry={registry}
      plugins={plugins}
      unfinished={unfinished}
      facet={facet}
      onFacet={(next) => setQuery({ facet: next === 'jobs' ? 'jobs' : undefined, page: undefined })}
      jobs={jobs} jobsFilter={jobsFilter} jobsPage={jobsPage} jobsFailure={jobsFailure} jobsBusy={jobsBusy}
      onJobsFilter={(filter) => setQuery({ state: filter.state, plugin: filter.plugin, page: undefined })}
      onJobsPage={(page) => setQuery({ page: page > 1 ? String(page) : undefined })}
      onRetryJob={(job) => void (async () => {
        setJobsBusy(true)
        try {
          const next = await retryPluginImport(token, organizationID, job)
          navigate(`/plugin-registry/imports/${next.id}`)
        } catch (error: unknown) {
          if (!signOutIfUnauthorized(error, signOut)) setJobsFailure(pluginRegistryErrorMessage(error, 'The retry could not be queued.'))
        } finally {
          setJobsBusy(false)
        }
      })()}
      onBackToRegistry={() => navigate('/buckets')}
      onOpenPlugin={(name) => navigate(`/plugin-registry/${encodeURIComponent(name)}`)}
      onUpload={() => navigate('/plugin-registry/upload')}
      onBrowse={() => navigate('/plugin-registry/hashicorp')}
      onImportGithub={() => navigate('/plugin-registry/github')}
      onOpenImport={(id) => navigate(`/plugin-registry/imports/${id}`)}
      onPlanSync={(chosen) => planCatalogueSync(token, organizationID, chosen)}
      onSyncSelected={async (names) => {
        const results = await syncCatalogue(token, organizationID, names)
        await reload()
        return results
      }}
      loading={loading}
      failure={failure}
      onRefresh={() => Promise.all([reload(), loadJobs()]).then(() => undefined)}
      onEnable={async () => {
        const next = await enablePluginRegistry(token, organizationID)
        setRegistry(next)
      }}
    />
  )
}

export function pluginRegistryErrorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}

type PluginRegistryViewProps = {
  organizationName: string
  callerRole: Role | null
  registry: PluginRegistry | null
  plugins: Plugin[]
  unfinished: PluginImport[]
  facet: 'plugins' | 'jobs'
  onFacet: (facet: 'plugins' | 'jobs') => void
  jobs: { imports: PluginImport[]; total: number } | null
  jobsFilter: JobsFilter
  jobsPage: number
  jobsFailure: string | null
  jobsBusy: boolean
  onJobsFilter: (filter: JobsFilter) => void
  onJobsPage: (page: number) => void
  onRetryJob: (job: PluginImport) => void
  onBackToRegistry: () => void
  onOpenPlugin: (name: string) => void
  onUpload: () => void
  onBrowse: () => void
  onImportGithub: () => void
  onOpenImport: (id: string) => void
  onPlanSync: (plugins: Plugin[]) => Promise<CatalogueSyncPlan[]>
  onSyncSelected: (names: string[]) => Promise<CatalogueSyncResult[]>
  loading: boolean
  failure: string | null
  onRefresh: () => void | Promise<void>
  onEnable: () => Promise<void>
}

export function PluginRegistryView(props: PluginRegistryViewProps) {
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const canConfigure = permitsAction(props.callerRole, 'configurePluginRegistry')
  const canPublish = permitsAction(props.callerRole, 'publishPlugin')

  const run = async (work: () => Promise<void>) => {
    setActionFailure(null)
    setBusy(true)
    try {
      await work()
    } catch (error: unknown) {
      setActionFailure(pluginRegistryErrorMessage(error, 'The plugin registry action failed.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <ScreenHeader
        breadcrumbs={(
          <Breadcrumb>
            <BreadcrumbItem component="button" onClick={props.onBackToRegistry}>Registry</BreadcrumbItem>
            <BreadcrumbItem isActive>Plugins</BreadcrumbItem>
          </Breadcrumb>
        )}
        title="Plugins"
        actions={canPublish && props.registry?.enabled ? (
          <>
            <Button variant="primary" onClick={props.onBrowse}>Browse HashiCorp</Button>{' '}
            <Button variant="secondary" onClick={props.onImportGithub}>Import from GitHub</Button>{' '}
            <Button variant="secondary" onClick={props.onUpload}>Upload</Button>
          </>
        ) : undefined}
        onRefresh={props.onRefresh}
        refreshing={props.loading}
      />
      <PageSection
        variant="secondary" isFilled hasBodyWrapper={false}
        padding={{ default: props.loading || props.failure || !props.registry?.enabled ? 'padding' : 'noPadding' }}
      >
        {actionFailure ? (
          <Alert variant="danger" isInline title="The action failed">
            <Content component="p">{actionFailure}</Content>
          </Alert>
        ) : null}
        {props.loading ? (
          <PluginLoadingCard message="Loading plugins…" />
        ) : props.failure ? (
          <PluginErrorCard title="Plugins could not be loaded" error={props.failure} onRetry={props.onRefresh} />
        ) : !props.registry ? null : !props.registry.enabled ? (
          <EmptyState titleText="The plugin registry isn't enabled" headingLevel="h2">
            <EmptyStateBody>
              Enable it to mirror Packer plugins for {props.organizationName}, so packer init resolves
              them from dufflebag instead of the internet.
            </EmptyStateBody>
            {canConfigure ? (
              <EmptyStateFooter><EmptyStateActions>
                <Button variant="primary" isLoading={busy} isDisabled={busy} onClick={() => void run(props.onEnable)}>
                  Enable the registry
                </Button>
              </EmptyStateActions></EmptyStateFooter>
            ) : null}
          </EmptyState>
        ) : (
          <EnabledRegistry
            {...props}
          />
        )}
      </PageSection>
    </>
  )
}

function EnabledRegistry({
  organizationName, callerRole, registry, plugins, unfinished, facet, onFacet, jobs, jobsFilter, jobsPage, jobsFailure, jobsBusy,
  onJobsFilter, onJobsPage, onRetryJob, onOpenPlugin, onUpload, onBrowse, onImportGithub, onOpenImport, onPlanSync, onSyncSelected, onRefresh,
}: PluginRegistryViewProps) {
  const exposed = registry?.exposed ?? false
  const canPublish = permitsAction(callerRole, 'publishPlugin')
  const notExposed = !exposed ? (
    <Alert variant="info" isInline title="The registry is enabled but not exposed">
      <Content component="p">
        Plugins below are stored and can be managed, but Packer can’t reach them until the registry is exposed.
      </Content>
      <Button component="a" variant="link" isInline href="/plugin-registry/settings">Plugin registry</Button>
    </Alert>
  ) : null

  return (
    <FacetRail
      active={facet}
      onSelect={onFacet}
      heading="Plugins"
      label="Plugin facets"
      unmountOnExit
      facets={[
        {
          key: 'plugins', label: 'Plugins', count: knownCount(plugins.length),
          content: (
            <>
              {notExposed}
              <PluginCatalogue
                organizationName={organizationName} plugins={plugins} unfinished={unfinished} canPublish={canPublish}
                onOpenPlugin={onOpenPlugin} onUpload={onUpload} onBrowse={onBrowse} onImportGithub={onImportGithub}
                onOpenImport={onOpenImport} onPlanSync={onPlanSync} onSyncSelected={onSyncSelected}
              />
            </>
          ),
        },
        {
          // The badge counts queued and running jobs, and hides at zero: a total of every job says nothing useful.
          key: 'jobs', label: 'Jobs', count: unfinished.length ? { status: 'known', value: unfinished.length, unread: true } : undefined,
          content: (
            <>
              {notExposed}
              <PluginJobsFacet
                jobs={jobs?.imports ?? []} total={jobs?.total ?? 0} filter={jobsFilter} page={jobsPage}
                plugins={[...new Set([...plugins.map((plugin) => plugin.name), ...unfinished.map((job) => job.plugin)])].sort()}
                loading={!jobs && !jobsFailure} failure={jobsFailure} canPublish={canPublish} busy={jobsBusy}
                onFilter={onJobsFilter} onPage={onJobsPage} onOpenJob={onOpenImport} onRetry={onRetryJob} onRefresh={onRefresh}
              />
            </>
          ),
        },
      ]}
    />
  )
}

export function DisablePluginRegistryConfirmation({
  organizationName, busy, onConfirm, onCancel,
}: {
  organizationName: string
  busy: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <TypedConfirmModal
      title="Disable the plugin registry?"
      body={<Content component="p">All plugin data is destroyed when the registry is disabled.</Content>}
      expected={organizationName}
      verb="Disable registry"
      busy={busy}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  )
}

export function PluginRegistryConfirmation({
  title, body, verb, busy, danger = false, onConfirm, onCancel,
}: {
  title: string
  body: string
  verb: string
  busy: boolean
  danger?: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <Modal aria-labelledby="plugin-registry-confirm-title" isOpen onClose={onCancel} variant="small">
      <PluginRegistryConfirmationView
        title={title} body={body} verb={verb} busy={busy} danger={danger}
        onConfirm={onConfirm} onCancel={onCancel}
      />
    </Modal>
  )
}

export function PluginRegistryConfirmationView({
  title, body, verb, busy, danger = false, onConfirm, onCancel,
}: {
  title: string
  body: string
  verb: string
  busy: boolean
  danger?: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <>
      <ModalHeader labelId="plugin-registry-confirm-title" title={title} />
      <ModalBody><Content component="p">{body}</Content></ModalBody>
      <ModalFooter>
        <Button variant={danger ? 'danger' : 'primary'} isLoading={busy} isDisabled={busy} onClick={onConfirm}>{verb}</Button>
        <Button variant="link" isDisabled={busy} onClick={onCancel}>Cancel</Button>
      </ModalFooter>
    </>
  )
}

const sourceFilters: readonly CatalogueSourceFilter[] = ['All sources', 'HashiCorp', 'GitHub', 'Local']
const pageSize = 20

function updateNote(plugin: Plugin): string {
  if (plugin.source.kind === 'upload') return 'Not checked (upload)'
  return plugin.update_check.enabled ? 'Up to date' : 'Checks off'
}

// A first import has no plugin row until a version is stored; the catalogue
// shows the job's row meanwhile, so the import never looks cancelled.
export function pendingFirstImports(plugins: Plugin[], unfinished: PluginImport[]): Plugin[] {
  const held = new Set(plugins.map((plugin) => plugin.name))
  const rows: Plugin[] = []
  for (const job of [...unfinished].reverse()) {
    if (held.has(job.plugin) || job.changes.length) continue
    held.add(job.plugin)
    rows.push({
      name: job.plugin, source: { kind: job.source as Plugin['source']['kind'], repository: job.source === 'upload' ? undefined : job.product },
      published_versions: 0, update_available: false, update_check: { enabled: false },
    })
  }
  return rows
}

export function catalogueJobLabel(job: PluginImport): string {
  const verb = jobTitle(job).split(' ')[0]
  if (job.state === 'running') return `${verb} running · ${job.outcomes.length} of ${job.changes.length || job.versions.length}`
  return `${verb} queued · ${job.queued_ahead} ahead`
}

export function PluginCatalogue({
  organizationName, plugins, unfinished, canPublish, onOpenPlugin, onUpload, onBrowse, onImportGithub, onOpenImport, onPlanSync, onSyncSelected,
}: {
  organizationName: string
  plugins: Plugin[]
  unfinished: PluginImport[]
  canPublish: boolean
  onOpenPlugin: (name: string) => void
  onUpload: () => void
  onBrowse: () => void
  onImportGithub: () => void
  onOpenImport: (id: string) => void
  onPlanSync: (plugins: Plugin[]) => Promise<CatalogueSyncPlan[]>
  onSyncSelected: (names: string[]) => Promise<CatalogueSyncResult[]>
}) {
  const [filter, setFilter] = useState('')
  const [source, setSource] = useState<CatalogueSourceFilter>('All sources')
  const [sourceOpen, setSourceOpen] = useState(false)
  const [updatesOnly, setUpdatesOnly] = useState(false)
  const [page, setPage] = useState(1)
  const [selected, setSelected] = useState<string[]>([])
  const [queued, setQueued] = useState<CatalogueSyncResult[]>([])
  const [confirming, setConfirming] = useState(false)
  const [plan, setPlan] = useState<CatalogueSyncPlan[] | null>(null)
  const [syncing, setSyncing] = useState(false)
  const [syncFailure, setSyncFailure] = useState<string | null>(null)

  const pending = pendingFirstImports(plugins, unfinished)
  if (plugins.length === 0 && pending.length === 0) {
    return (
      <EmptyState titleText="No plugins mirrored yet" headingLevel="h2">
        <EmptyStateBody>
          Mirror a Packer plugin here and <code>packer init</code> resolves it from dufflebag instead of the internet.
          Each plugin comes from one source.
        </EmptyStateBody>
        {canPublish ? (
          <EmptyStateFooter>
            <EmptyStateActions><Button variant="primary" onClick={onBrowse}>Browse HashiCorp</Button></EmptyStateActions>
            <EmptyStateActions>
              <Button variant="link" onClick={onImportGithub}>Import from a GitHub release</Button>
              <Button variant="link" onClick={onUpload}>Upload plugin files</Button>
            </EmptyStateActions>
          </EmptyStateFooter>
        ) : null}
      </EmptyState>
    )
  }

  const jobFor = (plugin: Plugin) => activeJobFor(unfinished, plugin.name)
  const rows = catalogueRows([...plugins, ...pending], { name: filter, source, updatesOnly })
  const shown = rows.slice((page - 1) * pageSize, page * pageSize)
  const selectable = (plugin: Plugin) => plugin.update_available && !jobFor(plugin)
  const pageSelectable = shown.filter(selectable).map((plugin) => plugin.name)
  const chosen = plugins.filter((plugin) => selected.includes(plugin.name))

  const openConfirm = async (names: string[]) => {
    setSelected(names)
    setConfirming(true)
    setPlan(null)
    setSyncFailure(null)
    try {
      setPlan(await onPlanSync(plugins.filter((plugin) => names.includes(plugin.name))))
    } catch (error: unknown) {
      setConfirming(false)
      setSyncFailure(pluginRegistryErrorMessage(error, 'The sync could not be planned.'))
    }
  }
  const start = async () => {
    setSyncing(true)
    try {
      const results = await onSyncSelected(selected)
      setQueued((current) => [...current.filter((r) => !results.some((n) => n.plugin === r.plugin)), ...results])
      setSelected([])
      setConfirming(false)
    } catch (error: unknown) {
      setSyncFailure(pluginRegistryErrorMessage(error, 'The sync could not be queued.'))
    } finally {
      setSyncing(false)
    }
  }
  const jobs = queued.filter((r) => r.import_id)
  const refused = queued.filter((r) => !r.import_id)

  return (
    <>
      {syncFailure ? <Alert variant="danger" isInline title="The sync could not be queued"><Content component="p">{syncFailure}</Content></Alert> : null}
      {jobs.length ? (
        <Alert variant="success" isInline title={`${jobs.length} sync ${jobs.length === 1 ? 'job' : 'jobs'} queued`}>
          <Content component="p">
            One job per plugin. Each brings the plugin to its newest stable version with the architectures it already has.
          </Content>
          {jobs.map((job) => (
            <Button key={job.plugin} variant="link" isInline onClick={() => onOpenImport(job.import_id ?? '')}>
              {job.plugin} → {job.version}
            </Button>
          ))}
          {refused.length ? <Content component="p">Not queued: {refused.map((r) => `${r.plugin} (${r.refused})`).join('; ')}.</Content> : null}
        </Alert>
      ) : null}
      <Card>
        <CardTitle>Plugins</CardTitle>
        <CardBody>
        <Toolbar id="plugins-toolbar">
          <ToolbarContent alignItems="center">
            <ToolbarItem>
              <SearchInput
                aria-label="Filter plugins by name" placeholder="Filter by name" value={filter}
                onChange={(_event, value) => { setFilter(value); setPage(1) }} onClear={() => setFilter('')}
              />
            </ToolbarItem>
            <ToolbarItem>
              <Select
                isOpen={sourceOpen} selected={source} onOpenChange={setSourceOpen}
                onSelect={(_event, value) => { setSource(value as CatalogueSourceFilter); setSourceOpen(false); setPage(1) }}
                toggle={(ref) => <MenuToggle ref={ref} onClick={() => setSourceOpen(!sourceOpen)} isExpanded={sourceOpen}>{source}</MenuToggle>}
              >
                <SelectList>{sourceFilters.map((option) => <SelectOption key={option} value={option}>{option}</SelectOption>)}</SelectList>
              </Select>
            </ToolbarItem>
            <ToolbarItem>
              <Checkbox id="updates-only" label="Update available" isChecked={updatesOnly} onChange={(_event, checked) => { setUpdatesOnly(checked); setPage(1) }} />
            </ToolbarItem>
            {canPublish ? (
              <>
                <ToolbarItem variant="separator" />
                <ToolbarItem><span style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>{selected.length ? `${selected.length} selected` : 'Select plugins with an update'}</span></ToolbarItem>
                <ToolbarItem>
                  <Button variant="primary" isDisabled={selected.length === 0} onClick={() => void openConfirm(selected)}>Sync selected</Button>
                </ToolbarItem>
              </>
            ) : null}
            <ToolbarItem variant="pagination" align={{ default: 'alignEnd' }}>
              <Pagination
                isCompact itemCount={rows.length} perPage={pageSize} page={page} perPageOptions={[{ title: '20', value: 20 }]}
                onSetPage={(_event, next) => setPage(next)} titles={{ paginationAriaLabel: 'Plugins pagination' }}
              />
            </ToolbarItem>
          </ToolbarContent>
        </Toolbar>
        <Table aria-label="Plugins" variant="compact" selectableRowCaptionText="Plugin">
          <Thead>
            <Tr>
              {canPublish ? (
                <Th
                  aria-label="Select plugins with an update"
                  select={{
                    isSelected: pageSelectable.length > 0 && pageSelectable.every((name) => selected.includes(name)),
                    isIndeterminate: pageSelectable.some((name) => selected.includes(name)) && !pageSelectable.every((name) => selected.includes(name)),
                    isHeaderSelectDisabled: pageSelectable.length === 0,
                    onSelect: (_event, isSelecting) => setSelected(isSelecting
                      ? [...new Set([...selected, ...pageSelectable])] : selected.filter((name) => !pageSelectable.includes(name))),
                  }}
                />
              ) : null}
              <Th>Name</Th><Th>Source</Th><Th>Newest mirrored</Th><Th>Versions</Th><Th>Updates</Th>
              {canPublish ? <Th screenReaderText="Actions" /> : null}
            </Tr>
          </Thead>
          <Tbody>
            {shown.map((plugin, index) => (
              <Tr key={plugin.name} isSelectable={canPublish && selectable(plugin)} isRowSelected={selected.includes(plugin.name)}>
                {canPublish ? (
                  <Td
                    title={selectable(plugin) ? undefined : jobFor(plugin) ? `A job for ${plugin.name} is already ${jobFor(plugin)?.state}` : 'Only plugins with an update available can be synced'}
                    select={{
                      rowIndex: (page - 1) * pageSize + index,
                      isSelected: selected.includes(plugin.name),
                      isDisabled: !selectable(plugin),
                      onSelect: (_e, checked) => setSelected(checked ? [...selected, plugin.name] : selected.filter((n) => n !== plugin.name)),
                    }}
                  />
                ) : null}
                <Td dataLabel="Name">
                  {plugin.published_versions === 0 && jobFor(plugin)
                    ? <span style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>{plugin.name}</span>
                    : <Button variant="link" isInline onClick={() => onOpenPlugin(plugin.name)}>{plugin.name}</Button>}
                  <Content component="small"> {organizationName}/{plugin.name}</Content>
                </Td>
                <Td dataLabel="Source">
                  <Label isCompact>{sourceLabel(plugin.source)}</Label>
                  {plugin.source.repository ? <> <code>{plugin.source.repository}</code></> : null}
                </Td>
                <Td dataLabel="Newest mirrored">{plugin.newest_version ?? (jobFor(plugin) ? 'None yet' : 'None available')}</Td>
                <Td dataLabel="Versions">{plugin.published_versions}</Td>
                <Td dataLabel="Updates">
                  {jobFor(plugin) ? (
                    <Button variant="link" isInline onClick={() => onOpenImport(jobFor(plugin)?.id ?? '')}>
                      <Label isCompact color="blue" variant={jobFor(plugin)?.state === 'running' ? 'filled' : 'outline'}>{catalogueJobLabel(jobFor(plugin) as PluginImport)}</Label>
                    </Button>
                  ) : plugin.update_available ? <Label isCompact color="purple">Update available · {plugin.update_check.latest}</Label>
                    : <Content component="small">{updateNote(plugin)}</Content>}
                </Td>
                {canPublish ? (
                  <Td isActionCell>
                    <ActionsColumn items={[
                      { title: 'Open', onClick: () => onOpenPlugin(plugin.name) },
                      { title: 'Sync', isDisabled: !selectable(plugin), onClick: () => void openConfirm([plugin.name]) },
                    ]} />
                  </Td>
                ) : null}
              </Tr>
            ))}
          </Tbody>
        </Table>
        {rows.length === 0 ? <Content component="p">No plugins match.</Content> : null}
        <Pagination
          variant="bottom" itemCount={rows.length} perPage={pageSize} page={page} perPageOptions={[{ title: '20', value: 20 }]}
          onSetPage={(_event, next) => setPage(next)} titles={{ paginationAriaLabel: 'Plugins pagination, bottom' }}
        />
        </CardBody>
      </Card>
      {confirming ? (
        <CatalogueSyncConfirmation
          plugins={chosen} plan={plan} busy={syncing}
          onCancel={() => { setConfirming(false); setPlan(null) }} onStart={() => void start()}
        />
      ) : null}
    </>
  )
}

export function CatalogueSyncConfirmation({ plugins, plan, busy, onCancel, onStart }: {
  plugins: Plugin[]
  plan: CatalogueSyncPlan[] | null
  busy: boolean
  onCancel: () => void
  onStart: () => void
}) {
  return (
    <Modal isOpen variant="medium" aria-labelledby="catalogue-sync-title" onClose={onCancel}>
      <CatalogueSyncConfirmationView plugins={plugins} plan={plan} busy={busy} onCancel={onCancel} onStart={onStart} />
    </Modal>
  )
}

export function CatalogueSyncConfirmationView({ plugins, plan, busy, onCancel, onStart }: {
  plugins: Plugin[]
  plan: CatalogueSyncPlan[] | null
  busy: boolean
  onCancel: () => void
  onStart: () => void
}) {
  const count = plugins.length
  return (
    <>
      <ModalHeader labelId="catalogue-sync-title" title={`Sync ${count} ${count === 1 ? 'plugin' : 'plugins'}`} />
      <ModalBody>
        <Content component="p">
          Each plugin moves to its newest stable version, with the architectures the plugin already has.
          This starts {count} separate {count === 1 ? 'job' : 'jobs'}, one per plugin.
        </Content>
        {plan ? (
          <Table aria-label="Plugins to sync" variant="compact">
            <Thead><Tr><Th>Plugin</Th><Th>Version</Th><Th>Architectures</Th></Tr></Thead>
            <Tbody>
              {plan.map((row) => (
                <Tr key={row.plugin}>
                  <Td dataLabel="Plugin">{row.plugin}</Td>
                  <Td dataLabel="Version">{row.from} → {row.to}</Td>
                  <Td dataLabel="Architectures">
                    {row.architectures}
                    {row.warning ? <Content component="small" style={{ color: 'var(--pf-t--global--color--status--warning--default)' }}>{row.warning}</Content> : null}
                  </Td>
                </Tr>
              ))}
            </Tbody>
          </Table>
        ) : <Spinner aria-label="Planning the sync…" />}
      </ModalBody>
      <ModalFooter>
        <Button variant="primary" isDisabled={!plan || busy} isLoading={busy} onClick={onStart}>Start {count} {count === 1 ? 'job' : 'jobs'}</Button>
        <Button variant="link" isDisabled={busy} onClick={onCancel}>Cancel</Button>
      </ModalFooter>
    </>
  )
}

export function DefaultPlatformsEditor({ platforms, busy, onSave }: {
  platforms: string[]
  busy: boolean
  onSave: (platforms: string[]) => void
}) {
  const [draft, setDraft] = useState<string[]>(platforms)
  useEffect(() => { setDraft(platforms) }, [platforms])
  const changed = [...draft].sort().join(',') !== [...platforms].sort().join(',')
  return (
    <>
      <Content component="p">
        Preselected when a plugin is first imported; later imports keep the platforms the plugin already has.
        The list is the platforms HashiCorp builds Packer plugins for.
      </Content>
      <Flex gap={{ default: 'gapLg' }} flexWrap={{ default: 'wrap' }}>
        {PLUGIN_OS_GROUPS.map(([os, arches]) => (
          <FlexItem key={os}>
            <Content component="small">{os}</Content>
            {arches.map((arch) => {
              const platform = `${os}_${arch}`
              return (
                <Checkbox
                  key={platform} id={`default-platform-${platform}`} label={<code>{arch}</code>}
                  isChecked={draft.includes(platform)} isDisabled={busy}
                  onChange={(_event, checked) => setDraft(checked ? [...draft, platform] : draft.filter((p) => p !== platform))}
                />
              )
            })}
          </FlexItem>
        ))}
      </Flex>
      <Button variant="secondary" isDisabled={busy || draft.length === 0 || !changed} onClick={() => onSave(PLUGIN_ARCHITECTURES.filter((p) => draft.includes(p)))} style={{ marginTop: 12 }}>
        Save default platforms
      </Button>
    </>
  )
}
