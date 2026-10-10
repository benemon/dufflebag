import { useCallback, useEffect, useState } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Checkbox, ClipboardCopyButton, CodeBlock,
  CodeBlockAction, CodeBlockCode, Content, DescriptionList, DescriptionListDescription, DescriptionListGroup,
  DescriptionListTerm, Flex, FlexItem, Label, PageSection, Switch,
} from '@patternfly/react-core'
import { ActionsColumn, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate, useParams } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  PLUGIN_ARCHITECTURES, PLUGIN_OS_GROUPS, deletePluginVersion, getPluginRegistry, listHashicorpPluginVersions,
  listPluginVersions, listPlugins, pendingItems, pluginChanges, setPluginUpdateCheck, sourceLabel, syncPlugin,
  templateStanza, type HashicorpPluginVersion, type Plugin, type PluginChange, type PluginEdit, type PluginRegistry,
  type PluginVersions,
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
  const [upstream, setUpstream] = useState<HashicorpPluginVersion[]>([])
  const [olderUpstream, setOlderUpstream] = useState<string | undefined>()
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)

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
      if (nextDetail.source.kind === 'releases-hashicorp' && nextDetail.source.repository) {
        const page = await listHashicorpPluginVersions(token, organizationID, nextDetail.source.repository)
        setUpstream(page.versions)
        setOlderUpstream(page.next)
      }
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
      callerRole={self?.role ?? null} registry={registry} detail={detail} summary={summary}
      upstream={upstream} hasOlderUpstream={olderUpstream !== undefined}
      loading={loading} failure={failure} onRefresh={reload}
      onBackToRegistry={() => navigate('/buckets')}
      onBackToPlugins={() => navigate('/plugin-registry')}
      onUpload={() => navigate('/plugin-registry/upload')}
      busy={busy} actionFailure={actionFailure} editing={editing}
      onToggleUpdates={act((enabled: boolean) => setPluginUpdateCheck(token, organizationID ?? '', name, enabled))}
      onEdit={() => { setActionFailure(null); setEditing(true) }}
      onDiscard={() => setEditing(false)}
      onOlderUpstream={() => void (async () => {
        if (!detail?.source.repository || !olderUpstream) return
        try {
          const page = await listHashicorpPluginVersions(token, organizationID ?? '', detail.source.repository, olderUpstream)
          setUpstream((current) => [...current, ...page.versions])
          setOlderUpstream(page.next)
        } catch (error: unknown) {
          if (!signOutIfUnauthorized(error, signOut)) setActionFailure(pluginRegistryErrorMessage(error, 'Older releases could not be loaded.'))
        }
      })()}
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

type GridRow = {
  version: string
  state: 'available' | 'revoked' | 'none'
  prerelease: boolean
  stored: Set<string>
  published: Set<string>
}

const platformKey = (p: { os: string; arch: string }) => `${p.os}_${p.arch}`

