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
import http from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { zstdCompressSync } from 'node:zlib'

import puppeteer from 'puppeteer-core'

const execFile = promisify(execFileCb)

const chrome =
  process.env.SMOKE_CHROME ||
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const serverBinary = process.env.SMOKE_BIN
const postgresContainer = `dufflebag-docs-shots-${process.pid}`
const objectContainer = `dufflebag-docs-shots-s3-${process.pid}`
const vaultContainer = `dufflebag-docs-shots-vault-${process.pid}`
const scannerContainer = `dufflebag-docs-shots-osv-${process.pid}`
const scannerImage = process.env.OSV_STUB_IMAGE || 'dufflebag-osv-stub:dev'
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
let webhookReceiver
let webhookReceiverPort
let scannerBase
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
  await execFile('docker', ['image', 'inspect', scannerImage]).catch((err) => {
    throw new Error(
      `missing ${scannerImage}; make docs-shots builds it before launching`,
      { cause: err },
    )
  })
  await execFile('docker', [
    'run', '-d', '--rm', '--name', scannerContainer,
    '-p', '127.0.0.1::8080', scannerImage,
  ])
  const { stdout: scannerPortLine } = await execFile(
    'docker', ['port', scannerContainer, '8080/tcp'],
  )
  const scannerPort = scannerPortLine.trim().split('\n')[0].split(':').pop()
  scannerBase = `http://127.0.0.1:${scannerPort}`
  await until('the recorded OSV stub to answer', async () => {
    const response = await fetch(`${scannerBase}/v1/vulns/GO-2026-4945`)
    await response.arrayBuffer()
    return response.status === 200
  }, 60000, 100)

  server = startServer({
    DFBG_DATABASE_URL:
      `postgres://dufflebag_app:app@127.0.0.1:${postgresPort}/dufflebag?sslmode=disable`,
    DFBG_HTTP_ADDR: `127.0.0.1:${serverPort}`,
    DFBG_KEY_PROVIDER: 'vault',
    DFBG_VAULT_ADDR: vaultBase,
    DFBG_VAULT_TOKEN: vaultToken,
    DFBG_OBJECT_STORAGE_ENDPOINT: `http://127.0.0.1:${objectPort}`,
    DFBG_OBJECT_STORAGE_REGION: 'us-east-1',
    DFBG_OBJECT_STORAGE_BUCKET: objectStorageBucket,
    DFBG_OBJECT_STORAGE_ACCESS_KEY: 'testaccess',
    DFBG_OBJECT_STORAGE_SECRET_KEY: 'testsecret',
    // The seeded webhook's receiver lives on loopback; the SSRF refusal is
    // deliberately relaxed for this isolated capture stack only.
    DFBG_WEBHOOK_ALLOW_PRIVATE: 'true',
    DFBG_SCANNER_ADAPTER: 'osv',
    DFBG_SCANNER_ENDPOINT: scannerBase,
    DFBG_SCANNER_INTERVAL: '2s',
  })
  webhookReceiver = http.createServer((_request, response) => {
    response.statusCode = 200
    response.end('ok')
  })
  await new Promise((resolve) => webhookReceiver.listen(0, '127.0.0.1', resolve))
  webhookReceiverPort = webhookReceiver.address().port
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
  if (fixture.sbom) {
    // SBOM uploads are only accepted during the build's running window.
    await api(token, 'PATCH', `${versionsPath}/${fixture.fingerprint}/builds/${build.id}`, {
      status: 'BUILD_RUNNING',
    })
    await api(token, 'PUT', `${versionsPath}/${fixture.fingerprint}/builds/${build.id}/sboms`, {
      name: fixture.sbom.name,
      format: 'SPDX',
      compressed_sbom: zstdCompressSync(
        Buffer.from(JSON.stringify(fixture.sbom.document)),
      ).toString('base64'),
    })
  }
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
  return build
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
  const maintainer = await createPrincipal(rootToken, {
    name: 'registry-maintainer', role: 'maintainer', ...scope,
  })
  await createPrincipal(rootToken, {
    name: 'catalog-reader', role: 'reader', ...scope,
  })

  const builderToken = await tokenFor(builder.client_id, builder.secret)
  const publisherToken = await tokenFor(publisher.client_id, publisher.secret)
  const maintainerToken = await tokenFor(maintainer.client_id, maintainer.secret)
  const bucketBase =
    `/packer/2023-01-01/organizations/${organization.id}` +
    `/projects/${project.id}/buckets`

  await api(builderToken, 'PUT', bucketBase, {
    name: 'base-images',
    description: 'Hardened base images for production workloads',
    labels: { owner: 'platform-engineering', lifecycle: 'managed' },
  })
  const baseVersions = `${bucketBase}/base-images/versions`
  const ubuntuBuild = await completeVersion(builderToken, baseVersions, {
    fingerprint: 'ubuntu-2404-2026-08',
    component: 'amazon-ebs.ubuntu',
    platform: 'aws',
    artifact: 'ami-0a12bc34de56f7890',
    region: 'eu-west-2',
    owner: 'platform-engineering',
    template: './images/ubuntu.pkr.hcl',
    os: 'ubuntu-24.04',
    sbom: {
      name: 'ubuntu-2404-base',
      document: {
        spdxVersion: 'SPDX-2.3',
        dataLicense: 'CC0-1.0',
        SPDXID: 'SPDXRef-DOCUMENT',
        name: 'ubuntu-2404-base',
        documentNamespace: 'https://registry.example.com/spdx/ubuntu-2404-base',
        creationInfo: {
          created: '2026-08-01T09:00:00Z',
          creators: ['Tool: hcp-sbom'],
        },
        packages: [
          {
            SPDXID: 'SPDXRef-Package-openssl',
            name: 'openssl',
            versionInfo: '3.0.13-0ubuntu3.5',
            downloadLocation: 'NOASSERTION',
            externalRefs: [{
              referenceCategory: 'PACKAGE-MANAGER',
              referenceType: 'purl',
              referenceLocator: 'pkg:deb/ubuntu/openssl@3.0.13-0ubuntu3.5',
            }],
          },
          {
            SPDXID: 'SPDXRef-Package-coreutils',
            name: 'coreutils',
            versionInfo: '9.4-3ubuntu6',
            downloadLocation: 'NOASSERTION',
            externalRefs: [{
              referenceCategory: 'PACKAGE-MANAGER',
              referenceType: 'purl',
              referenceLocator: 'pkg:deb/ubuntu/coreutils@9.4-3ubuntu6',
            }],
          },
          {
            // The one queryable package: its purl resolves to the stub's
            // recorded vulnerable go-jose captures, whose two advisory
            // details are both recorded — a completable findings scan.
            SPDXID: 'SPDXRef-Package-go-jose',
            name: 'github.com/go-jose/go-jose/v4',
            versionInfo: 'v4.1.1',
            downloadLocation: 'NOASSERTION',
            externalRefs: [{
              referenceCategory: 'PACKAGE-MANAGER',
              referenceType: 'purl',
              referenceLocator: 'pkg:golang/github.com/go-jose/go-jose/v4@v4.1.1',
            }],
          },
        ],
      },
    },
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
  await completeVersion(builderToken, baseVersions, {
    fingerprint: 'ubuntu-2404-2026-06',
    component: 'amazon-ebs.ubuntu',
    platform: 'aws',
    artifact: 'ami-0fedcba9876543210',
    region: 'eu-west-2',
    owner: 'platform-engineering',
    template: './images/ubuntu.pkr.hcl',
    os: 'ubuntu-24.04',
  })
  await completeVersion(builderToken, baseVersions, {
    fingerprint: 'ubuntu-2404-2026-05',
    component: 'amazon-ebs.ubuntu',
    platform: 'aws',
    artifact: 'ami-0aabbccddeeff0011',
    region: 'eu-west-2',
    owner: 'platform-engineering',
    template: './images/ubuntu.pkr.hcl',
    os: 'ubuntu-24.04',
  })
  await api(builderToken, 'POST', baseVersions, {
    fingerprint: 'ubuntu-2404-candidate', template_type: 'HCL2',
  })
  await api(maintainerToken, 'POST', `${bucketBase}/base-images/channels`, {
    name: 'production', restricted: true,
  })
  await api(maintainerToken, 'PATCH', `${bucketBase}/base-images/channels/production`, {
    update_mask: 'versionFingerprint', version_fingerprint: 'ubuntu-2404-2026-08',
  })

  await api(builderToken, 'PUT', bucketBase, {
    name: 'database-images',
    description: 'Database images maintained by the reliability team',
    labels: { owner: 'database-reliability', lifecycle: 'managed' },
  })
  const postgresBuild = await completeVersion(builderToken, `${bucketBase}/database-images/versions`, {
    fingerprint: 'postgres-17-2026-08',
    component: 'docker.postgres',
    platform: 'docker',
    artifact: 'sha256:78d337fa4c4d8f0e',
    region: 'registry.internal',
    owner: 'database-reliability',
    template: './images/postgres.pkr.hcl',
    os: 'alpine-3.22',
    sbom: {
      name: 'postgres-17-alpine',
      document: {
        spdxVersion: 'SPDX-2.3',
        dataLicense: 'CC0-1.0',
        SPDXID: 'SPDXRef-DOCUMENT',
        name: 'postgres-17-alpine',
        documentNamespace: 'https://registry.example.com/spdx/postgres-17-alpine',
        creationInfo: {
          created: '2026-08-01T09:00:00Z',
          creators: ['Tool: hcp-sbom'],
        },
        // Exactly one queryable package: the recorded OSV stub matches
        // request bodies byte-for-byte, and this purl resolves to its
        // patched busybox capture — a clean successful scan.
        packages: [
          {
            SPDXID: 'SPDXRef-Package-busybox',
            name: 'busybox',
            versionInfo: '1.36.1-r31',
            downloadLocation: 'NOASSERTION',
            externalRefs: [{
              referenceCategory: 'PACKAGE-MANAGER',
              referenceType: 'purl',
              referenceLocator: 'pkg:apk/alpine/busybox@1.36.1-r31?arch=x86_64&distro=alpine-3.20.10',
            }],
          },
        ],
      },
    },
  })

  await api(builderToken, 'PUT', bucketBase, {
    name: 'app-images',
    description: 'Application runtimes layered on the hardened bases',
    labels: { owner: 'app-platform', lifecycle: 'managed' },
  })
  const appVersions = `${bucketBase}/app-images/versions`
  await completeVersion(builderToken, appVersions, {
    fingerprint: 'node-22-2026-08',
    component: 'docker.node',
    platform: 'docker',
    artifact: 'sha256:1f9e8d7c6b5a4938',
    region: 'registry.internal',
    owner: 'app-platform',
    template: './images/node.pkr.hcl',
    os: 'ubuntu-24.04',
  })
  await completeVersion(builderToken, appVersions, {
    fingerprint: 'python-313-2026-08',
    component: 'docker.python',
    platform: 'docker',
    artifact: 'sha256:2a3b4c5d6e7f8091',
    region: 'registry.internal',
    owner: 'app-platform',
    template: './images/python.pkr.hcl',
    os: 'ubuntu-24.04',
  })

  // One real consumption handoff: the version carries both an AMI and a
  // docker-tag result. Both builds must exist before either finishes or the
  // first terminal update would complete the version.
  const taggedConsumptionFingerprint = 'dufflebag-demo-app-1-0-0'
  await api(builderToken, 'POST', appVersions, {
    fingerprint: taggedConsumptionFingerprint,
    template_type: 'HCL2',
  })
  const { build: consumptionAWSBuild } = await api(
    builderToken,
    'POST',
    `${appVersions}/${taggedConsumptionFingerprint}/builds`,
    {
      component_type: 'amazon-ebs.dufflebag-demo-app',
      packer_run_uuid: 'run-dufflebag-demo-app-1-0-0',
      artifacts: [],
    },
  )
  const { build: consumptionDockerBuild } = await api(
    builderToken,
    'POST',
    `${appVersions}/${taggedConsumptionFingerprint}/builds`,
    {
      component_type: 'docker.dufflebag-demo-app',
      packer_run_uuid: 'run-dufflebag-demo-app-1-0-0',
      artifacts: [],
    },
  )
  await api(
    builderToken,
    'PATCH',
    `${appVersions}/${taggedConsumptionFingerprint}/builds/${consumptionAWSBuild.id}`,
    {
      status: 'BUILD_DONE',
      platform: 'aws',
      artifacts: [{
        external_identifier: 'ami-0b6873e6a9ffc49be',
        region: 'eu-west-2',
      }],
      // Completion requires seen metadata; shapes mirror the live dual-output
      // specimen (app-multi-verify, packer 1.16.0).
      metadata: {
        packer: {
          version: '1.16.0',
          options: { path: 'app-multi.pkr.hcl' },
          os: { type: 'darwin', details: { arch: 'arm64', version: '24.6.0' } },
          plugins: [{ name: 'amazon', version: '1.8.2' }],
        },
      },
    },
  )
  const consumptionDigest =
    'sha256:387f75c3949157f96595e5cb3b6c6b2a99ebef7d9c535d2ce5bdc8807e44ad02'
  const consumptionTag = 'quay.io/benjamin_holmes/dufflebag-demo-app:1.0.0'
  await api(
    builderToken,
    'PATCH',
    `${appVersions}/${taggedConsumptionFingerprint}/builds/${consumptionDockerBuild.id}`,
    {
      status: 'BUILD_DONE',
      platform: 'docker',
      labels: {
        tags: consumptionTag,
        PackerArtifactID: consumptionTag,
        ImageSha256: consumptionDigest,
      },
      artifacts: [{ external_identifier: consumptionDigest, region: 'docker' }],
      metadata: {
        packer: {
          version: '1.16.0',
          options: { path: 'app-multi.pkr.hcl' },
          os: { type: 'darwin', details: { arch: 'arm64', version: '24.6.0' } },
          plugins: [{ name: 'docker', version: '1.1.4' }],
        },
      },
    },
  )
  await api(builderToken, 'PUT', bucketBase, {
    name: 'builder-images',
    description: 'CI build agents with pinned toolchains',
    labels: { owner: 'developer-experience', lifecycle: 'managed' },
  })
  await completeVersion(builderToken, `${bucketBase}/builder-images/versions`, {
    fingerprint: 'golang-1263-2026-08',
    component: 'docker.golang',
    platform: 'docker',
    artifact: 'sha256:3c4d5e6f70819203',
    region: 'registry.internal',
    owner: 'developer-experience',
    template: './images/golang.pkr.hcl',
    os: 'alpine-3.22',
  })
  await api(builderToken, 'PUT', bucketBase, {
    name: 'network-images',
    description: 'Edge and service-mesh appliance images',
    labels: { owner: 'network-engineering', lifecycle: 'evaluation' },
  })
  await completeVersion(builderToken, `${bucketBase}/network-images/versions`, {
    fingerprint: 'envoy-1332-2026-08',
    component: 'docker.envoy',
    platform: 'docker',
    artifact: 'sha256:4d5e6f7081920314',
    region: 'registry.internal',
    owner: 'network-engineering',
    template: './images/envoy.pkr.hcl',
    os: 'alpine-3.22',
  })
  await api(
    rootToken,
    'PUT',
    `/api/v1/organizations/${organization.id}/projects/${project.id}/pins/base-images`,
  )
  await api(
    rootToken,
    'PUT',
    `/api/v1/organizations/${organization.id}/projects/${project.id}/pins/database-images`,
  )

  const { buckets } = await api(rootToken, 'GET', bucketBase)
  const baseImagesBucket = buckets.find((candidate) => candidate.name === 'base-images')
  assert.ok(baseImagesBucket, 'base-images was not returned by the seeded bucket listing')
  const bucketPrincipal = await createPrincipal(rootToken, {
    name: 'base-images-builder',
    role: 'builder',
    ...scope,
    bucket_id: baseImagesBucket.id,
  })

  await api(rootToken, 'POST', '/api/v1/audit/targets', {
    path: join(tmpdir(), `dufflebag-docs-shots-audit-${process.pid}.log`),
  })

  await api(
    rootToken,
    'POST',
    `/api/v1/organizations/${organization.id}/projects/${project.id}/webhooks`,
    {
      name: 'ci-notifications',
      url: `http://127.0.0.1:${webhookReceiverPort}/hooks/registry`,
      description: 'Notify the pipeline when registry state changes.',
      events: [],
    },
  )

  return {
    ubuntuBuildID: ubuntuBuild.id,
    postgresBuildID: postgresBuild.id,
    compatBase: `/packer/2023-01-01/organizations/${organization.id}/projects/${project.id}`,
    builderToken,
    bucketPrincipal,
    rootCredentials: credentials,
  }
}

async function captureSeededScreens(seeded) {
  await page.goto(`${base}/`, { waitUntil: 'domcontentloaded' })
  await waitForText('All buckets')
  await waitForText('network-images')
  await capture('buckets.png')

  await page.click('#tenant-bucket')
  await waitForText('base-images')
  await waitForText('database-images')
  await waitForText('network-images')
  await capture('bucket-picker.png')

  // The bucket screen at its natural scroll: the facet rail (Overview /
  // Versions / Channels with counts) beside the overview cards.
  await page.goto(`${base}/buckets/base-images`, { waitUntil: 'domcontentloaded' })
  await waitForText('Bucket details')
  await page.waitForSelector('nav[aria-label="Bucket facets"] button[role="tab"]')
  await capture('bucket-facets.png')

  await clickByText('nav[aria-label="Bucket facets"] button', 'Versions')
  await waitForText('revoked')
  await capture('versions-table.png')

  // A bucket created ahead of its first publish: seeded here, after the
  // populated captures, so the list and picker shots keep their five buckets.
  await api(seeded.builderToken, 'PUT', `${seeded.compatBase}/buckets`, {
    name: 'windows-images',
    description: 'Created in the console ahead of the first publish',
    labels: { owner: 'platform-engineering', lifecycle: 'incubating' },
  })
  await page.goto(`${base}/buckets/windows-images`, { waitUntil: 'domcontentloaded' })
  await waitForText('Bucket details')
  await clickByText('nav[aria-label="Bucket facets"] button', 'Versions')
  await waitForText('No versions in this bucket')
  await waitForText('Connect a client')
  await capture('bucket-empty.png')

  // Back to the populated bucket for the channel capture.
  await page.goto(`${base}/buckets/base-images`, { waitUntil: 'domcontentloaded' })
  await waitForText('Bucket details')
  await clickByText('nav[aria-label="Bucket facets"] button', 'Channels')
  await waitForText('production')
  await capture('channels.png')

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

  await page.goto(
    `${base}/buckets/app-images/versions/dufflebag-demo-app-1-0-0`,
    { waitUntil: 'domcontentloaded' },
  )
  await waitForText('Consume this version')
  for (const consumer of ['terraform', 'docker', 'podman', 'aws']) {
    await page.waitForSelector(`#consume-${consumer}`)
  }
  await page.click('#consume-docker')
  await waitForText('docker pull quay.io/benjamin_holmes/dufflebag-demo-app:1.0.0')
  await until('the tagged Consume card to be visible', () =>
    page.$$eval('.pf-v6-c-card', (cards) => {
      const card = cards.find((candidate) => candidate.innerText.includes('Consume this version'))
      if (!card) return false
      card.scrollIntoView({ block: 'center' })
      return card.getBoundingClientRect().height > 0
    }))
  await capture('version-consume.png')

  await page.goto(
    `${base}/buckets/app-images/versions/node-22-2026-08`,
    { waitUntil: 'domcontentloaded' },
  )
  await waitForText('Consume this version')
  await page.waitForSelector('#consume-terraform')
  assert.equal(await page.$('#consume-docker'), null, 'untagged version offered Docker')
  assert.equal(await page.$('#consume-podman'), null, 'untagged version offered Podman')
  await until('the untagged Consume card to be visible', () =>
    page.$$eval('.pf-v6-c-card', (cards) => {
      const card = cards.find((candidate) => candidate.innerText.includes('Consume this version'))
      if (!card) return false
      card.scrollIntoView({ block: 'center' })
      return card.getBoundingClientRect().height > 0
    }))
  await capture('version-consume-untagged.png')

  // The 2s-cadence scanner should have findings for the seeded vulnerable
  // go module by now; wait on the API before photographing the build.
  await until('the vulnerable package to carry findings', async () => {
    const packages = await api(
      seeded.builderToken,
      'GET',
      `${seeded.compatBase}/buckets/base-images/versions/ubuntu-2404-2026-08/builds/${seeded.ubuntuBuildID}/packages`,
    )
    return JSON.stringify(packages).includes('vuln')
  }, 120000, 2000)
  await page.goto(
    `${base}/buckets/base-images/versions/ubuntu-2404-2026-08/builds/${seeded.ubuntuBuildID}`,
    { waitUntil: 'domcontentloaded' },
  )
  await waitForText('ubuntu-2404-base')
  await capture('build.png')
  await clickByText('button[role="tab"]', 'Packages')
  await waitForText('with findings')
  await until('the findings row to expand', async () => {
    const expander = await page.$('.pf-v6-c-table__compound-expansion-toggle button')
    if (!expander) return false
    await expander.click()
    return true
  })
  await waitForText('GHSA')
  await capture('scanner-findings.png')

  await page.goto(`${base}/audit`, { waitUntil: 'domcontentloaded' })
  await waitForText('dufflebag-docs-shots-audit')
  await capture('audit.png')

  await page.goto(`${base}/encryption`, { waitUntil: 'domcontentloaded' })
  await waitForText('Encryption')
  await waitForText('keyring')
  await capture('encryption.png')

  await page.goto(`${base}/webhooks`, { waitUntil: 'domcontentloaded' })
  await waitForText('ci-notifications')
  await capture('webhooks.png')

  await page.goto(`${base}/instance`, { waitUntil: 'domcontentloaded' })
  await waitForText('HCP_API_ADDRESS')
  await until('the client environment card to be visible', () =>
    page.$$eval('.pf-v6-c-card', (cards) => {
      const card = cards.find((candidate) => candidate.innerText.includes('Client environment'))
      if (!card) return false
      card.scrollIntoView({ block: 'center' })
      return card.getBoundingClientRect().height > 0
    }))
  await capture('instance.png')

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

  // This capture is last because it deliberately replaces the browser's root
  // session. Restore that original session before returning to keep additions
  // after this block independent of the bucket binding.
  await page.type('#client-id', seeded.bucketPrincipal.client_id)
  await page.type('#client-secret', seeded.bucketPrincipal.secret)
  await clickByText('button', 'Log in')
  await waitForText('Sign out')
  await page.goto(`${base}/instance`, { waitUntil: 'domcontentloaded' })
  await waitForText('HCP_PACKER_BUCKET_NAME=base-images')
  await until('the Bucket navigation entry and client environment to be visible', () =>
    page.evaluate(() => {
      const hasBucketNav = [...document.querySelectorAll('nav.app-global-nav a')]
        .some((link) => link.textContent?.trim() === 'Bucket')
      const card = [...document.querySelectorAll('.pf-v6-c-card')]
        .find((candidate) => candidate.textContent?.includes('Client environment'))
      if (!hasBucketNav || !card) return false
      card.scrollIntoView({ block: 'center' })
      return card.getBoundingClientRect().height > 0
    }))
  await capture('instance-bucket-scoped.png')

  await clickByText('button', 'Sign out')
  await waitForText('Log in')
  await page.type('#client-id', seeded.rootCredentials.clientID)
  await page.type('#client-secret', seeded.rootCredentials.clientSecret)
  await clickByText('button', 'Log in')
  await waitForText('Sign out')
}

async function cleanup() {
  if (webhookReceiver) webhookReceiver.close()
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
  await execFile('docker', ['rm', '-f', scannerContainer]).catch(() => {})
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
  const seeded = await seedFixtures(credentials)
  await captureSeededScreens(seeded)
}

try {
  await main()
} finally {
  await cleanup()
}
