import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let PrincipalsView, CreatePrincipalForm, IssueSecretModalView, IssuedCredentialCard
let PrincipalTableView
let DeletePrincipalConfirmation, RevokeSecretConfirmation
let TypedConfirmModalView
let grantableRoles
const principalScreenSource = readFileSync(new URL('../src/screens/Principals.tsx', import.meta.url), 'utf8')

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({
    PrincipalsView, CreatePrincipalForm, IssueSecretModalView, IssuedCredentialCard,
    PrincipalTableView, DeletePrincipalConfirmation, RevokeSecretConfirmation,
  } =
    await vite.ssrLoadModule('/src/screens/Principals.tsx'))
  ;({ grantableRoles } = await vite.ssrLoadModule('/src/data/principals.ts'))
  ;({ TypedConfirmModalView } =
    await vite.ssrLoadModule('/src/components/TypedConfirmModal.tsx'))
})

after(async () => {
  await vite.close()
})

const secret = (id, lastUsed = null) => ({
  id, created_at: '2026-07-01T00:00:00Z', last_used_at: lastUsed,
})

const principal = (over = {}) => ({
  id: 'p-1', name: 'sp-packer-ci', client_id: 'client-1', role: 'builder',
  organization_id: 'org-1', project_id: 'proj-1', created_at: '2026-07-01T00:00:00Z',
  secrets: [secret('s-1', '2026-07-30T00:00:00Z')],
  ...over,
})

// GET /api/v1/self emits this shape from GetSelf in
// internal/platform/v1/handler.go; role is the stored value, not a token claim.
const self = (role) => ({
  principal_id: 'p-caller', name: `${role} caller`, role,
  organization_id: 'org-1', project_id: 'proj-1',
})

const view = (over = {}) => renderToStaticMarkup(React.createElement(PrincipalsView, {
  principals: [principal()], loading: false, failure: null, reload: async () => {},
  selfID: null, callerRole: 'maintainer', token: 't', organizationID: 'org-1',
  projectID: 'proj-1', ...over,
}))

const issueModalProps = (over = {}) => ({
  principal: principal(), callerRole: 'maintainer', credential: null, failure: null,
  choice: 'never', customDate: '', onChoiceChange: () => {},
  onCustomDateChange: () => {}, onConfirm: async () => {}, onClose: () => {}, ...over,
})

const findElement = (node, predicate) => {
  if (Array.isArray(node)) {
    for (const child of node) {
      const found = findElement(child, predicate)
      if (found) return found
    }
    return null
  }
  if (!React.isValidElement(node)) return null
  if (predicate(node)) return node
  return findElement(node.props.children, predicate)
}

// ADR-0019: authority is one nested role resolved from storage, NOT the scope
// claims the mockups model. Building those would reintroduce the revocation
// delay that decision exists to remove.
test('the screen offers roles, never scope claims', () => {
  const markup = view()
  for (const scopeClaim of ['packer:read', 'packer:write', 'channels:write', 'principals:write']) {
    assert.doesNotMatch(markup, new RegExp(scopeClaim.replace(':', '\\:')))
  }
  assert.match(markup, /builder/)
})

test('principal mutations follow the server-resolved maintainer requirement', () => {
  const reader = view({ callerRole: self('reader').role })
  assert.match(reader, /Requires maintainer/)
  for (const label of ['Create principal', 'Issue secret', 'Delete', 'Revoke']) {
    assert.match(
      reader,
      new RegExp(`<button[^>]*disabled[^>]*>[\\s\\S]{0,240}${label}`),
      `${label} should be disabled for reader`,
    )
  }

  for (const role of ['maintainer', 'root']) {
    const permitted = view({ callerRole: self(role).role })
    assert.doesNotMatch(permitted, /Requires maintainer/)
    for (const label of ['Create principal', 'Issue secret', 'Delete', 'Revoke']) {
      const end = permitted.indexOf(`>${label}<`)
      assert.ok(end >= 0, `${label} is absent for ${role}`)
      assert.doesNotMatch(permitted.slice(Math.max(0, end - 300), end), /disabled/)
    }
  }
})

