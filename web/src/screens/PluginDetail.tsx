import { useCallback, useEffect, useState } from 'react'
import {
  Alert, Button, Checkbox, ClipboardCopyButton, CodeBlock, CodeBlockAction, CodeBlockCode, Content, Label,
  List, ListItem, PageSection, Spinner, Switch, Title,
} from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate, useParams } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  deletePluginVersion, getPluginRegistry, listHashicorpPluginVersions, listPluginVersions, listPlugins, pluginChanges,
  setPluginUpdateCheck, sourceLabel, syncPlugin, templateStanza, type HashicorpPluginVersion, type Plugin, type PluginChange,
  type PluginEdit, type PluginRegistry, type PluginVersions,
} from '../data/pluginRegistry'
import { useTenant } from '../data/tenant'
import { PluginRegistryConfirmation, pluginRegistryErrorMessage } from './Plugins'

export function PluginDetail() {
  const { name = '' } = useParams()
  const { state, self, selectedOrganization, signOut } = useAuth()
  const { tenant } = useTenant()
  const navigate = useNavigate()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? null
  const token = state?.token ?? ''
  const [registry, setRegistry] = useState<PluginRegistry | null>(null)
  const [detail, setDetail] = useState<PluginVersions | null>(null)
  const [summary, setSummary] = useState<Plugin | null>(null)
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)
  const [upstream, setUpstream] = useState<HashicorpPluginVersion[]>([])

  const reload = useCallback(async () => {
    if (!organizationID || token === '') return
    setLoading(true)
    try {
      const [nextRegistry, nextDetail, plugins] = await Promise.all([
        getPluginRegistry(token, organizationID), listPluginVersions(token, organizationID, name), listPlugins(token, organizationID),
      ])
      setRegistry(nextRegistry)
      setDetail(nextDetail)
      setSummary(plugins.find((plugin) => plugin.name === name) ?? null)
      setFailure(null)
    } catch (error: unknown) {
      if (signOutIfUnauthorized(error, signOut)) return
      setFailure(pluginRegistryErrorMessage(error, 'The plugin could not be loaded.'))
    } finally {
      setLoading(false)
    }
  }, [name, organizationID, signOut, token])

  const act = <T,>(work: (argument: T) => Promise<void>, removesPlugin = false) => async (argument: T) => {
    setBusy(true)
    setActionFailure(null)
    try {
      await work(argument)
      if (removesPlugin) {
        navigate('/plugin-registry')
        return
      }
      await reload()
    } catch (error: unknown) {
      if (signOutIfUnauthorized(error, signOut)) return
      setActionFailure(pluginRegistryErrorMessage(error, 'The action failed.'))
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => { void reload() }, [reload])

  return (
    <PluginDetailView
      name={name} organizationName={tenant.organization} host={window.location.hostname}
      callerRole={self?.role ?? null} registry={registry} detail={detail}
      loading={loading} failure={failure} onRefresh={reload}
      onUpload={() => navigate('/plugin-registry/upload')}
      busy={busy} actionFailure={actionFailure}
      editing={editing} upstream={upstream} summary={summary}
      onToggleUpdates={act((enabled: boolean) => setPluginUpdateCheck(token, organizationID ?? '', name, enabled))}
      onEdit={() => void (async () => {
        setActionFailure(null)
        setUpstream([])
        setEditing(true)
        if (detail?.source.kind !== 'releases-hashicorp' || !detail.source.repository) return
        try {
          const page = await listHashicorpPluginVersions(token, organizationID ?? '', detail.source.repository)
          setUpstream(page.versions.filter((v) => !v.prerelease))
        } catch (error: unknown) {
          if (!signOutIfUnauthorized(error, signOut)) {
            setActionFailure(pluginRegistryErrorMessage(error, 'releases.hashicorp.com could not be reached, so only mirrored versions can be changed.'))
          }
        }
      })()}
      onCancelEdit={() => setEditing(false)}
      onSync={(changes) => void (async () => {
        setBusy(true)
        setActionFailure(null)
        try {
          const job = await syncPlugin(token, organizationID ?? '', name, changes)
          navigate(`/plugin-registry/imports/${job.id}`)
        } catch (error: unknown) {
          if (!signOutIfUnauthorized(error, signOut)) setActionFailure(pluginRegistryErrorMessage(error, 'The sync could not be queued.'))
        } finally {
          setBusy(false)
        }
      })()}
      onRemove={act(
        (version: string) => deletePluginVersion(token, organizationID ?? '', name, version),
        detail?.versions.length === 1,
      )}
    />
  )
}

