import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let VersionsView
let VersionsFacet
let versionPage
let updateVersionSelection
let partitionBulkVersions
let runBulkVersionAction
let BulkVersionActionModalView
let BucketChannelsFacet
let CreateChannelModalView
let AssignChannelModalView
let DeleteChannelModalView
let AssignmentHistoryTable
let EnforcedProvisionersRow
let VersionView
let RevokeModalView
let RestoreModalView
let DeleteVersionModalView
let OperationsCard
let BuildTable
let BuildView
let ArtifactsCard
let PackagesCard
let loadVersions
let loadVersion
let loadBucketPage
let loadEnforcedProvisioners
let loadChannelHistory
let loadVersionDetail
let loadBuildDetail
let terraformConsumeSnippet
let terraformPromotionSnippet
let packerBuildCommand
let sbomFileName
let channelVersionGap
let parentFreshnessText
let ApiError
let revokeVersion
let restoreVersion
let deleteVersion
let createChannel
let assignChannelVersion
let deleteChannel
let FacetRail
let platformTenancyGap
let TypedConfirmModalView
let CopyableIdentifier
const versionDataSource = readFileSync(new URL('../src/data/versions.ts', import.meta.url), 'utf8')
const versionScreenSource = readFileSync(new URL('../src/screens/Version.tsx', import.meta.url), 'utf8')
const versionsScreenSource = readFileSync(new URL('../src/screens/Versions.tsx', import.meta.url), 'utf8')
const buildScreenSource = readFileSync(new URL('../src/screens/Build.tsx', import.meta.url), 'utf8')
let VersionStateLabel
let BuildStateLabel

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ VersionsView, VersionsFacet, versionPage, updateVersionSelection, partitionBulkVersions,
    runBulkVersionAction, BulkVersionActionModalView,
    BucketChannelsFacet, CreateChannelModalView,
    AssignChannelModalView, DeleteChannelModalView, AssignmentHistoryTable,
    EnforcedProvisionersRow, channelVersionGap, parentFreshnessText, VersionStateLabel } =
    await vite.ssrLoadModule('/src/screens/Versions.tsx'))
  ;({ VersionView, RevokeModalView, RestoreModalView, DeleteVersionModalView, OperationsCard, BuildTable,
    terraformConsumeSnippet, terraformPromotionSnippet, BuildStateLabel } =
    await vite.ssrLoadModule('/src/screens/Version.tsx'))
  ;({ BuildView, ArtifactsCard, PackagesCard, packerBuildCommand, sbomFileName } =
    await vite.ssrLoadModule('/src/screens/Build.tsx'))
  ;({ loadVersions, loadVersion, loadBucketPage, loadEnforcedProvisioners, loadChannelHistory,
    loadVersionDetail, loadBuildDetail } =
    await vite.ssrLoadModule('/src/data/versions.ts'))
  ;({ platformTenancyGap } = await vite.ssrLoadModule('/src/data/tenant.ts'))
  ;({ FacetRail } = await vite.ssrLoadModule('/src/screens/RegistryFacets.tsx'))
  ;({ ApiError, revokeVersion, restoreVersion, deleteVersion, createChannel, assignChannelVersion,
    deleteChannel } = await vite.ssrLoadModule('/src/api/client.ts'))
  ;({ TypedConfirmModalView } =
    await vite.ssrLoadModule('/src/components/TypedConfirmModal.tsx'))
  ;({ CopyableIdentifier } =
    await vite.ssrLoadModule('/src/components/CopyableIdentifier.tsx'))
})

after(async () => {
  await vite.close()
})

// Fixtures derived from the SERVER's rendering, not imagined: renderVersion and
// renderBuild in internal/compat/hcp2023/handler.go, field names and omitempty
// behaviour from internal/compat/hcp2023/models/
// hashicorp_cloud_packer20230101_{version,build,artifact}.go. A complete
// version leaves "v0" and gains VERSION_ACTIVE together; an incomplete one is
// "v0" VERSION_RUNNING. `builds` and `artifacts` carry no omitempty, so they
// are always present, [] when empty.
const completeVersion = {
  id: '01K1CJ4X8GVQZJ0R1T2U3V4W5X',
  name: 'v1',
  bucket_name: 'images',
  fingerprint: 'fp-complete',
  template_type: 'HCL2',
  status: 'VERSION_ACTIVE',
  builds: [
    {
      id: '01K1CJ4X8H0000000000000001',
      version_id: '01K1CJ4X8GVQZJ0R1T2U3V4W5X',
      component_type: 'docker.ubuntu',
      status: 'BUILD_DONE',
      platform: 'docker',
      packer_run_uuid: 'run-1',
      labels: { ImageDigest: 'sha256:label-value' },
      metadata: { packer: {
        version: '1.16.0',
        options: {
          path: './image.pkr.hcl', vars: ['base_image', 'run_label'],
          'var-files': ['./production.pkrvars.hcl'],
          only: ['docker.ubuntu'], debug: true, force: true,
        },
        os: { type: 'linux', details: { arch: 'amd64', version: '6.12' } },
        plugins: [{ name: 'docker', version: '1.1.4' }],
      } },
      artifacts: [
        {
          id: '01K1CJ4X8H0000000000000002',
          external_identifier: 'sha256:abc123',
          region: 'us-east-1',
          created_at: '2026-07-31T10:00:00.000Z',
        },
      ],
      created_at: '2026-07-31T10:00:00.000Z',
      updated_at: '2026-07-31T10:05:00.000Z',
    },
  ],
  created_at: '2026-07-31T10:00:00.000Z',
  updated_at: '2026-07-31T10:05:00.000Z',
}

const incompleteVersion = {
  id: '01K1CJ4X8H0000000000000003',
  name: 'v0',
  bucket_name: 'images',
  fingerprint: 'fp-incomplete',
  template_type: 'HCL2',
  status: 'VERSION_RUNNING',
  builds: [],
  created_at: '2026-07-31T11:00:00.000Z',
  updated_at: '2026-07-31T11:00:00.000Z',
}

const withFetch = async (routes, run) => {
  const originalFetch = globalThis.fetch
  globalThis.fetch = async (input) => {
    const path = String(input)
    for (const [suffix, respond] of Object.entries(routes)) {
      if (path.endsWith(suffix)) return respond()
    }
    throw new Error(`unexpected request ${path}`)
  }
  try {
    return await run()
  } finally {
    globalThis.fetch = originalFetch
  }
}

const json = (body, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })

const listMarkup = (versions, extra = {}) =>
  renderToStaticMarkup(React.createElement(VersionsView, {
    bucket: 'images',
    versions,
    loading: false,
    failure: null,
    onBack: () => {},
    onOpenVersion: () => {},
    ...extra,
  }))

const versionsFacetMarkup = (versions) =>
  renderToStaticMarkup(React.createElement(VersionsFacet, {
    versions,
    onOpenVersion: () => {},
  }))

const detailMarkup = (version, extra = {}) =>
  renderToStaticMarkup(React.createElement(VersionView, {
    bucket: 'images',
    version,
    loading: false,
    failure: null,
    onBackToRegistry: () => {},
    onBackToBucket: () => {},
    ...extra,
  }))

const actionVersion = (state = 'complete') => ({
  name: 'v7', fingerprint: 'fp-action', state, templateType: 'HCL2', channels: [],
  assignments: [], builds: [], parents: [], children: [], created: '2026-08-12T10:00:00.000Z',
})

const channelFixture = (over = {}) => ({
  name: 'production', versionName: 'v7', fingerprint: 'fp-action', managed: false,
  restricted: false, assignedAt: '2026-08-12T10:01:00.000Z', author: 'publisher', ...over,
})

const channelVersions = [
  { ...actionVersion(), name: 'v8', fingerprint: 'fp-new' },
  actionVersion(),
  { ...actionVersion('incomplete'), name: 'v0', fingerprint: 'fp-incomplete' },
  { ...actionVersion('revoked'), name: 'v6', fingerprint: 'fp-revoked' },
  { ...actionVersion('revocation-scheduled'), name: 'v5', fingerprint: 'fp-scheduled' },
]

const channelFacetMarkup = (callerRole, channels = [
  channelFixture({ name: 'latest', managed: true, restricted: true }),
  channelFixture(),
]) => renderToStaticMarkup(React.createElement(
  BucketChannelsFacet,
  {
    bucket: 'images',
    channels,
    versions: channelVersions,
    latestVersion: { name: 'v8', fingerprint: 'fp-new' },
    callerRole,
    onOpenVersion: () => {},
    onCreateChannel: async () => {},
    onAssignChannel: async () => {},
    onDeleteChannel: async () => {},
  },
))

const revokeModalProps = (over = {}) => ({
  bucket: 'images', version: actionVersion(), callerRole: 'publisher', message: '', when: 'now',
  scheduledAt: '', skipDescendants: false, disableRollback: false, submitting: false,
  failure: null, onMessageChange: () => {}, onWhenChange: () => {},
  onScheduledAtChange: () => {}, onSkipDescendantsChange: () => {},
  onDisableRollbackChange: () => {}, onConfirm: async () => {}, onClose: () => {}, ...over,
})

