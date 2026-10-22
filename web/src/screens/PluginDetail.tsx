import { useCallback, useEffect, useState } from 'react'
import {
  Alert, AlertActionLink, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Checkbox, ClipboardCopyButton, CodeBlock,
  MenuToggle, Pagination, Progress, ProgressMeasureLocation, SearchInput, Select, SelectList, SelectOption, ToggleGroup, ToggleGroupItem, Toolbar, ToolbarContent, ToolbarItem,
  CodeBlockAction, CodeBlockCode, Content, DescriptionList, DescriptionListDescription, DescriptionListGroup,
  DescriptionListTerm, Flex, FlexItem, Grid, GridItem, Label, PageSection, Switch,
} from '@patternfly/react-core'
import { ActionsColumn, ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate, useParams } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  PLUGIN_ARCHITECTURES, PLUGIN_OS_GROUPS, compareVersionsDesc, deletePluginVersion, getPluginDownloads, getPluginRegistry, listHashicorpPluginVersions,
  listPluginVersions, listPlugins, pendingItems, pluginChanges, restorePluginVersion, revokePluginVersion, setPluginUpdateCheck,
  sourceLabel, syncPlugin, templateStanza, type HashicorpPluginVersion, type Plugin, type PluginChange, type PluginRegistry,
  type PluginDownloads, type PluginVersionDownloads, type PluginVersions,
  activeJobFor, firstError, getPluginImport, importJobSummary, jobTitle, jobWhat, listPluginImports, terminalImportStates, type PluginImport,
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
  const [downloads, setDownloads] = useState<PluginDownloads | null>(null)
  const [upstream, setUpstream] = useState<HashicorpPluginVersion[]>([])
  const [olderUpstream, setOlderUpstream] = useState<string | undefined>()
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [activeJob, setActiveJob] = useState<PluginImport | null>(null)
  const [finishedJob, setFinishedJob] = useState<PluginImport | null>(null)

  const reload = useCallback(async (quiet = false) => {
    if (!organizationID || token === '') return
    if (!quiet) setLoading(true)
    try {
      const [nextRegistry, nextDetail, plugins, nextDownloads, jobs] = await Promise.all([
        getPluginRegistry(token, organizationID), listPluginVersions(token, organizationID, name), listPlugins(token, organizationID),
        getPluginDownloads(token, organizationID, name),
        listPluginImports(token, organizationID, { states: ['unfinished'], plugins: [name], limit: 100 }),
      ])
      setRegistry(nextRegistry)
      setDetail(nextDetail)
      setSummary(plugins.find((plugin) => plugin.name === name) ?? null)
      setDownloads(nextDownloads)
      setActiveJob(activeJobFor(jobs.imports, name) ?? null)
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
  // While the plugin has a job, poll it; when it finishes, the notice turns
  // to its outcome and the versions reload.
  useEffect(() => {
    if (!activeJob) return
    const timer = setTimeout(() => void (async () => {
      try {
        const latest = await getPluginImport(token, organizationID ?? '', activeJob.id)
        if (terminalImportStates.has(latest.state)) {
          setFinishedJob(latest)
          await reload(true)
        } else {
          setActiveJob(latest)
        }
      } catch (error: unknown) {
        if (!signOutIfUnauthorized(error, signOut)) setActionFailure(pluginRegistryErrorMessage(error, 'The job could not be read.'))
      }
    })(), 3000)
    return () => clearTimeout(timer)
  }, [activeJob, organizationID, reload, signOut, token])

  return (
    <PluginDetailView
      name={name} organizationName={tenant.organization} host={window.location.hostname}
      activeJob={activeJob} finishedJob={activeJob ? null : finishedJob}
      onOpenJob={(id) => navigate(`/plugin-registry/imports/${id}`)}
      onJobs={() => navigate(`/plugin-registry?facet=jobs&plugin=${encodeURIComponent(name)}`)}
      callerRole={self?.role ?? null} registry={registry} detail={detail} summary={summary}
      downloads={downloads}
      upstream={upstream} hasOlderUpstream={olderUpstream !== undefined}
      loading={loading} failure={failure} onRefresh={reload}
      onBackToRegistry={() => navigate('/buckets')}
      onBackToPlugins={() => navigate('/plugin-registry')}
      onUpload={() => navigate('/plugin-registry/upload')}
      busy={busy} actionFailure={actionFailure}
      onToggleUpdates={act((enabled: boolean) => setPluginUpdateCheck(token, organizationID ?? '', name, enabled))}
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
      onRevoke={act((version: string) => revokePluginVersion(token, organizationID ?? '', name, version))}
      onRestore={act((version: string) => restorePluginVersion(token, organizationID ?? '', name, version))}
      onRemove={act(
        (version: string) => deletePluginVersion(token, organizationID ?? '', name, version),
        detail?.versions.length === 1,
      )}
    />
  )
}

type VersionRow = {
  version: string
  state: 'available' | 'revoked' | 'none'
  prerelease: boolean
  stored: Set<string>
  published: Set<string>
  released?: string
}

// What one platform of one version is, in the strip, the legend and the row's controls.
type MarkKind = 'mirrored' | 'revoked' | 'add' | 'free' | 'upstream' | 'unpublished'

const MARK_WORD: Record<MarkKind, string> = {
  mirrored: 'Mirrored', revoked: 'Mirrored, revoked', add: 'Will be added', free: 'Upstream', upstream: 'Upstream', unpublished: 'Not published',
}

const MARK_STYLE: Record<MarkKind, { background: string; boxShadow?: string }> = {
  mirrored: { background: 'var(--pf-t--global--color--brand--default)' },
  // Darker than the secondary surface an unpublished platform shows, so the two marks read apart.
  revoked: { background: 'var(--pf-t--global--color--nonstatus--gray--clicked)' },
  add: { background: 'var(--pf-t--global--color--nonstatus--blue--default)' },
  free: { background: 'var(--pf-t--global--background--color--primary--default)', boxShadow: 'inset 0 0 0 1px var(--pf-t--global--border--color--default)' },
  upstream: { background: 'var(--pf-t--global--background--color--primary--default)', boxShadow: 'inset 0 0 0 1px var(--pf-t--global--border--color--disabled)' },
  unpublished: { background: 'var(--pf-t--global--background--color--secondary--default)' },
}

const platformKey = (p: { os: string; arch: string }) => `${p.os}_${p.arch}`
const versionsPerPage = 12
const downloadNumber = new Intl.NumberFormat('en-US')

const emptyDownloadWindows = { last_7_days: 0, last_30_days: 0, last_365_days: 0, all_time: 0 }

function downloadVersionLabel(version: PluginVersionDownloads): string {
  if (version.state === 'removed') return `${version.version} (removed)`
  if (version.state === 'revoked') return `${version.version} (revoked)`
  return version.version
}

function PluginDownloadsCard({ downloads }: { downloads: PluginDownloads | null }) {
  const [selectedVersion, setSelectedVersion] = useState<string | null>(null)
  return <PluginDownloadsCardView downloads={downloads} selectedVersion={selectedVersion} onSelect={setSelectedVersion} />
}

export function PluginDownloadsCardView({
  downloads, selectedVersion, onSelect,
}: {
  downloads: PluginDownloads | null
  selectedVersion: string | null
  onSelect: (version: string | null) => void
}) {
  const [open, setOpen] = useState(false)
  const selected = downloads?.versions.find((version) => version.version === selectedVersion)
  const windows = selected ?? downloads?.all_versions ?? emptyDownloadWindows
  const label = selected ? downloadVersionLabel(selected) : 'All versions'
  const rows = [
    ['Last 7 days', windows.last_7_days],
    ['Last 30 days', windows.last_30_days],
    ['Last 365 days', windows.last_365_days],
    ['All time', windows.all_time],
  ] as const
  return (
    <Card isFullHeight>
      <CardTitle>
        <Flex alignItems={{ default: 'alignItemsCenter' }}>
          <FlexItem>Downloads</FlexItem>
          <FlexItem align={{ default: 'alignRight' }}>
            <Select
              aria-label="Download version" isOpen={open} selected={selectedVersion ?? '__all_versions__'} onOpenChange={setOpen}
              onSelect={(_event, value) => { onSelect(value === '__all_versions__' ? null : String(value)); setOpen(false) }}
              toggle={(ref) => <MenuToggle ref={ref} onClick={() => setOpen(!open)} isExpanded={open}>{label}</MenuToggle>}
            >
              <SelectList>
                <SelectOption value="__all_versions__">All versions</SelectOption>
                {(downloads?.versions ?? []).map((version) => (
                  <SelectOption key={version.version} value={version.version}>{downloadVersionLabel(version)}</SelectOption>
                ))}
              </SelectList>
            </Select>
          </FlexItem>
        </Flex>
      </CardTitle>
      <CardBody>
        <DescriptionList isCompact columnModifier={{ default: '2Col' }}>
          {rows.map(([term, count]) => (
            <DescriptionListGroup key={term} style={{ display: 'flex', flexDirection: 'column-reverse', justifyContent: 'flex-end' }}>
              <DescriptionListTerm>
                <span style={{ fontWeight: 'normal', color: 'var(--pf-t--global--text--color--subtle)' }}>{term}</span>
              </DescriptionListTerm>
              <DescriptionListDescription>
                <span style={{ fontFamily: 'var(--pf-t--global--font--family--heading)', fontSize: 'var(--pf-t--global--font--size--heading--h3)', fontWeight: 'var(--pf-t--global--font--weight--heading--default)' }}>
                  {downloadNumber.format(count)}
                </span>
              </DescriptionListDescription>
            </DescriptionListGroup>
          ))}
        </DescriptionList>
      </CardBody>
    </Card>
  )
}

export function CoverageStrip({ marks }: { marks: { platform: string; kind: MarkKind }[] }) {
  const firstOfGroup = new Set(PLUGIN_OS_GROUPS.map(([os, arches]) => `${os}_${arches[0]}`))
  return (
    <span aria-label="Platform coverage" style={{ display: 'inline-flex', alignItems: 'center', verticalAlign: 'middle' }}>
      {marks.map((mark, index) => (
        <span
          key={mark.platform} title={`${mark.platform}: ${MARK_WORD[mark.kind]}`}
          style={{ width: 6, height: 14, borderRadius: 1, marginLeft: index === 0 ? 0 : firstOfGroup.has(mark.platform) ? 4 : 1, ...MARK_STYLE[mark.kind] }}
        />
      ))}
    </span>
  )
}

export function PluginDetailView({
  name, organizationName, host, callerRole, registry, detail, summary, downloads, upstream, hasOlderUpstream, loading, failure,
  onRefresh, onBackToRegistry, onBackToPlugins, onUpload, busy, actionFailure, onToggleUpdates,
  onOlderUpstream, onSync, onRevoke, onRestore, onRemove,
  activeJob = null, finishedJob = null, onOpenJob = () => {}, onJobs = () => {},
}: {
  name: string
  organizationName: string
  host: string
  activeJob?: PluginImport | null
  finishedJob?: PluginImport | null
  onOpenJob?: (id: string) => void
  onJobs?: () => void
  callerRole: Role | null
  registry: PluginRegistry | null
  detail: PluginVersions | null
  summary: Plugin | null
  downloads?: PluginDownloads | null
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
  onToggleUpdates: (enabled: boolean) => void
  onOlderUpstream: () => void
  onSync: (changes: PluginChange[]) => void
  onRevoke: (version: string) => void
  onRestore: (version: string) => void
  onRemove: (version: string) => void
}) {
  const [confirming, setConfirming] = useState<{ kind: ConfirmKind; version: string } | null>(null)
  const [added, setAdded] = useState<Record<string, string[]>>({})
  const [scope, setScope] = useState<'mirrored' | 'all'>('mirrored')
  const [filter, setFilter] = useState('')
  const [page, setPage] = useState(1)
  const [expanded, setExpanded] = useState<string | null>(null)
  useEffect(() => { setAdded({}) }, [detail])
  const canPublish = permitsAction(callerRole, 'publishPlugin')
  const local = detail?.source.kind === 'upload'
  const newest = detail?.versions.find((version) => !version.revoked)
  const mirrored = new Set(detail?.versions.map((v) => v.version) ?? [])
  const unmirrored = local ? [] : upstream.filter((v) => !mirrored.has(v.version))
  const releasedAt = new Map(upstream.map((v) => [v.version, v.created_at]))
  // After a plugin's first import, its existing platforms are the default (ADR-0027 A8).
  const existing = [...new Set(detail?.versions.flatMap((v) => v.stored_platforms.map(platformKey)) ?? [])]
  const rows: VersionRow[] = [
    ...unmirrored.map((v) => ({ version: v.version, state: 'none' as const, prerelease: v.prerelease, stored: new Set<string>(), published: new Set(v.platforms), released: v.created_at })),
    ...(detail?.versions ?? []).map((v) => ({
      version: v.version, state: v.revoked ? 'revoked' as const : 'available' as const, prerelease: false,
      stored: new Set(v.stored_platforms.map(platformKey)),
      published: new Set([...v.listed_platforms, ...v.stored_platforms].map(platformKey)),
      released: releasedAt.get(v.version) ?? v.created_at,
    })),
  ].sort((a, b) => compareVersionsDesc(a.version, b.version))
  const held = rows.filter((row) => row.state !== 'none')
  const newestHeld = held[0]
  const newer = newestHeld ? rows.filter((row) => row.state === 'none' && compareVersionsDesc(row.version, newestHeld.version) < 0) : []
  const changes = detail ? pluginChanges(detail.versions, unmirrored, added) : []
  const pending = pendingItems(changes, mirrored)
  const adds = (row: VersionRow) => (added[row.version] ?? []).filter((p) => !row.stored.has(p))
  const kind = (row: VersionRow, platform: string): MarkKind => {
    if (!row.published.has(platform)) return 'unpublished'
    if (row.stored.has(platform)) return row.state === 'revoked' ? 'revoked' : 'mirrored'
    if (adds(row).includes(platform)) return 'add'
    return row.state === 'revoked' ? 'upstream' : 'free'
  }
  const editable = (k: MarkKind) => canPublish && !local && (k === 'free' || k === 'add')
  const setAdds = (version: string, platforms: string[] | undefined) => setAdded((current) => {
    const next = { ...current }
    if (platforms === undefined) delete next[version]; else next[version] = platforms
    return next
  })
  const defaults = (row: VersionRow) => existing.filter((p) => row.published.has(p))
  const togglePlatform = (row: VersionRow, platform: string) => {
    const next = adds(row).includes(platform) ? adds(row).filter((p) => p !== platform) : [...adds(row), platform]
    setAdds(row.version, next.length ? next : undefined)
  }
  const action = (row: VersionRow): { text: string; run: () => void } | null => {
    if (!canPublish) return null
    if (row.state === 'available') return { text: 'Revoke', run: () => setConfirming({ kind: 'revoke', version: row.version }) }
    if (row.state === 'revoked') return { text: 'Restore', run: () => setConfirming({ kind: 'restore', version: row.version }) }
    if (local) return null
    return adds(row).length
      ? { text: 'Clear', run: () => setAdds(row.version, undefined) }
      : { text: `Add with ${defaults(row).length} platforms`, run: () => setAdds(row.version, defaults(row)) }
  }
  const marker = (row: VersionRow) => {
    const n = adds(row).length
    return n ? (row.state === 'none' ? `→ add ${n}` : `+${n}`) : ''
  }
  const coverage = (row: VersionRow) => {
    const n = adds(row).length
    return `${row.stored.size} of ${row.published.size}${n ? ` (+${n})` : ''}`
  }
  const listed = (scope === 'mirrored' ? held : rows).filter((row) => row.version.includes(filter.trim()))
  const shown = scope === 'all' ? listed.slice((page - 1) * versionsPerPage, page * versionsPerPage) : listed
  const newestStable = newer.find((row) => !row.prerelease)
  const confirmingRow = rows.find((row) => row.version === confirming?.version)
  const lastVersion = detail?.versions.length === 1
  const check = summary?.update_check
  const legend: { kind: MarkKind; text: string }[] = [
    { kind: 'mirrored', text: 'Mirrored' }, { kind: 'revoked', text: 'Revoked' }, { kind: 'add', text: 'Will be added' },
    { kind: 'free', text: 'Upstream' }, { kind: 'unpublished', text: 'Not published' },
  ]

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
        actions={detail ? <Button variant="link" onClick={onJobs}>Jobs for {name}</Button> : undefined}
        onRefresh={onRefresh} refreshing={loading}
      />
      <PageSection variant="secondary" isFilled>
        {detail && !registry?.exposed && registry ? <NotExposedAlert registry={registry} /> : null}
        {activeJob ?? finishedJob ? <JobNotice job={(activeJob ?? finishedJob) as PluginImport} organizationName={organizationName} onOpenJob={onOpenJob} onJobs={onJobs} /> : null}
        {actionFailure ? <Alert variant="danger" isInline title="The action failed"><Content component="p">{actionFailure}</Content></Alert> : null}
        {loading && !detail ? <PluginLoadingCard message={`Loading ${name}…`} /> : null}
        {failure ? <PluginErrorCard title={`${name} could not be loaded`} error={failure} onRetry={onRefresh} /> : null}
        {detail ? (
          <>
            <Grid hasGutter style={{ marginBottom: 'var(--pf-t--global--spacer--lg)' }}>
              <GridItem span={12} lg={6} style={{ minWidth: 0 }}>
                <Card isFullHeight>
                  <CardTitle>Template stanza <Content component="small">newest available version</Content></CardTitle>
                  <CardBody>
                    {newest ? <TemplateStanzaBlock hcl={templateStanza(host, organizationName, name, newest.version)} />
                      : <Content component="p">No version is available: every version is revoked.</Content>}
                  </CardBody>
                </Card>
              </GridItem>
              <GridItem span={12} md={6} lg={3} style={{ minWidth: 0 }}>
                <PluginDownloadsCard downloads={downloads ?? null} />
              </GridItem>
              <GridItem span={12} md={6} lg={3} style={{ minWidth: 0 }}>
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
                      </>
                    )}
                  </CardBody>
                </Card>
              </GridItem>
            </Grid>
            <Card>
              <CardTitle>Versions</CardTitle>
              <CardBody>
                <Toolbar id="plugin-versions-toolbar">
                  <ToolbarContent alignItems="center">
                    {local ? null : (
                      <ToolbarItem>
                        <ToggleGroup aria-label="Versions shown">
                          <ToggleGroupItem text={`Mirrored · ${held.length}`} buttonId="versions-mirrored" isSelected={scope === 'mirrored'} onChange={() => { setScope('mirrored'); setPage(1) }} />
                          <ToggleGroupItem text={`All · ${rows.length}`} buttonId="versions-all" isSelected={scope === 'all'} onChange={() => { setScope('all'); setPage(1) }} />
                        </ToggleGroup>
                      </ToolbarItem>
                    )}
                    <ToolbarItem>
                      <SearchInput aria-label="Filter versions" placeholder="Filter by version" value={filter} onChange={(_event, value) => { setFilter(value); setPage(1) }} onClear={() => setFilter('')} />
                    </ToolbarItem>
                    <ToolbarItem align={{ default: 'alignEnd' }}>
                      <span style={{ display: 'inline-flex', gap: 14, fontSize: 'var(--pf-t--global--font--size--xs)', color: 'var(--pf-t--global--text--color--subtle)' }}>
                        {legend.map((entry) => (
                          <span key={entry.kind} style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
                            <span style={{ width: 6, height: 14, borderRadius: 1, ...MARK_STYLE[entry.kind] }} />{entry.text}
                          </span>
                        ))}
                      </span>
                    </ToolbarItem>
                  </ToolbarContent>
                </Toolbar>
                {scope === 'mirrored' && newer.length ? (
                  <Alert variant="custom" isInline isPlain title={`${newer.length} newer upstream ${newer.length === 1 ? 'version' : 'versions'}`}>
                    <Flex gap={{ default: 'gapMd' }} alignItems={{ default: 'alignItemsCenter' }}>
                      <FlexItem>{newer.map((row) => row.version + (row.prerelease ? ' (prerelease)' : '')).join(' · ')}</FlexItem>
                      {canPublish && newestStable ? (
                        <FlexItem>
                          {adds(newestStable).length
                            ? <Content component="small">{newestStable.version} pending ({adds(newestStable).length} platforms)</Content>
                            : <Button variant="link" isInline onClick={() => setAdds(newestStable.version, defaults(newestStable))}>Add {newestStable.version} with {defaults(newestStable).length} platforms</Button>}
                        </FlexItem>
                      ) : null}
                      <FlexItem><Button variant="link" isInline onClick={() => { setScope('all'); setPage(1) }}>Show all versions</Button></FlexItem>
                    </Flex>
                  </Alert>
                ) : null}
                <Table aria-label="Versions" variant="compact">
                  <Thead>
                    <Tr>
                      <Th screenReaderText="Platforms" />
                      <Th>Version</Th><Th>Status</Th><Th>Platforms</Th><Th>Released</Th>
                      {canPublish ? <Th screenReaderText="Actions" /> : null}
                    </Tr>
                  </Thead>
                  {shown.map((row, index) => {
                    const isExpanded = expanded === row.version
                    const mark = marker(row)
                    const act = action(row)
                    return (
                      <Tbody key={row.version} isExpanded={isExpanded}>
                        <Tr isRowSelected={mark !== ''}>
                          <Td expand={{ rowIndex: index, isExpanded, onToggle: () => setExpanded(isExpanded ? null : row.version), expandId: `platforms-${row.version}` }} />
                          <Td dataLabel="Version"><code>{row.version}</code></Td>
                          <Td dataLabel="Status">
                            {row.state === 'available' ? <Label isCompact color="green">Available</Label>
                              : row.state === 'revoked' ? <Label isCompact color="grey">Revoked</Label>
                              : <Label isCompact variant="outline">{row.prerelease ? 'Not mirrored · prerelease' : 'Not mirrored'}</Label>}
                            {mark ? <> <span style={{ color: 'var(--pf-t--global--color--nonstatus--blue--default)', fontSize: 'var(--pf-t--global--font--size--xs)' }}>{mark}</span></> : null}
                          </Td>
                          <Td dataLabel="Platforms" modifier="nowrap">
                            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 12 }}>
                              <CoverageStrip marks={PLUGIN_ARCHITECTURES.map((platform) => ({ platform, kind: kind(row, platform) }))} />
                              <span style={{ color: 'var(--pf-t--global--text--color--subtle)', fontSize: 'var(--pf-t--global--font--size--sm)' }}>{coverage(row)}</span>
                            </span>
                          </Td>
                          <Td dataLabel="Released">{row.released ? new Date(row.released).toLocaleDateString() : ''}</Td>
                          {canPublish ? (
                            <Td isActionCell modifier="nowrap">
                              <Flex gap={{ default: 'gapSm' }} alignItems={{ default: 'alignItemsCenter' }} justifyContent={{ default: 'justifyContentFlexEnd' }} flexWrap={{ default: 'nowrap' }}>
                                {act ? <Button variant="link" isInline isDisabled={busy} onClick={act.run} style={{ whiteSpace: 'nowrap' }}>{act.text}</Button> : null}
                                {row.state !== 'none' ? <ActionsColumn items={[{ title: 'Remove version', onClick: () => setConfirming({ kind: 'remove', version: row.version }) }]} /> : null}
                              </Flex>
                            </Td>
                          ) : null}
                        </Tr>
                        <Tr isExpanded={isExpanded}>
                          <Td />
                          <Td dataLabel="Platforms" colSpan={canPublish ? 5 : 4}>
                            <ExpandableRowContent>
                              <Flex gap={{ default: 'gapXl' }} flexWrap={{ default: 'wrap' }}>
                                {PLUGIN_OS_GROUPS.map(([os, arches]) => {
                                  const items = arches.map((arch) => ({ platform: `${os}_${arch}`, arch, kind: kind(row, `${os}_${arch}`) }))
                                  const publishedCount = items.filter((item) => item.kind !== 'unpublished').length
                                  const mirroredCount = items.filter((item) => item.kind === 'mirrored' || item.kind === 'revoked').length
                                  return (
                                    <FlexItem key={os}>
                                      <Content component="small"><strong>{os}</strong> {publishedCount ? `${mirroredCount} of ${publishedCount}` : 'not published'}</Content>
                                      {items.map((item) => editable(item.kind) ? (
                                        <Checkbox
                                          key={item.platform} id={`add-${row.version}-${item.platform}`} aria-label={`Add ${item.platform} to ${row.version}`}
                                          label={<code>{item.arch}</code>} isChecked={item.kind === 'add'} title={MARK_WORD[item.kind]}
                                          onChange={() => togglePlatform(row, item.platform)}
                                        />
                                      ) : (
                                        <div key={item.platform} title={item.kind === 'upstream' && row.state === 'revoked' ? 'Restore the version first' : MARK_WORD[item.kind]} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                                          <span style={{ width: 12, height: 12, borderRadius: item.kind === 'unpublished' ? 0 : '50%', ...MARK_STYLE[item.kind] }} />
                                          <code style={{ color: item.kind === 'unpublished' ? 'var(--pf-t--global--text--color--disabled)' : undefined }}>{item.arch}</code>
                                        </div>
                                      ))}
                                    </FlexItem>
                                  )
                                })}
                              </Flex>
                            </ExpandableRowContent>
                          </Td>
                        </Tr>
                      </Tbody>
                    )
                  })}
                </Table>
                {listed.length === 0 ? <Content component="p">No versions match.</Content> : null}
                {scope === 'all' ? (
                  <Pagination
                    variant="bottom" itemCount={listed.length} perPage={versionsPerPage} page={page} perPageOptions={[{ title: '12', value: 12 }]}
                    onSetPage={(_event, next) => setPage(next)} titles={{ paginationAriaLabel: 'Versions pagination' }}
                  />
                ) : null}
                {scope === 'all' && hasOlderUpstream ? <Button variant="link" isInline onClick={onOlderUpstream}>Show older upstream versions</Button> : null}
                {canPublish ? (
                  <Flex gap={{ default: 'gapSm' }} alignItems={{ default: 'alignItemsCenter' }} style={{ marginTop: 16, paddingTop: 12, borderTop: '1px solid var(--pf-t--global--border--color--default)' }}>
                    <FlexItem><strong>{pending.length ? `Pending · ${pending.length}` : 'No pending changes'}</strong></FlexItem>
                    {pending.map((item) => (
                      <FlexItem key={item.version}>
                        <Label color="blue" onClose={() => setAdds(item.version, undefined)} closeBtnAriaLabel={`Undo ${item.text}`}>
                          {item.text}
                        </Label>
                      </FlexItem>
                    ))}
                    <FlexItem align={{ default: 'alignRight' }}>
                      <Button variant="primary" isLoading={busy} isDisabled={busy || changes.length === 0} onClick={() => onSync(changes)}>
                        {changes.length ? `Sync ${changes.length} ${changes.length === 1 ? 'change' : 'changes'}` : 'Sync'}
                      </Button>
                    </FlexItem>
                  </Flex>
                ) : null}
              </CardBody>
            </Card>
          </>
        ) : null}
        {confirming && confirmingRow ? (
          <PluginRegistryConfirmation
            {...versionConfirmation(confirming.kind, name, confirmingRow, lastVersion)} busy={busy}
            onCancel={() => setConfirming(null)}
            onConfirm={() => {
              const { kind, version } = confirming
              setConfirming(null)
              if (kind === 'remove') onRemove(version)
              else if (kind === 'revoke') onRevoke(version)
              else onRestore(version)
            }}
          />
        ) : null}
      </PageSection>
    </>
  )
}

