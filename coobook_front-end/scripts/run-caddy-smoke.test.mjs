import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createServer } from 'node:http'
import { mkdir, mkdtemp, readFile, readdir, rm, writeFile, chmod, stat, symlink } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from './run-caddy-smoke.mjs'

const runner = fileURLToPath(new URL('./run-caddy-smoke.mjs', import.meta.url))

const imageArgs = ['--frontend-image', 'frontend:fixture', '--backend-image', 'backend:fixture']
const httpDefaults = parseArgs(imageArgs)
assert.equal(httpDefaults.timeoutMs, 90000)
assert.equal(httpDefaults.commandTimeoutMs, 15000)

const browserDefaults = parseArgs([...imageArgs, '--browser'])
assert.equal(browserDefaults.timeoutMs, 240000)
assert.equal(browserDefaults.commandTimeoutMs, 120000)

const workerTemplatePath = fileURLToPath(new URL('../../deploy/cloudflare/worker.js.template', import.meta.url))
const workerTemplate = await readFile(workerTemplatePath, 'utf8')
const workerModule = await import(`data:text/javascript;base64,${Buffer.from(workerTemplate).toString('base64')}`)
assert.ok(workerTemplate.indexOf("if (url.pathname.startsWith('/api/')") < workerTemplate.indexOf('const assetPath = staticDocuments.get(url.pathname)'), 'Worker must proxy API and health routes before static document mapping')
const playwrightConfig = await readFile(new URL('../e2e/playwright.config.ts', import.meta.url), 'utf8')
const creatorProfileSpec = await readFile(new URL('../e2e/creator-profile.spec.ts', import.meta.url), 'utf8')
assert.match(playwrightConfig, /testMatch:\s*'\*\*\/\*\.spec\.ts'/, 'Caddy browser suite must use the shared all-spec discovery config')
assert.match(creatorProfileSpec, /creator profile static roots/, 'creator profile route coverage must remain a discoverable Playwright spec')
const caddyfilePath = fileURLToPath(new URL('../../deploy/Caddyfile', import.meta.url))
const caddyfile = await readFile(caddyfilePath, 'utf8')
assert.ok(caddyfile.indexOf('handle @api') < caddyfile.indexOf('import /etc/caddy/static-csp.caddy'), 'Caddy must keep API and health proxy handling before static headers and file serving')
assert.ok(caddyfile.includes('try_files {path} {path}.html'), 'Caddy must serve extensionless Astro documents from their .html output')
const assetRequests = []
const workerEnv = {
  ASSETS: {
    async fetch(request) {
      assetRequests.push(new URL(request.url))
      return new Response(request.url, { status: 200, headers: { 'content-type': 'text/html' } })
    },
  },
}
for (const [route, asset] of [
  ['/creator', '/creator.html'],
  ['/creator.html', '/creator.html'],
  ['/account/creator-profile', '/account/creator-profile.html'],
  ['/account/creator-profile.html', '/account/creator-profile.html'],
]) {
  const response = await workerModule.default.fetch(new Request(`https://cookbook.example${route}?id=synthetic`), workerEnv)
  assert.equal(response.status, 200)
  const assetRequest = assetRequests.at(-1)
  assert.equal(assetRequest.pathname, asset)
  assert.equal(assetRequest.search, '?id=synthetic')
}

for (const timeoutArgs of [
  ['--timeout-ms', '45000', '--command-timeout-ms', '30000', '--browser'],
  ['--browser', '--timeout-ms', '45000', '--command-timeout-ms', '30000'],
  ['--timeout-ms', '45000', '--browser', '--command-timeout-ms', '30000'],
]) {
  const options = parseArgs([...imageArgs, ...timeoutArgs])
  assert.equal(options.timeoutMs, 45000)
  assert.equal(options.commandTimeoutMs, 30000)
}
const fakeEngineSource = `#!/usr/bin/env node
import { readFile, writeFile } from 'node:fs/promises'
const [command, ...args] = process.argv.slice(2)
const statePath = process.env.CADDY_TEST_STATE
const state = await readFile(statePath, 'utf8').then(JSON.parse).catch(() => ({ calls: [], networks: [], containers: [] }))
const save = () => writeFile(statePath, JSON.stringify(state))
const option = (name) => args[args.indexOf(name) + 1]
const filterLabel = (prefix) => (option('--filter') ?? '').replace(prefix, '')
state.calls.push([command, ...args])

if (command === 'network' && args[0] === 'create') {
  const name = args.at(-1)
  if (process.env.CADDY_TEST_COLLISION === 'network') {
    state.networks.push({ id: 'foreign-network-id', name, label: 'unrelated=true' })
    await save()
    process.exit(125)
  }
  state.networks.push({ id: 'owned-network-id', name, label: option('--label') })
  if (process.env.CADDY_TEST_UNEXPECTED_OWNED === '1') state.networks.push({ id: 'unexpected-owned-network-id', name: 'unexpected-owned-network', label: option('--label') })
} else if (command === 'run') {
  const name = option('--name')
  const label = option('--label')
  const image = args.at(-1)
  if (process.env.CADDY_TEST_COLLISION === 'container' && image === 'frontend:collision') {
    state.containers.push({ id: 'foreign-container-id', name, label: 'unrelated=true' })
    await save()
    process.exit(125)
  }
  state.containers.push({ id: name.endsWith('-frontend') ? 'owned-frontend-id' : 'owned-backend-id', name, label })
  if (process.env.CADDY_TEST_UNEXPECTED_OWNED === '1' && name.endsWith('-frontend')) {
    state.containers.push({ id: 'unexpected-owned-container-id', name: 'unexpected-owned-container', label })
  }
  await save()
  if (process.env.CADDY_TEST_FAIL_BACKEND === '1' && image === 'backend:partial-failure') process.exit(17)
} else if (command === 'port') {
  const mapping = process.env.CADDY_TEST_PORT_MAPPING ?? (process.env.CADDY_TEST_PORT ? '127.0.0.1:' + process.env.CADDY_TEST_PORT : '127.0.0.1:43127')
  console.log(mapping)
  await save()
  if (process.env.CADDY_TEST_HOLD === '1') setInterval(() => {}, 1000)
} else if (command === 'cp') {
  await writeFile(args[1], '{"scriptHashes":[]}')
} else if (command === 'ps') {
  const label = filterLabel('label=')
  for (const item of state.containers.filter((entry) => entry.label === label)) console.log(item.id + ' ' + item.name)
} else if (command === 'network' && args[0] === 'ls') {
  const label = filterLabel('label=')
  for (const item of state.networks.filter((entry) => entry.label === label)) console.log(item.id + ' ' + item.name)
} else if (command === 'rm') {
  const id = args.at(-1)
  state.containers = state.containers.filter((entry) => entry.id !== id)
} else if (command === 'network' && args[0] === 'rm') {
  const id = args.at(-1)
  state.networks = state.networks.filter((entry) => entry.id !== id)
}

await save()
  if (command === 'network' && args[0] === 'create' && process.env.CADDY_TEST_HOLD_CREATE === '1') setInterval(() => {}, 1000)
`

