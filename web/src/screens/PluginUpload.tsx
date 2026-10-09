import { useEffect, useState } from 'react'
import { Alert, Button, Card, CardBody, Content, PageSection, Title } from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  getPluginRegistry, planPluginUpload, publishPluginVersion,
  type PluginRegistry, type PublishedPluginVersion, type UploadPlan,
} from '../data/pluginRegistry'
import { NotExposedAlert, TemplateStanzaBlock } from './PluginDetail'
import { pluginRegistryErrorMessage } from './Plugins'

export function PluginUpload() {
  const { state, selectedOrganization, signOut } = useAuth()
  const navigate = useNavigate()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? null
  const token = state?.token ?? ''
  const [registry, setRegistry] = useState<PluginRegistry | null>(null)
  const [plan, setPlan] = useState<UploadPlan | null>(null)
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)
  const [result, setResult] = useState<PublishedPluginVersion | null>(null)

  useEffect(() => {
    if (!organizationID || token === '') return
    getPluginRegistry(token, organizationID).then(setRegistry).catch((error: unknown) => {
      if (!signOutIfUnauthorized(error, signOut)) setFailure(pluginRegistryErrorMessage(error, 'The plugin registry could not be loaded.'))
    })
  }, [organizationID, signOut, token])

  return (
    <PluginUploadView
      registry={registry} plan={plan} busy={busy} failure={failure} result={result}
      onChoose={(files) => { setResult(null); setFailure(null); setPlan(files.length ? planPluginUpload(files) : null) }}
      onOpen={(name) => navigate(`/plugins/${encodeURIComponent(name)}`)}
      onSubmit={async () => {
        if (plan?.kind !== 'ready' || !organizationID) return
        setBusy(true)
        setFailure(null)
        try {
          setResult(await publishPluginVersion(token, organizationID, plan))
          setPlan(null)
        } catch (error: unknown) {
          if (signOutIfUnauthorized(error, signOut)) return
          setFailure(pluginRegistryErrorMessage(error, 'The upload failed.'))
        } finally {
          setBusy(false)
        }
      }}
    />
  )
}

function describeFile(field: string, platform?: string): string {
  if (field === 'zips') return platform ?? 'zip'
  return { sha256sums: 'SHA256SUMS', sha256sums_sig: 'Signature', manifest: 'Manifest' }[field] ?? field
}

function megabytes(size: number): string {
  return `${(size / 1_000_000).toFixed(1)} MB`
}

export function PluginUploadView({ registry, plan, busy, failure, result, onChoose, onOpen, onSubmit }: {
  registry: PluginRegistry | null
  plan: UploadPlan | null
  busy: boolean
  failure: string | null
  result: PublishedPluginVersion | null
  onChoose: (files: File[]) => void
  onOpen: (name: string) => void
  onSubmit: () => void | Promise<void>
}) {
  const enabled = registry?.enabled ?? false
  return (
    <>
      <ScreenHeader
        title="Upload plugin"
        description="Upload one version of a plugin built in-house: its SHA256SUMS file, the zips, and its signature or manifest if it has them."
      />
      <PageSection variant="secondary" isFilled>
        {registry && !enabled ? (
          <Alert variant="warning" isInline title="The plugin registry isn't enabled">
            <Content component="p">Enable the registry on the Plugins screen before uploading.</Content>
          </Alert>
        ) : null}
        {failure ? <Alert variant="danger" isInline title="The upload was refused"><Content component="p">{failure}</Content></Alert> : null}
        {result ? (
          <>
            <Alert variant="success" isInline title={`Published ${result.stanza.source} ${result.version.version}`}>
              <Button variant="link" isInline onClick={() => onOpen(result.stanza.source.split('/').pop() ?? '')}>Open the plugin</Button>
            </Alert>
            <Title headingLevel="h2" size="md">Template stanza</Title>
            <NotExposedAlert registry={registry} />
            <TemplateStanzaBlock hcl={result.stanza.hcl} />
          </>
        ) : null}
        <Card>
          <CardBody>
            <Content component="p">
              Files named packer-plugin-NAME_vVERSION_xAPI_OS_ARCH.zip or packer-plugin-NAME_VERSION_OS_ARCH.zip,
              with the version's SHA256SUMS. Zips the SHA256SUMS lists but you leave out are not served.
            </Content>
            <input
              aria-label="Choose plugin files" type="file" multiple disabled={!enabled || busy}
              onChange={(event) => onChoose([...(event.target.files ?? [])])}
            />
          </CardBody>
        </Card>
        {plan?.kind === 'refused' ? <Alert variant="danger" isInline title="These files can't be uploaded together"><Content component="p">{plan.reason}</Content></Alert> : null}
        {plan?.kind === 'ready' ? (
          <>
            <Title headingLevel="h2" size="md">{plan.name} · {plan.version}</Title>
            <Table aria-label="Files to upload" variant="compact">
              <Thead><Tr><Th>File</Th><Th>Kind</Th><Th>Size</Th></Tr></Thead>
              <Tbody>
                {plan.files.map(({ field, file, platform }) => (
                  <Tr key={file.name}>
                    <Td dataLabel="File">{file.name}</Td>
                    <Td dataLabel="Kind">{describeFile(field, platform)}</Td>
                    <Td dataLabel="Size">{megabytes(file.size)}</Td>
                  </Tr>
                ))}
              </Tbody>
            </Table>
            <Button variant="primary" isLoading={busy} isDisabled={busy || !enabled} onClick={() => void onSubmit()}>
              Upload {plan.name} {plan.version}
            </Button>
          </>
        ) : null}
      </PageSection>
    </>
  )
}
