import { useState } from 'react'
import {
  Alert, AlertActionLink, Button, Card, CardBody, CardTitle, Content, EmptyState, EmptyStateBody, Label, LabelGroup, MenuToggle, Pagination, Progress,
  ProgressMeasureLocation, Select, SelectList, SelectOption, Toolbar, ToolbarContent, ToolbarItem,
} from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'

import { ApiError } from '../api/client'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { importJobRows, jobWhat, type JobStateFilter, type PluginImport } from '../data/pluginRegistry'
import { firstError, jobOrigin, jobTitle, stateLabel } from './PluginImportJob'

export const jobsPageSize = 10

export type JobsFilter = { state?: JobStateFilter; plugin?: string }

const stateOptions: { value: JobStateFilter | 'all'; text: string }[] = [
  { value: 'all', text: 'All states' }, { value: 'unfinished', text: 'Running or queued' }, { value: 'running', text: 'Running' },
  { value: 'queued', text: 'Queued' }, { value: 'succeeded', text: 'Succeeded' }, { value: 'partially_succeeded', text: 'Partially succeeded' },
  { value: 'failed', text: 'Failed' },
]

export function stateOptionText(state: JobStateFilter | undefined): string {
  return stateOptions.find((option) => option.value === (state ?? 'all'))?.text ?? 'All states'
}

// One job's second line in the State cell: where it is in the queue, or how
// far along it is.
export function jobProgress(job: PluginImport): { text: string; percent?: number } | null {
  if (job.state === 'queued') return { text: job.queued_ahead === 0 ? 'Runs next' : `${job.queued_ahead} ${job.queued_ahead === 1 ? 'job' : 'jobs'} ahead` }
  if (job.state === 'running') {
    const total = importJobRows(job).length
    return { text: `${job.outcomes.length} of ${total} ${job.changes.length ? 'additions' : 'versions'}`, percent: Math.round((job.outcomes.length / Math.max(total, 1)) * 100) }
  }
  return null
}

const when = (value: string | undefined) => (value ? new Date(value).toLocaleString() : '—')

