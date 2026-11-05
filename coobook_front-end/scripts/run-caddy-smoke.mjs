import { spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync } from 'node:fs'
import { chmod, mkdir, mkdtemp, open, readFile, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { isAbsolute, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const labelKey = 'io.cookbook.caddy-smoke'
const backendDatabasePath = '/data/cookbook.db'
const requestTimeoutMs = 2500
const activeChildren = new Set()

export function parseArgs(args) {
  const options = { engine: 'podman', timeoutMs: 90000, commandTimeoutMs: 15000 }
  const seen = new Set()
  const fields = {
    '--frontend-image': 'frontendImage', '--backend-image': 'backendImage', '--engine': 'engine',
    '--timeout-ms': 'timeoutMs', '--command-timeout-ms': 'commandTimeoutMs',
    '--browser-grep': 'browserGrep',
    '--private-report-dir': 'privateReportDir',
    '--browser': 'browser',
  }
  for (let index = 0; index < args.length; index++) {
    const flag = args[index]
    if (flag === '--help') return { help: true }
    if (!(flag in fields)) {
      throw new Error(`unknown argument: ${flag}`)
    }
    if (seen.has(flag)) throw new Error(`duplicate argument: ${flag}`)
    seen.add(flag)
    if (flag === '--browser') {
      options.browser = true
      if (!seen.has('--timeout-ms')) options.timeoutMs = 240000
      if (!seen.has('--command-timeout-ms')) options.commandTimeoutMs = 120000
      continue
    }
    const value = args[++index]
    if (!value || value.startsWith('--')) throw new Error(`${flag} requires a value`)
    if (flag === '--timeout-ms' || flag === '--command-timeout-ms') {
      const timeoutMs = Number(value)
      const optionName = flag.slice(2)
      if (!Number.isSafeInteger(timeoutMs) || timeoutMs < 250 || timeoutMs > 300000) {
        throw new Error(`${optionName} must be an integer from 250 to 300000`)
      }
      options[fields[flag]] = timeoutMs
    } else {
      if (flag === '--browser-grep' && (value.length > 200 || /[\r\n]/.test(value))) {
        throw new Error('--browser-grep must be a single-line pattern up to 200 characters')
      }
      options[fields[flag]] = value
    }
  }
  if (!options.frontendImage || !options.backendImage) {
    throw new Error('provide both --frontend-image and --backend-image')
  }
  if (options.browserGrep && !options.browser) {
    throw new Error('--browser-grep requires --browser')
  }
  if (options.privateReportDir && !options.browser) {
    throw new Error('--private-report-dir requires --browser')
  }
  return options
}

function signalChild(child, signal) {
  try {
    if (child.ownsProcessGroup) process.kill(-child.pid, signal)
    else child.kill(signal)
  } catch {}
}

async function closeProcessGroup(child) {
  if (!child.ownsProcessGroup) return
  signalChild(child, 'SIGTERM')
  await new Promise((resolve) => setTimeout(resolve, 100))
  signalChild(child, 'SIGKILL')
}

function runCommand(engine, args, { allowFailure = false, timeoutMs = selectedCommandTimeoutMs, cwd, env, detached = false, redactFailureOutput = false } = {}) {
  const startedAt = Date.now()
  let commandPromise
  commandPromise = new Promise((resolve, reject) => {
    const child = spawn(engine, args, { stdio: ['ignore', 'pipe', 'pipe'], cwd, env, detached })
    child.ownsProcessGroup = detached && process.platform !== 'win32'
    child.commandPromise = commandPromise
    activeChildren.add(child)
    let stdout = ''
    let stderr = ''
    let timedOut = false
    const append = (current, chunk) => (current + chunk.toString()).slice(-65536)
    child.stdout.setEncoding('utf8').on('data', (chunk) => { stdout = append(stdout, chunk) })
    child.stderr.setEncoding('utf8').on('data', (chunk) => { stderr = append(stderr, chunk) })
    const timer = setTimeout(() => {
      timedOut = true
      signalChild(child, 'SIGTERM')
      setTimeout(() => signalChild(child, 'SIGKILL'), 1000).unref()
    }, timeoutMs)
    child.once('error', (error) => {
      clearTimeout(timer)
      activeChildren.delete(child)
      if (allowFailure) resolve({ code: null, stdout, stderr: error.message })
      else reject(Object.assign(new Error(`${engine} could not start: ${error.message}`, { cause: error }), { exitCode: null, durationMs: Date.now() - startedAt, timedOut: false }))
    })
    child.once('close', (code, signal) => {
      clearTimeout(timer)
      void closeProcessGroup(child).finally(() => {
        activeChildren.delete(child)
        if (code === 0 || allowFailure) resolve({ code, stdout: stdout.trim(), stderr: stderr.trim(), durationMs: Date.now() - startedAt })
        else {
          const error = new Error(timedOut
          ? `${engine} ${args[0]} timed out after ${timeoutMs}ms`
          : redactFailureOutput
            ? `${engine} ${args[0]} failed (${code ?? signal}); output was suppressed because it may contain a sign-in link`
            : `${engine} ${args[0]} failed (${code ?? signal}): ${stderr.trim() || stdout.trim()}`)
          if (redactFailureOutput) Object.assign(error, { stdout, stderr, exitCode: code, durationMs: Date.now() - startedAt, timedOut })
          reject(error)
        }
      })
    })
  })
  return commandPromise
}

async function request(url, init = {}) {
  return fetch(url, { ...init, redirect: 'error', signal: AbortSignal.timeout(requestTimeoutMs) })
}

async function responseText(response, maxBytes, description) {
  const contentLength = Number(response.headers.get('content-length'))
  if (Number.isFinite(contentLength) && contentLength > maxBytes) {
    await response.body?.cancel()
    throw new Error(`${description} body exceeds ${maxBytes} bytes`)
  }
  if (!response.body) return ''
  const reader = response.body.getReader()
  const chunks = []
  let totalBytes = 0
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      totalBytes += value.byteLength
      if (totalBytes > maxBytes) {
        await reader.cancel()
        throw new Error(`${description} body exceeds ${maxBytes} bytes`)
      }
      chunks.push(value)
    }
  } finally {
    reader.releaseLock()
  }
  const body = new Uint8Array(totalBytes)
  let offset = 0
  for (const chunk of chunks) {
    body.set(chunk, offset)
    offset += chunk.byteLength
  }
  return new TextDecoder().decode(body)
}

