import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { createServer } from 'vite'

let vite
let decodeClaims
let platformTenancyGap
let organizationRows
let AuthContext
let TenantSwitcher
let applyOrganizationRefresh
let selectionAfterInitialOrganizationLoad
let selectionAfterOrganizationRefresh
let startOrganizationRefresh
let scheduleSessionRenewal
let renewConsoleSession
let refreshOnPickerOpen
let selectionAfterProjectsRefresh
let grantableRoles
let PrincipalsView
let RegistryView
const tenantSwitcherSource = readFileSync(
  new URL('../src/shell/TenantSwitcher.tsx', import.meta.url),
  'utf8',
)
const authContextSource = readFileSync(
  new URL('../src/auth/AuthContext.tsx', import.meta.url),
  'utf8',
)

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ decodeClaims } = await vite.ssrLoadModule('/src/auth/token.ts'))
  ;({ platformTenancyGap, organizationRows } = await vite.ssrLoadModule('/src/data/tenant.ts'))
  ;({
    AuthContext, applyOrganizationRefresh, selectionAfterInitialOrganizationLoad,
    selectionAfterOrganizationRefresh,
    startOrganizationRefresh, scheduleSessionRenewal, renewConsoleSession,
    selectionAfterProjectsRefresh,
  } = await vite.ssrLoadModule('/src/auth/AuthContext.tsx'))
  ;({
    TenantSwitcher, refreshOnPickerOpen,
  } = await vite.ssrLoadModule('/src/shell/TenantSwitcher.tsx'))
  ;({ grantableRoles } = await vite.ssrLoadModule('/src/data/principals.ts'))
  ;({ PrincipalsView } = await vite.ssrLoadModule('/src/screens/Principals.tsx'))
  ;({ RegistryView } = await vite.ssrLoadModule('/src/screens/Registry.tsx'))
})

after(async () => {
  await vite.close()
})

const b64url = (value) => Buffer.from(value).toString('base64url')

/** A real HS256 JWT over exactly the given payload, signed like the server signs. */
function sign(payload) {
  const head = b64url(JSON.stringify({ alg: 'HS256', typ: 'JWT' }))
  const body = b64url(JSON.stringify(payload))
  const signature = createHmac('sha256', 'test-signing-key').update(`${head}.${body}`).digest('base64url')
  return `${head}.${body}.${signature}`
}

const inFifteenMinutes = () => Math.floor(Date.now() / 1000) + 900

// The claim set BasicAuthIssuer.Issue emits for the PLATFORM-scoped bootstrap
// principal, byte-derived from internal/domain/identity/token.go (Claims plus
// jwt.RegisteredClaims; organization_id and project_id are omitempty and a
// platform scope leaves both zero, so the keys are genuinely ABSENT — not
// empty, not null):
//
//   {"iss":...,"sub":...,"aud":["https://api.hashicorp.cloud"],
//    "exp":...,"iat":...,"sid":...,"scope":[],"grants":[]}
//
// This is the token /init's credential produces, and the one shape the suite
// never held before — which is how a decoder requiring organization_id
// survived every gate (duf-tkw).
function platformToken(exp = inFifteenMinutes()) {
  return sign({
    iss: 'https://dufflebag.local',
    sub: '01ARZ3NDEKTSV4RRFFQ69G5FAV',
    aud: ['https://api.hashicorp.cloud'],
    exp,
    iat: exp - 900,
    sid: '3b7fe617-532a-4e23-bb70-678ec8ec7661',
    scope: [],
    grants: [],
  })
}

test('session renewal schedules one minute before expiry and reschedules after success', async () => {
  const now = Date.now
  const realNow = now()
  Date.now = () => realNow
  try {
    const scheduled = []
    const schedule = (callback, delay) => {
      const timer = { callback, delay, cleared: false }
      scheduled.push(timer)
      return timer
    }
    const firstClaims = { expiresAt: new Date(realNow + 10 * 60_000) }
    scheduleSessionRenewal(firstClaims, async () => {
      await renewConsoleSession(
        async () => platformToken(Math.floor((realNow + 20 * 60_000) / 1000)),
        (_token, claims) => scheduleSessionRenewal(claims, () => {}, schedule),
        () => assert.fail('a successful renewal must not expire the session'),
      )
    }, schedule)
    assert.equal(scheduled[0].delay, 9 * 60_000)
    await scheduled[0].callback()
    assert.equal(scheduled.length, 2)
    assert.equal(
      scheduled[1].delay,
      Math.floor((realNow + 20 * 60_000) / 1000) * 1000 - realNow - 60_000,
    )
  } finally {
    Date.now = now
  }
})