async function withFakeEngine(run) {
  const directory = await mkdtemp(join(tmpdir(), 'cookbook-caddy-smoke-test-'))
  const statePath = join(directory, 'state.json')
  const engine = join(directory, 'fake-engine.mjs')
  try {
    await writeFile(engine, fakeEngineSource, { encoding: 'utf8', mode: 0o700 })
    await chmod(engine, 0o700)
    await run({ directory, statePath, engine })
  } finally {
    await rm(directory, { recursive: true, force: true })
  }
}

function startRunner(args, env) {
  const child = spawn(process.execPath, [runner, ...args], { env: { ...process.env, ...env }, stdio: ['ignore', 'pipe', 'pipe'] })
  let stdout = ''
  let stderr = ''
  const result = new Promise((resolve, reject) => {
    const timeout = setTimeout(async () => {
      const trace = env.CADDY_TEST_STATE ? await readFile(env.CADDY_TEST_STATE, 'utf8').catch(() => '') : ''
      child.kill('SIGTERM')
      const force = setTimeout(() => child.kill('SIGKILL'), 1000)
      force.unref()
      reject(new Error(`Caddy smoke fake-engine test exceeded its 8 second limit: ${args.join(' ')}; calls ${trace}`))
    }, 8000)
    child.stdout.setEncoding('utf8').on('data', (chunk) => { stdout += chunk })
    child.stderr.setEncoding('utf8').on('data', (chunk) => { stderr += chunk })
    child.once('error', (error) => { clearTimeout(timeout); reject(error) })
    child.once('close', (code, signal) => { clearTimeout(timeout); resolve({ code, signal, stdout, stderr }) })
  })
  return { child, result }
}

async function runRunner(args, env) {
  const execution = startRunner(args, env)
  try {
    return await execution.result
  } finally {
    if (execution.child.exitCode === null && execution.child.signalCode === null) {
      execution.child.kill('SIGTERM')
      await Promise.race([execution.result.catch(() => {}), new Promise((resolve) => setTimeout(resolve, 1500))])
      if (execution.child.exitCode === null && execution.child.signalCode === null) execution.child.kill('SIGKILL')
    }
  }
}

async function loadState(path) {
  return JSON.parse(await readFile(path, 'utf8'))
}

