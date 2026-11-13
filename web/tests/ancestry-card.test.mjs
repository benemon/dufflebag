import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { after, before, test } from 'node:test'

import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'

let vite
let AncestryCard, freshnessText
let loadVersionDetail
let loadBucketPage

// Every response below is the compatibility handler's own output, written by
// TestAncestryCardWebFixture (internal/compat/hcp2023/ancestry_fixture_test.go,
// DUFFLEBAG_UPDATE_FIXTURES=1) and keyed by the path the console requests.
const fixture = JSON.parse(readFileSync(new URL('./fixtures/ancestry-card.json', import.meta.url), 'utf8'))
const tenant = { organizationID: 'org', projectID: 'project' }

before(async () => {
  vite = await createServer({
    root: process.cwd(),
    logLevel: 'silent',
    server: { middlewareMode: true },
    appType: 'custom',
    ssr: { noExternal: [/@patternfly\//] },
  })
  ;({ AncestryCard, freshnessText } = await vite.ssrLoadModule('/src/components/AncestryCard.tsx'))
  ;({ loadVersionDetail, loadBucketPage } = await vite.ssrLoadModule('/src/data/versions.ts'))
})

after(async () => {
  await vite.close()
})

const fromFixture = async (run, overrides = {}) => {
  const requests = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = async (input) => {
    const path = String(input)
    requests.push(path)
    const key = Object.keys(overrides).find((suffix) => path.endsWith(suffix)) ??
      Object.keys(fixture).find((suffix) => path.endsWith(suffix))
    if (!key) throw new Error(`unexpected request ${path}`)
    const { status, body } = overrides[key] ?? fixture[key]
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
  }
  try {
    return { result: await run(), requests }
  } finally {
    globalThis.fetch = originalFetch
  }
}

const versionPage = () => fromFixture(() => loadVersionDetail('token', tenant, 'base', 'base-4'))
const bucketPage = () => fromFixture(() => loadBucketPage('token', tenant, 'agent'))

const render = (props) => renderToStaticMarkup(React.createElement(AncestryCard, {
  onOpenBucket: () => {}, onOpenVersion: () => {}, ...props,
}))

/** The markup of one bucket's group, split at its collapsed older-versions region. */
const group = (markup, bucket) => {
  const start = markup.indexOf(`role="group" aria-label="${bucket}"`)
  assert.notEqual(start, -1, `no ${bucket} group`)
  const end = markup.indexOf('role="group"', start + 1)
  const body = markup.slice(start, end === -1 ? undefined : end)
  const caret = body.indexOf('pf-v6-c-expandable-section')
  return caret === -1 ? { shown: body, older: '' } : { shown: body.slice(0, caret), older: body.slice(caret) }
}

const versionCard = (detail) => render({
  bucket: 'base',
  scope: { versionName: detail.version.name },
  parents: detail.version.parents,
  children: detail.version.children,
})

test('version page children are this version\'s own and each carries its relation status', async () => {
  const { result: detail } = await versionPage()
  assert.deepEqual(
    detail.version.children.map((child) => [child.bucket, child.versionName, child.buildFreshness.status]),
    [
      ['agent', 'v5', 'UP_TO_DATE'],
      ['agent', 'v4', 'OUT_OF_DATE'],
      ['retired', 'v1', 'UP_TO_DATE'],
      ['runner', 'v3', 'UNDETERMINED'],
      ['runner', 'v2', 'UNDETERMINED'],
    ],
  )
})

test('freshness is a pill only where it needs attention, on child rows too, with both undetermined readings', async () => {
  const markup = versionCard((await versionPage()).result)
  assert.doesNotMatch(group(markup, 'agent').shown, /behind:|empty<|no channel</, 'a current relation carries no pill')
  assert.match(group(markup, 'agent').older, /v4<[\s\S]*>behind: latest at v5</)
  assert.match(group(markup, 'runner').shown, /v3<[\s\S]*>staging empty</)
  assert.match(group(markup, 'runner').shown, /v2<[\s\S]*>no channel</)
  assert.doesNotMatch(markup, /Built from|still current/, 'the sentence lives only in the tooltip')
  assert.doesNotMatch(markup, /title=/)
})

test('each pill\'s tooltip carries the full sentence', () => {
  assert.equal(freshnessText({ status: 'OUT_OF_DATE', channel: 'latest', builtVersion: 'v4', currentVersion: 'v5' }),
    'Built from latest · latest now at v5')
  assert.equal(freshnessText({ status: 'UNDETERMINED', channel: 'staging', builtVersion: 'v4' }),
    'Built from staging · staging has no version')
  assert.equal(freshnessText({ status: 'UNDETERMINED', channel: '', builtVersion: 'v4' }),
    'Built from v4 · no channel to compare against')
})

test('version page card states its subject, counts and child group counts', async () => {
  const markup = versionCard((await versionPage()).result)
  assert.match(markup, />Ancestry</)
  assert.match(markup, />base v4</)
  assert.match(markup, />Parents · 0<[\s\S]*>No parents</)
  assert.match(markup, />Children · 5</)
  assert.match(markup, />2 built from this</)
  assert.match(markup, />1 built from this</)
  assert.doesNotMatch(markup, /built from this bucket/)
  assert.doesNotMatch(markup, /Lineage|ancestry scope/)
})

test('Newest never collapses, and a group with nothing to expand has no caret', async () => {
  const markup = versionCard((await versionPage()).result)
  const runner = group(markup, 'runner')
  // runner v3 is the highest complete version while latest stays on v2.
  assert.match(runner.shown, /v3<\/span><\/button><span class="pf-v6-c-label[^"]*ancestry-newest/)
  assert.match(runner.shown, />latest</)
  assert.equal(runner.older, '')
  assert.equal(group(markup, 'retired').older, '')
})

test('a version with no channel folds under a caret that says what it holds', async () => {
  const markup = versionCard((await versionPage()).result)
  const agent = group(markup, 'agent')
  assert.match(agent.shown, />latest<[\s\S]*>production<[\s\S]*>Newest</)
  assert.match(agent.older, /aria-expanded="false"[\s\S]*1 older version</)
  assert.match(agent.older, /hidden=""[\s\S]*>v4</)
  assert.match(agent.older, /Revocation scheduled/)
})

test('an unreadable related bucket keeps its entries without inventing state', async () => {
  const { result: detail, requests } = await versionPage()
  const retired = detail.version.children.find((child) => child.bucket === 'retired')
  assert.equal(retired.channels, undefined)
  assert.equal(retired.state, undefined)
  assert.equal(retired.newest, undefined)
  const markup = group(versionCard(detail), 'retired')
  assert.match(markup.shown, />v1</)
  assert.doesNotMatch(markup.shown, /pf-v6-c-label/)
  // One ListChannels and one ListVersions per bucket, the page's own reused.
  for (const bucket of ['base', 'agent', 'runner', 'retired']) {
    for (const list of ['channels', 'versions']) {
      assert.equal(requests.filter((path) => path.endsWith(`/buckets/${bucket}/${list}`)).length, 1,
        `${bucket} ${list}`)
    }
  }
})

test('a dead session still reaches sign-out rather than degrading', async () => {
  await assert.rejects(
    fromFixture(() => loadVersionDetail('token', tenant, 'base', 'base-4'), {
      '/buckets/retired/channels': { status: 401, body: { code: 16, message: 'unauthenticated' } },
    }),
    (err) => err.status === 401,
  )
})

test('bucket page parents read one row per parent version, without per-local-version text', async () => {
  const { result: page, requests } = await bucketPage()
  const markup = render({
    bucket: 'agent',
    scope: { versions: page.versions, newest: page.latestVersion?.fingerprint },
    parents: page.parents,
    children: page.children,
  })
  assert.match(markup, />agent · all versions</)
  assert.match(markup, />Parents · 3</)
  assert.match(markup, />Children · 0<[\s\S]*>No children</)
  const base = group(markup, 'base')
  assert.match(base.shown, />v4<[\s\S]*>production<[\s\S]*>behind: latest at v5</)
  // Revoked and channel-less, base v2 stays out because agent v3 holds stable.
  assert.match(base.shown, />v2<[\s\S]*>Revoked</)
  // base v1 serves only agent v2 and v1, neither holding a channel: it folds.
  assert.match(base.older, /1 parent of older versions</)
  assert.equal(base.older.split('>v1<').length - 1, 1)
  assert.match(base.older, />v1<[\s\S]*>behind: production at v4</)
  assert.doesNotMatch(markup, /built from this|>For |Built from/)
  for (const list of ['channels', 'versions']) {
    assert.equal(requests.filter((path) => path.endsWith(`/buckets/base/${list}`)).length, 1)
    assert.equal(requests.filter((path) => path.endsWith(`/buckets/agent/${list}`)).length, 1)
  }
})

test('bucket page children carry revocation and their inherited ancestor', async () => {
  const { result: page } = await fromFixture(() => loadBucketPage('token', tenant, 'base'))
  const markup = render({
    bucket: 'base',
    scope: { versions: page.versions, newest: page.latestVersion?.fingerprint },
    parents: page.parents,
    children: page.children,
  })
  assert.match(markup, />Children · 9</)
  assert.match(markup, />5 built from this bucket</)
  assert.match(markup, />3 built from this bucket</)
  const runner = group(markup, 'runner')
  assert.match(runner.shown, />v2</)
  assert.match(runner.older, />v1<[\s\S]*>Revoked<[\s\S]*Inherited from <button[\s\S]*?>base v2</)
  assert.doesNotMatch(markup, />From |Built from/)
  const agent = group(markup, 'agent')
  assert.match(agent.older, /3 older versions</)
  assert.match(agent.older, /Revocation scheduled <[\s\S]*?<time[^>]*dateTime="2026-11-18T12:00:00.000Z"/)
})

test('loading shows both headings at once and bars in place of groups', () => {
  const markup = render({ bucket: 'base', scope: { versionName: 'v4' }, parents: [], children: [], loading: true })
  assert.match(markup, /aria-busy="true"/)
  assert.match(markup, />Parents<[\s\S]*pf-v6-c-skeleton[\s\S]*>Children<[\s\S]*pf-v6-c-skeleton/)
  assert.doesNotMatch(markup, /Parents · |No parents|No children/)
})

test('names truncate through PatternFly, never a native title', async () => {
  const markup = versionCard((await versionPage()).result)
  assert.match(markup, /pf-v6-c-truncate/)
  assert.doesNotMatch(markup, /\stitle="/)
})