async function waitForHealth(url) {
  const deadline = Date.now() + 30000
  let lastError
  while (Date.now() < deadline) {
    try {
      const response = await request(url)
      if (response.ok) return response
      lastError = new Error(`${url} returned ${response.status}`)
    } catch (error) {
      lastError = error
    }
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  throw new Error(`service did not become healthy at ${url}: ${lastError?.message ?? 'no response'}`)
}

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

async function checkSmoke(origin) {
  const health = await waitForHealth(`${origin}/healthz`)
  const healthType = health.headers.get('content-type') ?? ''
  assert(healthType.includes('application/json'), `health route returned ${healthType || 'no content type'}`)
  assert(!(await responseText(health, 16 * 1024, 'health response')).includes('<!doctype html'), 'health route returned HTML')

  const home = await request(`${origin}/`)
  const homeHtml = await responseText(home, 2 * 1024 * 1024, 'static root')
  const homeCsp = home.headers.get('content-security-policy') ?? ''
  assert(home.status === 200 && (home.headers.get('content-type') ?? '').includes('text/html'), 'static root did not return HTML')
  assert(home.headers.get('cache-control') === 'no-store', 'static HTML is not no-store')
  assert(homeCsp.includes("default-src 'self'") && homeCsp.includes("script-src 'self'"), 'static root CSP is missing expected restrictions')
  assert(!/unsafe-inline|unsafe-eval/.test(homeCsp), 'static root CSP allows unsafe inline execution')

  const confirmation = await request(`${origin}/auth/confirm?token=synthetic-invalid-token`)
  const confirmationHtml = await responseText(confirmation, 2 * 1024 * 1024, 'confirmation route')
  const confirmationCsp = confirmation.headers.get('content-security-policy') ?? ''
  assert(confirmation.status === 200 && (confirmation.headers.get('content-type') ?? '').includes('text/html'), 'confirmation route did not serve its static HTML')
  assert(confirmation.headers.get('cache-control') === 'no-store', 'confirmation HTML is not no-store')
  assert(confirmationCsp === homeCsp || (confirmationCsp.includes("default-src 'self'") && confirmationCsp.includes("script-src 'self'")), 'confirmation route CSP is missing')
  assert(!confirmationHtml.includes('synthetic-invalid-token'), 'confirmation query token leaked into static HTML')

  const creatorPages = [
    { route: '/creator', description: 'View a local creator profile on this Cookbook instance.' },
    { route: '/account/creator-profile', description: 'Manage your local creator profile on this Cookbook instance.' },
  ]
  for (const page of creatorPages) {
    let canonicalHtml
    let canonicalCsp
    for (const path of [page.route, `${page.route}.html`]) {
      const response = await request(`${origin}${path}?static-route-check=creator`)
      const html = await responseText(response, 2 * 1024 * 1024, `static page ${path}`)
      const csp = response.headers.get('content-security-policy') ?? ''
      assert(response.status === 200 && (response.headers.get('content-type') ?? '').includes('text/html'), `static page ${path} did not return HTML`)
      assert(response.headers.get('cache-control') === 'no-store', `static page ${path} is not no-store`)
      assert(response.headers.get('referrer-policy') === 'no-referrer', `static page ${path} is missing Referrer-Policy`)
      assert(csp.includes("default-src 'self'") && csp.includes("script-src 'self'"), `static page ${path} is missing its generated CSP`)
      assert(!/unsafe-inline|unsafe-eval/.test(csp), `static page ${path} CSP permits unsafe execution`)
      assert(html.includes('<title>Creator profile — Cookbook</title>'), `static page ${path} served the wrong document title`)
      assert(html.includes(page.description), `static page ${path} served the wrong creator document`)
      assert(!html.includes('static-route-check'), `static page ${path} leaked its request query into the document`)
      if (path === page.route) {
        canonicalHtml = html
        canonicalCsp = csp
      } else {
        assert(html === canonicalHtml, `static alias ${path} did not serve the canonical document`)
        assert(csp === canonicalCsp, `static alias ${path} did not use the canonical generated CSP`)
      }
    }
  }

  const assetPath = homeHtml.match(/(?:src|href)="(\/_astro\/[^"?#]+)"/)?.[1]
  assert(assetPath, 'static root did not reference a built Astro asset')
  const asset = await request(new URL(assetPath, origin))
  const assetCache = asset.headers.get('cache-control') ?? ''
  assert(asset.status === 200, `Astro asset returned ${asset.status}`)
  assert(assetCache.includes('max-age=31536000') && assetCache.includes('immutable'), `Astro asset cache policy is ${assetCache || 'missing'}`)
  await responseText(asset, 1024 * 1024, 'Astro asset')

  const body = JSON.stringify({ token: 'bogus' })
  const consume = await request(`${origin}/api/auth/consume`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', origin },
    body,
  })
  const consumeType = consume.headers.get('content-type') ?? ''
  const consumeBody = await responseText(consume, 32 * 1024, 'token validation response')
  assert(consume.status === 400, `matching Host/Origin reached status ${consume.status}, expected token validation 400`)
  assert(consumeType.includes('application/json') && consumeBody.includes('invalid_or_expired_token'), 'bogus token did not reach token validation')
  assert(consume.headers.get('cache-control') === 'no-store', 'auth token response is not no-store')
  const apiCsp = consume.headers.get('content-security-policy') ?? ''
  assert(apiCsp.includes("default-src 'self'"), 'Go API CSP did not pass through Caddy')

  const wrongOrigin = await request(`${origin}/api/auth/consume`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', origin: 'http://wrong-origin.invalid' },
    body,
  })
  const missingOrigin = await request(`${origin}/api/auth/consume`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body,
  })
  assert(wrongOrigin.status === 403, `wrong Origin returned ${wrongOrigin.status}, expected 403`)
  assert(missingOrigin.status === 403, `missing Origin returned ${missingOrigin.status}, expected 403`)
  await responseText(wrongOrigin, 32 * 1024, 'wrong-Origin response')
  await responseText(missingOrigin, 32 * 1024, 'missing-Origin response')

  const unknownApi = await request(`${origin}/api/no-such-route`)
  assert(!(unknownApi.headers.get('content-type') ?? '').includes('text/html'), 'unknown API path fell through to static HTML')
  assert(!(await responseText(unknownApi, 32 * 1024, 'unknown API response')).includes('<!doctype html'), 'unknown API path returned the frontend document')
}