const fakePlaywrightSource = `#!/usr/bin/env node
import assert from 'node:assert/strict'
import { readFile, writeFile, stat } from 'node:fs/promises'
import { dirname, join } from 'node:path'
if (process.env.CADDY_BROWSER_GREP_EXPECTED) assert.ok(process.argv.includes('--grep=' + process.env.CADDY_BROWSER_GREP_EXPECTED))
assert.ok(process.argv.includes('--workers=1'))
assert.ok(process.argv.includes('--reporter=json'))
assert.equal(process.env.PLAYWRIGHT_BASE_URL, 'http://localhost:' + process.env.CADDY_TEST_PORT)
assert.equal(process.env.E2E_EMAIL_CAPTURE, join(dirname(process.env.E2E_EMAIL_CAPTURE), 'capture.json'))
assert.equal(process.env.PLAYWRIGHT_JSON_OUTPUT_FILE, join(process.env.PLAYWRIGHT_OUTPUT_DIR, 'playwright-report.json'))
assert.equal(await readFile(join(process.env.PLAYWRIGHT_DIST, 'static-csp.json'), 'utf8'), '{"scriptHashes":[]}')
const tempRoot = dirname(dirname(process.env.E2E_EMAIL_CAPTURE))
assert.equal((await stat(tempRoot)).mode & 0o777, 0o700)
assert.equal((await stat(dirname(process.env.E2E_EMAIL_CAPTURE))).mode & 0o777, 0o777)
const helper = await readFile(join(dirname(process.env.E2E_EMAIL_CAPTURE), 'capture-email'), 'utf8')
assert.ok(helper.includes('cat > /smoke-mail/capture.json'))
assert.ok(helper.includes('chmod 644'))
await writeFile(process.env.CADDY_BROWSER_STATE, JSON.stringify({
  baseUrl: process.env.PLAYWRIGHT_BASE_URL,
  dist: process.env.PLAYWRIGHT_DIST,
  output: process.env.PLAYWRIGHT_OUTPUT_DIR,
  browsers: process.env.PLAYWRIGHT_BROWSERS_PATH,
  capture: process.env.E2E_EMAIL_CAPTURE,
  report: process.env.PLAYWRIGHT_JSON_OUTPUT_FILE,
}))
const actionability = process.env.CADDY_BROWSER_ACTIONABILITY ?? ''
const malformedSteps = process.env.CADDY_BROWSER_MALFORMED_STEPS === '1'
const interceptionFixture = ['pointer_interception', 'all', 'unknown'].includes(actionability)
const result = (status, file, line, column, name = 'Error', errors = []) => ({
  status: status === 'unexpected' ? 'failed' : status === 'skipped' ? 'skipped' : 'passed',
  error: status === 'unexpected' ? {
    name: interceptionFixture ? 'TimeoutError' : name,
    message: interceptionFixture ? 'TimeoutError: Test timeout of 30000ms exceeded' : 'raw message with https://secret.invalid/?token=hidden',
    stack: '/private/secret/stack',
  } : undefined,
  errors: status === 'unexpected' ? [{ name, message: name + ': private error text', stack: '/private/secret/stack' }, ...errors] : [],
  errorLocation: status === 'unexpected' ? { file, line, column } : undefined,
  steps: status === 'unexpected' && interceptionFixture ? [{
    title: 'Open owner recipe dialog',
    category: 'test.step',
    steps: [{
      title: 'locator.click',
      category: 'pw:api',
      error: {
        name: 'TimeoutError',
        message: actionability === 'all'
          ? 'element intercepts pointer events; element is not stable; element detached from DOM; element is not visible; element is not enabled; element is outside of the viewport\\n- <button class="recipe-card" aria-label="synthetic">recipe</button> intercepts pointer events'
          : actionability === 'unknown'
            ? '- <x-secret class="secret-class" role="private">private user text</x-secret> intercepts pointer events'
            : 'TimeoutError: locator.click timed out; <a class="text-link" role="link">private link text</a> intercepts pointer events at https://secret.invalid/path?token=secret\\n- <div class="dialog-backdrop" role="presentation">/private/secret/email@example.test</div> intercepts pointer events',
      },
    }],
  }] : malformedSteps ? [null, { title: { token: 'malformed-secret' }, error: { message: { token: 'nested-secret' } }, steps: 'not-an-array' }] : undefined,
})
const spec = (title, status, file, line, column, name, errors) => ({
  title, file, line: 1, column: 1,
  tests: [{ status, expectedStatus: 'passed', results: [result(status, file, line, column, name, errors)] }],
})
const failed = process.env.CADDY_BROWSER_FAIL === '1'
const specs = failed ? [
  spec('opaque publication title with token=secret', 'unexpected', 'e2e/recipe-publication.spec.ts', 47, 9, 'PrivateTokenError'),
  spec('interleaved passing case', 'expected', 'e2e/static-host.spec.ts', 10, 1),
  spec('opaque auth title', 'unexpected', 'e2e/auth.ts', 221, 14, 'Error', [{ name: 'TimeoutError', message: 'secondary failure; /private/secret/e2e/static-host.spec.ts:321:4' }]),
  spec('unknown source title', 'unexpected', '/tmp/attacker/e2e/auth.ts', 2, 4),
  spec('out of range title', 'unexpected', 'e2e/static-host.spec.ts', 9999999, 1),
  spec('private creator profile source title', 'unexpected', 'e2e/creator-profile.spec.ts', 18, 7),
  spec('interleaved skipped case', 'skipped', 'e2e/static-host.spec.ts', 11, 1),
] : [
  spec('direct route serves built page', 'expected', 'e2e/static-host.spec.ts', 5, 1),
  spec('optional browser capability', 'skipped', 'e2e/static-host.spec.ts', 6, 1),
]
const reportMode = process.env.CADDY_BROWSER_REPORT_MODE ?? ''
if (reportMode === 'unknown-status') specs[0].tests[0].status = 'unrecognized'
if (reportMode === 'unknown-result-status') specs[0].tests[0].results[0].status = 'unrecognized'
if (reportMode === 'expected-failed-result') specs[0].tests[0].results[0].status = 'failed'
if (reportMode === 'all-skipped' || reportMode === 'skipped-passed-result') {
  specs.splice(0, specs.length, spec('skipped only', 'skipped', 'e2e/static-host.spec.ts', 7, 1))
}
let report = {
  stats: { expected: 1, unexpected: failed ? 5 : 0, skipped: 1, flaky: 0 },
  suites: [{ title: 'e2e', file: '/private/secret/e2e', line: 0, column: 0, specs }],
}
if (reportMode === 'empty') report = {}
if (reportMode === 'wrong-shape') report = { stats: { expected: 0, unexpected: 0, flaky: 0, skipped: 0 }, suites: [] }
if (reportMode === 'all-skipped' || reportMode === 'skipped-passed-result') report = { stats: { expected: 0, unexpected: 0, flaky: 0, skipped: 1 }, suites: [{ title: 'e2e', file: 'e2e', specs }] }
if (reportMode === 'skipped-passed-result') report.suites[0].specs[0].tests[0].results[0].status = 'passed'
const expectedReportPath = process.env.CADDY_BROWSER_EXPECTED_REPORT
if (reportMode === 'missing') {
  // Exercise the runner's missing-report path.
} else if (reportMode === 'malformed') {
  await writeFile(process.env.PLAYWRIGHT_JSON_OUTPUT_FILE, '{')
} else if (reportMode === 'oversized') {
  await writeFile(process.env.PLAYWRIGHT_JSON_OUTPUT_FILE, 'x'.repeat(1024 * 1024 + 1))
} else {
  const reportBytes = JSON.stringify(report)
  await writeFile(process.env.PLAYWRIGHT_JSON_OUTPUT_FILE, reportBytes)
  if (expectedReportPath) await writeFile(expectedReportPath, reportBytes)
}
console.error('Error: unrelated stderr with /private/secret/e2e/static-host.spec.ts:888:2 and token=not-for-summary')
if (process.env.CADDY_BROWSER_ZERO_EXIT !== '1' && (failed || process.env.CADDY_BROWSER_NONZERO_EXIT === '1')) process.exit(9)
`