test('session renewal signs out as expired when the cookie session is gone', async () => {
  let reason = null
  await renewConsoleSession(async () => null, () => assert.fail('no session must not be entered'), () => {
    reason = 'expired'
  })
  assert.equal(reason, 'expired')
})

test('session renewal timer cleanup clears the scheduled timer on sign-out or unmount', () => {
  let cleared = null
  const cancel = scheduleSessionRenewal(
    { expiresAt: new Date(Date.now() + 120_000) },
    () => {},
    () => ({ id: 'renewal' }),
    (timer) => { cleared = timer },
  )
  cancel()
  assert.deepEqual(cleared, { id: 'renewal' })
})

test('the token Issue emits for a platform principal decodes as platform scope', () => {
  const claims = decodeClaims(platformToken())
  assert.ok(claims, 'the bootstrap token must decode rather than read as malformed')
  assert.equal(claims.sub, '01ARZ3NDEKTSV4RRFFQ69G5FAV')
  assert.equal(claims.organizationID, null)
  assert.equal(claims.projectID, null)
})

test('a tenancy token still carries its organisation, and an absent project stays null', () => {
  const exp = inFifteenMinutes()
  const claims = decodeClaims(sign({
    iss: 'https://dufflebag.local',
    sub: 'p-1',
    aud: ['https://api.hashicorp.cloud'],
    exp,
    iat: exp - 900,
    sid: 's-1',
    scope: [],
    grants: [],
    organization_id: '3f2b1c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d',
  }))
  assert.ok(claims)
  assert.equal(claims.organizationID, '3f2b1c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d')
  assert.equal(claims.projectID, null)
})

// Absence means platform; PRESENCE of something that is not a string means the
// token was not minted by this server. The two must not blur, or the decoder
// starts accepting shapes Verify would refuse (token.go).
test('a present but non-string tenancy claim stays malformed', () => {
  const exp = inFifteenMinutes()
  const base = {
    iss: 'https://dufflebag.local', sub: 'p-1', aud: ['https://api.hashicorp.cloud'],
    exp, iat: exp - 900, sid: 's-1', scope: [], grants: [],
  }
  assert.equal(decodeClaims(sign({ ...base, organization_id: 7 })), null)
  assert.equal(decodeClaims(sign({ ...base, organization_id: null })), null)
  assert.equal(decodeClaims(sign({ ...base, organization_id: 'org-1', project_id: 7 })), null)
})

// Verify refuses a project without an organization as malformed rather than
// narrow (token.go), so the decoder must not construct that state either.
test('a project without an organisation is malformed, not narrow', () => {
  const exp = inFifteenMinutes()
  const claims = decodeClaims(sign({
    iss: 'https://dufflebag.local', sub: 'p-1', aud: ['https://api.hashicorp.cloud'],
    exp, iat: exp - 900, sid: 's-1', scope: [], grants: [],
    project_id: '9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d',
  }))
  assert.equal(claims, null)
})

// The gap states a platform session can be in, each said plainly rather than
// rendered as a healthy empty table (the finding-17 rule applied to tenancy).
test('a platform session states what is missing, in order', () => {
  const base = {
    platform: true, organizationCount: 1,
    selectedOrganization: 'org-1', projectCount: 1, selectedProject: 'proj-1',
  }
  assert.equal(platformTenancyGap(base), null)
  assert.match(platformTenancyGap({ ...base, organizationCount: 0 }).title, /No organisations exist/)
  assert.match(platformTenancyGap({ ...base, selectedOrganization: null }).title, /Choose an organisation/)
  assert.match(platformTenancyGap({ ...base, projectCount: 0 }).title, /No projects in this organisation/)
  assert.match(platformTenancyGap({ ...base, selectedProject: null }).title, /Choose a project/)
  assert.equal(platformTenancyGap({ ...base, platform: false, organizationCount: 0 }), null)
})

