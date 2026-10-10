import { useCallback, useEffect, useState } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, Checkbox, Content, EmptyState,
  EmptyStateActions, EmptyStateBody, EmptyStateFooter, Label, MenuToggle, Modal, ModalBody, ModalFooter,
  ModalHeader, PageSection, Pagination, SearchInput, Select, SelectList, SelectOption, Spinner, TextInput,
  Toolbar, ToolbarContent, ToolbarItem,
} from '@patternfly/react-core'
import { ActionsColumn, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import { TypedConfirmModal } from '../components/TypedConfirmModal'
import {
  catalogueRows, enablePluginRegistry, getPluginRegistry, listPlugins, planCatalogueSync, sourceLabel, syncCatalogue,
  type CatalogueSourceFilter, type CatalogueSyncPlan, type CatalogueSyncResult, type Plugin,
  type PluginRegistry,
} from '../data/pluginRegistry'
import { useTenant } from '../data/tenant'

export function Plugins() {
  const { state, self, selectedOrganization, signOut } = useAuth()
  const { tenant } = useTenant()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? null
  const token = state?.token ?? ''
  const navigate = useNavigate()
  const [registry, setRegistry] = useState<PluginRegistry | null>(null)
  const [plugins, setPlugins] = useState<Plugin[]>([])
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)

  const reload = useCallback(async () => {
    if (!organizationID || token === '') {
      setRegistry(null)
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      const next = await getPluginRegistry(token, organizationID)
      setPlugins(next.enabled ? await listPlugins(token, organizationID) : [])
      setRegistry(next)
      setFailure(null)
    } catch (error: unknown) {
      if (signOutIfUnauthorized(error, signOut)) return
      setFailure(pluginRegistryErrorMessage(error, 'Plugin registry could not be loaded.'))
    } finally {
      setLoading(false)
    }
  }, [organizationID, signOut, token])

  useEffect(() => { void reload() }, [reload])

  if (!organizationID) {
    return <PageSection><Alert variant="info" isInline title="Select an organization to manage plugins" /></PageSection>
  }

  return (
    <PluginRegistryView
      organizationName={tenant.organization}
      callerRole={self?.role ?? null}
      host={window.location.hostname}
      registry={registry}
      plugins={plugins}
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
      onRefresh={reload}
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
  host: string
  registry: PluginRegistry | null
  plugins: Plugin[]
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
        description={`Packer plugins mirrored for ${props.organizationName}. packer init resolves them from ${props.host}/plugins/${props.organizationName}.${
          canPublish ? '' : ' You have read-only access; publishers add and sync plugins.'}`}
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
      <PageSection variant="secondary" isFilled>
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
  organizationName, callerRole, registry, plugins, onOpenPlugin, onUpload, onBrowse, onImportGithub,
  onOpenImport, onPlanSync, onSyncSelected,
}: PluginRegistryViewProps) {
  const exposed = registry?.exposed ?? false

  return (
    <>
      {!exposed ? (
        <Alert variant="info" isInline title="The registry is enabled but not exposed">
          <Content component="p">
            Plugins below are stored and can be managed, but Packer can’t reach them until the registry is exposed.
          </Content>
          <Button component="a" variant="link" isInline href="/plugin-registry/settings">Registry settings</Button>
        </Alert>
      ) : null}
      <PluginCatalogue
        organizationName={organizationName} plugins={plugins}
        canPublish={permitsAction(callerRole, 'publishPlugin')}
        onOpenPlugin={onOpenPlugin} onUpload={onUpload} onBrowse={onBrowse} onImportGithub={onImportGithub}
        onOpenImport={onOpenImport} onPlanSync={onPlanSync} onSyncSelected={onSyncSelected}
      />
    </>
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
  title, body, verb, busy, onConfirm, onCancel,
}: {
  title: string
  body: string
  verb: string
  busy: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <Modal aria-labelledby="plugin-registry-confirm-title" isOpen onClose={onCancel} variant="small">
      <PluginRegistryConfirmationView
        title={title} body={body} verb={verb} busy={busy}
        onConfirm={onConfirm} onCancel={onCancel}
      />
    </Modal>
  )
}

