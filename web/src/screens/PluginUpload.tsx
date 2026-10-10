import { useCallback, useEffect, useState, type DragEvent } from 'react'
import {
  Alert, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Content, Flex, FlexItem, Label, PageSection,
  Title,
} from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  getPluginRegistry, listPluginVersions, planPluginUploads, publishPluginVersion, sourceLabel,
  type PluginRegistry, type PluginVersions, type TemplateStanza, type UploadPlan,
} from '../data/pluginRegistry'
import { useTenant } from '../data/tenant'
import { NotExposedAlert, TemplateStanzaBlock } from './PluginDetail'
import { pluginRegistryErrorMessage } from './Plugins'

export type UploadOutcome =
  | { status: 'sending' }
  | { status: 'published'; stanza: TemplateStanza }
  | { status: 'refused'; message: string }

const key = (name: string, version: string) => `${name} ${version}`

export function PluginUpload() {
  const { state, selectedOrganization, signOut } = useAuth()
  const { tenant } = useTenant()
  const navigate = useNavigate()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? null
  const token = state?.token ?? ''
  const [registry, setRegistry] = useState<PluginRegistry | null>(null)
  const [plan, setPlan] = useState<UploadPlan | null>(null)
  const [held, setHeld] = useState<Record<string, PluginVersions>>({})
  const [outcomes, setOutcomes] = useState<Record<string, UploadOutcome>>({})
  const [busy, setBusy] = useState(false)
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)

  const reload = useCallback(async () => {
    if (!organizationID || token === '') {
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      setRegistry(await getPluginRegistry(token, organizationID))
      setFailure(null)
    } catch (error: unknown) {
      if (!signOutIfUnauthorized(error, signOut)) setFailure(pluginRegistryErrorMessage(error, 'The plugin registry could not be loaded.'))
    } finally {
      setLoading(false)
    }
  }, [organizationID, signOut, token])

  useEffect(() => { void reload() }, [reload])

  // What the registry already holds under each planned name decides whether a
  // version is new, gains architectures, or is refused for a held name.
  const choose = async (files: File[]) => {
    setOutcomes({})
    setFailure(null)
    const next = files.length ? planPluginUploads(files) : null
    setPlan(next)
    if (!next || !organizationID) return
    const found: Record<string, PluginVersions> = {}
    for (const name of new Set(next.versions.map((v) => v.name))) {
      try {
        found[name] = await listPluginVersions(token, organizationID, name)
      } catch (error: unknown) {
        if (signOutIfUnauthorized(error, signOut)) return
      }
    }
    setHeld(found)
  }

  return (
    <PluginUploadView
      organizationName={tenant.organization}
      registry={registry} plan={plan} held={held} outcomes={outcomes} busy={busy} loading={loading} failure={failure}
      onBackToRegistry={() => navigate('/buckets')}
      onBackToPlugins={() => navigate('/plugin-registry')}
      onRefresh={reload}
      onChoose={(files) => void choose(files)}
      onOpen={(name) => navigate(`/plugin-registry/${encodeURIComponent(name)}`)}
      onSubmit={async () => {
        if (!plan || !organizationID) return
        setBusy(true)
        try {
          for (const upload of plan.versions) {
            const id = key(upload.name, upload.version)
            setOutcomes((current) => ({ ...current, [id]: { status: 'sending' } }))
            try {
              const published = await publishPluginVersion(token, organizationID, upload)
              setOutcomes((current) => ({ ...current, [id]: { status: 'published', stanza: published.stanza } }))
            } catch (error: unknown) {
              if (signOutIfUnauthorized(error, signOut)) return
              const message = pluginRegistryErrorMessage(error, 'The upload failed.')
              setOutcomes((current) => ({ ...current, [id]: { status: 'refused', message } }))
            }
          }
        } finally {
          setBusy(false)
        }
      }}
    />
  )
}

function megabytes(size: number): string {
  return size >= 1_000_000 ? `${(size / 1_000_000).toFixed(1)} MB` : `${Math.max(1, Math.round(size / 1000))} KB`
}