// duf-4qr: the blank project row means an ORGANISATION-scoped session can also
// stand above a project. Its organisation is the token's and never a choice,
// so only the project half of the gap applies — and it must apply, or the
// deliberate step up renders as a healthy empty table.
test('an organisation-scoped session at the blank project row gets the project gap', () => {
  const atBlank = {
    platform: false, organizationCount: 0,
    selectedOrganization: 'org-1', projectCount: 3, selectedProject: null,
  }
  assert.match(platformTenancyGap(atBlank).title, /Choose a project/)
  assert.match(
    platformTenancyGap({ ...atBlank, projectCount: 0 }).title,
    /No projects in this organisation/,
  )
})

/**
 * A constructed session for rendering context consumers. The defaults are a
 * PLATFORM session that has discovered one organisation — the state whose
 * auto-select used to trap the root inside the tenancy (adversarial review;
 * ADR-0014 says "nothing selected" is platform standing and must remain
 * reachable).
 */
function session(over = {}) {
  return {
    state: {
      token: 't',
      claims: { sub: 'p-1', organizationID: null, projectID: null, expiresAt: new Date(Date.now() + 900000) },
    },
    self: { role: 'root' },
    signIn: async () => {}, signOut: () => {},
    organizations: [{ id: 'org-1', name: 'default', created_at: '2026-07-01T00:00:00Z' }],
    boundOrganizationName: null,
    organizationsLoading: false, organizationFailure: null,
    organizationRefreshFailure: null, refreshOrganizations: async () => {},
    selectedOrganization: null, selectOrganization: () => {},
    permittedProjects: [], projectNames: {},
    selectedProject: null, selectProject: () => {},
    projectsLoading: false, projectFailure: null,
    refreshProjects: async () => [],
    ...over,
  }
}

const switcherMarkup = (value) =>
  renderToStaticMarkup(React.createElement(
    MemoryRouter, { initialEntries: ['/'] },
    React.createElement(
      AuthContext.Provider, { value },
      React.createElement(TenantSwitcher),
    ),
  ))

// The organisation select in a PLATFORM session leads with an explicit
// platform row so the root can always step back up instead of being trapped by
// the sole-organisation auto-select. It stores '', which the auto-select cannot
// undo ('' is not nullish). The project row keeps its deliberate dash marker.
test('the organisation rows lead with the labelled platform row', () => {
  const rows = organizationRows([{ id: 'org-1', name: 'default', created_at: '2026-07-01T00:00:00Z' }])
  assert.equal(rows[0].id, '')
  assert.equal(rows[0].name, 'All organisations (platform)')
  assert.deepEqual(rows.slice(1).map((row) => row.id), ['org-1'])
})

test('sole-organisation auto-select preserves deliberate platform standing', () => {
  const listed = [{ id: 'org-1', name: 'default', created_at: '2026-07-01T00:00:00Z' }]
  assert.equal(selectionAfterInitialOrganizationLoad(null, listed), 'org-1')
  assert.equal(selectionAfterInitialOrganizationLoad('', listed), '')
})

