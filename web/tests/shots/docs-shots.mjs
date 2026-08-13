/**
 * Repeatable documentation captures against a live, seeded stack.
 *
 * Regenerate with `make docs-shots` after console changes that move what a
 * capture shows. The generated PNGs in docs-site/public/screenshots are
 * documentation artifacts, not test snapshots.
 */

import assert from 'node:assert/strict'
import { execFile as execFileCb, spawn } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { existsSync, mkdirSync } from 'node:fs'
import net from 'node:net'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

import puppeteer from 'puppeteer-core'

const execFile = promisify(execFileCb)

const chrome =
  process.env.SMOKE_CHROME ||
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const serverBinary = process.env.SMOKE_BIN
const postgresContainer = `dufflebag-docs-shots-${process.pid}`
const objectContainer = `dufflebag-docs-shots-s3-${process.pid}`
const vaultContainer = `dufflebag-docs-shots-vault-${process.pid}`
const vaultToken = 'docs-shots-root'
const objectStorageImage = 'quay.io/benjamin_holmes/ceph-aio:v20'
const objectStorageBucket = 'dufflebag-docs-shots'
const signingKey = randomBytes(32).toString('hex')
const organizationName = 'platform-engineering'
const projectName = 'golden-images'
const screenshotsDir = fileURLToPath(
  new URL('../../../docs-site/public/screenshots/', import.meta.url),
)

let base
let server
let serverOutput = ''
let browser
let page
let vaultBase

/** Polls a condition to truth before a deadline. A wait, never a sleep. */
async function until(what, condition, timeoutMs = 30000, intervalMs = 100) {
  const deadline = Date.now() + timeoutMs
  let lastFailure
  for (;;) {
    try {
      const value = await condition()
      if (value) return value
      lastFailure = undefined
    } catch (err) {
      lastFailure = err
    }
    if (Date.now() > deadline) {
      throw new Error(
        `timed out waiting for ${what}` + (lastFailure ? `: ${lastFailure}` : ''),
      )
    }
    await new Promise((resolve) => setTimeout(resolve, intervalMs))
  }
}

const bodyText = () => page.evaluate(() => document.body.innerText)

const waitForText = async (text, timeout = 30000) => {
  try {
    await page.waitForFunction(
      (needle) => document.body.innerText.includes(needle),
      { timeout, polling: 100 },
      text,
    )
  } catch (err) {
    throw new Error(`never saw "${text}"; the page says:\n${await bodyText()}`, { cause: err })
  }
}

const clickByText = (selector, text) =>
  until(`clickable "${text}"`, () =>
    page.$$eval(
      selector,
      (elements, needle) => {
        const match = elements.find(
          (element) =>
            element.innerText && element.innerText.trim().includes(needle) && !element.disabled,
        )
        if (!match) return false
        match.click()
        return true
      },
      text,
    ),
  )

async function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer()
    probe.once('error', reject)
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address()
      probe.close(() => resolve(port))
    })
  })
}

function startServer(env) {
  const child = spawn(serverBinary, [], { env: { ...process.env, ...env } })
  child.stdout.on('data', (chunk) => (serverOutput += chunk))
  child.stderr.on('data', (chunk) => (serverOutput += chunk))
  return child
}

const waitForExit = (child) =>
  new Promise((resolve) => child.once('exit', (code) => resolve(code)))

async function tokenFor(clientID, clientSecret) {
  const response = await fetch(`${base}/oauth2/token`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      Authorization: `Basic ${Buffer.from(`${clientID}:${clientSecret}`).toString('base64')}`,
    },
    body: new URLSearchParams({
      grant_type: 'client_credentials',
      audience: 'https://api.hashicorp.cloud',
    }),
  })
  assert.equal(response.status, 200, `token endpoint answered ${response.status}`)
  const body = await response.json()
  assert.ok(body.access_token, 'no access_token in token response')
  return body.access_token
}

async function api(token, method, path, body) {
  const response = await fetch(`${base}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  })
  const text = await response.text()
  assert.ok(response.ok, `${method} ${path} answered ${response.status}: ${text}`)
  return text ? JSON.parse(text) : null
}

async function vaultWrite(path, body) {
  const response = await fetch(`${vaultBase}${path}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Vault-Token': vaultToken,
    },
    body: JSON.stringify(body),
  })
  const text = await response.text()
  assert.ok(response.ok, `POST ${path} answered ${response.status}: ${text}`)
}