function safeErrorClass(candidate) {
  return candidate === 'TimeoutError' || candidate === 'Error' ? candidate : 'unknown'
}

const safeFailureSources = new Map([
  ['e2e/recipe-publication.spec.ts', 'publication-spec'],
  ['e2e/creator-profile.spec.ts', 'creator-profile-spec'],
  ['e2e/static-host.spec.ts', 'static-host-spec'],
  ['e2e/auth.ts', 'auth-helper'],
])
const maxFailureLocation = 100_000
const maxPlaywrightReportBytes = 1024 * 1024

function safeFailureLocation(location, sourceRoot) {
  const file = typeof location?.file === 'string' ? location.file.replaceAll('\\', '/') : ''
  const resolvedFile = file ? resolve(sourceRoot, file) : ''
  const relativeFile = resolvedFile ? relative(sourceRoot, resolvedFile).replaceAll('\\', '/') : ''
  const source = relativeFile && relativeFile !== '..' && !relativeFile.startsWith('../') && !isAbsolute(relativeFile)
    ? safeFailureSources.get(relativeFile)
    : undefined
  const line = location?.line
  const column = location?.column
  if (!source || !Number.isSafeInteger(line) || !Number.isSafeInteger(column) || line < 1 || line > maxFailureLocation || column < 1 || column > maxFailureLocation) {
    return { source: 'unknown', line: null, column: null }
  }
  return { source, line, column }
}

const safeActionabilityPatterns = Object.freeze({
  pointer_interception: /\bintercepts? (?:pointer|mouse|touch) events\b/i,
  instability: /\b(?:element is not stable|not stable)\b/i,
  detachment: /\b(?:element (?:was )?detached from (?:the )?DOM|not attached to (?:the )?DOM|no longer attached)\b/i,
  invisibility: /\b(?:element is not visible|element is hidden|not visible)\b/i,
  disabled: /\b(?:element is not enabled|element is disabled|not enabled)\b/i,
  outside_viewport: /\b(?:element is outside of the viewport|outside of the viewport|outside viewport)\b/i,
})

const maxActionabilityMessageLength = 64 * 1024

const safePhaseIds = new Map([
  ['Email delivery', 'email_delivery'],
  ['Confirmation page', 'confirmation_page'],
  ['Session establishment', 'session_establishment'],
  ['Passkey registration', 'passkey_registration'],
  ['Recipe create and edit', 'recipe_create_and_edit'],
  ['Recipe history and stale update conflict', 'recipe_history_stale_update'],
  ['Logout closes the editor', 'logout_closes_editor'],
  ['Passkey sign-in', 'passkey_signin'],
  ['Logout and reject token replay', 'logout_replay'],
  ['Admin setup and public browsing setting', 'admin_setup_public_browsing'],
  ['Recipe owner sign-in and creation', 'owner_signin_create'],
  ['Confirm unpublished recipe stays anonymous-hidden', 'unpublished_hidden'],
  ['Open owner recipe dialog', 'open_owner_recipe_dialog'],
  ['Open owner publication settings', 'open_owner_publication_settings'],
  ['Verify owner publication GET and strong ETag', 'verify_owner_publication_get'],
  ['Confirm publication controls are ready', 'confirm_publication_controls_ready'],
  ['Save publication and verify owner access', 'save_publication_owner_access'],
  ['Verify anonymous published list and detail', 'verify_anonymous_published_detail'],
  ['Withdraw and preserve owner access', 'withdraw_owner_access'],
])