async function runBrowserFixture({ directory, statePath, engine }, port, { fail = false, grep, reportMode = '', zeroExit = false, nonzeroExit = false, actionability = '', malformedSteps = false, privateReport = false, runnerFailure = fail || Boolean(reportMode) || nonzeroExit } = {}) {
  const browserCache = join(directory, 'browser-cache')
  const playwright = join(directory, 'fake-playwright.mjs')
  const browserState = join(directory, 'browser-state.json')
  const privateReportDirectory = privateReport ? join(directory, 'private-report') : undefined
  const expectedReportPath = join(directory, 'expected-report.json')
  await writeFile(playwright, fakePlaywrightSource, { encoding: 'utf8', mode: 0o700 })
  await chmod(playwright, 0o700)
  const syntaxCheck = spawn(process.execPath, ['--check', playwright], { stdio: ['ignore', 'pipe', 'pipe'] })
  let syntaxStderr = ''
  syntaxCheck.stderr.setEncoding('utf8').on('data', (chunk) => { syntaxStderr += chunk })
  const syntaxExit = await new Promise((resolve) => syntaxCheck.once('close', resolve))
  assert.equal(syntaxExit, 0, syntaxStderr)
  await mkdir(browserCache)
  const result = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:browser-fixture',
    '--backend-image', 'backend:browser-fixture',
    '--browser',
    ...(grep ? ['--browser-grep', grep] : []),
    ...(privateReportDirectory ? ['--private-report-dir', privateReportDirectory] : []),
  ], {
    CADDY_TEST_STATE: statePath,
    CADDY_TEST_PORT: String(port),
    CADDY_BROWSER_STATE: browserState,
    CADDY_BROWSER_EXPECTED_REPORT: expectedReportPath,
    CADDY_BROWSER_FAIL: fail ? '1' : '0',
    CADDY_BROWSER_GREP_EXPECTED: grep ?? '',
    CADDY_BROWSER_REPORT_MODE: reportMode,
    CADDY_BROWSER_ACTIONABILITY: actionability,
    CADDY_BROWSER_MALFORMED_STEPS: malformedSteps ? '1' : '0',
    CADDY_BROWSER_ZERO_EXIT: zeroExit ? '1' : '0',
    CADDY_BROWSER_NONZERO_EXIT: nonzeroExit ? '1' : '0',
    COOKBOOK_CADDY_SMOKE_PLAYWRIGHT_CLI: playwright,
    PLAYWRIGHT_BROWSERS_PATH: browserCache,
    TMPDIR: directory,
  })
  assert.equal(result.code, runnerFailure ? 1 : 0, result.stdout + result.stderr)
  const state = await loadState(statePath)
  const backend = state.calls.find((record) => record[0] === 'run' && record.includes('backend:browser-fixture'))
  assert.ok(backend)
  assert.ok(backend.includes('--tmpfs') && backend.some((arg) => arg.startsWith('/data:')))
  assert.ok(backend.some((arg) => arg === `COOKBOOK_PUBLIC_URL=http://localhost:${port}`))
  assert.ok(backend.some((arg) => arg === `COOKBOOK_RP_ORIGIN=http://localhost:${port}`))
  assert.ok(backend.includes('COOKBOOK_EMAIL_COMMAND=/smoke-mail/capture-email'))
  assert.ok(backend.includes('COOKBOOK_REGISTRATION=open'))
  assert.ok(backend.includes('COOKBOOK_ADMIN_EMAIL=admin@example.test'))
  assert.ok(backend.includes('COOKBOOK_ADMIN_NAME=E2E Admin'))
  assert.ok(backend.some((arg) => arg.includes(':/smoke-mail:rw,Z')))
  const frontend = state.calls.find((record) => record[0] === 'run' && record.includes('frontend:browser-fixture'))
  assert.ok(frontend)
  assert.ok(frontend.includes('127.0.0.1::8080/tcp'))
  assert.ok(state.calls.some((record) => record[0] === 'cp' && record[1].endsWith(':/srv/static-csp.json')))
  assert.equal(state.containers.length, 0)
  assert.equal(state.networks.length, 0)
  const allowed = ['browser-cache', 'fake-engine.mjs', 'fake-playwright.mjs', 'state.json', 'browser-state.json']
  if ((await readdir(directory)).includes('expected-report.json')) allowed.push('expected-report.json')
  if (privateReportDirectory) allowed.push('private-report')
  assert.deepEqual((await readdir(directory)).sort(), allowed.sort())
  const browser = JSON.parse(await readFile(browserState, 'utf8'))
  assert.equal(browser.baseUrl, `http://localhost:${port}`)
  assert.equal(browser.browsers, browserCache)
  assert.match(browser.report, /playwright-output\/playwright-report\.json$/)
  assert.match(browser.dist, /playwright-dist$/)
  assert.match(browser.output, /playwright-output$/)
  assert.match(browser.capture, /\/mail\/capture\.json$/)
  if (privateReportDirectory) {
    assert.equal((await stat(privateReportDirectory)).mode & 0o777, 0o700)
    const captured = join(privateReportDirectory, 'playwright-report.json')
    const expectedBytes = await readFile(expectedReportPath).catch(() => undefined)
    if (expectedBytes) {
      assert.equal((await stat(captured)).mode & 0o777, 0o600)
      assert.deepEqual(await readFile(captured), expectedBytes)
    } else {
      assert.equal((await readdir(privateReportDirectory)).length, 0)
    }
  }
  return result
}

