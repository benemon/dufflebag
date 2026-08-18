import assert from 'node:assert/strict'
import { execFile as execFileCb, spawn } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import {
  appendFileSync, closeSync, existsSync, mkdirSync, mkdtempSync, openSync, readFileSync,
  rmSync, statfsSync, statSync, symlinkSync, writeFileSync, writeSync,
} from 'node:fs'
import https from 'node:https'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { after, before, test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { zstdCompressSync } from 'node:zlib'

import puppeteer from 'puppeteer-core'

/**
 * The console driven in a real browser against a real stack (duf-ybp).
 *
 * Everything an API check cannot see lives here: the binary serving the real
 * console rather than the placeholder (duf-6d0), the header naming the actual
 * session tenant rather than a fixture, and the first-run journey /init's
 * credential actually takes (duf-tkw).
 *
 * The stack is the real one: Postgres in Docker, migrations applied by the
 * server's own first-boot refusal (the documented procedure — migrate as the
 * privileged role, get refused, then serve as an unprivileged one so RLS
 * applies), and the compiled binary with the console embedded.
 *
 * NO FIXED SLEEPS. Every wait is on a condition — an element appearing, a
 * process exiting, an endpoint answering — with a deadline. A smoke test
 * people learn to re-run launders real failures into noise.
 */

const execFile = promisify(execFileCb)

const chrome =
  process.env.SMOKE_CHROME ||
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const serverBinary = process.env.SMOKE_BIN
const container = `dufflebag-smoke-${process.pid}`
const objectContainer = `dufflebag-smoke-s3-${process.pid}`
const vaultContainer = `dufflebag-smoke-vault-${process.pid}`
const scannerContainer = `dufflebag-smoke-osv-${process.pid}`
const vaultToken = 'smoke-root'
// Ceph rather than a lighter stand-in: it is what the lab runs.
const objectStorageImage = 'quay.io/benjamin_holmes/ceph-aio:v20'
const scannerImage = process.env.OSV_STUB_IMAGE || 'dufflebag-osv-stub:dev'
const objectStorageBucket = 'dufflebag-smoke'
const signingKey = randomBytes(32).toString('hex')
const wizardOrganizationName = 'smoke-foundation'
const wizardProjectName = 'smoke-registry'
const auditDir = mkdtempSync(join(tmpdir(), 'dufflebag-smoke-audit-'))
const auditVolumeImage = join(auditDir, 'audit.dmg')
const auditVolumeMount = join(auditDir, 'volume')
const auditVolumeFiller = join(auditVolumeMount, 'filler')
const auditGoodOne = join(auditDir, 'good-one.log')
const auditGoodTwo = join(auditDir, 'good-two.log')
const auditFull = join(auditVolumeMount, 'full.log')
const auditLinkTarget = join(auditDir, 'link-target.log')
const auditLink = join(auditDir, 'link.log')

let serverPort
let objectPort
let vaultBase
let scannerBase
let base
let server
let serverOutput = ''
let browser
let page
let auditVolumeAttached = false

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
      const { stdout } = await execFile('docker', args)
      lines.push(`${what}:\n${stdout.trim() || '(no output)'}`)
    } catch (err) {
      lines.push(`${what} unavailable: ${err.message}`)
    }
  }
  return lines.join('\n')
}

const waitForText = async (text, timeout = 30000) => {
  try {
    await page.waitForFunction(
      (needle) => document.body.innerText.includes(needle),
      { timeout, polling: 100 },
      text,
    )
  } catch (err) {
    // The page's own words are the diagnostic: a timeout without them reads as
    // flakiness, with them it reads as whichever state the screen was actually in.
    throw new Error(`never saw "${text}"; the page says:\n${await bodyText()}`, { cause: err })
  }
}

const bodyText = () => page.evaluate(() => document.body.innerText)

const globalNavItems = () =>
  page.$$eval('nav.app-global-nav a', (links) => links.map((link) => link.innerText.trim()))

/**
 * True when the app shell's chrome is on screen. Sign-in and the wizard must
 * render without it: neither session can use nav or the tenant switcher, and a
 * screen that shows controls it cannot honour is the failure this console keeps
 * designing out (duf-saw).
 */
const shellChromePresent = () =>
  page.evaluate(() => Boolean(document.querySelector('.pf-v6-c-masthead, nav.pf-v6-c-nav')))

/** Finds the first matching element by trimmed text and clicks it. */
const clickByText = (selector, text) =>
  until(`clickable "${text}"`, () =>
    page.$$eval(
      selector,
      (elements, needle) => {
        const match = elements.find(
          (el) => el.innerText && el.innerText.trim().includes(needle) && !el.disabled,
        )
        if (!match) return false
        match.click()
        return true
      },
      text,
    ),
  )

/**
 * Clicks a select option whose text is EXACTLY the given string. The blank
 * project row renders as a bare dash (duf-4qr), which an includes() match
 * would find inside every em-dash on the page.
 */
const clickOptionExact = (text) =>
  until(`option "${text}"`, () =>
    page.$$eval(
      'li button',
      (elements, needle) => {
        const match = elements.find((el) => el.innerText && el.innerText.trim() === needle)
        if (!match) return false
        match.click()
        return true
      },
      text,
    ),
  )

/** Typeahead selection is type-then-click; the stable id remains on its toggle. */
const choosePickerOption = async (id, text) => {
  await page.waitForSelector(`${id}-input`)
  const expanded = await page.$eval(
    `${id}-input`,
    (input) => input.getAttribute('aria-expanded') === 'true',
  )
  if (!expanded) await page.click(id)
  await page.waitForFunction(
    (inputId) => document.querySelector(inputId)?.getAttribute('aria-expanded') === 'true',
    {},
    `${id}-input`,
  )
  await page.$eval(`${id}-input`, (input) => {
    input.focus()
    input.select()
  })
  await page.type(`${id}-input`, text)
  await clickOptionExact(text)
}

const pickerValue = (id) => page.$eval(`${id}-input`, (input) => input.value)

/**
 * The text of the table row naming `name`. Row-scoped rather than page-scoped
 * because several principals list at once and each carries its own controls, so
 * a page-wide match would read the wrong principal's state.
 */
const rowText = (name) =>
  page.$$eval('tr', (rows, needle) => {
    const match = rows.find((row) => row.innerText.includes(needle))
    return match ? match.innerText : ''
  }, name)

const rowCellText = (name, label) =>
  page.$$eval('tr', (rows, needle, dataLabel) => {
    const row = rows.find((candidate) => candidate.innerText.includes(needle))
    return row?.querySelector(`td[data-label="${dataLabel}"]`)?.innerText.trim() ?? ''
  }, name, label)

const keyringRows = () =>
  page.$$eval('table[aria-label="Encryption keyring"] tbody tr', (rows) =>
    rows.map((row) => ({
      purpose: row.querySelector('td[data-label="Purpose"]')?.innerText.trim() ?? '',
      active: row.querySelector('td[data-label="Active"]')?.innerText.trim() ?? '',
      retained: row.querySelector('td[data-label="Retained"]')?.innerText.trim() ?? '',
      kekRef: row.querySelector('td[data-label="KEK version"]')?.innerText.trim() ?? '',
    })))

const rowCellBytes = (name, label) =>
  page.$$eval('tr', (rows, needle, dataLabel) => {
    const row = rows.find((candidate) => candidate.innerText.includes(needle))
    const raw = row?.querySelector(`td[data-label="${dataLabel}"] span[data-bytes]`)
      ?.getAttribute('data-bytes') ?? ''
    return /^\d+$/.test(raw) ? Number(raw) : null
  }, name, label)

/** A build table uses one tbody per expandable row, so detail stays row-scoped. */
const buildGroupText = (name) =>
  page.$$eval('tbody', (groups, needle) => {
    const match = groups.find((group) => group.innerText.includes(needle))
    return match ? match.innerText : ''
  }, name)

const facetItems = async (ariaLabel) => {
  await page.waitForSelector(`nav[aria-label="${ariaLabel}"] button[role="tab"]`)
  return page.$$eval(
    `nav[aria-label="${ariaLabel}"] button[role="tab"]`,
    (buttons) => buttons.map((button) => ({
      label: button.querySelector('.pf-v6-c-tabs__item-text')?.textContent.trim() ?? '',
      count: button.querySelector('.pf-v6-c-badge')?.textContent.trim() ?? '',
    })),
  )
}