async function objectStorageDiagnosis() {
  const lines = []
  for (const [what, args] of [
    ['healthcheck', ['inspect', '-f',
      '{{range .State.Health.Log}}{{printf "%.300s" .Output}}{{end}}', objectContainer]],
    ['disk', ['exec', objectContainer, 'df', '-h', '/']],
  ]) {
    try {
      const { stdout } = await execFile('docker', args)
      lines.push(`${what}:\n${stdout.trim() || '(no output)'}`)
    } catch (err) {
      lines.push(`${what} unavailable: ${err.message}`)
    }
  }
  return lines.join('\n')
}

async function bootStack() {
  assert.ok(serverBinary, 'SMOKE_BIN must point at a built dufflebag binary (run via `make docs-shots`)')
  assert.ok(
    existsSync(chrome),
    `no Chrome at ${chrome} — set SMOKE_CHROME to a Chrome/Chromium binary`,
  )

  await execFile('docker', [
    'run', '-d', '--rm', '--name', postgresContainer,
    '-e', 'POSTGRES_PASSWORD=postgres',
    '-e', 'POSTGRES_DB=dufflebag',
    '-p', '127.0.0.1::5432',
    'postgres:17-alpine',
  ])
  const { stdout: portLine } = await execFile(
    'docker', ['port', postgresContainer, '5432/tcp'],
  )
  const postgresPort = portLine.trim().split('\n')[0].split(':').pop()
  await until('postgres to accept connections', async () => {
    await execFile('docker', [
      'exec', postgresContainer, 'pg_isready', '-U', 'postgres', '-d', 'dufflebag',
    ])
    return true
  }, 60000, 250)

  await execFile('docker', [
    'run', '-d', '--rm', '--name', vaultContainer,
    '-e', `VAULT_DEV_ROOT_TOKEN_ID=${vaultToken}`,
    '-p', '127.0.0.1::8200',
    'hashicorp/vault:1.17',
  ])
  const { stdout: vaultPortLine } = await execFile(
    'docker', ['port', vaultContainer, '8200/tcp'],
  )
  const vaultPort = vaultPortLine.trim().split('\n')[0].split(':').pop()
  vaultBase = `http://127.0.0.1:${vaultPort}`
  await until('Vault to report healthy', async () => {
    const response = await fetch(`${vaultBase}/v1/sys/health`)
    await response.arrayBuffer()
    return response.status === 200
  }, 60000, 250)
  await vaultWrite('/v1/sys/mounts/transit', { type: 'transit' })

  await execFile('docker', [
    'run', '-d', '--rm', '--name', objectContainer,
    '-p', '127.0.0.1::8000',
    objectStorageImage,
  ])
  const { stdout: objectPortLine } = await execFile(
    'docker', ['port', objectContainer, '8000/tcp'],
  )
  const objectPort = objectPortLine.trim().split('\n')[0].split(':').pop()
  try {
    await until('Ceph to report healthy', async () => {
      const { stdout } = await execFile('docker', [
        'inspect', '-f', '{{.State.Health.Status}}', objectContainer,
      ])
      return stdout.trim() === 'healthy'
    }, 300000, 2000)
  } catch (err) {
    throw new Error(`${err.message}\n${await objectStorageDiagnosis()}`)
  }
  await execFile('docker', [
    'exec', objectContainer, 'radosgw-admin', 'user', 'create',
    '--uid=dufflebag-docs-shots', '--display-name=dufflebag docs shots',
    '--access-key=testaccess', '--secret-key=testsecret',
  ])
  await execFile('docker', [
    'cp', fileURLToPath(new URL('../../../e2e/support/create-bucket.py', import.meta.url)),
    `${objectContainer}:/tmp/create-bucket.py`,
  ])
  await execFile('docker', [
    'exec', objectContainer, 'python3', '/tmp/create-bucket.py',
    'testaccess', 'testsecret', objectStorageBucket,
  ])

  const adminURL =
    `postgres://postgres:postgres@127.0.0.1:${postgresPort}/dufflebag?sslmode=disable`
  await until('migrations to apply and the RLS gate to refuse the superuser', async () => {
    serverOutput = ''
    const migrator = startServer({
      DFBG_DATABASE_URL: adminURL,
      DFBG_TOKEN_SIGNING_KEY: signingKey,
    })
    await waitForExit(migrator)
    return serverOutput.includes('refusing to serve')
  }, 60000, 500)

  await execFile('docker', [
    'exec', postgresContainer, 'psql', '-v', 'ON_ERROR_STOP=1',
    '-U', 'postgres', '-d', 'dufflebag',
    '-c', "CREATE ROLE dufflebag_app LOGIN PASSWORD 'app' NOSUPERUSER NOBYPASSRLS",
    '-c', 'GRANT USAGE ON SCHEMA public TO dufflebag_app',
    '-c', 'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dufflebag_app',
  ])

  const serverPort = await freePort()
  base = `http://127.0.0.1:${serverPort}`
  serverOutput = ''
  server = startServer({
    DFBG_DATABASE_URL:
      `postgres://dufflebag_app:app@127.0.0.1:${postgresPort}/dufflebag?sslmode=disable`,
    DFBG_HTTP_ADDR: `127.0.0.1:${serverPort}`,
    DFBG_KEY_PROVIDER: 'vault',
    VAULT_ADDR: vaultBase,
    VAULT_TOKEN: vaultToken,
    DFBG_OBJECT_STORAGE_ENDPOINT: `http://127.0.0.1:${objectPort}`,
    DFBG_OBJECT_STORAGE_REGION: 'us-east-1',
    DFBG_OBJECT_STORAGE_BUCKET: objectStorageBucket,
    DFBG_OBJECT_STORAGE_ACCESS_KEY: 'testaccess',
    DFBG_OBJECT_STORAGE_SECRET_KEY: 'testsecret',
  })
  await until('the server to answer', async () => {
    const response = await fetch(`${base}/`)
    return response.status === 200
  }, 60000, 250)

  browser = await puppeteer.launch({
    executablePath: chrome,
    headless: true,
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  })
  page = await browser.newPage()
  page.setDefaultTimeout(30000)
  await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'light' }])
  await page.setViewport({ width: 1440, height: 900 })
}

