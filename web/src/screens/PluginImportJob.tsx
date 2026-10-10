import { Fragment, useEffect, useState } from 'react'
import {
  Alert, AlertActionLink, Breadcrumb, BreadcrumbItem, Button, Card, CardBody, CardTitle, Content, Flex, FlexItem, Label,
  PageSection, Progress, ProgressMeasureLocation, Spinner,
} from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'
import { useNavigate, useParams } from 'react-router'

import { signOutIfUnauthorized } from '../api/client'
import { useAuth } from '../auth/AuthContext'
import { permitsAction, type Role } from '../auth/permissions'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { ScreenHeader } from '../components/ScreenHeader'
import {
  getPluginImport, getPluginRegistry, importJobRows, importJobSummary, retryPluginImport, templateStanza,
  terminalImportStates, type ImportVersionOutcome, type PluginImport, type PluginRegistry,
} from '../data/pluginRegistry'
import { useTenant } from '../data/tenant'
import { NotExposedAlert, TemplateStanzaBlock } from './PluginDetail'
import { pluginRegistryErrorMessage } from './Plugins'

export function PluginImportJob() {
  const { id = '' } = useParams()
  const { state, self, selectedOrganization, signOut } = useAuth()
  const { tenant } = useTenant()
  const navigate = useNavigate()
  const organizationID = selectedOrganization ?? state?.claims.organizationID ?? ''
  const token = state?.token ?? ''
  const [job, setJob] = useState<PluginImport | null>(null)
  const [registry, setRegistry] = useState<PluginRegistry | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [revision, setRevision] = useState(0)

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
  }, [id, organizationID, revision, signOut, token])

  return (
    <PluginImportJobView
      job={job} registry={registry} failure={failure} organizationName={tenant.organization} host={window.location.hostname}
      id={id} loading={!job && !failure} callerRole={self?.role ?? null} busy={busy} actionFailure={actionFailure}
      onBackToRegistry={() => navigate('/buckets')}
      onBackToPlugins={() => navigate('/plugin-registry')}
      onRefresh={() => {
        setJob(null)
        setFailure(null)
        setRevision((current) => current + 1)
      }}
      onOpen={(name) => navigate(`/plugin-registry/${encodeURIComponent(name)}`)}
      onRetry={() => void (async () => {
        if (!job) return
        setBusy(true)
        setActionFailure(null)
        try {
          const next = await retryPluginImport(token, organizationID, job)
          navigate(`/plugin-registry/imports/${next.id}`)
        } catch (error: unknown) {
          if (!signOutIfUnauthorized(error, signOut)) setActionFailure(pluginRegistryErrorMessage(error, 'The retry could not be queued.'))
        } finally {
          setBusy(false)
        }
      })()}
    />
  )
}

const stateLabel = {
  queued: <Label color="grey">Queued</Label>,
  running: <Label color="blue">Running</Label>,
  succeeded: <Label color="green">Succeeded</Label>,
  partially_succeeded: <Label color="yellow">Partially succeeded</Label>,
  failed: <Label color="red">Failed</Label>,
}

const outcomeLabel = {
  imported: <Label isCompact color="green">Imported</Label>,
  already_mirrored: <Label isCompact color="grey">Already mirrored</Label>,
  revoked: <Label isCompact color="green">Revoked</Label>,
  restored: <Label isCompact color="green">Restored</Label>,
  failed: <Label isCompact color="red">Failed</Label>,
}

export function jobName(job: PluginImport): string {
  return job.product.replace(/^.*packer-plugin-/, '')
}

export function jobTitle(job: PluginImport): string {
  const verb = job.origin === 'upload' ? 'Upload' : job.changes.length ? 'Sync' : 'Import'
  return `${verb} ${jobName(job)}`
}

export function jobOrigin(job: PluginImport): string {
  switch (job.origin) {
    case 'plugin': return `from the plugin page, ${job.changes.length} ${job.changes.length === 1 ? 'change' : 'changes'}`
    case 'catalogue': return `from Sync selected (${job.batch_index} of ${job.batch_size} ${job.batch_size === 1 ? 'job' : 'jobs'})`
    case 'upload': return 'from Upload'
    default: return job.source === 'github' ? `from Import from GitHub (${job.product})` : `from Browse HashiCorp (${job.product})`
  }
}