async function withHttpFixture(run, { redirectRoot = false, oversizeRoot = false, creatorFault = '' } = {}) {
  const csp = "default-src 'self'; script-src 'self'; style-src 'self'"
  const creatorPages = new Map([
    ['/creator', 'View a local creator profile on this Cookbook instance.'],
    ['/creator.html', 'View a local creator profile on this Cookbook instance.'],
    ['/account/creator-profile', 'Manage your local creator profile on this Cookbook instance.'],
    ['/account/creator-profile.html', 'Manage your local creator profile on this Cookbook instance.'],
  ])
  const server = createServer((request, response) => {
    if (redirectRoot && request.url === '/') {
      response.writeHead(302, { location: 'http://example.invalid/redirect-target' }).end()
      return
    }
    if (oversizeRoot && request.url === '/') {
      response.writeHead(200, {
        'content-type': 'text/html', 'cache-control': 'no-store', 'content-security-policy': csp,
        'content-length': String(2 * 1024 * 1024 + 1),
      }).end('oversize fixture')
      return
    }
    if (request.url === '/healthz') {
      response.writeHead(200, { 'content-type': 'application/json', 'content-security-policy': csp }).end('{"status":"ok"}')
    } else if (request.url === '/') {
      response.writeHead(200, { 'content-type': 'text/html', 'cache-control': 'no-store', 'content-security-policy': csp })
        .end('<!doctype html><html><script src="/_astro/app.js"></script></html>')
    } else if (request.url?.startsWith('/auth/confirm?')) {
      response.writeHead(200, { 'content-type': 'text/html', 'cache-control': 'no-store', 'content-security-policy': csp })
        .end('<!doctype html><html><title>Confirm</title></html>')
    } else if (creatorPages.has(new URL(request.url ?? '/', 'http://localhost').pathname)) {
      const path = new URL(request.url ?? '/', 'http://localhost').pathname
      const description = creatorPages.get(path)
      const route = path.startsWith('/account/') ? 'account/creator-profile' : 'creator'
      const headers = {
        'content-type': 'text/html', 'cache-control': 'no-store',
        'content-security-policy': csp, 'referrer-policy': 'no-referrer',
      }
      if (creatorFault === 'missing-csp') delete headers['content-security-policy']
      if (creatorFault === 'cacheable') headers['cache-control'] = 'public, max-age=60'
      if (creatorFault === 'missing-referrer') delete headers['referrer-policy']
      const bodyRoute = creatorFault === 'wrong-alias' && path.endsWith('.html') ? 'other-page' : route
      response.writeHead(200, headers).end(`<!doctype html><html><head><title>Creator profile — Cookbook</title><meta name="description" content="${description}"></head><body>${bodyRoute}</body></html>`)
    } else if (request.url === '/_astro/app.js') {
      response.writeHead(200, { 'content-type': 'text/javascript', 'cache-control': 'public, max-age=31536000, immutable' }).end('')
    } else if (request.url === '/api/auth/consume' && request.method === 'POST') {
      const expectedPublicOrigin = `http://localhost:${server.address().port}`
      if (request.headers.host !== `localhost:${server.address().port}` || request.headers.origin !== expectedPublicOrigin) {
        response.writeHead(403, { 'content-type': 'application/json' }).end('{"error":"csrf_origin_mismatch"}')
      } else {
        response.writeHead(400, { 'content-type': 'application/json', 'cache-control': 'no-store', 'content-security-policy': csp })
          .end('{"error":"invalid_or_expired_token"}')
      }
    } else if (request.url?.startsWith('/api/')) {
      response.writeHead(401, { 'content-type': 'application/json', 'content-security-policy': csp }).end('{"error":"unauthorized"}')
    } else {
      response.writeHead(404).end('not found')
    }
  })
  await new Promise((resolve, reject) => server.once('error', reject).listen(0, '127.0.0.1', resolve))
  try {
    await run(server.address().port)
  } finally {
    await new Promise((resolveClose) => server.close(resolveClose))
  }
}

await withFakeEngine(async ({ statePath, engine }) => {
  const result = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend image:fixture',
    '--backend-image', 'backend:partial-failure',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_FAIL_BACKEND: '1' })
  assert.equal(result.code, 1, result.stdout + result.stderr)
  assert.match(result.stderr, /run failed \(17\)/)

  const state = await loadState(statePath)
  const frontend = state.calls.find((record) => record[0] === 'run' && record.includes('frontend image:fixture'))
  const backend = state.calls.find((record) => record[0] === 'run' && record.includes('backend:partial-failure'))
  assert.ok(frontend, 'frontend image argument with spaces was not forwarded as one argument')
  assert.ok(backend, 'backend image argument was not forwarded')
  assert.ok(frontend.includes('--pull=never'), 'frontend image was not protected from pulls')
  assert.ok(backend.includes('--tmpfs') && backend.some((arg) => arg.startsWith('/data:')))
  assert.ok(frontend.includes('--label') && backend.includes('--label'))
  assert.equal(frontend[frontend.indexOf('--label') + 1], backend[backend.indexOf('--label') + 1])
  assert.equal(state.containers.length, 0, 'partially started owned containers were not removed')
  assert.equal(state.networks.length, 0, 'owned network was not removed')
})