async function capture(name) {
  await page.evaluate(async () => {
    await document.fonts.ready
    if (!document.querySelector('#docs-shots-no-motion')) {
      const style = document.createElement('style')
      style.id = 'docs-shots-no-motion'
      style.textContent =
        '*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}'
      document.head.appendChild(style)
    }
  })
  await page.screenshot({ path: join(screenshotsDir, name) })
  console.log(`captured ${name}`)
}

async function initializeInstance() {
  await clickByText('button', 'Initialize this instance')
  await waitForText('Administrative credentials')
  const credentials = await until('the minted credentials to be readable', () =>
    page.evaluate(() => {
      const text = document.body.innerText
      const values = [...document.querySelectorAll('input')].map((input) => input.value)
      const uuid = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i
      const clientID = values.find((value) => uuid.test(value)) ?? (text.match(uuid) ?? [])[0]
      const clientSecret = values.find(
        (value) =>
          value && value.length >= 40 && !uuid.test(value) && !value.startsWith('dfbg-recovery-'),
      )
      return clientID && clientSecret ? { clientID, clientSecret } : null
    }),
  )
  await page.click('#init-stored')
  await clickByText('button', 'Continue to organization')
  await waitForText('Name your organization')
  await page.type('#organization-name', organizationName)
  await clickByText('button', 'Create organization and continue')
  await waitForText('Name your first project')
  await page.type('#project-name', projectName)
  await clickByText('button', 'Create project and open the console')
  await waitForText('Sign out')
  return credentials
}

async function createPrincipal(rootToken, body) {
  const created = await api(rootToken, 'POST', '/api/v1/principals', body)
  const issued = await api(rootToken, 'POST', `/api/v1/principals/${created.id}/secrets`, {})
  assert.ok(issued.secret, `no secret issued for ${body.name}`)
  return { ...created, secret: issued.secret }
}