export function PluginImportJobView({
  id, job, registry, loading, failure, organizationName, host, callerRole, busy, actionFailure,
  onBackToRegistry, onBackToPlugins, onRefresh, onOpen, onRetry,
}: {
  id: string
  job: PluginImport | null
  registry: PluginRegistry | null
  loading: boolean
  failure: string | null
  organizationName: string
  host: string
  callerRole: Role | null
  busy: boolean
  actionFailure: string | null
  onBackToRegistry: () => void
  onBackToPlugins: () => void
  onRefresh: () => void | Promise<void>
  onOpen: (name: string) => void
  onRetry: () => void
}) {
  const name = job ? jobName(job) : ''
  const rows = job ? importJobRows(job) : []
  const summary = job ? importJobSummary(job) : null
  const finished = job ? terminalImportStates.has(job.state) : false
  const imported = job?.outcomes.filter((o) => o.outcome === 'imported').map((o) => o.version) ?? []
  const newest = imported.at(-1)
  const failedRows = rows.filter((row) => row.outcome?.outcome === 'failed' || row.outcome?.platforms?.some((p) => p.outcome === 'failed'))
  const canRetry = permitsAction(callerRole, 'publishPlugin')
  const unit = job?.changes.length ? 'change' : 'version'
  return (
    <>
      <ScreenHeader
        breadcrumbs={(
          <Breadcrumb>
            <BreadcrumbItem component="button" onClick={onBackToRegistry}>Registry</BreadcrumbItem>
            <BreadcrumbItem component="button" onClick={onBackToPlugins}>Plugins</BreadcrumbItem>
            <BreadcrumbItem component="button" onClick={() => name && onOpen(name)}>{name || 'Plugin'}</BreadcrumbItem>
            <BreadcrumbItem isActive>Job {(job?.id ?? id).slice(0, 8)}</BreadcrumbItem>
          </Breadcrumb>
        )}
        title={job ? <>{jobTitle(job)} {stateLabel[job.state]}</> : 'Job'}
        description={job ? (
          [
            `Started by ${job.created_by || 'unknown'}`,
            new Date(job.created_at).toLocaleString(),
            ...(job.finished_at ? [`finished ${new Date(job.finished_at).toLocaleTimeString()}`] : []),
            jobOrigin(job),
          ].join(' · ')
        ) : undefined}
        onRefresh={onRefresh} refreshing={loading}
      />
      <PageSection variant="secondary" isFilled>
        <NotExposedAlert registry={registry} />
        {actionFailure ? <Alert variant="danger" isInline title="The retry could not be queued"><Content component="p">{actionFailure}</Content></Alert> : null}
        {loading ? (
          <PluginLoadingCard message="Loading job…" />
        ) : failure ? (
          <PluginErrorCard title="Job could not be loaded" error={failure} onRetry={onRefresh} />
        ) : null}
        {!loading && !failure && job ? (
          <>
            {job.state === 'failed' ? (
              <Alert
                variant="danger" isInline title="Job failed"
                actionLinks={canRetry ? <AlertActionLink isDisabled={busy} onClick={onRetry}>Retry job</AlertActionLink> : undefined}
              >
                <Content component="p">{firstError(job.outcomes) ?? 'Every step failed.'} Nothing was changed.</Content>
              </Alert>
            ) : null}
            {job.state === 'partially_succeeded' ? (
              <Alert
                variant="warning" isInline title={`${rows.length - failedRows.length} of ${rows.length} ${unit}s applied`}
                actionLinks={canRetry ? <AlertActionLink isDisabled={busy} onClick={onRetry}>Retry failed {failedRows.length === 1 ? unit : `${unit}s`}</AlertActionLink> : undefined}
              >
                <Content component="p">
                  {failedRows.map((row) => `${row.version} didn’t get ${row.outcome?.outcome === 'failed' ? 'applied' : failedPlatforms(row.outcome).join(', ')}`).join('; ')}.
                  {' '}Other {unit}s are live. Retrying runs only the failed {failedRows.length === 1 ? unit : `${unit}s`}.
                </Content>
              </Alert>
            ) : null}
            <Card>
              <CardTitle>
                <Flex gap={{ default: 'gapMd' }} alignItems={{ default: 'alignItemsCenter' }}>
                  <FlexItem>Outcome</FlexItem>
                  <FlexItem><Content component="small">{summary?.text}</Content></FlexItem>
                  {summary?.progress !== undefined ? (
                    <FlexItem flex={{ default: 'flex_1' }}>
                      <Progress value={summary.progress} aria-label="Job progress" measureLocation={ProgressMeasureLocation.none} size="sm" />
                    </FlexItem>
                  ) : null}
                </Flex>
              </CardTitle>
              <CardBody>
                <Table aria-label="Import outcomes" variant="compact">
                  <Thead><Tr><Th>Version</Th><Th>Change</Th><Th>Architectures</Th><Th>Outcome</Th></Tr></Thead>
                  <Tbody>
                    {rows.map((row, index) => {
                      const outcome = row.outcome
                      const active = !outcome && job.state === 'running' && index === job.outcomes.length
                      const subs = (outcome?.platforms ?? []).filter((p) => p.outcome !== 'imported')
                      const counted = outcome?.platforms?.length ? `${outcome.platforms.filter((p) => p.outcome === 'imported').length} of ${outcome.platforms.length}` : ''
                      return (
                        <Fragment key={`${row.version}-${index}`}>
                          <Tr>
                            <Td dataLabel="Version"><code>{row.version}</code></Td>
                            <Td dataLabel="Change">{row.change}</Td>
                            <Td dataLabel="Architectures">{row.architectures}</Td>
                            <Td dataLabel="Outcome">
                              {outcome ? outcomeLabel[outcome.outcome]
                                : active ? <><Spinner size="sm" aria-label="Importing" /> <Label isCompact color="blue">Importing</Label></>
                                : <Label isCompact variant="outline">Waiting</Label>}
                              {outcome && subs.length && counted ? <> <Content component="small">{counted}</Content></> : null}
                            </Td>
                          </Tr>
                          {subs.map((platform) => (
                            <Tr key={`${row.version}-${platform.platform}`} isBorderRow>
                              <Td dataLabel="Version" />
                              <Td dataLabel="Change" />
                              <Td dataLabel="Architectures">
                                <code>{platform.platform}</code>{' '}
                                <Content component="small">{platform.outcome === 'already_mirrored' ? 'Identical to the stored file; nothing written.' : platform.error}</Content>
                              </Td>
                              <Td dataLabel="Outcome">{outcomeLabel[platform.outcome]}</Td>
                            </Tr>
                          ))}
                          {outcome?.outcome === 'failed' && outcome.error ? (
                            <Tr key={`${row.version}-error`} isBorderRow>
                              <Td dataLabel="Version" />
                              <Td dataLabel="Change" />
                              <Td dataLabel="Architectures"><code>all</code> <Content component="small">{outcome.error}</Content></Td>
                              <Td dataLabel="Outcome">{outcomeLabel.failed}</Td>
                            </Tr>
                          ) : null}
                        </Fragment>
                      )
                    })}
                  </Tbody>
                </Table>
                {imported.length || finished ? <Button variant="link" isInline onClick={() => onOpen(name)}>Open {name}</Button> : null}
              </CardBody>
            </Card>
            <Card>
              <CardTitle>Template stanza</CardTitle>
              <CardBody>
                {newest ? (
                  <>
                    <TemplateStanzaBlock hcl={templateStanza(host, organizationName, name, newest)} />
                    <Content component="small">Pins the newest version this job made available.</Content>
                  </>
                ) : (
                  <Content component="p">{finished ? 'No stanza: nothing was imported.' : 'Appears when the job imports something.'}</Content>
                )}
              </CardBody>
            </Card>
          </>
        ) : null}
      </PageSection>
    </>
  )
}

function firstError(outcomes: ImportVersionOutcome[]): string | undefined {
  for (const outcome of outcomes) {
    if (outcome.error) return outcome.error
    const platform = outcome.platforms?.find((p) => p.error)
    if (platform?.error) return `${platform.platform}: ${platform.error}`
  }
  return undefined
}

function failedPlatforms(outcome: ImportVersionOutcome | undefined): string[] {
  return (outcome?.platforms ?? []).filter((p) => p.outcome === 'failed').map((p) => p.platform)
}
