import { useEffect, useState, type Ref } from 'react'
import {
  ActionGroup, Alert, Button, Card, CardBody, CardTitle, ClipboardCopy, Content, Form, FormGroup,
  Dropdown, DropdownItem, DropdownList,
  EmptyState, EmptyStateActions, EmptyStateBody, EmptyStateFooter,
  FormSelect, FormSelectOption, Label, MenuToggle, MenuToggleCheckbox, Modal, ModalBody,
  ModalFooter, ModalHeader, PageSection, Pagination, Popover, Radio, TextInput,
  Toolbar, ToolbarContent, ToolbarItem,
} from '@patternfly/react-core'
import type { MenuToggleElement } from '@patternfly/react-core'
import { ExpandableRowContent, Table, Tbody, Td, Th, Thead, Tr } from '@patternfly/react-table'

import {
  createPrincipal, deletePrincipal, everUsed, grantableRoles, issueSecret, refusalHint,
  revokeSecret, ROLE_DESCRIPTIONS, usePrincipals,
  type IssuedCredential, type Principal, type Role, type SecretMetadata, type Standing,
} from '../data/principals'
import { RoleRestrictedButton } from '../auth/RoleRestrictedButton'
import { ScreenHeader } from '../components/ScreenHeader'
import { updateBulkSelection } from '../components/BulkSelection'
import { TypedConfirmModal } from '../components/TypedConfirmModal'
import { CopyableIdentifier } from '../components/CopyableIdentifier'
import { SkeletonRows } from '../components/Loading'
import { When } from '../components/When'

const ROOT_KEYSTONE_REASON = 'A root principal must keep one secret that never expires'

/**
 * Service principals — the console's only write surface (ADR-0012, amended).
 *
 * Everything here mints or destroys a credential, so two rules shape it:
 *
 * A SECRET IS SHOWN ONCE. It is argon2id-hashed on write and cannot be
 * recovered, only replaced, so the modal that displays one does not disappear
 * on a stray click — it stays until the operator explicitly closes it. The
 * previous version of this screen invented credentials in the
 * browser and showed them under the same warning; the warning is now true.
 *
 * ROLES, NOT SCOPES. The design mockups model authority as token claims that can
 * be edited later. ADR-0019 decided otherwise: one nested role, resolved from
 * storage per request so revocation is immediate.
 */
export function Principals() {
  return <PrincipalsView {...usePrincipals()} />
}

type ViewProps = ReturnType<typeof usePrincipals>

