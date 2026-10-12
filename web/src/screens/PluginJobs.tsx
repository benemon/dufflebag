import { useState, type Ref } from 'react'
import {
  Alert, AlertActionLink, Button, Card, CardBody, CardTitle, Content, Dropdown, DropdownItem, DropdownList, EmptyState, EmptyStateBody,
  Label, LabelGroup, MenuToggle, MenuToggleCheckbox, Modal, ModalBody, ModalFooter, ModalHeader, Pagination, Progress,
  ProgressMeasureLocation, Select, SelectList, SelectOption, Toolbar, ToolbarContent, ToolbarItem, type MenuToggleElement,
} from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'

import { ApiError } from '../api/client'
import { PluginErrorCard, PluginLoadingCard } from '../components/PluginLoadState'
import { firstError, importJobRows, jobOrigin, jobTitle, jobWhat, type JobStateFilter, type PluginImport } from '../data/pluginRegistry'

export const stateLabel = {
  queued: <Label color="grey">Queued</Label>,
  running: <Label color="blue">Running</Label>,
  succeeded: <Label color="green">Succeeded</Label>,
  partially_succeeded: <Label color="yellow">Partially succeeded</Label>,
  failed: <Label color="red">Failed</Label>,
}

export const jobsPageSize = 10

export type JobsFilter = { states: JobStateFilter[]; plugins: string[] }

export const emptyJobsFilter: JobsFilter = { states: [], plugins: [] }

const stateOptions: { value: JobStateFilter; text: string }[] = [
  { value: 'queued', text: 'Queued' }, { value: 'running', text: 'Running' }, { value: 'succeeded', text: 'Succeeded' },
  { value: 'partially_succeeded', text: 'Partially succeeded' }, { value: 'failed', text: 'Failed' },
]