type ConfirmKind = 'revoke' | 'restore' | 'remove'

export function versionConfirmation(
  kind: ConfirmKind, name: string, row: { version: string; stored: Set<string> }, lastVersion: boolean,
): { title: string; body: string; verb: string; danger: boolean } {
  const files = `${row.stored.size} stored ${row.stored.size === 1 ? 'binary' : 'binaries'} (${[...row.stored].join(', ')})`
  switch (kind) {
    case 'revoke': return {
      title: `Revoke ${name} ${row.version}?`,
      body: `Packer gets 404 for each of its files, so templates pinned to ${row.version} fail packer init until it is restored. The ${files} ${row.stored.size === 1 ? 'is' : 'are'} kept. To delete ${row.stored.size === 1 ? 'it' : 'them'}, remove the version instead.`,
      verb: `Revoke ${row.version}`, danger: false,
    }
    case 'restore': return {
      title: `Restore ${name} ${row.version}?`,
      body: `Packer resolves ${row.version} again from its ${files}.`,
      verb: `Restore ${row.version}`, danger: false,
    }
    default: return {
      title: `Remove ${name} ${row.version}?`,
      body: `This deletes ${files}. Templates pinned to ${row.version} will fail packer init. This can’t be undone. To stop serving it but keep the files, revoke it instead.${lastVersion
        ? ` It is ${name}'s last version, so ${name} is removed and its name can be used by another source.` : ''}`,
      verb: `Remove ${row.version}`, danger: true,
    }
  }
}

