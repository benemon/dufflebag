import { useCallback, useEffect, useState } from 'react'
import {
  Alert, Button, ClipboardCopyButton, CodeBlock, CodeBlockAction, CodeBlockCode, Content, Label,
  PageSection, Spinner, Title,
} from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate, useParams } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  getPluginRegistry, listPluginVersions, sourceLabel, templateStanza,
  type PluginRegistry, type PluginVersions,
} from '../data/pluginRegistry'
import { useTenant } from '../data/tenant'
import { pluginRegistryErrorMessage } from './Plugins'

export function PluginDetail() {
  const { name = '' } = useParams()
  const { state, self, selectedOrganization, signOut } = useAuth()
  const { tenant } = useTenant()
  const navigate = useNavigate()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? null
  const token = state?.token ?? ''
  const [registry, setRegistry] = useState<PluginRegistry | null>(null)
  const [detail, setDetail] = useState<PluginVersions | null>(null)
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)

  const reload = useCallback(async () => {
    if (!organizationID || token === '') return
    setLoading(true)
    try {
      const [nextRegistry, nextDetail] = await Promise.all([
        getPluginRegistry(token, organizationID), listPluginVersions(token, organizationID, name),
      ])
      setRegistry(nextRegistry)
      setDetail(nextDetail)
      setFailure(null)
    } catch (error: unknown) {
      if (signOutIfUnauthorized(error, signOut)) return
      setFailure(pluginRegistryErrorMessage(error, 'The plugin could not be loaded.'))
    } finally {
      setLoading(false)
    }
  }, [name, organizationID, signOut, token])

  useEffect(() => { void reload() }, [reload])

  return (
    <PluginDetailView
      name={name} organizationName={tenant.organization} host={window.location.host}
      callerRole={self?.role ?? null} registry={registry} detail={detail}
      loading={loading} failure={failure} onRefresh={reload}
      onUpload={() => navigate('/plugins/upload')}
    />
  )
}

export function PluginDetailView({
  name, organizationName, host, callerRole, registry, detail, loading, failure, onRefresh, onUpload,
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
}) {
  const newest = detail?.versions.find((version) => !version.revoked)
  const platforms = [...new Set(detail?.versions.flatMap((version) =>
    [...version.listed_platforms, ...version.stored_platforms].map((p) => `${p.os}_${p.arch}`)) ?? [])].sort()
  return (
    <>
      <ScreenHeader
        title={name}
        description={detail ? <Label isCompact>{sourceLabel(detail.source)}</Label> : undefined}
        actions={detail?.source.kind === 'upload' && permitsAction(callerRole, 'publishPlugin')
          ? <Button variant="secondary" onClick={onUpload}>Upload version</Button> : undefined}
        onRefresh={onRefresh} refreshing={loading}
      />
      <PageSection variant="secondary" isFilled>
        {failure ? <Alert variant="danger" isInline title="The plugin could not be loaded"><Content component="p">{failure}</Content></Alert> : null}
        {loading && !detail ? <><Spinner aria-label="Loading plugin…" /><Content component="p">Loading plugin…</Content></> : null}
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
              ● Mirrored · ○ Listed in SHA256SUMS, not uploaded, so Packer on that platform gets an older
              version or none · – Not published
            </Content>
            <Table aria-label="Versions by platform" variant="compact">
              <Thead>
                <Tr><Th>Version</Th>{platforms.map((platform) => <Th key={platform}>{platform}</Th>)}</Tr>
              </Thead>
              <Tbody>
                {detail.versions.map((version) => {
                  const stored = new Set(version.stored_platforms.map((p) => `${p.os}_${p.arch}`))
                  const listed = new Set(version.listed_platforms.map((p) => `${p.os}_${p.arch}`))
                  return (
                    <Tr key={version.version}>
                      <Td dataLabel="Version">
                        {version.version} {version.revoked ? <Label isCompact color="grey">Revoked</Label> : null}
                      </Td>
                      {platforms.map((platform) => (
                        <Td key={platform} dataLabel={platform}>
                          {stored.has(platform) ? '●' : listed.has(platform) ? '○' : '–'}
                        </Td>
                      ))}
                    </Tr>
                  )
                })}
              </Tbody>
            </Table>
          </>
        ) : null}
      </PageSection>
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

export function TemplateStanzaBlock({ hcl }: { hcl: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <CodeBlock actions={
      <CodeBlockAction>
        <ClipboardCopyButton
          id="template-stanza-copy" textId="template-stanza" aria-label="Copy template stanza"
          exitDelay={copied ? 1500 : 600} variant="plain"
          onClick={() => { void navigator.clipboard.writeText(hcl); setCopied(true) }}
          onTooltipHidden={() => setCopied(false)}
        >{copied ? 'Copied' : 'Copy'}</ClipboardCopyButton>
      </CodeBlockAction>
    }>
      <CodeBlockCode id="template-stanza">{hcl}</CodeBlockCode>
    </CodeBlock>
  )
}