export function PluginRegistryConfirmationView({
  title, body, verb, busy, onConfirm, onCancel,
}: {
  title: string
  body: string
  verb: string
  busy: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <>
      <ModalHeader labelId="plugin-registry-confirm-title" title={title} />
      <ModalBody><Content component="p">{body}</Content></ModalBody>
      <ModalFooter>
        <Button variant="primary" isLoading={busy} isDisabled={busy} onClick={onConfirm}>{verb}</Button>
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

export function PluginCatalogue({
  organizationName, plugins, canPublish, onOpenPlugin, onUpload, onBrowse, onImportGithub, onOpenImport, onPlanSync, onSyncSelected,
}: {
  organizationName: string
  plugins: Plugin[]
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

  if (plugins.length === 0) {
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

  const queuedNames = new Set(queued.filter((r) => r.import_id).map((r) => r.plugin))
  const rows = catalogueRows(plugins, { name: filter, source, updatesOnly })
  const shown = rows.slice((page - 1) * pageSize, page * pageSize)
  const selectable = (plugin: Plugin) => plugin.update_available && !queuedNames.has(plugin.name)
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
        <Toolbar>
          <ToolbarContent>
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
                <ToolbarItem><Content component="small">{selected.length ? `${selected.length} selected` : 'Select plugins with an update'}</Content></ToolbarItem>
                <ToolbarItem>
                  <Button variant="primary" isDisabled={selected.length === 0} onClick={() => void openConfirm(selected)}>Sync selected</Button>
                </ToolbarItem>
              </>
            ) : null}
            <ToolbarItem align={{ default: 'alignEnd' }}>
              <Pagination
                isCompact itemCount={rows.length} perPage={pageSize} page={page} perPageOptions={[{ title: '20', value: 20 }]}
                onSetPage={(_event, next) => setPage(next)} titles={{ paginationAriaLabel: 'Plugins pagination' }}
              />
            </ToolbarItem>
          </ToolbarContent>
        </Toolbar>
        <Table aria-label="Plugins" variant="compact">
          <Thead>
            <Tr>
              {canPublish ? <Th screenReaderText="Select" /> : null}
              <Th>Name</Th><Th>Source</Th><Th>Newest mirrored</Th><Th>Versions</Th><Th>Updates</Th>
              {canPublish ? <Th screenReaderText="Actions" /> : null}
            </Tr>
          </Thead>
          <Tbody>
            {shown.map((plugin) => (
              <Tr key={plugin.name} isRowSelected={selected.includes(plugin.name)}>
                {canPublish ? (
                  <Td dataLabel="Select">
                    <Checkbox
                      id={`select-${plugin.name}`} aria-label={`Select ${plugin.name} to sync`}
                      isDisabled={!selectable(plugin)} isChecked={selected.includes(plugin.name)}
                      title={selectable(plugin) ? undefined : 'Only plugins with an update available can be synced'}
                      onChange={(_e, checked) => setSelected(checked ? [...selected, plugin.name] : selected.filter((n) => n !== plugin.name))}
                    />
                  </Td>
                ) : null}
                <Td dataLabel="Name">
                  <Button variant="link" isInline onClick={() => onOpenPlugin(plugin.name)}>{plugin.name}</Button>
                  <Content component="small"> {organizationName}/{plugin.name}</Content>
                </Td>
                <Td dataLabel="Source">
                  <Label isCompact>{sourceLabel(plugin.source)}</Label>
                  {plugin.source.repository ? <> <code>{plugin.source.repository}</code></> : null}
                </Td>
                <Td dataLabel="Newest mirrored">{plugin.newest_version ?? 'None available'}</Td>
                <Td dataLabel="Versions">{plugin.published_versions}</Td>
                <Td dataLabel="Updates">
                  {queuedNames.has(plugin.name) ? <Label isCompact color="blue">Sync queued</Label>
                    : plugin.update_available ? <Label isCompact color="purple">Update available · {plugin.update_check.latest}</Label>
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
  const [draft, setDraft] = useState(platforms.join(', '))
  useEffect(() => { setDraft(platforms.join(', ')) }, [platforms])
  const parsed = draft.split(',').map((platform) => platform.trim()).filter(Boolean)
  return (
    <>
      <Content component="p">
        Default platforms, used when a plugin is first imported. Later imports keep the platforms the plugin already has.
      </Content>
      <TextInput aria-label="Default platforms" value={draft} onChange={(_event, value) => setDraft(value)} />
      <Button variant="secondary" isDisabled={busy || parsed.length === 0 || parsed.join(', ') === platforms.join(', ')} onClick={() => onSave(parsed)}>
        Save default platforms
      </Button>
    </>
  )
}
