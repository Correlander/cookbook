import { spawn } from 'node:child_process'
import { access, cp, lstat, mkdtemp, mkdir, readdir, rm, writeFile, chmod, stat } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { isAbsolute, join, resolve } from 'node:path'
import { createServer } from 'node:net'
import { existsSync } from 'node:fs'
import { X_OK } from 'node:constants'

const root = resolve(import.meta.dirname, '../..')
const frontend = join(root, 'coobook_front-end')
const privateSnapshotOptIn = process.env.COOKBOOK_E2E_PRIVATE_SNAPSHOT === '1'
// Set E2E_API_BINARY=/absolute/path/to/cookbook-api to use a trusted local build; its provenance belongs to the caller's proof record.
const suppliedApiBinary = process.env.E2E_API_BINARY
let apiBinary
if (suppliedApiBinary !== undefined) {
  if (!suppliedApiBinary || !isAbsolute(suppliedApiBinary)) {
    throw new Error('E2E_API_BINARY must be an absolute path to an executable API binary')
  }
  try {
    const binaryInfo = await stat(suppliedApiBinary)
    if (!binaryInfo.isFile()) throw new Error('not a regular file')
    await access(suppliedApiBinary, X_OK)
  } catch (error) {
    throw new Error(`E2E_API_BINARY is not an executable file: ${suppliedApiBinary}`, { cause: error })
  }
  apiBinary = suppliedApiBinary
}
const nodeVersion = process.versions.node.split('.').map(Number)
if (nodeVersion[0] < 22 || (nodeVersion[0] === 22 && nodeVersion[1] < 23) || (nodeVersion[0] === 22 && nodeVersion[1] === 23 && nodeVersion[2] < 4)) {
  throw new Error('E2E requires the frontend/CI Node version 22.23.4 or newer')
}

const runDir = await mkdtemp(join(tmpdir(), 'cookbook-e2e-'))
await chmod(runDir, 0o700)
const capture = join(runDir, 'mail.json')
const emailCommand = join(runDir, 'capture-email')
await writeFile(emailCommand, `#!/bin/sh\ncat > '${capture}'\nchmod 600 '${capture}'\n`, { mode: 0o700 })
const children = []
let buildScratch
let shutdownPromise

async function makeWritableTree(path) {
  const entries = await readdir(path, { withFileTypes: true }).catch(() => [])
  for (const entry of entries) {
    const child = join(path, entry.name)
    if (entry.isDirectory()) {
      await chmod(child, 0o700).catch(() => {})
      await makeWritableTree(child)
    } else if (entry.isFile()) {
      const mode = (await lstat(child)).mode & 0o777
      await chmod(child, mode | 0o200).catch(() => {})
    }
  }
}

async function removeBuildScratch() {
  if (!buildScratch) return
  await makeWritableTree(buildScratch)
  await chmod(buildScratch, 0o700).catch(() => {})
  await rm(buildScratch, { recursive: true, force: true })
  buildScratch = undefined
}

function start(command, args, options) {
  const child = spawn(command, args, { stdio: ['ignore', 'inherit', 'inherit'], detached: process.platform !== 'win32', ...options })
  child.spawnError = null
  child.on('error', (error) => { child.spawnError = error })
  children.push(child)
  return child
}
function waitForExit(child) {
  if (child.spawnError) return Promise.resolve({ error: child.spawnError })
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve({ code: child.exitCode, signal: child.signalCode })
  return new Promise((resolveExit) => {
    let settled = false
    const finish = (result) => {
      if (settled) return
      settled = true
      child.off('error', onError)
      child.off('exit', onExit)
      resolveExit(result)
    }
    const onError = (error) => finish({ error })
    const onExit = (code, signal) => finish({ code, signal })
    child.once('error', onError)
    child.once('exit', onExit)
  })
}
function stopAll() {
  if (shutdownPromise) return shutdownPromise
  shutdownPromise = (async () => {
    for (const child of children.reverse()) {
      if (child.spawnError || child.exitCode !== null) continue
      try { process.kill(process.platform === 'win32' ? child.pid : -child.pid, 'SIGTERM') } catch {}
      let timeout
      const exited = await Promise.race([
        waitForExit(child).then(() => true),
        new Promise((done) => { timeout = setTimeout(() => done(false), 5000) }),
      ])
      clearTimeout(timeout)
      if (!exited && child.exitCode === null && child.signalCode === null) {
        try { process.kill(process.platform === 'win32' ? child.pid : -child.pid, 'SIGKILL') } catch {}
        await waitForExit(child)
      }
    }
    try {
      await makeWritableTree(runDir)
      await chmod(runDir, 0o700).catch(() => {})
      await rm(runDir, { recursive: true, force: true })
    } finally {
      await removeBuildScratch()
    }
  })()
  return shutdownPromise
}
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => { void stopAll().finally(() => process.exit(signal === 'SIGINT' ? 130 : 143)) })
async function freePort() {
  const server = createServer()
  await new Promise((resolveListen, reject) => server.once('error', reject).listen(0, '127.0.0.1', resolveListen))
  const address = server.address()
  await new Promise((resolveClose) => server.close(resolveClose))
  return address.port
}
async function run(command, args, options = {}) {
  const child = start(command, args, { ...options, stdio: 'inherit' })
  const result = await waitForExit(child)
  if (result.error) throw new Error(`${command} could not start`, { cause: result.error })
  if (result.code !== 0) throw new Error(`${command} exited with ${result.code ?? `signal ${result.signal}`}`)
}

