import assert from 'node:assert/strict'
import { execFile as execFileCb, spawn } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { existsSync } from 'node:fs'
import https from 'node:https'
import net from 'node:net'
import path from 'node:path'
import { after, before, test } from 'node:test'
import { promisify } from 'node:util'

const execFile = promisify(execFileCb)
const serverBinary = process.env.E2E_TERRAFORM_BIN
const work = process.env.E2E_TERRAFORM_WORK
const container = `dufflebag-terraform-e2e-${process.pid}`
const signingKey = randomBytes(32).toString('hex')
const bucketName = 'dufflebag-terraform-e2e'
const seededFingerprint = 'dufflebag-e2e-fingerprint'
const artifactID = 'ami-dufflebag-e2e'
const packerRunUUID = '9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d'
const organizationName = 'terraform-foundation'
const projectName = 'terraform-registry'
// With a key provider in the caller's environment (DFBG_KEY_PROVIDER plus
// VAULT_* passed through process.env) the lane runs the ENCRYPTED posture:
// the signing key lives in the wrapped keyring, and passing an env copy
// would be refused at startup (ADR-0024).
const encrypted = Boolean(process.env.DFBG_KEY_PROVIDER)

let serverPort
let pgPort
let server
let serverOutput = ''
let containerStarted = false
let terraformInitialized = false
let terraformDestroyed = false
let terraformEnv
let credentials
let token
let organizationID
let projectID

