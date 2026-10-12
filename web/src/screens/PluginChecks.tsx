import { useState } from 'react'
import { Button, Card, CardBody, CardTitle, Content, EmptyState, EmptyStateBody, Label, Switch, ToggleGroup, ToggleGroupItem } from '@patternfly/react-core'
import { Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'

import { activeJobFor, sourceLabel, type Plugin, type PluginImport } from '../data/pluginRegistry'

export type CheckFilter = 'all' | 'newer' | 'failed' | 'off'

// What a plugin's last check came to, in one word for the tabs and one line for the row.
export function checkState(plugin: Plugin, unfinished: PluginImport[]): { kind: 'newer' | 'failed' | 'ok' | 'off' | 'pending'; text: string } {
  const job = activeJobFor(unfinished, plugin.name)
  if (!plugin.update_check.enabled) return { kind: 'off', text: 'Checks off' }
  if (plugin.update_check.error) return { kind: 'failed', text: plugin.update_check.error }
  if (plugin.update_available) return { kind: 'newer', text: job ? `${job.changes.length ? 'Sync' : 'Import'} ${job.state}` : '' }
  if (!plugin.update_check.checked_at) return { kind: 'pending', text: 'Not checked yet' }
  return { kind: 'ok', text: 'Up to date' }
}

const when = (value: string | undefined) => (value ? new Date(value).toLocaleString() : '—')

export function PluginChecksFacet({ plugins, unfinished, canPublish, busy, onToggle, onOpenPlugin }: {
  plugins: Plugin[]
  unfinished: PluginImport[]
  canPublish: boolean
  busy: boolean
  onToggle: (name: string, enabled: boolean) => void
  onOpenPlugin: (name: string) => void
}) {
  const [filter, setFilter] = useState<CheckFilter>('all')
  const checkable = plugins.filter((plugin) => plugin.source.kind !== 'upload')
  const uploads = plugins.length - checkable.length
  const states = new Map(checkable.map((plugin) => [plugin.name, checkState(plugin, unfinished)]))
  const count = (kind: CheckFilter) => (kind === 'all' ? checkable.length : checkable.filter((plugin) => states.get(plugin.name)?.kind === kind).length)
  const shown = checkable.filter((plugin) => filter === 'all' || states.get(plugin.name)?.kind === filter)
  const uploadsNote = uploads ? <Content component="small">{uploads} uploaded {uploads === 1 ? 'plugin isn’t' : 'plugins aren’t'} checked: an upload has no upstream.</Content> : null
  if (checkable.length === 0) {
    return (
      <EmptyState titleText="No plugin to check" headingLevel="h2">
        <EmptyStateBody>Checks ask a plugin’s source for a newer stable release, so only imported plugins can be checked. {uploadsNote}</EmptyStateBody>
      </EmptyState>
    )
  }
  return (
    <Card>
      <CardTitle>Checks</CardTitle>
      <CardBody>
        <ToggleGroup aria-label="Check results">
          {([['all', 'All'], ['newer', 'Newer found'], ['failed', 'Failed'], ['off', 'Off']] as [CheckFilter, string][]).map(([kind, text]) => (
            <ToggleGroupItem key={kind} text={`${text} · ${count(kind)}`} buttonId={`checks-${kind}`} isSelected={filter === kind} onChange={() => setFilter(kind)} />
          ))}
        </ToggleGroup>
        {uploadsNote ? <div style={{ marginTop: 8 }}>{uploadsNote}</div> : null}
        <Table aria-label="Update checks" variant="compact">
          <Thead>
            <Tr><Th>Plugin</Th><Th>Source</Th><Th>Check</Th><Th>Last checked</Th><Th>Result</Th><Th>Next due</Th></Tr>
          </Thead>
          <Tbody>
            {shown.map((plugin) => {
              const state = states.get(plugin.name) ?? checkState(plugin, unfinished)
              return (
                <Tr key={plugin.name}>
                  <Td dataLabel="Plugin"><Button variant="link" isInline onClick={() => onOpenPlugin(plugin.name)}>{plugin.name}</Button></Td>
                  <Td dataLabel="Source">
                    <Label isCompact>{sourceLabel(plugin.source)}</Label>
                    {plugin.source.repository ? <> <code>{plugin.source.repository}</code></> : null}
                  </Td>
                  <Td dataLabel="Check">
                    <Switch
                      id={`check-${plugin.name}`} aria-label={`Check ${plugin.name} for updates`} isChecked={plugin.update_check.enabled}
                      isDisabled={busy || !canPublish} onChange={(_event, enabled) => onToggle(plugin.name, enabled)}
                    />
                  </Td>
                  <Td dataLabel="Last checked" modifier="nowrap">{when(plugin.update_check.checked_at)}</Td>
                  <Td dataLabel="Result">
                    {state.kind === 'newer' ? <Label isCompact color="purple">Newer · {plugin.update_check.latest}</Label> : null}
                    {state.kind === 'failed' ? <Label isCompact color="red">Failed</Label> : null}
                    {state.text ? <Content component="small" style={{ display: state.kind === 'newer' || state.kind === 'failed' ? 'block' : undefined, color: state.kind === 'failed' ? 'var(--pf-t--global--color--status--danger--default)' : undefined }}>{state.text}</Content> : null}
                  </Td>
                  <Td dataLabel="Next due" modifier="nowrap">{plugin.update_check.enabled ? when(plugin.update_check.next_check_at) : '—'}</Td>
                </Tr>
              )
            })}
          </Tbody>
        </Table>
        {shown.length === 0 ? <Content component="p">No plugins match.</Content> : null}
      </CardBody>
    </Card>
  )
}