try {
  const apiPort = await freePort()
  const staticPort = await freePort()
  const baseURL = `http://localhost:${staticPort}`
  const env = {
    ...process.env,
    ASTRO_TELEMETRY_DISABLED: '1',
    COOKBOOK_ADDR: `127.0.0.1:${apiPort}`,
    COOKBOOK_DATABASE: join(runDir, 'cookbook.db'),
    COOKBOOK_MEDIA_DIR: join(runDir, 'media'),
    COOKBOOK_PUBLIC_URL: baseURL,
    COOKBOOK_RP_ORIGIN: baseURL,
    COOKBOOK_ADMIN_EMAIL: 'admin@example.test',
    COOKBOOK_ADMIN_NAME: 'E2E Admin',
    COOKBOOK_REGISTRATION: 'open',
    COOKBOOK_EMAIL_COMMAND: emailCommand,
    PLAYWRIGHT_BASE_URL: baseURL,
    PLAYWRIGHT_OUTPUT_DIR: join(runDir, 'playwright-output'),
    PLAYWRIGHT_DIST: join(runDir, 'dist'),
    E2E_EMAIL_CAPTURE: capture,
    ...(privateSnapshotOptIn ? { COOKBOOK_FEDERATION_ORIGIN: 'https://cookbook-e2e.example.org' } : {}),
  }
  const retainedBrowserCache = resolve(root, '..', 'engineering-local/tools/playwright/browsers')
  if (!env.PLAYWRIGHT_BROWSERS_PATH && existsSync(retainedBrowserCache)) env.PLAYWRIGHT_BROWSERS_PATH = retainedBrowserCache
  await mkdir(env.COOKBOOK_MEDIA_DIR, { recursive: true, mode: 0o700 })
  if (process.env.E2E_SKIP_BUILD !== '1') {
    buildScratch = await mkdtemp(join(frontend, '.cookbook-e2e-build-'))
    await chmod(buildScratch, 0o700)
    const [frontendInfo, scratchInfo] = await Promise.all([stat(frontend), stat(buildScratch)])
    if (scratchInfo.dev !== frontendInfo.dev) throw new Error('E2E build scratch must share the frontend filesystem')
    await run('npm', ['run', 'build'], { cwd: frontend, env: { ...env, TMPDIR: buildScratch } })
  }
  await cp(join(frontend, 'dist'), env.PLAYWRIGHT_DIST, { recursive: true, errorOnExist: true })
  if (apiBinary === undefined) {
    apiBinary = join(runDir, 'cookbook-api')
    await run('go', ['build', '-mod=mod', '-o', apiBinary, '.'], {
      cwd: join(root, 'cookbook_back-end'),
      env: {
        ...env,
        GOCACHE: process.env.GOCACHE ?? join(runDir, 'go-cache'),
        GOMODCACHE: process.env.GOMODCACHE ?? join(runDir, 'go-mod-cache'),
        GOPATH: process.env.GOPATH ?? join(runDir, 'go-path'),
      },
    })
  }
  const api = start(apiBinary, [], { cwd: runDir, env })
  const staticHost = start(process.execPath, [join(frontend, 'scripts/static-host.mjs')], {
    cwd: frontend,
    env: { ...env, STATIC_DIST: env.PLAYWRIGHT_DIST, STATIC_PORT: String(staticPort), API_ORIGIN: `http://127.0.0.1:${apiPort}` },
  })
  for (const url of [`http://127.0.0.1:${apiPort}/healthz`, `${baseURL}/healthz`]) {
    let ready = false
    for (let attempt = 0; attempt < 100; attempt++) {
      try { if ((await fetch(url)).ok) { ready = true; break } } catch {}
      await new Promise((done) => setTimeout(done, 100))
    }
    if (!ready) throw new Error(`service did not become ready: ${new URL(url).pathname}`)
  }
  await run('go', ['test', '-run', '^TestPrepareE2EAdminActorFixture$', '-count=1', '.'], {
    cwd: join(root, 'cookbook_back-end'),
    env: {
      ...env,
      COOKBOOK_E2E_ADMIN_ACTOR_FIXTURE: '1',
      COOKBOOK_E2E_RUN_DIR: runDir,
      GOCACHE: process.env.GOCACHE ?? join(runDir, 'go-cache'),
      GOMODCACHE: process.env.GOMODCACHE ?? join(runDir, 'go-mod-cache'),
      GOPATH: process.env.GOPATH ?? join(runDir, 'go-path'),
    },
  })
  if (privateSnapshotOptIn) {
    await run('go', ['test', '-run', '^TestPreparePrivateSnapshotBrowserFixture$', '-count=1', '.'], {
      cwd: join(root, 'cookbook_back-end'),
      env: {
        ...env,
        COOKBOOK_E2E_PRIVATE_SNAPSHOT_FIXTURE: '1',
        COOKBOOK_E2E_RUN_DIR: runDir,
        COOKBOOK_E2E_INBOX_URL: `http://127.0.0.1:${apiPort}/api/federation/inbox`,
        GOCACHE: process.env.GOCACHE ?? join(runDir, 'go-cache'),
        GOMODCACHE: process.env.GOMODCACHE ?? join(runDir, 'go-mod-cache'),
        GOPATH: process.env.GOPATH ?? join(runDir, 'go-path'),
      },
    })
  }
  await run(join(frontend, 'node_modules/.bin/playwright'), ['test', '--config=e2e/playwright.config.ts', ...process.argv.slice(2)], { cwd: frontend, env })
} finally {
  await stopAll()
}
