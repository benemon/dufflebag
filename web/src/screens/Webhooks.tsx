import { useCallback, useEffect, useState } from 'react'
import {
  ActionGroup, Alert, Button, Card, CardBody, Checkbox, Content, Form, FormGroup,
  EmptyState, EmptyStateActions, EmptyStateBody, EmptyStateFooter,
  Label, PageSection, TextArea, TextInput, Title,
} from '@patternfly/react-core'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'

import { useAuth } from '../auth/AuthContext'
import { RoleRestrictedButton } from '../auth/RoleRestrictedButton'
import { TypedConfirmModal } from '../components/TypedConfirmModal'
import { When } from '../components/When'
import {
  WEBHOOK_OPERATIONS, createWebhook, deleteWebhook, listWebhookDeliveries, listWebhooks,
  verifyWebhook, type Webhook, type WebhookDelivery, type WebhookOperation,
} from '../data/webhooks'

type Draft = {
  name: string
  url: string
  description: string
  secret: string
  events: WebhookOperation[]
}

const emptyDraft: Draft = { name: '', url: '', description: '', secret: '', events: [] }

export function Webhooks() {
  const { state, self, selectedOrganization, selectedProject } = useAuth()
  const tenant = selectedOrganization && selectedProject
    ? { organizationID: selectedOrganization, projectID: selectedProject }
    : null
  const token = state?.token ?? ''
  const [webhooks, setWebhooks] = useState<Webhook[]>([])
  const [loading, setLoading] = useState(true)
  const [failure, setFailure] = useState<string | null>(null)

  const reload = useCallback(async () => {
    if (!tenant || token === '') {
      setWebhooks([])
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      setWebhooks(await listWebhooks(token, tenant))
      setFailure(null)
    } catch (error: unknown) {
      setFailure(messageFor(error, 'Webhooks could not be loaded.'))
    } finally {
      setLoading(false)
    }
  }, [tenant?.organizationID, tenant?.projectID, token])

  useEffect(() => { void reload() }, [reload])

  if (!tenant) {
    return <PageSection><Alert variant="info" isInline title="Select a project to manage webhooks" /></PageSection>
  }

  return (
    <WebhooksView
      webhooks={webhooks} loading={loading} failure={failure}
      callerRole={self?.role ?? null}
      onCreate={async (draft) => {
        await createWebhook(token, tenant, {
          name: draft.name, url: draft.url, events: draft.events,
          ...(draft.description === '' ? {} : { description: draft.description }),
          ...(draft.secret === '' ? {} : { secret: draft.secret }),
        })
        await reload()
      }}
      onVerify={async (id) => { await verifyWebhook(token, tenant, id); await reload() }}
      onDelete={async (id) => { await deleteWebhook(token, tenant, id); await reload() }}
      onDeliveries={(id) => listWebhookDeliveries(token, tenant, id)}
    />
  )
}