// Retried as a unit: the rail's tabs paint after navigation, so a one-shot
// query races the render (observed intermittently as "facet not found").
const clickFacet = (ariaLabel, label) =>
  until(`the "${label}" facet in ${ariaLabel}`, () =>
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

// The nav paints after a navigation; wait on it rather than racing the render
// (the suite's no-fixed-sleeps rule — every wait is a condition with a deadline).
const facetHeading = async (ariaLabel) => {
  const selector = `nav[aria-label="${ariaLabel}"] > .registry-facet-heading`
  await page.waitForSelector(selector)
  return page.$eval(selector, (heading) => heading.textContent.trim())
}

/** Clicks a control inside the row naming `name`, never the first on the page. */
const clickInRow = (name, label) =>
  until(`"${label}" in the ${name} row`, () =>
    page.$$eval('tr', (rows, needle, wanted) => {
      const row = rows.find((r) => r.innerText.includes(needle))
      if (!row) return false
      const button = [...row.querySelectorAll('button')].find(
        (b) => b.innerText.trim().includes(wanted) && !b.disabled,
      )
      if (!button) return false
      button.click()
      return true
  }, name, label))

/** Clicks a visible, enabled button in the active modal. */
const clickInModal = (label) =>
  until(`"${label}" in the active modal`, () =>
    page.$$eval('[role="dialog"] button', (buttons, wanted) => {
      const button = buttons.find(
        (candidate) => candidate.innerText.trim() === wanted && !candidate.disabled,
      )
      if (!button) return false
      button.click()
      return true
    }, label))

const toggleRow = (name) =>
  until(`expand control in the ${name} row`, () =>
    page.$$eval('tr', (rows, needle) => {
      const row = rows.find((candidate) => candidate.innerText.includes(needle))
      if (!row) return false
      const toggle = row.querySelector('button[aria-expanded]') ?? row.querySelector('button')
      if (!toggle || toggle.disabled) return false
      toggle.click()
      return true
    }, name))

const visibleHealthText = (path) =>
  until(`visible health for ${path}`, () =>
    page.$eval(`dl[aria-label="Health for ${path}"]`, (list) =>
      list.getBoundingClientRect().height > 0 ? list.innerText : ''),
  )

/** The role options the create form currently offers, in order. */
const roleOptions = () =>
  page.$$eval('#principal-role option', (options) => options.map((option) => option.value))

/**
 * Reads the one-time credential from the active modal: a uuid client id and a
 * long secret, both in read-only clipboard inputs.
 */
const readCredentialCard = () =>
  until('the issued credentials to be readable', () =>
    page.evaluate(() => {
      const modal = document.querySelector('[role="dialog"]')
      if (!modal) return null
      const values = [...modal.querySelectorAll('input')].map((i) => i.value)
      const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
      const clientID = values.find((v) => uuid.test(v))
      const secret = values.find((v) => v && v.length >= 40 && !uuid.test(v))
      return clientID && secret ? { clientID, secret } : null
    }),
  )

// Destructive modals demand the resource name typed before their danger
// button arms (duf-fcg6.5); the input id is the shared component's.
const typeToConfirm = async (expected) => {
  await page.waitForSelector('#typed-confirm-modal-input')
  await page.type('#typed-confirm-modal-input', expected)
}

const buttonDisabled = (text) =>
  page.$$eval(
    'button',
    (elements, needle) => {
      const match = elements.find((el) => el.innerText.trim().includes(needle))
      return match ? match.disabled : null
    },
    text,
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

// A tiny private filesystem provides a reversible, understood ENOSPC on an
// already-open descriptor — permissions cannot, because they are checked at
// open, not at write, so chmod would not revoke the FileSink's existing
// O_APPEND descriptor. macOS builds the volume from a disk image; Linux (the
// CI runner) mounts a bounded tmpfs, which requires passwordless sudo.
async function attachAuditVolume() {
  mkdirSync(auditVolumeMount)
  if (process.platform === 'darwin') {
    await execFile('/usr/bin/hdiutil', [
      'create', '-size', '16m', '-fs', 'HFS+', '-volname', `dufflebag-smoke-${process.pid}`,
      auditVolumeImage,
    ])
    await execFile('/usr/bin/hdiutil', [
      'attach', '-nobrowse', '-mountpoint', auditVolumeMount, auditVolumeImage,
    ])
  } else {
    const sudo = await execFile('sudo', ['-n', 'true']).then(() => true, () => false)
    assert.ok(sudo, 'the audit-volume fixture needs passwordless sudo here to mount a bounded tmpfs')
    await execFile('sudo', ['-n', 'mount', '-t', 'tmpfs',
      '-o', `size=16m,mode=0700,uid=${process.getuid()},gid=${process.getgid()}`,
      'tmpfs', auditVolumeMount,
    ])
  }
  auditVolumeAttached = true
}

async function detachAuditVolume() {
  if (process.platform === 'darwin') {
    await execFile('/usr/bin/hdiutil', ['detach', auditVolumeMount]).catch(() => {})
  } else {
    await execFile('sudo', ['-n', 'umount', auditVolumeMount]).catch(() => {})
  }
}

// Fill the target's isolated filesystem down to its last byte. Padding the
// target to an allocation boundary first means its next append needs a new
// block rather than fitting into slack already owned by the file.
function exhaustAuditVolume() {
  const blockSize = statfsSync(auditVolumeMount).bsize
  const remainder = statSync(auditFull).size % blockSize
  if (remainder !== 0) {
    appendFileSync(auditFull, Buffer.alloc(blockSize - remainder, '\n'))
  }

  const filler = openSync(auditVolumeFiller, 'w', 0o600)
  try {
    for (let size = 1024 * 1024; size >= 1; size = Math.floor(size / 2)) {
      const chunk = Buffer.alloc(size)
      for (;;) {
        try {
          assert.ok(writeSync(filler, chunk) > 0, 'filling the audit volume made no progress')
        } catch (err) {
          assert.equal(err.code, 'ENOSPC', `filling the audit volume failed unexpectedly: ${err}`)
          break
        }
      }
    }
  } finally {
    closeSync(filler)
  }
}

const waitForExit = (child) =>
  new Promise((resolve) => child.once('exit', (code) => resolve(code)))

// --- API helpers over plain HTTP: the console does not use hcp-sdk-go, so no
// --- TLS is needed anywhere in this test.

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

// The Bag Drop mirror destination the one-step Enable resolves against. The
// adapter refuses plain http, so this terminates TLS on loopback with a
// throwaway openssl certificate the config trusts through ca_chain, and
// answers the two calls resolution makes: a client-credentials token and a
// bucket-list probe.
async function startMirrorStub() {
  const work = mkdtempSync(join(tmpdir(), 'dufflebag-mirror-'))
  await execFile('openssl', [
    'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
    '-keyout', join(work, 'key.pem'), '-out', join(work, 'cert.pem'),
    '-days', '2', '-nodes', '-subj', '/CN=127.0.0.1',
    '-addext', 'subjectAltName=IP:127.0.0.1',
  ])
  const server = https.createServer(
    { key: readFileSync(join(work, 'key.pem')), cert: readFileSync(join(work, 'cert.pem')) },
    (request, response) => {
      if (request.method === 'POST' && request.url === '/oauth2/token') {
        response.writeHead(200, { 'Content-Type': 'application/json' })
        response.end(JSON.stringify({ access_token: 'smoke-mirror-token' }))
        return
      }
      if (request.method === 'GET' && request.url.startsWith('/packer/2023-01-01/organizations/')) {
        response.writeHead(200, { 'Content-Type': 'application/json' })
        response.end(JSON.stringify({ buckets: [] }))
        return
      }
      response.writeHead(404)
      response.end()
    },
  )
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  return {
    url: `https://127.0.0.1:${server.address().port}`,
    caChain: readFileSync(join(work, 'cert.pem'), 'utf8'),
    close: () =>
      new Promise((resolve) => {
        server.close(resolve)
        rmSync(work, { recursive: true, force: true })
      }),
  }
}

// A loopback port that answers nothing: bound, read, and released, so an
// Enable pointed at it fails with connection refused rather than a timeout.
async function closedPort() {
  const probe = net.createServer()
  await new Promise((resolve) => probe.listen(0, '127.0.0.1', resolve))
  const port = probe.address().port
  await new Promise((resolve) => probe.close(resolve))
  return port
}

async function scanAttribution(token, path) {
  const response = await fetch(`${base}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  const text = await response.text()
  assert.ok(response.ok, `GET ${path} answered ${response.status}: ${text}`)
  const adapter = response.headers.get('Dufflebag-Scan-Adapter')
  if (!adapter) return null
  const count = (name) => Number(response.headers.get(`Dufflebag-Scan-${name}`) ?? 0)
  return {
    adapter,
    observedAt: response.headers.get('Dufflebag-Scan-Observed-At'),
    submitted: count('Submitted'),
    unsupported: count('Unsupported'),
    unversioned: count('Unversioned'),
    invalid: count('Invalid'),
  }
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

before(async () => {
  assert.ok(serverBinary, 'SMOKE_BIN must point at a built dufflebag binary (run via `make test-smoke`)')
  assert.ok(
    existsSync(chrome),
    `no Chrome at ${chrome} — set SMOKE_CHROME to a Chrome/Chromium binary`,
  )

  // Postgres, exactly as test-integration runs it: postgres:17-alpine, with a
  // random host port so concurrent runs cannot collide. Lock waits over one
  // second are logged to the container so an intermittent server-side stall
  // (duf-3wo8: a bucket DELETE once hung ~5 minutes, unreproduced across 12
  // instrumented runs) self-diagnoses in `docker logs` at its next natural
  // occurrence instead of dying as an opaque fetch failure.
  await execFile('docker', [
    'run', '-d', '--rm', '--name', container,
    '-e', 'POSTGRES_PASSWORD=postgres',
    '-e', 'POSTGRES_DB=dufflebag',
    '-p', '127.0.0.1::5432',
    'postgres:17-alpine',
    '-c', 'log_lock_waits=on',
    '-c', 'deadlock_timeout=1s',
    '-c', 'log_min_duration_statement=5000',
  ])
  const { stdout: portLine } = await execFile('docker', ['port', container, '5432/tcp'])
  const pgPort = portLine.trim().split('\n')[0].split(':').pop()
  await until('postgres to accept connections', async () => {
    await execFile('docker', ['exec', container, 'pg_isready', '-U', 'postgres', '-d', 'dufflebag'])
    return true
  }, 60000, 250)

  // A live Vault transit engine supplies the wrapping key for the serving
  // process. The key itself is created on the server's first encrypt.
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

  // Ceph, because the console shows SBOM packages and SBOMs live in object
  // storage. Without it every upload here answers 503 and the package
  // assertions fail — which is what happened when object storage landed and
  // this suite was not given a bucket.
  await execFile('docker', [
    'run', '-d', '--rm', '--name', objectContainer,
    '-p', '127.0.0.1::8000',
    objectStorageImage,
  ])
  const { stdout: objectPortLine } = await execFile('docker', ['port', objectContainer, '8000/tcp'])
  objectPort = objectPortLine.trim().split('\n')[0].split(':').pop()
  // The image carries its own healthcheck, which is the only thing that knows
  // when Ceph is ready. Probing the published port is not enough and fails in
  // two distinct ways: docker accepts the connection before RGW listens, and
  // RGW serves HTTP before the cluster can run an admin command.
  try {
    await until('Ceph to report healthy', async () => {
      const { stdout } = await execFile('docker', [
        'inspect', '-f', '{{.State.Health.Status}}', objectContainer,
      ])
      return stdout.trim() === 'healthy'
    }, 300000, 2000)
  } catch (err) {
    throw new Error(`${err.message}\n${await objectStorageDiagnosis(objectContainer)}`)
  }
  await execFile('docker', [
    'exec', objectContainer, 'radosgw-admin', 'user', 'create',
    '--uid=dufflebag-smoke', '--display-name=dufflebag smoke',
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

  // test-scanner builds this exact recorded-fixture server before the smoke
  // lane in CI. The smoke harness only runs it: building images belongs to the
  // parent lane, and keeping one reusable image proves both tests drive the
  // same no-egress provider contract.
  await execFile('docker', ['image', 'inspect', scannerImage]).catch((err) => {
    throw new Error(
      `missing ${scannerImage}; parent must run: ` +
      `docker build -f Containerfile.scanner --target stub -t ${scannerImage} .`,
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

  // First boot with the privileged role applies the migrations and is then
  // DELIBERATELY refused, because a superuser bypasses row-level security.
  // This is the documented bring-up procedure (docs/local-end-to-end.md §3),
  // so the smoke test exercises it rather than working around it.
  const adminURL = `postgres://postgres:postgres@127.0.0.1:${pgPort}/dufflebag?sslmode=disable`
  await until('migrations to apply and the RLS gate to refuse the superuser', async () => {
    serverOutput = ''
    const migrator = startServer({
      DFBG_DATABASE_URL: adminURL,
      DFBG_TOKEN_SIGNING_KEY: signingKey,
    })
    await waitForExit(migrator)
    return serverOutput.includes('refusing to serve')
  }, 60000, 500)

  // The unprivileged serving role, as the documentation creates it.
  await execFile('docker', [
    'exec', container, 'psql', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'dufflebag',
    '-c', "CREATE ROLE dufflebag_app LOGIN PASSWORD 'app' NOSUPERUSER NOBYPASSRLS",
    '-c', 'GRANT USAGE ON SCHEMA public TO dufflebag_app',
    '-c', 'GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dufflebag_app',
  ])

  await attachAuditVolume()
  writeFileSync(auditFull, '', { mode: 0o600 })

  writeFileSync(auditLinkTarget, 'untouched', { mode: 0o600 })
  symlinkSync(auditLinkTarget, auditLink)

  serverPort = await freePort()
  base = `http://127.0.0.1:${serverPort}`
  serverOutput = ''
  server = startServer({
    DFBG_DATABASE_URL: `postgres://dufflebag_app:app@127.0.0.1:${pgPort}/dufflebag?sslmode=disable`,
    DFBG_HTTP_ADDR: `127.0.0.1:${serverPort}`,
    DFBG_KEY_PROVIDER: 'vault',
    DFBG_VAULT_ADDR: vaultBase,
    DFBG_VAULT_TOKEN: vaultToken,
    DFBG_OBJECT_STORAGE_ENDPOINT: `http://127.0.0.1:${objectPort}`,
    DFBG_OBJECT_STORAGE_REGION: 'us-east-1',
    DFBG_OBJECT_STORAGE_BUCKET: objectStorageBucket,
    DFBG_OBJECT_STORAGE_ACCESS_KEY: 'testaccess',
    DFBG_OBJECT_STORAGE_SECRET_KEY: 'testsecret',
    DFBG_SCANNER_ADAPTER: 'osv',
    DFBG_SCANNER_ENDPOINT: scannerBase,
    DFBG_SCANNER_INTERVAL: '2s',
  })
  await until('the server to answer', async () => {
    const response = await fetch(`${base}/`)
    return response.status === 200
  }, 60000, 250)

  browser = await puppeteer.launch({
    executablePath: chrome,
    headless: true,
    // Sandboxing is disabled for CI runners; locally it makes no difference to
    // what is being asserted.
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  })
  page = await browser.newPage()
  page.setDefaultTimeout(30000)
  // The harness browser's own prefers-color-scheme is environment-dependent
  // (this machine's headless Chrome prefers dark). The suite's fixtures pin
  // light-theme values, so the baseline is emulated deterministically; the
  // theme-toggle steps then exercise the switch from a known state.
  await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'light' }])
  // Wide enough that the sidebar navigation is expanded rather than behind the
  // hamburger toggle.
  await page.setViewport({ width: 1440, height: 900 })
})

after(async () => {
  if (browser) await browser.close().catch(() => {})
  if (server && server.exitCode === null) {
    const exited = waitForExit(server)
    server.kill('SIGTERM')
    await Promise.race([exited, new Promise((r) => setTimeout(r, 10000))])
    if (server.exitCode === null) server.kill('SIGKILL')
  }
  await execFile('docker', ['rm', '-f', container]).catch(() => {})
  await execFile('docker', ['rm', '-f', objectContainer]).catch(() => {})
  await execFile('docker', ['rm', '-f', vaultContainer]).catch(() => {})
  await execFile('docker', ['rm', '-f', scannerContainer]).catch(() => {})
  if (auditVolumeAttached) await detachAuditVolume()
  rmSync(auditDir, { recursive: true, force: true })
})