export function PluginDetailView({
  name, organizationName, host, callerRole, registry, detail, summary, upstream, hasOlderUpstream, loading, failure,
  onRefresh, onBackToRegistry, onBackToPlugins, onUpload, busy, actionFailure, editing, onToggleUpdates, onEdit,
  onDiscard, onOlderUpstream, onSync, onRemove,
}: {
  name: string
  organizationName: string
  host: string
  callerRole: Role | null
  registry: PluginRegistry | null
  detail: PluginVersions | null
  summary: Plugin | null
  upstream: HashicorpPluginVersion[]
  hasOlderUpstream: boolean
  loading: boolean
  failure: string | null
  onRefresh: () => void | Promise<void>
  onBackToRegistry: () => void
  onBackToPlugins: () => void
  onUpload: () => void
  busy: boolean
  actionFailure: string | null
  editing: boolean
  onToggleUpdates: (enabled: boolean) => void
  onEdit: () => void
  onDiscard: () => void
  onOlderUpstream: () => void
  onSync: (changes: PluginChange[]) => void
  onRemove: (version: string) => void
}) {
  const [removing, setRemoving] = useState<string | null>(null)
  const [edit, setEdit] = useState<PluginEdit>({ selected: {}, added: {} })
  useEffect(() => { if (!editing) setEdit({ selected: {}, added: {} }) }, [editing])
  const canPublish = permitsAction(callerRole, 'publishPlugin')
  const local = detail?.source.kind === 'upload'
  const newest = detail?.versions.find((version) => !version.revoked)
  const mirrored = new Set(detail?.versions.map((v) => v.version) ?? [])
  const unmirrored = local ? [] : upstream.filter((v) => !mirrored.has(v.version))
  // After a plugin's first import, its existing platforms are the default (ADR-0027 A8).
  const existing = [...new Set(detail?.versions.flatMap((v) => v.stored_platforms.map(platformKey)) ?? [])]
  const rows: GridRow[] = [
    ...unmirrored.map((v) => ({ version: v.version, state: 'none' as const, prerelease: v.prerelease, stored: new Set<string>(), published: new Set(v.platforms) })),
    ...(detail?.versions ?? []).map((v) => ({
      version: v.version, state: v.revoked ? 'revoked' as const : 'available' as const, prerelease: false,
      stored: new Set(v.stored_platforms.map(platformKey)),
      published: new Set([...v.listed_platforms, ...v.stored_platforms].map(platformKey)),
    })),
  ]
  const changes = detail && editing ? pluginChanges(detail.versions, unmirrored, edit) : []
  const pending = pendingItems(changes, mirrored)
  const checked = (row: GridRow) => edit.selected[row.version] ?? (row.state === 'available')
  const adds = (row: GridRow) => (edit.added[row.version] ?? []).filter((p) => !row.stored.has(p))
  const toggleRow = (row: GridRow) => setEdit((current) => {
    const now = !checked(row)
    const selected = { ...current.selected, [row.version]: now }
    let added = { ...current.added }
    if (row.state === 'none') added[row.version] = now ? existing.filter((p) => row.published.has(p)) : []
    if (row.state === 'available' && !now) added = { ...added, [row.version]: [] }
    return { selected, added }
  })
  const toggleCell = (row: GridRow, platform: string) => setEdit((current) => {
    const have = current.added[row.version] ?? []
    const next = have.includes(platform) ? have.filter((p) => p !== platform) : [...have, platform]
    const selected = row.state === 'none' ? { ...current.selected, [row.version]: next.length > 0 } : current.selected
    return { selected, added: { ...current.added, [row.version]: next } }
  })
  const undo = (version: string) => setEdit((current) => {
    const selected = { ...current.selected }
    const added = { ...current.added }
    delete selected[version]
    delete added[version]
    return { selected, added }
  })
  const marker = (row: GridRow) => {
    const on = checked(row)
    if (row.state === 'available' && !on) return '→ revoke'
    if (row.state === 'revoked' && on) return '→ restore'
    if (row.state === 'none' && on && adds(row).length) return '→ add'
    if (row.state === 'available' && adds(row).length) return `+${adds(row).length}`
    return ''
  }
  const removingRow = rows.find((row) => row.version === removing)
  const lastVersion = detail?.versions.length === 1
  const check = summary?.update_check

  return (
    <>
      <ScreenHeader
        breadcrumbs={(
          <Breadcrumb>
            <BreadcrumbItem component="button" onClick={onBackToRegistry}>Registry</BreadcrumbItem>
            <BreadcrumbItem component="button" onClick={onBackToPlugins}>Plugins</BreadcrumbItem>
            <BreadcrumbItem isActive>{name}</BreadcrumbItem>
          </Breadcrumb>
        )}
        title={detail ? (
          <>
            {name}{' '}
            <Label isCompact>{sourceLabel(detail.source)}</Label>{' '}
            {summary?.update_available ? <><Label isCompact color="purple">Update available · {check?.latest}</Label>{' '}</> : null}
            {check?.enabled && check.error ? <Label isCompact color="red">Last check failed</Label> : null}
          </>
        ) : name}
        description={detail ? sourceLine(detail) : undefined}
        onRefresh={onRefresh} refreshing={loading}
      />
      <PageSection variant="secondary" isFilled>
        {detail && !registry?.exposed && registry ? <NotExposedAlert registry={registry} /> : null}
        {actionFailure ? <Alert variant="danger" isInline title="The action failed"><Content component="p">{actionFailure}</Content></Alert> : null}
        {loading && !detail ? <PluginLoadingCard message={`Loading ${name}…`} /> : null}
        {failure ? <PluginErrorCard title={`${name} could not be loaded`} error={failure} onRetry={onRefresh} /> : null}
        {detail ? (
          <>
            <Flex gap={{ default: 'gapLg' }} alignItems={{ default: 'alignItemsStretch' }}>
              <FlexItem flex={{ default: 'flex_1' }}>
                <Card isFullHeight>
                  <CardTitle>Update check</CardTitle>
                  <CardBody>
                    {local ? (
                      <>
                        <Content component="p">Not available for uploaded plugins. To add a version, upload its files.</Content>
                        {canPublish ? <Button variant="secondary" onClick={onUpload}>Upload version</Button> : null}
                      </>
                    ) : (
                      <>
                        <Switch
                          id="update-check" label="Check daily for a newer stable version" isChecked={check?.enabled ?? false}
                          isDisabled={busy || !canPublish} onChange={(_event, enabled) => onToggleUpdates(enabled)}
                        />
                        <DescriptionList isCompact isHorizontal>
                          <DescriptionListGroup>
                            <DescriptionListTerm>Last checked</DescriptionListTerm>
                            <DescriptionListDescription>{checkedAt(check)}</DescriptionListDescription>
                          </DescriptionListGroup>
                          <DescriptionListGroup>
                            <DescriptionListTerm>Last error</DescriptionListTerm>
                            <DescriptionListDescription>
                              {check?.error ? <span style={{ color: 'var(--pf-t--global--color--status--danger--default)' }}>{check.error} The update pill reflects the last successful check.</span> : 'None'}
                            </DescriptionListDescription>
                          </DescriptionListGroup>
                        </DescriptionList>
                        <Content component="small">A check only flags a newer version. Nothing is mirrored until you sync.</Content>
                      </>
                    )}
                  </CardBody>
                </Card>
              </FlexItem>
              <FlexItem flex={{ default: 'flex_1' }}>
                <Card isFullHeight>
                  <CardTitle>Template stanza <Content component="small">newest available version</Content></CardTitle>
                  <CardBody>
                    {newest ? <TemplateStanzaBlock hcl={templateStanza(host, organizationName, name, newest.version)} />
                      : <Content component="p">No version is available: every version is revoked.</Content>}
                  </CardBody>
                </Card>
              </FlexItem>
            </Flex>
            <Card>
              <CardTitle>
                <Flex alignItems={{ default: 'alignItemsCenter' }} gap={{ default: 'gapMd' }}>
                  <FlexItem>Versions</FlexItem>
                  <FlexItem>
                    <Content component="small">
                      ● Mirrored · <span style={{ color: 'var(--pf-t--global--color--nonstatus--gray--default)' }}>●</span> Mirrored, revoked
                      {local ? '' : ' · ○ Upstream, not mirrored'} · – Not published
                    </Content>
                  </FlexItem>
                  <FlexItem align={{ default: 'alignRight' }}>
                    {editing ? (
                      <Content component="small">Editing: tick a version to add or restore it, untick to revoke · tick a box to add an architecture</Content>
                    ) : canPublish ? <Button variant="secondary" onClick={onEdit}>Edit versions</Button> : null}
                  </FlexItem>
                </Flex>
              </CardTitle>
              <CardBody>
                <div style={{ overflowX: 'auto' }}>
                <Table aria-label="Versions by architecture" variant="compact">
                  <Thead>
                    <Tr>
                      <Th />
                      {PLUGIN_OS_GROUPS.map(([os, arches]) => <Th key={os} colSpan={arches.length} hasRightBorder>{os}</Th>)}
                      {canPublish ? <Th /> : null}
                    </Tr>
                    <Tr>
                      <Th>Version</Th>
                      {PLUGIN_ARCHITECTURES.map((platform) => <Th key={platform} textCenter>{platform.split('_')[1]}</Th>)}
                      {canPublish ? <Th screenReaderText="Actions" /> : null}
                    </Tr>
                  </Thead>
                  <Tbody>
                    {rows.map((row) => {
                      const on = checked(row)
                      const revoking = row.state === 'available' && !on
                      const restoring = row.state === 'revoked' && on
                      const mark = marker(row)
                      return (
                        <Tr key={row.version} isRowSelected={editing && mark !== ''}>
                          <Td dataLabel="Version">
                            {editing ? (
                              <Checkbox
                                id={`serve-${row.version}`} aria-label={row.state === 'none' ? `Add ${row.version}` : `Serve ${row.version}`}
                                isChecked={on} onChange={() => toggleRow(row)} style={{ marginRight: 8 }}
                              />
                            ) : null}
                            <code>{row.version}</code>{' '}
                            {row.state === 'available' ? <Label isCompact color="green">Available</Label>
                              : row.state === 'revoked' ? <Label isCompact color="grey">Revoked</Label>
                              : <Label isCompact variant="outline">{row.prerelease ? 'Not mirrored · prerelease' : 'Not mirrored'}</Label>}
                            {mark ? <> <Content component="small">{mark}</Content></> : null}
                          </Td>
                          {PLUGIN_ARCHITECTURES.map((platform) => {
                            const published = row.published.has(platform)
                            const stored = row.stored.has(platform)
                            const added = adds(row).includes(platform)
                            const editable = editing && !local && (row.state === 'none' || (row.state === 'available' && on))
                            let cell
                            if (!published) cell = <span title="Not published upstream">–</span>
                            else if (stored) {
                              const grey = (row.state === 'revoked' && !restoring) || revoking
                              cell = <span title={editing ? 'Mirrored. To drop a bad binary, revoke or remove the version.' : 'Mirrored'} style={{ color: grey ? 'var(--pf-t--global--color--nonstatus--gray--default)' : 'var(--pf-t--global--color--brand--default)' }}>●</span>
                            } else if (editable) {
                              cell = <Checkbox id={`add-${row.version}-${platform}`} aria-label={`Add ${platform} to ${row.version}`} isChecked={added} title={added ? 'Will be added' : 'Add'} onChange={() => toggleCell(row, platform)} />
                            } else cell = <span title={editing && row.state === 'revoked' ? 'Restore the version to add architectures' : 'Upstream, not mirrored'}>○</span>
                            return <Td key={platform} dataLabel={platform} textCenter>{cell}</Td>
                          })}
                          {canPublish ? (
                            <Td isActionCell>
                              {row.state !== 'none' ? <ActionsColumn items={[{ title: 'Remove version', onClick: () => setRemoving(row.version) }]} /> : null}
                            </Td>
                          ) : null}
                        </Tr>
                      )
                    })}
                  </Tbody>
                </Table>
                </div>
                {hasOlderUpstream ? <Button variant="link" isInline onClick={onOlderUpstream}>Show older upstream versions</Button> : null}
                {editing ? (
                  <div style={{ marginTop: 16 }}>
                    <Content component="h3">Pending changes <span style={{ fontWeight: 'normal', color: 'var(--pf-t--global--text--color--subtle)' }}>· {pending.length}</span></Content>
                    {pending.length === 0 ? <Content component="p">None yet. Nothing changes until you sync.</Content> : null}
                    {pending.map((item) => (
                      <Flex key={`${item.verb}-${item.version}`} gap={{ default: 'gapSm' }} alignItems={{ default: 'alignItemsCenter' }}>
                        <Label isCompact color={item.verb === 'Add' ? 'blue' : item.verb === 'Revoke' ? 'yellow' : 'green'}>{item.verb}</Label>
                        <code>{item.what}</code>
                        <Content component="small">{item.detail}</Content>
                        <Button variant="link" isInline onClick={() => undo(item.version)}>Undo</Button>
                      </Flex>
                    ))}
                    <Flex gap={{ default: 'gapSm' }} alignItems={{ default: 'alignItemsCenter' }} style={{ marginTop: 12 }}>
                      <Button variant="primary" isLoading={busy} isDisabled={busy || changes.length === 0} onClick={() => onSync(changes)}>
                        {changes.length ? `Sync ${changes.length} ${changes.length === 1 ? 'change' : 'changes'}` : 'Sync'}
                      </Button>
                      <Button variant="link" isDisabled={busy} onClick={onDiscard}>Discard</Button>
                      <Content component="small">Applied as one job.</Content>
                    </Flex>
                  </div>
                ) : null}
              </CardBody>
            </Card>
          </>
        ) : null}
        {removing && removingRow ? (
          <PluginRegistryConfirmation
            title={`Remove ${name} ${removing}?`}
            body={`This deletes ${removingRow.stored.size} stored ${removingRow.stored.size === 1 ? 'binary' : 'binaries'} (${[...removingRow.stored].join(', ')}). Templates pinned to ${removing} will fail packer init. This can’t be undone. To stop serving it but keep the files, revoke it instead.${lastVersion
              ? ` It is ${name}'s last version, so ${name} is removed and its name can be used by another source.` : ''}`}
            verb={`Remove ${removing}`} busy={busy} danger
            onCancel={() => setRemoving(null)}
            onConfirm={() => { const version = removing; setRemoving(null); onRemove(version) }}
          />
        ) : null}
      </PageSection>
    </>
  )
}

function sourceLine(detail: PluginVersions): string {
  switch (detail.source.kind) {
    case 'releases-hashicorp': return `releases.hashicorp.com · github.com/hashicorp/${detail.source.repository ?? ''}`
    case 'github': return `github.com/${detail.source.repository ?? ''}`
    default: {
      const first = [...detail.versions].sort((a, b) => a.created_at.localeCompare(b.created_at))[0]
      return first ? `Uploaded · first version on ${new Date(first.created_at).toLocaleDateString()}` : 'Uploaded'
    }
  }
}

function checkedAt(check: Plugin['update_check'] | undefined): string {
  if (!check?.enabled) return 'Checks are off'
  if (!check.checked_at) return 'Not checked yet'
  const when = new Date(check.checked_at).toLocaleString()
  return check.error ? `${when} (failed)` : when
}

export function NotExposedAlert({ registry }: { registry: PluginRegistry | null }) {
  if (!registry || registry.exposed) return null
  return (
    <Alert variant="info" isInline title="The registry is enabled but not exposed">
      <Content component="p">Packer can’t resolve the template stanza on this page until the registry is exposed.</Content>
      <Button component="a" variant="link" isInline href="/plugin-registry/settings">Registry settings</Button>
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
