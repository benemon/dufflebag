import { useCallback, useEffect, useState, type Ref } from 'react'
import {
  ActionGroup, Alert, Button, Card, CardBody, Checkbox, Content, Form, FormGroup,
  Dropdown, DropdownItem, DropdownList,
  EmptyState, EmptyStateActions, EmptyStateBody, EmptyStateFooter,
  Label, MenuToggle, MenuToggleCheckbox, PageSection, Pagination, TextArea, TextInput,
  Toolbar, ToolbarContent, ToolbarItem,
} from '@patternfly/react-core'
import type { MenuToggleElement } from '@patternfly/react-core'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'

import { useAuth } from '../auth/AuthContext'
import { RoleRestrictedButton } from '../auth/RoleRestrictedButton'
import { ScreenHeader } from '../components/ScreenHeader'
import { updateBulkSelection } from '../components/BulkSelection'
import { TypedConfirmModal } from '../components/TypedConfirmModal'
import { When } from '../components/When'
import { SkeletonRows } from '../components/Loading'
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
      onBulkDelete={(id) => deleteWebhook(token, tenant, id)}
      onRefresh={reload}
      onDeliveries={(id) => listWebhookDeliveries(token, tenant, id)}
    />
  )
}

export function WebhooksView({
  webhooks, loading, failure, callerRole, onCreate, onVerify, onDelete, onBulkDelete,
  onRefresh, onDeliveries,
}: {
  webhooks: Webhook[]
  loading: boolean
  failure: string | null
  callerRole: import('../auth/permissions').Role | null
  onCreate: (draft: Draft) => Promise<void>
  onVerify: (id: string) => Promise<void>
  onDelete: (id: string) => Promise<void>
  onBulkDelete: (id: string) => Promise<void>
  onRefresh: () => void | Promise<void>
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
      <ScreenHeader
        title="Webhooks"
        onRefresh={onRefresh}
        refreshing={loading}
        description="Send signed project events to an HTTP endpoint after its activation handshake succeeds."
        actions={!creating && (loading || failure || webhooks.length > 0) ? (
          <RoleRestrictedButton
            action="configureWebhooks" callerRole={callerRole} variant="primary"
            onClick={() => setCreating(true)}
          >Create webhook</RoleRestrictedButton>
        ) : null}
      />
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
        {loading ? (
          <SkeletonRows screenreaderText="Loading webhooks…" />
        ) : webhooks.length === 0 && !failure ? (
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
            onDelete={(record) => setConfirming(record)} onBulkDelete={onBulkDelete}
            onRefresh={onRefresh} onDeliveries={onDeliveries}
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

function WebhookTable({
  webhooks, callerRole, onVerify, onDelete, onBulkDelete, onRefresh, onDeliveries,
}: {
  webhooks: Webhook[]
  callerRole: import('../auth/permissions').Role | null
  onVerify: (id: string) => void
  onDelete: (record: Webhook) => void
  onBulkDelete: (id: string) => Promise<void>
  onRefresh: () => void | Promise<void>
  onDeliveries: (id: string) => Promise<WebhookDelivery[]>
}) {
  const [expanded, setExpanded] = useState<string | null>(null)
  const [deliveries, setDeliveries] = useState<Record<string, WebhookDelivery[]>>({})
  const [deliveryFailure, setDeliveryFailure] = useState<string | null>(null)
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const [selected, setSelected] = useState<string[]>([])
  const [bulkSelectOpen, setBulkSelectOpen] = useState(false)
  const [bulkDeleting, setBulkDeleting] = useState(false)
  const lastPage = Math.max(1, Math.ceil(webhooks.length / perPage))
  const currentPage = Math.min(page, lastPage)
  const first = (currentPage - 1) * perPage
  const visibleWebhooks = webhookPage(webhooks, currentPage, perPage)
  const visibleIDs = visibleWebhooks.map((webhook) => webhook.id)
  const selectedWebhooks = webhooks.filter((webhook) => selected.includes(webhook.id))
  const visibleSelected = visibleIDs.filter((id) => selected.includes(id))
  const allVisibleSelected = visibleIDs.length > 0 && visibleSelected.length === visibleIDs.length

  useEffect(() => {
    const current = new Set(webhooks.map((webhook) => webhook.id))
    setSelected((selectedIDs) => selectedIDs.filter((id) => current.has(id)))
  }, [webhooks])

  const toggle = (record: Webhook) => {
    if (expanded === record.id) { setExpanded(null); return }
    setExpanded(record.id)
    setDeliveryFailure(null)
    void onDeliveries(record.id)
      .then((listed) => setDeliveries((current) => ({ ...current, [record.id]: listed })))
      .catch((error: unknown) => setDeliveryFailure(messageFor(error, 'Deliveries could not be loaded.')))
  }
  const setCurrentPage = (_event: unknown, nextPage: number) => {
    setPage(nextPage)
    setExpanded(null)
  }
  const selectPerPage = (_event: unknown, nextPerPage: number) => {
    setPerPage(nextPerPage)
    setPage(1)
    setExpanded(null)
  }
  return (
    <Card><CardBody>
      <Toolbar id="webhooks-toolbar">
        <ToolbarContent>
          <ToolbarItem>
            <Dropdown
              role="menu"
              isOpen={bulkSelectOpen}
              onOpenChange={setBulkSelectOpen}
              onSelect={(_event, value) => {
                if (value === 'none') setSelected([])
                if (value === 'page') setSelected((current) => updateBulkSelection(
                  current, visibleIDs, !allVisibleSelected,
                ))
                if (value === 'all') setSelected(
                  selected.length === webhooks.length ? [] : webhooks.map((webhook) => webhook.id),
                )
                setBulkSelectOpen(false)
              }}
              toggle={(toggleRef: Ref<MenuToggleElement>) => (
                <MenuToggle
                  ref={toggleRef}
                  isExpanded={bulkSelectOpen}
                  onClick={() => setBulkSelectOpen(!bulkSelectOpen)}
                  aria-label="Select webhooks"
                  splitButtonItems={[
                    <MenuToggleCheckbox
                      id="webhooks-bulk-select-checkbox"
                      key="webhooks-bulk-select-checkbox"
                      aria-label={selected.length > 0 ? 'Deselect all webhooks' : 'Select all webhooks'}
                      isChecked={selected.length === webhooks.length
                        ? true
                        : selected.length > 0 ? null : false}
                      onChange={(checked) => setSelected(
                        checked ? webhooks.map((webhook) => webhook.id) : [],
                      )}
                    >
                      {selected.length > 0 ? `${selected.length} selected` : null}
                    </MenuToggleCheckbox>,
                  ]}
                />
              )}
            >
              <DropdownList>
                <DropdownItem value="none">Select none (0 items)</DropdownItem>
                <DropdownItem value="page">Select page ({visibleWebhooks.length} items)</DropdownItem>
                <DropdownItem value="all">Select all ({webhooks.length} items)</DropdownItem>
              </DropdownList>
            </Dropdown>
          </ToolbarItem>
          {selected.length > 0 ? (
            <ToolbarItem>
              <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                {selected.length} selected
                <RoleRestrictedButton
                  action="configureWebhooks" callerRole={callerRole}
                  variant="danger" onClick={() => setBulkDeleting(true)}
                >Delete</RoleRestrictedButton>
              </span>
            </ToolbarItem>
          ) : null}
          <ToolbarItem variant="pagination" align={{ default: 'alignEnd' }}>
            <Pagination
              itemCount={webhooks.length} page={currentPage} perPage={perPage}
              onSetPage={setCurrentPage} onPerPageSelect={selectPerPage} isCompact
            />
          </ToolbarItem>
        </ToolbarContent>
      </Toolbar>
      <Table
        aria-label="Webhooks" variant="compact" selectableRowCaptionText="Webhook"
      >
        <Thead><Tr>
          <Th screenReaderText="Expand" />
          <Th
            aria-label="Select page"
            select={{
              isSelected: allVisibleSelected,
              isIndeterminate: visibleSelected.length > 0 && !allVisibleSelected,
              onSelect: (_event, isSelecting) => setSelected((current) =>
                updateBulkSelection(current, visibleIDs, isSelecting)),
            }}
          />
          <Th>Name</Th><Th>URL</Th><Th>State</Th><Th>Events</Th><Th screenReaderText="Actions" />
        </Tr></Thead>
        {visibleWebhooks.map((record, index) => (
          <Tbody key={record.id} isExpanded={expanded === record.id}>
            <Tr isSelectable isRowSelected={selected.includes(record.id)}>
              <Td expand={{ rowIndex: first + index, isExpanded: expanded === record.id, onToggle: () => toggle(record) }} />
              <Td select={{
                rowIndex: first + index,
                isSelected: selected.includes(record.id),
                onSelect: (_event, isSelecting) => setSelected((current) =>
                  updateBulkSelection(current, [record.id], isSelecting)),
              }} />
              <Td dataLabel="Name">{record.name}</Td>
              <Td dataLabel="URL">{record.url}</Td>
              <Td dataLabel="State"><Label status={record.state === 'active' ? 'success' : 'warning'} isCompact>{record.state}</Label></Td>
              <Td dataLabel="Events">{record.events.length === 0 ? 'All' : record.events.length}</Td>
              <Td dataLabel="Actions">
                <RoleRestrictedButton action="configureWebhooks" callerRole={callerRole} variant="link" onClick={() => onVerify(record.id)}>Verify</RoleRestrictedButton>
                <RoleRestrictedButton action="configureWebhooks" callerRole={callerRole} variant="link" isDanger onClick={() => onDelete(record)}>Delete</RoleRestrictedButton>
              </Td>
            </Tr>
            <Tr isExpanded={expanded === record.id}><Td colSpan={7}><ExpandableRowContent>
              {record.last_verification_error ? <Alert variant="warning" isInline title="Last verification failed"><Content component="p">{record.last_verification_error}</Content></Alert> : null}
              {deliveryFailure ? <Alert variant="danger" isInline title="Deliveries could not be loaded"><Content component="p">{deliveryFailure}</Content></Alert> : null}
              <DeliveryTable deliveries={deliveries[record.id] ?? []} />
            </ExpandableRowContent></Td></Tr>
          </Tbody>
        ))}
      </Table>
      <Pagination
        itemCount={webhooks.length} page={currentPage} perPage={perPage}
        onSetPage={setCurrentPage} onPerPageSelect={selectPerPage}
        variant="bottom" dropDirection="up"
      />
      {bulkDeleting ? (
        <BulkWebhookDeleteModal
          webhooks={selectedWebhooks}
          onDelete={onBulkDelete}
          onFinished={async (allSucceeded) => {
            await onRefresh()
            if (allSucceeded) setSelected([])
          }}
          onClose={() => setBulkDeleting(false)}
        />
      ) : null}
    </CardBody></Card>
  )
}

export type BulkWebhookResult = {
  webhook: Webhook
  status: 'success' | 'refused'
  message: string
}

export async function runBulkWebhookDelete(
  webhooks: Webhook[], operation: (webhook: Webhook) => Promise<void>,
): Promise<BulkWebhookResult[]> {
  const results: BulkWebhookResult[] = []
  for (const webhook of webhooks) {
    try {
      await operation(webhook)
      results.push({ webhook, status: 'success', message: 'Success' })
    } catch (err: unknown) {
      results.push({
        webhook,
        status: 'refused',
        message: err instanceof Error ? err.message : 'The action failed.',
      })
    }
  }
  return results
}

function BulkWebhookDeleteModal({ webhooks, onDelete, onFinished, onClose }: {
  webhooks: Webhook[]
  onDelete: (id: string) => Promise<void>
  onFinished: (allSucceeded: boolean) => void | Promise<void>
  onClose: () => void
}) {
  const [submitting, setSubmitting] = useState(false)
  const [results, setResults] = useState<BulkWebhookResult[] | null>(null)

  const confirm = async () => {
    setSubmitting(true)
    const nextResults = await runBulkWebhookDelete(
      webhooks, (webhook) => onDelete(webhook.id),
    )
    setResults(nextResults)
    setSubmitting(false)
    const allSucceeded = nextResults.every((result) => result.status === 'success')
    await onFinished(allSucceeded)
    if (allSucceeded) onClose()
  }

  return (
    <BulkWebhookDeleteModalView
      webhooks={webhooks} submitting={submitting} results={results}
      onConfirm={confirm} onClose={onClose}
    />
  )
}

export function BulkWebhookDeleteModalView({
  webhooks, submitting, results, onConfirm, onClose,
}: {
  webhooks: Webhook[]
  submitting: boolean
  results: BulkWebhookResult[] | null
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  return (
    <TypedConfirmModal
      variant="medium"
      title={`Delete ${webhooks.length} ${webhooks.length === 1 ? 'webhook' : 'webhooks'}`}
      expected="delete"
      verb="Delete"
      busy={submitting}
      confirmDisabled={results !== null}
      onConfirm={onConfirm}
      onCancel={onClose}
      body={<>
        <Content component="p">All {webhooks.length} selected webhooks will be deleted.</Content>
        <Content component="p">Selected webhooks:</Content>
        <Content component="ul">
          {webhooks.map((webhook) => <li key={webhook.id}>{webhook.name}</li>)}
        </Content>
        {results ? (
          <Content component="ul" aria-label="Delete results">
            {results.map((result) => (
              <li key={result.webhook.id}>
                <Label color={result.status === 'success' ? 'green' : 'red'} isCompact>
                  {result.status === 'success' ? 'Success' : 'Refused'}
                </Label>{' '}
                {result.webhook.name} — {result.message}
              </li>
            ))}
          </Content>
        ) : null}
      </>}
    />
  )
}

export function webhookPage(webhooks: Webhook[], page: number, perPage: number): Webhook[] {
  const first = (page - 1) * perPage
  return webhooks.slice(first, first + perPage)
}

export function DeliveryTable({ deliveries }: { deliveries: WebhookDelivery[] }) {
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const lastPage = Math.max(1, Math.ceil(deliveries.length / perPage))
  const currentPage = Math.min(page, lastPage)
  const first = (currentPage - 1) * perPage
  const visibleDeliveries = deliveries.slice(first, first + perPage)
  if (deliveries.length === 0) return <Content component="p">No deliveries recorded.</Content>
  return (
    <>
      <Pagination
        itemCount={deliveries.length}
        page={currentPage}
        perPage={perPage}
        onSetPage={(_event, nextPage) => setPage(nextPage)}
        onPerPageSelect={(_event, nextPerPage) => {
          setPerPage(nextPerPage)
          setPage(1)
        }}
        isCompact
      />
      <Table aria-label="Webhook deliveries" variant="compact">
        <Thead><Tr><Th>Operation</Th><Th>Status</Th><Th>Attempts</Th><Th>Response</Th><Th>Last attempt</Th><Th>Detail</Th></Tr></Thead>
        <Tbody>{visibleDeliveries.map((delivery) => (
          <Tr key={delivery.id}>
            <Td dataLabel="Operation">{delivery.operation}</Td><Td dataLabel="Status">{delivery.status}</Td>
            <Td dataLabel="Attempts">{delivery.attempt_count}</Td><Td dataLabel="Response">{delivery.response_code ?? '—'}</Td>
            <Td dataLabel="Last attempt"><When iso={delivery.last_attempted_at} /></Td><Td dataLabel="Detail">{delivery.detail ?? '—'}</Td>
          </Tr>
        ))}</Tbody>
      </Table>
      <Pagination
        itemCount={deliveries.length}
        page={currentPage}
        perPage={perPage}
        onSetPage={(_event, nextPage) => setPage(nextPage)}
        onPerPageSelect={(_event, nextPerPage) => {
          setPerPage(nextPerPage)
          setPage(1)
        }}
        variant="bottom"
        dropDirection="up"
      />
    </>
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