export function PluginJobsFacet({
  jobs, total, filter, plugins, page, loading, failure, canPublish, busy,
  onFilter, onPage, onOpenJob, onRetry, onRefresh,
}: {
  jobs: PluginImport[]
  total: number
  filter: JobsFilter
  plugins: string[]
  page: number
  loading: boolean
  failure: string | null
  canPublish: boolean
  busy: boolean
  onFilter: (filter: JobsFilter) => void
  onPage: (page: number) => void
  onOpenJob: (id: string) => void
  onRetry: (job: PluginImport) => void
  onRefresh: () => void | Promise<void>
}) {
  const [stateOpen, setStateOpen] = useState(false)
  const [pluginOpen, setPluginOpen] = useState(false)
  const filtered = filter.state !== undefined || filter.plugin !== undefined
  if (loading) return <PluginLoadingCard message="Loading jobs…" />
  if (failure) return <PluginErrorCard title="Jobs could not be loaded" error={failure} onRetry={onRefresh} />
  if (total === 0 && !filtered) {
    return (
      <EmptyState titleText="No jobs yet" headingLevel="h2">
        <EmptyStateBody>
          Imports and syncs run as jobs, one at a time in the order they were queued. The first one appears here when it is queued.
        </EmptyStateBody>
      </EmptyState>
    )
  }
  return (
    <Card>
      <CardTitle>All jobs</CardTitle>
      <CardBody>
        <Toolbar id="jobs-toolbar" clearAllFilters={() => onFilter({})}>
          <ToolbarContent alignItems="center">
            <ToolbarItem>
              <Select
                isOpen={stateOpen} selected={filter.state ?? 'all'} onOpenChange={setStateOpen}
                onSelect={(_event, value) => { onFilter({ ...filter, state: value === 'all' ? undefined : value as JobStateFilter }); setStateOpen(false) }}
                toggle={(ref) => <MenuToggle ref={ref} onClick={() => setStateOpen(!stateOpen)} isExpanded={stateOpen}>{stateOptionText(filter.state)}</MenuToggle>}
              >
                <SelectList>{stateOptions.map((option) => <SelectOption key={option.value} value={option.value}>{option.text}</SelectOption>)}</SelectList>
              </Select>
            </ToolbarItem>
            <ToolbarItem>
              <Select
                isOpen={pluginOpen} selected={filter.plugin ?? 'all'} onOpenChange={setPluginOpen}
                onSelect={(_event, value) => { onFilter({ ...filter, plugin: value === 'all' ? undefined : String(value) }); setPluginOpen(false) }}
                toggle={(ref) => <MenuToggle ref={ref} onClick={() => setPluginOpen(!pluginOpen)} isExpanded={pluginOpen}>{filter.plugin ? `Plugin: ${filter.plugin}` : 'All plugins'}</MenuToggle>}
              >
                <SelectList>
                  <SelectOption value="all">All plugins</SelectOption>
                  {plugins.map((name) => <SelectOption key={name} value={name}>{name}</SelectOption>)}
                </SelectList>
              </Select>
            </ToolbarItem>
            {filtered ? (
              <ToolbarItem>
                <LabelGroup categoryName="Showing">
                  {filter.state ? <Label variant="outline" onClose={() => onFilter({ ...filter, state: undefined })}>{stateOptionText(filter.state)}</Label> : null}
                  {filter.plugin ? <Label variant="outline" onClose={() => onFilter({ ...filter, plugin: undefined })}>{filter.plugin}</Label> : null}
                </LabelGroup>
              </ToolbarItem>
            ) : null}
            <ToolbarItem variant="pagination" align={{ default: 'alignEnd' }}>
              <Pagination
                isCompact itemCount={total} perPage={jobsPageSize} page={page} perPageOptions={[{ title: '10', value: 10 }]}
                onSetPage={(_event, next) => onPage(next)} titles={{ paginationAriaLabel: 'Jobs pagination' }}
              />
            </ToolbarItem>
          </ToolbarContent>
        </Toolbar>
        <Table aria-label="Jobs" variant="compact">
          <Thead>
            <Tr>
              <Th>State</Th><Th>Job</Th><Th>Started by</Th><Th>Queued</Th><Th>Finished</Th>
              {canPublish ? <Th screenReaderText="Actions" /> : null}
            </Tr>
          </Thead>
          <Tbody>
            {jobs.map((job) => {
              const progress = jobProgress(job)
              const reason = job.state === 'failed' || job.state === 'partially_succeeded' ? firstError(job.outcomes) : undefined
              return (
                <Tr key={job.id}>
                  <Td dataLabel="State" modifier="nowrap">
                    {stateLabel[job.state]}
                    {progress ? (
                      <div style={{ marginTop: 4 }}>
                        {progress.percent !== undefined ? <Progress value={progress.percent} aria-label={`${jobTitle(job)} progress`} measureLocation={ProgressMeasureLocation.none} size="sm" /> : null}
                        <Content component="small">{progress.text}</Content>
                      </div>
                    ) : null}
                  </Td>
                  <Td dataLabel="Job">
                    <Button variant="link" isInline onClick={() => onOpenJob(job.id)}>{jobTitle(job)}</Button>
                    <Content component="small"> {jobWhat(job)} · Job {job.id.slice(0, 8)}</Content>
                    {reason ? <Content component="small" style={{ display: 'block', color: job.state === 'failed' ? 'var(--pf-t--global--color--status--danger--default)' : 'var(--pf-t--global--color--status--warning--default)' }}>{reason}</Content> : null}
                  </Td>
                  <Td dataLabel="Started by">
                    {job.created_by || 'unknown'}
                    <Content component="small" style={{ display: 'block' }}>{jobOrigin(job)}</Content>
                  </Td>
                  <Td dataLabel="Queued" modifier="nowrap">{when(job.created_at)}</Td>
                  <Td dataLabel="Finished" modifier="nowrap">{when(job.finished_at)}</Td>
                  {canPublish ? (
                    <Td dataLabel="Actions" modifier="nowrap">
                      {job.state === 'failed' || job.state === 'partially_succeeded'
                        ? <Button variant="link" isInline isDisabled={busy} onClick={() => onRetry(job)}>Retry</Button>
                        : null}
                    </Td>
                  ) : null}
                </Tr>
              )
            })}
          </Tbody>
        </Table>
        {jobs.length === 0 ? <Content component="p">No jobs match.</Content> : null}
      </CardBody>
    </Card>
  )
}

export type ImportRefusal = { job: PluginImport; message: string; selection: string }

// The selection an import was refused for; the refusal lifts when it changes.
export function importSelectionKey(versions: string[], platforms: string[]): string {
  return `${[...versions].sort().join(',')}|${[...platforms].sort().join(',')}`
}

export function isDuplicateImportRefusal(error: unknown): error is ApiError & { importID: string } {
  return error instanceof ApiError && error.status === 409 && typeof error.importID === 'string' && /identical/.test(error.message)
}

export function ImportRefusalAlert({ refusal, onOpenJob, onJobs }: { refusal: ImportRefusal; onOpenJob: (id: string) => void; onJobs: () => void }) {
  const { job } = refusal
  const id = job.id.slice(0, 8)
  const total = importJobRows(job).length
  const detail = job.state === 'queued'
    ? `It was queued by ${job.created_by || 'unknown'} at ${new Date(job.created_at).toLocaleTimeString()} and has ${job.queued_ahead} ${job.queued_ahead === 1 ? 'job' : 'jobs'} ahead of it.`
    : `It started at ${new Date(job.started_at ?? job.created_at).toLocaleTimeString()} and is ${job.outcomes.length} of ${total} ${total === 1 ? 'version' : 'versions'} in.`
  return (
    <Alert
      variant="info" isInline title={`This import is already ${job.state}`}
      actionLinks={(
        <>
          <AlertActionLink onClick={() => onOpenJob(job.id)}>Open job {id}</AlertActionLink>
          <AlertActionLink onClick={onJobs}>All jobs</AlertActionLink>
        </>
      )}
    >
      <Content component="p">Job {id} imports {jobWhat(job)}. {detail} Starting another would only record “already mirrored”.</Content>
    </Alert>
  )
}
