import { useEffect, useState } from 'react'
import { Alert, Button, Card, CardBody, Content, Label, PageSection, Title } from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  getPluginRegistry, planPluginUploads, publishPluginVersion,
  type PluginRegistry, type TemplateStanza, type UploadPlan,
} from '../data/pluginRegistry'
import { NotExposedAlert, TemplateStanzaBlock } from './PluginDetail'
import { pluginRegistryErrorMessage } from './Plugins'

export type UploadOutcome =
  | { status: 'sending' }
  | { status: 'published'; stanza: TemplateStanza }
  | { status: 'refused'; message: string }

const key = (name: string, version: string) => `${name} ${version}`

export function PluginUpload() {
  const { state, selectedOrganization, signOut } = useAuth()
  const navigate = useNavigate()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? null
  const token = state?.token ?? ''
  const [registry, setRegistry] = useState<PluginRegistry | null>(null)
  const [plan, setPlan] = useState<UploadPlan | null>(null)
  const [outcomes, setOutcomes] = useState<Record<string, UploadOutcome>>({})
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string | null>(null)

  useEffect(() => {
    if (!organizationID || token === '') return
    getPluginRegistry(token, organizationID).then(setRegistry).catch((error: unknown) => {
      if (!signOutIfUnauthorized(error, signOut)) setFailure(pluginRegistryErrorMessage(error, 'The plugin registry could not be loaded.'))
    })
  }, [organizationID, signOut, token])

  return (
    <PluginUploadView
      registry={registry} plan={plan} outcomes={outcomes} busy={busy} failure={failure}
      onChoose={(files) => { setOutcomes({}); setFailure(null); setPlan(files.length ? planPluginUploads(files) : null) }}
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

function describeFile(field: string, platform?: string): string {
  if (field === 'zips') return platform ?? 'zip'
  return { sha256sums: 'SHA256SUMS', sha256sums_sig: 'Signature', manifest: 'Manifest' }[field] ?? field
}

function megabytes(size: number): string {
  return `${(size / 1_000_000).toFixed(1)} MB`
}

function OutcomeLabel({ outcome }: { outcome?: UploadOutcome }) {
  if (!outcome) return <Label isCompact color="grey">Ready</Label>
  if (outcome.status === 'sending') return <Label isCompact color="blue">Sending</Label>
  if (outcome.status === 'published') return <Label isCompact color="green">Published</Label>
  return <Label isCompact color="red">Refused</Label>
}

export function PluginUploadView({ registry, plan, outcomes, busy, failure, onChoose, onOpen, onSubmit }: {
  registry: PluginRegistry | null
  plan: UploadPlan | null
  outcomes: Record<string, UploadOutcome>
  busy: boolean
  failure: string | null
  onChoose: (files: File[]) => void
  onOpen: (name: string) => void
  onSubmit: () => void | Promise<void>
}) {
  const enabled = registry?.enabled ?? false
  const versions = plan?.versions ?? []
  const sent = versions.some((upload) => outcomes[key(upload.name, upload.version)])
  const published = versions.flatMap((upload) => {
    const outcome = outcomes[key(upload.name, upload.version)]
    return outcome?.status === 'published' ? [{ name: upload.name, stanza: outcome.stanza }] : []
  })
  return (
    <>
      <ScreenHeader
        title="Upload plugin"
        description="Upload plugin versions built in-house: each version's SHA256SUMS file, its zips, and its signature or manifest if it has them."
      />
      <PageSection variant="secondary" isFilled>
        {registry && !enabled ? (
          <Alert variant="warning" isInline title="The plugin registry isn't enabled">
            <Content component="p">Enable the registry on the Plugins screen before uploading.</Content>
          </Alert>
        ) : null}
        {failure ? <Alert variant="danger" isInline title="The plugin registry could not be loaded"><Content component="p">{failure}</Content></Alert> : null}
        <Card>
          <CardBody>
            <Content component="p">
              Files named packer-plugin-NAME_vVERSION_xAPI_OS_ARCH.zip or packer-plugin-NAME_VERSION_OS_ARCH.zip,
              with each version's SHA256SUMS. One version or several; they are grouped into one upload per
              version before anything is sent. Zips a SHA256SUMS lists but you leave out are not served.
            </Content>
            <input
              aria-label="Choose plugin files" type="file" multiple disabled={!enabled || busy}
              onChange={(event) => onChoose([...(event.target.files ?? [])])}
            />
          </CardBody>
        </Card>
        {plan?.refused.length ? (
          <Alert variant="danger" isInline title="Some files can't be uploaded">
            {plan.refused.map((refusal) => (
              <Content component="p" key={refusal.label}>{refusal.label}: {refusal.reason}. Not sent.</Content>
            ))}
          </Alert>
        ) : null}
        {versions.map((upload) => {
          const outcome = outcomes[key(upload.name, upload.version)]
          return (
            <Card key={key(upload.name, upload.version)}>
              <CardBody>
                <Title headingLevel="h2" size="md">
                  {upload.name} · {upload.version} <OutcomeLabel outcome={outcome} />
                </Title>
                {outcome?.status === 'refused' ? <Content component="p">{outcome.message}</Content> : null}
                {outcome?.status === 'published' ? (
                  <Button variant="link" isInline onClick={() => onOpen(upload.name)}>Open {upload.name}</Button>
                ) : null}
                <Table aria-label={`Files for ${upload.name} ${upload.version}`} variant="compact">
                  <Thead><Tr><Th>File</Th><Th>Kind</Th><Th>Size</Th></Tr></Thead>
                  <Tbody>
                    {upload.files.map(({ field, file, platform }) => (
                      <Tr key={file.name}>
                        <Td dataLabel="File">{file.name}</Td>
                        <Td dataLabel="Kind">{describeFile(field, platform)}</Td>
                        <Td dataLabel="Size">{megabytes(file.size)}</Td>
                      </Tr>
                    ))}
                  </Tbody>
                </Table>
              </CardBody>
            </Card>
          )
        })}
        {versions.length && !sent ? (
          <Button variant="primary" isLoading={busy} isDisabled={busy || !enabled} onClick={() => void onSubmit()}>
            {versions.length === 1
              ? `Upload ${versions[0]?.name} ${versions[0]?.version}`
              : `Upload ${versions.length} versions`}
          </Button>
        ) : null}
        {published.length ? (
          <>
            <Title headingLevel="h2" size="md">Template stanza</Title>
            <NotExposedAlert registry={registry} />
            {published.map(({ name, stanza }) => (
              <TemplateStanzaBlock key={`${name} ${stanza.version}`} id={`stanza-${name}-${stanza.version}`} hcl={stanza.hcl} />
            ))}
          </>
        ) : null}
      </PageSection>
    </>
  )
}
