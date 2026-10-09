import { useCallback, useEffect, useState } from 'react'
import {
  ActionGroup, Alert, AlertActionCloseButton, Button, Card, CardBody, CardTitle, Checkbox, Content, EmptyState,
  EmptyStateActions, EmptyStateBody, EmptyStateFooter, Label, List, ListItem, Modal, ModalBody, ModalFooter,
  ModalHeader, PageSection, SearchInput, Spinner, TextInput, Toolbar, ToolbarContent, ToolbarItem,
} from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'
import { ScreenHeader } from '../components/ScreenHeader'
import { TypedConfirmModal } from '../components/TypedConfirmModal'
import {
  disablePluginRegistry, enablePluginRegistry, exposePluginRegistry, getDefaultPlatforms, getPluginRegistry,
  listPlugins, setDefaultPlatforms, sourceLabel, syncCatalogue, unexposePluginRegistry, type CatalogueSyncResult, type Plugin,
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
  const [defaultPlatforms, setDefaultPlatformsState] = useState<string[]>([])
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
      setDefaultPlatformsState(next.enabled ? await getDefaultPlatforms(token, organizationID) : [])
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
      onOpenPlugin={(name) => navigate(`/plugin-registry/${encodeURIComponent(name)}`)}
      onUpload={() => navigate('/plugin-registry/upload')}
      onBrowse={() => navigate('/plugin-registry/hashicorp')}
      onImportGithub={() => navigate('/plugin-registry/github')}
      onOpenImport={(id) => navigate(`/plugin-registry/imports/${id}`)}
      onSyncSelected={async (names) => {
        const results = await syncCatalogue(token, organizationID, names)
        await reload()
        return results
      }}
      defaultPlatforms={defaultPlatforms}
      onSetDefaultPlatforms={async (platforms) => {
        setDefaultPlatformsState(await setDefaultPlatforms(token, organizationID, platforms))
      }}
      loading={loading}
      failure={failure}
      onRefresh={reload}
      onEnable={async () => {
        const next = await enablePluginRegistry(token, organizationID)
        setRegistry(next)
      }}
      onExpose={async () => {
        const next = await exposePluginRegistry(token, organizationID)
        setRegistry(next)
      }}
      onUnexpose={async () => {
        const next = await unexposePluginRegistry(token, organizationID)
        setRegistry(next)
      }}
      onDisable={async () => {
        await disablePluginRegistry(token, organizationID)
        setRegistry({ enabled: false, exposed: false })
        setPlugins([])
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
  onOpenPlugin: (name: string) => void
  onUpload: () => void
  onBrowse: () => void
  onImportGithub: () => void
  onOpenImport: (id: string) => void
  onSyncSelected: (names: string[]) => Promise<CatalogueSyncResult[]>
  defaultPlatforms: string[]
  onSetDefaultPlatforms: (platforms: string[]) => Promise<void>
  loading: boolean
  failure: string | null
  onRefresh: () => void | Promise<void>
  onEnable: () => Promise<void>
  onExpose: () => Promise<void>
  onUnexpose: () => Promise<void>
  onDisable: () => Promise<void>
}

export function PluginRegistryView(props: PluginRegistryViewProps) {
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const canConfigure = permitsAction(props.callerRole, 'configurePluginRegistry')

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
        title="Plugins"
        description="Mirror Packer plugins through this organization's registry."
        onRefresh={props.onRefresh}
        refreshing={props.loading}
      />
      <PageSection variant="secondary" isFilled>
        {props.failure ? (
          <Alert variant="danger" isInline title="Plugin registry could not be loaded">
            <Content component="p">{props.failure}</Content>
          </Alert>
        ) : null}
        {actionFailure ? (
          <Alert variant="danger" isInline title="The action failed">
            <Content component="p">{actionFailure}</Content>
          </Alert>
        ) : null}
        {props.loading ? (
          <><Spinner aria-label="Loading plugin registry…" /><Content component="p">Loading plugin registry…</Content></>
        ) : props.failure || !props.registry ? null : !props.registry.enabled ? (
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
            canConfigure={canConfigure}
            busy={busy}
            run={run}
          />
        )}
      </PageSection>
    </>
  )
}

function EnabledRegistry({
  organizationName, callerRole, host, registry, plugins, onOpenPlugin, onUpload, onBrowse, onImportGithub,
  onOpenImport, onSyncSelected, defaultPlatforms, onSetDefaultPlatforms, canConfigure, busy, run, onExpose, onUnexpose, onDisable,
}: PluginRegistryViewProps & {
  canConfigure: boolean
  busy: boolean
  run: (work: () => Promise<void>) => Promise<void>
}) {
  const [confirming, setConfirming] = useState<'expose' | 'unexpose' | 'disable' | null>(null)
  const exposed = registry?.exposed ?? false
  const confirm = (work: () => Promise<void>) => {
    setConfirming(null)
    void run(work)
  }

  return (
    <>
      {exposed ? (
        <Alert variant="success" isInline title="The registry is exposed">
          <Content component="p">Plugins are anonymously readable to anyone who can reach dufflebag.</Content>
        </Alert>
      ) : (
        <Alert variant="info" isInline title="The registry is enabled but not exposed">
          <Content component="p">Packer can't reach plugins until the registry is exposed.</Content>
        </Alert>
      )}
      <PluginCatalogue
        organizationName={organizationName} host={host} plugins={plugins}
        canPublish={permitsAction(callerRole, 'publishPlugin')}
        onOpenPlugin={onOpenPlugin} onUpload={onUpload} onBrowse={onBrowse} onImportGithub={onImportGithub}
        onOpenImport={onOpenImport} onSyncSelected={onSyncSelected}
      />
      {canConfigure ? (
        <Card aria-label="Registry settings">
          <CardTitle>Registry settings</CardTitle>
          <CardBody>
            <ActionGroup>
              {exposed ? (
                <Button variant="secondary" isDisabled={busy} onClick={() => setConfirming('unexpose')}>
                  Unexpose
                </Button>
              ) : (
                <Button variant="primary" isDisabled={busy} onClick={() => setConfirming('expose')}>
                  Expose
                </Button>
              )}
              <Button
                variant="danger" isDisabled={busy || exposed}
                onClick={() => setConfirming('disable')}
              >Disable registry</Button>
            </ActionGroup>
            {exposed ? <Content component="p">Unexpose the registry first before disabling it.</Content> : null}
            <DefaultPlatformsEditor
              platforms={defaultPlatforms} busy={busy}
              onSave={(platforms) => void run(() => onSetDefaultPlatforms(platforms))}
            />
          </CardBody>
        </Card>
      ) : null}
      {confirming === 'expose' ? (
        <PluginRegistryConfirmation
          title="Expose the plugin registry?"
          body="Plugins become anonymously readable to anyone who can reach dufflebag."
          verb="Expose registry"
          busy={busy}
          onCancel={() => setConfirming(null)}
          onConfirm={() => confirm(onExpose)}
        />
      ) : null}
      {confirming === 'unexpose' ? (
        <PluginRegistryConfirmation
          title="Unexpose the plugin registry?"
          body="In-flight packer init fails when the registry is unexposed."
          verb="Unexpose registry"
          busy={busy}
          onCancel={() => setConfirming(null)}
          onConfirm={() => confirm(onUnexpose)}
        />
      ) : null}
      {confirming === 'disable' ? (
        <DisablePluginRegistryConfirmation
          organizationName={organizationName}
          busy={busy}
          onCancel={() => setConfirming(null)}
          onConfirm={() => confirm(onDisable)}
        />
      ) : null}
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

export function PluginCatalogue({
  organizationName, host, plugins, canPublish, onOpenPlugin, onUpload, onBrowse, onImportGithub, onOpenImport, onSyncSelected,
}: {
  organizationName: string
  host: string
  plugins: Plugin[]
  canPublish: boolean
  onOpenPlugin: (name: string) => void
  onUpload: () => void
  onBrowse: () => void
  onImportGithub: () => void
  onOpenImport: (id: string) => void
  onSyncSelected: (names: string[]) => Promise<CatalogueSyncResult[]>
}) {
  const [filter, setFilter] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [results, setResults] = useState<CatalogueSyncResult[] | null>(null)
  const [syncing, setSyncing] = useState(false)
  const [syncFailure, setSyncFailure] = useState<string | null>(null)
  const sync = async () => {
    setSyncing(true)
    setSyncFailure(null)
    try {
      setResults(await onSyncSelected(selected))
      setSelected([])
    } catch (error: unknown) {
      setSyncFailure(pluginRegistryErrorMessage(error, 'The sync could not be queued.'))
    } finally {
      setSyncing(false)
    }
  }
  const upload = canPublish ? (
    <>
      <Button variant="primary" onClick={onBrowse}>Browse HashiCorp</Button>{' '}
      <Button variant="secondary" onClick={onImportGithub}>Import from GitHub</Button>{' '}
      <Button variant="secondary" onClick={onUpload}>Upload plugin files</Button>
    </>
  ) : null
  if (plugins.length === 0) {
    return (
      <EmptyState titleText="No plugins mirrored yet" headingLevel="h2">
        <EmptyStateBody>
          Mirror a Packer plugin here and packer init resolves it from dufflebag instead of the internet.
          Each plugin comes from one source.
        </EmptyStateBody>
        {upload ? <EmptyStateFooter><EmptyStateActions>{upload}</EmptyStateActions></EmptyStateFooter> : null}
      </EmptyState>
    )
  }
  const shown = plugins.filter((plugin) => plugin.name.includes(filter.trim().toLowerCase()))
  return (
    <>
      <Content component="p">
        Packer plugins mirrored for {organizationName}. packer init resolves them from {host}/plugins/{organizationName}.
      </Content>
      <Toolbar>
        <ToolbarContent>
          <ToolbarItem>
            <SearchInput
              aria-label="Filter plugins by name" placeholder="Filter by name" value={filter}
              onChange={(_event, value) => setFilter(value)} onClear={() => setFilter('')}
            />
          </ToolbarItem>
          {upload ? <ToolbarItem>{upload}</ToolbarItem> : null}
          {canPublish ? (
            <ToolbarItem>
              <Button variant="secondary" isLoading={syncing} isDisabled={syncing || selected.length === 0} onClick={() => void sync()}>
                Sync selected ({selected.length})
              </Button>
            </ToolbarItem>
          ) : null}
        </ToolbarContent>
      </Toolbar>
      {syncFailure ? <Alert variant="danger" isInline title="The sync could not be queued"><Content component="p">{syncFailure}</Content></Alert> : null}
      {results ? (
        <Alert variant="info" isInline title="Sync queued" actionClose={<AlertActionCloseButton onClose={() => setResults(null)} />}>
          <List aria-label="Sync results">
            {results.map((result) => (
              <ListItem key={result.plugin}>
                {result.import_id ? (
                  <>{result.plugin} {result.version}: <Button variant="link" isInline onClick={() => onOpenImport(result.import_id ?? '')}>view import</Button></>
                ) : <>{result.plugin}: {result.refused}</>}
              </ListItem>
            ))}
          </List>
        </Alert>
      ) : null}
      <Table aria-label="Plugins" variant="compact">
        <Thead>
          <Tr>
            {canPublish ? <Th screenReaderText="Select" /> : null}
            <Th>Name</Th><Th>Source</Th><Th>Newest mirrored</Th><Th>Versions</Th>
          </Tr>
        </Thead>
        <Tbody>
          {shown.map((plugin) => (
            <Tr key={plugin.name}>
              {canPublish ? (
                <Td dataLabel="Select">
                  <Checkbox
                    id={`select-${plugin.name}`} aria-label={`Select ${plugin.name} to sync`}
                    isDisabled={!plugin.update_available} isChecked={selected.includes(plugin.name)}
                    onChange={(_e, checked) => setSelected(checked ? [...selected, plugin.name] : selected.filter((n) => n !== plugin.name))}
                  />
                </Td>
              ) : null}
              <Td dataLabel="Name">
                <Button variant="link" isInline onClick={() => onOpenPlugin(plugin.name)}>{plugin.name}</Button>
                <Content component="small"> {organizationName}/{plugin.name}</Content>
              </Td>
              <Td dataLabel="Source"><Label isCompact>{sourceLabel(plugin.source)}</Label></Td>
              <Td dataLabel="Newest mirrored">
                {plugin.newest_version ?? 'None available'}
                {plugin.update_available ? <> <Label isCompact color="blue">Update available · {plugin.update_check.latest}</Label></> : null}
              </Td>
              <Td dataLabel="Versions">{plugin.published_versions}</Td>
            </Tr>
          ))}
        </Tbody>
      </Table>
      {shown.length === 0 ? <Content component="p">No plugins match “{filter}”.</Content> : null}
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
