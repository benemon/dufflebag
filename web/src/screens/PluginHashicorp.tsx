import { useCallback, useEffect, useState } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Checkbox, Content, Flex, FlexItem, Label,
  MenuToggle, PageSection, Select, SelectList, SelectOption, Spinner, Switch, Toolbar, ToolbarContent, ToolbarItem,
} from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import { FacetRail, knownCount } from './RegistryFacets'
import {
  PLUGIN_OS_GROUPS, createPluginImport, getDefaultPlatforms, listHashicorpPluginVersions, listHashicorpPlugins,
  listPluginVersions, sourceLabel, type HashicorpPlugin, type HashicorpPluginVersion, type PluginVersion,
} from '../data/pluginRegistry'
import { useTenant } from '../data/tenant'
import { pluginRegistryErrorMessage } from './Plugins'

export function PluginHashicorp() {
  const { state, selectedOrganization, signOut } = useAuth()
  const { tenant } = useTenant()
  const navigate = useNavigate()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? ''
  const token = state?.token ?? ''
  const [plugins, setPlugins] = useState<HashicorpPlugin[] | null>(null)
  const [selected, setSelected] = useState<HashicorpPlugin | null>(null)
  const [versions, setVersions] = useState<HashicorpPluginVersion[] | null>(null)
  const [heldVersions, setHeldVersions] = useState<PluginVersion[]>([])
  const [next, setNext] = useState<string | undefined>()
  const [preselected, setPreselected] = useState<string[]>([])
  const [failure, setFailure] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const guard = useCallback(async (work: () => Promise<void>, fallback: string) => {
    try {
      await work()
      setFailure(null)
    } catch (error: unknown) {
      if (!signOutIfUnauthorized(error, signOut)) setFailure(pluginRegistryErrorMessage(error, fallback))
    }
  }, [signOut])

  const reload = useCallback(async () => {
    if (!organizationID || token === '') return
    setPlugins(null)
    await guard(async () => setPlugins(await listHashicorpPlugins(token, organizationID)), 'releases.hashicorp.com could not be reached.')
  }, [guard, organizationID, token])

  useEffect(() => { void reload() }, [reload])

  const choose = async (plugin: HashicorpPlugin) => {
    setSelected(plugin)
    setVersions(null)
    setHeldVersions([])
    await guard(async () => {
      const page = await listHashicorpPluginVersions(token, organizationID, plugin.product)
      setVersions(page.versions)
      setNext(page.next)
      // After a first import, a plugin's existing platforms are the default;
      // before it, the organization's (ADR-0027 A8).
      if (plugin.mirrored_versions > 0) {
        const held = await listPluginVersions(token, organizationID, plugin.name)
        setHeldVersions(held.versions)
        setPreselected([...new Set(held.versions.flatMap((v) => v.stored_platforms.map((p) => `${p.os}_${p.arch}`)))])
      } else {
        setPreselected(await getDefaultPlatforms(token, organizationID))
      }
    }, 'The plugin\'s releases could not be loaded.')
  }

  return (
    <PluginHashicorpView
      organizationName={tenant.organization}
      plugins={plugins} selected={selected} versions={versions} heldVersions={heldVersions} hasMore={next !== undefined}
      preselected={preselected} loading={plugins === null && !failure} failure={failure} busy={busy}
      onBackToRegistry={() => navigate('/buckets')}
      onBackToPlugins={() => navigate('/plugin-registry')}
      onRefresh={reload}
      onChoose={(plugin) => void choose(plugin)}
      onMore={() => void guard(async () => {
        if (!selected || !next) return
        const page = await listHashicorpPluginVersions(token, organizationID, selected.product, next)
        setVersions((current) => [...(current ?? []), ...page.versions])
        setNext(page.next)
      }, 'More releases could not be loaded.')}
      onImport={async (chosenVersions, platforms) => {
        if (!selected) return
        setBusy(true)
        await guard(async () => {
          const job = await createPluginImport(token, organizationID, selected.product, chosenVersions, platforms)
          navigate(`/plugin-registry/imports/${job.id}`)
        }, 'The import could not be queued.')
        setBusy(false)
      }}
    />
  )
}