// validBinding ties root to platform scope exactly, so "root in an organisation"
// is malformed rather than narrower. The form must not offer it.
test('root is never offered inside a tenancy, and is the only option above one', () => {
  assert.deepEqual(grantableRoles('tenancy', null), ['reader', 'builder', 'publisher', 'maintainer'])
  assert.deepEqual(grantableRoles('platform', null), ['root'])

  const inTenancy = renderToStaticMarkup(React.createElement(CreatePrincipalForm, {
    roles: grantableRoles('tenancy', 'maintainer'), standing: 'project',
    callerRole: 'maintainer',
    onCreate: async () => {}, onCancel: () => {},
  }))
  assert.doesNotMatch(inTenancy, /value="root"/)
  assert.match(inTenancy, /value="maintainer"/)
})

// duf-4qr: the picker selection IS the scope of the principal being created —
// the form never asks for tenancy. It no longer states the standing in prose
// either (Ben, 2026-08-02): the offered roles show it, and the page
// description says principals are created in the selected context. The role
// list is the load-bearing part and is what this asserts.
test('the offered roles follow the standing, root never inside a tenancy', () => {
  const at = (standing) => renderToStaticMarkup(React.createElement(CreatePrincipalForm, {
    roles: grantableRoles(standing === 'platform' ? 'platform' : 'tenancy', null),
    callerRole: standing === 'platform' ? 'root' : 'maintainer',
    onCreate: async () => {}, onCancel: () => {},
  }))
  assert.match(at('platform'), /value="root"/)
  assert.doesNotMatch(at('organization'), /value="root"/)
  assert.doesNotMatch(at('project'), /value="root"/)
  // The relocated prose must not linger in the form.
  assert.doesNotMatch(at('project'), /for the selected project only/)
  assert.doesNotMatch(at('platform'), /above every tenancy/)
})

// No identity may grant a role more permissive than its own (ADR-0019).
test('a granter is never offered a role above its own', () => {
  assert.deepEqual(grantableRoles('tenancy', 'builder'), ['reader', 'builder'])
  assert.deepEqual(grantableRoles('tenancy', 'reader'), ['reader'])
  assert.ok(!grantableRoles('tenancy', 'publisher').includes('maintainer'))
})

// The secret exists nowhere else. The card carries the warning and both values;
// the modal keeps it visible until Close acknowledges it.
test('the one-time credential shows the secret and its warning', () => {
  const markup = renderToStaticMarkup(React.createElement(IssuedCredentialCard, {
    name: 'sp-packer-ci',
    credential: { secretID: 's-1', secret: 'PLAINTEXT-SECRET', clientID: 'client-1' },
  }))
  assert.match(markup, /PLAINTEXT-SECRET/)
  assert.match(markup, /only time it can be read/)
})

test('Issue secret opens the selected principal workflow in the modal view', () => {
  let opened = null
  const selected = principal()
  const table = PrincipalTableView({
    principals: [selected], selfID: null, callerRole: 'maintainer', expanded: null,
    onToggle: () => {}, onOpenIssue: (value) => { opened = value },
    onRevoke: () => {}, onDelete: () => {},
  })
  findElement(table, (element) => element.props.children === 'Issue secret').props.onClick()
  assert.equal(opened, selected)

  const markup = renderToStaticMarkup(React.createElement(
    IssueSecretModalView, issueModalProps({ principal: opened }),
  ))
  assert.match(markup, /Issue secret — sp-packer-ci/)
  assert.match(markup, /Never expires/)
  assert.match(markup, /90 days/)
  assert.match(markup, /Custom date/)
  assert.match(markup, /id="issue-secret-p-1-never"[^>]*checked/)
})

test('the 90-day choice supplies issueSecret with an expiry about 90 days from now', () => {
  let choice = 'never'
  const issueCalls = []
  const props = () => issueModalProps({
    choice,
    onChoiceChange: (selected) => { choice = selected },
    onConfirm: async (expiresAt) => issueCalls.push({
      token: 't', principal: principal(), expires_at: expiresAt,
    }),
  })

  const choiceView = IssueSecretModalView(props())
  findElement(choiceView, (element) => element.props.label === '90 days').props.onChange({}, true)
  assert.equal(choice, '90-days')

  const before = Date.now()
  const selectedView = IssueSecretModalView(props())
  findElement(selectedView, (element) => element.props.children === 'Confirm').props.onClick()
  const after = Date.now()

  assert.equal(issueCalls.length, 1)
  assert.deepEqual(Object.keys(issueCalls[0]).sort(), ['expires_at', 'principal', 'token'])
  assert.equal(issueCalls[0].token, 't')
  assert.deepEqual(issueCalls[0].principal, principal())
  const expiry = Date.parse(issueCalls[0].expires_at)
  const ninetyDays = 90 * 24 * 60 * 60 * 1000
  assert.ok(expiry >= before + ninetyDays && expiry <= after + ninetyDays)
})