export function PluginUploadView({
  organizationName, registry, plan, held, outcomes, busy, loading, failure,
  onBackToRegistry, onBackToPlugins, onRefresh, onChoose, onOpen, onSubmit,
}: {
  organizationName: string
  registry: PluginRegistry | null
  plan: UploadPlan | null
  held: Record<string, PluginVersions>
  outcomes: Record<string, UploadOutcome>
  busy: boolean
  loading: boolean
  failure: string | null
  onBackToRegistry: () => void
  onBackToPlugins: () => void
  onRefresh: () => void | Promise<void>
  onChoose: (files: File[]) => void
  onOpen: (name: string) => void
  onSubmit: () => void | Promise<void>
}) {
  const [dragging, setDragging] = useState(false)
  const enabled = registry?.enabled ?? false
  const versions = plan?.versions ?? []
  const refused = plan?.refused ?? []
  const names = [...new Set(versions.map((v) => v.name))]
  const heldElsewhere = names.filter((name) => held[name] && held[name].source.kind !== 'upload')
  const sent = versions.some((upload) => outcomes[key(upload.name, upload.version)])
  const fileCount = versions.reduce((n, upload) => n + upload.files.length, 0)
  const published = versions.flatMap((upload) => {
    const outcome = outcomes[key(upload.name, upload.version)]
    return outcome?.status === 'published' ? [{ name: upload.name, stanza: outcome.stanza }] : []
  })
  const drop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault()
    setDragging(false)
    onChoose([...event.dataTransfer.files])
  }
  return (
    <>
      <ScreenHeader
        breadcrumbs={(
          <Breadcrumb>
            <BreadcrumbItem component="button" onClick={onBackToRegistry}>Registry</BreadcrumbItem>
            <BreadcrumbItem component="button" onClick={onBackToPlugins}>Plugins</BreadcrumbItem>
            <BreadcrumbItem isActive>Upload</BreadcrumbItem>
          </Breadcrumb>
        )}
        title="Upload plugin"
        description="Upload plugin binaries built in-house. Uploaded plugins have no upstream, so update checks don’t apply."
      />
      <PageSection variant="secondary" isFilled>
        {loading ? (
          <PluginLoadingCard message="Loading upload…" />
        ) : failure ? (
          <PluginErrorCard title="Upload could not be loaded" error={failure} onRetry={onRefresh} />
        ) : (
          <>
            {registry && !enabled ? (
              <Alert variant="warning" isInline title="The plugin registry isn’t enabled">
                <Content component="p">Nothing is stored before the registry is enabled. Enable it in Registry settings before uploading.</Content>
                <Button component="a" variant="link" isInline href="/plugin-registry/settings">Registry settings</Button>
              </Alert>
            ) : null}
            {heldElsewhere.map((name) => {
              const source = held[name]?.source
              if (!source) return null
              return (
                <Alert key={name} variant="danger" isInline title={`${name} is held from ${sourceLabel(source)}`}>
                  <Content component="p">
                    In {organizationName}, {name} comes from {sourceLabel(source)}{source.repository ? ` (${source.repository})` : ''}.
                    A name belongs to one source per organization, so these uploads are refused.
                    The name is freed once every {name} version is removed; revoked versions still count.
                  </Content>
                </Alert>
              )
            })}
            <Card>
              <CardBody>
                <div
                  onDragOver={(event) => { event.preventDefault(); setDragging(true) }}
                  onDragLeave={() => setDragging(false)} onDrop={drop}
                  style={{
                    border: `2px dashed ${dragging ? 'var(--pf-t--global--color--brand--default)' : 'var(--pf-t--global--border--color--default)'}`,
                    borderRadius: 8, padding: 24, textAlign: 'center',
                  }}
                >
                  <Title headingLevel="h2" size="md">Drag plugin files here</Title>
                  <Content component="p">
                    Files named <code>packer-plugin-NAME_vVERSION_xAPI_OS_ARCH.zip</code> with each version’s SHA256SUMS.
                    One version or several; they’re grouped before anything is sent.
                  </Content>
                  <input
                    aria-label="Choose plugin files" type="file" multiple disabled={!enabled || busy}
                    onChange={(event) => onChoose([...(event.target.files ?? [])])}
                  />
                </div>
              </CardBody>
            </Card>
            {plan ? (
              <>
                <Flex gap={{ default: 'gapMd' }} alignItems={{ default: 'alignItemsBaseline' }}>
                  <Title headingLevel="h2" size="lg">{names.join(', ') || 'No plugin'} · {versions.length} {versions.length === 1 ? 'version' : 'versions'}</Title>
                  <Content component="small">
                    grouped from {fileCount + refused.reduce((n, r) => n + r.files.length, 0)} files; {sent ? `${fileCount} sent` : 'nothing has been sent yet'}
                  </Content>
                </Flex>
                {versions.map((upload) => {
                  const id = key(upload.name, upload.version)
                  const outcome = outcomes[id]
                  const heldHere = held[upload.name]
                  const refusedName = Boolean(heldHere && heldHere.source.kind !== 'upload')
                  const existing = heldHere?.source.kind === 'upload' ? heldHere.versions.find((v) => v.version === upload.version) : undefined
                  const stored = new Set(existing?.stored_platforms.map((p) => `${p.os}_${p.arch}`) ?? [])
                  const status = (platform?: string) => {
                    if (refusedName) return <span style={{ color: 'var(--pf-t--global--color--status--danger--default)' }}>Not sent</span>
                    if (outcome?.status === 'published') return 'Sent'
                    if (!existing) return 'New'
                    if (platform) return stored.has(platform) ? 'Already mirrored' : 'New architecture'
                    return 'Already mirrored'
                  }
                  return (
                    <Card key={id}>
                      <CardTitle>
                        <Flex gap={{ default: 'gapSm' }} alignItems={{ default: 'alignItemsCenter' }}>
                          <FlexItem><code>{upload.version}</code></FlexItem>
                          <FlexItem>
                            {refusedName || outcome?.status === 'refused' ? <Label isCompact color="red">Refused</Label>
                              : outcome?.status === 'sending' ? <Label isCompact color="blue">Sending</Label>
                              : outcome?.status === 'published' ? <Label isCompact color="green">Imported</Label>
                              : existing ? <Label isCompact color="green">{existing.revoked ? 'Revoked' : 'Available'}</Label>
                              : <Label isCompact color="blue">New version</Label>}
                          </FlexItem>
                          <FlexItem>
                            <Content component="small">
                              {refusedName && heldHere ? `name held from ${sourceLabel(heldHere.source)}`
                                : outcome?.status === 'refused' ? outcome.message
                                : existing ? 'mirrored · architectures are added, never replaced'
                                : `${upload.files.length} ${upload.files.length === 1 ? 'file' : 'files'}`}
                            </Content>
                          </FlexItem>
                          {outcome?.status === 'published' ? (
                            <FlexItem align={{ default: 'alignRight' }}>
                              <Button variant="link" isInline onClick={() => onOpen(upload.name)}>Open {upload.name}</Button>
                            </FlexItem>
                          ) : null}
                        </Flex>
                      </CardTitle>
                      <CardBody>
                        <Table aria-label={`Files for ${upload.name} ${upload.version}`} variant="compact" borders={false}>
                          <Thead><Tr><Th>File</Th><Th>Platform</Th><Th>Size</Th><Th>Status</Th></Tr></Thead>
                          <Tbody>
                            {upload.files.map(({ file, platform }) => (
                              <Tr key={file.name}>
                                <Td dataLabel="File"><code>{file.name}</code></Td>
                                <Td dataLabel="Platform">{platform ?? '—'}</Td>
                                <Td dataLabel="Size">{megabytes(file.size)}</Td>
                                <Td dataLabel="Status">{status(platform)}</Td>
                              </Tr>
                            ))}
                          </Tbody>
                        </Table>
                      </CardBody>
                    </Card>
                  )
                })}
                {refused.length ? (
                  <Card>
                    <CardTitle>
                      <Flex gap={{ default: 'gapSm' }} alignItems={{ default: 'alignItemsCenter' }}>
                        <FlexItem>Not sent</FlexItem>
                        <FlexItem><Label isCompact color="grey">{refused.reduce((n, r) => n + r.files.length, 0)} {refused.reduce((n, r) => n + r.files.length, 0) === 1 ? 'file' : 'files'}</Label></FlexItem>
                      </Flex>
                    </CardTitle>
                    <CardBody>
                      <Table aria-label="Files not sent" variant="compact" borders={false}>
                        <Thead><Tr><Th>File</Th><Th>Platform</Th><Th>Size</Th><Th>Status</Th></Tr></Thead>
                        <Tbody>
                          {refused.flatMap((refusal) => refusal.files.map((file) => (
                            <Tr key={`${refusal.label}-${file.name}`}>
                              <Td dataLabel="File"><code>{file.name}</code> <Content component="small">{refusal.reason}</Content></Td>
                              <Td dataLabel="Platform">—</Td>
                              <Td dataLabel="Size">{megabytes(file.size)}</Td>
                              <Td dataLabel="Status">Skipped</Td>
                            </Tr>
                          )))}
                        </Tbody>
                      </Table>
                    </CardBody>
                  </Card>
                ) : null}
                {!sent ? (
                  <Flex gap={{ default: 'gapSm' }}>
                    <Button variant="primary" isLoading={busy} isDisabled={busy || !enabled || versions.length === 0 || heldElsewhere.length > 0} onClick={() => void onSubmit()}>
                      {versions.length ? `Upload ${fileCount} ${fileCount === 1 ? 'file' : 'files'} · ${versions.length} ${versions.length === 1 ? 'version' : 'versions'}` : 'Upload'}
                    </Button>
                    <Button variant="link" isDisabled={busy} onClick={onBackToPlugins}>Cancel</Button>
                  </Flex>
                ) : null}
              </>
            ) : null}
            {published.length ? (
              <Card>
                <CardTitle>Template stanza</CardTitle>
                <CardBody>
                  <NotExposedAlert registry={registry} />
                  {published.map(({ name, stanza }) => (
                    <TemplateStanzaBlock key={`${name} ${stanza.version}`} id={`stanza-${name}-${stanza.version}`} hcl={stanza.hcl} />
                  ))}
                </CardBody>
              </Card>
            ) : null}
          </>
        )}
      </PageSection>
    </>
  )
}