test('masthead pickers carry visible captions and loading uses a compact skeleton', () => {
  const markup = switcherMarkup(session({
    selectedOrganization: 'org-1',
    selectedProject: 'proj-1',
    permittedProjects: ['proj-1'],
    projectNames: { 'proj-1': 'widgets' },
  }))
  assert.match(markup, /Organisation:[\s\S]*tenant-organization/)
  assert.match(markup, /Project:[\s\S]*tenant-project/)
  // The smoke test drives the pickers through their inner inputs by id and
  // reads the selection from input.value — PatternFly only places the id on
  // the real <input> via inputId, so the markup must bind them together.
  assert.match(markup, /<input(?=[^>]*id="tenant-organization-input")(?=[^>]*value="[^"]+")[^>]*>/)
  assert.match(markup, /<input(?=[^>]*id="tenant-project-input")(?=[^>]*value="widgets")[^>]*>/)

  const loading = switcherMarkup(session({ organizationsLoading: true }))
  assert.match(loading, /Organisation:/)
  assert.match(loading, /pf-v6-c-skeleton/)
  assert.match(loading, /Loading organisations…/)
  assert.doesNotMatch(loading, /Organisations could not be loaded|No organisations exist/)

  const failed = switcherMarkup(session({ organizationFailure: 'network unavailable' }))
  assert.match(failed, /Organisation:[\s\S]*Organisations could not be loaded/)
  assert.doesNotMatch(failed, /pf-v6-c-skeleton|No organisations exist/)
})

test('a projects refresh preserves organisation standing and dead selections fall to oldest', async () => {
  const projects = [
    { id: 'p-old', created_at: '2026-01-01T00:00:00Z' },
    { id: 'p-new', created_at: '2026-02-01T00:00:00Z' },
  ]
  // '' is the deliberate dash — organisation standing — and must survive the
  // refresh a picker-open now triggers (it once raced and got stomped).
  assert.equal(selectionAfterProjectsRefresh('', projects), '')
  assert.equal(selectionAfterProjectsRefresh('p-new', projects), 'p-new')
  assert.equal(selectionAfterProjectsRefresh('p-gone', projects), 'p-old')
  assert.equal(selectionAfterProjectsRefresh(null, projects), 'p-old')
  assert.equal(selectionAfterProjectsRefresh(null, []), null)
})

test('opening a picker refreshes once, while closing it does not', async () => {
  let refreshes = 0
  const refresh = async () => { refreshes++ }
  refreshOnPickerOpen(false, refresh)
  assert.equal(refreshes, 0)
  refreshOnPickerOpen(true, refresh)
  assert.equal(refreshes, 1)
})

// duf-4hje: newly created projects only appeared after an organisation
// round-trip, because the project picker opened onto a cached list. Both
// pickers must wire the open-refresh, not just the organisation one.
test('all three pickers refresh their listing on open', () => {
  assert.match(tenantSwitcherSource, /refreshOnPickerOpen\(true, refreshOrganizations\)/)
  assert.match(tenantSwitcherSource, /refreshOnPickerOpen\(true, refreshProjects\)/)
  assert.match(tenantSwitcherSource, /refreshOnPickerOpen\(true, onRefresh\)/)
})

test('opening and typing into a picker starts from an unfiltered listing', () => {
  const typeahead = tenantSwitcherSource.match(
    /function TypeaheadPicker\([\s\S]*?\nfunction OrganizationSelect/,
  )?.[0] ?? ''
  assert.match(typeahead, /if \(nextOpen\) \{[\s\S]*?setFilterValue\(''\)/)
  assert.match(
    typeahead,
    /onChange=\{\(_event, value\) => \{[\s\S]*?setFilterValue\(value\)[\s\S]*?setOpen\(true\)/,
  )
  assert.doesNotMatch(
    typeahead,
    /onChange=\{\(_event, value\) => \{[\s\S]*?if \(!open\) setPickerOpen\(true\)/,
  )
})

test('opening the project picker refreshes without replacing its settled input', () => {
  const refreshProjects = authContextSource.match(
    /const refreshProjects = useCallback[\s\S]*?\n  \}, \[state, selectedOrganization, signOut\]\)/,
  )?.[0] ?? ''
  assert.match(refreshProjects, /setOrganizationProjects\(ordered\)/)
  assert.doesNotMatch(refreshProjects, /setProjectsLoading\(/)
})

test('concurrent organisation refresh signals share one request', async () => {
  let finish
  let starts = 0
  const flight = { current: null }
  const start = () => {
    starts++
    return new Promise((resolve) => { finish = resolve })
  }
  const first = startOrganizationRefresh(flight, start)
  const second = startOrganizationRefresh(flight, start)
  assert.equal(first, second)
  assert.equal(starts, 1)
  finish()
  await first
  await startOrganizationRefresh(flight, async () => { starts++ })
  assert.equal(starts, 2, 'a later picker opening must be allowed to refresh again')
})

test('a failed organisation refresh keeps the loaded list and reports the failure', () => {
  const organizations = [
    { id: 'org-1', name: 'default', created_at: '2026-07-01T00:00:00Z' },
    { id: 'org-2', name: 'acme', created_at: '2026-07-02T00:00:00Z' },
  ]
  const refreshed = applyOrganizationRefresh(
    { organizations, failure: null },
    { kind: 'failed', failure: 'network unavailable' },
  )
  assert.equal(refreshed.organizations, organizations)
  assert.equal(refreshed.failure, 'network unavailable')

  const markup = switcherMarkup(session({
    organizations,
    organizationRefreshFailure: refreshed.failure,
    selectedOrganization: 'org-1',
  }))
  assert.match(markup, /default/)
  assert.match(markup, /Show organisation refresh failure/)
  assert.match(
    tenantSwitcherSource,
    /alertSeverityVariant="danger"[\s\S]*?<span role="alert">Organisations could not be refreshed:/,
  )
})

test('refresh selection changes only when the selected organisation disappeared', () => {
  const listed = [
    { id: 'org-1', name: 'default', created_at: '2026-07-01T00:00:00Z' },
    { id: 'org-2', name: 'acme', created_at: '2026-07-02T00:00:00Z' },
  ]
  assert.equal(selectionAfterOrganizationRefresh('org-1', listed), 'org-1')
  assert.equal(selectionAfterOrganizationRefresh('', listed), '')
  assert.equal(selectionAfterOrganizationRefresh(null, [listed[0]]), null)
  assert.equal(selectionAfterOrganizationRefresh('deleted-org', listed), '')
})

test('a platform session at the labelled platform row stands at the platform: root only', () => {
  // The toggle names the platform row — the step up reads as deliberate, not as an
  // unanswered prompt — and no project select renders beneath a non-organisation.
  const markup = switcherMarkup(session({ selectedOrganization: '' }))
  assert.match(markup, /tenant-organization/)
  assert.match(markup, /All organisations \(platform\)/)
  assert.doesNotMatch(markup, /Choose an organisation/)
  assert.doesNotMatch(markup, /tenant-project/)

  // At '' the principals screen answers the PLATFORM scope again: its empty
  // state is the platform one, never the organisation-level explanation…
  const principals = renderToStaticMarkup(React.createElement(PrincipalsView, {
    principals: [], loading: false, failure: null, reload: async () => {},
    selfID: null, callerRole: null, token: 't', organizationID: '', projectID: null,
  }))
  assert.match(principals, /No service principals are visible to you/)
  assert.doesNotMatch(principals, /No organisation-scoped principals/)
  // …and the role list reverts to the one legally unscoped role.
  assert.deepEqual(grantableRoles('platform', null), ['root'])
})

// Tenancy sessions are untouched: their organisation is the token's and never
// a choice, so the combined selector renders alone, with no organisation
// select and no dash row to step out of the tenancy with.
test('a tenancy session gets no dash row', () => {
  const markup = switcherMarkup(session({
    state: {
      token: 't',
      claims: { sub: 'p-1', organizationID: 'org-1', projectID: 'proj-1', expiresAt: new Date(Date.now() + 900000) },
    },
    organizations: [],
    boundOrganizationName: 'acme',
    selectedOrganization: 'org-1',
    selectedProject: 'proj-1',
    permittedProjects: ['proj-1'],
    projectNames: { 'proj-1': 'widgets' },
  }))
  assert.match(markup, /tenant-project/)
  assert.match(markup, /acme \/ widgets/)
  assert.doesNotMatch(markup, /tenant-organization/)
  assert.doesNotMatch(markup, /—/)
})

test('all three masthead pickers use typeahead toggles and role-gated creation footers', () => {
  assert.match(tenantSwitcherSource, /<Select[\s\S]*?variant="typeahead"/)
  assert.match(tenantSwitcherSource, /<MenuToggle[\s\S]*?variant="typeahead"/)
  assert.match(tenantSwitcherSource, /<TextInputGroupMain/)
  assert.match(tenantSwitcherSource, /label\.toLowerCase\(\)\.includes\(query\)/)
  assert.equal((tenantSwitcherSource.match(/<MenuFooter>/g) ?? []).length, 3)
  assert.match(tenantSwitcherSource, /<MenuFooter>[\s\S]*?kind="organization"/)
  assert.match(tenantSwitcherSource, /<MenuFooter>[\s\S]*?kind="project"/)
  assert.match(tenantSwitcherSource, /<MenuFooter>[\s\S]*?<CreateBucketButton/)
})

test('zero-organisation masthead keeps its compact recovery action', () => {
  const markup = switcherMarkup(session({ organizations: [] }))
  assert.match(markup, /No organisations exist/)
  assert.match(markup, /Create organisation/)
  assert.doesNotMatch(markup, /Requires root/)
})

test('the data screens render a tenancy gap instead of a healthy empty state', () => {
  const gap = platformTenancyGap({
    platform: true, organizationCount: 0,
    selectedOrganization: null, projectCount: 0, selectedProject: null,
  })
  assert.match(gap.title, /No organisations exist/)
  const landing = renderToStaticMarkup(React.createElement(RegistryView, {
    onConnectClient: () => {},
  }))
  assert.match(landing, /Choose a bucket/)
  assert.match(landing, /masthead picker/)
  assert.doesNotMatch(landing, /No buckets yet/)
})
