import { Fragment, useEffect, useState } from 'react'
import { Alert, Button, Content, Label, PageSection, Spinner, Title } from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate, useParams } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  getPluginImport, getPluginRegistry, templateStanza, terminalImportStates,
  type PluginImport, type PluginRegistry,
} from '../data/pluginRegistry'
import { useTenant } from '../data/tenant'
import { NotExposedAlert, TemplateStanzaBlock } from './PluginDetail'
import { pluginRegistryErrorMessage } from './Plugins'

export function PluginImportJob() {
  const { id = '' } = useParams()
  const { state, selectedOrganization, signOut } = useAuth()
  const { tenant } = useTenant()
  const navigate = useNavigate()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? ''
  const token = state?.token ?? ''
  const [job, setJob] = useState<PluginImport | null>(null)
  const [registry, setRegistry] = useState<PluginRegistry | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  useEffect(() => {
    if (!organizationID || token === '') return
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const poll = async () => {
      try {
        const [nextJob, nextRegistry] = await Promise.all([
          getPluginImport(token, organizationID, id), getPluginRegistry(token, organizationID),
        ])
        if (stopped) return
        setJob(nextJob)
        setRegistry(nextRegistry)
        setFailure(null)
        if (!terminalImportStates.has(nextJob.state)) timer = setTimeout(() => void poll(), 2000)
      } catch (error: unknown) {
        if (!signOutIfUnauthorized(error, signOut)) setFailure(pluginRegistryErrorMessage(error, 'The import could not be loaded.'))
      }
    }
    void poll()
    return () => { stopped = true; clearTimeout(timer) }
  }, [id, organizationID, signOut, token])

  return (
    <PluginImportJobView
      job={job} registry={registry} failure={failure} organizationName={tenant.organization} host={window.location.hostname}
      onOpen={(name) => navigate(`/plugin-registry/${encodeURIComponent(name)}`)}
    />
  )
}

const stateLabel = {
  queued: <Label color="grey">Queued</Label>,
  running: <Label color="blue">Running</Label>,
  succeeded: <Label color="green">Succeeded</Label>,
  partially_succeeded: <Label color="orange">Partially succeeded</Label>,
  failed: <Label color="red">Failed</Label>,
}

const outcomeLabel = {
  imported: <Label isCompact color="green">Imported</Label>,
  already_mirrored: <Label isCompact color="grey">Already mirrored</Label>,
  revoked: <Label isCompact color="orange">Revoked</Label>,
  restored: <Label isCompact color="green">Restored</Label>,
  failed: <Label isCompact color="red">Failed</Label>,
}

export function PluginImportJobView({ job, registry, failure, organizationName, host, onOpen }: {
  job: PluginImport | null
  registry: PluginRegistry | null
  failure: string | null
  organizationName: string
  host: string
  onOpen: (name: string) => void
}) {
  const name = job?.product.replace(/^.*packer-plugin-/, '') ?? ''
  const imported = job?.outcomes.filter((o) => o.outcome === 'imported').map((o) => o.version) ?? []
  const newest = imported.at(-1)
  const sync = Boolean(job?.changes.length)
  const rows = sync
    ? job?.changes.map((c) => ({ version: c.version, detail: c.action === 'add' ? `add ${c.platforms?.join(', ')}` : c.action })) ?? []
    : job?.versions.map((version) => ({ version, detail: '' })) ?? []
  return (
    <>
      <ScreenHeader
        title={job ? `${sync ? 'Sync' : 'Import'} ${name}` : 'Import'}
        description={job ? (
          <>{stateLabel[job.state]} {sync ? `${rows.length} ${rows.length === 1 ? 'change' : 'changes'}, applied in order`
            : `from ${job.source === 'github' ? `GitHub (${job.product})` : 'releases.hashicorp.com'}`}</>
        ) : undefined}
      />
      <PageSection variant="secondary" isFilled>
        {failure ? <Alert variant="danger" isInline title="The import could not be loaded"><Content component="p">{failure}</Content></Alert> : null}
        {!job && !failure ? <Spinner aria-label="Loading import…" /> : null}
        {job ? (
          <>
            {sync ? null : <Content component="p">Platforms: {job.platforms.join(', ')}</Content>}
            <Table aria-label="Import outcomes" variant="compact">
              <Thead><Tr><Th>Version</Th>{sync ? <Th>Change</Th> : null}<Th>Outcome</Th><Th>Detail</Th></Tr></Thead>
              <Tbody>
                {rows.map(({ version, detail }, index) => {
                  // Outcomes are recorded in request order; a GitHub job requests tags, its outcomes name versions.
                  const outcome = job.outcomes[index]
                  return (
                    <Fragment key={version}>
                      <Tr>
                        <Td dataLabel="Version">{version}</Td>
                        {sync ? <Td dataLabel="Change">{detail}</Td> : null}
                        <Td dataLabel="Outcome">{outcome ? outcomeLabel[outcome.outcome] : <Label isCompact color="grey">Pending</Label>}</Td>
                        <Td dataLabel="Detail">{outcome?.error ?? ''}</Td>
                      </Tr>
                      {outcome?.platforms?.filter((p) => p.outcome === 'failed').map((platform) => (
                        <Tr key={`${version}-${platform.platform}`}>
                          <Td dataLabel="Version" />
                          {sync ? <Td dataLabel="Change" /> : null}
                          <Td dataLabel="Outcome"><Label isCompact color="red">{platform.platform} failed</Label></Td>
                          <Td dataLabel="Detail">{platform.error ?? ''}</Td>
                        </Tr>
                      ))}
                    </Fragment>
                  )
                })}
              </Tbody>
            </Table>
            {imported.length || (sync && terminalImportStates.has(job.state)) ? <Button variant="link" isInline onClick={() => onOpen(name)}>Open {name}</Button> : null}
            {newest ? (
              <>
                <Title headingLevel="h2" size="md">Template stanza</Title>
                <NotExposedAlert registry={registry} />
                <TemplateStanzaBlock hcl={templateStanza(host, organizationName, name, newest)} />
              </>
            ) : null}
          </>
        ) : null}
      </PageSection>
    </>
  )
}