await withFakeEngine(async ({ statePath, engine }) => {
  const result = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:collision',
    '--backend-image', 'backend:fixture',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_COLLISION: 'container' })
  assert.equal(result.code, 1)
  const state = await loadState(statePath)
  assert.equal(state.containers.length, 1, 'cleanup removed an unrelated same-name container')
  assert.equal(state.containers[0].id, 'foreign-container-id')
  assert.equal(state.networks.length, 0, 'owned network was not removed after container-name collision')
  assert.equal(state.calls.some((record) => record[0] === 'rm'), false, 'collision cleanup attempted to remove a container by name')
})

await withFakeEngine(async ({ statePath, engine }) => {
  const result = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:fixture',
    '--backend-image', 'backend:fixture',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_COLLISION: 'network' })
  assert.equal(result.code, 1)
  const state = await loadState(statePath)
  assert.equal(state.networks.length, 1, 'cleanup removed an unrelated same-name network')
  assert.equal(state.networks[0].id, 'foreign-network-id')
  assert.equal(state.containers.length, 0)
  assert.equal(state.calls.some((record) => record[0] === 'network' && record[1] === 'rm'), false, 'collision cleanup attempted to remove a network by name')
})

await withHttpFixture(async (port) => withFakeEngine(async ({ statePath, engine }) => {
  const result = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:fixture',
    '--backend-image', 'backend:fixture',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_PORT: String(port) })
  assert.equal(result.code, 0, result.stdout + result.stderr)
  assert.match(result.stdout, /Caddy\/Go proxy smoke passed/)
  const state = await loadState(statePath)
  assert.equal(state.containers.length, 0, 'successful run left owned containers behind')
  assert.equal(state.networks.length, 0, 'successful run left owned network behind')
}))

await withFakeEngine(async ({ statePath, engine }) => {
  const result = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:fixture',
    '--backend-image', 'backend:fixture',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_PORT_MAPPING: '0.0.0.0:43127' })
  assert.equal(result.code, 1)
  assert.match(result.stderr, /strictly on loopback/)
  const state = await loadState(statePath)
  assert.equal(state.containers.length, 0)
  assert.equal(state.networks.length, 0)
})

await withFakeEngine(async ({ statePath, engine }) => {
  const result = await runRunner([
    '--engine', engine,
    '--timeout-ms', '1000',
    '--frontend-image', 'frontend:fixture',
    '--backend-image', 'backend:fixture',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_HOLD_CREATE: '1' })
  assert.equal(result.code, 1, result.stdout + result.stderr)
  assert.match(result.stderr, /overall timeout/)
  const state = await loadState(statePath)
  assert.equal(state.networks.length, 0, 'overall timeout left its created network behind')
  assert.equal(state.containers.length, 0)
})

await withFakeEngine(async ({ statePath, engine }) => {
  const result = await runRunner([
    '--engine', engine,
    '--command-timeout-ms', '1000',
    '--frontend-image', 'frontend:fixture',
    '--backend-image', 'backend:fixture',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_HOLD_CREATE: '1' })
  assert.equal(result.code, 1, result.stdout + result.stderr)
  assert.match(result.stderr, /network timed out after 1000ms/)
  const state = await loadState(statePath)
  assert.equal(state.networks.length, 0, 'command timeout left its owned network behind')
  assert.equal(state.containers.length, 0)
})

await withFakeEngine(async ({ statePath, engine }) => {
  const result = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:fixture',
    '--backend-image', 'backend:partial-failure',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_FAIL_BACKEND: '1', CADDY_TEST_UNEXPECTED_OWNED: '1' })
  assert.equal(result.code, 1, result.stdout + result.stderr)
  assert.match(result.stderr, /unexpected container/)
  assert.match(result.stderr, /unexpected network/)
  const state = await loadState(statePath)
  assert.deepEqual(state.containers.map((entry) => entry.id), ['unexpected-owned-container-id'])
  assert.deepEqual(state.networks.map((entry) => entry.id), ['unexpected-owned-network-id'])
})

await withHttpFixture(async (port) => withFakeEngine(async ({ statePath, engine }) => {
  const result = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:fixture',
    '--backend-image', 'backend:fixture',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_PORT: String(port) })
  assert.equal(result.code, 1)
  assert.match(result.stderr, /redirect|fetch/i)
  const state = await loadState(statePath)
  assert.equal(state.containers.length, 0)
  assert.equal(state.networks.length, 0)
}), { redirectRoot: true })

await withHttpFixture(async (port) => withFakeEngine(async ({ statePath, engine }) => {
  const result = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:fixture',
    '--backend-image', 'backend:fixture',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_PORT: String(port) })
  assert.equal(result.code, 1)
  assert.match(result.stderr, /static root body exceeds/)
  const state = await loadState(statePath)
  assert.equal(state.containers.length, 0)
  assert.equal(state.networks.length, 0)
}), { oversizeRoot: true })

await withHttpFixture(async (port) => withFakeEngine(async ({ statePath, engine }) => {
  const result = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:fixture',
    '--backend-image', 'backend:fixture',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_PORT: String(port) })
  assert.equal(result.code, 0, result.stdout + result.stderr)
  assert.match(result.stdout, /Caddy\/Go proxy smoke passed/)
  const state = await loadState(statePath)
  assert.equal(state.containers.length, 0)
  assert.equal(state.networks.length, 0)
}))