function summarizeActionability(result) {
  const flags = Object.fromEntries(Object.keys(safeActionabilityPatterns).map((name) => [name, false]))
  let phase = 'unknown'
  let visited = 0
  const inspectMessage = (message) => {
    if (typeof message !== 'string') return
    const bounded = message.slice(-maxActionabilityMessageLength)
    for (const [name, pattern] of Object.entries(safeActionabilityPatterns)) {
      if (pattern.test(bounded)) flags[name] = true
    }
  }
  const inspectError = (error) => {
    if (error && typeof error === 'object' && !Array.isArray(error)) inspectMessage(error.message)
  }

  if (Array.isArray(result?.errors)) for (const error of result.errors) inspectError(error)
  inspectError(result?.error)
  const pending = []
  if (Array.isArray(result?.steps)) for (let i = result.steps.length - 1; i >= 0; i -= 1) pending.push({ step: result.steps[i], phase: undefined })
  while (pending.length > 0 && visited < 10_000) {
    const item = pending.pop()
    visited += 1
    const entry = item?.step
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) continue
    const stepPhase = typeof entry.title === 'string' ? safePhaseIds.get(entry.title) : undefined
    const currentPhase = stepPhase ?? item.phase
    if (entry.error && typeof entry.error === 'object' && currentPhase) phase = currentPhase
    inspectError(entry.error)
    if (Array.isArray(entry.steps)) {
      for (let i = entry.steps.length - 1; i >= 0; i -= 1) pending.push({ step: entry.steps[i], phase: currentPhase })
    }
  }
  return { phase, flags }
}

function deriveTestOutcome(expectedStatus, results) {
  let skipped = 0
  let expected = 0
  let unexpected = 0
  for (const result of results) {
    if (result.status === 'interrupted') continue
    if (result.status === 'skipped' && expectedStatus === 'skipped') skipped += 1
    else if (result.status === 'skipped') continue
    else if (result.status === expectedStatus) expected += 1
    else unexpected += 1
  }
  if (expected === 0 && unexpected === 0) return 'skipped'
  if (unexpected === 0) return 'expected'
  if (expected === 0 && skipped === 0) return 'unexpected'
  return 'flaky'
}

function summarizePlaywrightReport(report, durationMs, childExitCode, timedOut, sourceRoot) {
  const maxTests = 100
  const validTestStatuses = new Set(['expected', 'unexpected', 'flaky', 'skipped'])
  const validResultStatuses = new Set(['passed', 'failed', 'skipped', 'timedOut', 'interrupted'])
  const validExpectedStatuses = new Set(['passed', 'failed', 'skipped', 'timedOut', 'interrupted'])
  const tests = []
  let truncated = false
  let invalidReport = !report || typeof report !== 'object' || !Array.isArray(report.suites)
  const counts = { expected: 0, unexpected: 0, flaky: 0, skipped: 0 }
  let executedResults = 0
  const visitSuites = (suites) => {
    if (!Array.isArray(suites)) {
      invalidReport = true
      return
    }
    for (const suite of suites) {
      if (!suite || typeof suite !== 'object' || !Array.isArray(suite.specs) || (suite.suites !== undefined && !Array.isArray(suite.suites))) {
        invalidReport = true
        continue
      }
      for (const spec of suite.specs) {
        if (!spec || typeof spec !== 'object' || !Array.isArray(spec.tests)) {
          invalidReport = true
          continue
        }
        for (const test of spec.tests) {
          if (!test || typeof test !== 'object' || !validTestStatuses.has(test.status) || !validExpectedStatuses.has(test.expectedStatus) || !Array.isArray(test.results) || test.results.length === 0) {
            invalidReport = true
            continue
          }
          if (tests.length >= maxTests) {
            truncated = true
            return
          }
          tests.push(test)
          counts[test.status] += 1
          for (const result of test.results) {
            if (!result || typeof result !== 'object' || !validResultStatuses.has(result.status)) invalidReport = true
            else if (result.status === 'passed' || result.status === 'failed' || result.status === 'timedOut') executedResults += 1
          }
          if (test.results.every((result) => result && validResultStatuses.has(result.status)) && deriveTestOutcome(test.expectedStatus, test.results) !== test.status) invalidReport = true
        }
      }
      visitSuites(suite.suites ?? [])
      if (truncated) return
    }
  }
  visitSuites(report?.suites)
  const reportStats = report?.stats
  for (const status of Object.keys(counts)) {
    if (!Number.isSafeInteger(reportStats?.[status]) || reportStats[status] < 0 || reportStats[status] !== counts[status]) invalidReport = true
  }
  if (tests.length === 0 || executedResults === 0 || truncated) invalidReport = true
  const passed = tests.filter((test) => test?.status === 'expected' || test?.status === 'flaky').length
  const skipped = tests.filter((test) => test?.status === 'skipped').length
  const failures = tests.flatMap((test, index) => {
    if (test?.status !== 'unexpected') return []
    const results = Array.isArray(test.tests) ? test.tests.flatMap((entry) => entry?.results ?? []) : (Array.isArray(test.results) ? test.results : [])
    const failedResult = [...results].reverse().find((entry) => entry?.status === 'failed' || entry?.status === 'timedOut' || entry?.status === 'interrupted')
    const errorMessage = failedResult?.errors?.[0]?.message ?? failedResult?.error?.message
    const safeClass = typeof errorMessage === 'string' ? errorMessage.match(/^(TimeoutError|Error):/)?.[1] : undefined
    const actionability = summarizeActionability(failedResult)
    return [{
      ordinal: index + 1,
      errorClass: safeErrorClass(safeClass),
      ...safeFailureLocation(failedResult?.errorLocation, sourceRoot),
      phase: actionability.phase,
      actionability: actionability.flags,
    }]
  })
  const failed = failures.length
  const exitMismatch = timedOut ? false : childExitCode === 0 ? failed > 0 : Number.isInteger(childExitCode) ? failed === 0 : true
  const result = timedOut ? 'timed out' : invalidReport ? 'unknown' : childExitCode === 0 && failed === 0 ? 'passed' : 'failed'
  return { result, durationMs, passed, failed, skipped, truncated, invalidReport, exitMismatch, failures }
}