async function until(what, condition, timeoutMs = 60000, intervalMs = 100) {
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

async function command(file, args, options = {}) {
  try {
    return await execFile(file, args, { maxBuffer: 16 * 1024 * 1024, ...options })
  } catch (err) {
    err.message = [
      `${file} ${args.join(' ')} failed: ${err.message}`,
      err.stdout ? `stdout:\n${err.stdout}` : '',
      err.stderr ? `stderr:\n${err.stderr}` : '',
    ].filter(Boolean).join('\n')
    throw err
  }
}

async function terraformIn(directory, ...args) {
  const result = await command('terraform', args, { cwd: directory, env: terraformEnv })
  process.stdout.write(`\n$ terraform ${args.join(' ')}\n${result.stdout}${result.stderr}`)
  return result.stdout
}

async function terraform(...args) {
  return terraformIn(work, ...args)
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

const waitForExit = (child) =>
  new Promise((resolve) => child.once('exit', (code) => resolve(code)))

function request(method, requestPath, body, headers = {}) {
  return new Promise((resolve, reject) => {
    const encoded = body === undefined
      ? undefined
      : typeof body === 'string' ? body : JSON.stringify(body)
    const req = https.request({
      hostname: '127.0.0.1',
      port: serverPort,
      path: requestPath,
      method,
      rejectUnauthorized: false,
      headers: {
        ...headers,
        ...(encoded === undefined ? {} : { 'Content-Length': Buffer.byteLength(encoded) }),
      },
    }, (response) => {
      let responseBody = ''
      response.on('data', (chunk) => (responseBody += chunk))
      response.on('end', () => {
        let json
        if (responseBody) {
          try {
            json = JSON.parse(responseBody)
          } catch {
            json = undefined
          }
        }
        resolve({ status: response.statusCode, body: responseBody, json })
      })
    })
    req.once('error', reject)
    if (encoded !== undefined) req.write(encoded)
    req.end()
  })
}

async function api(method, requestPath, body) {
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

async function seedCompletedVersion() {
  // Producer-derived wire shapes: the generated hcp-sdk-go calls exercised in
  // contract/hcp2023_contract_test.go. Keep the exact CreateVersion ->
  // CreateBuild -> UpdateBuild BUILD_DONE order from dossier section 6.
  const base = `/packer/2023-01-01/organizations/${organizationID}` +
    `/projects/${projectID}/buckets/${bucketName}`
  await api('POST', `${base}/versions`, {
    fingerprint: seededFingerprint,
    template_type: 'HCL2',
  })
  const created = await api('POST', `${base}/versions/${seededFingerprint}/builds`, {
    component_type: 'amazon-ebs.e2e',
    packer_run_uuid: packerRunUUID,
    status: 'BUILD_UNSET',
    artifacts: [],
  })
  assert.ok(created.build?.id, `CreateBuild returned no build id: ${JSON.stringify(created)}`)
  await api('PATCH', `${base}/versions/${seededFingerprint}/builds/${created.build.id}`, {
    status: 'BUILD_DONE',
    platform: 'aws',
    packer_run_uuid: packerRunUUID,
    artifacts: [{ external_identifier: artifactID, region: 'eu-west-1' }],
    metadata: {},
  })
  const latest = await api('GET', `${base}/channels/latest`)
  assert.equal(latest.channel?.version?.fingerprint, seededFingerprint)
  process.stdout.write(
    `SEEDED: latest -> ${latest.channel.version.fingerprint} (${latest.channel.version.name})\n`,
  )
}

before(async () => {
  assert.ok(serverBinary, 'E2E_TERRAFORM_BIN must point at the built server')
  assert.ok(work, 'E2E_TERRAFORM_WORK must point at the isolated Terraform directory')
  assert.ok(existsSync(serverBinary), `no server binary at ${serverBinary}`)
  assert.ok(existsSync(path.join(work, 'main.tf')), `no Terraform config in ${work}`)

  const certFile = path.join(work, 'tls.crt')
  const keyFile = path.join(work, 'tls.key')
  await command('openssl', [
    'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
    '-keyout', keyFile, '-out', certFile, '-subj', '/CN=127.0.0.1',
    '-addext', 'subjectAltName=IP:127.0.0.1',
  ])

  await command('docker', [
    'run', '-d', '--rm', '--name', container,
    '-e', 'POSTGRES_PASSWORD=postgres',
    '-e', 'POSTGRES_DB=dufflebag',
    '-p', '127.0.0.1::5432',
    'postgres:17-alpine',
  ])
  containerStarted = true
  const { stdout: portLine } = await command('docker', ['port', container, '5432/tcp'])
  pgPort = portLine.trim().split('\n')[0].split(':').pop()
  await until('postgres to accept connections', async () => {
    await command('docker', [
      'exec', container, 'pg_isready', '-U', 'postgres', '-d', 'dufflebag',
    ])
    return true
  }, 60000, 250)

  const adminURL = `postgres://postgres:postgres@127.0.0.1:${pgPort}/dufflebag?sslmode=disable`
  await until('migrations to apply and the RLS gate to refuse the superuser', async () => {
    serverOutput = ''
    const migrator = startServer({
      DFBG_DATABASE_URL: adminURL,
      ...(encrypted ? {} : { DFBG_TOKEN_SIGNING_KEY: signingKey }),
    })
    await waitForExit(migrator)
    return serverOutput.includes('refusing to serve')
  }, 60000, 500)

  await command('docker', [
    'exec', container, 'psql', '-v', 'ON_ERROR_STOP=1',
    '-U', 'postgres', '-d', 'dufflebag',
    '-c', "CREATE ROLE dufflebag_app LOGIN PASSWORD 'app' NOSUPERUSER NOBYPASSRLS",
    '-c', 'GRANT USAGE ON SCHEMA public TO dufflebag_app',
    '-c', 'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dufflebag_app',
  ])

  serverPort = await freePort()
  const endpoint = `https://127.0.0.1:${serverPort}`
  serverOutput = ''
  server = startServer({
    DFBG_DATABASE_URL: `postgres://dufflebag_app:app@127.0.0.1:${pgPort}/dufflebag?sslmode=disable`,
    DFBG_HTTP_ADDR: `127.0.0.1:${serverPort}`,
    ...(encrypted ? {} : { DFBG_TOKEN_SIGNING_KEY: signingKey }),
    DFBG_TOKEN_ISSUER: endpoint,
    DFBG_TLS_CERT_FILE: certFile,
    DFBG_TLS_KEY_FILE: keyFile,
  })
  await until('the TLS server to answer', async () => {
    const response = await request('GET', '/')
    return response.status === 200
  }, 60000, 250)

  // Non-vacuity for the encrypted posture: a silently-unencrypted run must not
  // pass as an encrypted one, so the instance itself has to say the keyring is
  // wrapped and healthy before any Terraform runs.
  if (encrypted) {
    const health = await request('GET', '/sys/health')
    assert.equal(
      health.json?.encryption, 'ok',
      `encrypted posture, but /sys/health reports: ${health.body}`,
    )
    process.stdout.write('ASSERT encrypted posture: /sys/health encryption ok\n')
  }

  const initialized = await request('POST', '/sys/init')
  assert.equal(initialized.status, 200, `POST /sys/init answered ${initialized.status}: ${initialized.body}`)
  credentials = initialized.json
  assert.ok(credentials?.client_id && credentials?.client_secret, 'POST /sys/init returned no credentials')

  const tokenResponse = await request(
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
  assert.equal(tokenResponse.status, 200, `token endpoint answered ${tokenResponse.status}: ${tokenResponse.body}`)
  token = tokenResponse.json?.access_token
  assert.ok(token, 'token endpoint returned no access_token')

  // The /sys/init credential authenticates before any tenancy exists, then uses
  // the same two platform endpoints as the browser wizard. This is the Rule 8
  // reachability proof: no database seed and no privileged side door.
  const organization = await api('POST', '/api/v1/organizations', { name: organizationName })
  assert.equal(organization.name, organizationName)
  organizationID = organization.id
  const project = await api(
    'POST', `/api/v1/organizations/${organizationID}/projects`, { name: projectName },
  )
  assert.equal(project.name, projectName)
  projectID = project.id

  // The bootstrap root creates this credential through the real platform API;
  // no database fixture stands in for its producer. Creation and issuance are
  // two calls (duf-4ac): the principal exists holding nothing, then a secret is
  // issued through the endpoint rotation also uses.
  const terraformPrincipal = await api('POST', '/api/v1/principals', {
    name: 'terraform end-to-end',
    role: 'publisher',
    organization_id: organizationID,
  })
  assert.ok(terraformPrincipal.client_id)
  assert.equal(
    terraformPrincipal.secret, undefined,
    'CreatePrincipal returned a secret; creation must mint none',
  )
  const issued = await api('POST', `/api/v1/principals/${terraformPrincipal.id}/secrets`, {})
  assert.ok(issued.secret, 'no secret was issued for the terraform principal')
  credentials = {
    client_id: terraformPrincipal.client_id,
    client_secret: issued.secret,
  }
  const terraformToken = await request(
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
  assert.equal(terraformToken.status, 200, `token endpoint answered ${terraformToken.status}: ${terraformToken.body}`)
  token = terraformToken.json?.access_token
  assert.ok(token, 'token endpoint returned no access_token for Terraform principal')

  terraformEnv = {
    ...process.env,
    TF_IN_AUTOMATION: '1',
    TF_INPUT: '0',
    CHECKPOINT_DISABLE: '1',
    HCP_API_ADDRESS: `127.0.0.1:${serverPort}`,
    HCP_API_TLS: 'insecure',
    HCP_AUTH_URL: endpoint,
    HCP_AUTH_TLS: 'insecure',
    HCP_CLIENT_ID: credentials.client_id,
    HCP_CLIENT_SECRET: credentials.client_secret,
    HCP_SKIP_STATUS_CHECK: 'true',
    TF_VAR_bucket_name: bucketName,
    TF_VAR_seeded_fingerprint: seededFingerprint,
  }
  delete terraformEnv.HCP_OAUTH_CLIENT_ID
  delete terraformEnv.HCP_ORGANIZATION_ID
  delete terraformEnv.HCP_PROJECT_ID
})

after(async () => {
  if (terraformInitialized && !terraformDestroyed && terraformEnv) {
    await command('terraform', [
      'destroy', '-auto-approve', '-input=false', '-no-color',
    ], { cwd: work, env: terraformEnv }).catch(() => {})
  }
  if (server && server.exitCode === null) {
    const exited = waitForExit(server)
    server.kill('SIGTERM')
    await Promise.race([exited, new Promise((resolve) => setTimeout(resolve, 10000))])
    if (server.exitCode === null) server.kill('SIGKILL')
  }
  if (containerStarted) {
    await command('docker', ['rm', '-f', container]).catch(() => {})
  }
})

test('real Terraform applies the ADR-0013 surface and destroys it cleanly', async () => {
  const version = await command('terraform', ['version'], { cwd: work, env: terraformEnv })
  assert.match(version.stdout, /Terraform v1\.14\.7/)
  process.stdout.write(`TERRAFORM: ${version.stdout.split('\n')[0]}\n`)

  await terraform('init', '-input=false', '-no-color')
  terraformInitialized = true
  await terraform(
    'apply', '-auto-approve', '-input=false', '-no-color',
    '-target=hcp_packer_bucket.images',
  )
  await seedCompletedVersion()

  await terraform('apply', '-auto-approve', '-input=false', '-no-color')

  const stateOutput = await terraform('state', 'list')
  const actualState = stateOutput.trim().split('\n').filter(Boolean).sort()
  const expectedState = [
    'data.hcp_packer_artifact.seeded',
    'data.hcp_packer_bucket_names.all',
    'data.hcp_packer_version.latest',
    'hcp_packer_bucket.images',
    'hcp_packer_channel.latest',
    'hcp_packer_channel.production',
    'hcp_packer_channel_assignment.production',
  ].sort()
  assert.deepEqual(actualState, expectedState)
  process.stdout.write(`ASSERT state set exact: ${actualState.join(', ')}\n`)

  const outputs = JSON.parse(await terraform('output', '-json'))
  assert.equal(outputs.version_fingerprint.value, seededFingerprint)
  assert.equal(outputs.artifact_fingerprint.value, seededFingerprint)
  assert.equal(outputs.artifact_external_identifier.value, artifactID)
  assert.deepEqual(outputs.bucket_names.value, [bucketName])
  process.stdout.write(
    `ASSERT seeded fingerprints: version=${outputs.version_fingerprint.value} ` +
    `artifact=${outputs.artifact_fingerprint.value}; artifact=${outputs.artifact_external_identifier.value}\n`,
  )
  process.stdout.write(`ASSERT bucket_names: ${outputs.bucket_names.value.join(',')}\n`)

  // Teeth for the encrypted posture, in the spirit of the store's
  // TestSabotagedMACKeyFailsAuthentication: corrupt the seeded version row's
  // integrity MAC and the provider's refresh of the seeded data sources must
  // fail — anything else means Terraform is reading MAC-carrying rows without
  // the store verifying them. Restored afterwards so the destroy still runs.
  if (encrypted) {
    const psql = (sql) => command('docker', [
      'exec', container, 'psql', '-At', '-v', 'ON_ERROR_STOP=1',
      '-U', 'postgres', '-d', 'dufflebag', '-c', sql,
    ])
    const { stdout: savedMAC } = await psql(
      `SELECT encode(integrity_mac, 'hex') FROM versions WHERE fingerprint = '${seededFingerprint}'`,
    )
    assert.ok(savedMAC.trim(), 'encrypted posture, but the seeded version row carries no integrity MAC')
    process.stdout.write('ASSERT encrypted posture: seeded version row carries an integrity MAC\n')

    await psql(
      `UPDATE versions SET integrity_mac = '\\x00' WHERE fingerprint = '${seededFingerprint}'`,
    )
    await assert.rejects(
      terraform('plan', '-input=false', '-no-color'),
      /Error/,
      'terraform plan succeeded against a sabotaged version MAC',
    )
    assert.match(
      serverOutput, /row integrity verification failed/,
      'the failed plan did not trip the store’s MAC verification',
    )
    process.stdout.write('ASSERT sabotaged version MAC: plan failed, server logged row integrity verification failed\n')

    await psql(
      `UPDATE versions SET integrity_mac = decode('${savedMAC.trim()}', 'hex') WHERE fingerprint = '${seededFingerprint}'`,
    )
    await terraform('plan', '-input=false', '-no-color')
    process.stdout.write('ASSERT restored version MAC: plan green again\n')
  }

  await terraform('destroy', '-auto-approve', '-input=false', '-no-color')
  terraformDestroyed = true
  const emptyState = await terraform('state', 'list')
  assert.equal(emptyState.trim(), '')

  const bucketPath = `/packer/2023-01-01/organizations/${organizationID}` +
    `/projects/${projectID}/buckets/${bucketName}`
  const missing = await request('GET', bucketPath, undefined, {
    Authorization: `Bearer ${token}`,
  })
  assert.equal(missing.status, 404, `destroyed bucket answered ${missing.status}: ${missing.body}`)
  assert.equal(missing.json?.code, 5)
  process.stdout.write(`ASSERT destroyed bucket: HTTP ${missing.status} code ${missing.json.code}\n`)

  // Provider v0.112.0 differs from Packer: pinning both ids still causes
  // Configure to fetch ProjectService_Get before any Packer operation
  // (terraform-provider-hcp v0.112.0 internal/provider/provider.go:291-306;
  // dossier section 2 correction, 2026-08-01).
  terraformEnv.HCP_ORGANIZATION_ID = organizationID
  terraformEnv.HCP_PROJECT_ID = projectID
  terraformDestroyed = false
  await terraform(
    'apply', '-auto-approve', '-input=false', '-no-color',
    '-target=hcp_packer_bucket.images',
  )
  await seedCompletedVersion()
  await terraform('apply', '-auto-approve', '-input=false', '-no-color')
  const pinnedOutputs = JSON.parse(await terraform('output', '-json'))
  assert.equal(pinnedOutputs.version_fingerprint.value, seededFingerprint)
  assert.equal(pinnedOutputs.artifact_external_identifier.value, artifactID)
  process.stdout.write(
    `ASSERT pinned HCP_PROJECT_ID provider path: project=${projectID} ` +
    `fingerprint=${pinnedOutputs.version_fingerprint.value}\n`,
  )
  await terraform('destroy', '-auto-approve', '-input=false', '-no-color')
  terraformDestroyed = true
  assert.equal((await terraform('state', 'list')).trim(), '')

  const tokenRequests = (serverOutput.match(/"operation":"token.issue"/g) ?? []).length
  assert.doesNotMatch(serverOutput, /"reason":"rate_limited"/)
  process.stdout.write(`ASSERT unpaced token requests: ${tokenRequests}\n`)
})