test('a past custom date disables modal Confirm and explains why inline', () => {
  const props = issueModalProps({ choice: 'custom', customDate: '2000-01-01' })
  const modal = IssueSecretModalView(props)
  const confirm = findElement(modal, (element) => element.props.children === 'Confirm')
  assert.equal(confirm.props.isDisabled, true)

  const markup = renderToStaticMarkup(React.createElement(IssueSecretModalView, props))
  assert.match(markup, /cannot be issued already expired/)
  assert.match(markup, /aria-invalid="true"/)
})

test('cancelling the modal expiry choice mints nothing', () => {
  let cancelled = 0
  let issued = 0
  const modal = IssueSecretModalView(issueModalProps({
    onClose: () => { cancelled++ },
    onConfirm: async () => { issued++ },
  }))
  findElement(modal, (element) => element.props.children === 'Cancel').props.onClick()
  assert.equal(cancelled, 1)
  assert.equal(issued, 0)
})

test('the modal expiry flow remains disabled with a reason for a reader', () => {
  const markup = renderToStaticMarkup(React.createElement(
    IssueSecretModalView, issueModalProps({ callerRole: 'reader' }),
  ))
  assert.match(markup, /Requires maintainer/)
  assert.match(markup, /<button[^>]*disabled[^>]*>[\s\S]{0,240}Confirm/)
})

test('a successful mint reveals the secret in the modal with a Close-only footer', async () => {
  const minted = { secretID: 's-2', secret: 'PLAINTEXT-SECRET', clientID: 'client-1' }
  const mockIssueSecret = async () => minted
  let credential = null
  const props = issueModalProps({
    onConfirm: async () => { credential = await mockIssueSecret() },
  })
  const choose = IssueSecretModalView(props)
  await findElement(choose, (element) => element.props.children === 'Confirm').props.onClick()

  const markup = renderToStaticMarkup(React.createElement(
    IssueSecretModalView, { ...props, credential },
  ))
  assert.match(markup, /sp-packer-ci — credential issued/)
  assert.match(markup, /PLAINTEXT-SECRET/)
  assert.match(markup, />Close<\/span>/)
  assert.doesNotMatch(markup, />Cancel<\/span>/)
  assert.doesNotMatch(markup, />Confirm<\/span>/)
})

test('an issue refusal, including a keystone 409, stays inside the modal', () => {
  const markup = renderToStaticMarkup(React.createElement(
    IssueSecretModalView,
    issueModalProps({ failure: 'A root principal must keep one secret that never expires.' }),
  ))
  assert.match(markup, /The action was refused/)
  assert.match(markup, /root principal must keep one secret that never expires/)
})

// A list can never carry a secret, so no screen can render one by accident.
test('the listing never renders a secret value', () => {
  const markup = view({
    principals: [principal({ secrets: [secret('s-1'), secret('s-2')] })],
  })
  assert.doesNotMatch(markup, /PLAINTEXT/)
  assert.doesNotMatch(markup, />Issue secret</)
  // A secret that never authenticated is the signal a rotation has not landed.
  assert.match(markup, /never used/)
})

// ADR-0004 as amended 2026-08-02: a non-root principal's last secret CAN be
// revoked, so nothing about it is disabled or explained.
test('a non-root sole secret offers revoke, with no rule stated in prose', () => {
  const markup = view()
  assert.match(markup, />Revoke</)
  assert.doesNotMatch(markup, /must keep one secret that never expires/)
  const revoke = markup.slice(markup.indexOf('>Revoke<') - 200, markup.indexOf('>Revoke<'))
  assert.doesNotMatch(revoke, /disabled/)
})

const soleRoot = () => principal({
  id: 'p-root', name: 'initial administrator', role: 'root',
  organization_id: null, project_id: null, secrets: [secret('s-1')],
})

// Ben, 2026-08-02: disable the control rather than letting the operator hit a
// server error, and explain it ONCE above the listing — nothing on the button.
// Both halves are asserted together because either alone is a defect: a
// disabled control with no explanation reads as a bug, and an explanation with
// a live control is a lie.
test("a root's sole secret disables revoke and says why above the listing", () => {
  const markup = view({ principals: [soleRoot()] })
  assert.match(markup, /must keep one secret that never expires/)
  assert.match(markup, /Issue another never-expiring secret first/)
  const revoke = markup.slice(markup.indexOf('>Revoke<') - 200, markup.indexOf('>Revoke<'))
  assert.match(revoke, /disabled/)
})

