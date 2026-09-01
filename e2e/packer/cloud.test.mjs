import assert from 'node:assert/strict'
import { execFile as execFileCb, spawn } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync } from 'node:fs'
import https from 'node:https'
import { createRequire } from 'node:module'
import net from 'node:net'
import path from 'node:path'
import { test } from 'node:test'
import { promisify } from 'node:util'

const execFile = promisify(execFileCb)
const serverBinary = process.env.E2E_PACKER_BIN
const work = process.env.E2E_PACKER_WORK
const templateDir = process.env.E2E_PACKER_TEMPLATE_DIR
const packer = process.env.E2E_PACKER_CLI
const docker = process.env.E2E_PACKER_DOCKER
const hostname = process.env.E2E_PACKER_HOSTNAME
const certFile = process.env.E2E_PACKER_CERT_FILE
const keyFile = process.env.E2E_PACKER_KEY_FILE
const caFile = process.env.E2E_PACKER_CA_FILE
const resourceGroup = process.env.AZURE_CI_RESOURCE_GROUP || 'rg-dufflebag-ci'
const awsRegion = process.env.AWS_DEFAULT_REGION || 'eu-west-2'
const runSuffix = process.env.GITHUB_RUN_ID || 'local'
const container = `dufflebag-packer-cloud-${process.pid}`
const objectContainer = `dufflebag-packer-cloud-s3-${process.pid}`
const objectStorageImage = 'quay.io/benjamin_holmes/ceph-aio:v20'
const objectStorageBucket = 'dufflebag-packer-cloud'
const captureScreenshots = process.env.CLOUD_SHOTS === '1'
const screenshotsDir = process.env.CLOUD_SHOTS_DIR || path.join(work || '.', 'shots')
const chrome = process.env.SMOKE_CHROME
const signingKey = randomBytes(32).toString('hex')
const organizationName = 'cloud-verification'
const projectName = 'packer-registry'
const principalName = 'cloud image builder'

let serverPort
let server
let serverOutput = ''
let containerStarted = false
let objectContainerStarted = false
let browser