export function hashicorpPluginNote(plugin: HashicorpPlugin): string {
  if (plugin.held_by) return `Held from ${sourceLabel(plugin.held_by)}${plugin.held_by.repository ? ` (${plugin.held_by.repository})` : ''}`
  return plugin.mirrored_versions ? `${plugin.mirrored_versions} mirrored` : 'Not mirrored'
}

export function PluginHashicorpView({
  organizationName, plugins, selected, versions, heldVersions, hasMore, preselected, loading, failure, busy,
  onBackToRegistry, onBackToPlugins, onRefresh, onChoose, onMore, onImport,
}: {
  organizationName: string
  plugins: HashicorpPlugin[] | null
  selected: HashicorpPlugin | null
  versions: HashicorpPluginVersion[] | null
  heldVersions: PluginVersion[]
  hasMore: boolean
  preselected: string[]
  loading: boolean
  failure: string | null
  busy: boolean
  onBackToRegistry: () => void
  onBackToPlugins: () => void
  onRefresh: () => void | Promise<void>
  onChoose: (plugin: HashicorpPlugin) => void
  onMore: () => void
  onImport: (versions: string[], platforms: string[]) => void | Promise<void>
}) {
  const [showPrereleases, setShowPrereleases] = useState(false)
  const [platformFilter, setPlatformFilter] = useState('any')
  const [filterOpen, setFilterOpen] = useState(false)
  const [chosen, setChosen] = useState<string[]>([])
  const [platforms, setPlatforms] = useState<string[] | null>(null)
  useEffect(() => { setChosen([]); setPlatforms(null); setPlatformFilter('any') }, [selected])
  const held = selected?.held_by
  const all = versions ?? []
  const shownVersions = all
    .filter((v) => showPrereleases || !v.prerelease)
    .filter((v) => platformFilter === 'any' || v.platforms.includes(platformFilter))
  const published = [...new Set(all.flatMap((v) => v.platforms))].sort()
  const publishedForChosen = [...new Set(all.filter((v) => chosen.includes(v.version)).flatMap((v) => v.platforms))]
  const selectedPlatforms = platforms ?? preselected
  const importable = chosen.length > 0 && selectedPlatforms.length > 0
  const firstImport = (selected?.mirrored_versions ?? 0) === 0
  const toggle = (list: string[], value: string) => (list.includes(value) ? list.filter((v) => v !== value) : [...list, value])
  const heldByVersion = new Map(heldVersions.map((v) => [v.version, v]))
  const choosable = shownVersions.filter((v) => !v.mirrored && !held).map((v) => v.version)
  const allChosen = choosable.length > 0 && choosable.every((v) => chosen.includes(v))

  const releases = selected ? (
    <>
      <Card>
        <CardTitle>
          Releases{' '}
          <Content component="small">github.com/hashicorp/{selected.product}</Content>
        </CardTitle>
        <CardBody>
          {held ? (
            <Alert variant="danger" isInline title={`${selected.name} is held from ${sourceLabel(held)}`}>
              <Content component="p">
                In {organizationName}, {selected.name} comes from {sourceLabel(held)}{held.repository ? ` (${held.repository})` : ''}.
                A name belongs to one source per organization, so HashiCorp’s {selected.name} can’t be imported.
                The name is freed once every {selected.name} version is removed; revoked versions still count.
              </Content>
            </Alert>
          ) : null}
          <Toolbar id="hashicorp-releases-toolbar">
            <ToolbarContent alignItems="center">
              <ToolbarItem>
                <Switch id="show-prereleases" label="Show prereleases" isChecked={showPrereleases} onChange={(_e, value) => setShowPrereleases(value)} />
              </ToolbarItem>
              <ToolbarItem>
                <Select
                  isOpen={filterOpen} selected={platformFilter} onOpenChange={setFilterOpen}
                  onSelect={(_event, value) => { setPlatformFilter(String(value)); setFilterOpen(false) }}
                  toggle={(ref) => <MenuToggle ref={ref} onClick={() => setFilterOpen((open) => !open)} isExpanded={filterOpen}>Platform: {platformFilter}</MenuToggle>}
                >
                  <SelectList>
                    <SelectOption value="any">any</SelectOption>
                    {published.map((platform) => <SelectOption key={platform} value={platform}>{platform}</SelectOption>)}
                  </SelectList>
                </Select>
              </ToolbarItem>
              <ToolbarItem align={{ default: 'alignEnd' }}>
                <span style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
                  {versions === null ? '' : `${shownVersions.length} of ${all.length} versions${showPrereleases ? '' : ' · prereleases hidden'}`}
                </span>
              </ToolbarItem>
            </ToolbarContent>
          </Toolbar>
          {versions === null ? <Spinner aria-label="Loading releases…" /> : (
            <Table aria-label={`${selected.name} releases`} variant="compact" selectableRowCaptionText="Release">
              <Thead>
                <Tr>
                  <Th
                    aria-label="Select importable releases"
                    select={{
                      isSelected: allChosen,
                      isIndeterminate: choosable.some((v) => chosen.includes(v)) && !allChosen,
                      isHeaderSelectDisabled: choosable.length === 0,
                      onSelect: (_event, isSelecting) => setChosen(isSelecting
                        ? [...new Set([...chosen, ...choosable])] : chosen.filter((v) => !choosable.includes(v))),
                    }}
                  />
                  <Th>Version</Th><Th>Released</Th><Th>Lifecycle</Th><Th>Platforms</Th><Th>Changelog</Th><Th>In dufflebag</Th>
                </Tr>
              </Thead>
              <Tbody>
                {shownVersions.map((version, index) => {
                  const mirrored = heldByVersion.get(version.version)
                  const on = version.mirrored || chosen.includes(version.version)
                  const disabled = version.mirrored || Boolean(held)
                  return (
                    <Tr key={version.version} isSelectable={!disabled} isRowSelected={on && !disabled}>
                      <Td
                        title={version.mirrored ? 'Already mirrored' : held ? 'Not importable' : undefined}
                        select={{
                          rowIndex: index,
                          isSelected: on && !held,
                          isDisabled: disabled,
                          onSelect: () => setChosen(toggle(chosen, version.version)),
                        }}
                      />
                      <Td dataLabel="Version">
                        <code>{version.version}</code>{' '}
                        {version.prerelease ? <Label isCompact variant="outline">prerelease</Label> : null}
                      </Td>
                      <Td dataLabel="Released">{version.created_at ? new Date(version.created_at).toLocaleDateString() : ''}</Td>
                      <Td dataLabel="Lifecycle">{version.state ?? '—'}</Td>
                      <Td dataLabel="Platforms">{version.platforms.length} platforms</Td>
                      <Td dataLabel="Changelog">
                        {version.changelog ? <a href={version.changelog} target="_blank" rel="noreferrer">Changelog ↗</a> : '—'}
                      </Td>
                      <Td dataLabel="In dufflebag">
                        {version.mirrored
                          ? <>{mirrored?.revoked ? <Label isCompact color="grey">Revoked</Label> : <Label isCompact color="green">Available</Label>}{mirrored ? <> <Content component="small">{mirrored.stored_platforms.length} platforms</Content></> : null}</>
                          : held ? <Content component="small">Not importable</Content> : null}
                      </Td>
                    </Tr>
                  )
                })}
              </Tbody>
            </Table>
          )}
          {hasMore ? <Button variant="link" isInline onClick={onMore}>Show older releases</Button> : null}
        </CardBody>
      </Card>
      {!held ? (
        <Card>
          <CardTitle>
            Platforms to import{' '}
            <Content component="small">
              {firstImport
                ? `Preselected: ${organizationName}’s default platforms (Administration, Plugin registry), since this is a first import.`
                : `Preselected: the architectures ${selected.name} already has.`}
            </Content>
          </CardTitle>
          <CardBody>
            <Flex gap={{ default: 'gapLg' }} flexWrap={{ default: 'wrap' }}>
              {PLUGIN_OS_GROUPS.map(([os, arches]) => (
                <FlexItem key={os}>
                  <Content component="small">{os}</Content>
                  {arches.map((arch) => {
                    const platform = `${os}_${arch}`
                    const unpublished = chosen.length > 0 && !publishedForChosen.includes(platform)
                    return (
                      <Checkbox
                        key={platform} id={`platform-${platform}`}
                        label={<>{arch} {preselected.includes(platform) ? <Label isCompact variant="outline">{firstImport ? 'org default' : 'existing'}</Label> : null}</>}
                        isChecked={selectedPlatforms.includes(platform)} isDisabled={unpublished}
                        title={unpublished ? 'Not published for the selected versions' : undefined}
                        onChange={() => setPlatforms(toggle(selectedPlatforms, platform))}
                      />
                    )
                  })}
                </FlexItem>
              ))}
            </Flex>
            <Flex gap={{ default: 'gapSm' }} alignItems={{ default: 'alignItemsCenter' }} style={{ marginTop: 16 }}>
              <Button
                variant="primary" isLoading={busy} isDisabled={busy || !importable}
                onClick={() => void onImport(chosen, selectedPlatforms)}
              >
                {chosen.length ? `Import ${chosen.length} ${chosen.length === 1 ? 'version' : 'versions'} · ${selectedPlatforms.length} platforms` : 'Import'}
              </Button>
              <Button variant="link" isDisabled={busy} onClick={onBackToPlugins}>Cancel</Button>
              <span style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>
                {chosen.length ? `${chosen.join(', ')} × ${selectedPlatforms.join(', ')}` : 'Select at least one version'}
              </span>
            </Flex>
          </CardBody>
        </Card>
      ) : null}
    </>
  ) : (
    <Card><CardBody><Content component="p">Choose a plugin to see its releases.</Content></CardBody></Card>
  )

  return (
    <>
      <ScreenHeader
        breadcrumbs={(
          <Breadcrumb>
            <BreadcrumbItem component="button" onClick={onBackToRegistry}>Registry</BreadcrumbItem>
            <BreadcrumbItem component="button" onClick={onBackToPlugins}>Plugins</BreadcrumbItem>
            <BreadcrumbItem isActive>Browse HashiCorp</BreadcrumbItem>
          </Breadcrumb>
        )}
        title="Browse HashiCorp"
      />
      <PageSection
        variant="secondary" isFilled hasBodyWrapper={false}
        padding={{ default: loading || failure || !plugins ? 'padding' : 'noPadding' }}
      >
        {loading ? (
          <PluginLoadingCard message="Reading releases.hashicorp.com…" />
        ) : failure ? (
          <PluginErrorCard title="HashiCorp's plugin list could not be read" error={failure} onRetry={onRefresh} />
        ) : plugins ? (
          <FacetRail
            active={selected?.product ?? ''}
            onSelect={(product) => { const plugin = plugins.find((p) => p.product === product); if (plugin) onChoose(plugin) }}
            heading={`Plugins · ${plugins.length} published`}
            label="HashiCorp plugins"
            unmountOnExit
            facets={plugins.map((plugin) => ({
              key: plugin.product,
              label: plugin.held_by ? `${plugin.name} · held` : plugin.name,
              count: plugin.mirrored_versions ? knownCount(plugin.mirrored_versions) : undefined,
              content: selected?.product === plugin.product ? releases : null,
            }))}
          />
        ) : null}
      </PageSection>
    </>
  )
}