export function PrincipalsView({
  principals, loading, failure, reload, selfID, callerRole, token, organizationID, projectID,
  bucketID = null, bucketName = null,
}: ViewProps) {
  const [creating, setCreating] = useState(false)
  const [issuing, setIssuing] = useState<Principal | null>(null)
  const [issued, setIssued] = useState<IssuedCredential | null>(null)
  const [issueFailure, setIssueFailure] = useState<string | null>(null)
  const [actionFailure, setActionFailure] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<Principal | null>(null)
  const [revoking, setRevoking] = useState<{
    principal: Principal
    secret: SecretMetadata
  } | null>(null)

  // The picker selection IS the scope, of the listing and of anything created
  // here (duf-4qr): nothing selected is the platform, an organisation with the
  // blank project row is that organisation, a whole pair is that project. The
  // form never asks for tenancy, so it cannot express a pairing validBinding
  // would refuse — only root above every tenancy, never root inside one, and
  // no role above publisher at bucket standing.
  const standing: Standing = organizationID
    ? (projectID ? (bucketID ? 'bucket' : 'project') : 'organization')
    : 'platform'
  const offerable = grantableRoles(standing, callerRole)

  async function run(work: () => Promise<void>) {
    setActionFailure(null)
    try {
      await work()
      await reload()
    } catch (err: unknown) {
      setActionFailure(
        refusalHint(err) ?? (err instanceof Error ? err.message : 'The action failed.'),
      )
    }
  }

  return (
    <>
      <ScreenHeader
        title="Principals"
        onRefresh={reload}
        refreshing={loading}
        description={(
          <>
            Service principals authenticate with a client id and secret, and receive a
            short-lived token. Two secrets can be active at once, so rotation needs no window.
            New ones are created in the context selected above.
          </>
        )}
        actions={!creating && (loading || failure || principals.length > 0) ? (
          <RoleRestrictedButton
            action="managePrincipals" callerRole={callerRole}
            variant="primary" onClick={() => setCreating(true)}
          >
            Create principal
          </RoleRestrictedButton>
        ) : null}
      />

      <PageSection variant="secondary" isFilled>
        {failure ? (
          <Alert variant="danger" isInline title="Principals could not be loaded">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        {actionFailure ? (
          <Alert variant="danger" isInline title="The action was refused">
            <Content component="p">{actionFailure}</Content>
          </Alert>
        ) : null}

        {/*
          Rendered from the listing, not from standing: it appears exactly when
          a disabled Revoke is on screen, and never otherwise. The same
          predicate also gives that control its local explanation, and it means
          the notice cannot become furniture, since issuing a second root
          secret removes both at once.

          The `loading` guard is part of that coupling, not decoration. Reload
          marks itself loading WITHOUT clearing the previous scope's principals,
          so the table is replaced by "Loading principals…" while this array
          still describes the scope just navigated away from. Without the guard
          the alert outlives the rows it explains, which is the same disagreement
          the predicate above avoids.
        */}
        {!loading && principals.some(rootsLastSecret) ? (
          <Alert
            variant="info"
            isInline
            title={ROOT_KEYSTONE_REASON}
            style={{ marginBottom: 16 }}
          >
            <Content component="p">
              Revoking it is disabled while it is the only one without an expiry. Nothing
              sits above root to issue a replacement, so a root left holding only expiring
              secrets is locked out on a timer. Issue another never-expiring secret first,
              then revoke this one.
            </Content>
          </Alert>
        ) : null}

        {creating ? (
          <CreatePrincipalForm
            roles={offerable}
            callerRole={callerRole}
            onCancel={() => setCreating(false)}
            onCreate={async (name, role) => {
              if (!token) return
              await run(async () => {
                // Creation alone. The new principal appears in the listing
                // holding no secrets, and the operator issues one from its row
                // — the same action that adds a second secret (duf-4ac).
                await createPrincipal(token, { name, role, organizationID, projectID, bucketID })
                setCreating(false)
              })
            }}
          />
        ) : null}

        {loading ? (
          <SkeletonRows screenreaderText="Loading principals…" />
        ) : principals.length === 0 && !failure ? (
          // The listing is EXACTLY the selection's scope, so an empty table
          // needs to say which scope answered empty — an organisation with no
          // org-scoped principals is not an organisation with no principals.
          standing === 'bucket' ? (
            <EmptyState titleText="No bucket-scoped principals" headingLevel="h2">
              <EmptyStateBody>
                Principals created here are bound to {bucketName ?? 'the selected bucket'}: they
                publish into exactly this bucket and see nothing else in the project. Clear the
                bucket in the header to stand at the project.
              </EmptyStateBody>
              <EmptyStateFooter>
                <EmptyStateActions>
                  {!creating ? (
                    <RoleRestrictedButton
                      action="managePrincipals" callerRole={callerRole}
                      variant="primary" onClick={() => setCreating(true)}
                    >
                      Create principal
                    </RoleRestrictedButton>
                  ) : null}
                </EmptyStateActions>
              </EmptyStateFooter>
            </EmptyState>
          ) : standing === 'organization' ? (
            <EmptyState titleText="No organisation-scoped principals" headingLevel="h2">
              <EmptyStateBody>
                Principals bound to a project are listed at that project — select one in the header
                to see its principals.
              </EmptyStateBody>
              <EmptyStateFooter>
                <EmptyStateActions>
                  {!creating ? (
                    <RoleRestrictedButton
                      action="managePrincipals" callerRole={callerRole}
                      variant="primary" onClick={() => setCreating(true)}
                    >
                      Create principal
                    </RoleRestrictedButton>
                  ) : null}
                </EmptyStateActions>
              </EmptyStateFooter>
            </EmptyState>
          ) : (
            <EmptyState titleText="No service principals are visible to you" headingLevel="h2">
              <EmptyStateBody>
                Create a service principal in the selected context.
              </EmptyStateBody>
              <EmptyStateFooter>
                <EmptyStateActions>
                  {!creating ? (
                    <RoleRestrictedButton
                      action="managePrincipals" callerRole={callerRole}
                      variant="primary" onClick={() => setCreating(true)}
                    >
                      Create principal
                    </RoleRestrictedButton>
                  ) : null}
                </EmptyStateActions>
              </EmptyStateFooter>
            </EmptyState>
          )
        ) : (
          <Card>
            <CardBody>
              <PrincipalTable
                principals={principals}
                selfID={selfID}
                callerRole={callerRole}
                onOpenIssue={(principal) => {
                  setIssuing(principal)
                  setIssued(null)
                  setIssueFailure(null)
                }}
                onRevoke={(principal, secret) => setRevoking({ principal, secret })}
                onDelete={setDeleting}
                onBulkDelete={async (principal) => {
                  if (!token) throw new Error('No session.')
                  await deletePrincipal(token, principal.id)
                }}
                onRefresh={reload}
              />
            </CardBody>
          </Card>
        )}
      </PageSection>

      {issuing ? (
        <IssueSecretModal
          key={issuing.id}
          principal={issuing}
          callerRole={callerRole}
          credential={issued}
          failure={issueFailure}
          onConfirm={async (expiresAt) => {
            if (!token) return
            setIssueFailure(null)
            try {
              const credential = await issueSecret(token, issuing, expiresAt)
              setIssued(credential)
              await reload()
            } catch (err: unknown) {
              setIssueFailure(
                refusalHint(err) ?? (err instanceof Error ? err.message : 'The action failed.'),
              )
            }
          }}
          onClose={() => {
            setIssuing(null)
            setIssued(null)
            setIssueFailure(null)
          }}
        />
      ) : null}
      {deleting ? (
        <DeletePrincipalConfirmation
          principal={deleting}
          onCancel={() => setDeleting(null)}
          onConfirm={() => {
            const principal = deleting
            setDeleting(null)
            void run(async () => {
              if (!token) return
              await deletePrincipal(token, principal.id)
            })
          }}
        />
      ) : null}
      {revoking ? (
        <RevokeSecretConfirmation
          principal={revoking.principal}
          secret={revoking.secret}
          onCancel={() => setRevoking(null)}
          onConfirm={() => {
            const selected = revoking
            setRevoking(null)
            void run(async () => {
              if (!token) return
              await revokeSecret(token, selected.principal.id, selected.secret.id)
            })
          }}
        />
      ) : null}
    </>
  )
}

export function DeletePrincipalConfirmation({
  principal, onConfirm, onCancel,
}: {
  principal: Principal
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <TypedConfirmModal
      title={`Delete ${principal.name}?`}
      body={<Content component="p">Deleting this principal revokes all of its secrets.</Content>}
      expected={principal.name}
      verb="Delete principal"
      busy={false}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  )
}

export type BulkPrincipalExclusion = { principal: Principal; reason: string }

export function partitionBulkPrincipals(principals: Principal[], selfID: string | null): {
  included: Principal[]
  excluded: BulkPrincipalExclusion[]
} {
  const included: Principal[] = []
  const excluded: BulkPrincipalExclusion[] = []
  for (const principal of principals) {
    if (principal.id === selfID) {
      excluded.push({
        principal,
        reason: 'is your current session; a principal may not delete itself',
      })
    } else {
      included.push(principal)
    }
  }
  return { included, excluded }
}

export type BulkPrincipalResult = {
  principal: Principal
  status: 'success' | 'refused'
  message: string
}

export async function runBulkPrincipalDelete(
  principals: Principal[], operation: (principal: Principal) => Promise<void>,
): Promise<BulkPrincipalResult[]> {
  const results: BulkPrincipalResult[] = []
  for (const principal of principals) {
    try {
      await operation(principal)
      results.push({ principal, status: 'success', message: 'Success' })
    } catch (err: unknown) {
      results.push({
        principal,
        status: 'refused',
        message: refusalHint(err) ?? (err instanceof Error ? err.message : 'The action failed.'),
      })
    }
  }
  return results
}

function BulkPrincipalDeleteModal({ principals, selfID, onDelete, onFinished, onClose }: {
  principals: Principal[]
  selfID: string | null
  onDelete: (principal: Principal) => Promise<void>
  onFinished: (allSucceeded: boolean) => void | Promise<void>
  onClose: () => void
}) {
  const [submitting, setSubmitting] = useState(false)
  const [results, setResults] = useState<BulkPrincipalResult[] | null>(null)
  const partition = partitionBulkPrincipals(principals, selfID)

  const confirm = async () => {
    setSubmitting(true)
    const nextResults = await runBulkPrincipalDelete(partition.included, onDelete)
    setResults(nextResults)
    setSubmitting(false)
    const allSucceeded = nextResults.every((result) => result.status === 'success')
    await onFinished(allSucceeded)
    if (allSucceeded) onClose()
  }

  return (
    <BulkPrincipalDeleteModalView
      principals={principals} partition={partition} submitting={submitting} results={results}
      onConfirm={confirm} onClose={onClose}
    />
  )
}

export function BulkPrincipalDeleteModalView({
  principals, partition, submitting, results, onConfirm, onClose,
}: {
  principals: Principal[]
  partition: ReturnType<typeof partitionBulkPrincipals>
  submitting: boolean
  results: BulkPrincipalResult[] | null
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  return (
    <TypedConfirmModal
      variant="medium"
      title={`Delete ${principals.length} ${principals.length === 1 ? 'principal' : 'principals'}`}
      expected="delete"
      verb="Delete"
      busy={submitting}
      confirmDisabled={partition.included.length === 0 || results !== null}
      onConfirm={onConfirm}
      onCancel={onClose}
      body={<>
        <Content component="p">Deleting a principal revokes all of its secrets.</Content>
        <Content component="p">
          {partition.included.length} of {principals.length} will be deleted.
        </Content>
        {partition.included.length > 0 ? (
          <>
            <Content component="p">Included principals:</Content>
            <Content component="ul">
              {partition.included.map((principal) => <li key={principal.id}>{principal.name}</li>)}
            </Content>
          </>
        ) : null}
        {partition.excluded.length > 0 ? (
          <>
            <Content component="p">Excluded principals:</Content>
            <Content component="ul">
              {partition.excluded.map(({ principal, reason }) => (
                <li key={principal.id}>{principal.name} {reason}.</li>
              ))}
            </Content>
          </>
        ) : null}
        {results ? (
          <Content component="ul" aria-label="Delete results">
            {results.map((result) => (
              <li key={result.principal.id}>
                <Label color={result.status === 'success' ? 'green' : 'red'} isCompact>
                  {result.status === 'success' ? 'Success' : 'Refused'}
                </Label>{' '}
                {result.principal.name} — {result.message}
              </li>
            ))}
          </Content>
        ) : null}
      </>}
    />
  )
}

export function RevokeSecretConfirmation({
  principal, secret, onConfirm, onCancel,
}: {
  principal: Principal
  secret: SecretMetadata
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <TypedConfirmModal
      title={`Revoke secret for ${principal.name}?`}
      expected="revoke"
      verb="Revoke secret"
      busy={false}
      onConfirm={onConfirm}
      onCancel={onCancel}
      body={<Content component="p">
        Secret {secret.id} will stop authenticating immediately.
      </Content>}
    />
  )
}

/**
 * The one revocation the server refuses: a root principal's last secret.
 *
 * Root is the exception because nothing sits above it to re-issue on its behalf
 * (ADR-0004, amended 2026-08-02). Exported so the disabled control and the alert
 * that explains it are driven by ONE predicate — if they could drift apart, the
 * console would eventually disable a button with nothing on screen saying why.
 */
export function rootsKeystoneSecret(principal: Principal, secret: SecretMetadata): boolean {
  // Mirrors the server's survivor rule exactly: the revocation is refused when
  // no OTHER secret is never-expiring — which also covers a root whose only
  // secret carries an expiry (duf-2rw). Loose checks: a secret that has never
  // carried the field is permanent too.
  if (principal.role !== 'root') return false
  return !principal.secrets.some((held) => held.id !== secret.id && !held.expires_at)
}

export function rootsLastSecret(principal: Principal): boolean {
  // Drives the alert. A root holding none is a real state since creation
  // stopped minting secrets (duf-4ac) — it has no secret row and so no Revoke
  // control at all, and the alert would have explained a disabled button that
  // is not on screen.
  return principal.secrets.some((secret) => rootsKeystoneSecret(principal, secret))
}

/**
 * The one-time credential.
 *
 * Deliberately NOT a toast or an auto-dismissing alert: this is the only moment
 * the secret exists anywhere but a hash. The containing modal stays in its
 * reveal phase until the operator explicitly acknowledges it by closing.
 */
export function IssuedCredentialCard({
  name, credential, principal,
}: {
  name: string
  credential: IssuedCredential
  /** When given, the card also offers the ready-to-paste MCP environment. */
  principal?: Principal
}) {
  const endpoint = typeof window === 'undefined' ? '' : window.location.origin
  const mcpEnvironment = principal && principal.organization_id && principal.project_id
    ? [
        `DFBG_MCP_ENDPOINT=${endpoint}`,
        `DFBG_MCP_CLIENT_ID=${credential.clientID}`,
        `DFBG_MCP_CLIENT_SECRET=${credential.secret}`,
        `DFBG_MCP_ORGANIZATION_ID=${principal.organization_id}`,
        `DFBG_MCP_PROJECT_ID=${principal.project_id}`,
        ...(principal.bucket_id != null ? [`DFBG_MCP_BUCKET_ID=${principal.bucket_id}`] : []),
      ].join('\n')
    : null
  return (
    <Card>
      <CardTitle>{name} — credential issued</CardTitle>
      <CardBody>
        <Alert
          variant="danger"
          isInline
          title="Copy the secret now. This is the only time it can be read."
        />

        <Content component="p">Client ID</Content>
        <ClipboardCopy isReadOnly hoverTip="Copy" clickTip="Copied">
          {credential.clientID}
        </ClipboardCopy>

        <Content component="p">Client secret</Content>
        <ClipboardCopy isReadOnly hoverTip="Copy" clickTip="Copied">
          {credential.secret}
        </ClipboardCopy>

        {mcpEnvironment ? (
          <>
            <Content component="p">
              MCP server environment — the same credential, ready for
              dufflebag-mcp
            </Content>
            <ClipboardCopy
              isReadOnly
              isCode
              isBlock
              variant="expansion"
              hoverTip="Copy"
              clickTip="Copied"
            >
              {mcpEnvironment}
            </ClipboardCopy>
          </>
        ) : null}
      </CardBody>
    </Card>
  )
}

export function CreatePrincipalForm({
  roles, callerRole, onCreate, onCancel,
}: {
  /**
   * Already narrowed to what the caller's standing and role permit, which is
   * why the form no longer states the standing itself: the offered roles show
   * it, and the page description says principals are created in the selected
   * context.
   */
  roles: Role[]
  callerRole: Role | null
  onCreate: (name: string, role: Role) => Promise<void>
  onCancel: () => void
}) {
  const [name, setName] = useState('')
  const [role, setRole] = useState<Role>(roles[0] ?? 'reader')

  return (
    <Card>
      <CardTitle>New service principal</CardTitle>
      <CardBody>
        <Form>
          <FormGroup label="Name" isRequired fieldId="principal-name">
            <TextInput
              id="principal-name"
              value={name}
              onChange={(_event, value) => setName(value)}
              placeholder="sp-packer-ci"
            />
          </FormGroup>

          <FormGroup label="Role" isRequired fieldId="principal-role">
            <FormSelect
              id="principal-role"
              value={role}
              onChange={(_event, value) => setRole(value as Role)}
            >
              {roles.map((option) => (
                <FormSelectOption key={option} value={option} label={option} />
              ))}
            </FormSelect>
            <Content component="p">{ROLE_DESCRIPTIONS[role]}</Content>
          </FormGroup>

          <ActionGroup>
            <RoleRestrictedButton
              action="managePrincipals"
              callerRole={callerRole}
              variant="primary"
              isDisabled={name.trim() === '' || roles.length === 0}
              onClick={() => void onCreate(name.trim(), role)}
            >
              Create principal
            </RoleRestrictedButton>
            <Button variant="link" onClick={onCancel}>
              Cancel
            </Button>
          </ActionGroup>
          <Content component="p">
            Created without a secret. Issue one from its row when you are ready — it is
            shown once and never again.
          </Content>
        </Form>
      </CardBody>
    </Card>
  )
}

type PrincipalTableProps = {
  principals: Principal[]
  selfID: string | null
  callerRole: Role | null
  onOpenIssue: (principal: Principal) => void
  onRevoke: (principal: Principal, secret: SecretMetadata) => void
  onDelete: (principal: Principal) => void
}

export function principalPage(principals: Principal[], page: number, perPage: number): Principal[] {
  const first = (page - 1) * perPage
  return principals.slice(first, first + perPage)
}

function PrincipalTable(props: PrincipalTableProps & {
  onBulkDelete: (principal: Principal) => Promise<void>
  onRefresh: () => void | Promise<void>
}) {
  const [expanded, setExpanded] = useState<string | null>(null)
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const [selected, setSelected] = useState<string[]>([])
  const [bulkSelectOpen, setBulkSelectOpen] = useState(false)
  const [bulkDeleting, setBulkDeleting] = useState(false)
  const lastPage = Math.max(1, Math.ceil(props.principals.length / perPage))
  const currentPage = Math.min(page, lastPage)
  const visiblePrincipals = principalPage(props.principals, currentPage, perPage)
  const selectedPrincipals = props.principals.filter((principal) => selected.includes(principal.id))

  useEffect(() => {
    const current = new Set(props.principals.map((principal) => principal.id))
    setSelected((selectedIDs) => selectedIDs.filter((id) => current.has(id)))
  }, [props.principals])

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
    <>
      <Pagination
        itemCount={props.principals.length}
        page={currentPage}
        perPage={perPage}
        onSetPage={setCurrentPage}
        onPerPageSelect={selectPerPage}
        isCompact
      />
      <PrincipalTableView
        {...props}
        principals={visiblePrincipals}
        allPrincipals={props.principals}
        selected={selected}
        bulkSelectOpen={bulkSelectOpen}
        onBulkSelectOpenChange={setBulkSelectOpen}
        onSelectionChange={(ids, isSelecting) => setSelected((current) =>
          updateBulkSelection(current, ids, isSelecting))}
        onBulkDelete={() => setBulkDeleting(true)}
        expanded={expanded}
        onToggle={(principal) => setExpanded(expanded === principal.id ? null : principal.id)}
      />
      <Pagination
        itemCount={props.principals.length}
        page={currentPage}
        perPage={perPage}
        onSetPage={setCurrentPage}
        onPerPageSelect={selectPerPage}
        variant="bottom"
        dropDirection="up"
      />
      {bulkDeleting ? (
        <BulkPrincipalDeleteModal
          principals={selectedPrincipals}
          selfID={props.selfID}
          onDelete={props.onBulkDelete}
          onFinished={async (allSucceeded) => {
            await props.onRefresh()
            if (allSucceeded) setSelected([])
          }}
          onClose={() => setBulkDeleting(false)}
        />
      ) : null}
    </>
  )
}

/** Controlled table view keeps row actions testable without a browser renderer. */
export function PrincipalTableView({
  principals, allPrincipals = principals, selfID, callerRole, onOpenIssue, onRevoke, onDelete,
  selected = [], bulkSelectOpen = false, onBulkSelectOpenChange = () => {},
  onSelectionChange = () => {}, onBulkDelete = () => {}, expanded, onToggle,
}: PrincipalTableProps & {
  allPrincipals?: Principal[]
  selected?: string[]
  bulkSelectOpen?: boolean
  onBulkSelectOpenChange?: (open: boolean) => void
  onSelectionChange?: (ids: string[], isSelecting: boolean) => void
  onBulkDelete?: () => void
  expanded: string | null
  onToggle: (principal: Principal) => void
}) {
  const visibleIDs = principals.map((principal) => principal.id)
  const visibleSelected = visibleIDs.filter((id) => selected.includes(id))
  const allVisibleSelected = visibleIDs.length > 0 && visibleSelected.length === visibleIDs.length
  return (
    <>
    <Toolbar id="principals-toolbar">
      <ToolbarContent>
        <ToolbarItem>
          <Dropdown
            role="menu"
            isOpen={bulkSelectOpen}
            onOpenChange={onBulkSelectOpenChange}
            onSelect={(_event, value) => {
              if (value === 'none') onSelectionChange(selected, false)
              if (value === 'page') onSelectionChange(visibleIDs, !allVisibleSelected)
              if (value === 'all') onSelectionChange(
                allPrincipals.map((principal) => principal.id),
                selected.length !== allPrincipals.length,
              )
              onBulkSelectOpenChange(false)
            }}
            toggle={(toggleRef: Ref<MenuToggleElement>) => (
              <MenuToggle
                ref={toggleRef}
                isExpanded={bulkSelectOpen}
                onClick={() => onBulkSelectOpenChange(!bulkSelectOpen)}
                aria-label="Select principals"
                splitButtonItems={[
                  <MenuToggleCheckbox
                    id="principals-bulk-select-checkbox"
                    key="principals-bulk-select-checkbox"
                    aria-label={selected.length > 0 ? 'Deselect all principals' : 'Select all principals'}
                    isChecked={selected.length === allPrincipals.length
                      ? true
                      : selected.length > 0 ? null : false}
                    onChange={(checked) => onSelectionChange(
                      allPrincipals.map((principal) => principal.id), checked,
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
              <DropdownItem value="page">Select page ({principals.length} items)</DropdownItem>
              <DropdownItem value="all">Select all ({allPrincipals.length} items)</DropdownItem>
            </DropdownList>
          </Dropdown>
        </ToolbarItem>
        {selected.length > 0 ? (
          <ToolbarItem>
            <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              {selected.length} selected
              <RoleRestrictedButton
                action="managePrincipals" callerRole={callerRole}
                variant="danger" onClick={onBulkDelete}
              >Delete</RoleRestrictedButton>
            </span>
          </ToolbarItem>
        ) : null}
      </ToolbarContent>
    </Toolbar>
    <Table
      aria-label="Service principals" variant="compact" isStickyHeader
      selectableRowCaptionText="Principal"
    >
      <Thead>
        <Tr>
          <Th screenReaderText="Expand" />
          <Th
            aria-label="Select page"
            select={{
              isSelected: allVisibleSelected,
              isIndeterminate: visibleSelected.length > 0 && !allVisibleSelected,
              onSelect: (_event, isSelecting) => onSelectionChange(visibleIDs, isSelecting),
            }}
          />
          <Th>Name</Th>
          <Th>Role</Th>
          <Th>Scope</Th>
          <Th>Client ID</Th>
          <Th>Secrets</Th>
          <Th screenReaderText="Actions" />
        </Tr>
      </Thead>
      {principals.map((principal, index) => (
        <Tbody key={principal.id} isExpanded={expanded === principal.id}>
          <Tr isSelectable isRowSelected={selected.includes(principal.id)}>
            <Td
              expand={{
                rowIndex: index,
                isExpanded: expanded === principal.id,
                onToggle: () => onToggle(principal),
              }}
            />
            <Td select={{
              rowIndex: index,
              isSelected: selected.includes(principal.id),
              onSelect: (_event, isSelecting) => onSelectionChange([principal.id], isSelecting),
            }} />
            <Td dataLabel="Name">
              {principal.name}
              {principal.id === selfID ? <Label isCompact>your session</Label> : null}
            </Td>
            <Td dataLabel="Role">
              <Label isCompact>{principal.role}</Label>
            </Td>
            <Td dataLabel="Scope">{scopeLabel(principal)}</Td>
            <Td dataLabel="Client ID">
              <CopyableIdentifier value={principal.client_id} label={`${principal.name} client ID`} />
            </Td>
            <Td dataLabel="Secrets">
              {/* Usable secrets: an expired one grants nothing and does not
                  count against the cap, exactly as the server counts. */}
              {usableSecrets(principal).length} of 2
            </Td>
            <Td dataLabel="Actions">
              {usableSecrets(principal).length < 2 ? (
                <RoleRestrictedButton
                  action="managePrincipals" callerRole={callerRole}
                  variant="secondary" onClick={() => onOpenIssue(principal)}
                >
                  Issue secret
                </RoleRestrictedButton>
              ) : null}
              {/*
                A principal may not delete itself, and the server refuses it. The
                button is absent rather than disabled-with-a-tooltip: an action
                that cannot succeed should not look available.
              */}
              {principal.id === selfID ? null : (
                <RoleRestrictedButton
                  action="managePrincipals" callerRole={callerRole}
                  variant="link" isDanger onClick={() => onDelete(principal)}
                >
                  Delete
                </RoleRestrictedButton>
              )}
            </Td>
          </Tr>
          <Tr isExpanded={expanded === principal.id}>
            <Td colSpan={8}>
              <ExpandableRowContent>
                <SecretSlots
                  principal={principal}
                  callerRole={callerRole}
                  onRevoke={(secret) => onRevoke(principal, secret)}
                />
              </ExpandableRowContent>
            </Td>
          </Tr>
        </Tbody>
      ))}
    </Table>
    </>
  )
}

type SecretExpiryChoice = 'never' | '90-days' | 'custom'

function IssueSecretModal({
  principal, callerRole, credential, failure, onConfirm, onClose,
}: {
  principal: Principal
  callerRole: Role | null
  credential: IssuedCredential | null
  failure: string | null
  onConfirm: (expiresAt?: string) => Promise<void>
  onClose: () => void
}) {
  const [choice, setChoice] = useState<SecretExpiryChoice>('never')
  const [customDate, setCustomDate] = useState('')

  return (
    <Modal
      aria-labelledby="issue-secret-modal-title"
      isOpen
      onClose={onClose}
      variant="medium"
    >
      <IssueSecretModalView
        principal={principal}
        callerRole={callerRole}
        credential={credential}
        failure={failure}
        choice={choice}
        customDate={customDate}
        onChoiceChange={setChoice}
        onCustomDateChange={setCustomDate}
        onConfirm={onConfirm}
        onClose={onClose}
      />
    </Modal>
  )
}

/** Modal contents exported because PatternFly portals are absent from server-rendered tests. */
export function IssueSecretModalView({
  principal, callerRole, credential, failure, choice, customDate,
  onChoiceChange, onCustomDateChange, onConfirm, onClose,
}: {
  principal: Principal
  callerRole: Role | null
  credential: IssuedCredential | null
  failure: string | null
  choice: SecretExpiryChoice
  customDate: string
  onChoiceChange: (choice: SecretExpiryChoice) => void
  onCustomDateChange: (date: string) => void
  onConfirm: (expiresAt?: string) => Promise<void>
  onClose: () => void
}) {
  const idPrefix = `issue-secret-${principal.id}`
  const parsedCustomDate = customDate === '' ? null : new Date(`${customDate}T00:00:00.000Z`)
  const customDateMissing = choice === 'custom' && (
    parsedCustomDate === null || Number.isNaN(parsedCustomDate.getTime())
  )
  const customDatePast = choice === 'custom' && !customDateMissing &&
    parsedCustomDate!.getTime() <= Date.now()
  const customDateFailure = customDateMissing
    ? 'Choose a custom expiry date.'
    : customDatePast
      ? 'Expiry must be in the future; a secret cannot be issued already expired.'
      : null

  return (
    <>
      <ModalHeader
        labelId="issue-secret-modal-title"
        title={`Issue secret — ${principal.name}`}
      />
      <ModalBody>
        {failure ? (
          <Alert variant="danger" isInline title="The action was refused">
            <Content component="p">{failure}</Content>
          </Alert>
        ) : null}
        {credential ? (
          <IssuedCredentialCard name={principal.name} credential={credential} principal={principal} />
        ) : (
          <Form>
            <FormGroup label="Expiry" fieldId={`${idPrefix}-expiry`} role="radiogroup">
              <Radio
                id={`${idPrefix}-never`}
                name={`${idPrefix}-expiry`}
                label="Never expires"
                isChecked={choice === 'never'}
                onChange={() => onChoiceChange('never')}
              />
              <Radio
                id={`${idPrefix}-90-days`}
                name={`${idPrefix}-expiry`}
                label="90 days"
                isChecked={choice === '90-days'}
                onChange={() => onChoiceChange('90-days')}
              />
              <Radio
                id={`${idPrefix}-custom`}
                name={`${idPrefix}-expiry`}
                label="Custom date"
                isChecked={choice === 'custom'}
                onChange={() => onChoiceChange('custom')}
              />
            </FormGroup>

            {choice === 'custom' ? (
              <FormGroup label="Expiry date" isRequired fieldId={`${idPrefix}-custom-date`}>
                <TextInput
                  id={`${idPrefix}-custom-date`}
                  type="date"
                  value={customDate}
                  validated={customDateFailure ? 'error' : 'default'}
                  aria-invalid={customDateFailure ? 'true' : undefined}
                  aria-describedby={customDateFailure ? `${idPrefix}-custom-date-error` : undefined}
                  onChange={(_event, value) => onCustomDateChange(value)}
                />
                {customDateFailure ? (
                  <Content component="p" id={`${idPrefix}-custom-date-error`}>
                    {customDateFailure}
                  </Content>
                ) : null}
              </FormGroup>
            ) : null}
          </Form>
        )}
      </ModalBody>
      <ModalFooter>
        {credential ? (
          <Button variant="primary" onClick={onClose}>Close</Button>
        ) : (
          <>
            <RoleRestrictedButton
              action="managePrincipals"
              callerRole={callerRole}
              variant="primary"
              isDisabled={customDateFailure !== null}
              onClick={() => {
                if (choice === '90-days') {
                  return onConfirm(new Date(Date.now() + 90 * 24 * 60 * 60 * 1000).toISOString())
                } else if (choice === 'custom' && parsedCustomDate) {
                  return onConfirm(parsedCustomDate.toISOString())
                }
                return onConfirm()
              }}
            >
              Confirm
            </RoleRestrictedButton>
            <Button variant="link" onClick={onClose}>Cancel</Button>
          </>
        )}
      </ModalFooter>
    </>
  )
}

function SecretSlots({
  principal, callerRole, onRevoke,
}: {
  principal: Principal
  callerRole: Role | null
  onRevoke: (secret: SecretMetadata) => void
}) {
  return (
    <Table aria-label={`Secrets for ${principal.name}`} variant="compact">
      <Thead>
        <Tr>
          <Th>Secret</Th>
          <Th>Created</Th>
          <Th>Last used</Th>
          <Th>Expires</Th>
          <Th screenReaderText="Actions" />
        </Tr>
      </Thead>
      <Tbody>
        {principal.secrets.map((secret) => {
          const keystoneSecret = rootsKeystoneSecret(principal, secret)
          const revoke = (
            <RoleRestrictedButton
              action="managePrincipals"
              callerRole={callerRole}
              variant="link"
              isDanger
              isDisabled={keystoneSecret}
              onClick={() => onRevoke(secret)}
            >
              Revoke
            </RoleRestrictedButton>
          )
          return <Tr key={secret.id}>
            <Td dataLabel="Secret">{secret.id}</Td>
            <Td dataLabel="Created"><When iso={secret.created_at} /></Td>
            <Td dataLabel="Last used">
              {/*
                "never used" is the signal that matters: a rotation whose new
                secret never authenticates is a rotation that has not happened,
                and revoking the old one then breaks the build.
              */}
              <When iso={everUsed(secret) ? secret.last_used_at : null} emptyText="never used" />
            </Td>
            <Td dataLabel="Expires">
              <ExpiryCell secret={secret} />
            </Td>
            <Td dataLabel="Actions">
              {/*
                Offered for every principal except a root holding its last
                secret, which is the one revocation the server refuses
                (ADR-0004, amended 2026-08-02). Disabling it is an AFFORDANCE,
                not the guard: the server still refuses, and still has the test
                that proves it. Deleting this check would restore an error
                dialog, not a hole.

                The global alert and this control's local popover are both
                derived from keystoneSecret, so neither can disagree with the
                server predicate.
              */}
              {keystoneSecret ? <Popover bodyContent={ROOT_KEYSTONE_REASON}>{revoke}</Popover> : revoke}
            </Td>
          </Tr>
        })}
      </Tbody>
    </Table>
  )
}

function scopeLabel(principal: Principal): string {
  if (principal.organization_id === null) return 'platform'
  if (principal.project_id === null) return 'organisation'
  if (principal.bucket_id != null) return 'bucket'
  return 'project'
}

/** Secrets that still grant anything, exactly as the server counts the cap. */
function usableSecrets(principal: Principal): SecretMetadata[] {
  return principal.secrets.filter((secret) => !secretExpired(secret))
}

/**
 * Parsed, not string-compared: the server's RFC3339 timestamps carry variable
 * sub-second precision, and "09:00:00Z" sorts after "09:00:00.500Z" as text.
 */
function secretExpired(secret: SecretMetadata): boolean {
  if (!secret.expires_at) return false
  return Date.parse(secret.expires_at) <= Date.now()
}

/**
 * The expiry cell states the failure before it is one: an impending expiry
 * reads plainly, an expired secret says so with its date — 'expired on the
 * 4th' beats 'authentication failed' (duf-2rw).
 */
function ExpiryCell({ secret }: { secret: SecretMetadata }) {
  if (!secret.expires_at) return <>never</>
  if (!secretExpired(secret)) return <When iso={secret.expires_at} dateOnly />
  return <Label isCompact color="red">expired <When iso={secret.expires_at} dateOnly /></Label>
}