async function completeVersion(token, versionsPath, fixture) {
  await api(token, 'POST', versionsPath, {
    fingerprint: fixture.fingerprint,
    template_type: 'HCL2',
  })
  const { build } = await api(
    token,
    'POST',
    `${versionsPath}/${fixture.fingerprint}/builds`,
    {
      component_type: fixture.component,
      packer_run_uuid: `run-${fixture.fingerprint}`,
      artifacts: [],
    },
  )
  await api(token, 'PATCH', `${versionsPath}/${fixture.fingerprint}/builds/${build.id}`, {
    status: 'BUILD_DONE',
    platform: fixture.platform,
    artifacts: [{ external_identifier: fixture.artifact, region: fixture.region }],
    labels: { owner: fixture.owner },
    metadata: {
      packer: {
        version: '1.16.0',
        options: { path: fixture.template, vars: [], only: [fixture.component] },
        os: { type: 'linux', details: { arch: 'amd64', version: fixture.os } },
        plugins: [{ name: fixture.platform, version: '1.0.0' }],
      },
    },
  })
}

async function seedFixtures(credentials) {
  const rootToken = await tokenFor(credentials.clientID, credentials.clientSecret)
  const { organizations } = await api(rootToken, 'GET', '/api/v1/organizations')
  const organization = organizations.find((candidate) => candidate.name === organizationName)
  assert.ok(organization, `organization ${organizationName} was not created`)
  const { projects } = await api(
    rootToken,
    'GET',
    `/api/v1/organizations/${organization.id}/projects`,
  )
  const project = projects.find((candidate) => candidate.name === projectName)
  assert.ok(project, `project ${projectName} was not created`)

  const scope = { organization_id: organization.id, project_id: project.id }
  const builder = await createPrincipal(rootToken, {
    name: 'image-builder', role: 'builder', ...scope,
  })
  const publisher = await createPrincipal(rootToken, {
    name: 'release-manager', role: 'publisher', ...scope,
  })
  await createPrincipal(rootToken, {
    name: 'catalog-reader', role: 'reader', ...scope,
  })

  const builderToken = await tokenFor(builder.client_id, builder.secret)
  const publisherToken = await tokenFor(publisher.client_id, publisher.secret)
  const bucketBase =
    `/packer/2023-01-01/organizations/${organization.id}` +
    `/projects/${project.id}/buckets`

  await api(builderToken, 'PUT', bucketBase, {
    name: 'base-images',
    description: 'Hardened base images for production workloads',
    labels: { owner: 'platform-engineering', lifecycle: 'managed' },
  })
  const baseVersions = `${bucketBase}/base-images/versions`
  await completeVersion(builderToken, baseVersions, {
    fingerprint: 'ubuntu-2404-2026-08',
    component: 'amazon-ebs.ubuntu',
    platform: 'aws',
    artifact: 'ami-0a12bc34de56f7890',
    region: 'eu-west-2',
    owner: 'platform-engineering',
    template: './images/ubuntu.pkr.hcl',
    os: 'ubuntu-24.04',
  })
  await completeVersion(builderToken, baseVersions, {
    fingerprint: 'ubuntu-2404-2026-07',
    component: 'amazon-ebs.ubuntu',
    platform: 'aws',
    artifact: 'ami-0123456789abcdef0',
    region: 'eu-west-2',
    owner: 'platform-engineering',
    template: './images/ubuntu.pkr.hcl',
    os: 'ubuntu-24.04',
  })
  await api(publisherToken, 'PATCH', `${baseVersions}/ubuntu-2404-2026-07`, {
    revoke_in: '0s',
    revocation_message: 'Superseded after the August hardening release',
  })
  await api(builderToken, 'POST', baseVersions, {
    fingerprint: 'ubuntu-2404-candidate', template_type: 'HCL2',
  })
  await api(publisherToken, 'POST', `${bucketBase}/base-images/channels`, {
    name: 'production', restricted: true,
  })
  await api(publisherToken, 'PATCH', `${bucketBase}/base-images/channels/production`, {
    update_mask: 'versionFingerprint', version_fingerprint: 'ubuntu-2404-2026-08',
  })

  await api(builderToken, 'PUT', bucketBase, {
    name: 'database-images',
    description: 'Database images maintained by the reliability team',
    labels: { owner: 'database-reliability', lifecycle: 'managed' },
  })
  await completeVersion(builderToken, `${bucketBase}/database-images/versions`, {
    fingerprint: 'postgres-17-2026-08',
    component: 'docker.postgres',
    platform: 'docker',
    artifact: 'sha256:78d337fa4c4d8f0e',
    region: 'registry.internal',
    owner: 'database-reliability',
    template: './images/postgres.pkr.hcl',
    os: 'alpine-3.22',
  })

  await api(
    rootToken,
    'PUT',
    `/api/v1/organizations/${organization.id}/projects/${project.id}/pins/base-images`,
  )

}