function checkedAt(check: Plugin['update_check'] | undefined): string {
  if (!check?.enabled) return 'Checks are off'
  if (!check.checked_at) return 'Not checked yet'
  const when = new Date(check.checked_at).toLocaleString()
  return check.error ? `${when} (failed)` : when
}

// The plugin page's notice for its unfinished job, and for that job's
// outcome once it finishes while the page is open.
export function JobNotice({ job, organizationName, onOpenJob, onJobs }: {
  job: PluginImport
  organizationName: string
  onOpenJob: (id: string) => void
  onJobs: () => void
}) {
  const id = job.id.slice(0, 8)
  const name = jobTitle(job).split(' ').slice(1).join(' ')
  const ahead = job.queued_ahead
  const copy: Record<PluginImport['state'], { variant: 'info' | 'success' | 'warning' | 'danger'; title: string; body: string }> = {
    queued: {
      variant: 'info', title: `${jobTitle(job)} is queued`,
      body: `Job ${id}: ${jobWhat(job)}. ${ahead ? `${ahead} ${ahead === 1 ? 'job' : 'jobs'} for ${organizationName} run first; nothing below changes until it runs.` : 'It runs next; nothing below changes until it does.'}`,
    },
    running: { variant: 'info', title: `${jobTitle(job)} is running`, body: `Job ${id}: ${jobWhat(job)}. Versions below update as each one is stored.` },
    succeeded: { variant: 'success', title: `${jobTitle(job)} succeeded`, body: `Job ${id}: ${jobWhat(job)}. The versions below are up to date.` },
    partially_succeeded: { variant: 'warning', title: `${jobTitle(job)} partly succeeded`, body: `Job ${id}: ${jobWhat(job)}. ${firstError(job.outcomes) ?? ''} The versions below show what landed.` },
    failed: { variant: 'danger', title: `${jobTitle(job)} failed`, body: `Job ${id}: ${jobWhat(job)}. ${firstError(job.outcomes) ?? ''} Nothing below changed.` },
  }
  const { variant, title, body } = copy[job.state]
  const progress = job.state === 'running' ? importJobSummary(job).progress : undefined
  return (
    <Alert
      variant={variant} isInline title={title}
      actionLinks={(
        <>
          <AlertActionLink onClick={() => onOpenJob(job.id)}>Open job</AlertActionLink>
          <AlertActionLink onClick={onJobs}>All jobs for {name}</AlertActionLink>
        </>
      )}
    >
      <Content component="p">{body}</Content>
      {progress !== undefined ? <Progress value={progress} aria-label="Job progress" measureLocation={ProgressMeasureLocation.none} size="sm" /> : null}
    </Alert>
  )
}

export function NotExposedAlert({ registry }: { registry: PluginRegistry | null }) {
  if (!registry || registry.exposed) return null
  return (
    <Alert variant="info" isInline title="The registry is enabled but not exposed">
      <Content component="p">Packer can’t resolve the template stanza on this page until the registry is exposed.</Content>
      <Button component="a" variant="link" isInline href="/plugin-registry/settings">Plugin registry</Button>
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
