import { useCallback, useEffect, useState } from 'react'
import {
  Breadcrumb, BreadcrumbItem, Button, Checkbox, Content, Label, PageSection, Spinner, Switch, Title,
} from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  createPluginImport, getDefaultPlatforms, listHashicorpPluginVersions, listHashicorpPlugins, listPluginVersions,
  sourceLabel, type HashicorpPlugin, type HashicorpPluginVersion,
} from '../data/pluginRegistry'
import { pluginRegistryErrorMessage } from './Plugins'

export function PluginHashicorp() {
  const { state, selectedOrganization, signOut } = useAuth()
  const navigate = useNavigate()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? ''
  const token = state?.token ?? ''
  const [plugins, setPlugins] = useState<HashicorpPlugin[] | null>(null)
  const [selected, setSelected] = useState<HashicorpPlugin | null>(null)
  const [versions, setVersions] = useState<HashicorpPluginVersion[] | null>(null)
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
    await guard(async () => {
      const page = await listHashicorpPluginVersions(token, organizationID, plugin.product)
      setVersions(page.versions)
      setNext(page.next)
      // After a first import, a plugin's existing platforms are the default;
      // before it, the organization's (ADR-0027 A8).
      if (plugin.mirrored_versions > 0) {
        const held = await listPluginVersions(token, organizationID, plugin.name)
        setPreselected([...new Set(held.versions.flatMap((v) => v.stored_platforms.map((p) => `${p.os}_${p.arch}`)))])
      } else {
        setPreselected(await getDefaultPlatforms(token, organizationID))
      }
    }, 'The plugin\'s releases could not be loaded.')
  }

  return (
    <PluginHashicorpView
      plugins={plugins} selected={selected} versions={versions} hasMore={next !== undefined}
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

export function PluginHashicorpView({
  plugins, selected, versions, hasMore, preselected, loading, failure, busy,
  onBackToRegistry, onBackToPlugins, onRefresh, onChoose, onMore, onImport,
}: {
  plugins: HashicorpPlugin[] | null
  selected: HashicorpPlugin | null
  versions: HashicorpPluginVersion[] | null
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
  const [chosen, setChosen] = useState<string[]>([])
  const [platforms, setPlatforms] = useState<string[] | null>(null)
  useEffect(() => { setChosen([]); setPlatforms(null) }, [selected])
  const shownVersions = (versions ?? []).filter((v) => showPrereleases || !v.prerelease)
  const available = [...new Set(shownVersions.filter((v) => chosen.includes(v.version)).flatMap((v) => v.platforms))].sort()
  const selectedPlatforms = platforms ?? preselected.filter((p) => available.includes(p))
  const toggle = (list: string[], value: string) => (list.includes(value) ? list.filter((v) => v !== value) : [...list, value])
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
        description="Packer plugins published on releases.hashicorp.com. Each imported version's SHA256SUMS is verified against HashiCorp's signing key before anything is stored."
      />
      <PageSection variant="secondary" isFilled>
        {loading ? (
          <PluginLoadingCard message="Reading releases.hashicorp.com…" />
        ) : failure ? (
          <PluginErrorCard title="HashiCorp's plugin list could not be read" error={failure} onRetry={onRefresh} />
        ) : null}
        {!loading && !failure && plugins ? (
          <Table aria-label="HashiCorp plugins" variant="compact">
            <Thead><Tr><Th>Plugin</Th><Th>Mirrored versions</Th><Th>Status</Th></Tr></Thead>
            <Tbody>
              {plugins.map((plugin) => (
                <Tr key={plugin.product} isRowSelected={selected?.product === plugin.product}>
                  <Td dataLabel="Plugin">
                    <Button variant="link" isInline isDisabled={Boolean(plugin.held_by)} onClick={() => onChoose(plugin)}>{plugin.name}</Button>
                  </Td>
                  <Td dataLabel="Mirrored versions">{plugin.mirrored_versions}</Td>
                  <Td dataLabel="Status">
                    {plugin.held_by
                      ? `Held by ${sourceLabel(plugin.held_by)}${plugin.held_by.repository ? ` (${plugin.held_by.repository})` : ''}. A name belongs to one source; it is freed once every version is removed.`
                      : null}
                  </Td>
                </Tr>
              ))}
            </Tbody>
          </Table>
        ) : null}
        {!loading && !failure && selected ? (
          <>
            <Title headingLevel="h2" size="md">{selected.name}</Title>
            <Switch id="show-prereleases" label="Show prereleases" isChecked={showPrereleases} onChange={(_e, value) => setShowPrereleases(value)} />
            {versions === null ? <Spinner aria-label="Loading releases…" /> : (
              <Table aria-label={`${selected.name} releases`} variant="compact">
                <Thead><Tr><Th screenReaderText="Select" /><Th>Version</Th><Th>Released</Th><Th>State</Th><Th>Platforms</Th></Tr></Thead>
                <Tbody>
                  {shownVersions.map((version) => (
                    <Tr key={version.version}>
                      <Td dataLabel="Select">
                        <Checkbox
                          id={`version-${version.version}`} aria-label={`Select ${version.version}`}
                          isChecked={version.mirrored || chosen.includes(version.version)} isDisabled={version.mirrored}
                          onChange={() => setChosen(toggle(chosen, version.version))}
                        />
                      </Td>
                      <Td dataLabel="Version">
                        {version.version} {version.mirrored ? <Label isCompact color="green">Mirrored</Label> : null}
                        {version.prerelease ? <Label isCompact color="orange">Prerelease</Label> : null}
                      </Td>
                      <Td dataLabel="Released">{version.created_at.slice(0, 10)}</Td>
                      <Td dataLabel="State">{version.state ?? ''}</Td>
                      <Td dataLabel="Platforms">{version.platforms.length}</Td>
                    </Tr>
                  ))}
                </Tbody>
              </Table>
            )}
            {hasMore ? <Button variant="link" onClick={onMore}>Load older releases</Button> : null}
            {chosen.length ? (
              <>
                <Title headingLevel="h3" size="md">Platforms</Title>
                <Content component="small">
                  {selected.mirrored_versions > 0 ? `Preselected: the platforms ${selected.name} already has.` : 'Preselected: the organization\'s default platforms.'}
                </Content>
                {available.map((platform) => (
                  <Checkbox
                    key={platform} id={`platform-${platform}`} label={platform}
                    isChecked={selectedPlatforms.includes(platform)}
                    onChange={() => setPlatforms(toggle(selectedPlatforms, platform))}
                  />
                ))}
                <Button
                  variant="primary" isLoading={busy} isDisabled={busy || selectedPlatforms.length === 0}
                  onClick={() => void onImport(chosen, selectedPlatforms)}
                >
                  Import {chosen.length === 1 ? `${selected.name} ${chosen[0]}` : `${chosen.length} versions`} · {selectedPlatforms.length} platforms
                </Button>
              </>
            ) : null}
          </>
        ) : null}
      </PageSection>
    </>
  )
}