async function captureSeededScreens() {
  await page.goto(`${base}/buckets`, { waitUntil: 'domcontentloaded' })
  await waitForText('base-images')
  await waitForText('database-images')
  await page.waitForSelector('section[aria-label="Pinned buckets"]')
  await capture('buckets.png')

  // The bucket screen at its natural scroll: the facet rail (Overview /
  // Versions / Channels with counts) beside the overview cards.
  await page.goto(`${base}/buckets/base-images`, { waitUntil: 'domcontentloaded' })
  await waitForText('Bucket details')
  await page.waitForSelector('nav[aria-label="Bucket facets"] button[role="tab"]')
  await capture('bucket-facets.png')

  await page.goto(
    `${base}/buckets/base-images/versions/ubuntu-2404-2026-08`,
    { waitUntil: 'domcontentloaded' },
  )
  await waitForText('Operations')
  await waitForText('production')
  await until('the Operations card to be visible', () =>
    page.$$eval('.pf-v6-c-card', (cards) => {
      const card = cards.find((candidate) => candidate.innerText.includes('Operations'))
      if (!card) return false
      card.scrollIntoView({ block: 'center' })
      return card.getBoundingClientRect().height > 0
    }))
  await capture('version-operations.png')

  await clickByText('button', 'Revoke')
  await waitForText('Revoke base-images v1')
  await page.waitForSelector('#typed-confirm-modal-input')
  await page.type('#typed-confirm-modal-input', 'v1')
  await capture('typed-confirm.png')
  await clickByText('[role="dialog"] button', 'Cancel')

  await page.goto(`${base}/principals`, { waitUntil: 'domcontentloaded' })
  await waitForText('Service principals')
  await waitForText('image-builder')
  await waitForText('release-manager')
  await waitForText('catalog-reader')
  await capture('principals.png')

  await page.goto(`${base}/bagdrop`, { waitUntil: 'domcontentloaded' })
  await waitForText('Bag Drop is not configured')
  await page.type('#bagdrop-organization-id', 'destination-platform')
  await page.type('#bagdrop-project-id', 'production-images')
  await page.type('#bagdrop-client-id', 'bag-drop-publisher')
  await page.type('#bagdrop-client-secret', 'not-a-real-secret')
  await capture('bag-drop.png')

  await clickByText('button', 'Sign out')
  await waitForText('Log in')
  await capture('login.png')
}

async function cleanup() {
  if (browser) await browser.close().catch(() => {})
  if (server && server.exitCode === null) {
    const exited = waitForExit(server)
    server.kill('SIGTERM')
    await Promise.race([exited, new Promise((resolve) => setTimeout(resolve, 10000))])
    if (server.exitCode === null) server.kill('SIGKILL')
  }
  await execFile('docker', ['rm', '-f', postgresContainer]).catch(() => {})
  await execFile('docker', ['rm', '-f', objectContainer]).catch(() => {})
  await execFile('docker', ['rm', '-f', vaultContainer]).catch(() => {})
}

async function main() {
  mkdirSync(screenshotsDir, { recursive: true })
  await bootStack()

  // Capture the fresh instance before the only boot is claimed; all remaining
  // captures reuse that initialized, authenticated process after API seeding.
  await page.goto(base, { waitUntil: 'domcontentloaded' })
  await waitForText('Whoever completes this flow first owns the deployment')
  await capture('first-run.png')

  const credentials = await initializeInstance()
  await seedFixtures(credentials)
  await captureSeededScreens()
}

try {
  await main()
} finally {
  await cleanup()
}
