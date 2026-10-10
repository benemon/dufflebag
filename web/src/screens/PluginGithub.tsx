import { useState } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Checkbox, Content, PageSection, TextInput, Title,
} from '@patternfly/react-core'
import { useNavigate } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  createGithubImport, getDefaultPlatforms, listPluginVersions, resolveGithubRelease, sourceLabel, type GithubRelease,
} from '../data/pluginRegistry'
import { pluginRegistryErrorMessage } from './Plugins'

export function PluginGithub() {
  const { state, selectedOrganization, signOut } = useAuth()
  const navigate = useNavigate()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? ''
  const token = state?.token ?? ''
  const [release, setRelease] = useState<GithubRelease | null>(null)
  const [preselected, setPreselected] = useState<string[]>([])
  const [failure, setFailure] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [link, setLink] = useState('')

  const guard = async (work: () => Promise<void>, fallback: string) => {
    setBusy(true)
    setFailure(null)
    try {
      await work()
    } catch (error: unknown) {
      if (!signOutIfUnauthorized(error, signOut)) setFailure(pluginRegistryErrorMessage(error, fallback))
    } finally {
      setBusy(false)
    }
  }

  const resolve = () => guard(async () => {
    const resolved = await resolveGithubRelease(token, organizationID, link)
    try {
      const held = await listPluginVersions(token, organizationID, resolved.name)
      setPreselected([...new Set(held.versions.flatMap((v) => v.stored_platforms.map((p) => `${p.os}_${p.arch}`)))])
    } catch {
      setPreselected(await getDefaultPlatforms(token, organizationID))
    }
    setRelease(resolved)
  }, 'The release could not be resolved.')

  return (
    <PluginGithubView
      link={link} release={release} preselected={preselected} loading={busy && !release} failure={failure} busy={busy}
      onBackToRegistry={() => navigate('/buckets')}
      onBackToPlugins={() => navigate('/plugin-registry')}
      onLinkChange={(value) => { setLink(value); setRelease(null) }}
      onResolve={() => void resolve()}
      onRefresh={() => void resolve()}
      onImport={(platforms) => void guard(async () => {
        if (!release) return
        const job = await createGithubImport(token, organizationID, release.repository, release.tag, platforms)
        navigate(`/plugin-registry/imports/${job.id}`)
      }, 'The import could not be queued.')}
    />
  )
}

export function PluginGithubView({
  link, release, preselected, loading, failure, busy,
  onBackToRegistry, onBackToPlugins, onLinkChange, onResolve, onRefresh, onImport,
}: {
  link: string
  release: GithubRelease | null
  preselected: string[]
  loading: boolean
  failure: string | null
  busy: boolean
  onBackToRegistry: () => void
  onBackToPlugins: () => void
  onLinkChange: (value: string) => void
  onResolve: () => void
  onRefresh: () => void | Promise<void>
  onImport: (platforms: string[]) => void
}) {
  const [platforms, setPlatforms] = useState<string[] | null>(null)
  const chosen = platforms ?? preselected.filter((p) => release?.platforms.includes(p))
  const importable = release && release.has_checksum && !release.held_by
  const latest = /\/releases\/latest\/?$/.test(link.trim())
  return (
    <>
      <ScreenHeader
        breadcrumbs={(
          <Breadcrumb>
            <BreadcrumbItem component="button" onClick={onBackToRegistry}>Registry</BreadcrumbItem>
            <BreadcrumbItem component="button" onClick={onBackToPlugins}>Plugins</BreadcrumbItem>
            <BreadcrumbItem isActive>Import from GitHub</BreadcrumbItem>
          </Breadcrumb>
        )}
        title="Import from GitHub"
        description="Paste a public packer-plugin release link. dufflebag shows what it found before anything is imported. GitHub releases carry no signing key dufflebag can check; a signature they include is kept as published."
      />
      <PageSection variant="secondary" isFilled>
        {loading ? (
          <PluginLoadingCard message="Loading GitHub release…" />
        ) : failure ? (
          <PluginErrorCard title="GitHub release could not be loaded" error={failure} onRetry={onRefresh} />
        ) : (
          <>
            <TextInput
              aria-label="GitHub release link" placeholder="https://github.com/<owner>/packer-plugin-<name>/releases/tag/<tag>"
              value={link} onChange={(_event, value) => { setPlatforms(null); onLinkChange(value) }}
            />
            <Button variant="secondary" isDisabled={busy || link.trim() === ''} onClick={onResolve}>Resolve</Button>
          </>
        )}
        {!loading && !failure && release ? (
          <>
            <Title headingLevel="h2" size="md">{release.name} · {release.version}</Title>
            <Content component="p">
              From {release.repository}, tag {release.tag}{latest ? ', the latest release now. The import uses this tag even if a newer release appears.' : '.'}
            </Content>
            {!release.has_checksum ? (
              <Alert variant="danger" isInline title="This release can't be imported">
                <Content component="p">It has no SHA256SUMS asset, so its zips can neither be verified nor served to Packer.</Content>
              </Alert>
            ) : null}
            {release.held_by ? (
              <Alert variant="danger" isInline title={`${release.name} is held by another source`}>
                <Content component="p">
                  This organization's {release.name} comes from {sourceLabel(release.held_by)}{release.held_by.repository ? ` (${release.held_by.repository})` : ''}.
                  A name belongs to one source; it is freed once every {release.name} version is removed, and revoked versions still count.
                </Content>
              </Alert>
            ) : null}
            {importable ? (
              <>
                <Title headingLevel="h3" size="md">Platforms</Title>
                {release.platforms.map((platform) => (
                  <Checkbox
                    key={platform} id={`github-platform-${platform}`} label={platform}
                    isChecked={chosen.includes(platform)}
                    onChange={() => setPlatforms(chosen.includes(platform) ? chosen.filter((p) => p !== platform) : [...chosen, platform])}
                  />
                ))}
                <Button variant="primary" isLoading={busy} isDisabled={busy || chosen.length === 0} onClick={() => onImport(chosen)}>
                  Import {release.name} {release.version} · {chosen.length} platforms
                </Button>
              </>
            ) : null}
          </>
        ) : null}
      </PageSection>
    </>
  )
}