async function readPlaywrightReport(path) {
  const info = await stat(path)
  if (info.size > maxPlaywrightReportBytes) throw new Error('Playwright structured report exceeded its private size limit')
  return JSON.parse(await readFile(path, 'utf8'))
}

async function preparePrivateReportDirectory(path) {
  const directory = resolve(path)
  try {
    await mkdir(directory, { mode: 0o700 })
    await chmod(directory, 0o700)
  } catch {
    throw new Error('private report directory must be a new directory with an existing parent')
  }
  return directory
}

async function capturePrivatePlaywrightReport(sourcePath, destinationDirectory) {
  if (!destinationDirectory) return 'disabled'
  let file
  try {
    file = await open(sourcePath, 'r')
    const bounded = Buffer.allocUnsafe(maxPlaywrightReportBytes + 1)
    let length = 0
    while (length < bounded.length) {
      const { bytesRead } = await file.read(bounded, length, bounded.length - length, null)
      if (bytesRead === 0) break
      length += bytesRead
    }
    if (length > maxPlaywrightReportBytes) return 'oversized'
    const contents = bounded.subarray(0, length)
    JSON.parse(contents.toString('utf8'))
    const destination = join(destinationDirectory, 'playwright-report.json')
    await writeFile(destination, contents, { flag: 'wx', mode: 0o600 })
    await chmod(destination, 0o600)
    return 'captured'
  } catch (error) {
    if (error?.code === 'ENOENT') return 'missing'
    if (error instanceof SyntaxError) return 'malformed'
    return 'unavailable'
  } finally {
    await file?.close().catch(() => {})
  }
}

async function summarizePlaywrightReportFile(path, durationMs, childExitCode, timedOut, sourceRoot) {
  try {
    return summarizePlaywrightReport(await readPlaywrightReport(path), durationMs, childExitCode, timedOut, sourceRoot)
  } catch {
    return {
      result: timedOut ? 'timed out' : childExitCode === 0 ? 'unknown' : 'failed',
      durationMs,
      passed: 0,
      failed: 0,
      skipped: 0,
      truncated: true,
      reportUnavailable: true,
      invalidReport: true,
      exitMismatch: !timedOut && childExitCode === 0,
      failures: [],
    }
  }
}

function formatPlaywrightSummary(summary) {
  const parts = [`Playwright ${summary.result}: ${summary.passed} passed, ${summary.failed} failed, ${summary.skipped} skipped in ${summary.durationMs}ms`]
  for (const failure of summary.failures) {
    const actionability = Object.entries(failure.actionability ?? {}).map(([name, value]) => `${name}=${value}`).join(',')
    parts.push(`failed test #${failure.ordinal} (${failure.errorClass}; source=${failure.source}; line=${failure.line ?? 'unknown'}; column=${failure.column ?? 'unknown'}) phase=${failure.phase ?? 'unknown'} actionability=${actionability || 'unavailable'}`)
  }
  if (summary.reportUnavailable) parts.push('structured Playwright report unavailable')
  if (summary.invalidReport && !summary.reportUnavailable) parts.push('structured Playwright report rejected')
  if (summary.exitMismatch) parts.push('Playwright process/report status mismatch')
  if (summary.truncated && !summary.reportUnavailable) parts.push('structured Playwright report exceeded the test summary limit')
  return parts.join('\n')
}

const usage = `Usage: node scripts/run-caddy-smoke.mjs --frontend-image IMAGE --backend-image IMAGE [--engine podman] [--browser] [--browser-grep PATTERN] [--private-report-dir NEW_DIR] [--timeout-ms MS] [--command-timeout-ms MS]\n\nUses existing images only. The default container engine is Podman. HTTP-only defaults: 90000ms overall and 15000ms per command. Browser defaults: 240000ms overall and 120000ms per command.`
let cleanupPromise
let selectedEngine = 'podman'
let selectedCommandTimeoutMs = 15000
let networkName
let runLabel
let cleanupInProgress = false
let overallTimeout
let containerNames = []
let databaseVolumeName
let runDirectory

