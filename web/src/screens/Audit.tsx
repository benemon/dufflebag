import { useState } from 'react'
import {
  Alert, Button, Card, CardBody, CardTitle, Content, Form, FormGroup, Label,
  PageSection, TextInput, Title,
} from '@patternfly/react-core'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'

import {
  auditRefusalHint, createAuditTarget, deleteAuditTarget, useAuditTargets,
  type AuditTarget,
} from '../data/audit'
import { RoleRestrictedButton } from '../auth/RoleRestrictedButton'
import type { Role } from '../auth/permissions'
import { TypedConfirmModal } from '../components/TypedConfirmModal'

export function Audit() {
  return <AuditView {...useAuditTargets()} />
}

type ViewProps = ReturnType<typeof useAuditTargets>

export function AuditView({ targets, loading, failure, reload, token, callerRole }: ViewProps) {
  const [creating, setCreating] = useState(false)
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [confirming, setConfirming] = useState<AuditTarget | null>(null)

  async function run(work: () => Promise<void>) {
    setActionFailure(null)
    try {
      await work()
      await reload()
    } catch (err: unknown) {
      setActionFailure(
        auditRefusalHint(err) ?? (err instanceof Error ? err.message : 'The action failed.'),
      )
    }
  }

  const remove = (target: AuditTarget) => {
    setConfirming(target)
  }

  const atLimit = auditTargetLimitReached(targets, loading)

  return (
    <>
      <PageSection variant="default">
        <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start' }}>
          <div style={{ flex: 1 }}>
            <Title headingLevel="h1" size="2xl">Audit</Title>
            <Content component="p">
              Audit targets are append-only files that receive every request and response record.
              Up to three can be active at once. This configuration is visible only to root.
            </Content>
          </div>
          {!creating && !loading ? (
            <RoleRestrictedButton
              action="configureAudit"
              callerRole={callerRole}
              variant="primary"
              isDisabled={atLimit}
              onClick={() => setCreating(true)}
            >
              Add target
            </RoleRestrictedButton>
          ) : null}
        </div>
      </PageSection>

      <PageSection variant="secondary" isFilled>
        {failure ? (
          <Alert variant="danger" isInline title="Audit targets could not be loaded">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        {actionFailure ? (
          <Alert variant="danger" isInline title="The action was refused">
            <Content component="p">{actionFailure}</Content>
          </Alert>
        ) : null}

        {atLimit ? (
          <Alert
            variant="info"
            isInline
            title="Three audit targets are already configured"
            style={{ marginBottom: 16 }}
          >
            <Content component="p">
              Add target is disabled while all three slots are occupied. Remove a target to free a slot.
            </Content>
          </Alert>
        ) : null}

        {confirming && lastTargetRemovalNeedsConfirmation(targets) ? (
          <LastTargetConfirmation
            target={confirming}
            callerRole={callerRole}
            onCancel={() => setConfirming(null)}
            onConfirm={() => {
              const target = confirming
              setConfirming(null)
              void run(async () => {
                if (token) await deleteAuditTarget(token, target.id)
              })
            }}
          />
        ) : confirming ? (
          <TargetRemovalConfirmation
            target={confirming}
            callerRole={callerRole}
            onCancel={() => setConfirming(null)}
            onConfirm={() => {
              const target = confirming
              setConfirming(null)
              void run(async () => {
                if (token) await deleteAuditTarget(token, target.id)
              })
            }}
          />
        ) : null}

        {creating ? (
          <CreateAuditTargetForm
            callerRole={callerRole}
            onCancel={() => setCreating(false)}
            onCreate={async (path) => {
              await run(async () => {
                if (!token) return
                await createAuditTarget(token, path)
                setCreating(false)
              })
            }}
          />
        ) : null}

        {loading ? (
          <Content component="p">Loading audit targets…</Content>
        ) : targets.length === 0 && !failure ? (
          <Alert variant="info" isInline title="No audit targets are configured">
            <Content component="p">This instance is not recording audit events to a file.</Content>
          </Alert>
        ) : (
          <Card>
            <CardBody>
              <AuditTargetTable targets={targets} callerRole={callerRole} onRemove={remove} />
            </CardBody>
          </Card>
        )}
      </PageSection>
    </>
  )
}

export function auditTargetLimitReached(targets: AuditTarget[], loading: boolean): boolean {
  return !loading && targets.length >= 3
}

export function lastTargetRemovalNeedsConfirmation(targets: AuditTarget[]): boolean {
  return targets.length === 1
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  const rendered = value < 10 ? value.toFixed(1).replace(/\.0$/, '') : value.toFixed(0)
  return `${rendered} ${units[unit]}`
}

export function LastTargetConfirmation({
  target, onConfirm, onCancel,
}: {
  target: AuditTarget
  callerRole: Role | null
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <TypedConfirmModal
      title="Remove the last audit target?"
      expected={target.path}
      verb="Remove last target"
      busy={false}
      onConfirm={onConfirm}
      onCancel={onCancel}
      body={<Content component="p">
        Removing {target.path} stops this instance recording audit events entirely until another
        target is added.
      </Content>}
    />
  )
}

export function TargetRemovalConfirmation({
  target, callerRole, onConfirm, onCancel,
}: {
  target: AuditTarget
  callerRole: Role | null
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <Alert variant="warning" isInline title={`Remove ${target.path}?`} style={{ marginBottom: 16 }}>
      <RoleRestrictedButton
        action="configureAudit" callerRole={callerRole} variant="danger" onClick={onConfirm}
      >
        Remove target
      </RoleRestrictedButton>
      <Button variant="link" onClick={onCancel}>Cancel</Button>
    </Alert>
  )
}

export function CreateAuditTargetForm({
  callerRole, onCreate, onCancel,
}: {
  callerRole: Role | null
  onCreate: (path: string) => Promise<void>
  onCancel: () => void
}) {
  const [path, setPath] = useState('')
  const [submitting, setSubmitting] = useState(false)

  return (
    <Card>
      <CardTitle>New audit target</CardTitle>
      <CardBody>
        <Form>
          <FormGroup label="File path" isRequired fieldId="audit-target-path">
            <TextInput
              id="audit-target-path"
              value={path}
              onChange={(_event, value) => setPath(value)}
              placeholder="/var/log/dufflebag/audit.log"
            />
          </FormGroup>
          <RoleRestrictedButton
            action="configureAudit"
            callerRole={callerRole}
            variant="primary"
            isDisabled={path.trim() === '' || submitting}
            isLoading={submitting}
            onClick={() => {
              setSubmitting(true)
              void onCreate(path.trim()).finally(() => setSubmitting(false))
            }}
          >
            Add target
          </RoleRestrictedButton>
          <Button variant="link" onClick={onCancel}>Cancel</Button>
        </Form>
      </CardBody>
    </Card>
  )
}

export function AuditTargetTable({
  targets, callerRole, onRemove,
}: {
  targets: AuditTarget[]
  callerRole: Role | null
  onRemove: (target: AuditTarget) => void
}) {
  const [expanded, setExpanded] = useState<string | null>(null)

  return (
    <Table aria-label="Audit targets" variant="compact">
      <Thead>
        <Tr>
          <Th screenReaderText="Expand" />
          <Th>Path</Th>
          <Th>Created</Th>
          <Th>Current file</Th>
          <Th>Space remaining</Th>
          <Th>Last reopened</Th>
          <Th>Status</Th>
          <Th screenReaderText="Actions" />
        </Tr>
      </Thead>
      {targets.map((target, index) => (
        <Tbody key={target.id} isExpanded={expanded === target.id}>
          <Tr>
            <Td
              expand={{
                rowIndex: index,
                isExpanded: expanded === target.id,
                onToggle: () => setExpanded(expanded === target.id ? null : target.id),
              }}
            />
            <Td dataLabel="Path">{target.path}</Td>
            <Td dataLabel="Created">{target.created_at}</Td>
            <Td dataLabel="Current file">
              <MeasurementValue target={target} field="current_file_size_bytes" />
            </Td>
            <Td dataLabel="Space remaining">
              <MeasurementValue target={target} field="filesystem_free_bytes" />
            </Td>
            <Td dataLabel="Last reopened">{target.last_reopened_at ?? 'Never'}</Td>
            <Td dataLabel="Status">
              <Label color={target.status === 'healthy' ? 'green' : 'red'} isCompact>
                {target.status}
              </Label>
            </Td>
            <Td dataLabel="Actions">
              <RoleRestrictedButton
                action="configureAudit" callerRole={callerRole} variant="link" isDanger
                onClick={() => onRemove(target)}
              >
                Remove
              </RoleRestrictedButton>
            </Td>
          </Tr>
          <Tr isExpanded={expanded === target.id}>
            <Td colSpan={8}>
              <ExpandableRowContent>
                <AuditTargetHealth target={target} />
              </ExpandableRowContent>
            </Td>
          </Tr>
        </Tbody>
      ))}
    </Table>
  )
}

function MeasurementValue({
  target, field,
}: {
  target: AuditTarget
  field: 'current_file_size_bytes' | 'filesystem_free_bytes'
}) {
  if (target.measurement.state === 'unavailable') return <>Unavailable</>
  const bytes = target.measurement[field]
  return <span title={`${bytes} bytes`}>{formatBytes(bytes)}</span>
}

function AuditTargetHealth({ target }: { target: AuditTarget }) {
  return (
    <Table aria-label={`Health for ${target.path}`} variant="compact">
      <Thead>
        <Tr><Th>Health field</Th><Th>Value</Th></Tr>
      </Thead>
      <Tbody>
        <Tr><Td dataLabel="Health field">Status</Td><Td dataLabel="Value">{target.status}</Td></Tr>
        <Tr><Td dataLabel="Health field">Since</Td><Td dataLabel="Value">{target.since ?? '—'}</Td></Tr>
        <Tr>
          <Td dataLabel="Health field">Consecutive failures</Td>
          <Td dataLabel="Value">{target.consecutive_failures}</Td>
        </Tr>
        <Tr>
          <Td dataLabel="Health field">Cumulative failures</Td>
          <Td dataLabel="Value">{target.cumulative_failures}</Td>
        </Tr>
        <Tr>
          <Td dataLabel="Health field">Last failure</Td>
          <Td dataLabel="Value">{target.last_failure_at ?? '—'}</Td>
        </Tr>
      </Tbody>
    </Table>
  )
}