for (const [creatorFault, expected] of [
  ['missing-csp', /generated CSP/],
  ['cacheable', /not no-store/],
  ['missing-referrer', /Referrer-Policy/],
  ['wrong-alias', /did not serve the canonical document/],
]) {
  await withHttpFixture(async (port) => withFakeEngine(async ({ statePath, engine }) => {
    const result = await runRunner([
      '--engine', engine,
      '--frontend-image', 'frontend:fixture',
      '--backend-image', 'backend:fixture',
    ], { CADDY_TEST_STATE: statePath, CADDY_TEST_PORT: String(port) })
    assert.equal(result.code, 1)
    assert.match(result.stderr, expected)
    const state = await loadState(statePath)
    assert.equal(state.containers.length, 0)
    assert.equal(state.networks.length, 0)
  }), { creatorFault })
}

await withFakeEngine(async ({ statePath, engine }) => {
  const execution = startRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:signal',
    '--backend-image', 'backend:signal',
  ], { CADDY_TEST_STATE: statePath, CADDY_TEST_HOLD: '1' })
  try {
    let state
    for (let attempt = 0; attempt < 100; attempt++) {
      try { state = await loadState(statePath) } catch { /* Wait for the fake engine to reach its held port query. */ }
      if (state?.calls.some((record) => record[0] === 'port')) break
      await new Promise((resolve) => setTimeout(resolve, 25))
    }
    assert.ok(state?.calls.some((record) => record[0] === 'port'), 'runner did not reach the signal test hold point')
    execution.child.kill('SIGTERM')
    const result = await execution.result
    assert.equal(result.code, 143, result.stdout + result.stderr)
    state = await loadState(statePath)
    assert.equal(state.containers.length, 0, 'SIGTERM left the owned frontend container behind')
    assert.equal(state.networks.length, 0, 'SIGTERM left the owned network behind')
  } finally {
    if (execution.child.exitCode === null && execution.child.signalCode === null) execution.child.kill('SIGTERM')
  }
})

await withHttpFixture(async (port) => withFakeEngine(async (context) => {
  const result = await runBrowserFixture(context, port, { grep: 'signed-in account users|signed-in instance administrators|email sign-in remains explicit' })
  assert.equal(result.code, 0, result.stdout + result.stderr)
  assert.match(result.stdout, /Playwright passed: 1 passed, 0 failed, 1 skipped/)
  assert.match(result.stdout, /Caddy\/Go proxy smoke passed/)
}))

await withHttpFixture(async (port) => withFakeEngine(async (context) => {
  const result = await runBrowserFixture(context, port, { privateReport: true })
  assert.equal(result.code, 0, result.stdout + result.stderr)
  assert.match(result.stdout, /Private Playwright report: captured/)
  assert.match(result.stdout, /Caddy\/Go proxy smoke passed/)
}))

for (const reportMode of ['missing', 'malformed', 'oversized']) {
  await withHttpFixture(async (port) => withFakeEngine(async (context) => {
    const result = await runBrowserFixture(context, port, { fail: true, reportMode, privateReport: true })
    assert.equal(result.code, 1, result.stdout + result.stderr)
    assert.match(result.stdout, new RegExp(`Private Playwright report: ${reportMode === 'oversized' ? 'oversized' : reportMode}`))
    assert.deepEqual(await readdir(join(context.directory, 'private-report')), [])
  }))
}