async function cleanup() {
  if (cleanupPromise) return cleanupPromise
  if (!runLabel) {
    if (runDirectory) {
      await rm(runDirectory, { recursive: true, force: true })
      runDirectory = undefined
    }
    return
  }
  cleanupPromise = (async () => {
    cleanupInProgress = true
    try {
      const failures = []
      const ownedContainers = await runCommand(selectedEngine, [
        'ps', '--all', '--filter', `label=${runLabel}`, '--format', '{{.ID}} {{.Names}}',
      ], { allowFailure: true })
      if (ownedContainers.code !== 0) {
        failures.push(`${selectedEngine} ps: ${ownedContainers.stderr || ownedContainers.stdout || `exit ${ownedContainers.code}`}`)
      } else {
        for (const line of ownedContainers.stdout.split(/\r?\n/)) {
          const [id, name] = line.trim().split(/\s+/, 2)
          if (!id) continue
          if (!containerNames.includes(name)) {
            failures.push(`${selectedEngine} ps found unexpected container ${name || '(unnamed)'} with this run's ownership label; left untouched`)
            continue
          }
          const result = await runCommand(selectedEngine, ['rm', '--force', id], { allowFailure: true })
          if (result.code !== 0) failures.push(`${selectedEngine} rm ${name}: ${result.stderr || result.stdout || `exit ${result.code}`}`)
        }
      }
      const ownedNetworks = await runCommand(selectedEngine, [
        'network', 'ls', '--filter', `label=${runLabel}`, '--format', '{{.ID}} {{.Name}}',
      ], { allowFailure: true })
      if (ownedNetworks.code !== 0) {
        failures.push(`${selectedEngine} network ls: ${ownedNetworks.stderr || ownedNetworks.stdout || `exit ${ownedNetworks.code}`}`)
      } else {
        for (const line of ownedNetworks.stdout.split(/\r?\n/)) {
          const [id, name] = line.trim().split(/\s+/, 2)
          if (!id) continue
          if (name !== networkName) {
            failures.push(`${selectedEngine} network ls found unexpected network ${name || '(unnamed)'} with this run's ownership label; left untouched`)
            continue
          }
          const result = await runCommand(selectedEngine, ['network', 'rm', id], { allowFailure: true })
          if (result.code !== 0) failures.push(`${selectedEngine} network rm ${name}: ${result.stderr || result.stdout || `exit ${result.code}`}`)
        }
      }

      const remainingContainers = await runCommand(selectedEngine, [
        'ps', '--all', '--filter', `label=${runLabel}`, '--format', '{{.ID}} {{.Names}}',
      ], { allowFailure: true })
      if (remainingContainers.code !== 0) {
        failures.push(`${selectedEngine} ps verification: ${remainingContainers.stderr || remainingContainers.stdout || `exit ${remainingContainers.code}`}`)
      } else if (remainingContainers.stdout.trim()) {
        failures.push(`${selectedEngine} ps verification found leftover run-labeled containers: ${remainingContainers.stdout.trim()}`)
      }
      const remainingNetworks = await runCommand(selectedEngine, [
        'network', 'ls', '--filter', `label=${runLabel}`, '--format', '{{.ID}} {{.Name}}',
      ], { allowFailure: true })
      if (remainingNetworks.code !== 0) {
        failures.push(`${selectedEngine} network ls verification: ${remainingNetworks.stderr || remainingNetworks.stdout || `exit ${remainingNetworks.code}`}`)
      } else if (remainingNetworks.stdout.trim()) {
        failures.push(`${selectedEngine} network ls verification found leftover run-labeled networks: ${remainingNetworks.stdout.trim()}`)
      }
      const ownedVolumes = await runCommand(selectedEngine, [
        'volume', 'ls', '--filter', `label=${runLabel}`, '--format', '{{.Name}}',
      ], { allowFailure: true })
      if (ownedVolumes.code !== 0) {
        failures.push(`${selectedEngine} volume ls: ${ownedVolumes.stderr || ownedVolumes.stdout || `exit ${ownedVolumes.code}`}`)
      } else {
        for (const name of ownedVolumes.stdout.split(/\r?\n/).map((entry) => entry.trim()).filter(Boolean)) {
          if (name !== databaseVolumeName) {
            failures.push(`${selectedEngine} volume ls found unexpected volume ${name} with this run's ownership label; left untouched`)
            continue
          }
          const result = await runCommand(selectedEngine, ['volume', 'rm', name], { allowFailure: true })
          if (result.code !== 0) failures.push(`${selectedEngine} volume rm ${name}: ${result.stderr || result.stdout || `exit ${result.code}`}`)
        }
      }

      const remainingVolumes = await runCommand(selectedEngine, [
        'volume', 'ls', '--filter', `label=${runLabel}`, '--format', '{{.Name}}',
      ], { allowFailure: true })
      if (remainingVolumes.code !== 0) {
        failures.push(`${selectedEngine} volume ls verification: ${remainingVolumes.stderr || remainingVolumes.stdout || `exit ${remainingVolumes.code}`}`)
      } else if (remainingVolumes.stdout.trim()) {
        failures.push(`${selectedEngine} volume ls verification found leftover run-labeled volumes: ${remainingVolumes.stdout.trim()}`)
      }
      if (runDirectory) {
        try {
          await rm(runDirectory, { recursive: true, force: true })
          runDirectory = undefined
        } catch (error) {
          failures.push(`remove private browser test directory: ${error.message}`)
        }
      }
      if (failures.length) throw new AggregateError(failures.map((message) => new Error(message)), 'failed to remove all owned Caddy smoke resources')
    } finally {
      cleanupInProgress = false
    }
  })()
  return cleanupPromise
}