export function WebhooksView({
  webhooks, loading, failure, callerRole, onCreate, onVerify, onDelete, onDeliveries,
}: {
  webhooks: Webhook[]
  loading: boolean
  failure: string | null
  callerRole: import('../auth/permissions').Role | null
  onCreate: (draft: Draft) => Promise<void>
  onVerify: (id: string) => Promise<void>
  onDelete: (id: string) => Promise<void>
  onDeliveries: (id: string) => Promise<WebhookDelivery[]>
}) {
  const [creating, setCreating] = useState(false)
  const [confirming, setConfirming] = useState<Webhook | null>(null)
  const [actionFailure, setActionFailure] = useState<string | null>(null)

  const run = async (work: () => Promise<void>) => {
    setActionFailure(null)
    try { await work() } catch (error: unknown) {
      setActionFailure(messageFor(error, 'The webhook action failed.'))
    }
  }

  return (
    <>
      <PageSection variant="default">
        <div style={{ display: 'flex', gap: 24, alignItems: 'flex-start' }}>
          <div style={{ flex: 1 }}>
            <Title headingLevel="h1" size="2xl">Webhooks</Title>
            <Content component="p">
              Send signed project events to an HTTP endpoint after its activation handshake succeeds.
            </Content>
          </div>
          {!creating && (loading || failure || webhooks.length > 0) ? (
            <RoleRestrictedButton
              action="configureWebhooks" callerRole={callerRole} variant="primary"
              onClick={() => setCreating(true)}
            >Create webhook</RoleRestrictedButton>
          ) : null}
        </div>
      </PageSection>
      <PageSection variant="secondary" isFilled>
        {failure ? <Alert variant="danger" isInline title="Webhooks could not be loaded"><Content component="p">{failure}</Content></Alert> : null}
        {actionFailure ? <Alert variant="danger" isInline title="The action failed"><Content component="p">{actionFailure}</Content></Alert> : null}
        {confirming ? (
          <DeleteConfirmation
            webhook={confirming} onCancel={() => setConfirming(null)}
            onConfirm={() => {
              const selected = confirming
              setConfirming(null)
              void run(() => onDelete(selected.id))
            }}
          />
        ) : null}
        {creating ? (
          <CreateWebhookForm
            callerRole={callerRole} onCancel={() => setCreating(false)}
            onCreate={async (draft) => run(async () => { await onCreate(draft); setCreating(false) })}
          />
        ) : null}
        {loading ? <Content component="p">Loading webhooks…</Content> : webhooks.length === 0 && !failure ? (
          <EmptyState titleText="No webhooks are configured" headingLevel="h2">
            <EmptyStateBody>Create a webhook to send signed project events.</EmptyStateBody>
            <EmptyStateFooter>
              <EmptyStateActions>
                {!creating ? (
                  <RoleRestrictedButton
                    action="configureWebhooks" callerRole={callerRole} variant="primary"
                    onClick={() => setCreating(true)}
                  >Create webhook</RoleRestrictedButton>
                ) : null}
              </EmptyStateActions>
            </EmptyStateFooter>
          </EmptyState>
        ) : (
          <WebhookTable
            webhooks={webhooks} callerRole={callerRole} onVerify={(id) => run(() => onVerify(id))}
            onDelete={(record) => setConfirming(record)} onDeliveries={onDeliveries}
          />
        )}
      </PageSection>
    </>
  )
}

export function CreateWebhookForm({ callerRole, onCreate, onCancel }: {
  callerRole: import('../auth/permissions').Role | null
  onCreate: (draft: Draft) => Promise<void>
  onCancel: () => void
}) {
  const [draft, setDraft] = useState<Draft>(emptyDraft)
  const [saving, setSaving] = useState(false)
  const valid = draft.name.trim() !== '' && /^https?:\/\//.test(draft.url)
  const toggle = (operation: WebhookOperation, checked: boolean) => setDraft({
    ...draft,
    events: checked ? [...draft.events, operation] : draft.events.filter((value) => value !== operation),
  })
  return (
    <Card aria-label="Create webhook" style={{ marginBottom: 16 }}>
      <CardBody>
        <Form>
          <FormGroup label="Name" isRequired fieldId="webhook-name">
            <TextInput id="webhook-name" value={draft.name} onChange={(_event, value) => setDraft({ ...draft, name: value })} />
          </FormGroup>
          <FormGroup label="URL" isRequired fieldId="webhook-url">
            <TextInput id="webhook-url" type="url" value={draft.url} onChange={(_event, value) => setDraft({ ...draft, url: value })} />
          </FormGroup>
          <FormGroup label="Description" fieldId="webhook-description">
            <TextArea id="webhook-description" value={draft.description} onChange={(_event, value) => setDraft({ ...draft, description: value })} />
          </FormGroup>
          <FormGroup label="HMAC key" fieldId="webhook-secret">
            <TextInput id="webhook-secret" type="password" value={draft.secret} onChange={(_event, value) => setDraft({ ...draft, secret: value })} />
          </FormGroup>
          <FormGroup label="Events" fieldId="webhook-events">
            <Content component="p">Leave every box clear to subscribe to all operations.</Content>
            {WEBHOOK_OPERATIONS.map((operation) => (
              <Checkbox
                key={operation} id={`event-${operation}`} label={operation}
                isChecked={draft.events.includes(operation)}
                onChange={(_event, checked) => toggle(operation, checked)}
              />
            ))}
          </FormGroup>
          <ActionGroup>
            <RoleRestrictedButton
              action="configureWebhooks" callerRole={callerRole} variant="primary"
              isDisabled={!valid || saving} isLoading={saving}
              onClick={() => { setSaving(true); void onCreate(draft).finally(() => setSaving(false)) }}
            >Create and verify</RoleRestrictedButton>
            <Button variant="link" onClick={onCancel}>Cancel</Button>
          </ActionGroup>
        </Form>
      </CardBody>
    </Card>
  )
}

