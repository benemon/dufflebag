import assert from 'node:assert/strict'
import { execFile as execFileCb, spawn } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import dns from 'node:dns/promises'
import {
  existsSync,
  mkdirSync,
  readFileSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import http from 'node:http'
import https from 'node:https'
import net from 'node:net'
import path from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'

const execFile = promisify(execFileCb)
const serverBinary = process.env.E2E_PACKER_BIN
const work = process.env.E2E_PACKER_WORK
const template = process.env.E2E_PACKER_TEMPLATE
const childTemplate = process.env.E2E_PACKER_CHILD_TEMPLATE
const packer = process.env.E2E_PACKER_CLI
const expectedPackerVersion = process.env.E2E_PACKER_EXPECT_VERSION
const docker = process.env.E2E_PACKER_DOCKER
const hostname = process.env.E2E_PACKER_HOSTNAME
const certFile = process.env.E2E_PACKER_CERT_FILE
const keyFile = process.env.E2E_PACKER_KEY_FILE
const caFile = process.env.E2E_PACKER_CA_FILE
const baseImage = process.env.E2E_PACKER_BASE_IMAGE
const container = `dufflebag-packer-e2e-${process.pid}`
const objectContainer = `dufflebag-packer-e2e-s3-${process.pid}`
// Ceph rather than a lighter stand-in: it is what the lab runs, and RGW's
// quirks — path-style addressing, a user created rather than inherited — are
// the ones worth meeting before a deployment does.
const objectStorageImage = 'quay.io/benjamin_holmes/ceph-aio:v20'
const signingKey = randomBytes(32).toString('hex')
const auditKey = randomBytes(32).toString('hex')
const bucketName = 'dufflebag-sbom-e2e'
// A registry bucket and an S3 bucket are different things that share a word.
// This is the S3 one.
const objectStorageBucket = 'dufflebag-packer-e2e'
const childBucketName = 'dufflebag-derived-e2e'
const organizationName = 'packer-foundation'
const projectName = 'packer-registry'
const principalName = 'packer end-to-end'
const fingerprint = `dufflebag-sbom-${Date.now()}`
const childFingerprint = `${fingerprint}-derived`
const hiddenOrganizationID = '00000000-0000-4000-8000-000000000099'
const hiddenProjectID = '00000000-0000-4000-8000-000000000199'
const hiddenParentBucket = 'tenant-hidden-parent'
const hiddenChildBucket = 'tenant-hidden-child'
const hiddenParentVersionID = '01J00000000000000000000001'
const hiddenChildVersionID = '01J00000000000000000000002'

let serverPort
let server
let serverOutput = ''
let containerStarted = false
let objectContainerStarted = false
let imageLabel

async function command(file, args, options = {}) {
  try {
    return await execFile(file, args, {
      maxBuffer: 64 * 1024 * 1024,
      timeout: 10 * 60 * 1000,
      ...options,
    })
  } catch (err) {
    err.message = [
      `${file} ${args.join(' ')} failed: ${err.message}`,
      err.stdout ? `stdout:\n${err.stdout}` : '',
      err.stderr ? `stderr:\n${err.stderr}` : '',
    ].filter(Boolean).join('\n')
    throw err
  }
}

async function until(what, condition, timeoutMs = 60000, intervalMs = 250) {
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

/**
 * Explains a Ceph that never reported healthy. It needs real disk to bring its
 * OSDs up, so a full Docker VM starves it into a crash loop that looks exactly
 * like an ordinary timeout — an hour of the duf-egk2 audit went into
 * rediscovering that. The container is still running when this fires, so its
 * own healthcheck output and free space are both reachable.
 */
async function objectStorageDiagnosis(container) {
  const lines = []
  for (const [what, args] of [
    ['healthcheck', ['inspect', '-f',
      '{{range .State.Health.Log}}{{printf "%.300s" .Output}}{{end}}', container]],
    ['disk', ['exec', container, 'df', '-h', '/']],
  ]) {
    try {
      const { stdout } = await command(docker, args)
      lines.push(`${what}:\n${stdout.trim() || '(no output)'}`)
    } catch (err) {
      lines.push(`${what} unavailable: ${err.message}`)
    }
  }
  return lines.join('\n')
}

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

function waitForExit(child) {
  return new Promise((resolve) => child.once('exit', (code) => resolve(code)))
}

async function stopServer() {
  if (!server || server.exitCode !== null) return
  const exited = waitForExit(server)
  server.kill('SIGTERM')
  await Promise.race([exited, new Promise((resolve) => setTimeout(resolve, 10000))])
  if (server.exitCode === null) server.kill('SIGKILL')
}

function request(method, requestPath, body, headers = {}) {
  return new Promise((resolve, reject) => {
    const encoded = body === undefined
      ? undefined
      : typeof body === 'string' ? body : JSON.stringify(body)
    const req = https.request({
      hostname,
      port: serverPort,
      path: requestPath,
      method,
      ca: readFileSync(caFile),
      headers: {
        ...headers,
        ...(encoded === undefined ? {} : { 'Content-Length': Buffer.byteLength(encoded) }),
      },
    }, (response) => {
      let responseBody = ''
      const chunks = []
      response.on('data', (chunk) => {
        chunks.push(chunk)
        responseBody += chunk
      })
      response.on('end', () => {
        let json
        if (responseBody) {
          try {
            json = JSON.parse(responseBody)
          } catch {
            json = undefined
          }
        }
        resolve({ status: response.statusCode, body: responseBody, bytes: Buffer.concat(chunks), json })
      })
    })
    req.once('error', reject)
    if (encoded !== undefined) req.write(encoded)
    req.end()
  })
}

async function tokenFor(credentials) {
  const response = await request(
    'POST',
    '/oauth2/token',
    new URLSearchParams({
      grant_type: 'client_credentials',
      audience: 'https://api.hashicorp.cloud',
    }).toString(),
    {
      Authorization: `Basic ${Buffer.from(`${credentials.client_id}:${credentials.client_secret}`).toString('base64')}`,
      'Content-Type': 'application/x-www-form-urlencoded',
    },
  )
  assert.equal(response.status, 200, `token endpoint answered ${response.status}: ${response.body}`)
  assert.ok(response.json?.access_token, 'token endpoint returned no access_token')
  return response.json.access_token
}

async function api(token, method, requestPath, body) {
  const response = await request(method, requestPath, body, {
    Authorization: `Bearer ${token}`,
    ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
  })
  assert.ok(
    response.status >= 200 && response.status < 300,
    `${method} ${requestPath} answered ${response.status}: ${response.body}`,
  )
  return response.json
}

async function vaultPost(requestPath, body) {
  const vaultAddress = process.env.VAULT_ADDR
  const url = new URL(requestPath, `${vaultAddress.replace(/\/$/, '')}/`)
  const headers = {
    'Content-Type': 'application/json',
    'X-Vault-Token': process.env.VAULT_TOKEN,
    // The server under test reaches its namespaced transit through the SDK's
    // ambient VAULT_NAMESPACE; this direct operator call must name the same
    // namespace or it lands at root and is refused.
    ...(process.env.VAULT_NAMESPACE
      ? { 'X-Vault-Namespace': process.env.VAULT_NAMESPACE }
      : {}),
  }
  const encoded = JSON.stringify(body)

  if (url.protocol !== 'https:' || !process.env.VAULT_CACERT) {
    const response = await fetch(url, { method: 'POST', headers, body: encoded })
    const text = await response.text()
    assert.ok(response.ok, `POST ${url.pathname} answered ${response.status}: ${text}`)
    return
  }

  const response = await new Promise((resolve, reject) => {
    const req = https.request(url, {
      method: 'POST',
      ca: readFileSync(process.env.VAULT_CACERT),
      headers: { ...headers, 'Content-Length': Buffer.byteLength(encoded) },
    }, (result) => {
      let responseBody = ''
      result.on('data', (chunk) => (responseBody += chunk))
      result.on('end', () => resolve({ status: result.statusCode, body: responseBody }))
    })
    req.once('error', reject)
    req.write(encoded)
    req.end()
  })
  assert.ok(
    response.status >= 200 && response.status < 300,
    `POST ${url.pathname} answered ${response.status}: ${response.body}`,
  )
}

async function assertPreconditions() {
  const missingTLSFiles = [
    ['lab TLS certificate', certFile],
    ['lab TLS private key', keyFile],
    ['lab CA chain', caFile],
  ].filter(([, file]) => !existsSync(file))
  assert.deepEqual(
    missingTLSFiles,
    [],
    `PRECONDITION: missing lab TLS files: ${missingTLSFiles.map(([name, file]) => `${name} at ${file}`).join(', ')}`,
  )

  if (expectedPackerVersion) {
    let packerVersion
    try {
      packerVersion = await command(packer, ['version'])
    } catch (err) {
      throw new Error(`PRECONDITION: expected Packer v${expectedPackerVersion}\n${err.message}`)
    }
    const actualPackerVersion = packerVersion.stdout.trim()
    assert.ok(
      actualPackerVersion.split(/\r?\n/).includes(`Packer v${expectedPackerVersion}`),
      `PRECONDITION: expected Packer v${expectedPackerVersion}; got ${actualPackerVersion}`,
    )
    process.stdout.write(`PRECONDITION stock Packer: ${actualPackerVersion}\n`)
  }

  try {
    await command(docker, ['info', '--format', '{{.ServerVersion}}'])
    await command(docker, ['image', 'inspect', baseImage])
  } catch (err) {
    throw new Error(
      `PRECONDITION: a reachable Docker daemon with local image ${baseImage} is required\n${err.message}`,
    )
  }
  process.stdout.write(`PRECONDITION Docker image: ${baseImage}\n`)

  let addresses
  try {
    addresses = await dns.lookup(hostname, { all: true })
  } catch (err) {
    throw new Error(`PRECONDITION: ${hostname} must resolve to loopback\n${err.message}`)
  }
  assert.ok(
    addresses.some(({ address }) => address === '127.0.0.1' || address === '::1'),
    `PRECONDITION: ${hostname} must resolve to loopback; got ${addresses.map(({ address }) => address).join(', ')}`,
  )
  process.stdout.write(`PRECONDITION lab DNS: ${hostname} -> loopback\n`)

  try {
    await command('openssl', [
      'verify', '-no-CApath', '-no-CAstore',
      '-CAfile', caFile, '-untrusted', certFile, certFile,
    ])
    await command('openssl', ['x509', '-in', certFile, '-checkhost', hostname, '-noout'])
    await command('openssl', ['x509', '-in', certFile, '-checkend', '0', '-noout'])
    const certificateKey = await command('openssl', ['x509', '-in', certFile, '-pubkey', '-noout'])
    const privateKey = await command('openssl', ['pkey', '-in', keyFile, '-pubout'])
    assert.equal(
      privateKey.stdout,
      certificateKey.stdout,
      'PRECONDITION: lab TLS certificate and private key do not match',
    )
  } catch (err) {
    throw new Error(`PRECONDITION: the provisioned lab TLS chain must be valid\n${err.message}`)
  }
  process.stdout.write(`PRECONDITION lab TLS: ${certFile} verified by ${caFile}\n`)
}

async function removeBuiltImages() {
  if (!imageLabel) return
  try {
    const { stdout } = await command(docker, [
      'images', '-a', '-q', '--filter', `label=dev.dufflebag.e2e=${imageLabel}`,
    ])
    const images = [...new Set(stdout.trim().split('\n').filter(Boolean))]
    if (images.length > 0) await command(docker, ['image', 'rm', '-f', ...images])
  } catch {
    // Preserve the primary failure; teardown is asserted below on a green run.
  }
}

test('stock Packer publishes registry metadata with paired file audit records', async (t) => {
  t.after(async () => {
    await stopServer()
    await removeBuiltImages()
    if (containerStarted) await command(docker, ['rm', '-f', container]).catch(() => {})
    if (objectContainerStarted) await command(docker, ['rm', '-f', objectContainer]).catch(() => {})
  })

  await assertPreconditions()

  await command(docker, [
    'run', '-d', '--rm', '--name', container,
    '-e', 'POSTGRES_PASSWORD=postgres',
    '-e', 'POSTGRES_DB=dufflebag',
    '-p', '127.0.0.1::5432',
    'postgres:17-alpine',
  ])
  containerStarted = true
  const { stdout: portLine } = await command(docker, ['port', container, '5432/tcp'])
  const pgPort = portLine.trim().split('\n')[0].split(':').pop()
  await until('Postgres to accept connections', async () => {
    await command(docker, ['exec', container, 'pg_isready', '-U', 'postgres', '-d', 'dufflebag'])
    return true
  })

  await command(docker, [
    'run', '-d', '--rm', '--name', objectContainer,
    '-p', '127.0.0.1::8000',
    objectStorageImage,
  ])
  objectContainerStarted = true
  const { stdout: objectPortLine } = await command(docker, ['port', objectContainer, '8000/tcp'])
  const objectPort = objectPortLine.trim().split('\n')[0].split(':').pop()
  // The image carries its own healthcheck, which is the only thing that knows
  // when Ceph is ready. Probing the published port is not enough and fails in
  // two distinct ways: docker accepts the connection before RGW listens, and
  // RGW serves HTTP before the cluster can run an admin command.
  try {
    await until('Ceph to report healthy', async () => {
      const { stdout } = await command(docker, [
        'inspect', '-f', '{{.State.Health.Status}}', objectContainer,
      ])
      return stdout.trim() === 'healthy'
    }, 300000, 2000)
  } catch (err) {
    throw new Error(`${err.message}\n${await objectStorageDiagnosis(objectContainer)}`)
  }
  // RGW has no root credential to inherit; the user is created.
  await command(docker, [
    'exec', objectContainer, 'radosgw-admin', 'user', 'create',
    '--uid=dufflebag-e2e', '--display-name=dufflebag e2e',
    '--access-key=testaccess', '--secret-key=testsecret',
  ])
  await command(docker, [
    'cp', fileURLToPath(new URL('../support/create-bucket.py', import.meta.url)),
    `${objectContainer}:/tmp/create-bucket.py`,
  ])
  await command(docker, [
    'exec', objectContainer, 'python3', '/tmp/create-bucket.py',
    'testaccess', 'testsecret', objectStorageBucket,
  ])

  const adminURL = `postgres://postgres:postgres@127.0.0.1:${pgPort}/dufflebag?sslmode=disable`
  serverOutput = ''
  // Under the encrypted posture the env signing key is refused before the
  // migrate-then-refuse sequence can run (ADR-0024), so it is omitted there.
  const migrator = startServer({
    DFBG_DATABASE_URL: adminURL,
    ...(process.env.DFBG_KEY_PROVIDER ? {} : { DFBG_TOKEN_SIGNING_KEY: signingKey }),
  })
  const migrationExit = await Promise.race([
    waitForExit(migrator),
    new Promise((_, reject) => setTimeout(() => reject(new Error('migration process did not exit')), 60000)),
  ])
  assert.notEqual(migrationExit, 0, 'migration process served with the PostgreSQL superuser')
  assert.match(serverOutput, /refusing to serve/, 'migration process did not apply migrations then refuse the superuser')

  await command(docker, [
    'exec', container, 'psql', '-v', 'ON_ERROR_STOP=1',
    '-U', 'postgres', '-d', 'dufflebag',
    '-c', "CREATE ROLE dufflebag_app LOGIN PASSWORD 'app' NOSUPERUSER NOBYPASSRLS",
    '-c', 'GRANT CONNECT ON DATABASE dufflebag TO dufflebag_app',
    '-c', 'GRANT USAGE ON SCHEMA public TO dufflebag_app',
    '-c', 'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dufflebag_app',
  ])

  serverPort = await freePort()
  const endpoint = `https://${hostname}:${serverPort}`
  const auditFile = path.join(work, 'packer-audit.jsonl')
  serverOutput = ''
  // With a key provider in the caller's environment (DFBG_KEY_PROVIDER plus
  // VAULT_* passed through process.env) the lane runs the ENCRYPTED posture:
  // the signing and audit keys live in the wrapped keyring, and passing env
  // copies would be refused at startup (ADR-0024).
  const encrypted = Boolean(process.env.DFBG_KEY_PROVIDER)
  const servingEnv = {
    DFBG_DATABASE_URL: `postgres://dufflebag_app:app@127.0.0.1:${pgPort}/dufflebag?sslmode=disable`,
    DFBG_HTTP_ADDR: `127.0.0.1:${serverPort}`,
    ...(encrypted ? {} : {
      DFBG_TOKEN_SIGNING_KEY: signingKey,
      DFBG_AUDIT_HMAC_KEY: auditKey,
      DFBG_AUDIT_HMAC_KEY_VERSION: 'packer-e2e-v1',
    }),
    DFBG_TOKEN_ISSUER: endpoint,
    DFBG_TLS_CERT_FILE: certFile,
    DFBG_TLS_KEY_FILE: keyFile,
    DFBG_OBJECT_STORAGE_ENDPOINT: `http://127.0.0.1:${objectPort}`,
    DFBG_OBJECT_STORAGE_REGION: 'us-east-1',
    DFBG_OBJECT_STORAGE_BUCKET: objectStorageBucket,
    DFBG_OBJECT_STORAGE_ACCESS_KEY: 'testaccess',
    DFBG_OBJECT_STORAGE_SECRET_KEY: 'testsecret',
  }
  server = startServer(servingEnv)
  await until('the trusted TLS server to answer', async () => {
    if (server.exitCode !== null) throw new Error(`server exited ${server.exitCode}: ${serverOutput}`)
    return (await request('GET', '/')).status === 200
  })

  const initialized = await request('POST', '/sys/init')
  assert.equal(initialized.status, 200, `POST /sys/init answered ${initialized.status}: ${initialized.body}`)
  assert.ok(initialized.json?.client_id && initialized.json?.client_secret, 'POST /sys/init returned no credentials')
  const rootToken = await tokenFor(initialized.json)

  process.stdout.write(
    `ASSERT deployment-configured object storage: http://127.0.0.1:${objectPort}/dufflebag-packer-e2e\n`,
  )

  const auditTarget = await api(rootToken, 'POST', '/api/v1/audit/targets', { path: auditFile })
  assert.equal(auditTarget.path, auditFile)
  assert.equal(auditTarget.status, 'healthy')
  assert.ok(existsSync(auditFile), `configured audit target did not create ${auditFile}`)
  assert.equal(statSync(auditFile).mode & 0o777, 0o600, 'configured audit target is not mode 0600')
  process.stdout.write(`ASSERT configured audit target: ${auditFile} (healthy, mode 0600)\n`)

  const organization = await api(rootToken, 'POST', '/api/v1/organizations', { name: organizationName })
  const project = await api(
    rootToken,
    'POST',
    `/api/v1/organizations/${organization.id}/projects`,
    { name: projectName },
  )
  const principal = await api(rootToken, 'POST', '/api/v1/principals', {
    name: principalName,
    role: 'builder',
    organization_id: organization.id,
    project_id: project.id,
  })
  assert.equal(principal.role, 'builder')
  assert.equal(principal.organization_id, organization.id)
  assert.equal(principal.project_id, project.id)
  assert.equal(principal.secret, undefined, 'CreatePrincipal returned a secret instead of using issuance')
  const issued = await api(rootToken, 'POST', `/api/v1/principals/${principal.id}/secrets`, {})
  assert.ok(issued.secret, 'ordinary secret issuance returned no one-time secret')
  const principalToken = await tokenFor({
    client_id: principal.client_id,
    client_secret: issued.secret,
  })
  process.stdout.write(`ASSERT ordinary service-principal bootstrap: ${principalName} (project-scoped builder)\n`)

  const home = path.join(work, 'home')
  mkdirSync(home, { recursive: true })
  const goPath = (await command('go', ['env', 'GOPATH'])).stdout.trim()
  const goModuleCache = (await command('go', ['env', 'GOMODCACHE'])).stdout.trim()
  const expectedCache = path.join(home, '.config', 'hcp', 'cred_cache.json')
  assert.ok(
    expectedCache.startsWith(`${work}${path.sep}`),
    `isolated HOME escaped the gate work directory: ${home}`,
  )
  // HOME alone does not isolate Packer on Linux: it resolves its plugin and
  // config directories through XDG_CONFIG_HOME, which the environment may
  // already set — a CI runner does. Left unset, the gate installs the Docker
  // plugin into the caller's real config directory and reads whatever is
  // already there, which is how a second run in one job first failed here.
  const configHome = path.join(home, '.config')
  const cacheHome = path.join(home, '.cache')
  const packerEnv = {
    ...process.env,
    HOME: home,
    XDG_CONFIG_HOME: configHome,
    XDG_CACHE_HOME: cacheHome,
    GOPATH: goPath,
    GOMODCACHE: goModuleCache,
    CHECKPOINT_DISABLE: '1',
    SSL_CERT_FILE: caFile,
    HCP_AUTH_URL: endpoint,
    HCP_API_ADDRESS: `${hostname}:${serverPort}`,
    HCP_CLIENT_ID: principal.client_id,
    HCP_CLIENT_SECRET: issued.secret,
    HCP_ORGANIZATION_ID: organization.id,
    HCP_PROJECT_ID: project.id,
    HCP_SKIP_STATUS_CHECK: 'true',
    HCP_PACKER_BUILD_FINGERPRINT: fingerprint,
  }
  delete packerEnv.HCP_AUTH_TLS
  delete packerEnv.HCP_API_TLS
  delete packerEnv.HCP_OAUTH_CLIENT_ID
  process.stdout.write(`ASSERT isolated Packer HOME: ${home}\n`)

  const generated = await command(packer, ['sbom-generate', '-o', 'spdx-json', '.'], {
    cwd: process.cwd(),
    env: packerEnv,
  })
  const sbomSource = path.join(work, 'dufflebag.spdx.json')
  writeFileSync(sbomSource, generated.stdout, { mode: 0o600 })
  const generatedSBOM = JSON.parse(readFileSync(sbomSource, 'utf8'))
  assert.match(generatedSBOM.spdxVersion, /^SPDX-2\./, 'Packer did not produce an SPDX document')

  const variables = [
    '-var', `base_image=${baseImage}`,
    '-var', `run_label=${fingerprint}`,
    '-var', `sbom_source=${sbomSource}`,
  ]
  const required = await command(packer, ['plugins', 'required', template], { env: packerEnv })
  const requiredOutput = `${required.stdout}${required.stderr}`
  assert.equal(
    requiredOutput.trim(),
    'docker github.com/hashicorp/docker "= 1.1.4"',
    `expected only Docker plugin 1.1.4; got:\n${requiredOutput}`,
  )
  process.stdout.write(`ASSERT required plugin set: docker only\n${requiredOutput}`)

  const initializedPlugins = await command(packer, ['init', template], { env: packerEnv })
  process.stdout.write(`${initializedPlugins.stdout}${initializedPlugins.stderr}`)
  // Observed, not assumed: the plugin has to land inside the gate's own
  // directory. Computing an isolated path proves nothing about where Packer
  // actually wrote, and the difference is what let a leaked plugin survive a
  // run and break the next one.
  const installedPlugins = path.join(configHome, 'packer', 'plugins')
  assert.ok(
    existsSync(installedPlugins),
    `Packer installed its plugins outside the isolated home: expected ${installedPlugins}`,
  )
  process.stdout.write(`ASSERT isolated Packer plugins: ${installedPlugins}\n`)
  const validated = await command(packer, ['validate', ...variables, template], { env: packerEnv })
  process.stdout.write(`${validated.stdout}${validated.stderr}`)

  imageLabel = fingerprint
  const build = await command(packer, ['build', '-color=false', ...variables, template], {
    env: { ...packerEnv, PACKER_LOG: '1' },
  })
  const buildOutput = `${build.stdout}${build.stderr}`
  process.stdout.write(buildOutput)
  assert.match(
    buildOutput,
    /Published metadata to HCP Packer registry/,
    'real Packer did not report registry publication',
  )

  const registryBase = `/packer/2023-01-01/organizations/${organization.id}/projects/${project.id}`
  const published = await api(
    principalToken,
    'GET',
    `${registryBase}/buckets/${bucketName}/versions/${fingerprint}`,
  )
  assert.equal(published.version.author_id, principal.id, 'real Packer version author did not match its principal')
  assert.equal(published.version.builds.length, 1, 'real Packer version did not return its build')
  const publishedMetadata = published.version.builds[0].metadata
  assert.ok(publishedMetadata?.packer, 'real Packer build metadata did not survive the registry read')
  for (const field of ['options', 'os', 'plugins']) {
    assert.ok(
      Object.hasOwn(publishedMetadata.packer, field),
      `real Packer metadata.packer omitted ${field}: ${JSON.stringify(publishedMetadata)}`,
    )
  }
  process.stdout.write(
    `ASSERT real Packer registry projection: author_id=${published.version.author_id} ` +
    `metadata.packer keys=${Object.keys(publishedMetadata.packer).sort().join(',')}\n`,
  )

  const bucketDetail = await api(
    principalToken,
    'GET',
    `${registryBase}/buckets/${bucketName}`,
  )
  const bucketList = await api(principalToken, 'GET', `${registryBase}/buckets`)
  const listedBucket = bucketList.buckets.find((bucket) => bucket.name === bucketName)
  for (const [read, bucket] of [['GetBucket', bucketDetail.bucket], ['ListBuckets', listedBucket]]) {
    assert.ok(bucket, `${read} omitted the real Packer bucket`)
    assert.equal(
      bucket.latest_version?.id,
      published.version.id,
      `${read} did not report the real Packer bucket's newest version`,
    )
    assert.equal(bucket.latest_version?.fingerprint, fingerprint, `${read} newest fingerprint differs`)
    assert.equal(bucket.latest_version?.name, 'v1', `${read} newest version name differs`)
    assert.equal(bucket.latest_version?.builds.length, 1, `${read} newest version omitted its build`)
    assert.deepEqual(bucket.platforms, ['docker'], `${read} platforms did not come from the newest version`)
    assert.equal(bucket.parents, undefined, `${read} invented parents before ancestry existed`)
    assert.equal(bucket.children, undefined, `${read} invented children before ancestry existed`)
  }
  process.stdout.write(
    `ASSERT real Packer bucket projection: GetBucket and ListBuckets latest_version=v1 ` +
    `platforms=docker\n`,
  )

  const publishedBuildID = published.version.builds[0].id
  const sbomReadBase = `${registryBase}/buckets/${bucketName}/versions/${fingerprint}/builds/${publishedBuildID}`
  const listedSBOMs = await api(principalToken, 'GET', `${sbomReadBase}/sboms`)
  assert.equal(listedSBOMs.sboms.length, 1, `SBOM read returned ${JSON.stringify(listedSBOMs)}`)
  assert.equal(listedSBOMs.sboms[0].name, fingerprint, 'SBOM read did not preserve the defaulted name')
  assert.equal(listedSBOMs.sboms[0].format, 'SPDX', 'SBOM read did not preserve the real format')
  const sbomDownload = await api(
    principalToken,
    'GET',
    `${sbomReadBase}/sboms/${encodeURIComponent(listedSBOMs.sboms[0].name)}`,
  )
  const downloadPath = new URL(sbomDownload.download_url).pathname
  const downloadedSBOM = await request('GET', downloadPath, undefined, {
    Authorization: `Bearer ${principalToken}`,
  })
  assert.equal(downloadedSBOM.status, 200, `SBOM proxy download answered ${downloadedSBOM.status}`)
  // The observed wire (live HCP, probed 2026-08-08): the download is the
  // DECOMPRESSED document, byte-identical to what the provisioner read — not
  // the zstd envelope it travels in. An earlier revision asserted the zstd
  // magic here, which pinned the storage detail instead of the contract.
  assert.ok(downloadedSBOM.bytes.length > 4, 'SBOM proxy returned no object bytes')
  const downloadedDocument = JSON.parse(downloadedSBOM.bytes.toString('utf8'))
  assert.equal(downloadedDocument.SPDXID, generatedSBOM.SPDXID, 'download is not the uploaded SBOM document')
  assert.deepEqual(downloadedDocument, generatedSBOM, 'download must be byte-faithful to the generated document')

  await t.test('the encrypted keyring rotates without losing retained payloads', {
    skip: encrypted ? false : 'requires DFBG_KEY_PROVIDER and the lab Vault environment',
  }, async () => {
    assert.ok(process.env.VAULT_ADDR, 'encrypted posture has no VAULT_ADDR')
    assert.ok(process.env.VAULT_TOKEN, 'encrypted posture has no VAULT_TOKEN')
    const transitMount = (process.env.DFBG_VAULT_TRANSIT_MOUNT || 'transit')
      .replace(/^\/+|\/+$/g, '')
    const transitKey = process.env.DFBG_VAULT_TRANSIT_KEY || 'dufflebag'
    const before = await api(rootToken, 'GET', '/api/v1/encryption')
    const beforeRefs = new Map(before.keyring.map((entry) => [
      `${entry.purpose}:${entry.version}`, entry.kek_ref,
    ]))

    await vaultPost(
      `/v1/${transitMount}/keys/${encodeURIComponent(transitKey)}/rotate`,
      {},
    )
    const rewrapResponse = await request('POST', '/api/v1/encryption/rewrap', undefined, {
      Authorization: `Bearer ${rootToken}`,
    })
    assert.equal(
      rewrapResponse.status, 200,
      `POST /api/v1/encryption/rewrap answered ${rewrapResponse.status}: ${rewrapResponse.body}`,
    )
    const rewrapped = rewrapResponse.json
    assert.equal(rewrapped.keyring.length, before.keyring.length)
    for (const entry of rewrapped.keyring) {
      const previous = beforeRefs.get(`${entry.purpose}:${entry.version}`)
      assert.ok(previous, `rewrap returned an unknown keyring row: ${JSON.stringify(entry)}`)
      const previousVersion = Number.parseInt(previous.slice(1), 10)
      const currentVersion = Number.parseInt(entry.kek_ref.slice(1), 10)
      assert.ok(
        Number.isInteger(previousVersion) && Number.isInteger(currentVersion) &&
          currentVersion > previousVersion,
        `${entry.purpose} v${entry.version} did not advance past KEK ${previous}`,
      )
    }

    const rotateResponse = await request('POST', '/api/v1/encryption/rotate', undefined, {
      Authorization: `Bearer ${rootToken}`,
    })
    assert.equal(
      rotateResponse.status, 200,
      `POST /api/v1/encryption/rotate answered ${rotateResponse.status}: ${rotateResponse.body}`,
    )
    const rotated = rotateResponse.json
    const purposes = ['audit_hmac', 'integrity', 'payload', 'token_signing']
    for (const purpose of purposes) {
      assert.deepEqual(
        rotated.keyring.filter((entry) => entry.purpose === purpose)
          .map((entry) => entry.version).sort(),
        [1, 2],
        `${purpose} did not retain both key versions`,
      )
    }

    const retainedVersion = await api(
      principalToken,
      'GET',
      `${registryBase}/buckets/${bucketName}/versions/${fingerprint}`,
    )
    assert.equal(retainedVersion.version.id, published.version.id)
    const retainedSBOM = await request('GET', downloadPath, undefined, {
      Authorization: `Bearer ${principalToken}`,
    })
    assert.equal(retainedSBOM.status, 200, `retained SBOM answered ${retainedSBOM.status}`)
    assert.deepEqual(
      JSON.parse(retainedSBOM.bytes.toString('utf8')), generatedSBOM,
      'retained SBOM download must stay the document across key rotation',
    )

    await stopServer()
    serverOutput = ''
    server = startServer(servingEnv)
    const health = await until('the restarted encrypted server to report healthy', async () => {
      if (server.exitCode !== null) {
        throw new Error(`server exited ${server.exitCode}: ${serverOutput}`)
      }
      const response = await request('GET', '/sys/health')
      return response.status === 200 && response.json?.encryption === 'ok' ? response : false
    })
    assert.equal(health.json.encryption, 'ok')
  })

  const describedIDs = new Set((generatedSBOM.relationships ?? [])
    .filter((relationship) =>
      relationship.relationshipType === 'DESCRIBES' &&
      relationship.spdxElementId === generatedSBOM.SPDXID)
    .map((relationship) => relationship.relatedSpdxElement))
  const expectedByIdentity = new Map()
  for (const pkg of generatedSBOM.packages ?? []) {
    if (describedIDs.has(pkg.SPDXID)) continue
    const purl = (pkg.externalRefs ?? []).find(
      (reference) => reference.referenceType?.toLowerCase() === 'purl',
    )?.referenceLocator ?? ''
    const projected = { name: pkg.name ?? '', version: pkg.versionInfo ?? '', purl }
    expectedByIdentity.set(JSON.stringify(projected), projected)
  }
  const byIdentity = (left, right) =>
    left.name.localeCompare(right.name) ||
    left.version.localeCompare(right.version) ||
    left.purl.localeCompare(right.purl)
  const expectedPackages = [...expectedByIdentity.values()].sort(byIdentity)
  assert.ok(describedIDs.size > 0, 'real Packer SPDX document had no DESCRIBES target to test')
  assert.ok(expectedPackages.length > 1, 'real Packer SPDX document was not a multi-package oracle')

  const actualPackages = []
  let nextPageToken = ''
  do {
    const query = new URLSearchParams({ 'pagination.page_size': '37' })
    if (nextPageToken) query.set('pagination.next_page_token', nextPageToken)
    const page = await api(principalToken, 'GET', `${sbomReadBase}/packages?${query}`)
    for (const pkg of page.packages) {
      assert.equal(
        Object.hasOwn(pkg, 'vuln_details'),
        false,
        `unscanned real package claimed vulnerability detail: ${JSON.stringify(pkg)}`,
      )
      assert.deepEqual(
        pkg.sboms,
        [listedSBOMs.sboms[0]],
        `real package lost its SBOM source: ${JSON.stringify(pkg)}`,
      )
      actualPackages.push({
        name: pkg.name ?? '', version: pkg.version ?? '', purl: pkg.purl ?? '',
      })
    }
    nextPageToken = page.pagination?.next_page_token ?? ''
  } while (nextPageToken)
  actualPackages.sort(byIdentity)
  assert.deepEqual(
    actualPackages,
    expectedPackages,
    'packages read did not equal the actual contents of Packer\'s generated SPDX document',
  )
  process.stdout.write(
    `ASSERT real Packer package projection: ${actualPackages.length} package identities ` +
    `from ${generatedSBOM.packages.length} SPDX records; ${describedIDs.size} DESCRIBES self-entry excluded; ` +
    `paginated read and SBOM attribution verified\n`,
  )

  const childEnv = {
    ...packerEnv,
    HCP_PACKER_BUILD_FINGERPRINT: childFingerprint,
  }
  const childVariables = ['-var', `run_label=${fingerprint}`]
  const initializedChildPlugins = await command(packer, ['init', childTemplate], { env: childEnv })
  process.stdout.write(`${initializedChildPlugins.stdout}${initializedChildPlugins.stderr}`)
  const validatedChild = await command(
    packer,
    ['validate', ...childVariables, childTemplate],
    { env: childEnv },
  )
  process.stdout.write(`${validatedChild.stdout}${validatedChild.stderr}`)
  const childBuild = await command(
    packer,
    ['build', '-color=false', ...childVariables, childTemplate],
    { env: { ...childEnv, PACKER_LOG: '1' } },
  )
  const childBuildOutput = `${childBuild.stdout}${childBuild.stderr}`
  process.stdout.write(childBuildOutput)
  assert.match(
    childBuildOutput,
    /Published metadata to HCP Packer registry/,
    'real Packer did not publish the child registry metadata',
  )

  const projectedParent = await api(
    principalToken,
    'GET',
    `${registryBase}/buckets/${bucketName}/versions/${fingerprint}`,
  )
  const projectedChild = await api(
    principalToken,
    'GET',
    `${registryBase}/buckets/${childBucketName}/versions/${childFingerprint}`,
  )
  const parentArtifactIdentifier = published.version.builds[0].artifacts[0].external_identifier
  assert.equal(
    projectedChild.version.builds[0].source_external_identifier,
    parentArtifactIdentifier,
    'real child build did not report the parent artifact identifier as its source',
  )
  const persistedParentFields = await command(docker, [
    'exec', container, 'psql', '-v', 'ON_ERROR_STOP=1', '-At',
    '-U', 'postgres', '-d', 'dufflebag', '-c', `
      SELECT json_build_object(
        'parent_version_id', b.parent_version_id,
        'parent_channel_id', b.parent_channel_id
      )::text
      FROM builds b
      JOIN versions v ON v.id = b.version_id
      WHERE v.fingerprint = '${childFingerprint}'`,
  ])
  const parentFields = JSON.parse(persistedParentFields.stdout.trim())
  const parentChannel = await api(
    principalToken,
    'GET',
    `${registryBase}/buckets/${bucketName}/channels/latest`,
  )
  assert.deepEqual(
    parentFields,
    {
      parent_version_id: published.version.id,
      parent_channel_id: parentChannel.channel.id,
    },
    'terminal child update did not persist stock Packer\'s declared parent IDs',
  )
  assert.equal(projectedParent.version.has_descendants, true, 'parent version did not derive has_descendants')
  assert.ok(projectedChild.version.parents, 'child version did not project parents')
  assert.match(
    projectedChild.version.parents?.href ?? '',
    /\/buckets\/dufflebag-derived-e2e\/ancestry\?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=/,
    'child version parents did not link to its ancestry read',
  )

  const relatedBucketDetail = await Promise.all([
    api(principalToken, 'GET', `${registryBase}/buckets/${bucketName}`),
    api(principalToken, 'GET', `${registryBase}/buckets/${childBucketName}`),
  ])
  const relatedBucketList = await api(principalToken, 'GET', `${registryBase}/buckets`)
  const relatedBuckets = new Map(
    relatedBucketList.buckets.map((bucket) => [bucket.name, bucket]),
  )
  const parentChildrenHref = `${registryBase}/buckets/${bucketName}/ancestry?type=ANCESTRY_TYPE_CHILDREN`
  const childParentsHref = `${registryBase}/buckets/${childBucketName}/ancestry?type=ANCESTRY_TYPE_PARENTS`
  for (const [read, parentBucketProjection, childBucketProjection] of [
    ['GetBucket', relatedBucketDetail[0].bucket, relatedBucketDetail[1].bucket],
    ['ListBuckets', relatedBuckets.get(bucketName), relatedBuckets.get(childBucketName)],
  ]) {
    assert.deepEqual(
      parentBucketProjection.children,
      { href: parentChildrenHref, status: 'UP_TO_DATE' },
      `${read} parent bucket did not project its child status`,
    )
    assert.equal(parentBucketProjection.parents, undefined, `${read} parent bucket invented parents`)
    assert.deepEqual(
      childBucketProjection.parents,
      { href: childParentsHref, status: 'UP_TO_DATE' },
      `${read} child bucket did not project its parent status`,
    )
    assert.equal(childBucketProjection.children, undefined, `${read} child bucket invented children`)
  }
  process.stdout.write(
    'ASSERT real Packer bucket ancestry projection: GetBucket and ListBuckets report ' +
    'parent children=UP_TO_DATE, child parents=UP_TO_DATE, and absent sides are omitted\n',
  )

  await command(docker, [
    'exec', container, 'psql', '-v', 'ON_ERROR_STOP=1',
    '-U', 'postgres', '-d', 'dufflebag', '-c', `
      INSERT INTO organizations (id, name, created_at)
      VALUES ('${hiddenOrganizationID}', 'tenant-hidden', now());
      INSERT INTO projects (id, organization_id, name, created_at)
      VALUES ('${hiddenProjectID}', '${hiddenOrganizationID}', 'tenant-hidden', now());
      INSERT INTO buckets (
        organization_id, project_id, id, name, description, labels, created_at, updated_at
      ) VALUES
        ('${hiddenOrganizationID}', '${hiddenProjectID}', '01J00000000000000000000011',
         '${hiddenParentBucket}', '', '{}', now(), now()),
        ('${hiddenOrganizationID}', '${hiddenProjectID}', '01J00000000000000000000012',
         '${hiddenChildBucket}', '', '{}', now(), now());
      INSERT INTO versions (
        organization_id, project_id, id, bucket_id, fingerprint, template_type,
        complete, sequence, created_at, updated_at, author_id
      ) VALUES
        ('${hiddenOrganizationID}', '${hiddenProjectID}', '${hiddenParentVersionID}',
         '01J00000000000000000000011', 'hidden-parent', 'HCL2', true, 1, now(), now(), ''),
        ('${hiddenOrganizationID}', '${hiddenProjectID}', '${hiddenChildVersionID}',
         '01J00000000000000000000012', 'hidden-child', 'HCL2', true, 1, now(), now(), '');
      INSERT INTO builds (
        organization_id, project_id, id, version_id, component_type, status,
        platform, metadata_seen, packer_run_uuid, labels, source_external_identifier,
        parent_version_id, parent_channel_id, metadata, created_at, updated_at
      ) VALUES
        ('${hiddenOrganizationID}', '${hiddenProjectID}', '01J00000000000000000000021',
         '${hiddenChildVersionID}', 'hidden-child', 'done', 'docker', true, '', '{}', '',
         '${published.version.id}', NULL, '{}', now(), now()),
        ('${organization.id}', '${project.id}', '01J00000000000000000000022',
         '${projectedChild.version.id}', 'tenant-isolation-probe', 'done', 'docker', true, '', '{}', '',
         '${hiddenParentVersionID}', NULL, '{}', now(), now());`,
  ])
  const parentAncestry = await api(
    principalToken,
    'GET',
    `${registryBase}/buckets/${bucketName}/ancestry?type=ANCESTRY_TYPE_CHILDREN`,
  )
  const childAncestry = await api(
    principalToken,
    'GET',
    `${registryBase}/buckets/${childBucketName}/ancestry?type=ANCESTRY_TYPE_PARENTS&version_fingerprint=${childFingerprint}`,
  )
  const parentRelation = parentAncestry.relations.find((relation) =>
    relation.parent.version_id === published.version.id &&
    relation.child.version_id === projectedChild.version.id)
  const childRelation = childAncestry.relations.find((relation) =>
    relation.parent.version_id === published.version.id &&
    relation.child.version_id === projectedChild.version.id)
  assert.ok(
    parentRelation,
    `parent ancestry omitted the real child: ${JSON.stringify(parentAncestry)}`,
  )
  assert.ok(
    childRelation,
    `child ancestry omitted the real parent: ${JSON.stringify(childAncestry)}`,
  )
  assert.equal(parentRelation.status, 'UP_TO_DATE', 'parent ancestry relation status was not UP_TO_DATE')
  assert.equal(childRelation.status, 'UP_TO_DATE', 'child ancestry relation status was not UP_TO_DATE')
  const leakedRelations = [...parentAncestry.relations, ...childAncestry.relations].filter(
    (relation) => relation.parent.bucket_name === hiddenParentBucket ||
      relation.child.bucket_name === hiddenChildBucket,
  )
  assert.deepEqual(
    leakedRelations,
    [],
    `tenant isolation: ancestry exposed a hidden parent or child: ${JSON.stringify(leakedRelations)}`,
  )
  assert.equal(projectedChild.version.author_id, principal.id, 'derived version author did not match its principal')
  process.stdout.write(
    'ASSERT real Packer ancestry: parent reports descendant; child reports parent; ' +
    'both relations UP_TO_DATE; source identifier matches artifact; declared parent IDs persisted; ' +
    'another tenant\'s parent and child are absent\n',
  )

  const query = `
    SELECT json_build_object(
      'fingerprint', v.fingerprint,
      'complete', v.complete,
      'status', b.status,
      'name', s.name,
      'format', s.format,
      'object_key', s.object_key,
      'parse_status', s.parse_status,
      'columns_that_could_hold_bytes_or_credentials',
        (SELECT count(*) FROM information_schema.columns
         WHERE table_schema = 'public'
           AND column_name IN ('compressed_data', 'secret_key', 'access_key'))
    )::text
    FROM sboms s
    JOIN builds b
      ON b.organization_id = s.organization_id
     AND b.project_id = s.project_id
     AND b.id = s.build_id
    JOIN versions v
      ON v.organization_id = b.organization_id
     AND v.project_id = b.project_id
     AND v.id = b.version_id
    WHERE v.fingerprint = '${fingerprint}'`
  const persisted = await command(docker, [
    'exec', container, 'psql', '-v', 'ON_ERROR_STOP=1', '-At',
    '-U', 'postgres', '-d', 'dufflebag', '-c', query,
  ])
  const rows = persisted.stdout.trim().split('\n').filter(Boolean)
  assert.equal(rows.length, 1, `expected one persisted SBOM row, got ${rows.length}: ${persisted.stdout}`)
  const sbom = JSON.parse(rows[0])
  assert.deepEqual(
    {
      fingerprint: sbom.fingerprint,
      complete: sbom.complete,
      status: sbom.status,
      name: sbom.name,
      format: sbom.format,
      parse_status: sbom.parse_status,
      columns_that_could_hold_bytes_or_credentials:
        sbom.columns_that_could_hold_bytes_or_credentials,
    },
    {
      fingerprint,
      complete: true,
      status: 'done',
      name: fingerprint,
      format: 'SPDX',
      parse_status: 'parsed',
      columns_that_could_hold_bytes_or_credentials: 0,
    },
  )
  assert.ok(sbom.object_key, 'persisted SBOM has no object key')
  process.stdout.write(
    `ASSERT persisted SBOM: fingerprint=${fingerprint} status=done name-defaulted ` +
    `format=SPDX no-column-can-hold-bytes-or-credentials object-key=${sbom.object_key} ` +
    `proxied-bytes=${downloadedSBOM.bytes.length} magic=28b52ffd\n`,
  )

  await stopServer()
  const auditRecords = readFileSync(auditFile, 'utf8')
    .trim()
    .split('\n')
    .filter(Boolean)
    .map((line) => JSON.parse(line))
  const registryRequests = auditRecords.filter(
    (record) => record.kind === 'request' && record.path.startsWith('/packer/'),
  )
  assert.ok(registryRequests.length > 0, 'audit target contains no Packer registry requests')
  const registryResponses = auditRecords.filter(
    (record) => record.kind === 'response' &&
      registryRequests.some((requestRecord) => requestRecord.correlation_id === record.correlation_id),
  )
  assert.equal(
    registryResponses.length,
    registryRequests.length,
    `registry audit records are not paired: ${registryRequests.length} requests, ${registryResponses.length} responses`,
  )
  for (const requestRecord of registryRequests) {
    const responses = registryResponses.filter(
      (responseRecord) => responseRecord.correlation_id === requestRecord.correlation_id,
    )
    assert.equal(responses.length, 1, `registry request ${requestRecord.correlation_id} has ${responses.length} responses`)
    assert.equal(responses[0].principal_id, principal.id)
    assert.equal(responses[0].principal_name, principalName)
    assert.equal(responses[0].organization_id, organization.id)
    assert.equal(responses[0].project_id, project.id)
  }
  const operations = new Set(registryResponses.map((record) => record.operation))
  const expectedOperations = [
    'bucket_ancestry.list',
    'bucket.list',
    'bucket.read',
    'bucket.create',
    'version.read',
    'version.create',
    'build.list',
    'build.create',
    'build.update',
    'channel.read',
    'enforced_block.list',
    'sbom.upload',
    'sbom.list',
    'sbom.read',
    'sbom.download',
    'package.list',
  ].sort()
  assert.deepEqual(
    [...operations].sort(),
    expectedOperations,
    'real Packer audit trail contained the wrong registry operation set',
  )
  if (encrypted) {
    // The keyring operations are platform-plane requests by the root
    // principal, so they sit outside the /packer/ pairing above; the same
    // audit file must still carry all three as successes. Only request
    // records carry the path — responses join through the correlation id.
    const encryptionRequests = auditRecords.filter(
      (record) => record.kind === 'request' && record.path.startsWith('/api/v1/encryption'),
    )
    const encryptionResponses = auditRecords.filter(
      (record) => record.kind === 'response' && encryptionRequests.some(
        (requestRecord) => requestRecord.correlation_id === record.correlation_id,
      ),
    )
    assert.deepEqual(
      [...new Set(encryptionResponses.map((record) => record.operation))].sort(),
      ['encryption.read', 'encryption.rewrap', 'encryption.rotate'].sort(),
      'keyring operations are missing from the audit trail',
    )
    for (const record of encryptionResponses) {
      assert.equal(record.outcome, 'success', `${record.operation} audited as ${record.outcome}`)
    }
  }
  process.stdout.write(
    `ASSERT paired audit trail: ${registryRequests.length} Packer registry request/response pairs; ` +
    `operations=${[...operations].sort().join(',')}\n`,
  )

  await removeBuiltImages()
  const remaining = await command(docker, [
    'images', '-a', '-q', '--filter', `label=dev.dufflebag.e2e=${imageLabel}`,
  ])
  assert.equal(remaining.stdout.trim(), '', 'Packer output image survived gate teardown')
  imageLabel = undefined
  process.stdout.write('ASSERT Packer output image removed\n')
})