async function command(file, args, options = {}) {
  try {
    return await execFile(file, args, {
      maxBuffer: 64 * 1024 * 1024,
      timeout: 25 * 60 * 1000,
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

async function objectStorageDiagnosis() {
  const lines = []
  for (const [what, args] of [
    ['healthcheck', ['inspect', '-f',
      '{{range .State.Health.Log}}{{printf "%.300s" .Output}}{{end}}', objectContainer]],
    ['disk', ['exec', objectContainer, 'df', '-h', '/']],
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
        resolve({
          status: response.statusCode,
          body: responseBody,
          bytes: Buffer.from(responseBody),
          headers: response.headers,
          json,
        })
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

async function detectAvailability() {
  const availability = {
    aws: {
      available: Boolean(process.env.AWS_ACCESS_KEY_ID),
      reason: 'AWS_ACCESS_KEY_ID is unset',
    },
    azure: {
      available: Boolean(process.env.ARM_CLIENT_ID),
      reason: 'ARM_CLIENT_ID is unset',
    },
    docker: { available: false, reason: 'Docker CLI or daemon is unavailable' },
  }

  try {
    await command(docker || 'docker', ['info', '--format', '{{.ServerVersion}}'], { timeout: 10000 })
    availability.docker = { available: true, reason: '' }
  } catch {
    // The reason above is printed with the skip.
  }

  return availability
}

function assertArtifact(platform, artifact, profile) {
  assert.ok(artifact?.external_identifier, `${platform} build returned no artifact identifier`)
  if (platform === 'aws') {
    assert.match(artifact.external_identifier, /^ami-[0-9a-f]+$/, 'AWS artifact was not an AMI ID')
    assert.equal(artifact.region, awsRegion, 'AWS artifact region differed from the build region')
    return
  }
  if (platform === 'azure') {
    const imageName = `dufflebag-ci-${runSuffix}-azure-${profile.name}`
    const imageIDPath = `/resourceGroups/${resourceGroup}/providers/Microsoft.Compute/images/${imageName}`
    assert.ok(
      artifact.external_identifier.includes(imageIDPath),
      `Azure artifact ${artifact.external_identifier} did not contain ${imageIDPath}`,
    )
    assert.equal(artifact.region, profile.expectedAzureRegion, 'Azure artifact region differed from its resource group')
    return
  }
  if (platform === 'docker') {
    assert.match(
      artifact.external_identifier,
      /^sha256:[0-9a-f]{64}$/,
      'Docker artifact was not an image SHA256',
    )
    assert.equal(artifact.region, 'docker', 'Docker artifact region was not docker')
    return
  }

  assert.fail(`unexpected build platform ${platform}`)
}

async function assertPublished(token, registryBase, buildRun, expectedPlatforms, expectedAzureRegion) {
  const bucketPath = `${registryBase}/buckets/${buildRun.bucket}`
  const bucket = await api(token, 'GET', bucketPath)
  assert.equal(bucket.bucket?.name, buildRun.bucket, `${buildRun.bucket} did not exist`)

  const published = await api(
    token,
    'GET',
    `${bucketPath}/versions/${buildRun.fingerprint}`,
  )
  assert.equal(published.version?.status, 'VERSION_ACTIVE', `${buildRun.bucket} version was not complete`)
  assert.equal(
    published.version?.builds?.length,
    expectedPlatforms.length,
    `${buildRun.bucket} returned the wrong build count`,
  )

  const platforms = published.version.builds.map((build) => build.platform).sort()
  assert.deepEqual(platforms, [...expectedPlatforms].sort(), `${buildRun.bucket} platform set differed`)
  assert.deepEqual(
    [...bucket.bucket.platforms].sort(),
    [...expectedPlatforms].sort(),
    `${buildRun.bucket} bucket platforms differed`,
  )
  for (const build of published.version.builds) {
    assert.equal(build.status, 'BUILD_DONE', `${buildRun.bucket}/${build.platform} was not complete`)
    assert.equal(build.artifacts?.length, 1, `${buildRun.bucket}/${build.platform} did not return one artifact`)
    assertArtifact(build.platform, build.artifacts[0], {
      expectedAzureRegion,
      name: buildRun.profile,
    })
  }

  const latest = await api(token, 'GET', `${bucketPath}/channels/latest`)
  assert.equal(latest.channel?.managed, true, `${buildRun.bucket} latest was not managed`)
  assert.equal(
    latest.channel?.version?.id,
    published.version.id,
    `${buildRun.bucket} latest was not assigned to its completed version`,
  )
  const history = await api(token, 'GET', `${bucketPath}/channels/latest/history`)
  assert.equal(history.count, 1, `${buildRun.bucket} latest was not assigned exactly once`)
  assert.equal(history.history?.length, 1, `${buildRun.bucket} latest history length differed`)
  assert.equal(
    history.history[0]?.version?.id,
    published.version.id,
    `${buildRun.bucket} latest history targeted another version`,
  )
  process.stdout.write(
    `ASSERT ${buildRun.bucket}: complete builds=${platforms.join(',')} latest_assignments=${history.count}\n`,
  )
  return { ...buildRun, version: published.version }
}

async function assertSBOMs(token, registryBase, publishedRun) {
  for (const build of publishedRun.version.builds) {
    const buildPath =
      `${registryBase}/buckets/${publishedRun.bucket}/versions/${publishedRun.fingerprint}` +
      `/builds/${build.id}`
    const listed = await api(token, 'GET', `${buildPath}/sboms`)
    assert.equal(
      listed.sboms?.length,
      1,
      `${publishedRun.bucket}/${build.platform} did not publish exactly one SBOM`,
    )
    assert.equal(listed.sboms[0].format, 'SPDX', `${publishedRun.bucket}/${build.platform} SBOM was not SPDX`)
    const metadata = await api(
      token,
      'GET',
      `${buildPath}/sboms/${encodeURIComponent(listed.sboms[0].name)}`,
    )
    assert.ok(metadata.download_url, `${publishedRun.bucket}/${build.platform} returned no SBOM download URL`)
    const downloaded = await request(
      'GET',
      new URL(metadata.download_url).pathname,
      undefined,
      { Authorization: `Bearer ${token}` },
    )
    assert.equal(
      downloaded.status,
      200,
      `${publishedRun.bucket}/${build.platform} SBOM download answered ${downloaded.status}: ${downloaded.body}`,
    )
    assert.equal(
      downloaded.json?.spdxVersion,
      'SPDX-2.3',
      `${publishedRun.bucket}/${build.platform} download was not SPDX 2.3 JSON`,
    )
    assert.ok(
      Array.isArray(downloaded.json?.packages) && downloaded.json.packages.length > 0,
      `${publishedRun.bucket}/${build.platform} SPDX document contained zero packages`,
    )
    process.stdout.write(
      `ASSERT ${publishedRun.bucket}/${build.platform}: ` +
      `sbom=SPDX-2.3 packages=${downloaded.json.packages.length}\n`,
    )
  }
}

async function scanAttribution(token, requestPath) {
  const response = await request('GET', requestPath, undefined, {
    Authorization: `Bearer ${token}`,
  })
  assert.ok(
    response.status >= 200 && response.status < 300,
    `GET ${requestPath} answered ${response.status}: ${response.body}`,
  )
  assert.ok(Array.isArray(response.json?.packages), `${requestPath} returned no packages array`)
  const adapter = response.headers['dufflebag-scan-adapter']
  if (!adapter) return null
  const count = (name) => Number(response.headers[`dufflebag-scan-${name}`] ?? 0)
  return {
    adapter,
    observedAt: response.headers['dufflebag-scan-observed-at'],
    submitted: count('submitted'),
    unsupported: count('unsupported'),
    unversioned: count('unversioned'),
    invalid: count('invalid'),
  }
}

async function assertScanPipeline(rootToken, principalToken, registryBase, publishedRuns) {
  const builds = publishedRuns.flatMap((publishedRun) =>
    publishedRun.version.builds.map((build) => ({ publishedRun, build })))
  const scans = await until('all published SBOMs to finish scanning', async () => {
    const observed = await Promise.all(builds.map(({ publishedRun, build }) =>
      scanAttribution(
        principalToken,
        `${registryBase}/buckets/${publishedRun.bucket}/versions/${publishedRun.fingerprint}` +
        `/builds/${build.id}/packages?pagination.page_size=1`,
      )))
    return observed.every(Boolean) ? observed : false
    // Seven builds, five of them whole-VM inventories of hundreds of
    // packages each: OSV needs minutes per SBOM, not seconds.
  }, 20 * 60 * 1000, 5000)

  for (const [index, scan] of scans.entries()) {
    const { publishedRun, build } = builds[index]
    assert.equal(scan.adapter, 'osv', `${publishedRun.bucket}/${build.platform} used another scanner`)
    assert.ok(scan.observedAt, `${publishedRun.bucket}/${build.platform} returned no scan observation time`)
    process.stdout.write(
      `ASSERT ${publishedRun.bucket}/${build.platform}: scan=osv ` +
      `submitted=${scan.submitted} unsupported=${scan.unsupported} ` +
      `unversioned=${scan.unversioned} invalid=${scan.invalid}\n`,
    )
  }

  for (const { build } of builds) {
    assert.match(build.id, /^[0-9a-f-]{36}$/, `unsafe build id returned by registry: ${build.id}`)
  }
  const quotedBuildIDs = builds.map(({ build }) => `'${build.id}'`).join(',')
  const { stdout: pendingOutput } = await command(docker, [
    'exec', container, 'psql', '-At', '-U', 'postgres', '-d', 'dufflebag',
    '-c', `SELECT count(*) FROM pending_scans WHERE build_id IN (${quotedBuildIDs})`,
  ])
  assert.equal(Number(pendingOutput.trim()), 0, 'completed cloud versions retained pending scans')

  const health = await api(rootToken, 'GET', '/api/v1/scanner/health')
  assert.equal(health.state, 'ok', `scanner health was ${health.state}: ${health.detail ?? ''}`)
  assert.equal(health.adapter, 'osv', 'scanner health named another adapter')
  assert.ok(health.last_observed_at, 'scanner health returned no observation time')

  for (const publishedRun of publishedRuns) {
    const summary = await api(
      principalToken,
      'GET',
      `${registryBase}/buckets/${publishedRun.bucket}/packages/vulnerability-summary`,
    )
    for (const field of [
      'channels_by_criticality',
      'packages_by_criticality',
      'total_by_criticality',
    ]) {
      assert.ok(Array.isArray(summary[field]), `${publishedRun.bucket} summary omitted ${field}`)
    }
    process.stdout.write(`ASSERT ${publishedRun.bucket}: scan settled, health=ok, summary=valid\n`)
  }
}

async function captureEvidence(endpoint, credentials, publishedRuns) {
  if (!captureScreenshots) return
  assert.ok(chrome && existsSync(chrome), `CLOUD_SHOTS=1 requires Chrome at ${chrome}`)
  const webPackage = path.join(templateDir, '..', '..', 'web', 'package.json')
  assert.ok(existsSync(webPackage), `web package is missing at ${webPackage}`)
  const puppeteer = createRequire(webPackage)('puppeteer-core')
  mkdirSync(screenshotsDir, { recursive: true })
  browser = await puppeteer.launch({
    executablePath: chrome,
    headless: true,
    acceptInsecureCerts: true,
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  })
  const page = await browser.newPage()
  page.setDefaultTimeout(30000)
  await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'light' }])
  await page.setViewport({ width: 1440, height: 900 })

  const bodyText = () => page.evaluate(() => document.body.innerText)
  const waitForText = async (text) => {
    try {
      await page.waitForFunction(
        (needle) => document.body.innerText.includes(needle),
        { polling: 100 },
        text,
      )
    } catch (err) {
      throw new Error(`never saw "${text}"; the page says:\n${await bodyText()}`, { cause: err })
    }
  }
  const clickByText = (selector, text) => until(`clickable "${text}"`, () =>
    page.$$eval(selector, (elements, needle) => {
      const match = elements.find((element) =>
        element.innerText?.trim().includes(needle) && !element.disabled)
      if (!match) return false
      match.click()
      return true
    }, text))
  const clickFacet = (ariaLabel, label) => until(`the "${label}" facet in ${ariaLabel}`, () =>
    page.$$eval(
      `nav[aria-label="${ariaLabel}"] button[role="tab"]`,
      (buttons, needle) => {
        const button = buttons.find((candidate) =>
          candidate.querySelector('.pf-v6-c-tabs__item-text')?.textContent.trim() === needle)
        if (!button) return false
        button.click()
        return true
      },
      label,
    ))
  const capture = async (name) => {
    await page.evaluate(async () => {
      await document.fonts.ready
      const style = document.createElement('style')
      style.textContent = '*,*::before,*::after{animation:none!important;transition:none!important}'
      document.head.appendChild(style)
    })
    await page.screenshot({ path: path.join(screenshotsDir, name) })
    process.stdout.write(`CAPTURE ${name}\n`)
  }

  await page.goto(endpoint, { waitUntil: 'domcontentloaded' })
  await waitForText('Log in')
  await page.type('#client-id', credentials.clientID)
  await page.type('#client-secret', credentials.clientSecret)
  await clickByText('button', 'Log in')
  await waitForText('Sign out')

  await page.goto(`${endpoint}/buckets`, { waitUntil: 'domcontentloaded' })
  await waitForText('Buckets')
  for (const publishedRun of publishedRuns) await waitForText(publishedRun.bucket)
  await capture('buckets.png')

  const multi = publishedRuns.find((publishedRun) => publishedRun.bucket === 'verify-multi')
  if (multi) {
    await page.goto(
      `${endpoint}/buckets/verify-multi/versions/${encodeURIComponent(multi.fingerprint)}`,
      { waitUntil: 'domcontentloaded' },
    )
    await clickFacet('Version facets', 'Builds')
    for (const component of ['amazon-ebs.ubuntu', 'azure-arm.ubuntu', 'docker.ubuntu']) {
      await waitForText(component)
    }
    await capture('verify-multi-version.png')
  } else {
    process.stdout.write('SKIP CAPTURE verify-multi-version.png: joint build did not run\n')
  }

  const candidates = publishedRuns.flatMap((publishedRun) =>
    publishedRun.version.builds.map((build) => ({ publishedRun, build })))
  const selected = candidates.find(({ publishedRun, build }) =>
    publishedRun.bucket === 'verify-multi' && ['aws', 'azure'].includes(build.platform)) ||
    candidates.find(({ build }) => ['aws', 'azure'].includes(build.platform)) || candidates[0]
  assert.ok(selected, 'no published build was available for evidence capture')
  const buildURL =
    `${endpoint}/buckets/${encodeURIComponent(selected.publishedRun.bucket)}` +
    `/versions/${encodeURIComponent(selected.publishedRun.fingerprint)}` +
    `/builds/${encodeURIComponent(selected.build.id)}`
  await page.goto(buildURL, { waitUntil: 'domcontentloaded' })
  await clickFacet('Build facets', 'Artifacts')
  await waitForText(selected.build.artifacts[0].external_identifier)
  await capture('artifact-detail.png')

  await clickFacet('Build facets', 'Packages')
  await waitForText('with findings')
  await capture('vulnerabilities-packages.png')

  await browser.close()
  browser = undefined
}

// Subtest concurrency is the point: the builds must overlap or wall-clock is
// the sum of three 15-minute cloud builds, not the slowest one.
test('available Packer builders publish solo and multi-platform registry versions', { concurrency: 5 }, async (t) => {
  t.after(async () => {
    if (browser) await browser.close().catch(() => {})
    await stopServer()
    if (containerStarted) await command(docker, ['rm', '-f', container]).catch(() => {})
    if (objectContainerStarted) {
      await command(docker, ['rm', '-f', objectContainer]).catch(() => {})
    }
  })

  const availability = await detectAvailability()
  const availableSources = Object.entries(availability)
    .filter(([, source]) => source.available)
    .map(([name]) => name)
  if (availableSources.length === 0) {
    t.skip(`no Packer source available: ${Object.values(availability).map((source) => source.reason).join('; ')}`)
    return
  }

  for (const [name, source] of Object.entries(availability)) {
    if (!source.available) process.stdout.write(`SKIP verify-${name}: ${source.reason}\n`)
  }
  const missingJointSources = Object.entries(availability)
    .filter(([, source]) => !source.available)
    .map(([name]) => name)
  if (missingJointSources.length > 0) {
    process.stdout.write(`SKIP verify-multi: missing sources: ${missingJointSources.join(', ')}\n`)
  }

  assert.ok(serverBinary, 'E2E_PACKER_BIN must point at the built server')
  assert.ok(work, 'E2E_PACKER_WORK must point at the isolated Packer directory')
  assert.ok(templateDir, 'E2E_PACKER_TEMPLATE_DIR must point at the Packer templates')
  assert.ok(packer, 'E2E_PACKER_CLI must name the Packer executable')
  assert.ok(docker, 'E2E_PACKER_DOCKER must name the Docker executable')
  assert.ok(hostname, 'E2E_PACKER_HOSTNAME must name the TLS server')
  assert.ok(existsSync(serverBinary), `no server binary at ${serverBinary}`)
  for (const [what, file] of [
    ['TLS certificate', certFile],
    ['TLS private key', keyFile],
    ['TLS CA', caFile],
    ...['aws', 'azure', 'docker', 'multi'].map((name) => [
      `${name} template`, path.join(templateDir, `${name}.pkr.hcl`),
    ]),
  ]) {
    assert.ok(file && existsSync(file), `${what} is missing at ${file}`)
  }

  if (availability.aws.available) {
    process.stdout.write(`CLEANUP_AWS_AMI=dufflebag-ci-${runSuffix}-aws-solo\n`)
    process.stdout.write(`CLEANUP_AWS_AMI=dufflebag-ci-${runSuffix}-aws-multi\n`)
  }
  if (availability.azure.available) {
    process.stdout.write(`CLEANUP_AZURE_IMAGE=dufflebag-ci-${runSuffix}-azure-solo\n`)
    process.stdout.write(`CLEANUP_AZURE_IMAGE=dufflebag-ci-${runSuffix}-azure-multi\n`)
  }

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
  try {
    await until('Ceph to report healthy', async () => {
      const { stdout } = await command(docker, [
        'inspect', '-f', '{{.State.Health.Status}}', objectContainer,
      ])
      return stdout.trim() === 'healthy'
    }, 300000, 2000)
  } catch (err) {
    throw new Error(`${err.message}\n${await objectStorageDiagnosis()}`)
  }
  await command(docker, [
    'exec', objectContainer, 'radosgw-admin', 'user', 'create',
    '--uid=dufflebag-packer-cloud', '--display-name=dufflebag packer cloud',
    '--access-key=testaccess', '--secret-key=testsecret',
  ])
  await command(docker, [
    'cp', path.join(templateDir, '..', 'support', 'create-bucket.py'),
    `${objectContainer}:/tmp/create-bucket.py`,
  ])
  await command(docker, [
    'exec', objectContainer, 'python3', '/tmp/create-bucket.py',
    'testaccess', 'testsecret', objectStorageBucket,
  ])

  const adminURL = `postgres://postgres:postgres@127.0.0.1:${pgPort}/dufflebag?sslmode=disable`
  serverOutput = ''
  const migrator = startServer({
    DFBG_DATABASE_URL: adminURL,
    DFBG_TOKEN_SIGNING_KEY: signingKey,
  })
  const migrationExit = await Promise.race([
    waitForExit(migrator),
    new Promise((_, reject) => setTimeout(
      () => reject(new Error('migration process did not exit')),
      60000,
    )),
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
  serverOutput = ''
  server = startServer({
    DFBG_DATABASE_URL: `postgres://dufflebag_app:app@127.0.0.1:${pgPort}/dufflebag?sslmode=disable`,
    DFBG_HTTP_ADDR: `127.0.0.1:${serverPort}`,
    DFBG_TOKEN_SIGNING_KEY: signingKey,
    DFBG_TOKEN_ISSUER: endpoint,
    DFBG_TLS_CERT_FILE: certFile,
    DFBG_TLS_KEY_FILE: keyFile,
    DFBG_OBJECT_STORAGE_ENDPOINT: `http://127.0.0.1:${objectPort}`,
    DFBG_OBJECT_STORAGE_REGION: 'us-east-1',
    DFBG_OBJECT_STORAGE_BUCKET: objectStorageBucket,
    DFBG_OBJECT_STORAGE_ACCESS_KEY: 'testaccess',
    DFBG_OBJECT_STORAGE_SECRET_KEY: 'testsecret',
    DFBG_SCANNER_ADAPTER: 'osv',
    DFBG_SCANNER_INTERVAL: '2s',
    DFBG_SCANNER_PASS_TIMEOUT: '10m',
    DFBG_SCANNER_WORKERS: '4',
    // A whole-VM SPDX overflows the 4MiB compat default even compressed, and
    // an UploadSbom refusal hard-fails a stock Packer build. The operator
    // knob is the product's answer for VM-scale SBOMs; the lane uses it too.
    DFBG_API_MAX_BODY_BYTES: String(64 << 20),
  })
  await until('the trusted TLS server to answer', async () => {
    if (server.exitCode !== null) throw new Error(`server exited ${server.exitCode}: ${serverOutput}`)
    return (await request('GET', '/')).status === 200
  })

  const initialized = await request('POST', '/sys/init')
  assert.equal(initialized.status, 200, `POST /sys/init answered ${initialized.status}: ${initialized.body}`)
  assert.ok(initialized.json?.client_id && initialized.json?.client_secret, 'POST /sys/init returned no credentials')
  const rootToken = await tokenFor(initialized.json)

  const organization = await api(rootToken, 'POST', '/api/v1/organizations', {
    name: organizationName,
  })
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
  const issued = await api(rootToken, 'POST', `/api/v1/principals/${principal.id}/secrets`, {})
  assert.ok(issued.secret, 'ordinary secret issuance returned no one-time secret')
  // Minted before any build starts purely to prove the credential works;
  // assertion-time tokens are minted fresh because builds outlive the TTL.
  const principalToken = await tokenFor({
    client_id: principal.client_id,
    client_secret: issued.secret,
  })

  const home = path.join(work, 'home')
  mkdirSync(home, { recursive: true })
  const packerEnv = {
    ...process.env,
    HOME: home,
    XDG_CONFIG_HOME: path.join(home, '.config'),
    XDG_CACHE_HOME: path.join(home, '.cache'),
    CHECKPOINT_DISABLE: '1',
    SSL_CERT_FILE: caFile,
    HCP_AUTH_URL: endpoint,
    HCP_API_ADDRESS: `${hostname}:${serverPort}`,
    HCP_CLIENT_ID: principal.client_id,
    HCP_CLIENT_SECRET: issued.secret,
    HCP_ORGANIZATION_ID: organization.id,
    HCP_PROJECT_ID: project.id,
    HCP_SKIP_STATUS_CHECK: 'true',
  }
  delete packerEnv.HCP_AUTH_TLS
  delete packerEnv.HCP_API_TLS
  delete packerEnv.HCP_OAUTH_CLIENT_ID

  let expectedAzureRegion
  if (availability.azure.available) {
    const { stdout } = await command('az', [
      'group', 'show',
      '--name', resourceGroup,
      '--subscription', process.env.ARM_SUBSCRIPTION_ID,
      '--query', 'location',
      '--output', 'tsv',
    ])
    expectedAzureRegion = stdout.trim()
    assert.ok(expectedAzureRegion, `Azure resource group ${resourceGroup} returned no location`)
  }

  const initializedPlugins = await command(
    packer,
    ['init', path.join(templateDir, 'multi.pkr.hcl')],
    { env: packerEnv },
  )
  process.stdout.write(`${initializedPlugins.stdout}${initializedPlugins.stderr}`)

  const started = Date.now()
  const buildRuns = availableSources.map((name) => ({
    name,
    bucket: `verify-${name}`,
    fingerprint: `verify-${name}-${runSuffix}-${started}`,
    profile: 'solo',
    template: path.join(templateDir, `${name}.pkr.hcl`),
  }))
  if (missingJointSources.length === 0) {
    buildRuns.push({
      name: 'multi',
      bucket: 'verify-multi',
      fingerprint: `verify-multi-${runSuffix}-${started}`,
      profile: 'multi',
      template: path.join(templateDir, 'multi.pkr.hcl'),
    })
  }

  const registryBase = `/packer/2023-01-01/organizations/${organization.id}/projects/${project.id}`
  // One subtest per bucket so the JUnit report carries a case per claim
  // rather than one monolithic verdict; the subtests run concurrently.
  const publishedRuns = []
  await Promise.all(buildRuns.map((buildRun) =>
    t.test(`${buildRun.bucket} publishes, completes and carries SBOMs`, async () => {
      const variables = buildRun.name === 'multi' ? ['-var', `work_dir=${work}`] : []
      const result = await command(
        packer,
        ['build', '-color=false', ...variables, buildRun.template],
        {
          env: {
            ...packerEnv,
            HCP_PACKER_BUILD_FINGERPRINT: buildRun.fingerprint,
            PACKER_LOG: '1',
          },
        },
      )
      const output = `${result.stdout}${result.stderr}`
      process.stdout.write(output)
      assert.match(
        output,
        /Published metadata to HCP Packer registry/,
        `${buildRun.bucket} did not report registry publication`,
      )
      // Tokens age out within 15 minutes and a cloud build can outlive that:
      // mint at the assertion boundary, never before the build.
      const freshToken = await tokenFor({
        client_id: principal.client_id,
        client_secret: issued.secret,
      })
      const publishedRun = await assertPublished(
        freshToken,
        registryBase,
        buildRun,
        buildRun.name === 'multi' ? ['aws', 'azure', 'docker'] : [buildRun.name],
        expectedAzureRegion,
      )
      await assertSBOMs(freshToken, registryBase, publishedRun)
      publishedRuns.push(publishedRun)
    })))
  assert.equal(
    publishedRuns.length, buildRuns.length,
    'a bucket subtest failed before publication; skipping the scan and capture phases',
  )
  await t.test('the scan pipeline settles across every bucket', async () => {
    const scanRootToken = await tokenFor(initialized.json)
    const scanPrincipalToken = await tokenFor({
      client_id: principal.client_id,
      client_secret: issued.secret,
    })
    await assertScanPipeline(scanRootToken, scanPrincipalToken, registryBase, publishedRuns)
  })
  await captureEvidence(endpoint, {
    clientID: principal.client_id,
    clientSecret: issued.secret,
  }, publishedRuns)
})