function WebhookTable({ webhooks, callerRole, onVerify, onDelete, onDeliveries }: {
  webhooks: Webhook[]
  callerRole: import('../auth/permissions').Role | null
  onVerify: (id: string) => void
  onDelete: (record: Webhook) => void
  onDeliveries: (id: string) => Promise<WebhookDelivery[]>
}) {
  const [expanded, setExpanded] = useState<string | null>(null)
  const [deliveries, setDeliveries] = useState<Record<string, WebhookDelivery[]>>({})
  const [deliveryFailure, setDeliveryFailure] = useState<string | null>(null)
  const toggle = (record: Webhook) => {
    if (expanded === record.id) { setExpanded(null); return }
    setExpanded(record.id)
    setDeliveryFailure(null)
    void onDeliveries(record.id)
      .then((listed) => setDeliveries((current) => ({ ...current, [record.id]: listed })))
      .catch((error: unknown) => setDeliveryFailure(messageFor(error, 'Deliveries could not be loaded.')))
  }
  return (
    <Card><CardBody>
      <Table aria-label="Webhooks" variant="compact">
        <Thead><Tr><Th screenReaderText="Expand" /><Th>Name</Th><Th>URL</Th><Th>State</Th><Th>Events</Th><Th screenReaderText="Actions" /></Tr></Thead>
        {webhooks.map((record, index) => (
          <Tbody key={record.id} isExpanded={expanded === record.id}>
            <Tr>
              <Td expand={{ rowIndex: index, isExpanded: expanded === record.id, onToggle: () => toggle(record) }} />
              <Td dataLabel="Name">{record.name}</Td>
              <Td dataLabel="URL">{record.url}</Td>
              <Td dataLabel="State"><Label color={record.state === 'active' ? 'green' : 'orange'} isCompact>{record.state}</Label></Td>
              <Td dataLabel="Events">{record.events.length === 0 ? 'All' : record.events.length}</Td>
              <Td dataLabel="Actions">
                <RoleRestrictedButton action="configureWebhooks" callerRole={callerRole} variant="link" onClick={() => onVerify(record.id)}>Verify</RoleRestrictedButton>
                <RoleRestrictedButton action="configureWebhooks" callerRole={callerRole} variant="link" isDanger onClick={() => onDelete(record)}>Delete</RoleRestrictedButton>
              </Td>
            </Tr>
            <Tr isExpanded={expanded === record.id}><Td colSpan={6}><ExpandableRowContent>
              {record.last_verification_error ? <Alert variant="warning" isInline title="Last verification failed"><Content component="p">{record.last_verification_error}</Content></Alert> : null}
              {deliveryFailure ? <Alert variant="danger" isInline title="Deliveries could not be loaded"><Content component="p">{deliveryFailure}</Content></Alert> : null}
              <DeliveryTable deliveries={deliveries[record.id] ?? []} />
            </ExpandableRowContent></Td></Tr>
          </Tbody>
        ))}
      </Table>
    </CardBody></Card>
  )
}

export function DeliveryTable({ deliveries }: { deliveries: WebhookDelivery[] }) {
  if (deliveries.length === 0) return <Content component="p">No deliveries recorded.</Content>
  return (
    <Table aria-label="Webhook deliveries" variant="compact">
      <Thead><Tr><Th>Operation</Th><Th>Status</Th><Th>Attempts</Th><Th>Response</Th><Th>Last attempt</Th><Th>Detail</Th></Tr></Thead>
      <Tbody>{deliveries.map((delivery) => (
        <Tr key={delivery.id}>
          <Td dataLabel="Operation">{delivery.operation}</Td><Td dataLabel="Status">{delivery.status}</Td>
          <Td dataLabel="Attempts">{delivery.attempt_count}</Td><Td dataLabel="Response">{delivery.response_code ?? '—'}</Td>
          <Td dataLabel="Last attempt"><When iso={delivery.last_attempted_at} /></Td><Td dataLabel="Detail">{delivery.detail ?? '—'}</Td>
        </Tr>
      ))}</Tbody>
    </Table>
  )
}

export function DeleteConfirmation({ webhook, onConfirm, onCancel }: {
  webhook: Webhook
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <TypedConfirmModal
      title={`Delete ${webhook.name}?`}
      body={<Content component="p">Delivery history for this webhook will also be deleted.</Content>}
      expected={webhook.name}
      verb="Delete webhook"
      busy={false}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  )
}

function messageFor(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}