test('version and channel screens keep raw timestamps on semantic time elements', () => {
  const versionMarkup = detailMarkup({
    ...actionVersion(), created: '2026-08-12T10:00:00.123456Z',
  })
  assert.match(versionMarkup, /<time[^>]*dateTime="2026-08-12T10:00:00.123456Z"/)

  const channelMarkup = channelFacetMarkup('publisher')
  assert.match(channelMarkup, /<time[^>]*dateTime="2026-08-12T10:01:00.000Z"/)
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
  return findElement([node.props.children, node.props.body], predicate)
}

const renderTyped = (element) => renderToStaticMarkup(React.createElement(
  TypedConfirmModalView,
  { ...element.props, confirmation: '', onConfirmationChange: () => {} },
))

test('a complete version projects complete and a v0 projects incomplete', async () => {
  const versions = await withFetch(
    {
      // ListVersions answers newest first (registry_handler.go listVersions).
      '/versions': () => json({ versions: [incompleteVersion, completeVersion] }),
      '/channels': () => json({ channels: [] }),
    },
    () => loadVersions('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  assert.deepEqual(
    versions.map(({ fingerprint, name, state }) => ({ fingerprint, name, state })),
    [
      { fingerprint: 'fp-incomplete', name: 'v0', state: 'incomplete' },
      { fingerprint: 'fp-complete', name: 'v1', state: 'complete' },
    ],
  )
})

test('a revoked version projects and renders its revocation state, not incomplete', async () => {
  const versions = await withFetch(
    {
      '/versions': () => json({ versions: [
        { ...completeVersion, status: 'VERSION_REVOKED' },
        { ...completeVersion, fingerprint: 'fp-scheduled', status: 'VERSION_REVOCATION_SCHEDULED' },
      ] }),
      '/channels': () => json({ channels: [] }),
    },
    () => loadVersions('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  // A revoked version keeps its number: completion is carried by the name.
  assert.deepEqual(
    versions.map(({ name, state }) => ({ name, state })),
    [
      { name: 'v1', state: 'revoked' },
      { name: 'v1', state: 'revocation-scheduled' },
    ],
  )
  const markup = versionsFacetMarkup(versions)
  assert.match(markup, />revoked</)
  assert.match(markup, />revocation scheduled</)
  assert.doesNotMatch(markup, />incomplete</)
})

test('a restored version projects and renders as active again', async () => {
	const restoredVersion = {
		...completeVersion,
		fingerprint: 'fp-restored',
		status: 'VERSION_ACTIVE',
		revoke_at: null,
	}
	const versions = await withFetch(
		{
			'/versions': () => json({ versions: [restoredVersion] }),
			'/channels': () => json({ channels: [] }),
		},
		() => loadVersions('token', { organizationID: 'org', projectID: 'project' }, 'images'),
	)
	assert.deepEqual(versions.map(({ name, state }) => ({ name, state })), [
		{ name: 'v1', state: 'complete' },
	])
	const markup = versionsFacetMarkup(versions)
	assert.match(markup, />complete</)
	assert.doesNotMatch(markup, />revoked</)
})

test('version safety actions are live for publisher and restricted below publisher', () => {
  for (const role of ['reader', 'builder']) {
    const revoke = detailMarkup(actionVersion('complete'), { callerRole: role })
    assert.match(revoke, /Requires publisher/)
    assert.match(revoke, /<button[^>]*disabled[^>]*>[\s\S]{0,240}Revoke/)

    const restore = detailMarkup(actionVersion('revoked'), { callerRole: role })
    assert.match(restore, /Requires publisher/)
    assert.match(restore, /<button[^>]*disabled[^>]*>[\s\S]{0,240}Restore/)
    assert.match(revoke, /<button[^>]*disabled[^>]*>[\s\S]{0,240}Delete version/)
  }

  for (const [state, label] of [['complete', 'Revoke'], ['revoked', 'Restore']]) {
    const markup = detailMarkup(actionVersion(state), { callerRole: 'publisher' })
    assert.match(markup, new RegExp(`>${label}<`))
    assert.doesNotMatch(markup, /Requires publisher/)
    assert.doesNotMatch(markup, new RegExp(`<button[^>]*disabled[^>]*>[\\s\\S]{0,240}${label}`))
    assert.match(markup, />Delete version</)
    assert.doesNotMatch(markup, /<button[^>]*disabled[^>]*>[\s\S]{0,240}Delete version/)
  }
})

test('bucket-detail deletion is live for publisher and disabled with a reason below publisher', () => {
  for (const role of ['reader', 'builder']) {
    const markup = listMarkup([], { callerRole: role })
    assert.match(markup, /Requires publisher/)
    assert.match(markup, /<button[^>]*disabled[^>]*>[\s\S]{0,240}Delete bucket/)
  }

  const publisher = listMarkup([], { callerRole: 'publisher' })
  assert.match(publisher, />Delete bucket</)
  assert.doesNotMatch(publisher, /Requires publisher/)
  assert.doesNotMatch(publisher, /<button[^>]*disabled[^>]*>[\s\S]{0,240}Delete bucket/)
})

test('the version state selects Revoke or Restore without a second action', () => {
  const active = detailMarkup(actionVersion('complete'), { callerRole: 'publisher' })
  assert.match(active, />Revoke</)
  assert.doesNotMatch(active, />Restore</)

  const revoked = detailMarkup(actionVersion('revoked'), { callerRole: 'publisher' })
  assert.match(revoked, />Restore</)
  assert.doesNotMatch(revoked, />Revoke</)

  const scheduled = detailMarkup(actionVersion('revocation-scheduled'), { callerRole: 'publisher' })
  assert.match(scheduled, />Restore</)
  assert.doesNotMatch(scheduled, />Revoke</)
  assert.match(scheduled, /Restoring cancels the scheduled revocation/)
})

test('the revoke modal builds immediate and scheduled options from its controls', async () => {
  const immediateCalls = []
  const before = Date.now()
  const immediate = RevokeModalView(revokeModalProps({
    message: '   ', onConfirm: async (options) => immediateCalls.push(options),
  }))
  assert.equal(immediate.props.expected, 'v7')
  assert.match(renderTyped(immediate), /Type <strong>v7<\/strong> to confirm/)
  await immediate.props.onConfirm()
  const after = Date.now()
  assert.equal(immediateCalls.length, 1)
  assert.deepEqual(Object.keys(immediateCalls[0]), ['revoke_at'])
  assert.ok(Date.parse(immediateCalls[0].revoke_at) >= before)
  assert.ok(Date.parse(immediateCalls[0].revoke_at) <= after)

  let skipDescendants = false
  let disableRollback = false
  const scheduledCalls = []
  const props = () => revokeModalProps({
    message: 'retired image',
    when: 'scheduled',
    scheduledAt: '2099-04-03T12:30',
    skipDescendants,
    disableRollback,
    onSkipDescendantsChange: (checked) => { skipDescendants = checked },
    onDisableRollbackChange: (checked) => { disableRollback = checked },
    onConfirm: async (options) => scheduledCalls.push(options),
  })
  const choices = RevokeModalView(props())
  findElement(choices, (element) => element.props.label === 'Skip descendant revocation')
    .props.onChange({}, true)
  findElement(choices, (element) => element.props.label === 'Do not roll channels back')
    .props.onChange({}, true)
  assert.equal(skipDescendants, true)
  assert.equal(disableRollback, true)

  const scheduled = RevokeModalView(props())
  await scheduled.props.onConfirm()
  assert.deepEqual(scheduledCalls, [{
    revoke_at: new Date('2099-04-03T12:30').toISOString(),
    revocation_message: 'retired image',
    skip_descendants_revocation: true,
    disable_rollback_channels: true,
  }])
})

test('the revoke modal uses the medium PatternFly variant', () => {
  assert.match(
    versionScreenSource,
    /title=\{`Revoke \$\{bucket\} \$\{version\.name\}`\}[\s\S]{0,80}variant="medium"/,
  )
})

test('revoke, restore and delete clients send exact compat-plane requests', async () => {
  const originalFetch = globalThis.fetch
  const calls = []
  globalThis.fetch = async (input, init) => {
    calls.push({ path: String(input), init })
    return json({ version: completeVersion })
  }
  try {
    const tenant = { organizationID: 'org one', projectID: 'project/one' }
    await revokeVersion('bearer', tenant, 'images/base', 'fp one', {
      revoke_at: '2026-08-12T14:00:00.000Z',
      revocation_message: '',
    })
    await revokeVersion('bearer', tenant, 'images/base', 'fp one', {
      revoke_at: '2026-08-13T09:15:00.000Z',
      revocation_message: 'retired image',
      skip_descendants_revocation: true,
      disable_rollback_channels: true,
    })
    await restoreVersion('bearer', tenant, 'images/base', 'fp one')
    await deleteVersion('bearer', tenant, 'images/base', 'fp one')
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.equal(calls.length, 4)
  for (const call of calls) {
    assert.equal(call.init.headers.Authorization, 'Bearer bearer')
    assert.match(
      call.path,
      /\/organizations\/org%20one\/projects\/project%2Fone\/buckets\/images%2Fbase\/versions\/fp%20one$/,
    )
  }
  assert.deepEqual(calls.map(({ init }) => init.method), ['PATCH', 'PATCH', 'PATCH', 'DELETE'])
  assert.deepEqual(JSON.parse(calls[0].init.body), {
    revoke_at: '2026-08-12T14:00:00.000Z',
  })
  assert.equal('revoke_in' in JSON.parse(calls[0].init.body), false)
  assert.deepEqual(JSON.parse(calls[1].init.body), {
    revoke_at: '2026-08-13T09:15:00.000Z',
    revocation_message: 'retired image',
    skip_descendants_revocation: true,
    disable_rollback_channels: true,
  })
  assert.deepEqual(JSON.parse(calls[2].init.body), { restore: true })
  assert.equal(calls[3].init.body, undefined)
})

test('delete version confirmation states permanence and renders server refusal verbatim', async () => {
  const refusal = 'Version is assigned by channels: production. Please, remove the channels assignment before deleting the version.'
  const calls = []
  const markup = renderTyped(DeleteVersionModalView({
    bucket: 'images', version: actionVersion(), callerRole: 'publisher', submitting: false,
    failure: refusal, onConfirm: async () => calls.push('delete'), onClose: () => {},
  }))
  assert.match(markup, /Delete images — v7/)
  assert.match(markup, /Type <strong>v7<\/strong> to confirm/)
  assert.match(markup, /is permanent/)
  assert.match(markup, /builds, artifacts and SBOMs/)
  assert.match(markup, /Channels must be unassigned/)
  assert.match(markup, new RegExp(refusal.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))

  const view = DeleteVersionModalView({
    bucket: 'images', version: actionVersion(), callerRole: 'publisher', submitting: false,
    failure: null, onConfirm: async () => calls.push('delete'), onClose: () => {},
  })
  assert.equal(view.props.expected, 'v7')
  await view.props.onConfirm()
  assert.deepEqual(calls, ['delete'])
})

test('the Version container navigates to the bucket only after delete succeeds', () => {
  assert.match(
    versionScreenSource,
    /await deleteVersion\(state\.token, tenant, bucket, fingerprint\)\s+navigate\(`\/buckets\/\$\{encodeURIComponent\(bucket\)\}`\)/,
  )
})

test('a compat-plane ApiError message is rendered verbatim in either modal', () => {
  const message = new ApiError(409, 'restore refused by registry policy').message
  const revoke = renderTyped(RevokeModalView(revokeModalProps({ failure: message })))
  const restore = renderToStaticMarkup(React.createElement(RestoreModalView, {
    bucket: 'images', version: actionVersion('revoked'), callerRole: 'publisher',
    submitting: false, failure: message, onConfirm: async () => {}, onClose: () => {},
  }))
  for (const markup of [revoke, restore]) {
    assert.match(markup, /The action was refused/)
    assert.match(markup, /restore refused by registry policy/)
  }
})

test('channel actions are live for publisher and disabled with a reason below publisher', () => {
  for (const role of ['reader', 'builder']) {
    const markup = channelFacetMarkup(role)
    assert.match(markup, /Requires publisher/)
    assert.match(markup, /<button[^>]*disabled[^>]*>[\s\S]{0,240}Create channel/)
    assert.match(markup, /<button[^>]*disabled[^>]*aria-label="Actions for production"/)
  }

  const publisher = channelFacetMarkup('publisher')
  assert.match(publisher, />Create channel</)
  assert.match(publisher, /aria-label="Actions for production"/)
  assert.doesNotMatch(publisher, /Requires publisher/)
  assert.doesNotMatch(publisher, /<button[^>]*disabled[^>]*aria-label="Actions for production"/)
})

test('disabled channel menu items keep name-only labels and attach their publisher reason', () => {
  assert.match(versionsScreenSource, /tooltipProps=\{refused \? \{ content: reason \} : undefined\}/)
  assert.match(versionsScreenSource, />\s*Assign version…\s*\{refused \? <span/)
  assert.match(versionsScreenSource, />\s*Delete channel\s*\{refused \? <span/)
  assert.doesNotMatch(versionsScreenSource, /Assign version…\{refused \? ` — \$\{reason\}`/)
  assert.doesNotMatch(versionsScreenSource, /Delete channel\{refused \? ` — \$\{reason\}`/)
})

test('version and build labels map lifecycle states to PatternFly status treatment', () => {
  assert.deepEqual(
    ['complete', 'incomplete', 'revoked', 'revocation-scheduled'].map((state) =>
      [state, VersionStateLabel({ state }).props.status]),
    [
      ['complete', 'success'], ['incomplete', 'info'], ['revoked', 'danger'],
      ['revocation-scheduled', 'warning'],
    ],
  )
  assert.deepEqual(
    ['done', 'running', 'failed', 'cancelled', 'pending'].map((state) =>
      [state, BuildStateLabel({ state }).props.status]),
    [
      ['done', 'success'], ['running', 'info'], ['failed', 'danger'],
      ['cancelled', 'warning'], ['pending', 'warning'],
    ],
  )
})

test('SBOM file names use PatternFly truncation instead of native title text', () => {
  assert.match(buildScreenSource, /<Truncate content=\{sbomFileName\(sbom\)\} \/>/)
  assert.doesNotMatch(buildScreenSource, /title=\{sbomFileName\(sbom\)\}/)
})

test('Create channel moves between the empty state and populated card header with its role gate', () => {
  const emptyReader = channelFacetMarkup('reader', [])
  assert.match(emptyReader, /<h2[^>]*>No channels in this bucket<\/h2>/)
  assert.match(emptyReader, /pf-v6-c-empty-state__body">A channel names a version consumers can resolve/)
  assert.match(emptyReader, /pf-v6-c-empty-state[\s\S]*Create channel/)
  assert.match(emptyReader, /Requires publisher/)
  assert.equal((emptyReader.match(/Create channel/g) ?? []).length, 1)

  const populatedReader = channelFacetMarkup('reader')
  assert.match(populatedReader, /pf-v6-c-card__title[\s\S]{0,1000}Create channel/)
  assert.doesNotMatch(populatedReader, /No channels in this bucket/)
  assert.match(populatedReader, /Requires publisher/)
  assert.equal((populatedReader.match(/Create channel/g) ?? []).length, 1)
})

test('managed channel rows never render a kebab', () => {
  for (const role of ['reader', 'builder', 'publisher']) {
    const markup = channelFacetMarkup(role)
    assert.doesNotMatch(markup, /aria-label="Actions for latest"/)
    assert.match(markup, /aria-label="Actions for production"/)
  }
})

test('restricted channel controls require maintainer', () => {
  const restricted = [channelFixture({ name: 'production', restricted: true })]
  const publisher = channelFacetMarkup('publisher', restricted)
  assert.match(publisher, /aria-label="Actions for production"[^>]*disabled/)
  assert.match(publisher, /Requires maintainer/)

  const maintainer = channelFacetMarkup('maintainer', restricted)
  assert.match(maintainer, /aria-label="Actions for production"/)
  assert.doesNotMatch(maintainer, /Requires maintainer/)
})

test('channel version selects contain only active complete versions, newest first', () => {
  const create = renderToStaticMarkup(React.createElement(CreateChannelModalView, {
    bucket: 'images', versions: channelVersions, callerRole: 'publisher', name: 'staging',
    restricted: false, fingerprint: '', submitting: false, failure: null,
    onNameChange: () => {}, onRestrictedChange: () => {}, onFingerprintChange: () => {},
    onConfirm: async () => {}, onClose: () => {},
  }))
  const assign = renderToStaticMarkup(React.createElement(AssignChannelModalView, {
    bucket: 'images', channel: channelFixture(), versions: channelVersions,
    callerRole: 'publisher', fingerprint: 'fp-action', submitting: false, failure: null,
    onFingerprintChange: () => {}, onConfirm: async () => {}, onClose: () => {},
  }))
  for (const markup of [create, assign]) {
    assert.match(markup, /value="fp-new">v8/)
    assert.match(markup, /value="fp-action"[^>]*>v7/)
    assert.doesNotMatch(markup, /fp-incomplete|>v0</)
    assert.doesNotMatch(markup, /fp-revoked|fp-scheduled/)
    assert.ok(markup.indexOf('value="fp-new"') < markup.indexOf('value="fp-action"'))
  }
  assert.match(assign, /value="fp-action"[^>]*disabled[^>]*>v7 \(current\)/)
})

test('channel modal failures preserve duplicate and managed-refusal messages', () => {
  const duplicate = new ApiError(
    409, 'Error: The channel with identifier production already exists.',
  ).message
  const managed = new ApiError(
    400, 'Can\'t update channel assignment on channel "latest". This channel is managed by Dufflebag',
  ).message
  const create = renderToStaticMarkup(React.createElement(CreateChannelModalView, {
    bucket: 'images', versions: channelVersions, callerRole: 'publisher', name: 'production',
    restricted: false, fingerprint: '', submitting: false, failure: duplicate,
    onNameChange: () => {}, onRestrictedChange: () => {}, onFingerprintChange: () => {},
    onConfirm: async () => {}, onClose: () => {},
  }))
  const assign = renderToStaticMarkup(React.createElement(AssignChannelModalView, {
    bucket: 'images', channel: channelFixture({ name: 'latest', managed: true }),
    versions: channelVersions, callerRole: 'publisher', fingerprint: 'fp-new',
    submitting: false, failure: managed, onFingerprintChange: () => {},
    onConfirm: async () => {}, onClose: () => {},
  }))
  assert.match(create, /Error: The channel with identifier production already exists\./)
  assert.match(assign, /Can&#x27;t update channel assignment on channel &quot;latest&quot;\. This channel is managed by Dufflebag/)
})

test('delete confirmation names the channel, its expected string and its history consequence', async () => {
  const calls = []
  const view = DeleteChannelModalView({
    bucket: 'images', channel: channelFixture(), callerRole: 'publisher', submitting: false,
    failure: null, onConfirm: async () => calls.push('delete'), onClose: () => {},
  })
  assert.equal(calls.length, 0)
  assert.equal(view.props.expected, 'production')
  await view.props.onConfirm()
  assert.deepEqual(calls, ['delete'])
  const markup = renderTyped(DeleteChannelModalView({
    bucket: 'images', channel: channelFixture(), callerRole: 'publisher', submitting: false,
    failure: null, onConfirm: async () => {}, onClose: () => {},
  }))
  assert.match(markup, /Delete images — production/)
  assert.match(markup, /Deleting production destroys its assignment history\./)
  assert.match(markup, /Type <strong>production<\/strong> to confirm/)
  assert.match(markup, />Delete production</)
})

test('delete, restore and assign titles separate their object identifiers', () => {
  const restore = renderToStaticMarkup(React.createElement(RestoreModalView, {
    bucket: 'images', version: actionVersion('revoked'), callerRole: 'publisher',
    submitting: false, failure: null, onConfirm: async () => {}, onClose: () => {},
  }))
  const assign = renderToStaticMarkup(React.createElement(AssignChannelModalView, {
    bucket: 'images', channel: channelFixture(), versions: channelVersions,
    callerRole: 'publisher', fingerprint: 'fp-new', submitting: false, failure: null,
    onFingerprintChange: () => {}, onConfirm: async () => {}, onClose: () => {},
  }))
  assert.match(restore, /Restore images — v7/)
  assert.match(assign, /Assign images version — production/)
})

test('Promote is live for publisher, role-restricted below publisher, and absent unless active-complete', () => {
  const render = (state, callerRole) => renderToStaticMarkup(React.createElement(OperationsCard, {
    bucket: 'images', version: actionVersion(state),
    channels: [
      channelFixture({ name: 'latest', managed: true }),
      channelFixture({ name: 'production' }),
    ],
    callerRole,
    onPromote: async () => {},
  }))
  for (const role of ['reader', 'builder']) {
    const markup = render('complete', role)
    assert.match(markup, /Requires publisher/)
    assert.match(markup, /<button[^>]*disabled[^>]*>[\s\S]{0,240}Promote/)
  }
  const publisher = render('complete', 'publisher')
  assert.match(publisher, />Promote</)
  assert.doesNotMatch(publisher, /Requires publisher/)
  for (const state of ['incomplete', 'revoked', 'revocation-scheduled']) {
    const markup = render(state, 'publisher')
    assert.doesNotMatch(markup, /<button[^>]*>Promote<\/button>/)
    assert.match(markup, /hcp_packer_channel_assignment/)
  }
})

test('promoting to a restricted channel requires maintainer', () => {
  const render = (callerRole) => renderToStaticMarkup(React.createElement(OperationsCard, {
    bucket: 'images', version: actionVersion('complete'),
    channels: [channelFixture({ name: 'production', restricted: true })],
    callerRole, onPromote: async () => {},
  }))
  assert.match(render('publisher'), /Requires maintainer/)
  assert.doesNotMatch(render('maintainer'), /Requires maintainer/)
})

test('channel clients send exact compat-plane paths and bodies', async () => {
  const originalFetch = globalThis.fetch
  const calls = []
  globalThis.fetch = async (input, init) => {
    calls.push({ path: String(input), init })
    return new Response(null, { status: 204 })
  }
  try {
    const tenant = { organizationID: 'org one', projectID: 'project/one' }
    await createChannel('bearer', tenant, 'images/base', { name: 'plain' })
    await createChannel('bearer', tenant, 'images/base', {
      name: 'restricted', restricted: true,
    })
    await createChannel('bearer', tenant, 'images/base', {
      name: 'initial', fingerprint: 'fp one',
    })
    await createChannel('bearer', tenant, 'images/base', {
      name: 'both', restricted: true, fingerprint: 'fp two',
    })
    await assignChannelVersion('bearer', tenant, 'images/base', 'production/main', 'fp three')
    await deleteChannel('bearer', tenant, 'images/base', 'staging/main')
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.equal(calls.length, 6)
  for (const call of calls) assert.equal(call.init.headers.Authorization, 'Bearer bearer')
  assert.deepEqual(JSON.parse(calls[0].init.body), { name: 'plain' })
  assert.equal('restricted' in JSON.parse(calls[0].init.body), false)
  assert.equal('version_fingerprint' in JSON.parse(calls[0].init.body), false)
  assert.deepEqual(JSON.parse(calls[1].init.body), { name: 'restricted', restricted: true })
  assert.deepEqual(JSON.parse(calls[2].init.body), {
    name: 'initial', version_fingerprint: 'fp one',
  })
  assert.deepEqual(JSON.parse(calls[3].init.body), {
    name: 'both', restricted: true, version_fingerprint: 'fp two',
  })
  for (const call of calls.slice(0, 4)) {
    assert.equal(call.init.method, 'POST')
    assert.match(call.path, /\/organizations\/org%20one\/projects\/project%2Fone\/buckets\/images%2Fbase\/channels$/)
  }
  assert.equal(calls[4].init.method, 'PATCH')
  assert.match(calls[4].path, /\/channels\/production%2Fmain$/)
  assert.deepEqual(JSON.parse(calls[4].init.body), {
    update_mask: 'versionFingerprint', version_fingerprint: 'fp three',
  })
  assert.equal(calls[5].init.method, 'DELETE')
  assert.match(calls[5].path, /\/channels\/staging%2Fmain$/)
  assert.equal('body' in calls[5].init, false)
})

test('an incomplete version renders as incomplete, not as broken', async () => {
  const versions = await withFetch(
    {
      '/versions': () => json({ versions: [incompleteVersion, completeVersion] }),
      '/channels': () => json({ channels: [] }),
    },
    () => loadVersions('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  const markup = `${listMarkup(versions)}${versionsFacetMarkup(versions)}`
  assert.match(markup, /class="registry-facet-heading">This bucket<\/div>/)
  assert.match(markup, />v0</)
  assert.match(markup, />incomplete</)
  assert.match(markup, />v1</)
  assert.match(markup, />complete</)
  assert.match(markup, /0 builds · 0 artifacts/)
  assert.doesNotMatch(markup, /could not be loaded/)
})

const facetVersion = {
  name: 'v1', fingerprint: 'fp-complete', state: 'complete', templateType: 'HCL2',
  channels: [], assignments: [], created: '2026-08-01T14:02:00.000Z',
  builds: [{ artifacts: [] }], parents: [], children: [],
}

test('the Versions facet is one full-width card without a redundant summary', () => {
  const markup = renderToStaticMarkup(React.createElement(VersionsFacet, {
    versions: [facetVersion],
    onOpenVersion: () => {},
  }))
  assert.match(markup, /^<div [^>]*class="pf-v6-c-card\b/)
  assert.equal((markup.match(/class="pf-v6-c-card\b/g) ?? []).length, 1)
  assert.match(markup, /class="pf-v6-c-card__title-text">Versions<\/div>/)
  const table = markup.match(/<table[^>]*aria-label="Versions"[^>]*>/)?.[0] ?? ''
  assert.match(table, /pf-m-sticky-header/)
  assert.doesNotMatch(markup, /Version summary|>Complete<|>Incomplete<|>Artifacts</)
  assert.match(versionsScreenSource, /Select page \(\{visibleVersions\.length\} items\)/)
  assert.match(versionsScreenSource, /Select all \(\{versions\.length\} items\)/)
  assert.match(markup, /aria-label="Select all versions"/)
  assert.match(markup, /type="checkbox"/)
})

test('versions selection survives pagination and select-all-N covers the filtered set', () => {
  const versions = Array.from({ length: 4 }, (_unused, index) => ({
    ...facetVersion, name: `v${index + 1}`, fingerprint: `fp-${index + 1}`,
  }))
  let selected = updateVersionSelection(
    [], versionPage(versions, 1, 2).map((version) => version.fingerprint), true,
  )
  assert.deepEqual(selected, ['fp-1', 'fp-2'])
  assert.deepEqual(versionPage(versions, 2, 2).map((version) => version.fingerprint), ['fp-3', 'fp-4'])
  assert.deepEqual(selected, ['fp-1', 'fp-2'], 'paging must not discard page-one selection')
  selected = updateVersionSelection(
    selected, versions.map((version) => version.fingerprint), true,
  )
  assert.deepEqual(selected, ['fp-1', 'fp-2', 'fp-3', 'fp-4'])
})

const bulkModalProps = (action, versions, partition, over = {}) => ({
  action, versions, partition,
  message: '', when: 'now', scheduledAt: '', skipDescendants: false,
  disableRollback: false, submitting: false, results: null,
  onMessageChange: () => {}, onWhenChange: () => {}, onScheduledAtChange: () => {},
  onSkipDescendantsChange: () => {}, onDisableRollbackChange: () => {},
  onConfirm: async () => {}, onClose: () => {}, ...over,
})

test('bulk revoke eligibility excludes already-revoked versions before requests', async () => {
  const versions = [
    { ...facetVersion, name: 'v9', fingerprint: 'fp-9', state: 'revoked' },
    { ...facetVersion, name: 'v8', fingerprint: 'fp-8' },
    { ...facetVersion, name: 'v7', fingerprint: 'fp-7' },
    { ...facetVersion, name: 'v6', fingerprint: 'fp-6' },
  ]
  const partition = partitionBulkVersions(versions, 'revoke')
  assert.deepEqual(partition.included.map((version) => version.name), ['v8', 'v7', 'v6'])
  assert.deepEqual(partition.excluded.map(({ version, reason }) => [version.name, reason]), [
    ['v9', 'is already revoked'],
  ])

  const requests = []
  await runBulkVersionAction(partition.included, async (version) => requests.push(version.name))
  assert.deepEqual(requests, ['v8', 'v7', 'v6'])

  const modal = BulkVersionActionModalView(bulkModalProps('revoke', versions, partition))
  assert.equal(modal.props.expected, 'revoke')
  assert.equal(modal.props.variant, 'medium')
  const markup = renderTyped(modal)
  assert.match(markup, /Revoke 4 versions/)
  assert.match(markup, /3 of 4 will be revoked/)
  assert.match(markup, /v9 is already revoked/)
  for (const name of ['v8', 'v7', 'v6']) assert.match(markup, new RegExp(`>${name}<`))
  assert.equal((markup.match(/Revocation message/g) ?? []).length, 1)
  assert.match(markup, /Type <strong>revoke<\/strong> to confirm/)
})

test('bulk delete eligibility excludes channel-assigned versions with reasons', () => {
  const versions = [
    { ...facetVersion, name: 'v4', fingerprint: 'fp-4', channels: ['production'] },
    { ...facetVersion, name: 'v3', fingerprint: 'fp-3' },
  ]
  const partition = partitionBulkVersions(versions, 'delete')
  assert.deepEqual(partition.included.map((version) => version.name), ['v3'])
  assert.deepEqual(partition.excluded.map(({ version, reason }) => [version.name, reason]), [
    ['v4', 'is assigned to channel: production'],
  ])
  const modal = BulkVersionActionModalView(bulkModalProps('delete', versions, partition))
  assert.equal(modal.props.expected, 'delete')
  const markup = renderTyped(modal)
  assert.match(markup, /Delete 2 versions/)
  assert.match(markup, /1 of 2 will be deleted/)
  assert.match(markup, /v4 is assigned to channel: production/)
  assert.match(markup, /Type <strong>delete<\/strong> to confirm/)
})

test('bulk partial failures render every per-row result and the server refusal verbatim', async () => {
  const versions = [
    { ...facetVersion, name: 'v3', fingerprint: 'fp-3' },
    { ...facetVersion, name: 'v2', fingerprint: 'fp-2' },
    { ...facetVersion, name: 'v1', fingerprint: 'fp-1' },
  ]
  const order = []
  const refusal = 'v2 is protected by registry policy'
  const results = await runBulkVersionAction(versions, async (version) => {
    order.push(version.name)
    if (version.name === 'v2') throw new Error(refusal)
  })
  assert.deepEqual(order, ['v3', 'v2', 'v1'])
  assert.deepEqual(results.map(({ version, status }) => [version.name, status]), [
    ['v3', 'success'], ['v2', 'refused'], ['v1', 'success'],
  ])

  const partition = partitionBulkVersions(versions, 'delete')
  const markup = renderTyped(BulkVersionActionModalView(bulkModalProps(
    'delete', versions, partition, { results },
  )))
  assert.equal((markup.match(/>Success<\/span>/g) ?? []).length, 2)
  assert.equal((markup.match(/>Refused<\/span>/g) ?? []).length, 1)
  for (const name of ['v3', 'v2', 'v1']) assert.match(markup, new RegExp(name))
  assert.match(markup, new RegExp(refusal))
  assert.doesNotMatch(markup, /The action was refused/)
})

test('fingerprint fields use the compact read-only copy control everywhere they render', () => {
  const fingerprint = `dufflebag-sbom-demo-${'1'.repeat(80)}`
  const control = CopyableIdentifier({ value: fingerprint, label: 'Test fingerprint' })
  assert.equal(control.props.variant, 'inline-compact')
  assert.equal(control.props.isReadOnly, true)
  assert.equal(control.props.isCode, true)
  assert.equal(control.props.truncation, true)

  const markup = renderToStaticMarkup(React.createElement(VersionsFacet, {
    versions: [{ ...facetVersion, fingerprint }],
    onOpenVersion: () => {},
  }))
  assert.match(markup, /pf-v6-c-clipboard-copy pf-m-inline pf-m-truncate/)
  assert.match(markup, new RegExp(`<code[^>]*>[\\s\\S]{0,300}${fingerprint}[\\s\\S]{0,100}<\\/code>`))
  assert.equal((versionsScreenSource.match(/<CopyableIdentifier/g) ?? []).length, 3)
  assert.equal((versionScreenSource.match(/<CopyableIdentifier/g) ?? []).length, 2)
  assert.equal((buildScreenSource.match(/<CopyableIdentifier/g) ?? []).length, 1)
  assert.doesNotMatch(versionsScreenSource + versionScreenSource, /registry-fingerprint/)
})

test('enforced provisioners row distinguishes configured, empty, loading, and failure states', () => {
  const markup = (props) => renderToStaticMarkup(React.createElement(EnforcedProvisionersRow, props))

  const configured = markup({
    provisioners: ['required-plugins', 'eb-02'], loading: false, failure: null,
  })
  assert.match(configured, />Enforced provisioners</)
  assert.match(configured, />required-plugins, eb-02</)
  assert.doesNotMatch(configured, /None configured|Could not be loaded|Loading…/)

  const empty = markup({ provisioners: [], loading: false, failure: null })
  assert.match(empty, />None configured</)
  assert.doesNotMatch(empty, />—</)

  const loading = markup({ provisioners: [], loading: true, failure: null })
  assert.match(loading, />Loading…</)
  assert.doesNotMatch(loading, /None configured/)

  const failed = markup({ provisioners: [], loading: false, failure: 'reader request failed' })
  assert.match(failed, />Could not be loaded: reader request failed</)
  assert.doesNotMatch(failed, /None configured/)
})

test('enforced provisioners load from the bucket endpoint and prefer names over identifiers', async () => {
  const provisioners = await withFetch(
    {
      '/enforced_blocks/bucket/images': () => json({ enforced_block_detail: [
        { id: 'eb-01', name: 'required-plugins' },
        { id: 'eb-02' },
      ] }),
    },
    () => loadEnforcedProvisioners(
      'token', { organizationID: 'org', projectID: 'project' }, 'images',
    ),
  )
  assert.deepEqual(provisioners, ['required-plugins', 'eb-02'])
})

const facetRailMarkup = (unmountOnExit = false) =>
  renderToStaticMarkup(React.createElement(FacetRail, {
    active: 'packages',
    heading: 'This build',
    label: 'Build facets',
    facets: [
      { key: 'overview', label: 'Overview', content: 'Overview content' },
      {
        key: 'artifacts', label: 'Artifacts', count: { status: 'known', value: 0 },
        content: 'Artifact content',
      },
      {
        key: 'packages', label: 'Packages', count: { status: 'unknown' },
        content: 'Package content',
      },
    ],
    onSelect: () => {},
    unmountOnExit,
  }))

test('the facet rail keeps its natural mixed-case heading in the markup', () => {
  const markup = facetRailMarkup()
  assert.match(markup, /class="registry-facet-heading">This build<\/div>/)
})

test('the facet rail renders badges only for known counts', () => {
  const markup = facetRailMarkup()
  assert.match(markup, /pf-v6-c-tabs__item-text">Artifacts<\/span><span class="pf-v6-c-badge pf-m-read">0<\/span>/)
  assert.match(markup, /pf-v6-c-tabs__item-text">Overview<\/span><\/button>/)
  assert.match(markup, /pf-v6-c-tabs__item-text">Packages<\/span><\/button>/)
  assert.equal((markup.match(/pf-v6-c-badge/g) ?? []).length, 1)
})

test('the facet rail uses tab semantics and marks only its active tab selected', () => {
  const markup = facetRailMarkup()
  assert.match(markup, /role="tablist"/)
  assert.equal((markup.match(/role="tab"/g) ?? []).length, 3)
  assert.equal((markup.match(/aria-selected="true"/g) ?? []).length, 1)
  assert.equal((markup.match(/aria-selected="false"/g) ?? []).length, 2)
  assert.match(markup, /role="tab" aria-selected="true"><span class="pf-v6-c-tabs__item-text">Packages/)
  assert.equal((markup.match(/role="tabpanel"/g) ?? []).length, 3)
})

test('unmountOnExit keeps only the active facet panel mounted', () => {
  const markup = facetRailMarkup(true)
  assert.equal((markup.match(/role="tabpanel"/g) ?? []).length, 1)
  assert.match(markup, /Package content/)
  assert.doesNotMatch(markup, /Overview content|Artifact content/)
})

test('all three facet screens unmount inactive panels', () => {
  for (const [source, label] of [
    [versionsScreenSource, 'Bucket'],
    [versionScreenSource, 'Version'],
    [buildScreenSource, 'Build'],
  ]) {
    assert.match(
      source,
      new RegExp(`<FacetRail[\\s\\S]{0,180}label="${label} facets"[\\s\\S]{0,80}unmountOnExit`),
    )
  }
})

test('build and artifact details come through to the version page', async () => {
  const version = await withFetch(
    {
      '/versions/fp-complete': () => json({ version: completeVersion }),
      '/channels': () => json({ channels: [] }),
    },
    () => loadVersion('token', { organizationID: 'org', projectID: 'project' }, 'images', 'fp-complete'),
  )
  assert.deepEqual(version.builds, [
    {
      id: '01K1CJ4X8H0000000000000001',
      component: 'docker.ubuntu',
      platform: 'docker',
      state: 'done',
      packerRunUUID: 'run-1',
      sourceExternalIdentifier: '',
      labels: { ImageDigest: 'sha256:label-value' },
      artifacts: [
        {
          id: '01K1CJ4X8H0000000000000002',
          externalIdentifier: 'sha256:abc123',
          region: 'us-east-1',
        },
      ],
      packerVersion: '1.16.0',
      plugins: [{ name: 'docker', version: '1.1.4' }],
      runnerOS: 'linux',
      arch: 'amd64',
      options: {
        path: './image.pkr.hcl', variables: ['base_image', 'run_label'],
        variableFiles: ['./production.pkrvars.hcl'],
        only: ['docker.ubuntu'], except: [], debug: true, force: true,
      },
      updated: '2026-07-31T10:05:00.000Z',
      packageInventory: { status: 'not-loaded' },
    },
  ])
  const markup = `${detailMarkup(version)}${renderToStaticMarkup(React.createElement(BuildTable, {
    builds: version.builds, onOpenBuild: () => {},
  }))}`
  assert.match(markup, /class="registry-facet-heading">This version<\/div>/)
  assert.match(markup, /docker\.ubuntu/)
  assert.match(markup, />done</)
  assert.match(markup, /run-1/)
  assert.match(markup, /linux/)
  assert.match(markup, /amd64/)
  assert.match(markup, /docker 1\.1\.4/)
  assert.match(markup, /<time[^>]*dateTime="2026-07-31T10:00:00.000Z"/)
  assert.match(markup, /<time[^>]*dateTime="2026-07-31T10:05:00.000Z"/)
})

test('the build list renders metadata columns and expandable package detail', () => {
  const version = {
    ...completeVersion,
    name: 'v1',
    fingerprint: 'fp-complete',
    state: 'complete',
    templateType: 'HCL2',
    channels: [],
    assignments: [],
    parents: [],
    children: [],
    created: '2026-07-31T10:00:00.000Z',
    builds: [{
      id: 'build-list-id', component: 'docker.ubuntu', platform: 'docker', state: 'done',
      packerRunUUID: 'run-list', sourceExternalIdentifier: '', labels: {}, artifacts: [],
      packerVersion: '1.16.0', plugins: [{ name: 'docker', version: '1.1.4' }],
      runnerOS: 'linux', arch: 'amd64', updated: '2026-07-31T10:05:00.000Z',
      options: {
        path: './image.pkr.hcl', variables: [], variableFiles: [],
        only: [], except: [], debug: false, force: false,
      },
      packageInventory: {
        status: 'parsed',
        packages: [
          { name: 'openssl', version: '3', purl: '', sboms: [] },
          { name: 'zlib', version: '1', purl: '', sboms: [] },
          { name: 'curl', version: '8', purl: '', sboms: [] },
        ],
      },
    }],
  }
  const markup = renderToStaticMarkup(React.createElement(BuildTable, {
    builds: version.builds, onOpenBuild: () => {},
  }))
  assert.match(markup, />Packer runner OS</)
  for (const expected of [
    'Name', 'Status', 'Arch', 'Updated', 'docker.ubuntu',
    'done', 'linux', 'amd64', '1.16.0', 'docker 1.1.4', '0 artifacts',
    '3 packages', 'run-list',
  ]) assert.match(markup, new RegExp(expected))
})

test('build overview reconstructs masked options and the Artifacts facet names Platform', async () => {
  const detail = await withFetch(
    {
      '/versions/fp-complete': () => json({ version: completeVersion }),
      '/packages?pagination.page_size=100': () => json({
        packages: [{ name: 'openssl', version: '3.0.11' }],
        pagination: {},
      }),
      '/sboms': () => json({ sboms: [
        { id: 'sb-1', name: 'fp-complete', format: 'SPDX' },
      ] }),
    },
    () => loadBuildDetail(
      'token', { organizationID: 'org', projectID: 'project' }, 'images',
      'fp-complete', completeVersion.builds[0].id,
    ),
  )
  assert.equal(detail.build.packageInventory.packages.length, 1)
  assert.equal(
    packerBuildCommand(detail.build),
    'packer build \\\n  -var="base_image=***" \\\n  -var="run_label=***" \\\n  -var-file=./production.pkrvars.hcl \\\n  -only=docker.ubuntu \\\n  -debug \\\n  -force \\\n  ./image.pkr.hcl',
  )

  const markup = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images', detail, loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  const artifactsMarkup = renderToStaticMarkup(React.createElement(ArtifactsCard, { build: detail.build }))
  const packagesMarkup = renderToStaticMarkup(React.createElement(PackagesCard, { build: detail.build }))
  assert.match(markup, /class="registry-facet-heading">This build<\/div>/)
  assert.match(markup, /Build options/)
  assert.match(markup, /Variable values are masked/)
  assert.match(markup, /Packer runner environment/)
  assert.match(markup, /Build labels/)
  assert.match(markup, /base_image=\*\*\*/)
  assert.match(markup, /ImageDigest/)
  assert.match(artifactsMarkup, />Platform</)
  assert.match(artifactsMarkup, />External ID</)
  assert.match(artifactsMarkup, /sha256:abc123/)
  assert.match(markup, />Packages<\/span><span class="pf-v6-c-badge pf-m-read">1<\/span>/)
  assert.match(packagesMarkup, /openssl/)
  assert.match(packagesMarkup, /Reported by client-supplied SBOMs/)
  assert.doesNotMatch(artifactsMarkup, />Artifact</)

  // The stored SBOM offers its download on the overview (duf-cse): one row
  // per document in the Security card's row idiom, saved as the DOCUMENT
  // under "<name>.json" exactly as live HCP serves it (probed 2026-08-08).
  assert.deepEqual(detail.sboms, [{ id: 'sb-1', name: 'fp-complete', format: 'SPDX' }])
  assert.match(markup, />SBOM</)
  assert.match(markup, /aria-label="SBOM downloads"/)
  assert.match(markup, /pf-v6-c-data-list__item pf-m-clickable/)
  assert.match(markup, /aria-label="Download fp-complete\.json"/)
  assert.match(markup, />SPDX</)
  assert.doesNotMatch(markup, /zstd/)
  assert.doesNotMatch(markup, /aria-label="Download format|Select SBOM/)
  assert.match(packagesMarkup, /pf-v6-c-pagination/)
  assert.equal(sbomFileName(detail.sboms[0]), 'fp-complete.json')
  // The wire names every format the same way — no format-suffix invention.
  assert.equal(sbomFileName({ id: 'x', name: 'inv', format: 'CYCLONEDX' }), 'inv.json')

  // Several documents render one row each.
  const multi = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images',
    detail: { ...detail, sboms: [
      { id: 'sb-1', name: 'fp-complete', format: 'SPDX' },
      { id: 'sb-2', name: 'inventory', format: 'CYCLONEDX' },
    ] },
    loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  assert.match(multi, /aria-label="Download fp-complete\.json"/)
  assert.match(multi, /aria-label="Download inventory\.json"/)
  assert.match(multi, />CYCLONEDX</)

  // A build with no stored SBOMs offers no card at all: a control that
  // cannot succeed should not look available.
  const bare = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images', detail: { ...detail, sboms: [] }, loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  assert.doesNotMatch(bare, /aria-label="Download /)
  assert.doesNotMatch(bare, /pf-v6-c-card__title[^>]*>SBOM</)
})

test('an unparseable SBOM is not rendered as a zero package inventory', async () => {
  const load = (packageResponse) => withFetch(
    {
      '/versions/fp-complete': () => json({ version: completeVersion }),
      '/packages?pagination.page_size=100': packageResponse,
      '/sboms': () => json({ sboms: [] }),
    },
    () => loadBuildDetail(
      'token', { organizationID: 'org', projectID: 'project' }, 'images',
      'fp-complete', completeVersion.builds[0].id,
    ),
  )
  const unparseable = await load(() => json({
    message: 'package inventory is unparseable for SBOMs ["broken-report"]',
  }, 422))
  const unparseableMarkup = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images', detail: unparseable, loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  assert.match(unparseableMarkup, /SBOM unparseable/)
  assert.doesNotMatch(unparseableMarkup, /0 packages/)

  const empty = await load(() => json({ packages: [], pagination: {} }))
  const emptyMarkup = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images', detail: empty, loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  assert.match(emptyMarkup, /0 packages/)
  assert.doesNotMatch(emptyMarkup, /SBOM unparseable/)
})

test('a build load failure renders as a failure rather than an empty overview', () => {
  const markup = renderToStaticMarkup(React.createElement(BuildView, {
    bucket: 'images', detail: null, loading: false, failure: 'build not found',
    onBackToRegistry: () => {}, onBackToBucket: () => {}, onBackToVersion: () => {},
  }))
  assert.match(markup, /Build could not be loaded/)
  assert.match(markup, /build not found/)
  assert.doesNotMatch(markup, /Build options/)
})

test('package count follows every page rather than treating the first page as the total', async () => {
  const detail = await withFetch(
    {
      '/packages?pagination.page_size=100&pagination.next_page_token=next': () => json({
        packages: [{ name: 'zlib' }], pagination: {},
      }),
      '/packages?pagination.page_size=100': () => json({
        packages: [{ name: 'openssl' }], pagination: { next_page_token: 'next' },
      }),
      '/versions/fp-complete': () => json({ version: completeVersion }),
      '/sboms': () => json({ sboms: [] }),
    },
    () => loadBuildDetail(
      'token', { organizationID: 'org', projectID: 'project' }, 'images',
      'fp-complete', completeVersion.builds[0].id,
    ),
  )
  assert.equal(detail.build.packageInventory.status, 'parsed')
  assert.deepEqual(detail.build.packageInventory.packages.map(({ name }) => name), ['openssl', 'zlib'])
})

test('package inventory refetches while a build is in progress', () => {
  assert.match(
    versionDataSource,
    /useVersionData<VersionDetail \| null>\([\s\S]*?some\(buildIsInProgress\)/,
  )
  assert.match(
    versionDataSource,
    /useVersionData<BuildDetail \| null>\([\s\S]*?buildIsInProgress\(detail\.build\)/,
  )
  assert.match(
    versionDataSource,
    /reloadWhile\?\.\(loaded\)\) retry = setTimeout\(refresh, 500\)/,
  )
  assert.match(versionDataSource, /if \(retry\) clearTimeout\(retry\)/)
})

test('an incomplete version page states the absence of builds plainly', async () => {
  const version = await withFetch(
    {
      '/versions/fp-incomplete': () => json({ version: incompleteVersion }),
      '/channels': () => json({ channels: [] }),
    },
    () => loadVersion('token', { organizationID: 'org', projectID: 'project' }, 'images', 'fp-incomplete'),
  )
  const markup = detailMarkup(version)
  assert.match(markup, />v0</)
  assert.match(markup, />incomplete</)
  assert.equal(version.builds.length, 0)
  assert.match(versionScreenSource, /No builds have been reported for this version/)
  assert.doesNotMatch(markup, /could not be loaded/)
})

test('channel assignment context names the channels pointing at each version', async () => {
  const versions = await withFetch(
    {
      '/versions': () => json({ versions: [incompleteVersion, completeVersion] }),
      // renderChannel (internal/compat/hcp2023/handler.go) nests the
      // assignment as a full version object — there is no flat
      // version_fingerprint on the wire.
      '/channels': () => json({ channels: [
        { name: 'production', version: { name: 'v1', fingerprint: 'fp-complete' } },
        { name: 'staging', version: { name: 'v1', fingerprint: 'fp-complete' } },
        // An unassigned channel points at nothing, so it appears nowhere here.
        { name: 'dev' },
        // The managed latest is unassigned until a completion; it must not
        // attach itself to any version here.
        { name: 'latest', managed: true, restricted: true },
      ] }),
    },
    () => loadVersions('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  assert.deepEqual(
    versions.map(({ fingerprint, channels }) => ({ fingerprint, channels })),
    [
      { fingerprint: 'fp-incomplete', channels: [] },
      { fingerprint: 'fp-complete', channels: ['production', 'staging'] },
    ],
  )
  const markup = versionsFacetMarkup(versions)
  assert.match(markup, />production</)
  assert.match(markup, />staging</)
})

test('a version row expands to fingerprint, build summary, parent freshness, and children', () => {
  const version = {
    name: 'v14', fingerprint: 'child-fingerprint', state: 'complete', templateType: 'HCL2',
    channels: ['production'], assignments: [], created: '2026-08-01T14:02:00.000Z',
    builds: [{ artifacts: [{ id: 'artifact-1' }, { id: 'artifact-2' }] }],
    parents: [{
      bucket: 'base-images', versionName: 'v22', fingerprint: 'parent-fingerprint',
      channel: 'latest', freshness: { status: 'behind', currentVersion: 'v24' },
    }],
    children: [{
      bucket: 'derived-images', versionName: 'v3', fingerprint: 'descendant-fingerprint',
    }],
  }
  const markup = versionsFacetMarkup([version])
  for (const expected of [
    'child-fingerprint', '1 build · 2 artifacts', 'base-images v22',
    'bucket now at v24', 'derived-images v3', 'Open v14',
  ]) assert.match(markup, new RegExp(expected))
})

test('collapsed bucket detail uses channel metadata and does not fetch assignment history', async () => {
  const bucket = await withFetch(
    {
      '/buckets/images': () => json({ bucket: {
        name: 'images', description: 'Golden images', labels: { team: 'platform' },
        latest_version: { name: 'v1', fingerprint: 'fp-complete' },
      } }),
      '/versions': () => json({ versions: [completeVersion] }),
      '/channels': () => json({ channels: [
        {
          name: 'production', updated_at: '2026-08-01T09:10:00.000Z',
          author_id: 'principal-real-author',
          version: { name: 'v1', fingerprint: 'fp-complete' },
        },
      ] }),
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-complete': () =>
        json({ relations: [] }),
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-complete': () =>
        json({ relations: [] }),
    },
    () => loadBucketPage('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  assert.equal(bucket.description, 'Golden images')
  assert.deepEqual(bucket.labels, { team: 'platform' })
  assert.deepEqual(bucket.templateTypes, ['HCL2'])
  assert.deepEqual(bucket.latestVersion, { name: 'v1', fingerprint: 'fp-complete' })
  assert.deepEqual(bucket.versions[0].assignments, [{
    channel: 'production',
    assignedAt: '2026-08-01T09:10:00.000Z',
    author: 'principal-real-author',
  }])
  assert.equal(bucket.channels[0].author, 'principal-real-author')
})

test('bucket status and rail counts come from the loaded newest version and facets', async () => {
  const bucket = await withFetch(
    {
      '/buckets/images': () => json({ bucket: {
        name: 'images', description: 'Golden images', platforms: ['docker'],
        latest_version: { name: 'v1', fingerprint: 'fp-complete' },
      } }),
      '/versions': () => json({ versions: [incompleteVersion, completeVersion] }),
      '/channels': () => json({ channels: [{
        name: 'latest', managed: true,
        version: { name: 'v1', fingerprint: 'fp-complete' },
      }] }),
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-incomplete': () =>
        json({ relations: [] }),
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-incomplete': () =>
        json({ relations: [] }),
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-complete': () =>
        json({ relations: [] }),
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-complete': () =>
        json({ relations: [] }),
    },
    () => loadBucketPage('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  assert.deepEqual(bucket.latestVersion, { name: 'v1', fingerprint: 'fp-complete' })
  assert.deepEqual(bucket.newestVersion, {
    name: 'v0', fingerprint: 'fp-incomplete', state: 'incomplete',
    created: '2026-07-31T11:00:00.000Z',
  })
  const markup = renderToStaticMarkup(React.createElement(VersionsView, {
    bucket: 'images', bucketData: bucket, loading: false, failure: null,
    onBack: () => {}, onOpenVersion: () => {},
  }))
  assert.match(markup, />Versions<\/span><span class="pf-v6-c-badge pf-m-read">2<\/span>/)
  assert.match(markup, />Channels<\/span><span class="pf-v6-c-badge pf-m-read">1<\/span>/)
  assert.match(markup, /aria-selected="true"><span[^>]*>Overview/)
  assert.match(markup, /Bucket details[\s\S]*Newest version[\s\S]*v0[\s\S]*Status[\s\S]*incomplete/)
})

test('assignment history marks only the current row active and preserves unknown authorship', async () => {
  const history = await withFetch(
    {
      '/channels/production/history': () => json({ history: [
        {
          assigned_at: '2026-08-02T09:10:00.000Z',
          author_id: 'principal-current',
          version: {
            name: 'v2', fingerprint: 'fp-current',
            parents: { status: 'UP_TO_DATE' },
          },
        },
        {
          assigned_at: '2026-08-01T09:10:00.000Z',
          author_id: '',
          version: {
            name: 'v2', fingerprint: 'fp-current',
            parents: { status: 'UP_TO_DATE' },
          },
        },
        {
          assigned_at: '2026-07-31T09:10:00.000Z',
          author_id: 'principal-old',
          version: {
            name: 'v1', fingerprint: 'fp-old',
            parents: { status: 'OUT_OF_DATE' },
          },
        },
      ] }),
    },
    () => loadChannelHistory(
      'token', { organizationID: 'org', projectID: 'project' },
      'images', 'production', 'fp-current',
    ),
  )
  assert.deepEqual(history.map(({ status, author, parentStatus }) => ({
    status, author, parentStatus,
  })), [
    { status: 'active', author: 'principal-current', parentStatus: 'current' },
    { status: 'historical', author: null, parentStatus: 'current' },
    { status: 'historical', author: 'principal-old', parentStatus: 'out-of-date' },
  ])

  const markup = renderToStaticMarkup(React.createElement(AssignmentHistoryTable, {
    channel: 'production', history,
  }))
  assert.equal((markup.match(/>Active</g) ?? []).length, 1)
  assert.equal((markup.match(/>Historical</g) ?? []).length, 2)
  assert.match(markup, />Unknown</)
  assert.doesNotMatch(markup, /<td[^>]*data-label="Assigned by"[^>]*><\/td>/)
})

test('managed and unmanaged channels name their different managers', () => {
  const channel = (overrides) => ({
    name: 'production', versionName: 'v2', fingerprint: 'fp-current',
    managed: false, restricted: false, assignedAt: '2026-08-02T09:10:00.000Z',
    author: 'principal-current', ...overrides,
  })
  const markup = renderToStaticMarkup(React.createElement(BucketChannelsFacet, {
    bucket: 'images',
    channels: [
      channel({ name: 'latest', managed: true, restricted: true, author: 'Dufflebag' }),
      channel({ author: null }),
    ],
    latestVersion: { name: 'v2', fingerprint: 'fp-current' },
    onOpenVersion: () => {},
  }))
  for (const heading of ['Channel', 'Assigned version', 'Assigned by', 'Assigned time']) {
    assert.match(markup, new RegExp(`>${heading}<`))
  }
  assert.match(markup, /dufflebag, on version completion/)
  assert.match(markup, /hcp_packer_channel_assignment/)
  assert.match(markup, />Unknown</)
})

test('channel gap names the newest comparison and says current plainly', () => {
  const newest = { name: 'v5', fingerprint: 'fp-5' }
  assert.equal(
    channelVersionGap({ versionName: 'v5', fingerprint: 'fp-5' }, newest),
    'This channel points to the newest version in the bucket (v5).',
  )
  assert.equal(
    channelVersionGap({ versionName: 'v3', fingerprint: 'fp-3' }, newest),
    '2 complete versions behind the newest version (v5).',
  )
})

test('version detail renders persisted parent links and Terraform from real identifiers', async () => {
  const detail = await withFetch(
    {
      '/versions/fp-complete': () => json({ version: completeVersion }),
      // The specific bucket routes must precede the generic '/channels' one:
      // withFetch matches by suffix in insertion order.
      '/buckets/base-images/channels': () => json({ channels: [
        { name: 'stable', version: { name: 'v7', fingerprint: 'parent-fingerprint' } },
        { name: 'staging', version: { name: 'v7', fingerprint: 'parent-fingerprint' } },
        { name: 'retired', version: { name: 'v2', fingerprint: 'unrelated' } },
      ] }),
      '/buckets/derived-images/channels': () => json({ channels: [
        { name: 'canary', version: { name: 'v3', fingerprint: 'child-fingerprint' } },
      ] }),
      '/channels': () => json({ channels: [
        {
          name: 'latest', managed: true,
          version: { name: 'v1', fingerprint: 'fp-complete' },
        },
        {
          name: 'production', managed: false,
          version: { name: 'v0', fingerprint: 'fp-previous' },
        },
      ] }),
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-complete': () => json({
        relations: [{
          status: 'OUT_OF_DATE',
          parent: {
            bucket_name: 'base-images', version_name: 'v7',
            version_fingerprint: 'parent-fingerprint', channel_name: 'production',
            channel_version: { name: 'v8', fingerprint: 'new-parent' },
          },
          child: { bucket_name: 'images', version_name: 'v1', version_fingerprint: 'fp-complete' },
        }],
      }),
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-complete': () => json({
        relations: [{
          parent: {
            bucket_name: 'images', version_name: 'v1',
            version_fingerprint: 'fp-complete',
          },
          child: {
            bucket_name: 'derived-images', version_name: 'v3',
            version_fingerprint: 'child-fingerprint',
          },
        }],
      }),
      '/packages?pagination.page_size=100': () => json({ packages: [], pagination: {} }),
    },
    () => loadVersionDetail(
      'token', { organizationID: 'org', projectID: 'project' }, 'images', 'fp-complete',
    ),
  )

  const markup = renderToStaticMarkup(React.createElement(VersionView, {
    bucket: 'images', detail, loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {},
  }))
  // The lineage card is the design's two-column ancestry shape (duf-dus4):
  // "bucket vN" links with the related version's current channels beneath —
  // no fingerprints, which live on the linked version's own screen.
  assert.match(markup, /Lineage/)
  assert.match(markup, /Parents/)
  assert.match(markup, /Children/)
  assert.match(markup, /base-images v7/)
  assert.match(markup, /stable, staging/)
  assert.match(markup, /derived-images v3/)
  assert.match(markup, /canary/)
  assert.doesNotMatch(markup, /retired/)
  assert.doesNotMatch(markup, /parent-fingerprint|child-fingerprint/)
  assert.doesNotMatch(markup, /resolved through/)
  assert.doesNotMatch(markup, /Descendants|Revoking/)
  assert.deepEqual(detail.version.parents[0].freshness, {
    status: 'behind', currentVersion: 'v8',
  })
  assert.deepEqual(detail.version.parents[0].channels, ['stable', 'staging'])
  assert.deepEqual(detail.version.children[0].channels, ['canary'])
  assert.equal(parentFreshnessText({ status: 'newest' }), 'newest')
  assert.equal(
    parentFreshnessText({ status: 'behind', currentVersion: 'v24' }),
    'bucket now at v24',
  )

  // An empty side is stated, never omitted: both columns read "None." when a
  // version has no recorded relations.
  const emptyLineage = renderToStaticMarkup(React.createElement(VersionView, {
    bucket: 'images',
    detail: { ...detail, version: { ...detail.version, parents: [], children: [] } },
    loading: false, failure: null,
    onBackToRegistry: () => {}, onBackToBucket: () => {},
  }))
  assert.equal(emptyLineage.split('None.').length - 1, 2)

  // The copy control must not re-render the snippet: each Terraform block
  // appears exactly once in the card (duf-yxa — ClipboardCopy rendered its
  // children as a second, unformatted copy of the code).
  assert.equal(markup.split('hcp_packer_version').length - 1, 1)
  assert.equal(markup.split('hcp_packer_artifact').length - 1, 1)

  const consume = terraformConsumeSnippet('images', detail.version)
  assert.match(consume, /data "hcp_packer_version" "images"/)
  assert.match(consume, /channel_name = "latest"/)
  assert.match(consume, /data "hcp_packer_artifact" "images"/)
  assert.match(consume, /version_fingerprint = "fp-complete"/)
  assert.match(consume, /platform            = "docker"/)
  assert.match(consume, /region              = "us-east-1"/)

  const promotion = terraformPromotionSnippet('images', 'production', 'fp-complete')
  assert.match(promotion, /resource "hcp_packer_channel_assignment" "production"/)
  assert.match(promotion, /bucket_name         = "images"/)
  assert.match(promotion, /channel_name        = "production"/)
  assert.match(promotion, /version_fingerprint = "fp-complete"/)
  assert.doesNotMatch(promotion, /latest/)
})

test('a versions failure is visible instead of empty successful data', () => {
  const markup = renderToStaticMarkup(React.createElement(VersionsView, {
    bucket: 'images',
    versions: [],
    loading: false,
    failure: '500 from /packer/buckets/images/versions',
    onBack: () => {},
    onOpenVersion: () => {},
  }))
  assert.match(markup, /Versions could not be loaded/)
  assert.match(markup, /500 from \/packer\/buckets\/images\/versions/)
  assert.doesNotMatch(markup, /No versions in this bucket/)

  const detail = renderToStaticMarkup(React.createElement(VersionView, {
    bucket: 'images',
    version: null,
    loading: false,
    failure: 'version not found',
    onBackToRegistry: () => {},
    onBackToBucket: () => {},
  }))
  assert.match(detail, /Version could not be loaded/)
  assert.match(detail, /version not found/)
})

test('a nested channels failure rejects the whole load', async () => {
  await withFetch(
    {
      '/versions': () => json({ versions: [completeVersion] }),
      '/channels': () => json({}, 500),
    },
    () =>
      assert.rejects(
        loadVersions('token', { organizationID: 'org', projectID: 'project' }, 'images'),
        /500 from .*\/channels/,
      ),
  )
})

test('versions paginate the 30-row recent window and expose the five older rows on page 2', () => {
  const versions = Array.from({ length: 35 }, (_, index) => ({
    name: `v${35 - index}`,
    fingerprint: `fp-${35 - index}`,
    state: 'complete',
    templateType: 'HCL2',
    channels: [],
    assignments: [],
    builds: [],
    parents: [],
    children: [],
    created: '2026-07-31T10:00:00.000Z',
  }))
  const markup = versionsFacetMarkup(versions)
  assert.match(markup, />v35</)
  assert.match(markup, />v6</)
  assert.doesNotMatch(markup, />v5</)
  assert.equal((markup.match(/pf-v6-c-pagination/g) ?? []).length >= 2, true)
  assert.doesNotMatch(markup, /older versions|show all/i)

  const secondPage = versionPage(versions, 2, 30)
  assert.equal(secondPage.length, 5)
  assert.deepEqual(secondPage.map((version) => version.name), ['v5', 'v4', 'v3', 'v2', 'v1'])
})

test('empty and gap states are distinct, and list rows remain read-only', async () => {
  const emptyMarkup = versionsFacetMarkup([])
  assert.match(emptyMarkup, /<h2[^>]*>No versions in this bucket<\/h2>/)
  assert.match(emptyMarkup, /pf-v6-c-empty-state__body">Publish with packer build to create one\./)

  const gap = platformTenancyGap({
    platform: true, organizationCount: 1,
    selectedOrganization: null, projectCount: 0, selectedProject: null,
  })
  const gapMarkup = listMarkup([], { gap })
  assert.match(gapMarkup, /Choose an organisation/)
  assert.doesNotMatch(gapMarkup, /No versions in this bucket/)

  // Channel actions belong to the Channels facet and the loaded detail's
  // Operations card; a compact version projection does not invent them.
  const version = await withFetch(
    {
      '/versions/fp-complete': () => json({ version: completeVersion }),
      // renderChannel (internal/compat/hcp2023/handler.go) nests the
      // assignment as a full version object — there is no flat
      // version_fingerprint on the wire, and a fixture carrying one passes
      // against a shape the producer can never emit.
      '/channels': () => json({ channels: [
        { name: 'production', version: { name: 'v1', fingerprint: 'fp-complete' } },
      ] }),
    },
    () => loadVersion('token', { organizationID: 'org', projectID: 'project' }, 'images', 'fp-complete'),
  )
  // The association actually projects and renders — the channel pill names
  // production on both screens, so the safety action does not displace the
  // assignment context.
  assert.deepEqual(version.channels, ['production'])
  const list = `${listMarkup([version])}${versionsFacetMarkup([version])}`
  const detail = detailMarkup(version, { callerRole: 'publisher' })
  for (const markup of [list, detail]) assert.match(markup, />production</)
  for (const unsupported of ['Promote', 'Assign', 'Create version', 'Schedule']) {
    assert.doesNotMatch(list, new RegExp(unsupported))
  }
  assert.equal((list.match(/>Delete bucket</g) ?? []).length, 1)
  assert.doesNotMatch(list, /Delete version|Delete channel|Delete v1/)
  assert.doesNotMatch(list, />Revoke</)
  assert.match(detail, />Revoke</)
})

test('the bucket ancestry card aggregates every version and names the local version', async () => {
  const newestComplete = { ...completeVersion, fingerprint: 'fp-new', name: 'v2' }
  const olderComplete = { ...completeVersion, fingerprint: 'fp-old', name: 'v1' }
  const bucket = await withFetch(
    {
      '/buckets/images': () => json({ bucket: {
        name: 'images', latest_version: { name: 'v2', fingerprint: 'fp-new' },
      } }),
      '/versions': () => json({ versions: [newestComplete, olderComplete] }),
      '/channels': () => json({ channels: [] }),
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-new': () =>
        json({ relations: [] }),
      // The newest version has a child of its own — its note must say (latest).
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-new': () =>
        json({ relations: [{
          parent: { bucket_name: 'images', version_name: 'v2', version_fingerprint: 'fp-new' },
          child: { bucket_name: 'derived', version_name: 'v9', version_fingerprint: 'fp-nine' },
        }] }),
      // The OLDER version carries the parent link the newest-scoped bucket
      // listing hides (duf-okej.11) — and a duplicate edge from a second build
      // collapses to one entry.
      '/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=fp-old': () =>
        json({ relations: [{
          status: 'UP_TO_DATE',
          parent: {
            bucket_name: 'base-images', version_name: 'v7',
            version_fingerprint: 'fp-base', channel_name: 'production',
            channel_version: { name: 'v7', fingerprint: 'fp-base' },
          },
          child: { bucket_name: 'images', version_name: 'v1', version_fingerprint: 'fp-old' },
        }] }),
      '/ancestry?type=ANCESTRY_TYPE_CHILDREN&version_fingerprint=fp-old': () =>
        json({ relations: [
          {
            parent: { bucket_name: 'images', version_name: 'v1', version_fingerprint: 'fp-old' },
            child: { bucket_name: 'derived', version_name: 'v3', version_fingerprint: 'fp-child' },
          },
          {
            parent: { bucket_name: 'images', version_name: 'v1', version_fingerprint: 'fp-old' },
            child: { bucket_name: 'derived', version_name: 'v3', version_fingerprint: 'fp-child' },
          },
        ] }),
    },
    () => loadBucketPage('token', { organizationID: 'org', projectID: 'project' }, 'images'),
  )
  assert.deepEqual(
    bucket.parents.map(({ bucket: name, versionName, localVersionName }) =>
      ({ name, versionName, localVersionName })),
    [{ name: 'base-images', versionName: 'v7', localVersionName: 'v1' }],
  )
  assert.deepEqual(
    bucket.children.map(({ bucket: name, versionName, localVersionName }) =>
      ({ name, versionName, localVersionName })),
    [
      { name: 'derived', versionName: 'v9', localVersionName: 'v2' },
      { name: 'derived', versionName: 'v3', localVersionName: 'v1' },
    ],
  )

  const markup = renderToStaticMarkup(React.createElement(VersionsView, {
    bucket: 'images', bucketData: bucket, loading: false, failure: null,
    onBack: () => {}, onOpenVersion: () => {},
  }))
  assert.match(markup, /base-images v7/)
  assert.match(markup, /parent of v1/)
  assert.match(markup, /built from v2 \(latest\)/)
  assert.match(markup, /built from v1/)
  assert.match(markup, /aria-label="Parents ancestry scope"/)
  assert.match(markup, /aria-label="Children ancestry scope"/)
  assert.doesNotMatch(markup, /parent of v1 \(latest\)/)
})