export function PluginDetailView({
  name, organizationName, host, callerRole, registry, detail, loading, failure, onRefresh, onUpload,
  busy, actionFailure, editing, upstream, summary, onToggleUpdates, onEdit, onCancelEdit, onSync, onRemove,
}: {
  name: string
  organizationName: string
  host: string
  callerRole: Role | null
  registry: PluginRegistry | null
  detail: PluginVersions | null
  loading: boolean
  failure: string | null
  onRefresh: () => void | Promise<void>
  onUpload: () => void
  busy: boolean
  actionFailure: string | null
  editing: boolean
  upstream: HashicorpPluginVersion[]
  summary: Plugin | null
  onToggleUpdates: (enabled: boolean) => void
  onEdit: () => void
  onCancelEdit: () => void
  onSync: (changes: PluginChange[]) => void
  onRemove: (version: string) => void
}) {
  const [removing, setRemoving] = useState<string | null>(null)
  const [edit, setEdit] = useState<PluginEdit>({ selected: {}, added: {} })
  useEffect(() => { if (!editing) setEdit({ selected: {}, added: {} }) }, [editing])
  const canPublish = permitsAction(callerRole, 'publishPlugin')
  const lastVersion = detail?.versions.length === 1
  const newest = detail?.versions.find((version) => !version.revoked)
  const key = (p: { os: string; arch: string }) => `${p.os}_${p.arch}`
  const mirrored = new Set(detail?.versions.map((v) => v.version) ?? [])
  const unmirrored = editing ? upstream.filter((v) => !mirrored.has(v.version)) : []
  const platforms = [...new Set([
    ...(detail?.versions.flatMap((version) => [...version.listed_platforms, ...version.stored_platforms].map(key)) ?? []),
    ...unmirrored.flatMap((v) => v.platforms),
  ])].sort()
  // After a plugin's first import, its existing platforms are the default (ADR-0027 A8).
  const defaults = [...new Set(detail?.versions.flatMap((v) => v.stored_platforms.map(key)) ?? [])]
  const changes = detail && editing ? pluginChanges(detail.versions, unmirrored, edit) : []
  const imported = detail?.source.kind !== 'upload'
  const toggleRow = (version: string, selected: boolean, available: string[] = []) => setEdit((current) => ({
    selected: { ...current.selected, [version]: selected },
    added: !mirrored.has(version) && current.added[version] === undefined
      ? { ...current.added, [version]: defaults.filter((p) => available.includes(p)) } : current.added,
  }))
  const toggleCell = (version: string, platform: string) => setEdit((current) => {
    const added = current.added[version] ?? []
    return { ...current, added: { ...current.added, [version]: added.includes(platform) ? added.filter((p) => p !== platform) : [...added, platform] } }
  })
  return (
    <>
      <ScreenHeader
        title={name}
        description={detail ? (
          <>
            <Label isCompact>{sourceLabel(detail.source)}</Label>{' '}
            {summary?.update_available ? <Label isCompact color="blue">Update available · {summary.update_check.latest}</Label> : null}
          </>
        ) : undefined}
        actions={canPublish && detail && !editing ? (
          <>
            {detail.source.kind === 'upload' ? <><Button variant="secondary" onClick={onUpload}>Upload version</Button>{' '}</> : null}
            <Button variant="secondary" onClick={onEdit}>Edit versions</Button>
          </>
        ) : undefined}
        onRefresh={onRefresh} refreshing={loading}
      />
      <PageSection variant="secondary" isFilled>
        {actionFailure ? <Alert variant="danger" isInline title="The action failed"><Content component="p">{actionFailure}</Content></Alert> : null}
        {failure ? <Alert variant="danger" isInline title="The plugin could not be loaded"><Content component="p">{failure}</Content></Alert> : null}
        {loading && !detail ? <><Spinner aria-label="Loading plugin…" /><Content component="p">Loading plugin…</Content></> : null}
        {detail && summary && imported ? <UpdateCheck summary={summary} canPublish={canPublish} busy={busy} onToggle={onToggleUpdates} /> : null}
        {detail ? (
          <>
            {newest ? (
              <>
                <Title headingLevel="h2" size="md">Template stanza</Title>
                <Content component="small">Pinned to the newest available version, {newest.version}.</Content>
                <NotExposedAlert registry={registry} />
                <TemplateStanzaBlock hcl={templateStanza(host, organizationName, name, newest.version)} />
              </>
            ) : null}
            <Title headingLevel="h2" size="md">Versions</Title>
            <Content component="small">
              ● Mirrored · ○ Listed in SHA256SUMS, not {imported ? 'mirrored' : 'uploaded'}, so Packer on that platform gets an older
              version or none · – Not published
            </Content>
            {editing ? (
              <Content component="small">
                Untick a version to revoke it; tick a revoked one to restore it.
                {imported ? ' Tick an unmirrored platform to add it. Mirrored platforms cannot be removed.' : ''}
                {unmirrored.length ? ' Tick a version not yet mirrored to add it with the platforms this plugin already has.' : ''}
              </Content>
            ) : null}
            <Table aria-label="Versions by platform" variant="compact">
              <Thead>
                <Tr>
                  {editing ? <Th screenReaderText="Select" /> : null}
                  <Th>Version</Th>{platforms.map((platform) => <Th key={platform}>{platform}</Th>)}
                  {canPublish && !editing ? <Th screenReaderText="Actions" /> : null}
                </Tr>
              </Thead>
              <Tbody>
                {unmirrored.map((version) => {
                  const selected = edit.selected[version.version] ?? false
                  const added = edit.added[version.version] ?? []
                  return (
                    <Tr key={version.version}>
                      <Td dataLabel="Select">
                        <Checkbox id={`sync-${version.version}`} aria-label={`Add ${version.version}`} isChecked={selected}
                          onChange={(_e, checked) => toggleRow(version.version, checked, version.platforms)} />
                      </Td>
                      <Td dataLabel="Version">{version.version} <Label isCompact color="blue">Not mirrored</Label></Td>
                      {platforms.map((platform) => (
                        <Td key={platform} dataLabel={platform}>
                          {version.platforms.includes(platform) ? (
                            <Checkbox id={`sync-${version.version}-${platform}`} aria-label={`Add ${platform} to ${version.version}`}
                              isDisabled={!selected} isChecked={selected && added.includes(platform)}
                              onChange={() => toggleCell(version.version, platform)} />
                          ) : '–'}
                        </Td>
                      ))}
                    </Tr>
                  )
                })}
                {detail.versions.map((version) => {
                  const stored = new Set(version.stored_platforms.map(key))
                  const listed = new Set(version.listed_platforms.map(key))
                  const selected = edit.selected[version.version] ?? !version.revoked
                  const added = edit.added[version.version] ?? []
                  const cellsEditable = editing && imported && !version.revoked && selected
                  return (
                    <Tr key={version.version}>
                      {editing ? (
                        <Td dataLabel="Select">
                          <Checkbox id={`sync-${version.version}`} aria-label={`Serve ${version.version}`} isChecked={selected}
                            onChange={(_e, checked) => toggleRow(version.version, checked)} />
                        </Td>
                      ) : null}
                      <Td dataLabel="Version">
                        {version.version} {version.revoked ? <Label isCompact color="grey">Revoked</Label> : null}
                      </Td>
                      {platforms.map((platform) => (
                        <Td key={platform} dataLabel={platform}>
                          {stored.has(platform) ? '●' : listed.has(platform) && cellsEditable ? (
                            <Checkbox id={`sync-${version.version}-${platform}`} aria-label={`Add ${platform} to ${version.version}`}
                              isChecked={added.includes(platform)} onChange={() => toggleCell(version.version, platform)} />
                          ) : listed.has(platform) ? '○' : '–'}
                        </Td>
                      ))}
                      {canPublish && !editing ? (
                        <Td dataLabel="Actions" isActionCell>
                          <Button variant="link" isDanger isInline isDisabled={busy} onClick={() => setRemoving(version.version)}>
                            Remove version
                          </Button>
                        </Td>
                      ) : null}
                    </Tr>
                  )
                })}
              </Tbody>
            </Table>
            {editing ? (
              <>
                <Title headingLevel="h3" size="md">Pending changes</Title>
                {changes.length ? (
                  <List aria-label="Pending changes">
                    {changes.map((change) => (
                      <ListItem key={change.version}>
                        {change.action === 'revoke' ? <><Label isCompact color="orange">Revoke</Label> {change.version}: Packer stops installing it; its files are kept.</>
                          : change.action === 'restore' ? <><Label isCompact color="green">Restore</Label> {change.version}</>
                          : mirrored.has(change.version) ? <><Label isCompact color="blue">Add</Label> {change.platforms?.join(', ')} to {change.version}</>
                          : <><Label isCompact color="blue">Mirror</Label> {change.version} · {change.platforms?.join(', ')}</>}
                      </ListItem>
                    ))}
                  </List>
                ) : <Content component="p">No changes yet.</Content>}
                <Button variant="primary" isLoading={busy} isDisabled={busy || changes.length === 0} onClick={() => onSync(changes)}>
                  Sync {changes.length} {changes.length === 1 ? 'change' : 'changes'}
                </Button>{' '}
                <Button variant="link" isDisabled={busy} onClick={onCancelEdit}>Cancel</Button>
              </>
            ) : null}
          </>
        ) : null}
        {removing ? (
          <PluginRegistryConfirmation
            title={`Remove ${name} ${removing}?`}
            body={`Its files are deleted and Packer can no longer install it.${lastVersion
              ? ` It is ${name}'s last version, so ${name} is removed and its name can be used by another source.` : ''}`}
            verb="Remove version" busy={busy}
            onCancel={() => setRemoving(null)}
            onConfirm={() => { const version = removing; setRemoving(null); onRemove(version) }}
          />
        ) : null}
      </PageSection>
    </>
  )
}

function UpdateCheck({ summary, canPublish, busy, onToggle }: {
  summary: Plugin
  canPublish: boolean
  busy: boolean
  onToggle: (enabled: boolean) => void
}) {
  const check = summary.update_check
  return (
    <>
      {canPublish ? (
        <Switch
          id="update-check" label="Check for updates" isChecked={check.enabled} isDisabled={busy}
          onChange={(_event, enabled) => onToggle(enabled)}
        />
      ) : <Content component="p">Update checks are {check.enabled ? 'on' : 'off'}.</Content>}
      {check.enabled ? (
        <Content component="small">
          {check.checked_at ? `Last checked ${new Date(check.checked_at).toLocaleString()}` : 'Not checked yet'}
          {check.latest ? `; newest stable release seen: ${check.latest}` : ''}.
          {check.error ? ` The last check failed: ${check.error}` : ''}
          {' '}A check only looks; nothing is imported until you sync.
        </Content>
      ) : null}
    </>
  )
}

export function NotExposedAlert({ registry }: { registry: PluginRegistry | null }) {
  if (!registry || registry.exposed) return null
  return (
    <Alert variant="info" isInline title="The registry is enabled but not exposed">
      <Content component="p">Packer can't resolve this template stanza until the registry is exposed.</Content>
    </Alert>
  )
}

export function TemplateStanzaBlock({ hcl, id = 'template-stanza' }: { hcl: string; id?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <CodeBlock actions={
      <CodeBlockAction>
        <ClipboardCopyButton
          id={`${id}-copy`} textId={id} aria-label="Copy template stanza"
          exitDelay={copied ? 1500 : 600} variant="plain"
          onClick={() => { void navigator.clipboard.writeText(hcl); setCopied(true) }}
          onTooltipHidden={() => setCopied(false)}
        >{copied ? 'Copied' : 'Copy'}</ClipboardCopyButton>
      </CodeBlockAction>
    }>
      <CodeBlockCode id={id}>{hcl}</CodeBlockCode>
    </CodeBlock>
  )
}