async function prepareBrowserArtifacts() {
  runDirectory = await mkdtemp(join(tmpdir(), 'cookbook-caddy-browser-'))
  try {
    await chmod(runDirectory, 0o700)
    const mailDirectory = join(runDirectory, 'mail')
    await mkdir(mailDirectory, { mode: 0o777 })
    await chmod(mailDirectory, 0o777)
    const emailCommand = join(mailDirectory, 'capture-email')
    await writeFile(emailCommand, '#!/bin/sh\ncat > /smoke-mail/capture.json\nchmod 644 /smoke-mail/capture.json\n', { mode: 0o755 })
    await chmod(emailCommand, 0o755)
    const playwrightDist = join(runDirectory, 'playwright-dist')
    const playwrightOutput = join(runDirectory, 'playwright-output')
    await Promise.all([
      mkdir(playwrightDist, { mode: 0o700 }),
      mkdir(playwrightOutput, { mode: 0o700 }),
    ])
    return {
      runDirectory,
      mailDirectory,
      emailCapturePath: join(mailDirectory, 'capture.json'),
      playwrightDist,
      playwrightOutput,
      playwrightReport: join(playwrightOutput, 'playwright-report.json'),
    }
  } catch (error) {
    await rm(runDirectory, { recursive: true, force: true })
    runDirectory = undefined
    throw error
  }
}

async function seedCaddyBrowserAdmins(backendName, browserArtifacts) {
  const fixtureDirectory = join(browserArtifacts.runDirectory, 'cookbook-e2e-caddy-fixture')
  const fixtureDatabase = join(fixtureDirectory, 'cookbook.db')
  await mkdir(fixtureDirectory, { mode: 0o700 })
  await chmod(fixtureDirectory, 0o700)

  // SQLite must be cold while the fixture mutates it. Browser mode mounts an
  // owned volume at the image's /data path so the canonical database survives
  // stop; copy it out only after the backend has exited, then copy it back.
  await runCommand(selectedEngine, ['stop', backendName])
  await runCommand(selectedEngine, ['cp', `${backendName}:${backendDatabasePath}`, fixtureDatabase])
  await chmod(fixtureDatabase, 0o600)
  const backendRoot = resolve(import.meta.dirname, '../../cookbook_back-end')
  await runCommand('go', ['test', '-run', '^TestPrepareE2EAdminActorFixture$', '-count=1', '.'], {
    cwd: backendRoot,
    env: {
      ...process.env,
      COOKBOOK_E2E_ADMIN_ACTOR_FIXTURE: '1',
      COOKBOOK_E2E_RUN_DIR: fixtureDirectory,
      COOKBOOK_DATABASE: fixtureDatabase,
    },
  })
  // Engine copy into the volume is root-owned. Keep the host copy protected
  // by its 0700 parent, and make the database writable to the cookbook UID.
  await chmod(fixtureDatabase, 0o666)
  await runCommand(selectedEngine, ['cp', fixtureDatabase, `${backendName}:${backendDatabasePath}`])
  await runCommand(selectedEngine, ['start', backendName])
}

let interrupted = false
async function halt(exitCode, message) {
  if (interrupted) return
  interrupted = true
  if (message) console.error(message)
  const pending = [...activeChildren].map((child) => child.commandPromise?.catch(() => {}) ?? Promise.resolve())
  if (!cleanupInProgress) for (const child of activeChildren) signalChild(child, 'SIGTERM')
  try {
    await Promise.all(pending)
    await cleanup()
  } catch (error) {
    console.error(error.message)
  }
  process.exit(exitCode)
}

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => { void halt(signal === 'SIGINT' ? 130 : 143) })
}