// Independent review of 8c7978d. A root holding NO secrets is a real state since
// creation stopped minting them (duf-4ac), and it has no secret row — so there
// is no disabled Revoke for the alert to explain. Showing it anyway is the
// exact button/alert disagreement the coupling exists to prevent.
test('a root with no secrets says nothing, having no control to explain', () => {
  const markup = view({
    principals: [principal({
      id: 'p-root', role: 'root', organization_id: null, project_id: null, secrets: [],
    })],
  })
  assert.doesNotMatch(markup, /must keep one secret that never expires/)
  assert.doesNotMatch(markup, />Revoke</)
})

// Also from that review: reload marks itself loading without clearing the previous scope's
// principals, so an unguarded alert outlives the rows it explains.
test('the notice does not outlive the listing it explains while loading', () => {
  const markup = view({ principals: [soleRoot()], loading: true })
  assert.match(markup, /Loading principals…/)
  assert.doesNotMatch(markup, /must keep one secret that never expires/)
})

// The notice is a property of the state, not of the screen: a root that has
// rotated onto two secrets is under no constraint, so neither the disabled
// control nor the explanation should survive.
test('a root holding two secrets is unconstrained, and says nothing', () => {
  const markup = view({
    principals: [principal({
      id: 'p-root', role: 'root', organization_id: null, project_id: null,
      secrets: [secret('s-1'), secret('s-2')],
    })],
  })
  assert.doesNotMatch(markup, /must keep one secret that never expires/)
  const revoke = markup.slice(markup.indexOf('>Revoke<') - 200, markup.indexOf('>Revoke<'))
  assert.doesNotMatch(revoke, /disabled/)
})

test('a principal may not delete itself, so the action is absent rather than disabled', () => {
  const mine = view({ selfID: 'p-1' })
  assert.doesNotMatch(mine, />Delete</)
  assert.match(mine, /your session/)

  const theirs = view({ selfID: 'someone-else' })
  assert.match(theirs, />Delete</)
})