test('the console works end to end, from first run to a seeded tenancy', async (t) => {
  let credentials

  await t.test('a fresh instance lands on the wizard, not on sign-in', async () => {
    await page.goto(base, { waitUntil: 'domcontentloaded' })
    // /sys/health answered 501, so the wizard IS the landing screen: no
    // sign-in form, no first-run link to guess at (duf-2so).
    await waitForText('Whoever completes this flow first owns the deployment')
    // The placeholder console must never reach a binary again (duf-6d0).
    assert.doesNotMatch(await bodyText(), /web console was not built/)
    assert.doesNotMatch(await bodyText(), /Log in|First run\?/)
  })

  await t.test('the wizard claims the uninitialized instance', async () => {
    assert.equal(await shellChromePresent(), false, 'the wizard rendered inside the app shell')
    assert.equal(await shellChromePresent(), false, 'the wizard rendered inside the app shell')
    await clickByText('button', 'Initialize this instance')
    await waitForText('Administrative credentials')
    assert.equal(await shellChromePresent(), false, 'the credential step rendered inside the app shell')
  })

  await t.test('credentials are shown once, and Continue is gated on acknowledgement', async () => {
    credentials = await until('the minted credentials to be readable', () =>
      page.evaluate(() => {
        const text = document.body.innerText
        const values = [...document.querySelectorAll('input')].map((i) => i.value)
        const uuid = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i
        const clientID =
          values.find((v) => uuid.test(v)) ?? (text.match(uuid) ?? [])[0]
        // The recovery share also sits in a long readonly input, so the
        // secret is whichever long value is NOT a share.
        const secret = values.find(
          (v) => v && v.length >= 40 && !uuid.test(v) && !v.startsWith('dfbg-recovery-'),
        )
        return clientID && secret ? { clientID, secret } : null
      }),
    )
    // The recovery share is displayed in the same shown-once step, behind the
    // same acknowledgement (ADR-0024, duf-9rr).
    const share = await page.evaluate(() =>
      [...document.querySelectorAll('input, textarea')]
        .map((i) => i.value)
        .find((v) => v && v.startsWith('dfbg-recovery-1:')),
    )
    assert.ok(share, 'the wizard did not display a recovery share')
    const acknowledgement = await page.evaluate(
      () => document.querySelector('label[for="init-stored"]')?.innerText ?? '',
    )
    assert.match(acknowledgement, /recovery share/, 'the acknowledgement does not name the recovery share')
    assert.equal(await buttonDisabled('Continue to organization'), true)
    await page.click('#init-stored')
    await until('Continue to enable', async () => (await buttonDisabled('Continue to organization')) === false)
    await clickByText('button', 'Continue to organization')
  })

  await t.test('step 3 creates the named organization with the minted credential', async () => {
    await waitForText('Name your organization')
    assert.equal(await buttonDisabled('Create organization and continue'), true)
    await page.type('#organization-name', wizardOrganizationName)
    await clickByText('button', 'Create organization and continue')
  })

  await t.test('step 4 creates the named project in that organization', async () => {
    await waitForText('Name your first project')
    await waitForText(wizardOrganizationName)
    assert.equal(await buttonDisabled('Create project and open the console'), true)
    await page.type('#project-name', wizardProjectName)
    await clickByText('button', 'Create project and open the console')
  })

  await t.test('completing the wizard lands in the console, already authenticated', async () => {
    // No sign-in form between the wizard and the shell: the wizard proved the
    // credential against /oauth2/token to create the tenancy, so acknowledging
    // and finishing IS the sign-in (duf-2so).
    await waitForText('Sign out')
    assert.doesNotMatch(await bodyText(), /Log in/)
    await until('all root navigation items to appear', async () =>
      (await globalNavItems()).length === 7)
    assert.deepEqual(
      await globalNavItems(),
      ['Buckets', 'Principals', 'Audit', 'Encryption', 'Bag Drop', 'Webhooks', 'Instance'],
    )
    // The themed background paints on the PatternFly page element, not body.
    // The sidebar is asserted separately: it once pinned its surface to a
    // light-scoped token and stayed white in dark mode.
    const pageBackground = () => page.$eval(
      '.pf-v6-c-page', (el) => getComputedStyle(el).backgroundColor)
    const sidebarBackground = () => page.$eval(
      '.pf-v6-c-page__sidebar', (el) => getComputedStyle(el).backgroundColor)
    const lightBackground = await pageBackground()
    const lightSidebar = await sidebarBackground()
    await page.click('button[aria-label="Switch to dark theme"]')
    assert.equal(await page.evaluate(() =>
      document.documentElement.classList.contains('pf-v6-theme-dark')), true)
    assert.notEqual(await pageBackground(), lightBackground)
    assert.notEqual(await sidebarBackground(), lightSidebar)
    await page.click('button[aria-label="Switch to light theme"]')
    assert.equal(await page.evaluate(() =>
      document.documentElement.classList.contains('pf-v6-theme-dark')), false)
    assert.equal(await pageBackground(), lightBackground)
    assert.equal(await sidebarBackground(), lightSidebar)
  })

  await t.test('a reload keeps the session', async () => {
    // The boot exchange at /sys/session hands the reload its token back
    // (ADR-0021): no bounce to sign-in, no credentials retyped.
    await page.reload({ waitUntil: 'networkidle0' })
    await waitForText('Sign out')
    assert.doesNotMatch(await bodyText(), /Log in/)
  })

  await t.test('the header names the tenancy the wizard created, never a fixture', async () => {
    // The wizard created these names through the authenticated platform API,
    // and the platform session's picker auto-selects the sole tenancy.
    await until('the organisation toggle to name the real tenancy', async () =>
      (await pickerValue('#tenant-organization')) === wizardOrganizationName)
    assert.equal(await pickerValue('#tenant-project'), wizardProjectName)
    assert.doesNotMatch(await bodyText(), /orbital-home|lab-registry/)
  })

  await t.test('the data screens show the real, scoped state', async () => {
    await waitForText('No buckets yet')
  })

  await t.test('a bucket published from outside appears without a reload', async () => {
    // The empty console is the awaiting-change state: demo-publish landing a
    // bucket must surface in the masthead picker and the screen by polling,
    // never by hard refresh (duf-ear2).
    const rootToken = await tokenFor(credentials.clientID, credentials.secret)
    const { organizations } = await api(rootToken, 'GET', '/api/v1/organizations')
    const wizardOrg = organizations.find((o) => o.name === wizardOrganizationName)
    const { projects } = await api(
      rootToken, 'GET', `/api/v1/organizations/${wizardOrg.id}/projects`)
    await api(
      rootToken, 'PUT',
      `/packer/2023-01-01/organizations/${wizardOrg.id}/projects/${projects[0].id}/buckets`,
      { name: 'first-published', description: 'landed from outside the console' },
    )
    await waitForText('first-published')
    await until('the masthead picker to grow its toggle', () =>
      page.$('#tenant-bucket').then((el) => el !== null))
  })

  await t.test('the Instance build card matches the authenticated endpoint', async () => {
    const rootToken = await tokenFor(credentials.clientID, credentials.secret)
    const instance = await api(rootToken, 'GET', '/api/v1/instance')
    assert.equal(instance.version, 'dev')
    assert.equal(instance.commit, 'unknown')
    assert.deepEqual(instance.api_versions, [
      '/packer/2023-01-01', '/resource-manager/2019-12-10', '/api/v1',
    ])
    assert.ok(instance.initialized_at, 'the one-shot claim did not record initialized_at')
    assert.equal(instance.store, true)
    assert.equal(instance.object_storage, 'ok')
    assert.equal(instance.encryption, 'ok')
    assert.equal(instance.audit, 'disabled')
    assert.deepEqual(instance.scanner, { configured: true, adapter: 'osv' })

    await clickByText('a', 'Instance')
    await waitForText('Build')
    for (const supplied of ['dev', 'unknown', '/packer/2023-01-01']) {
      await waitForText(supplied)
    }
    // Initialized renders formatted (duf-fcg6.6); the semantic time element
    // still carries the API's full-precision value.
    await until('the initialized time element to carry the API value', () =>
      page.$$eval('time', (times, iso) =>
        times.some((t) => t.getAttribute('datetime') === iso), instance.initialized_at))
    await waitForText('Scanner')
    await waitForText('Adapter')
    await waitForText('osv')
  })

  await t.test('principals are listed exactly where the session stands', async () => {
    // The session stands at the project created by the wizard, so the
    // listing is that PROJECT's principals — empty — and never the
    // platform-scoped bootstrap principal (exact scope, not subtree, duf-4qr).
    await clickByText('a', 'Principals')
    await waitForText('Service principals')
    await waitForText('No service principals are visible to you')
    assert.doesNotMatch(await bodyText(), /initial administrator/)
    // At project standing the form offers the four tenancy roles, never root.
    // Click-and-check as one retried unit: the empty-state button re-renders
    // across the listing's settle cycle, and a click landing in that window
    // can be lost (observed intermittently).
    await until('the create-principal form to open', async () => {
      if ((await bodyText()).includes('New service principal')) return true
      await page.$$eval('button', (buttons) => {
        const button = buttons.find(
          (candidate) => candidate.innerText.trim() === 'Create principal' && !candidate.disabled,
        )
        if (button) button.click()
      })
      return (await bodyText()).includes('New service principal')
    })
    assert.deepEqual(await roleOptions(), ['reader', 'builder', 'publisher', 'maintainer'])
    await clickByText('button', 'Cancel')
  })

  await t.test('the blank project row is the deliberate step up to organisation level', async () => {
    await choosePickerOption('#tenant-project', '—')
    await until('the project toggle to show the dash', async () =>
      (await pickerValue('#tenant-project')) === '—')
    // An empty organisation-level table explains which scope answered empty.
    await waitForText('No organisation-scoped principals')
    await waitForText('select one in the header')
    assert.doesNotMatch(await bodyText(), /initial administrator/)
    // Organisation standing still never offers root.
    // Same retried click-and-check as the project-standing step above.
    await until('the create-principal form to open', async () => {
      if ((await bodyText()).includes('New service principal')) return true
      await page.$$eval('button', (buttons) => {
        const button = buttons.find(
          (candidate) => candidate.innerText.trim() === 'Create principal' && !candidate.disabled,
        )
        if (button) button.click()
      })
      return (await bodyText()).includes('New service principal')
    })
    assert.deepEqual(await roleOptions(), ['reader', 'builder', 'publisher', 'maintainer'])
    await clickByText('button', 'Cancel')
  })

  let seeded
  await t.test('a tenancy seeded over the platform API exists to choose', async () => {
    const rootToken = await tokenFor(credentials.clientID, credentials.secret)
    const organization = await api(rootToken, 'POST', '/api/v1/organizations', { name: 'acme' })
    const project = await api(
      rootToken, 'POST', `/api/v1/organizations/${organization.id}/projects`, { name: 'widgets' },
    )
    // Creation mints no credential (duf-4ac), so each seeded principal is
    // issued one explicitly — the same two calls the console now makes.
    const withSecret = async (body) => {
      const created = await api(rootToken, 'POST', '/api/v1/principals', body)
      assert.equal(
        created.secret, undefined,
        'CreatePrincipal returned a secret; creation must mint none',
      )
      const issued = await api(
        rootToken, 'POST', `/api/v1/principals/${created.id}/secrets`, {},
      )
      assert.ok(issued.secret, `no secret issued for ${body.name}`)
      return { ...created, secret: issued.secret }
    }

    const principal = await withSecret({
      name: 'smoke-builder', role: 'builder', organization_id: organization.id,
    })
    const promoter = await withSecret({
      name: 'smoke-promoter', role: 'publisher',
      organization_id: organization.id, project_id: project.id,
    })
    const builderToken = await tokenFor(principal.client_id, principal.secret)
    await api(
      builderToken, 'PUT',
      `/packer/2023-01-01/organizations/${organization.id}/projects/${project.id}/buckets`,
      {
        name: 'smoke-images', description: 'seeded by the smoke test',
        labels: { team: 'platform', purpose: 'browser-proof' },
      },
    )
    await api(
      builderToken, 'PUT',
      `/packer/2023-01-01/organizations/${organization.id}/projects/${project.id}/buckets`,
      { name: 'zzz-newer', description: 'newer bucket for the default-order guard' },
    )
    seeded = { organization, project, principal, promoter }
  })

  await t.test('an organisation created after sign-in appears when the picker reopens', async () => {
    // Opening the picker refreshes the platform session in place: no sign-out,
    // credential entry or session replacement sits between creation and this
    // assertion. Open it before asking what its options say.
    await page.click('#tenant-organization')
    assert.equal(await until('the refreshed picker to list acme', () =>
      page.$$eval('li button', (options) =>
        options.some((option) => option.innerText.trim() === 'acme'))), true)
    // A successful refresh preserves the still-valid wizard selection. The
    // operator, not the arrival of a second row, chooses when to step up.
    assert.equal(
      await pickerValue('#tenant-organization'),
      wizardOrganizationName,
    )
    await choosePickerOption('#tenant-organization', 'All organisations (platform)')
    await until('the organisation toggle to show platform standing', async () =>
      (await pickerValue('#tenant-organization')) ===
        'All organisations (platform)')
    // The data screen states the gap rather than showing a silent empty table.
    await clickByText('a', 'Buckets')
    await waitForText('can view any organisation')
  })

  await t.test('nothing selected is platform standing: root only, platform principals only', async () => {
    await clickByText('a', 'Principals')
    await waitForText('Service principals')
    // The platform listing holds exactly the platform-scoped principals: the
    // bootstrap root, and never the org-scoped builder seeded into acme.
    await waitForText('initial administrator')
    await waitForText('platform')
    assert.doesNotMatch(await bodyText(), /smoke-builder/)
    // The bootstrap root holds exactly one secret, which is the state where
    // revoking it is refused — so the notice is present and the control is
    // genuinely disabled in a real browser, not merely marked so in markup
    // (Ben, 2026-08-02).
    await waitForText('A root principal must keep one secret that never expires')
    await page.click('tbody button[aria-label], tbody .pf-v6-c-table__toggle button')
    // Since duf-bd2, minting a token records the secret's last use — and this
    // root signed in through /oauth2/token to get here, so its secret honestly
    // shows a timestamp where it once said "never used".
    await until('the root secret shows its last use', async () => {
      const text = await bodyText()
      return !/never used/.test(text) && /20\d\d/.test(text)
    })
    assert.equal(
      await buttonDisabled('Revoke'), true,
      'a root with one secret must not offer a live Revoke',
    )
    // With nothing selected the only creatable role is root — the one legally
    // unscoped role, and the cheap second-root path.
    await clickByText('button', 'Create principal')
    await waitForText('New service principal')
    // The offered roles ARE the proof of standing; the prose that also said so
    // was removed 2026-08-02 and is deliberately not asserted in its place.
    assert.deepEqual(await roleOptions(), ['root'])
    await clickByText('button', 'Cancel')
  })

  await t.test('audit targets are managed and their real health is visible to root', async () => {
    const addTarget = async (path) => {
      await clickByText('button', 'Add target')
      await waitForText('New audit target')
      await page.type('#audit-target-path', path)
      await clickByText('button', 'Add target')
      await waitForText(path)
    }

    await clickByText('a', 'Audit')
    await waitForText('No audit targets are configured')

    // The API returns a safe category, and the screen turns it into useful
    // guidance without echoing a raw path or errno.
    await clickByText('button', 'Add target')
    await page.type('#audit-target-path', auditLink)
    await clickByText('button', 'Add target')
    await waitForText('The action was refused')
    await waitForText('Symlinks are refused for audit targets')
    await clickByText('button', 'Cancel')

    await addTarget(auditGoodOne)
    await addTarget(auditGoodTwo)
    await addTarget(auditFull)

    // The existing 16 MiB HFS+ ENOSPC fixture is also the storage oracle. Wait
    // for activation, then compare the browser's descriptor-backed raw values
    // with the real open file and mounted filesystem. The response audit record
    // lands after measurement, so allow one small record/allocation of drift.
    const measuredStorage = await until('the HFS+ audit target storage measurement', async () => {
      const current = await rowCellBytes(auditFull, 'Current file')
      const remaining = await rowCellBytes(auditFull, 'Space remaining')
      if (current !== null && remaining !== null) return { current, remaining }
      await page.reload({ waitUntil: 'domcontentloaded' })
      await waitForText(auditFull)
      return false
    })
    const actualCurrent = statSync(auditFull).size
    const volume = statfsSync(auditVolumeMount)
    const actualRemaining = volume.bavail * volume.bsize
    assert.ok(measuredStorage.current > 0, 'current open audit file was rendered as empty')
    assert.ok(measuredStorage.current <= actualCurrent, 'reported current file is newer than the real descriptor')
    assert.ok(actualCurrent - measuredStorage.current < 64 * 1024, 'current file measurement drift exceeds one audit record')
    assert.ok(measuredStorage.remaining > 0, 'fresh 16 MiB audit volume has no reported free space')
    assert.ok(measuredStorage.remaining <= 16 * 1024 * 1024, 'reported free space exceeds the fixture volume')
    assert.ok(
      Math.abs(measuredStorage.remaining - actualRemaining) < 128 * 1024,
      'reported free space does not match the fixture volume',
    )

    exhaustAuditVolume()

    // `auditFull` opened as a regular file before its private filesystem was
    // exhausted. Activation follows the create response's audit write,
    // so the browser's immediate list request can overtake it and truthfully
    // see a target which has not been written yet. Keep driving ordinary UI
    // requests until a later request hits all three sinks: two accept the pair
    // and this one's already-open descriptor returns ENOSPC, so the failing
    // target remains real broker state, not an intercepted response.
    await waitForText('Three audit targets are already configured')
    assert.equal(await buttonDisabled('Add target'), true, 'the fourth target control is live')
    const failingRow = await until('the full audit target to report failing', async () => {
      await clickByText('a', 'Principals')
      await waitForText('Service principals')
      await clickByText('a', 'Audit')
      await waitForText(auditFull)
      const text = await rowText(auditFull)
      return /failing/.test(text) ? text : false
    })
    assert.match(failingRow, /failing/)
    assert.equal(await rowCellText(auditFull, 'Space remaining'), '0 B')
    await toggleRow(auditFull)
    let health = await visibleHealthText(auditFull)
    // Timestamps render locale-formatted (duf-fcg6.6); a real date carries a year.
    assert.match(health, /Since\s+.*\d{4}/)
    assert.match(health, /Consecutive failures\s+[1-9]\d*/)
    assert.match(health, /Cumulative failures\s+[1-9]\d*/)
    assert.match(health, /Last failure\s+.*\d{4}/)

    // Freeing the isolated filesystem makes the next ordinary audited request
    // recover it. The current streak resets, while lifetime history stays.
    rmSync(auditVolumeFiller)
    // Reload only while the browser is on the Audit route. Besides supplying
    // the later audited request, a document reload discards the table's local
    // expanded-row state before another refresh can begin. This root was
    // deliberately already at platform standing, so restoring its session
    // preserves the expected unselected organisation.
    const healthyRow = await until('the full audit target to recover', async () => {
      await page.reload({ waitUntil: 'domcontentloaded' })
      await page.waitForFunction(
        (path) => document.body.innerText.includes(path),
        { timeout: 5000, polling: 100 },
        auditFull,
      )
      const text = await rowText(auditFull)
      return /healthy/.test(text) ? text : false
    })
    assert.match(healthyRow, /healthy/)
    assert.notEqual(await rowCellText(auditFull, 'Space remaining'), '0 B')
    await toggleRow(auditFull)
    health = await visibleHealthText(auditFull)
    assert.match(health, /Since\s+—/)
    assert.match(health, /Consecutive failures\s+0/)
    assert.match(health, /Cumulative failures\s+[1-9]\d*/)
    assert.match(health, /Last failure\s+.*\d{4}/)
    await toggleRow(auditFull)
    await until('the full audit target health row to collapse', () =>
      page.$eval(`dl[aria-label="Health for ${auditFull}"]`, (list) =>
        list.getBoundingClientRect().height === 0))

    // With three targets the explanation is present; removing one makes both
    // it and the disabled state disappear together.
    await clickInRow(auditGoodTwo, 'Remove')
    // Non-last removal confirms in the typed modal like every other
    // destructive action (duf-et4h.4): the target path arms the button.
    await waitForText(`Remove ${auditGoodTwo}?`)
    await typeToConfirm(auditGoodTwo)
    await clickInModal('Remove target')
    await until('the second target to disappear', async () => !(await bodyText()).includes(auditGoodTwo))
    // Removal triggers a reload, and while it is in flight the form is not
    // rendered at all — so the re-enabled Add control is a condition to wait
    // on, never an instant assertion (a fast machine hides the window).
    await until('the limit explanation to clear and Add target to re-enable', async () =>
      !(await bodyText()).includes('Three audit targets are already configured') &&
      (await buttonDisabled('Add target')) === false)

    // Exhaust the recovered target again, then remove the remaining healthy
    // sink. The broker is genuinely degraded while the database remains healthy.
    exhaustAuditVolume()
    await clickInRow(auditGoodOne, 'Remove')
    await waitForText(`Remove ${auditGoodOne}?`)
    await typeToConfirm(auditGoodOne)
    await clickInModal('Remove target')
    await until('the Audit screen to report its degraded load failure', async () => {
      await page.click('a[href="/principals"]')
      await page.waitForFunction(
        () => window.location.pathname === '/principals',
        { timeout: 5000, polling: 100 },
      )
      await page.click('a[href="/audit"]')
      await page.waitForFunction(
        () => window.location.pathname === '/audit',
        { timeout: 5000, polling: 100 },
      )
      await page.waitForFunction(
        (path) => {
          const text = document.body.innerText
          return text.includes('Audit targets could not be loaded') || text.includes(path)
        },
        { timeout: 5000, polling: 100 },
        auditFull,
      )
      return (await bodyText()).includes('Audit targets could not be loaded')
    })
    await waitForText('Audit targets could not be loaded')
    await clickByText('button', 'Sign out')
    await waitForText('Audit recording is degraded')
    await page.reload({ waitUntil: 'domcontentloaded' })
    await waitForText('Audit recording is degraded')
    await waitForText('Log in')
    assert.doesNotMatch(await bodyText(), /instance database is unavailable/i)
    assert.match(await bodyText(), /database answered/)

    // Freeing space does not itself clear sink health. Drive an unauthenticated
    // but audited API request through the sole target, then observe the later
    // successful write through the exempt health endpoint. Static console
    // assets remain exempt and the browser stays on its usable sign-in screen.
    rmSync(auditVolumeFiller)
    await until('the sole target to recover after a later audited write', async () => {
      const trigger = await fetch(`${base}/api/v1/audit/targets`)
      await trigger.arrayBuffer()
      if (trigger.status !== 401) return false
      const response = await fetch(`${base}/sys/health`)
      const body = await response.json()
      return response.status === 200 && body.database === true && body.audit === 'ok'
    })

    await page.type('#client-id', credentials.clientID)
    await page.type('#client-secret', credentials.secret)
    await clickByText('button', 'Log in')
    await waitForText('Sign out')
    await clickByText('a', 'Audit')
    await waitForText(auditFull)

    // The server permits deleting the last target, so the row offers the
    // action and the console confirms its materially different consequence.
    await clickInRow(auditFull, 'Remove')
    await waitForText('Remove the last audit target?')
    await waitForText('stops this instance recording audit events entirely')
    await clickByText('button', 'Cancel')
    assert.match(await bodyText(), new RegExp(auditFull.replaceAll('/', '\\/')))
    await clickInRow(auditFull, 'Remove')
    await typeToConfirm(auditFull)
    await clickByText('button', 'Remove last target')
    await waitForText('No audit targets are configured')

    // The browser can render the list reload before the preceding delete's
    // response record has finished appending. Poll the real files until that
    // later write lands, then make the original record assertions unchanged.
    const records = await until('all audit target response records to reach their sinks', () => {
      const current = [auditGoodOne, auditGoodTwo, auditFull].flatMap((path) => {
        const raw = readFileSync(path, 'utf8').trim()
        if (!raw) return []
        return raw.split('\n').flatMap((line) => {
          try { return [JSON.parse(line)] } catch { return [] }
        })
      })
      const successful = (operation) => current.filter((record) =>
        record.kind === 'response' && record.operation === operation &&
        record.outcome === 'success')
      return successful('audit_target.create').length >= 1 &&
        successful('audit_target.delete').length >= 3 ? current : false
    })
    const successfulResponses = (operation) => records.filter((record) =>
      record.kind === 'response' && record.operation === operation && record.outcome === 'success')
    assert.ok(
      successfulResponses('audit_target.create').length >= 1,
      'console target creation produced no successful response audit record',
    )
    assert.ok(
      successfulResponses('audit_target.delete').length >= 3,
      'the three console removals did not each produce a successful response audit record',
    )
  })

  await t.test('the keyring is rewrapped and rotated from the console, and degraded health warns before failing', async () => {
    const purposes = ['audit_hmac', 'integrity', 'payload', 'token_signing']
    const hasPurposes = (rows) =>
      [...new Set(rows.map((row) => row.purpose))].sort().join(',') === purposes.join(',')

    await clickByText('a', 'Encryption')
    // One row per purpose, always: the display collapses versions in place
    // rather than growing a row per rotation.
    await until('four fresh keyring rows wrapped under v1', async () => {
      const rows = await keyringRows()
      return rows.length === 4 && hasPurposes(rows) &&
        rows.every((row) => row.active === '1' && row.retained === '1' && row.kekRef === 'v1')
    })

    await vaultWrite('/v1/transit/keys/dufflebag/rotate', {})
    await clickByText('button', 'Rewrap keyring')
    await typeToConfirm('rewrap')
    await clickInModal('Rewrap keyring')
    await until('every keyring row to be rewrapped under v2', async () => {
      const rows = await keyringRows()
      return rows.length === 4 && rows.every((row) => row.kekRef === 'v2')
    })

    await clickByText('button', 'Rotate keys')
    await waitForText('Rotate every encryption key?')
    await typeToConfirm('rotate')
    await clickInModal('Rotate keys')
    await until('rotation to bump the active version in place', async () => {
      const rows = await keyringRows()
      return rows.length === 4 && hasPurposes(rows) &&
        rows.every((row) => row.active === '2' && row.retained === '2' && row.kekRef === 'v2')
    })

    await vaultWrite('/v1/transit/keys/dufflebag/rotate', {})
    await vaultWrite('/v1/transit/keys/dufflebag/config', { min_decryption_version: 3 })
    await clickByText('button', 'Rewrap keyring')
    await typeToConfirm('rewrap')
    await clickInModal('Rewrap keyring')
    await waitForText('The action was refused')
    await waitForText('The key service refused or was unreachable')
    await page.reload({ waitUntil: 'domcontentloaded' })
    await waitForText('The key service could not unwrap the keyring at its last heartbeat')
    assert.equal(
      await buttonDisabled('Rewrap keyring'), false,
      'degraded state disabled the recovery affordance',
    )

    await vaultWrite('/v1/transit/keys/dufflebag/config', { min_decryption_version: 1 })
    await clickByText('button', 'Rewrap keyring')
    await typeToConfirm('rewrap')
    await clickInModal('Rewrap keyring')
    await until('every retained keyring row to be rewrapped under v3', async () => {
      const rows = await keyringRows()
      return rows.length === 4 &&
        rows.every((row) => row.retained === '2' && row.kekRef === 'v3')
    })
    await until('the degraded warning to disappear', async () =>
      !(await bodyText()).includes(
        'The key service could not unwrap the keyring at its last heartbeat',
      ))

    const health = await fetch(`${base}/sys/health`)
    const healthBody = await health.json()
    assert.equal(health.status, 200)
    assert.equal(healthBody.encryption, 'ok')
  })

  await t.test('the picker drives the data screens to the seeded tenancy', async () => {
    await choosePickerOption('#tenant-organization', 'acme')
    await until('the organisation toggle to follow the selection', async () =>
      (await pickerValue('#tenant-organization')) === 'acme')
    // widgets is the organisation's only project, so it is selected the way an
    // unpinned CLI would select it: oldest first.
    await until('the project toggle to follow', async () =>
      (await pickerValue('#tenant-project')) === 'widgets')
    // The screen in view persists across navigation, so name it explicitly.
    await clickByText('a', 'Buckets')
    await waitForText('smoke-images')
    await waitForText('zzz-newer')
    assert.deepEqual(
      await page.$$eval(
        'table[aria-label="Buckets"] td[data-label="Bucket"] > div:first-child button',
        (buttons) => buttons.map((button) => button.innerText.trim()),
      ),
      ['zzz-newer', 'smoke-images'],
      'same-day buckets do not default to full-timestamp newest-first order',
    )
    // PatternFly transitions nav-link background-color, so a naive read samples
    // the fade and returns a different alpha every run. Settle motion first, or
    // the assertion measures an animation rather than the rule.
    await page.evaluate(() => {
      const style = document.createElement('style')
      style.id = 'smoke-no-motion'
      style.textContent = '*,*::before,*::after{transition:none!important;animation:none!important}'
      document.head.appendChild(style)
    })
    const navStyles = await page.evaluate(() => {
      const panel = document.querySelector('.app-sidebar')
      const heading = document.querySelector('.app-global-nav .pf-v6-c-nav__section-title')
      const selected = document.querySelector('.app-global-nav .pf-v6-c-nav__link[aria-current="page"]')
      const panelStyle = getComputedStyle(panel)
      const headingStyle = getComputedStyle(heading)
      const selectedStyle = getComputedStyle(selected)
      return {
        panel: {
          width: panelStyle.width,
          background: panelStyle.backgroundColor,
          border: `${panelStyle.borderRightWidth} ${panelStyle.borderRightStyle} ${panelStyle.borderRightColor}`,
          padding: `${panelStyle.paddingTop} ${panelStyle.paddingRight} ${panelStyle.paddingBottom} ${panelStyle.paddingLeft}`,
        },
        heading: {
          padding: `${headingStyle.paddingTop} ${headingStyle.paddingRight} ${headingStyle.paddingBottom} ${headingStyle.paddingLeft}`,
          fontWeight: headingStyle.fontWeight,
          fontSize: headingStyle.fontSize,
          lineHeight: headingStyle.lineHeight,
          color: headingStyle.color,
        },
        selected: {
          display: selectedStyle.display,
          width: selectedStyle.width,
          padding: `${selectedStyle.paddingTop} ${selectedStyle.paddingRight} ${selectedStyle.paddingBottom} ${selectedStyle.paddingLeft}`,
          borderLeft: `${selectedStyle.borderLeftWidth} ${selectedStyle.borderLeftStyle} ${selectedStyle.borderLeftColor}`,
          background: selectedStyle.backgroundColor,
          fontWeight: selectedStyle.fontWeight,
          fontSize: selectedStyle.fontSize,
          lineHeight: selectedStyle.lineHeight,
        },
      }
    })
    assert.deepEqual(navStyles, {
      panel: {
        width: '212px', background: 'rgb(255, 255, 255)',
        border: '1px solid rgb(224, 224, 224)', padding: '8px 0px 8px 0px',
      },
      heading: {
        padding: '16px 16px 6px 16px', fontWeight: '500', fontSize: '14px',
        lineHeight: '19.6px', color: 'rgb(77, 77, 77)',
      },
      selected: {
        // PF's native link is inset within the item rather than a full-bleed
        // row — 175px is the 212px sidebar minus PatternFly's item insets.
        display: 'flex', width: '175px', padding: '8px 16px 8px 16px',
        borderLeft: '0px none rgb(21, 21, 21)', background: 'rgb(255, 255, 255)',
        fontWeight: '500', fontSize: '14px', lineHeight: '19.6px',
      },
    })
    // Bucket choice is route state: type into the masthead picker, choose the
    // exact option, and the detail route becomes the selection.
    // The typeahead filter states no-match rather than an empty menu, and
    // clearing restores the listing — the list screen's filter contract, kept.
    await page.click('#tenant-bucket-input')
    await page.type('#tenant-bucket-input', 'no-such-bucket')
    await waitForText('No results found for')
    await page.click('[aria-label="Clear bucket search"]')
    await choosePickerOption('#tenant-bucket', 'smoke-images')
    await until('the bucket picker to navigate', () =>
      new URL(page.url()).pathname.endsWith('/buckets/smoke-images'))
    assert.equal(await pickerValue('#tenant-bucket'), 'smoke-images')
    await waitForText('Bucket details')
    // A fresh bucket states its emptiness (the old list's fresh-bucket proof).
    await clickByText('button', 'Versions')
    await waitForText('No versions in this bucket')
    // Channel names live on the Channels facet; Overview shows counts only.
    await clickByText('button', 'Channels')
    await waitForText('latest')
    // The facet is screen state that outlives navigation; leave the default
    // in place for the tests that follow.
    await clickByText('button', 'Overview')
    await waitForText('Bucket details')
  })

  await t.test('a privileged session pins and unpins from the bucket detail header', async () => {
    await clickByText('button', 'Pin bucket')
    await waitForText('Unpin bucket')
    // Pinning surfaces the bucket in the picker's Pinned group; unpinning
    // removes the group — the pinned-gallery contract in its new home.
    await page.click('#tenant-bucket')
    await waitForText('Pinned')
    await page.keyboard.press('Escape')
    await clickByText('button', 'Unpin bucket')
    await waitForText('Pin bucket')
    await page.click('#tenant-bucket')
    await until('the pinned group to disappear', async () =>
      !(await bodyText()).includes('Pinned'))
    await page.keyboard.press('Escape')
  })

  await t.test('a completed version and an incomplete v0 drill down from the bucket row', async () => {
    // Seed through the compat API with the SAME call sequence the contract
    // test drives via hcp-sdk-go (contract/hcp2023_contract_test.go): create
    // the version (v0), create a build, then PATCH it BUILD_DONE with an
    // artifact and metadata. Completion is server-side — the name leaving v0
    // is how every client sees it.
    const builderToken = await tokenFor(seeded.principal.client_id, seeded.principal.secret)
    const versionsPath =
      `/packer/2023-01-01/organizations/${seeded.organization.id}` +
      `/projects/${seeded.project.id}/buckets/smoke-images/versions`
    const bucketBase =
      `/packer/2023-01-01/organizations/${seeded.organization.id}` +
      `/projects/${seeded.project.id}/buckets`

    // A real parent bucket/version/channel is recorded on the child build, so
    // the lineage card below is proving the baseline's stored relation.
    await api(builderToken, 'PUT', bucketBase, {
      name: 'smoke-base', description: 'parent seeded by the smoke test',
    })
    const baseVersionsPath = `${bucketBase}/smoke-base/versions`
    const { version: parentVersion } = await api(builderToken, 'POST', baseVersionsPath, {
      fingerprint: 'smoke-parent', template_type: 'HCL2',
    })
    const { build: parentBuild } = await api(
      builderToken, 'POST', `${baseVersionsPath}/smoke-parent/builds`,
      { component_type: 'docker.base', packer_run_uuid: 'smoke-parent-run', artifacts: [] },
    )
    await api(builderToken, 'PATCH', `${baseVersionsPath}/smoke-parent/builds/${parentBuild.id}`, {
      status: 'BUILD_DONE', platform: 'docker',
      artifacts: [{ external_identifier: 'sha256:smoke-parent-artifact', region: 'local' }],
      metadata: {},
    })
    const { channels: parentChannels } = await api(
      builderToken, 'GET', `${bucketBase}/smoke-base/channels`,
    )
    const parentLatest = parentChannels.find((channel) => channel.name === 'latest')
    assert.ok(parentLatest?.id, 'the parent managed latest channel was not returned')

    await api(builderToken, 'POST', versionsPath, {
      fingerprint: 'smoke-done', template_type: 'HCL2',
    })
    const completedBuildsPath = `${versionsPath}/smoke-done/builds`
    const { build } = await api(builderToken, 'POST', completedBuildsPath, {
      component_type: 'docker.smoke', packer_run_uuid: 'smoke-run-1', artifacts: [],
      parent_version_id: parentVersion.id, parent_channel_id: parentLatest.id,
    })
    const { build: emptyBuild } = await api(builderToken, 'POST', completedBuildsPath, {
      component_type: 'docker.empty', packer_run_uuid: 'smoke-run-empty', artifacts: [],
    })
    const { build: brokenBuild } = await api(builderToken, 'POST', completedBuildsPath, {
      component_type: 'docker.broken', packer_run_uuid: 'smoke-run-broken', artifacts: [],
    })

    const packerMetadata = {
      packer: {
        version: '1.16.0',
        options: {
          path: './smoke.pkr.hcl', vars: ['base_image', 'run_label'],
          only: ['docker.smoke'], debug: false, force: true,
        },
        os: { type: 'linux', details: { arch: 'amd64', version: 'smoke' } },
        plugins: [{ name: 'docker', version: '1.1.4' }],
      },
    }
    // SBOMs upload during the running window, as real Packer does — the compat
    // plane now reproduces live HCP's refusal for non-running builds (A.11).
    await api(builderToken, 'PATCH', `${completedBuildsPath}/${build.id}`, { status: 'BUILD_RUNNING' })
    await api(builderToken, 'PATCH', `${completedBuildsPath}/${brokenBuild.id}`, { status: 'BUILD_RUNNING' })
    const inventory = zstdCompressSync(Buffer.from(JSON.stringify({
      bomFormat: 'CycloneDX',
      components: [{
        'bom-ref': 'pkg:generic/openssl@3.0.11', name: 'openssl', version: '3.0.11',
        purl: 'pkg:generic/openssl@3.0.11',
      }],
    }))).toString('base64')
    await api(builderToken, 'PUT', `${completedBuildsPath}/${build.id}/sboms`, {
      compressed_sbom: inventory, format: 'CYCLONEDX', name: 'smoke-inventory',
    })
    const brokenInventory = zstdCompressSync(Buffer.from('{not-json')).toString('base64')
    await api(builderToken, 'PUT', `${completedBuildsPath}/${brokenBuild.id}/sboms`, {
      compressed_sbom: brokenInventory, format: 'CYCLONEDX', name: 'broken-client-report',
    })
    await api(builderToken, 'PATCH', `${completedBuildsPath}/${build.id}`, {
      status: 'BUILD_DONE',
      platform: 'docker',
      artifacts: [{ external_identifier: 'sha256:smoke-artifact', region: 'local' }],
      labels: { ImageDigest: 'sha256:smoke-label' },
      metadata: packerMetadata,
    })
    await api(builderToken, 'PATCH', `${completedBuildsPath}/${emptyBuild.id}`, {
      status: 'BUILD_DONE', platform: 'docker', artifacts: [], metadata: packerMetadata,
    })
    await api(builderToken, 'PATCH', `${completedBuildsPath}/${brokenBuild.id}`, {
      status: 'BUILD_DONE', platform: 'docker', artifacts: [], metadata: packerMetadata,
    })

    // A second version left exactly as Packer leaves an unfinished run.
    await api(builderToken, 'POST', versionsPath, {
      fingerprint: 'smoke-wip', template_type: 'HCL2',
    })

    // A manual channel assignment produces the second kind of author row:
    // unlike managed latest's Dufflebag author, this one is the real caller.
    const promoterToken = await tokenFor(seeded.promoter.client_id, seeded.promoter.secret)
    const { channel: productionChannel } = await api(
      promoterToken, 'POST', `${bucketBase}/smoke-images/channels`, {
      name: 'production', restricted: false,
      },
    )
    await api(promoterToken, 'PATCH', `${bucketBase}/smoke-images/channels/production`, {
      update_mask: 'versionFingerprint', version_fingerprint: 'smoke-done',
    })
    const historyRequests = []
    const recordHistoryRequest = (request) => {
      if (/\/channels\/[^/]+\/history$/.test(new URL(request.url()).pathname)) {
        historyRequests.push(request.url())
      }
    }
    page.on('request', recordHistoryRequest)
    t.after(() => page.off('request', recordHistoryRequest))

    // Open the versions list from the universal bucket picker. Picking the
    // bucket already on screen is a same-route no-op, so step onto the
    // landing first — the old drill-down navigated fresh, and so must this.
    await clickByText('a', 'Buckets')
    await waitForText('smoke-images')
    await clickByText('table[aria-label="Buckets"] button', 'smoke-images')
    await waitForText('seeded by the smoke test')
    await waitForText('team=platform')
    await waitForText('purpose=browser-proof')
    await waitForText('Bucket details')
    // The facet rail consumes the base background token ladder, which
    // PatternFly's dark theme never redefines — the rail once stayed white in
    // dark mode (duf-66xa). Prove it follows the theme like the sidebar does.
    const railBackground = () => page.$eval(
      '.registry-facet-heading', (el) => getComputedStyle(el).backgroundColor)
    const lightRail = await railBackground()
    await page.click('button[aria-label="Switch to dark theme"]')
    assert.notEqual(await railBackground(), lightRail)
    await page.click('button[aria-label="Switch to light theme"]')
    assert.equal(await railBackground(), lightRail)
    assert.equal(await facetHeading('Bucket facets'), 'This bucket')
    assert.deepEqual(await facetItems('Bucket facets'), [
      { label: 'Overview', count: '' },
      { label: 'Versions', count: '2' },
      { label: 'Channels', count: '2' },
    ])
    const bucketDetails = await page.$eval(
      '.pf-v6-c-card',
      (card) => card.innerText.includes('Bucket details') ? card.innerText : '',
    )
    assert.match(bucketDetails, /Status\s+incomplete/)
    assert.doesNotMatch(bucketDetails, /Status\s+complete/)
    assert.equal(historyRequests.length, 0, 'collapsed channels fetched assignment history')
    await clickFacet('Bucket facets', 'Versions')
    await page.waitForSelector('table[aria-label="Versions"]')
    const versionsLayout = await page.$eval('table[aria-label="Versions"]', (table) => {
      const card = table.closest('.pf-v6-c-card')
      const content = table.closest('[role="tabpanel"]')
      const cardRect = card.getBoundingClientRect()
      const contentRect = content.getBoundingClientRect()
      const contentPaddingLeft = Number.parseFloat(getComputedStyle(content).paddingLeft)
      const contentPaddingRight = Number.parseFloat(getComputedStyle(content).paddingRight)
      return {
        visibleCards: [...content.querySelectorAll('.pf-v6-c-card')]
          .filter((candidate) => candidate.getBoundingClientRect().height > 0).length,
        cardLeft: cardRect.left,
        cardRight: cardRect.right,
        contentLeft: contentRect.left,
        contentRight: contentRect.right,
        contentPaddingLeft,
        contentPaddingRight,
      }
    })
    assert.equal(versionsLayout.visibleCards, 1)
    assert.equal(
      versionsLayout.cardLeft,
      versionsLayout.contentLeft + versionsLayout.contentPaddingLeft,
    )
    // The card fills the content column inside a SYMMETRIC 24px inset: 7a2222f
    // widened the well's padding-left-only to padding all round, and this
    // assertion previously demanded the pre-reframe geometry (card to the
    // border edge), which failed by exactly the new right padding — the
    // 1412 !== 1436 that red-flagged every branch while the Actions outage
    // hid 7a2222f's own run (duf-fyku).
    assert.equal(versionsLayout.contentPaddingRight, versionsLayout.contentPaddingLeft)
    assert.equal(
      versionsLayout.cardRight,
      versionsLayout.contentRight - versionsLayout.contentPaddingRight,
    )
    assert.doesNotMatch(await bodyText(), /Version summary/)
    await waitForText(`by ${seeded.promoter.id}`)
    // The completed version renders complete, with its counts; the incomplete
    // v0 renders as incomplete — an honest state, not a failure.
    await waitForText('v1')
    await toggleRow('v1')
    await waitForText('smoke-done')
    await waitForText('3 builds · 1 artifact')
    await waitForText('Parent status')
    await waitForText('smoke-base v1')
    await waitForText('newest')
    await waitForText('v0')
    await waitForText('incomplete')
    await toggleRow('v0')
    await waitForText('smoke-wip')
    // The version defaults to Overview; descend through its Builds facet.
    // package state proved in a real browser against the real package route.
    await clickByText('button', 'v1')
    await waitForText('Lineage')
    assert.equal(await facetHeading('Version facets'), 'This version')
    assert.deepEqual(await facetItems('Version facets'), [
      { label: 'Overview', count: '' },
      { label: 'Builds', count: '3' },
    ])
    // The lineage card links the parent as "bucket vN" (duf-dus4); the
    // childless side is stated as "None." rather than omitted.
    await waitForText('smoke-base v1')
    await waitForText('None.')
    await waitForText('Consume this version')
    await waitForText('data "hcp_packer_version" "smoke_images"')
    await waitForText('version_fingerprint = "smoke-done"')
    await waitForText('Operations')
    await waitForText('resource "hcp_packer_channel_assignment" "production"')
    await clickFacet('Version facets', 'Builds')
    await waitForText('docker.smoke')
    await waitForText('Packer runner OS')
    await waitForText('linux')
    await waitForText('amd64')
    await toggleRow('docker.smoke')
    await until('the parsed package count to render', async () =>
      (await buildGroupText('docker.smoke')).includes('1 package'))
    await toggleRow('docker.empty')
    await until('the genuinely empty package count to render', async () =>
      (await buildGroupText('docker.empty')).includes('0 packages'))
    await toggleRow('docker.broken')
    await until('the unparseable inventory to stay non-numeric', async () => {
      const text = await buildGroupText('docker.broken')
      return text.includes('SBOM unparseable') && !text.includes('0 packages')
    })
    assert.doesNotMatch(await bodyText(), /could not be loaded/)

    // An unparseable inventory has an unknown count in the build rail. It is
    // never rewritten as the known-empty label Packages (0).
    await clickByText('button', 'docker.broken')
    await clickFacet('Build facets', 'Packages')
    await waitForText('Package inventory is unavailable')
    assert.equal(await facetHeading('Build facets'), 'This build')
    assert.deepEqual(await facetItems('Build facets'), [
      { label: 'Overview', count: '' },
      { label: 'Artifacts', count: '0' },
      { label: 'Packages', count: '' },
    ])
    await clickByText('button', 'v1')
    await clickFacet('Version facets', 'Builds')

    // Opening the build proves the three approved overview cards, reconstructed
    // masking, and the Artifacts facet's corrected first-column label.
    await clickByText('button', 'docker.smoke')
    assert.equal(await facetHeading('Build facets'), 'This build')
    assert.deepEqual(await facetItems('Build facets'), [
      { label: 'Overview', count: '' },
      { label: 'Artifacts', count: '1' },
      { label: 'Packages', count: '1' },
    ])
    await waitForText('Build options')
    await waitForText('base_image=***')
    await waitForText('Variable values are masked.')
    await waitForText('Packer runner environment')
    await waitForText('docker 1.1.4')
    await waitForText('smoke-run-1')
    await waitForText('Build labels')
    await waitForText('ImageDigest')
    await clickFacet('Build facets', 'Packages')
    await waitForText('Reported by client-supplied SBOMs')
    await waitForText('openssl')
    await waitForText('smoke-inventory')
    await clickFacet('Build facets', 'Artifacts')
    await waitForText('Platform')
    await waitForText('External ID')
    await waitForText('sha256:smoke-artifact')
    await waitForText('local')

    // The breadcrumb walks back out: build → version → bucket → registry.
    await clickByText('button', 'v1')
    await waitForText('Lineage')
    await clickByText('button', 'smoke-images')
    await waitForText('Bucket details')

    // The bucket Channels facet is lazy at row granularity. Neither the bucket
    // load nor selecting the facet fetches history; each expanded row adds one
    // request and renders its own source of management truth.
    await clickFacet('Bucket facets', 'Channels')
    await waitForText('Assigned time')
    assert.equal(historyRequests.length, 0, 'collapsed channel table fetched assignment history')
    await toggleRow('latest')
    await waitForText('dufflebag, on version completion')
    await until('only latest history to be fetched', () => historyRequests.length === 1)
    await toggleRow('production')
    await waitForText('hcp_packer_channel_assignment')
    await until('only the two expanded histories to be fetched', () => historyRequests.length === 2)
    // The request counter ticks at request start; wait for the rendered rows
    // before reading them.
    await until('the production history rows to render', async () =>
      (await page.$$('table[aria-label="production assignment history"] tbody tr')).length > 0)
    const statuses = await page.$$eval(
      'table[aria-label="production assignment history"] td[data-label="Status"]',
      (cells) => cells.map((cell) => cell.innerText.trim()),
    )
    assert.deepEqual(statuses, ['Active'])
    const authors = await page.$$eval(
      'table[aria-label="production assignment history"] td[data-label="Assigned by"]',
      (cells) => cells.map((cell) => cell.innerText.trim()),
    )
    assert.deepEqual(authors, [seeded.promoter.id])

    // Now tamper: append a history row by hand, bypassing the application. On
    // this encrypted stack that row carries no integrity MAC, so the history
    // read refuses and the console says so rather than fabricating or
    // emptying the history (ADR-0024: database write access is not
    // administration). The row stays — assignment history is append-only by
    // trigger, tampered or not — and nothing later reads this channel again.
    // The unknown-author rendering this fixture used to exercise lives in the
    // SSR unit lane: a baseline encrypted instance cannot naturally create an
    // unknown-author row.
    assert.match(productionChannel.id, /^[0-9A-HJKMNP-TV-Z]{26}$/)
    await execFile('docker', [
      'exec', container, 'psql', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', 'dufflebag',
      '-c',
      `INSERT INTO channel_assignments (` +
        `organization_id, project_id, id, bucket_id, channel_id, version_id, author_id, assigned_at` +
        `) SELECT organization_id, project_id, '00000000000000000000000000', ` +
        `bucket_id, channel_id, version_id, '', assigned_at - INTERVAL '1 microsecond' ` +
        `FROM channel_assignments WHERE channel_id = '${productionChannel.id}' ` +
        'ORDER BY assigned_at DESC, id DESC LIMIT 1',
    ])
    await toggleRow('production')
    await toggleRow('production')
    await until('the reopened production history to be fetched', () => historyRequests.length === 3)
    await waitForText('Assignment history could not be loaded')

    await clickByText('button', 'Registry')
    await waitForText('smoke-images')
  })

  await t.test('a version is revoked and restored through the console, confirmed at the wire', async () => {
    // A dedicated bucket keeps revocation cascades (descendants, channel
    // rollback) away from every other facet's fixtures.
    const builderToken = await tokenFor(seeded.principal.client_id, seeded.principal.secret)
    const bucketBase =
      `/packer/2023-01-01/organizations/${seeded.organization.id}` +
      `/projects/${seeded.project.id}/buckets`
    await api(builderToken, 'PUT', bucketBase, {
      name: 'smoke-revocable', description: 'revocation facet fixture',
    })
    const revPath = `${bucketBase}/smoke-revocable/versions`
    await api(builderToken, 'POST', revPath, {
      fingerprint: 'smoke-revocable-fp', template_type: 'HCL2',
    })
    const { build: revocableBuild } = await api(
      builderToken, 'POST', `${revPath}/smoke-revocable-fp/builds`,
      { component_type: 'docker.revocable', packer_run_uuid: 'smoke-revocable-run', artifacts: [] },
    )
    await api(builderToken, 'PATCH', `${revPath}/smoke-revocable-fp/builds/${revocableBuild.id}`, {
      status: 'BUILD_DONE', platform: 'docker',
      artifacts: [{ external_identifier: 'sha256:smoke-revocable', region: 'local' }],
      metadata: {},
    })

    await choosePickerOption('#tenant-bucket', 'smoke-revocable')
    await clickFacet('Bucket facets', 'Versions')
    await page.waitForSelector('table[aria-label="Versions"]')
    await clickByText('button', 'v1')
    await waitForText('Lineage')

    // Revoke now, through the danger confirmation; the state label only flips
    // when the wire answered and the refetch landed.
    await clickByText('button', 'Revoke')
    await waitForText('Revoke smoke-revocable v1')
    await typeToConfirm('v1')
    await clickByText('button', 'Revoke smoke-revocable v1')
    await until('the version to render revoked after the wire confirms', async () =>
      /revoked/.test(await bodyText()))
    const revoked = await api(builderToken, 'GET', `${revPath}/smoke-revocable-fp`)
    assert.ok(revoked.version.revoke_at, 'the console revoke did not reach the wire')

    // Restore through its confirmation; active again on screen and at the wire.
    await clickByText('button', 'Restore')
    await waitForText('Restore smoke-revocable — v1')
    await clickByText('button', 'Restore smoke-revocable — v1')
    await until('the version to render active after restore', async () => {
      const text = await bodyText()
      return !/revoked/.test(text) && /complete/.test(text)
    })
    const restored = await api(builderToken, 'GET', `${revPath}/smoke-revocable-fp`)
    assert.equal(restored.version.revoke_at, null, 'the console restore did not clear the wire')
  })

  await t.test('a channel is created, promoted to, and deleted through the console', async () => {
    // Reuses the revoke facet's bucket: v1 is active again after its restore.
    const builderToken = await tokenFor(seeded.principal.client_id, seeded.principal.secret)
    const channelsPath =
      `/packer/2023-01-01/organizations/${seeded.organization.id}` +
      `/projects/${seeded.project.id}/buckets/smoke-revocable/channels`

    await choosePickerOption('#tenant-bucket', 'smoke-revocable')
    await clickFacet('Bucket facets', 'Channels')
    await waitForText('latest')

    // Create an unassigned channel through the modal; the opener and the
    // confirm share a label, so the confirm is scoped to the modal box.
    await clickByText('button', 'Create channel')
    await waitForText('Create channel in smoke-revocable')
    await page.type('#channel-name', 'staging')
    await clickByText('.pf-v6-c-modal-box button', 'Create channel')
    await until('the staging channel row to appear', async () =>
      (await bodyText()).includes('staging'))
    const created = await api(builderToken, 'GET', channelsPath)
    const staging = created.channels.find((channel) => channel.name === 'staging')
    assert.ok(staging, 'the console create did not reach the wire')
    assert.ok(!staging.version, 'a fresh channel arrived assigned')

    // Promote this version onto it from the version screen's Operations card.
    await clickFacet('Bucket facets', 'Versions')
    await page.waitForSelector('table[aria-label="Versions"]')
    await clickByText('button', 'v1')
    await waitForText('Operations')
    await page.select('#promotion-channel', 'staging')
    await clickByText('button', 'Promote')
    await until('the promotion to land at the wire', async () => {
      const { channels } = await api(builderToken, 'GET', channelsPath)
      return channels.find((channel) => channel.name === 'staging')
        ?.version?.fingerprint === 'smoke-revocable-fp'
    })

    // Delete it through the kebab's danger confirmation; history goes with it.
    await choosePickerOption('#tenant-bucket', 'smoke-revocable')
    await clickFacet('Bucket facets', 'Channels')
    await page.click('button[aria-label="Actions for staging"]')
    await clickByText('button', 'Delete channel')
    await waitForText('Delete smoke-revocable — staging')
    await typeToConfirm('staging')
    await clickByText('.pf-v6-c-modal-box button', 'Delete staging')
    await until('the staging channel to leave the wire', async () => {
      const { channels } = await api(builderToken, 'GET', channelsPath)
      return !channels.some((channel) => channel.name === 'staging')
    })
    // The managed latest was never offered an action and is still there.
    const remaining = await api(builderToken, 'GET', channelsPath)
    assert.ok(remaining.channels.some((channel) => channel.name === 'latest'))
  })

  await t.test('the facet bucket is deleted through the console, gone at the wire', async () => {
    // Ends the smoke-revocable arc: the bucket detail's danger action removes
    // the bucket the revoke and channel facets used, landing back on Registry.
    const builderToken = await tokenFor(seeded.principal.client_id, seeded.principal.secret)
    const bucketPath =
      `/packer/2023-01-01/organizations/${seeded.organization.id}` +
      `/projects/${seeded.project.id}/buckets/smoke-revocable`

    // The previous test leaves this bucket's detail on its Channels facet;
    // a same-route pick would keep it. Navigate fresh from the landing.
    await clickByText('a', 'Buckets')
    await waitForText('smoke-revocable')
    await choosePickerOption('#tenant-bucket', 'smoke-revocable')
    await waitForText('Bucket details')
    // The opener and the modal confirm share a label; scope the confirm.
    await clickByText('button', 'Delete bucket')
    await waitForText('Delete smoke-revocable')
    await typeToConfirm('smoke-revocable')
    await clickByText('.pf-v6-c-modal-box button', 'Delete bucket')
    await until('the console to land back on the loaded list', async () => {
      const text = await bodyText()
      return text.includes('smoke-images') && !text.includes('smoke-revocable')
    })
    await until('the bucket to leave the wire', async () => {
      try {
        await api(builderToken, 'GET', bucketPath)
        return false
      } catch {
        return true
      }
    })
    assert.doesNotMatch(await bodyText(), /smoke-revocable/)
  })

  await t.test('scanner states remain explicit from no scan through channel movement', async () => {
    const rootToken = await tokenFor(credentials.clientID, credentials.secret)
    const compatBase =
      `/packer/2023-01-01/organizations/${seeded.organization.id}` +
      `/projects/${seeded.project.id}/buckets`
    const rescanBase =
      `/api/v1/organizations/${seeded.organization.id}` +
      `/projects/${seeded.project.id}/builds`
    const patched = {
      'bom-ref': 'pkg:apk/alpine/busybox@1.36.1-r31?arch=aarch64&distro=alpine-3.20.10',
      name: 'busybox', version: '1.36.1-r31',
      purl: 'pkg:apk/alpine/busybox@1.36.1-r31?arch=aarch64&distro=alpine-3.20.10',
    }
    const unsupported = {
      'bom-ref': 'pkg:rpm/amzn/openssl@1.0.2k-24.amzn2.0.7',
      name: 'openssl', version: '1.0.2k-24.amzn2.0.7',
      purl: 'pkg:rpm/amzn/openssl@1.0.2k-24.amzn2.0.7',
    }
    const unversioned = {
      'bom-ref': 'pkg:golang/github.com/benemon/dufflebag',
      name: 'github.com/benemon/dufflebag',
      purl: 'pkg:golang/github.com/benemon/dufflebag',
    }
    const vulnerable = {
      'bom-ref': 'pkg:golang/github.com/go-jose/go-jose/v4@v4.1.1',
      name: 'github.com/go-jose/go-jose/v4', version: 'v4.1.1',
      purl: 'pkg:golang/github.com/go-jose/go-jose/v4@v4.1.1',
    }

    const seedVersion = async ({ bucket, fingerprint, components, complete, createBucket = true }) => {
      if (createBucket) {
        await api(rootToken, 'PUT', compatBase, {
          name: bucket, description: 'scanner browser state',
        })
      }
      const versionsPath = `${compatBase}/${bucket}/versions`
      await api(rootToken, 'POST', versionsPath, { fingerprint, template_type: 'HCL2' })
      const { build } = await api(rootToken, 'POST', `${versionsPath}/${fingerprint}/builds`, {
        component_type: `docker.${bucket}`, packer_run_uuid: `run-${fingerprint}`, artifacts: [],
      })
      if (components.length > 0) {
        // Running window first: the compat plane refuses SBOM uploads on
        // non-running builds, matching live HCP (A.11).
        await api(rootToken, 'PATCH', `${versionsPath}/${fingerprint}/builds/${build.id}`, {
          status: 'BUILD_RUNNING',
        })
        const compressed = zstdCompressSync(Buffer.from(JSON.stringify({
          bomFormat: 'CycloneDX', specVersion: '1.6', components,
        }))).toString('base64')
        await api(rootToken, 'PUT', `${versionsPath}/${fingerprint}/builds/${build.id}/sboms`, {
          compressed_sbom: compressed, format: 'CYCLONEDX', name: 'scanner-state',
        })
      }
      if (complete) {
        await api(rootToken, 'PATCH', `${versionsPath}/${fingerprint}/builds/${build.id}`, {
          status: 'BUILD_DONE', platform: 'docker', artifacts: [], metadata: {},
        })
      }
      return {
        bucket, fingerprint, build,
        packagesPath: `${versionsPath}/${fingerprint}/builds/${build.id}/packages`,
      }
    }

    const never = await seedVersion({
      bucket: 'scanner-never', fingerprint: 'scanner-never', components: [], complete: false,
    })
    const full = await seedVersion({
      bucket: 'scanner-full', fingerprint: 'scanner-full', components: [patched], complete: true,
    })
    const gap = await seedVersion({
      bucket: 'scanner-gap', fingerprint: 'scanner-gap',
      components: [patched, unsupported, unversioned], complete: true,
    })
    const findings = await seedVersion({
      bucket: 'scanner-findings', fingerprint: 'scanner-findings',
      components: [vulnerable], complete: true,
    })

    // Queue all three scans before waiting for any one of them. The worker
    // pool can make progress in parallel, keeping this long-running lane sane.
    await Promise.all([full, gap, findings].map((state) =>
      api(rootToken, 'POST', `${rescanBase}/${state.build.id}/rescan`)))
    const [fullScan, gapScan, findingsScan] = await Promise.all([
      until('the full-coverage scan', async () => {
        const value = await scanAttribution(rootToken, full.packagesPath)
        return value?.submitted === 1 && value.unsupported === 0 && value.unversioned === 0
          ? value : false
      }, 60000, 250),
      until('the coverage-gap scan', async () => {
        const value = await scanAttribution(rootToken, gap.packagesPath)
        return value?.submitted === 1 && value.unsupported === 1 && value.unversioned === 1
          ? value : false
      }, 60000, 250),
      until('the findings scan', async () => {
        const value = await scanAttribution(rootToken, findings.packagesPath)
        return value?.submitted === 1 ? value : false
      }, 60000, 250),
    ])
    assert.equal(fullScan.adapter, 'osv')
    assert.equal(gapScan.adapter, 'osv')
    assert.equal(findingsScan.adapter, 'osv')

    const openVersion = async (state) => {
      await page.goto(
        `${base}/buckets/${encodeURIComponent(state.bucket)}` +
        `/versions/${encodeURIComponent(state.fingerprint)}`,
        { waitUntil: 'domcontentloaded' },
      )
      // A hard load costs the platform session its in-memory tenancy
      // selection; the deep link honestly asks rather than guessing. Wait for
      // the page to settle into either the content or the gap, then choose
      // the seeded tenancy the way a user would and the route re-resolves.
      const settled = await until('the deep link to settle', async () => {
        const text = await bodyText()
        if (/Security/.test(text)) return 'content'
        if (/Choose an organisation/.test(text)) return 'gap'
        return false
      })
      if (settled === 'gap') {
        await choosePickerOption('#tenant-organization', seeded.organization.name)
      }
      await waitForText('Security')
    }
    const securityText = () => page.$$eval('.pf-v6-c-card', (cards) =>
      cards.find((card) => card.innerText.trim().startsWith('Security'))?.innerText ?? '')
    // The scan date renders formatted (duf-fcg6.6); the card's time element
    // keeps the real observation date for exact assertion.
    const securityScanDate = () => page.$$eval('.pf-v6-c-card', (cards) => {
      const card = cards.find((c) => c.innerText.trim().startsWith('Security'))
      return card?.querySelector('time')?.getAttribute('datetime') ?? ''
    })
    const assertNoVerdicts = async () => {
      const visible = (await bodyText()).toLowerCase()
      assert.doesNotMatch(visible, /\bclean\b/)
      assert.doesNotMatch(visible, /\bstale\b/)
    }

    // 1. Never scanned: a running build is ineligible by construction, so
    // this state cannot race the background worker.
    await openVersion(never)
    await until('the never-scanned state', async () =>
      (await securityText()).includes('Not scanned.'))
    assert.match(await securityText(), /Not scanned/)
    assert.doesNotMatch(await securityText(), /No known findings|Last scanned/)
    await assertNoVerdicts()

    // 2. Zero findings with complete coverage: attribution is exact, and a
    // missing coverage line is meaningful only alongside the submitted count.
    await openVersion(full)
    const fullDate = new Date(fullScan.observedAt).toISOString().slice(0, 10)
    await until('the full-coverage figures', async () =>
      (await securityScanDate()).startsWith(fullDate))
    const fullText = await securityText()
    assert.match(fullText, /Last scanned:/)
    assert.match(fullText, /No known findings/)
    assert.match(fullText, /1 scanned/)
    assert.doesNotMatch(fullText, /Coverage:/)
    await assertNoVerdicts()

    // 3. The same zero result with unsupported and unqueryable packages is a
    // visibly different answer, with every coverage value pinned exactly.
    await openVersion(gap)
    const gapDate = new Date(gapScan.observedAt).toISOString().slice(0, 10)
    const gapCoverage =
      'Coverage: 1 queried; 1 in ecosystems the scanner does not cover; ' +
      '1 without a version to match.'
    await until('the coverage-gap figures', async () =>
      (await securityText()).includes(gapCoverage))
    const gapText = await securityText()
    assert.ok((await securityScanDate()).startsWith(gapDate), 'gap scan date must render')
    assert.match(gapText, /No known findings/)
    assert.match(gapText, /3 scanned/)
    assert.ok(gapText.includes(gapCoverage), `coverage text was:\n${gapText}`)
    await assertNoVerdicts()

    // 4. The recorded Go fixture yields exactly two advisories on one package.
    await openVersion(findings)
    const findingsDate = new Date(findingsScan.observedAt).toISOString().slice(0, 10)
    await until('the findings figures', async () =>
      (await securityText()).includes('2 findings across 1 package'))
    const findingsText = await securityText()
    assert.ok((await securityScanDate()).startsWith(findingsDate), 'findings scan date must render')
    assert.match(findingsText, /2 findings across 1 package/)
    assert.match(findingsText, /1 high/)
    assert.match(findingsText, /1 unknown/)
    await assertNoVerdicts()

    await page.click(`[data-build-link="${findings.build.id}"]`)
    await clickFacet('Build facets', 'Packages')
    await waitForText('github.com/go-jose/go-jose/v4')
    assert.equal(
      await rowCellText('github.com/go-jose/go-jose/v4', 'Name'),
      'github.com/go-jose/go-jose/v4',
    )
    assert.equal(await rowCellText('github.com/go-jose/go-jose/v4', 'Version'), 'v4.1.1')
    await toggleRow('github.com/go-jose/go-jose/v4')
    await waitForText('GHSA-78h2-9frx-2jm8')
    const findingRows = await page.$$eval('table[aria-label="Findings"] tbody tr', (rows) =>
      Object.fromEntries(rows.map((row) => [
        row.querySelector('td[data-label="Advisory"]')?.innerText.trim() ?? '',
        Object.fromEntries([...row.querySelectorAll('td')].map((cell) => [
          cell.getAttribute('data-label'), cell.innerText.trim(),
        ])),
      ])))
    assert.deepEqual(findingRows['GHSA-78h2-9frx-2jm8'], {
      Advisory: 'GHSA-78h2-9frx-2jm8', Severity: 'high',
      Reported: 'CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H',
      Aliases: 'CVE-2026-34986, GO-2026-4945', 'Fixed in': '4.1.4',
    })
    assert.deepEqual(findingRows['GO-2026-4945'], {
      Advisory: 'GO-2026-4945', Severity: 'unknown', Reported: '—',
      Aliases: 'CVE-2026-34986, GHSA-78h2-9frx-2jm8', 'Fixed in': '4.1.4',
    })

    // 5. Completing the next version moves managed latest. The prior version
    // keeps its exact figures and timestamp, but its treatment visibly changes.
    await seedVersion({
      bucket: findings.bucket, fingerprint: 'scanner-findings-moved',
      components: [patched], complete: true, createBucket: false,
    })
    await openVersion(findings)
    await until('the prior findings to become visibly unmaintained', async () =>
      Boolean(await page.$('.dfbg-findings-unmaintained')))
    await waitForText('not updated')
    const movedText = await securityText()
    assert.ok((await securityScanDate()).startsWith(findingsDate), 'moved scan date must render')
    assert.match(movedText, /2 findings across 1 package/)
    assert.match(movedText, /1 high/)
    assert.match(movedText, /1 unknown/)
    assert.match(movedText, /these figures are not being updated/)
    const movedStyle = await page.$eval('.dfbg-findings-unmaintained', (element) => {
      const style = getComputedStyle(element)
      const rect = element.getBoundingClientRect()
      return {
        visible: rect.width > 0 && rect.height > 0,
        filter: style.filter,
        opacity: style.opacity,
        borderLeftStyle: style.borderLeftStyle,
        borderLeftWidth: style.borderLeftWidth,
      }
    })
    assert.deepEqual(movedStyle, {
      visible: true, filter: 'none', opacity: '1',
      borderLeftStyle: 'solid', borderLeftWidth: '2px',
    })
    await assertNoVerdicts()
  })

  await t.test('Bag Drop mirrors a bucket and un-association only acts through its confirmation', async () => {
    // The console facet of ADR-0025 (duf-d8t7), under the duf-fcg6.3 ruling:
    // Enable is the only write path and a failed resolution persists nothing.
    // Standing in acme/widgets as root: Enable is refused honestly against a
    // dead destination, succeeds against a stub mirror, the seeded bucket is
    // associated, and the destructive direction — un-associating, which
    // deletes the destination copy — acts only through its confirmation
    // (duf-mq17's severity ruling). This subtest is the named smoke gate for
    // that confirmation: without the warning step, the waits below never see it.
    const clickDeepByText = (selector, text) =>
      until(`deep-clickable "${text}"`, () =>
        page.$$eval(
          selector,
          (elements, needle) => {
            const matches = elements.filter(
              (el) => el.innerText && el.innerText.trim().includes(needle) && !el.disabled,
            )
            const deepest = matches.find(
              (el) => !matches.some((other) => other !== el && el.contains(other)),
            )
            if (!deepest) return false
            deepest.click()
            return true
          },
          text,
        ),
      )
    const stub = await startMirrorStub()
    try {
    await clickByText('a', 'Bag Drop')
    await waitForText('Mirror selected buckets to another registry')
    // Unconfigured is a rendered state, not a blank.
    await waitForText('Bag Drop is not configured')
    // One-step Enable against an unreachable destination persists NOTHING —
    // the maintainer ruling that superseded ADR-0025's save/verify/enable
    // ceremony: a failed resolution leaves no half-saved configuration.
    const deadPort = await closedPort()
    // The heading text lands before React renders the form; select() does not
    // wait, so anchor on the element itself.
    await page.waitForSelector('#bagdrop-adapter')
    await page.select('#bagdrop-adapter', 'dufflebag')
    await page.type('#bagdrop-endpoint', `https://127.0.0.1:${deadPort}`)
    await page.type('#bagdrop-organization-id', seeded.organization.id)
    await page.type('#bagdrop-project-id', seeded.project.id)
    await page.type('#bagdrop-client-id', 'smoke-mirror-client')
    await page.type('#bagdrop-client-secret', 'smoke-mirror-secret')
    await clickByText('button', 'Enable')
    await waitForText('Nothing was saved. No configuration was created.')
    // Leaving and returning re-reads the server: still unconfigured.
    await clickByText('a', 'Buckets')
    await clickByText('a', 'Bag Drop')
    await waitForText('Bag Drop is not configured')
    // Against a resolvable destination the same single action saves, verifies,
    // and enables.
    await page.waitForSelector('#bagdrop-adapter')
    await page.select('#bagdrop-adapter', 'dufflebag')
    await page.type('#bagdrop-endpoint', stub.url)
    await page.type('#bagdrop-ca-chain', stub.caChain)
    await page.type('#bagdrop-organization-id', seeded.organization.id)
    await page.type('#bagdrop-project-id', seeded.project.id)
    await page.type('#bagdrop-client-id', 'smoke-mirror-client')
    await page.type('#bagdrop-client-secret', 'smoke-mirror-secret')
    await clickByText('button', 'Enable')
    await waitForText('Secret set')
    // Enabled is proven by its off-switch appearing.
    await until('the Disable action to appear', () =>
      page.$$eval('button', (buttons) =>
        buttons.some((b) => b.innerText.trim() === 'Disable')))
    // This stack seals with the vault keyring, so the env-key warning must
    // not appear (it belongs to unencrypted deployments only).
    assert.doesNotMatch(await bodyText(), /sealed with an environment key/)
    // Disable before the association dance: it exercises the confirmation
    // flow, not destination sync, and a disabled configuration keeps
    // un-association immediate for a never-attempted bucket.
    await clickByText('button', 'Disable')
    await until('the Disable action to withdraw', () =>
      page.$$eval('button', (buttons) =>
        buttons.every((b) => b.innerText.trim() !== 'Disable')))
    // Select-and-open as one retried unit: the association run may still be
    // settling (busy disables the control, and starting a run consumes the
    // selections), and the item click TOGGLES — so it only fires when the
    // item is not already selected.
    const openStopMirroringWarning = (bucket) =>
      until(`the stop-mirroring warning for ${bucket}`, async () => {
        await page.$eval(`#mirrored-${bucket}`, (item) => {
          const selected = item.querySelector('.pf-m-selected') !== null ||
            item.className.includes('pf-m-selected') ||
            item.getAttribute('aria-selected') === 'true'
          if (!selected) {
            const target = item.querySelector('.pf-v6-c-dual-list-selector__item') ?? item
            target.click()
          }
        })
        const opened = await page.$$eval(
          'button[aria-label="Stop mirroring selected bucket"]',
          (buttons) => {
            const control = buttons[0]
            if (!control || control.disabled) return false
            control.click()
            return true
          },
        )
        if (!opened) return false
        return (await bodyText()).includes(`Stop mirroring ${bucket}?`)
      })

    // Associate the seeded bucket through the dual list.
    await clickDeepByText('#bagdrop-buckets li, #bagdrop-buckets li *', 'smoke-images')
    await page.click('button[aria-label="Mirror selected bucket"]')
    await until('the bucket to appear in the mirrored pane', async () =>
      Boolean(await page.$('#mirrored-smoke-images')))
    // The destructive direction opens the warning; Cancel changes nothing.
    // Select-and-open as one retried unit: the association run may still be
    // settling (busy disables the control, and the run consumes selections).
    await openStopMirroringWarning('smoke-images')
    await waitForText('Its copy at the destination will be deleted')
    await clickByText('button', 'Cancel')
    assert.ok(await page.$('#mirrored-smoke-images'), 'Cancel must leave the association in place')
    // Confirming acts: a never-attempted association is removed immediately
    // and the bucket returns to the local pane.
    await openStopMirroringWarning('smoke-images')
    await typeToConfirm('smoke-images')
    await clickDeepByText('button', 'Stop mirroring smoke-images')
    await until('the bucket to return to the local pane', async () =>
      Boolean(await page.$('#available-smoke-images')))
    // Leave the world as this subtest found it. Deleting the configuration
    // now demands the adapter's name typed (duf-fcg6.5).
    await clickByText('button', 'Delete configuration')
    await waitForText('Delete Bag Drop configuration?')
    await typeToConfirm('dufflebag')
    await clickByText('.pf-v6-c-modal-box button', 'Delete configuration')
    await waitForText('Bag Drop is not configured')
    } finally {
      await stub.close()
    }
  })

  await t.test('the bucket-delete confirmation warns for a Bag Drop-mirrored bucket', async () => {
    // Enable-with-body is the only config write path now, so seeding rides
    // the stub mirror and steps back down to disabled; the warning reads the
    // same reader-visible status the Bag Drop screen does.
    const rootToken = await tokenFor(credentials.clientID, credentials.secret)
    const bagdropBase =
      `/api/v1/organizations/${seeded.organization.id}` +
      `/projects/${seeded.project.id}/bagdrop`
    const stub = await startMirrorStub()
    try {
    await api(rootToken, 'POST', `${bagdropBase}/enable`, {
      adapter: 'dufflebag',
      dufflebag: {
        endpoint: stub.url,
        ca_chain: stub.caChain,
        organization_id: seeded.organization.id,
        project_id: seeded.project.id,
        client_id: 'smoke-mirror-client',
        client_secret: 'smoke-mirror-secret',
      },
    })
    await api(rootToken, 'POST', `${bagdropBase}/disable`)
    await api(rootToken, 'PUT', `${bagdropBase}/buckets/smoke-images`)

    await choosePickerOption('#tenant-bucket', 'smoke-images')
    await clickByText('button', 'Delete bucket')
    await waitForText('Delete smoke-images')
    await waitForText('This bucket is mirrored by Bag Drop')
    await clickByText('.pf-v6-c-modal-box button', 'Cancel')

    // Un-associated, the same confirmation carries no mirror warning.
    await api(rootToken, 'DELETE', `${bagdropBase}/buckets/smoke-images`)
    await clickByText('button', 'Delete bucket')
    await waitForText('Delete smoke-images')
    await until('the warning to stay absent for the un-mirrored bucket', async () =>
      !(await bodyText()).includes('mirrored by Bag Drop'))
    await clickByText('.pf-v6-c-modal-box button', 'Cancel')

    // Leave the world as this subtest found it.
    await api(rootToken, 'DELETE', bagdropBase)
    } finally {
      await stub.close()
    }
  })

  await t.test('a version is deleted through the console, refused first while assigned', async () => {
    // Self-contained bucket: two complete versions, a user channel on v1.
    const builderToken = await tokenFor(seeded.principal.client_id, seeded.principal.secret)
    const promoterToken = await tokenFor(seeded.promoter.client_id, seeded.promoter.secret)
    const bucketBase =
      `/packer/2023-01-01/organizations/${seeded.organization.id}` +
      `/projects/${seeded.project.id}/buckets`
    await api(builderToken, 'PUT', bucketBase, { name: 'smoke-deletable' })
    const delPath = `${bucketBase}/smoke-deletable/versions`
    for (const [fp, run] of [['fp-d1', 'smoke-del-1'], ['fp-d2', 'smoke-del-2']]) {
      await api(builderToken, 'POST', delPath, { fingerprint: fp, template_type: 'HCL2' })
      const { build } = await api(builderToken, 'POST', `${delPath}/${fp}/builds`, {
        component_type: 'docker.del', packer_run_uuid: run, artifacts: [],
      })
      await api(builderToken, 'PATCH', `${delPath}/${fp}/builds/${build.id}`, {
        status: 'BUILD_DONE', platform: 'docker',
        artifacts: [{ external_identifier: `del-${fp}`, region: 'local' }], metadata: {},
      })
    }
    await api(promoterToken, 'POST', `${bucketBase}/smoke-deletable/channels`, { name: 'hold' })
    await api(promoterToken, 'PATCH', `${bucketBase}/smoke-deletable/channels/hold`, {
      update_mask: 'versionFingerprint', version_fingerprint: 'fp-d1',
    })

    // The assigned version's delete is refused with the server's own words.
    await choosePickerOption('#tenant-bucket', 'smoke-deletable')
    await clickFacet('Bucket facets', 'Versions')
    await page.waitForSelector('table[aria-label="Versions"]')
    await clickByText('button', 'v1')
    await waitForText('Lineage')
    await clickByText('button', 'Delete version')
    await waitForText('Delete smoke-deletable — v1')
    await typeToConfirm('v1')
    await clickByText('.pf-v6-c-modal-box button', 'Delete version')
    await waitForText('Version is assigned by channels: hold')
    await clickByText('.pf-v6-c-modal-box button', 'Cancel')

    // Unassigned, the same action deletes; the console lands on the bucket and
    // the wire agrees the version is gone while its sibling survives.
    await api(promoterToken, 'PATCH', `${bucketBase}/smoke-deletable/channels/hold`, {
      update_mask: 'versionFingerprint', version_fingerprint: '',
    })
    await clickByText('button', 'Delete version')
    await waitForText('Delete smoke-deletable — v1')
    await typeToConfirm('v1')
    await clickByText('.pf-v6-c-modal-box button', 'Delete version')
    await until('the console to land on the bucket after deletion', async () =>
      (await bodyText()).includes('Bucket details'))
    await until('the deleted version to leave the wire', async () => {
      try {
        await api(builderToken, 'GET', `${delPath}/fp-d1`)
        return false
      } catch {
        return true
      }
    })
    const survivor = await api(builderToken, 'GET', `${delPath}/fp-d2`)
    assert.equal(survivor.version.name, 'v2')
    await api(promoterToken, 'DELETE', `${bucketBase}/smoke-deletable/channels/hold`)
    await api(promoterToken, 'DELETE', `${bucketBase}/smoke-deletable`)
  })

  let consoleBuilder
  // Bucket visits carry their selection into the tenancy (duf-4qr extended);
  // a test that intends PROJECT standing steps back up through the picker's
  // blank row first, exactly as an operator would.
  const stepUpToProject = async () => {
    if ((await pickerValue('#tenant-bucket')) === '—') return
    await page.click('#tenant-bucket')
    await clickOptionExact('—')
  }

  await t.test('a builder principal is minted through the form, where the session stands', async () => {
    // Standing in acme/widgets, the form creates a PROJECT-scoped builder —
    // least-privilege packer credentials from the console, no tenancy field
    // anywhere (duf-4qr).
    await stepUpToProject()
    await clickByText('a', 'Principals')
    await waitForText('Service principals')
    await clickByText('button', 'Create principal')
    await waitForText('New service principal')
    assert.deepEqual(await roleOptions(), ['reader', 'builder', 'publisher', 'maintainer'])
    await page.type('#principal-name', 'console-builder')
    await page.select('#principal-role', 'builder')
    // Creation alone: the principal exists holding no secrets, and no
    // credential card appears, because none was minted (duf-4ac).
    await clickByText('button', 'Create principal')
    await waitForText('console-builder')
    assert.doesNotMatch(await bodyText(), /credential issued/)
    await until('the new principal to list with no secrets', async () =>
      (await rowText('console-builder')).includes('0 of 2'))

    // Issuing is the second, explicit action, from the principal's own row —
    // the same control that adds a second secret.
    await clickInRow('console-builder', 'Issue secret')
    await clickInModal('Confirm')
    await waitForText('console-builder — credential issued')
    consoleBuilder = await readCredentialCard()
    await clickInModal('Close')
    await until('the principal to hold the issued secret', async () =>
      (await rowText('console-builder')).includes('1 of 2'))
    // The new principal lists at the scope it was created in, labelled as such.
    await waitForText('project')
    // The org-scoped seeded builder does not bleed into the project listing.
    assert.doesNotMatch(await bodyText(), /smoke-builder/)
  })

  await t.test('a revoked secret and a deleted principal stop working at the wire', async () => {
    // The console's destructive controls were the one console surface no lane
    // clicked in anger: the disabled Revoke above proves the guard, nothing
    // proved the live paths (duf-egk2.4). A disposable principal keeps the
    // fixtures the later sign-in subtests depend on intact.
    const tokenStatus = async ({ clientID, secret }) => {
      const response = await fetch(`${base}/oauth2/token`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/x-www-form-urlencoded',
          Authorization: `Basic ${Buffer.from(`${clientID}:${secret}`).toString('base64')}`,
        },
        body: new URLSearchParams({
          grant_type: 'client_credentials',
          audience: 'https://api.hashicorp.cloud',
        }),
      })
      await response.arrayBuffer()
      return response.status
    }
    const revokeFirstSecretOf = (name) =>
      until(`the first Revoke in the secrets of ${name}`, () =>
        page.$$eval(`table[aria-label="Secrets for ${name}"] tbody tr`, (rows) => {
          const button = rows[0]?.querySelector('button')
          if (!button || button.disabled) return false
          button.click()
          return true
        }))

    await clickByText('button', 'Create principal')
    await waitForText('New service principal')
    await page.type('#principal-name', 'doomed-builder')
    await page.select('#principal-role', 'builder')
    await clickByText('button', 'Create principal')
    await waitForText('doomed-builder')

    await clickInRow('doomed-builder', 'Issue secret')
    await clickInModal('Confirm')
    await waitForText('doomed-builder — credential issued')
    const doomedFirst = await readCredentialCard()
    await clickInModal('Close')
    // Baseline first: a later refusal only means the revoke did it if the
    // credential demonstrably worked beforehand.
    assert.equal(await tokenStatus(doomedFirst), 200, 'the issued credential must mint a token')

    // A second secret makes revoking the first the ordinary rotation, not the
    // sole-secret case the server refuses.
    await clickInRow('doomed-builder', 'Issue secret')
    await clickInModal('Confirm')
    await until('the second issued credential to replace the card', async () => {
      const card = await readCredentialCard()
      return card.secret !== doomedFirst.secret ? card : false
    })
    const doomedSecond = await readCredentialCard()
    await clickInModal('Close')
    await until('the principal to hold both secrets', async () =>
      (await rowText('doomed-builder')).includes('2 of 2'))

    await toggleRow('doomed-builder')
    await revokeFirstSecretOf('doomed-builder')
    await typeToConfirm('revoke')
    await clickInModal('Revoke secret')
    await until('the revoked secret to leave the listing', async () =>
      (await rowText('doomed-builder')).includes('1 of 2'))
    // The seam the unit suites cannot see: the console said it revoked, so the
    // token endpoint must refuse that secret and keep honouring the other.
    assert.equal(await tokenStatus(doomedFirst), 401, 'a revoked secret must stop minting tokens')
    assert.equal(await tokenStatus(doomedSecond), 200, 'the surviving secret must still work')

    await clickInRow('doomed-builder', 'Delete')
    await typeToConfirm('doomed-builder')
    await clickInModal('Delete principal')
    await until('the deleted principal to leave the listing', async () =>
      !(await page.$$eval('table[aria-label="Service principals"]', (tables) =>
        tables.map((table) => table.innerText).join(' '))).includes('doomed-builder'))
    assert.equal(
      await tokenStatus(doomedSecond), 401,
      'deleting a principal must retire its remaining secret',
    )
  })

  await t.test('organisation level lists org-scoped principals only', async () => {
    await choosePickerOption('#tenant-project', '—')
    await until('the project toggle to show the dash', async () =>
      (await pickerValue('#tenant-project')) === '—')
    // smoke-builder was seeded org-scoped; console-builder lives at widgets.
    await waitForText('smoke-builder')
    // The LISTING drops the project-scoped principal. The issued-credential
    // card deliberately stays on screen — it holds a secret shown once, so it
    // is not dismissed by a tenancy switch (duf-1cn) — which is why this looks
    // at the tables rather than the whole body.
    await until('the project-scoped principal to drop out of the listing', async () =>
      !(await page.$$eval('table', (tables) => tables.map((table) => table.innerText).join(' ')))
        .includes('console-builder'))
  })

  await t.test('a tenancy-scoped sign-in lands in its own tenancy', async () => {
    await clickByText('button', 'Sign out')
    await waitForText('Log in')
    await page.type('#client-id', seeded.principal.client_id)
    await page.type('#client-secret', seeded.principal.secret)
    await clickByText('button', 'Log in')
    await waitForText('Sign out')
    // The fixed organisation binding resolves through its item read, while its
    // projects resolve through the same resource-manager listing the CLI uses.
    await until('the combined toggle to name the real tenant', async () => {
      const toggle = await pickerValue('#tenant-project')
      return toggle === 'acme / widgets'
    })
    await choosePickerOption('#tenant-bucket', 'smoke-images')
    await waitForText('Bucket details')
  })

  await t.test('the picker carries the bucket to Principals, and the minted principal lands in it', async () => {
    // Ben's workflow, end to end: pick the bucket, move to Principals, and the
    // screen honours the selection — creation is bucket-scoped from context,
    // the form itself never asks (duf-4qr extended to buckets).
    // The preceding subtest leaves a builder session; this workflow is an
    // operator's, so it signs in as root first.
    await clickByText('button', 'Sign out')
    await waitForText('Log in')
    await page.type('#client-id', credentials.clientID)
    await page.type('#client-secret', credentials.secret)
    await clickByText('button', 'Log in')
    await waitForText('Sign out')
    await choosePickerOption('#tenant-organization', seeded.organization.name)
    await until('the project to follow', async () =>
      (await pickerValue('#tenant-project')) !== '')
    await choosePickerOption('#tenant-bucket', 'smoke-images')
    await waitForText('Bucket details')
    await waitForText('Principals')
    await clickByText('a', 'Principals')
    await waitForText('Service principals')
    // The carried bucket is the standing: the listing answers exactly the
    // bucket's principals, which is none yet.
    await waitForText('No bucket-scoped principals')
    assert.equal(await pickerValue('#tenant-bucket'), 'smoke-images')
    await clickByText('button', 'Create principal')
    assert.deepEqual(await roleOptions(), ['reader', 'builder', 'publisher'])
    assert.equal(await page.$eval('#principal-role', (select) => select.value), 'reader')
    await page.type('#principal-name', 'smoke-bucket-scoped')
    await clickByText('button', 'Create principal')
    await waitForText('smoke-bucket-scoped')
    // Issue its credential; the card offers the MCP environment with the
    // bucket binding stated.
    await clickInRow('smoke-bucket-scoped', 'Issue secret')
    await clickInModal('Confirm')
    await waitForText('smoke-bucket-scoped — credential issued')
    // The environment block is a collapsed expansion: its text is an input
    // value, invisible to text waits — and the one place the whole contract
    // can be read back, credential included.
    const environment = await until('the MCP environment block', () =>
      page.$$eval('[role="dialog"] input', (inputs) =>
        inputs.map((input) => input.value)
          .find((value) => value.startsWith('DFBG_MCP_ENDPOINT=')) ?? false))
    const scopedID = /DFBG_MCP_CLIENT_ID=([^\s]+)/.exec(environment)?.[1]
    const scopedSecret = /DFBG_MCP_CLIENT_SECRET=([^\s]+)/.exec(environment)?.[1]
    assert.ok(scopedID && scopedSecret, `credential missing from the environment: ${environment}`)
    assert.match(environment, /DFBG_MCP_BUCKET_ID=[0-9A-HJKMNP-TV-Z]{26}/)
    await clickInModal('Close')
    // The minted credential signs in and lands in its bucket: never a list.
    await clickByText('button', 'Sign out')
    await waitForText('Log in')
    await page.type('#client-id', scopedID)
    await page.type('#client-secret', scopedSecret)
    await clickByText('button', 'Log in')
    await waitForText('Sign out')
    // Signing in resumes the route where login happened; the landing's
    // scope routing is the root route's job.
    await page.goto(`${base}/`, { waitUntil: 'domcontentloaded' })
    await waitForText('Bucket details')
    await waitForText('smoke-images')
    assert.equal(await pickerValue('#tenant-bucket'), 'smoke-images')
    // The listing entry swaps for a way back: 'Bucket', never 'Buckets'
    // (duf-xmg5). Admin screens must not strand the session.
    const nav = await globalNavItems()
    assert.ok(!nav.includes('Buckets'), `Buckets nav offered to a bucket-scoped session: ${nav}`)
    assert.ok(nav.includes('Bucket'), `no Bucket nav for a bucket-scoped session: ${nav}`)
    // Instance names the session's bucket in the client environment — the
    // variable Packer reads when the template names none (duf-ccwl).
    await clickByText('nav.app-global-nav a', 'Instance')
    await waitForText('Client environment')
    await waitForText('HCP_PACKER_BUCKET_NAME=smoke-images')
    // From an admin screen, Bucket is the one-click path home (duf-xmg5).
    await clickByText('nav.app-global-nav a', 'Bucket')
    await waitForText('Bucket details')
    await waitForText('smoke-images')
    // The picker's Create bucket is refused by scope with the reason stated,
    // not silently no-opped by the server (duf-3p03).
    await page.click('#tenant-bucket')
    await until('the create trigger to state its scope refusal', () =>
      page.$$eval('span[aria-label="A bucket-scoped session cannot create buckets"]',
        (spans) => spans.length > 0))
    assert.equal(await buttonDisabled('Create bucket'), true)
    await page.keyboard.press('Escape')
    await page.goto(`${base}/buckets`, { waitUntil: 'domcontentloaded' })
    await waitForText('Bucket details')
    assert.doesNotMatch(await bodyText(), /All buckets/)
    await clickByText('button', 'Sign out')
    await waitForText('Log in')
    await page.type('#client-id', credentials.clientID)
    await page.type('#client-secret', credentials.secret)
    await clickByText('button', 'Log in')
    await waitForText('Sign out')
  })

  await t.test('the picker-footer create survives the picker closing under it', async () => {
    // The create modal opens from the Select's footer, and the first click
    // into the modal closes the Select — which unmounts the footer. When the
    // footer owned the modal, that click made the whole flow vanish
    // mid-submit and swallowed every failure (duf-3p03). This walks the exact
    // sequence: open picker, open modal, click into the field, submit.
    await choosePickerOption('#tenant-organization', seeded.organization.name)
    await until('the project to follow', async () =>
      (await pickerValue('#tenant-project')) !== '')
    await page.click('#tenant-bucket')
    await clickByText('button', 'Create bucket')
    await page.waitForSelector('#create-bucket-name')
    await page.click('#create-bucket-name')
    await page.type('#create-bucket-name', 'smoke-footer-created')
    await clickInModal('Create bucket')
    await waitForText('Bucket details')
    await until('the created bucket to become the selection', async () =>
      (await pickerValue('#tenant-bucket')) === 'smoke-footer-created')
  })

  await t.test('the tenancy picker-footer create survives the picker closing under it', async () => {
    // Organisation perturbs no exact fixture list or count. Click into the
    // portaled field after opening from the footer: that closes the picker and
    // proves its unmounted footer does not own the modal.
    await page.click('#tenant-organization')
    await clickByText('button', 'Create organisation')
    await page.waitForSelector('#create-organization-name')
    await page.click('#create-organization-name')
    await page.type('#create-organization-name', 'smoke-footer-org')
    await clickInModal('Create organisation')
    await until('the created organisation to become the selection', async () =>
      (await pickerValue('#tenant-organization')) === 'smoke-footer-org')
  })

  await t.test('the console-minted builder signs in, scoped to exactly its project', async () => {
    assert.ok(consoleBuilder, 'the create-through-the-form step must have captured a credential')
    await clickByText('button', 'Sign out')
    await waitForText('Log in')
    await page.type('#client-id', consoleBuilder.clientID)
    await page.type('#client-secret', consoleBuilder.secret)
    await clickByText('button', 'Log in')
    await waitForText('Sign out')
    // A PROJECT-scoped session is refused the resource-manager project listing
    // by design (ADR-0016), so it resolves only the two item reads permitted by
    // its fixed binding and must name the seeded acme/widgets pair.
    await until('the combined toggle to name exactly the created scope', async () => {
      const toggle = await pickerValue('#tenant-project')
      return toggle === 'acme / widgets'
    })
    assert.deepEqual(await globalNavItems(), ['Buckets', 'Bag Drop', 'Instance'])
    // It can read its project's bucket…
    await choosePickerOption('#tenant-bucket', 'smoke-images')
    await waitForText('Bucket details')
    // …and its role stops at builder: managing principals is refused, and the
    // screen says so rather than rendering a healthy empty table.
    await page.goto(`${base}/principals`, { waitUntil: 'domcontentloaded' })
    await waitForText('Principals could not be loaded')
    await waitForText('Your role does not permit this')
    await waitForText('Requires maintainer')
    assert.equal(await buttonDisabled('Create principal'), true)
    // Audit configuration is root-only too, and the refusal is a rendered
    // state rather than a blank or a healthy empty listing.
    await page.goto(`${base}/audit`, { waitUntil: 'domcontentloaded' })
    await waitForText('Audit targets could not be loaded')
    await waitForText('Only a platform root can view or change audit targets')
    await waitForText('Requires root')
    assert.equal(await buttonDisabled('Add target'), true)
    assert.doesNotMatch(await bodyText(), /No audit targets are configured/)
  })
})