export function stateOptionText(state: JobStateFilter): string {
  return state === 'unfinished' ? 'Running or queued' : stateOptions.find((option) => option.value === state)?.text ?? state
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
const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`

// The confirmation's parts: queued jobs are taken out of the queue, finished
// ones lose their record; a running job is never in the selection.
export function clearConfirmation(jobs: PluginImport[]): { title: string; parts: { head: string; danger: boolean; body: string }[]; verb: string } {
  const queued = jobs.filter((job) => job.state === 'queued')
  const finished = jobs.filter((job) => job.state !== 'queued')
  const parts = []
  if (queued.length) {
    parts.push({
      head: `${queued.length} queued: ${queued.length === 1 ? 'it won’t run' : 'they won’t run'}`, danger: true,
      body: `${queued.map((job) => `${jobTitle(job)} (${jobWhat(job)})`).join('; ')}. ${queued.length === 1 ? 'It is' : 'They are'} taken out of the queue and the jobs behind move up. Nothing is mirrored for ${queued.length === 1 ? 'it' : 'them'}; queue the work again to get it.`,
    })
  }
  if (finished.length) {
    parts.push({
      head: `${finished.length} finished: ${finished.length === 1 ? 'record' : 'records'} deleted`, danger: false,
      body: `${finished.map(jobTitle).join(', ')}. ${finished.length === 1 ? 'Its outcomes and link stop' : 'Their outcomes and links stop'} working. Versions ${finished.length === 1 ? 'it' : 'they'} mirrored are unchanged.`,
    })
  }
  parts.push({ head: '', danger: false, body: 'This can’t be undone. If a queued job starts running before this goes through, it isn’t cleared.' })
  return { title: `Clear ${plural(jobs.length, 'job')}?`, parts, verb: `Clear ${plural(jobs.length, 'job')}` }
}

export type ClearOutcome = { cleared: PluginImport[]; running: PluginImport[]; failed: { job: PluginImport; message: string }[] }

export function clearOutcomeAlert(outcome: ClearOutcome): { variant: 'success' | 'warning' | 'danger'; title: string; body: string } {
  const asked = outcome.cleared.length + outcome.running.length + outcome.failed.length
  const queued = outcome.cleared.filter((job) => job.state === 'queued')
  const finished = outcome.cleared.filter((job) => job.state !== 'queued')
  const sentences = []
  if (queued.length) sentences.push(`${plural(queued.length, 'queued job')} ${queued.length === 1 ? 'was' : 'were'} taken out of the queue and won’t run: ${queued.map(jobTitle).join(', ')}.`)
  if (finished.length) sentences.push(`${plural(finished.length, 'finished job record')} ${finished.length === 1 ? 'was' : 'were'} deleted; the versions ${finished.length === 1 ? 'it' : 'they'} mirrored are unchanged.`)
  if (outcome.running.length) sentences.push(`${outcome.running.map(jobTitle).join(', ')} started running before ${outcome.running.length === 1 ? 'it' : 'they'} could be cleared, so ${outcome.running.length === 1 ? 'it' : 'they'} will run to the end. Running jobs can’t be cleared.`)
  if (outcome.failed.length) sentences.push(`Not cleared: ${outcome.failed.map(({ job, message }) => `${jobTitle(job)} (${message})`).join('; ')}.`)
  if (outcome.cleared.length === asked) return { variant: 'success', title: `${plural(asked, 'job')} cleared`, body: sentences.join(' ') }
  if (outcome.cleared.length === 0 && outcome.failed.length) return { variant: 'danger', title: 'Nothing was cleared', body: sentences.join(' ') }
  return { variant: 'warning', title: `${outcome.cleared.length} of ${plural(asked, 'job')} cleared`, body: sentences.join(' ') }
}

export function PluginJobsFacet({
  jobs, total, filter, plugins, page, loading, failure, canPublish, busy, outcome,
  onFilter, onPage, onOpenJob, onRetry, onRefresh, onClear, onMatching, onDismissOutcome,
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
  outcome: ClearOutcome | null
  onFilter: (filter: JobsFilter) => void
  onPage: (page: number) => void
  onOpenJob: (id: string) => void
  onRetry: (job: PluginImport) => void
  onRefresh: () => void | Promise<void>
  onClear: (jobs: PluginImport[]) => void
  onMatching: () => Promise<PluginImport[]>
  onDismissOutcome: () => void
}) {
  const [stateOpen, setStateOpen] = useState(false)
  const [pluginOpen, setPluginOpen] = useState(false)
  const [bulkOpen, setBulkOpen] = useState(false)
  const [selected, setSelected] = useState<PluginImport[]>([])
  const [confirming, setConfirming] = useState(false)
  const filtered = filter.states.length > 0 || filter.plugins.length > 0
  const clearable = (job: PluginImport) => job.state !== 'running'
  const pageClearable = jobs.filter(clearable)
  // The selection follows the rows as they change: a job that finished or
  // started while selected keeps or loses its place by its current state.
  const chosen = selected.map((job) => jobs.find((row) => row.id === job.id) ?? job).filter(clearable)
  const isChosen = (job: PluginImport) => chosen.some((row) => row.id === job.id)
  const toggle = (list: JobStateFilter[] | string[], value: string) => (list.includes(value as never) ? list.filter((v) => v !== value) : [...list, value])
  const outcomeAlert = outcome ? (
    (() => { const alert = clearOutcomeAlert(outcome); return <Alert variant={alert.variant} isInline title={alert.title} actionClose={undefined} actionLinks={<AlertActionLink onClick={onDismissOutcome}>Dismiss</AlertActionLink>}><Content component="p">{alert.body}</Content></Alert> })()
  ) : null
  if (loading) return <PluginLoadingCard message="Loading jobs…" />
  if (failure) return <PluginErrorCard title="Jobs could not be loaded" error={failure} onRetry={onRefresh} />
  if (total === 0 && !filtered) {
    return (
      <>
        {outcomeAlert}
        <EmptyState titleText="No jobs yet" headingLevel="h2">
          <EmptyStateBody>
            Imports and syncs run as jobs, one at a time in the order they were queued. The first one appears here when it is queued.
          </EmptyStateBody>
        </EmptyState>
      </>
    )
  }
  const queuedChosen = chosen.filter((job) => job.state === 'queued').length
  return (
    <>
      {outcomeAlert}
      <Card>
        <CardTitle>Jobs</CardTitle>
        <CardBody>
          <Toolbar id="jobs-toolbar" clearAllFilters={() => onFilter(emptyJobsFilter)}>
            <ToolbarContent alignItems="center">
              {canPublish ? (
                <ToolbarItem>
                  <Dropdown
                    role="menu" isOpen={bulkOpen} onOpenChange={setBulkOpen}
                    onSelect={(_event, value) => {
                      setBulkOpen(false)
                      if (value === 'none') setSelected([])
                      if (value === 'page') setSelected(pageClearable)
                      if (value === 'all') void onMatching().then((matching) => setSelected(matching.filter(clearable)))
                    }}
                    toggle={(toggleRef: Ref<MenuToggleElement>) => (
                      <MenuToggle
                        ref={toggleRef} isExpanded={bulkOpen} onClick={() => setBulkOpen(!bulkOpen)} aria-label="Select jobs"
                        splitButtonItems={[
                          <MenuToggleCheckbox
                            id="jobs-bulk-select" key="jobs-bulk-select" aria-label={chosen.length ? 'Deselect all jobs' : 'Select the page'}
                            isChecked={pageClearable.length > 0 && pageClearable.every(isChosen) ? true : chosen.length > 0 ? null : false}
                            onChange={(checked) => setSelected(checked ? pageClearable : [])}
                          >
                            {chosen.length ? `${chosen.length} selected` : null}
                          </MenuToggleCheckbox>,
                        ]}
                      />
                    )}
                  >
                    <DropdownList>
                      <DropdownItem value="none">Select none (0)</DropdownItem>
                      <DropdownItem value="page">Select page ({pageClearable.length})</DropdownItem>
                      <DropdownItem value="all">Select all matching ({total})</DropdownItem>
                    </DropdownList>
                  </Dropdown>
                </ToolbarItem>
              ) : null}
              <ToolbarItem>
                <Select
                  role="menu" isOpen={stateOpen} onOpenChange={setStateOpen} selected={filter.states}
                  onSelect={(_event, value) => onFilter({ ...filter, states: toggle(filter.states, String(value)) as JobStateFilter[] })}
                  toggle={(ref) => <MenuToggle ref={ref} onClick={() => setStateOpen(!stateOpen)} isExpanded={stateOpen}>{filter.states.length ? `State · ${filter.states.length}` : 'State'}</MenuToggle>}
                >
                  <SelectList>
                    {stateOptions.map((option) => (
                      <SelectOption key={option.value} value={option.value} hasCheckbox isSelected={filter.states.includes(option.value)}>{option.text}</SelectOption>
                    ))}
                  </SelectList>
                </Select>
              </ToolbarItem>
              <ToolbarItem>
                <Select
                  role="menu" isOpen={pluginOpen} onOpenChange={setPluginOpen} selected={filter.plugins}
                  onSelect={(_event, value) => onFilter({ ...filter, plugins: toggle(filter.plugins, String(value)) as string[] })}
                  toggle={(ref) => <MenuToggle ref={ref} onClick={() => setPluginOpen(!pluginOpen)} isExpanded={pluginOpen}>{filter.plugins.length ? `Plugin · ${filter.plugins.length}` : 'Plugin'}</MenuToggle>}
                >
                  <SelectList>
                    {plugins.map((name) => <SelectOption key={name} value={name} hasCheckbox isSelected={filter.plugins.includes(name)}>{name}</SelectOption>)}
                  </SelectList>
                </Select>
              </ToolbarItem>
              {filtered ? (
                <ToolbarItem>
                  {filter.states.length ? (
                    <LabelGroup categoryName="State">
                      {filter.states.map((state) => <Label key={state} variant="outline" onClose={() => onFilter({ ...filter, states: filter.states.filter((s) => s !== state) })}>{stateOptionText(state)}</Label>)}
                    </LabelGroup>
                  ) : null}
                  {' '}
                  {filter.plugins.length ? (
                    <LabelGroup categoryName="Plugin">
                      {filter.plugins.map((name) => <Label key={name} variant="outline" onClose={() => onFilter({ ...filter, plugins: filter.plugins.filter((p) => p !== name) })}>{name}</Label>)}
                    </LabelGroup>
                  ) : null}
                </ToolbarItem>
              ) : null}
              {canPublish ? (
                <>
                  <ToolbarItem variant="separator" />
                  <ToolbarItem><span style={{ color: 'var(--pf-t--global--text--color--subtle)' }}>{chosen.length ? `${chosen.length} selected${queuedChosen ? ` · ${queuedChosen} queued` : ''}` : 'Select jobs to clear'}</span></ToolbarItem>
                  <ToolbarItem>
                    <Button variant="secondary" isDanger isDisabled={busy || chosen.length === 0} onClick={() => setConfirming(true)}>
                      {chosen.length ? `Clear ${plural(chosen.length, 'job')}` : 'Clear jobs'}
                    </Button>
                  </ToolbarItem>
                </>
              ) : null}
              <ToolbarItem variant="pagination" align={{ default: 'alignEnd' }}>
                <Pagination
                  isCompact itemCount={total} perPage={jobsPageSize} page={page} perPageOptions={[{ title: '10', value: 10 }]}
                  onSetPage={(_event, next) => onPage(next)} titles={{ paginationAriaLabel: 'Jobs pagination' }}
                />
              </ToolbarItem>
            </ToolbarContent>
          </Toolbar>
          <Table aria-label="Jobs" variant="compact" selectableRowCaptionText="Job">
            <Thead>
              <Tr>
                {canPublish ? <Th screenReaderText="Select" /> : null}
                <Th>State</Th><Th>Job</Th><Th>Started by</Th><Th>Queued</Th><Th>Finished</Th>
                {canPublish ? <Th screenReaderText="Actions" /> : null}
              </Tr>
            </Thead>
            <Tbody>
              {jobs.map((job, index) => {
                const progress = jobProgress(job)
                const reason = job.state === 'failed' || job.state === 'partially_succeeded' ? firstError(job.outcomes) : undefined
                const on = isChosen(job)
                const running = job.state === 'running'
                return (
                  <Tr key={job.id} isSelectable={canPublish && !running} isRowSelected={on}>
                    {canPublish ? (
                      <Td
                        title={running ? 'A running job can’t be cleared' : undefined}
                        select={{
                          rowIndex: index, isSelected: on, isDisabled: running,
                          onSelect: (_event, checked) => setSelected(checked ? [...chosen, job] : chosen.filter((row) => row.id !== job.id)),
                        }}
                      />
                    ) : null}
                    <Td dataLabel="State" modifier="nowrap">
                      {stateLabel[job.state]}
                      {progress ? (
                        <div style={{ marginTop: 4 }}>
                          {progress.percent !== undefined ? <Progress value={progress.percent} aria-label={`${jobTitle(job)} progress`} measureLocation={ProgressMeasureLocation.none} size="sm" /> : null}
                          <Content component="small" style={on && job.state === 'queued' ? { color: 'var(--pf-t--global--color--status--danger--default)' } : undefined}>
                            {on && job.state === 'queued' ? 'won’t run if cleared' : running ? `${progress.text} · can’t be cleared` : progress.text}
                          </Content>
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
      {confirming && chosen.length ? (
        <ClearJobsConfirmation
          jobs={chosen} busy={busy}
          onCancel={() => setConfirming(false)}
          onConfirm={() => { setConfirming(false); setSelected([]); onClear(chosen) }}
        />
      ) : null}
    </>
  )
}

export function ClearJobsConfirmation({ jobs, busy, onCancel, onConfirm }: { jobs: PluginImport[]; busy: boolean; onCancel: () => void; onConfirm: () => void }) {
  const copy = clearConfirmation(jobs)
  return (
    <Modal aria-labelledby="clear-jobs-title" isOpen onClose={onCancel} variant="medium">
      <ModalHeader labelId="clear-jobs-title" title={copy.title} />
      <ModalBody>
        {copy.parts.map((part, index) => (
          <Content key={index} component="p">
            {part.head ? <strong style={part.danger ? { color: 'var(--pf-t--global--color--status--danger--default)' } : undefined}>{part.head}. </strong> : null}
            {part.body}
          </Content>
        ))}
      </ModalBody>
      <ModalFooter>
        <Button variant="danger" isLoading={busy} isDisabled={busy} onClick={onConfirm}>{copy.verb}</Button>
        <Button variant="link" isDisabled={busy} onClick={onCancel}>Cancel</Button>
      </ModalFooter>
    </Modal>
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
          <AlertActionLink onClick={onJobs}>Jobs</AlertActionLink>
        </>
      )}
    >
      <Content component="p">Job {id} imports {jobWhat(job)}. {detail} Starting another would only record “already mirrored”.</Content>
    </Alert>
  )
}