test('principal deletion requires the typed modal before its action fires', () => {
  let selected = null
  let deletes = 0
  const record = principal()
  const table = PrincipalTableView({
    principals: [record], selfID: null, callerRole: 'maintainer', expanded: null,
    onToggle: () => {}, onOpenIssue: () => {}, onRevoke: () => {},
    onDelete: (value) => { selected = value },
  })
  findElement(table, (element) => element.props.children === 'Delete').props.onClick()
  assert.equal(selected, record)
  assert.equal(deletes, 0)

  const confirmation = DeletePrincipalConfirmation({
    principal: selected, onCancel: () => {}, onConfirm: () => { deletes++ },
  })
  assert.equal(confirmation.props.expected, 'sp-packer-ci')
  const markup = renderToStaticMarkup(React.createElement(TypedConfirmModalView, {
    ...confirmation.props, confirmation: '', onConfirmationChange: () => {},
  }))
  assert.match(markup, /Type <strong>sp-packer-ci<\/strong> to confirm/)
  assert.equal(deletes, 0)
  confirmation.props.onConfirm()
  assert.equal(deletes, 1)
  assert.match(principalScreenSource, /onDelete=\{setDeleting\}/)
  assert.match(principalScreenSource, /deleting \? \(\s*<DeletePrincipalConfirmation/)
})

test('secret revoke requires revoke before its danger action arms', () => {
  let revokes = 0
  const record = principal()
  const confirmation = RevokeSecretConfirmation({
    principal: record, secret: record.secrets[0], onCancel: () => {},
    onConfirm: () => { revokes++ },
  })
  assert.equal(confirmation.props.expected, 'revoke')
  assert.equal(revokes, 0)
  const blocked = renderToStaticMarkup(React.createElement(TypedConfirmModalView, {
    ...confirmation.props, confirmation: '', onConfirmationChange: () => {},
  }))
  assert.match(blocked, /Revoke secret for sp-packer-ci\?/)
  assert.match(blocked, /Secret s-1 will stop authenticating immediately\./)
  assert.match(blocked, /Type <strong>revoke<\/strong> to confirm/)
  assert.match(blocked, /<button[^>]*disabled[^>]*>[\s\S]{0,160}Revoke secret/)
  const armed = renderToStaticMarkup(React.createElement(TypedConfirmModalView, {
    ...confirmation.props, confirmation: 'revoke', onConfirmationChange: () => {},
  }))
  assert.doesNotMatch(armed, /<button[^>]*disabled[^>]*>[\s\S]{0,160}Revoke secret/)
  confirmation.props.onConfirm()
  assert.equal(revokes, 1)
  assert.match(principalScreenSource, /onRevoke=\{\(principal, secret\) => setRevoking\(\{ principal, secret \}\)\}/)
  assert.match(principalScreenSource, /revoking \? \(\s*<RevokeSecretConfirmation/)
})

// Finding 17: a failure must not render as a healthy empty state.
test('failures are visible, and refusals are distinguished from absence', () => {
  const failed = view({ principals: [], failure: '403 refused' })
  assert.match(failed, /could not be loaded/)
  assert.doesNotMatch(failed, /No service principals are visible/)

  const empty = view({ principals: [] })
  assert.match(empty, /<h2[^>]*>No service principals are visible to you<\/h2>/)
  assert.match(empty, /class="pf-v6-c-empty-state__body"/)
})

test('Create principal moves between the empty state and populated header with its role gate', () => {
  const emptyReader = view({ principals: [], callerRole: 'reader' })
  assert.match(emptyReader, /pf-v6-c-empty-state[\s\S]*Create principal/)
  assert.match(emptyReader, /Requires maintainer/)
  assert.equal((emptyReader.match(/Create principal/g) ?? []).length, 1)

  const populatedReader = view({ callerRole: 'reader' })
  assert.match(populatedReader, /pf-v6-c-page__main-section[\s\S]{0,2000}Create principal/)
  assert.doesNotMatch(populatedReader, /pf-v6-c-empty-state/)
  assert.match(populatedReader, /Requires maintainer/)
  assert.equal((populatedReader.match(/Create principal/g) ?? []).length, 1)
})

// duf-4qr: the listing is exactly the selection's scope, so an empty
// organisation-level table says which scope answered empty and where the rest
// went — never a bare zero-row table.
test('an empty organisation-level listing explains itself', () => {
  const atOrganization = view({ principals: [], projectID: null })
  assert.match(atOrganization, /<h2[^>]*>No organisation-scoped principals<\/h2>/)
  assert.match(atOrganization, /select one in the header/)
  assert.match(atOrganization, /pf-v6-c-empty-state[\s\S]*Create principal/)
  assert.doesNotMatch(atOrganization, /No service principals are visible/)
})

// duf-2rw: expiry is a property of the SECRET, and the console states it —
// 'expired on the 4th' beats 'authentication failed'.
test('an expired secret is labelled with its date and stops counting against the cap', () => {
  const markup = view({ principals: [principal({ secrets: [
    { id: 's-old', created_at: '2026-06-01T00:00:00Z', last_used_at: null,
      expires_at: '2026-07-01T00:00:00Z' },
    { id: 's-new', created_at: '2026-07-02T00:00:00Z', last_used_at: null, expires_at: null },
  ] })] })
  assert.match(markup, /expired <div[^>]*><span[^>]*><time[^>]*dateTime="2026-07-01T00:00:00Z"/)
  // One usable of two: the expired secret no longer counts, so Issue stays offered.
  assert.match(markup, /1 of 2/)
  assert.match(markup, />Issue secret</)
})

test("a root's expiring secret stays revocable while its permanent one is the keystone", () => {
  const future = new Date(Date.now() + 86400000).toISOString()
  const markup = view({ principals: [principal({
    id: 'p-root', role: 'root', organization_id: null, project_id: null,
    secrets: [
      { id: 's-permanent', created_at: '2026-06-01T00:00:00Z', last_used_at: null, expires_at: null },
      { id: 's-expiring', created_at: '2026-07-01T00:00:00Z', last_used_at: null, expires_at: future },
    ],
  })] })
  assert.match(markup, /must keep one secret that never expires/)
  // Exactly one of the two Revoke controls is disabled: the permanent keystone.
  const revokes = markup.split('>Revoke<').length - 1
  assert.equal(revokes, 2)
  const firstRevoke = markup.slice(markup.indexOf('>Revoke<') - 250, markup.indexOf('>Revoke<'))
  assert.match(firstRevoke, /disabled/)
  const rest = markup.slice(markup.indexOf('>Revoke<') + 8)
  const secondRevoke = rest.slice(rest.indexOf('>Revoke<') - 250, rest.indexOf('>Revoke<'))
  assert.doesNotMatch(secondRevoke, /disabled/)
})
