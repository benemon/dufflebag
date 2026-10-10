import { useState } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Checkbox, Content, DescriptionList,
  DescriptionListDescription, DescriptionListGroup, DescriptionListTerm, Flex, FlexItem, Form, FormGroup, Label,
  PageSection, TextInput,
} from '@patternfly/react-core'
import { useNavigate } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  createGithubImport, getDefaultPlatforms, githubRateLimit, listPluginVersions, resolveGithubRelease, sourceLabel,
  type GithubRelease,
} from '../data/pluginRegistry'
import { useTenant } from '../data/tenant'
import { pluginRegistryErrorMessage } from './Plugins'

export function PluginGithub() {
  const { state, selectedOrganization, signOut } = useAuth()
  const { tenant } = useTenant()
  const navigate = useNavigate()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? ''
  const token = state?.token ?? ''
  const [release, setRelease] = useState<GithubRelease | null>(null)
  const [mirroredVersions, setMirroredVersions] = useState<number | null>(null)
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
    // After a first import, a plugin's existing platforms are the default;
    // before it, the organization's (ADR-0027 A8).
    try {
      const held = await listPluginVersions(token, organizationID, resolved.name)
      setMirroredVersions(held.versions.length)
      setPreselected([...new Set(held.versions.flatMap((v) => v.stored_platforms.map((p) => `${p.os}_${p.arch}`)))])
    } catch {
      setMirroredVersions(0)
      setPreselected(await getDefaultPlatforms(token, organizationID))
    }
    setRelease(resolved)
  }, 'The release could not be resolved.')

  return (
    <PluginGithubView
      organizationName={tenant.organization}
      link={link} release={release} mirroredVersions={mirroredVersions} preselected={preselected}
      loading={busy && !release} failure={failure} busy={busy}
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
  organizationName, link, release, mirroredVersions, preselected, loading, failure, busy,
  onBackToRegistry, onBackToPlugins, onLinkChange, onResolve, onRefresh, onImport,
}: {
  organizationName: string
  link: string
  release: GithubRelease | null
  mirroredVersions: number | null
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
  const importable = Boolean(release && release.has_checksum && !release.held_by)
  const latest = /\/releases\/latest\/?$/.test(link.trim())
  const firstImport = !mirroredVersions
  const rateLimit = failure ? githubRateLimit(failure) : null
  const mono = { fontFamily: 'var(--pf-t--global--font--family--mono)' }
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
        description="Mirror a plugin from a public GitHub release. Binaries are verified against the release’s checksum file."
      />
      <PageSection variant="secondary" isFilled>
        {rateLimit ? (
          <Alert variant="warning" isInline title="GitHub rate limit reached">
            <Content component="p">
              GitHub refused the request: all 60 unauthenticated requests this hour are used.
              The limit resets at {rateLimit.resetsAt.toLocaleTimeString()}, in {Math.max(1, Math.round((rateLimit.resetsAt.getTime() - Date.now()) / 60_000))} minutes.
              Mirrored plugins are unaffected.
            </Content>
          </Alert>
        ) : null}
        <Card>
          <CardBody>
            <Form onSubmit={(event) => { event.preventDefault(); if (link.trim()) onResolve() }}>
              <FormGroup label="Release link" isRequired fieldId="github-release-link">
                <Flex gap={{ default: 'gapSm' }}>
                  <FlexItem flex={{ default: 'flex_1' }}>
                    <TextInput
                      id="github-release-link" aria-label="GitHub release link"
                      placeholder="https://github.com/OWNER/packer-plugin-NAME/releases/tag/v1.0.0"
                      value={link} onChange={(_event, value) => { setPlatforms(null); onLinkChange(value) }}
                    />
                  </FlexItem>
                  <FlexItem>
                    <Button variant="secondary" isDisabled={busy || link.trim() === '' || Boolean(rateLimit)} onClick={onResolve}>Resolve</Button>
                  </FlexItem>
                </Flex>
                <Content component="small">
                  A release tag, such as <code>…/releases/tag/v1.2.0</code>, or <code>…/releases/latest</code>. Latest is pinned to an exact version when you resolve it.
                </Content>
              </FormGroup>
            </Form>
          </CardBody>
        </Card>
        {loading ? (
          <PluginLoadingCard message="Loading GitHub release…" />
        ) : failure && !rateLimit ? (
          <PluginErrorCard title="GitHub release could not be loaded" error={failure} onRetry={onRefresh} />
        ) : null}
        {!loading && !failure && release ? (
          <>
            {!release.has_checksum ? (
              <Alert variant="danger" isInline title={`Not importable: ${release.tag} has no checksum file`}>
                <Content component="p">
                  The release has {release.platforms.length} {release.platforms.length === 1 ? 'binary' : 'binaries'} but no SHA256SUMS asset.
                  dufflebag verifies every binary against the release’s checksums, so it won’t import this release.
                </Content>
              </Alert>
            ) : null}
            {release.held_by ? (
              <Alert variant="danger" isInline title={`${release.name} is held from ${sourceLabel(release.held_by)}`}>
                <Content component="p">
                  In {organizationName}, {release.name} comes from {sourceLabel(release.held_by)}
                  {release.held_by.repository ? ` (${release.held_by.repository})` : ''}.
                  A name belongs to one source per organization, so this GitHub release is refused.
                  The name is freed once every {release.name} version is removed; revoked versions still count.
                </Content>
              </Alert>
            ) : null}
            <Card>
              <CardTitle>Inferred from the release</CardTitle>
              <CardBody>
                <DescriptionList isHorizontal isCompact>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Repository</DescriptionListTerm>
                    <DescriptionListDescription><span style={mono}>{release.repository}</span></DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Plugin name</DescriptionListTerm>
                    <DescriptionListDescription><span style={mono}>{release.name}</span> <Content component="small">from the repository name</Content></DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Version</DescriptionListTerm>
                    <DescriptionListDescription>
                      <span style={mono}>{release.version}</span>{' '}
                      <Content component="small">{latest ? `from /releases/latest, tag ${release.tag}` : `tag ${release.tag}`}{release.prerelease ? ' · prerelease' : ''}</Content>
                    </DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Released</DescriptionListTerm>
                    <DescriptionListDescription>{release.published_at ? new Date(release.published_at).toLocaleDateString() : 'Unknown'}</DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>Checksum file</DescriptionListTerm>
                    <DescriptionListDescription>
                      {release.checksum_asset
                        ? <span style={mono}>{release.checksum_asset}</span>
                        : <span style={{ color: 'var(--pf-t--global--color--status--danger--default)' }}>None found</span>}
                    </DescriptionListDescription>
                  </DescriptionListGroup>
                  <DescriptionListGroup>
                    <DescriptionListTerm>In dufflebag</DescriptionListTerm>
                    <DescriptionListDescription>
                      {release.held_by
                        ? <span style={{ color: 'var(--pf-t--global--color--status--danger--default)' }}>Held from {sourceLabel(release.held_by)}</span>
                        : firstImport ? 'New plugin' : `${mirroredVersions} ${mirroredVersions === 1 ? 'version' : 'versions'} mirrored`}
                    </DescriptionListDescription>
                  </DescriptionListGroup>
                </DescriptionList>
              </CardBody>
            </Card>
            {importable ? (
              <Card>
                <CardTitle>
                  Platforms{' '}
                  <Content component="small">
                    · {release.platforms.length} in the release · {firstImport
                      ? `first import, so ${organizationName}’s default platforms are preselected`
                      : `the architectures ${release.name} already has are preselected`}
                  </Content>
                </CardTitle>
                <CardBody>
                  <Flex gap={{ default: 'gapMd' }} flexWrap={{ default: 'wrap' }}>
                    {release.platforms.map((platform) => (
                      <FlexItem key={platform}>
                        <Checkbox
                          id={`github-platform-${platform}`}
                          label={<>{platform} {preselected.includes(platform) ? <Label isCompact variant="outline">{firstImport ? 'org default' : 'existing'}</Label> : null}</>}
                          isChecked={chosen.includes(platform)}
                          onChange={() => setPlatforms(chosen.includes(platform) ? chosen.filter((p) => p !== platform) : [...chosen, platform])}
                        />
                      </FlexItem>
                    ))}
                  </Flex>
                  <Flex gap={{ default: 'gapSm' }} style={{ marginTop: 16 }}>
                    <Button variant="primary" isLoading={busy} isDisabled={busy || chosen.length === 0} onClick={() => onImport(chosen)}>
                      Import {release.tag} · {chosen.length} {chosen.length === 1 ? 'platform' : 'platforms'}
                    </Button>
                    <Button variant="link" isDisabled={busy} onClick={onBackToPlugins}>Cancel</Button>
                  </Flex>
                </CardBody>
              </Card>
            ) : null}
          </>
        ) : null}
      </PageSection>
    </>
  )
}