await withHttpFixture(async (port) => withFakeEngine(async (context) => {
  const result = await runBrowserFixture(context, port, { fail: true, actionability: 'pointer_interception' })
  assert.equal(result.code, 1, result.stdout + result.stderr)
  assert.match(result.stderr, /Playwright failed: 1 passed, 5 failed, 1 skipped/)
  assert.match(result.stderr, /failed test #1 \(unknown; source=publication-spec; line=47; column=9\)/)
  assert.match(result.stderr, /phase=open_owner_recipe_dialog/)
  assert.match(result.stderr, /actionability=pointer_interception=true,instability=false,detachment=false,invisibility=false,disabled=false,outside_viewport=false/)
  assert.match(result.stderr, /failed test #3 \(Error; source=auth-helper; line=221; column=14\)/)
  assert.match(result.stderr, /failed test #4 \(Error; source=unknown; line=unknown; column=unknown\)/)
  assert.match(result.stderr, /failed test #5 \(Error; source=unknown; line=unknown; column=unknown\)/)
  assert.match(result.stderr, /failed test #6 \(Error; source=creator-profile-spec; line=18; column=7\)/)
  assert.doesNotMatch(result.stderr, /opaque publication title|opaque auth title|unknown source title|out of range title|https:\/\/|\/private\/secret|token=|PrivateTokenError|TimeoutError|attacker|9999999|888:2|dialog-backdrop|text-link|private link text|<div/)
}))

await withHttpFixture(async (port) => withFakeEngine(async (context) => {
  const result = await runBrowserFixture(context, port, { fail: true, malformedSteps: true })
  assert.equal(result.code, 1, result.stdout + result.stderr)
  assert.match(result.stderr, /phase=unknown actionability=pointer_interception=false,instability=false,detachment=false,invisibility=false,disabled=false,outside_viewport=false/)
  assert.doesNotMatch(result.stdout + result.stderr, /malformed-secret|nested-secret|token=|https:\/\/|\/private\/secret/)
}))

await withHttpFixture(async (port) => withFakeEngine(async (context) => {
  const result = await runBrowserFixture(context, port, { fail: true, actionability: 'unknown' })
  assert.equal(result.code, 1, result.stdout + result.stderr)
  assert.match(result.stderr, /phase=open_owner_recipe_dialog actionability=pointer_interception=true,instability=false,detachment=false,invisibility=false,disabled=false,outside_viewport=false/)
  assert.doesNotMatch(result.stdout + result.stderr, /https:\/\/|\/private\/secret|token=|email@example\.test|secret-class|private user text|<x-secret/)
}))

await withHttpFixture(async (port) => withFakeEngine(async (context) => {
  const result = await runBrowserFixture(context, port, { fail: true, actionability: 'all' })
  assert.equal(result.code, 1, result.stdout + result.stderr)
  assert.match(result.stderr, /actionability=pointer_interception=true,instability=true,detachment=true,invisibility=true,disabled=true,outside_viewport=true/)
  assert.doesNotMatch(result.stdout + result.stderr, /https:\/\/|\/private\/secret|token=|email@example\.test|recipe-card|synthetic/)
}))

for (const reportMode of ['missing', 'malformed', 'oversized', 'empty', 'wrong-shape', 'unknown-status', 'unknown-result-status', 'all-skipped', 'expected-failed-result', 'skipped-passed-result']) {
  await withHttpFixture(async (port) => withFakeEngine(async (context) => {
    const result = await runBrowserFixture(context, port, { reportMode })
    assert.equal(result.code, 1, result.stdout + result.stderr)
    assert.doesNotMatch(result.stdout + result.stderr, /Caddy\/Go proxy smoke passed/)
    assert.match(result.stderr, reportMode === 'missing' || reportMode === 'malformed' || reportMode === 'oversized'
      ? /structured Playwright report unavailable/
      : /structured Playwright report rejected/)
    assert.doesNotMatch(result.stdout + result.stderr, /\/tmp\/|playwright-report\.json|secret|token=/)
  }))
}

await withHttpFixture(async (port) => withFakeEngine(async (context) => {
  const result = await runBrowserFixture(context, port, { fail: true, zeroExit: true })
  assert.equal(result.code, 1, result.stdout + result.stderr)
  assert.match(result.stderr, /Playwright process\/report status mismatch/)
  assert.doesNotMatch(result.stdout + result.stderr, /Caddy\/Go proxy smoke passed/)
}))

await withHttpFixture(async (port) => withFakeEngine(async (context) => {
  const result = await runBrowserFixture(context, port, { nonzeroExit: true })
  assert.equal(result.code, 1, result.stdout + result.stderr)
  assert.match(result.stderr, /Playwright process\/report status mismatch/)
  assert.doesNotMatch(result.stdout + result.stderr, /Caddy\/Go proxy smoke passed/)
}))

const missingImages = await runRunner(['--frontend-image', 'frontend:fixture'], {})
assert.equal(missingImages.code, 1)
assert.match(missingImages.stderr, /provide both --frontend-image and --backend-image/)
await withFakeEngine(async ({ statePath, engine }) => {
  await writeFile(statePath, JSON.stringify({ calls: [], networks: [], containers: [] }))
  const grepWithoutBrowser = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:fixture', '--backend-image', 'backend:fixture', '--browser-grep', 'route test',
  ], { CADDY_TEST_STATE: statePath })
  assert.equal(grepWithoutBrowser.code, 1)
  assert.match(grepWithoutBrowser.stderr, /browser-grep requires --browser/)
  assert.deepEqual((await loadState(statePath)).calls, [], 'invalid browser-grep arguments started the container engine')
})
const longBrowserGrep = await runRunner([
  '--frontend-image', 'frontend:fixture', '--backend-image', 'backend:fixture', '--browser-grep', 'x'.repeat(201),
], {})
assert.equal(longBrowserGrep.code, 1)
assert.match(longBrowserGrep.stderr, /browser-grep must be a single-line pattern up to 200 characters/)

await withFakeEngine(async ({ statePath, engine, directory }) => {
  await writeFile(statePath, JSON.stringify({ calls: [], networks: [], containers: [] }))
  const existingDirectory = join(directory, 'existing-private-report')
  await mkdir(existingDirectory, { mode: 0o700 })
  const occupiedPrivateReport = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:fixture', '--backend-image', 'backend:fixture', '--browser',
    '--private-report-dir', existingDirectory,
  ], { CADDY_TEST_STATE: statePath })
  assert.equal(occupiedPrivateReport.code, 1)
  assert.match(occupiedPrivateReport.stderr, /private report directory must be a new directory/)
  assert.deepEqual((await loadState(statePath)).calls, [], 'existing private report directory started the container engine')
  const symlinkTarget = join(directory, 'symlink-target')
  await mkdir(symlinkTarget, { mode: 0o700 })
  const symlinkPath = join(directory, 'private-report-symlink')
  await symlink(symlinkTarget, symlinkPath)
  const symlinkPrivateReport = await runRunner([
    '--engine', engine,
    '--frontend-image', 'frontend:fixture', '--backend-image', 'backend:fixture', '--browser',
    '--private-report-dir', symlinkPath,
  ], { CADDY_TEST_STATE: statePath })
  assert.equal(symlinkPrivateReport.code, 1)
  assert.match(symlinkPrivateReport.stderr, /private report directory must be a new directory/)
  assert.deepEqual((await loadState(statePath)).calls, [], 'symlink private report directory started the container engine')
})

const reportWithoutBrowser = await runRunner([
  '--frontend-image', 'frontend:fixture', '--backend-image', 'backend:fixture', '--private-report-dir', '/tmp/private-report-without-browser',
], {})
assert.equal(reportWithoutBrowser.code, 1)
assert.match(reportWithoutBrowser.stderr, /private-report-dir requires --browser/)

console.log('Caddy smoke runner ownership, argument, timeout, redirect, loopback, failure, and signal checks passed')
