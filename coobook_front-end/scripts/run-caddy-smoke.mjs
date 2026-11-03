import { spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync } from 'node:fs'
import { chmod, mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

const labelKey = 'io.cookbook.caddy-smoke'
const requestTimeoutMs = 2500
const activeChildren = new Set()

function parseArgs(args) {
  const options = { engine: 'podman', timeoutMs: 90000, commandTimeoutMs: 15000 }
  const seen = new Set()
  const fields = {
    '--frontend-image': 'frontendImage', '--backend-image': 'backendImage', '--engine': 'engine',
    '--timeout-ms': 'timeoutMs', '--command-timeout-ms': 'commandTimeoutMs',
    '--browser-grep': 'browserGrep',
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
      else reject(new Error(`${engine} could not start: ${error.message}`, { cause: error }))
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
          if (redactFailureOutput) Object.assign(error, { stdout, stderr, durationMs: Date.now() - startedAt, timedOut })
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

function summarizePlaywright(output, durationMs, timedOut = false) {
  const maxInputLength = 128 * 1024
  const maxLineLength = 512
  const maxTests = 100
  const boundedOutput = output.slice(-maxInputLength)
  const tests = []
  let current
  let truncated = output.length > maxInputLength
  const saveCurrent = () => {
    if (current) tests.push(current)
    current = undefined
  }
  for (const line of boundedOutput.split(/\r?\n/)) {
    if (line.length > maxLineLength) {
      truncated = true
      continue
    }
    const match = line.match(/^(PASS|SKIP|FAIL) ([A-Za-z0-9][A-Za-z0-9 .,'():/_-]{0,159})$/)
    if (match) {
      saveCurrent()
      if (tests.length >= maxTests) {
        truncated = true
        continue
      }
      current = { ordinal: tests.length + 1, status: match[1].toLowerCase() }
      continue
    }
    if (current?.status === 'fail' && current.errorClass === undefined) {
      const errorClass = line.match(/^([A-Z][A-Za-z0-9]*Error|Error):/)?.[1]
      if (errorClass) current.errorClass = safeErrorClass(errorClass)
    }
    if (/^Playwright result:/.test(line)) saveCurrent()
  }
  saveCurrent()
  const counts = {
    passed: tests.filter(({ status }) => status === 'pass').length,
    failed: tests.filter(({ status }) => status === 'fail').length,
    skipped: tests.filter(({ status }) => status === 'skip').length,
  }
  const failures = tests.filter(({ status }) => status === 'fail')
  const errorClasses = boundedOutput.split(/\r?\n/)
    .filter((line) => line.length <= 256)
    .map((line) => line.match(/^([A-Z][A-Za-z0-9]*Error|Error):/)?.[1])
    .filter(Boolean)
  failures.forEach((failure, index) => {
    if (!failure.errorClass && errorClasses[index]) {
      failure.errorClass = safeErrorClass(errorClasses[index])
    }
  })
  const resultLine = boundedOutput.split(/\r?\n/).find((line) => line.length <= 64 && /^Playwright result: (passed|failed|timed out|interrupted)$/.test(line))
  const reportedResult = resultLine?.match(/^Playwright result: (passed|failed|timed out|interrupted)$/)?.[1]
  const result = timedOut ? 'timed out' : reportedResult ?? (counts.failed ? 'failed' : 'unknown')
  return { result, durationMs, ...counts, truncated, failures: failures.map(({ ordinal, status, errorClass = 'unknown' }) => ({ ordinal, status, errorClass: safeErrorClass(errorClass) })) }
}

function formatPlaywrightSummary(summary) {
  const parts = [`Playwright ${summary.result}: ${summary.passed} passed, ${summary.failed} failed, ${summary.skipped} skipped in ${summary.durationMs}ms`]
  for (const failure of summary.failures) parts.push(`failed test #${failure.ordinal} (${failure.errorClass})`)
  return parts.join('\n')
}

const usage = `Usage: node scripts/run-caddy-smoke.mjs --frontend-image IMAGE --backend-image IMAGE [--engine podman] [--browser] [--browser-grep PATTERN] [--timeout-ms 90000] [--command-timeout-ms 15000]\n\nUses existing images only. The default container engine is Podman.`
let cleanupPromise
let selectedEngine = 'podman'
let selectedCommandTimeoutMs = 15000
let networkName
let runLabel
let cleanupInProgress = false
let overallTimeout
let containerNames = []
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
    return { runDirectory, mailDirectory, emailCapturePath: join(mailDirectory, 'capture.json'), playwrightDist, playwrightOutput }
  } catch (error) {
    await rm(runDirectory, { recursive: true, force: true })
    runDirectory = undefined
    throw error
  }
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
  overallTimeout = setTimeout(() => { void halt(1, `Caddy smoke exceeded its ${options.timeoutMs}ms overall timeout`) }, options.timeoutMs)
  overallTimeout.unref()

  try {
    await runCommand(selectedEngine, ['network', 'create', '--label', label, networkName])

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
      '--tmpfs', '/data:rw,size=64m,mode=1777',
      '--env', 'COOKBOOK_ADDR=0.0.0.0:8000',
      '--env', 'COOKBOOK_DATABASE=/data/cookbook.db',
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
      const frontendRoot = resolve(import.meta.dirname, '..')
      const browserCache = resolve(frontendRoot, '..', '..', 'engineering-local/tools/playwright/browsers')
      const browsersPath = process.env.PLAYWRIGHT_BROWSERS_PATH || browserCache
      if (!existsSync(browsersPath)) throw new Error(`retained Playwright browser cache is missing: ${browsersPath}`)
      await runCommand(selectedEngine, ['cp', `${frontendName}:/srv/static-csp.json`, join(browserArtifacts.playwrightDist, 'static-csp.json')])
      const playwright = process.env.COOKBOOK_CADDY_SMOKE_PLAYWRIGHT_CLI || join(frontendRoot, 'node_modules/.bin/playwright')
      let playwrightResult
      try {
        playwrightResult = await runCommand(playwright, [
          'test', '--config=e2e/playwright.config.ts', '--workers=1',
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
            PLAYWRIGHT_BROWSERS_PATH: browsersPath,
            E2E_EMAIL_CAPTURE: browserArtifacts.emailCapturePath,
          },
        })
      } catch (error) {
        const output = `${error.stdout ?? ''}\n${error.stderr ?? ''}`
        const summary = summarizePlaywright(output, error.durationMs ?? 0, error.timedOut)
        await writeFile(join(browserArtifacts.playwrightOutput, 'failure-summary.json'), `${JSON.stringify(summary, null, 2)}\n`, { mode: 0o600 })
        throw new Error(formatPlaywrightSummary(summary))
      }
      const summary = summarizePlaywright(playwrightResult.stdout, playwrightResult.durationMs)
      await writeFile(join(browserArtifacts.playwrightOutput, 'result-summary.json'), `${JSON.stringify(summary, null, 2)}\n`, { mode: 0o600 })
      console.log(formatPlaywrightSummary(summary))
    }
    console.log(`Caddy/Go proxy smoke passed at ${origin}`)
  } finally {
    clearTimeout(overallTimeout)
    await cleanup()
  }
}

main().catch((error) => {
  console.error(error instanceof AggregateError ? `${error.message}\n${error.errors.map((cause) => cause.message).join('\n')}` : error.message)
  process.exitCode = 1
})