async function main() {
  const options = parseArgs(process.argv.slice(2))
  if (options.help) {
    console.log(usage)
    return
  }
  selectedEngine = options.engine
  selectedCommandTimeoutMs = options.commandTimeoutMs
  const privateReportDirectory = options.privateReportDir
    ? await preparePrivateReportDirectory(options.privateReportDir)
    : undefined
  let browserArtifacts
  if (options.browser) {
    const nodeVersion = process.versions.node.split('.').map(Number)
    if (nodeVersion[0] < 22 || (nodeVersion[0] === 22 && nodeVersion[1] < 23) || (nodeVersion[0] === 22 && nodeVersion[1] === 23 && nodeVersion[2] < 4)) {
      throw new Error('browser mode requires Node 22.23.4 or newer')
    }
    browserArtifacts = await prepareBrowserArtifacts()
  }
  const runId = randomUUID().replaceAll('-', '')
  runLabel = `${labelKey}=${runId}`
  const label = runLabel
  networkName = `cookbook-caddy-smoke-${runId}`
  const frontendName = `${networkName}-frontend`
  const backendName = `${networkName}-backend`
  containerNames = [frontendName, backendName]
  if (browserArtifacts) databaseVolumeName = `cookbook-caddy-${runId}-data`
  overallTimeout = setTimeout(() => { void halt(1, `Caddy smoke exceeded its ${options.timeoutMs}ms overall timeout`) }, options.timeoutMs)
  overallTimeout.unref()

  try {
    await runCommand(selectedEngine, ['network', 'create', '--label', label, networkName])
    if (databaseVolumeName) {
      // tmpfs contents vanish when the stopped container exits; the browser
      // fixture needs an owned volume to preserve its cold database handoff.
      await runCommand(selectedEngine, ['volume', 'create', '--label', label, databaseVolumeName])
      const createdDatabaseVolume = await runCommand(selectedEngine, [
        'volume', 'ls', '--filter', `label=${runLabel}`, '--format', '{{.Name}}',
      ])
      assert(createdDatabaseVolume.stdout === databaseVolumeName, 'database volume must be newly owned by this Caddy run')
    }

    await runCommand(selectedEngine, [
      'run', '--detach', '--pull=never', '--name', frontendName,
      '--label', label, '--network', networkName,
      '--publish', '127.0.0.1::8080/tcp', options.frontendImage,
    ])
    const portResult = await runCommand(selectedEngine, ['port', frontendName, '8080/tcp'])
    const mappedPort = portResult.stdout.trim().match(/^127\.0\.0\.1:([0-9]+)$/)?.[1]
    assert(mappedPort, `container engine did not publish frontend strictly on loopback: ${portResult.stdout}`)
    const origin = `http://localhost:${mappedPort}`

    await runCommand(selectedEngine, [
      'run', '--detach', '--pull=never', '--name', backendName,
      '--label', label, '--network', networkName, '--network-alias', 'cookbook',
      ...(browserArtifacts ? ['--mount', `type=volume,src=${databaseVolumeName},dst=/data`] : ['--tmpfs', '/data:rw,size=64m,mode=1777']),
      '--env', 'COOKBOOK_ADDR=0.0.0.0:8000',
      '--env', `COOKBOOK_DATABASE=${backendDatabasePath}`,
      '--env', 'COOKBOOK_MEDIA_DIR=/data/media',
      '--env', `COOKBOOK_PUBLIC_URL=${origin}`,
      '--env', `COOKBOOK_RP_ORIGIN=${origin}`,
      '--env', options.browser ? 'COOKBOOK_EMAIL_COMMAND=/smoke-mail/capture-email' : 'COOKBOOK_EMAIL_COMMAND=/bin/true',
      ...(browserArtifacts ? [
        '--env', 'COOKBOOK_REGISTRATION=open',
        '--env', 'COOKBOOK_ADMIN_EMAIL=admin@example.test',
        '--env', 'COOKBOOK_ADMIN_NAME=E2E Admin',
        '--volume', `${browserArtifacts.mailDirectory}:/smoke-mail:rw,Z`,
      ] : []),
      options.backendImage,
    ])

    await checkSmoke(origin)
    if (browserArtifacts) {
      await seedCaddyBrowserAdmins(backendName, browserArtifacts)
      await checkSmoke(origin)
      const frontendRoot = resolve(import.meta.dirname, '..')
      const browserCache = resolve(frontendRoot, '..', '..', 'engineering-local/tools/playwright/browsers')
      const browsersPath = process.env.PLAYWRIGHT_BROWSERS_PATH || browserCache
      if (!existsSync(browsersPath)) throw new Error(`retained Playwright browser cache is missing: ${browsersPath}`)
      await runCommand(selectedEngine, ['cp', `${frontendName}:/srv/static-csp.json`, join(browserArtifacts.playwrightDist, 'static-csp.json')])
      const playwright = process.env.COOKBOOK_CADDY_SMOKE_PLAYWRIGHT_CLI || join(frontendRoot, 'node_modules/.bin/playwright')
      let playwrightResult
      try {
        playwrightResult = await runCommand(playwright, [
          'test', '--config=e2e/playwright.config.ts', '--workers=1', '--reporter=json',
          ...(options.browserGrep ? [`--grep=${options.browserGrep}`] : []),
        ], {
          cwd: frontendRoot,
          detached: true,
          redactFailureOutput: true,
          env: {
            ...process.env,
            PLAYWRIGHT_BASE_URL: origin,
            PLAYWRIGHT_DIST: browserArtifacts.playwrightDist,
            PLAYWRIGHT_OUTPUT_DIR: browserArtifacts.playwrightOutput,
            PLAYWRIGHT_JSON_OUTPUT_FILE: browserArtifacts.playwrightReport,
            PLAYWRIGHT_BROWSERS_PATH: browsersPath,
            E2E_EMAIL_CAPTURE: browserArtifacts.emailCapturePath,
          },
        })
      } catch (error) {
        const capture = await capturePrivatePlaywrightReport(browserArtifacts.playwrightReport, privateReportDirectory)
        if (capture !== 'disabled') console.log(`Private Playwright report: ${capture}`)
        const summary = await summarizePlaywrightReportFile(
          browserArtifacts.playwrightReport,
          error.durationMs ?? 0,
          error.exitCode,
          Boolean(error.timedOut),
          frontendRoot,
        )
        await writeFile(join(browserArtifacts.playwrightOutput, 'failure-summary.json'), `${JSON.stringify(summary, null, 2)}\n`, { mode: 0o600 })
        throw new Error(formatPlaywrightSummary(summary))
      }
      const capture = await capturePrivatePlaywrightReport(browserArtifacts.playwrightReport, privateReportDirectory)
      if (capture !== 'disabled') console.log(`Private Playwright report: ${capture}`)
      const summary = await summarizePlaywrightReportFile(browserArtifacts.playwrightReport, playwrightResult.durationMs, playwrightResult.code, false, frontendRoot)
      await writeFile(join(browserArtifacts.playwrightOutput, 'result-summary.json'), `${JSON.stringify(summary, null, 2)}\n`, { mode: 0o600 })
      if (summary.reportUnavailable || summary.invalidReport || summary.truncated || summary.exitMismatch || summary.result !== 'passed') throw new Error(formatPlaywrightSummary(summary))
      console.log(formatPlaywrightSummary(summary))
    }
    console.log(`Caddy/Go proxy smoke passed at ${origin}`)
  } finally {
    clearTimeout(overallTimeout)
    await cleanup()
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error instanceof AggregateError ? `${error.message}\n${error.errors.map((cause) => cause.message).join('\n')}` : error.message)
    process.exitCode = 1
  })
}
