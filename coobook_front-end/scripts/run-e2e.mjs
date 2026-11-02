import { spawn } from 'node:child_process'
import { cp, lstat, mkdtemp, mkdir, readdir, rm, writeFile, chmod } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createServer } from 'node:net'
import { existsSync } from 'node:fs'

const root = resolve(import.meta.dirname, '../..')
const frontend = join(root, 'coobook_front-end')
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

function start(command, args, options) {
  const child = spawn(command, args, { stdio: ['ignore', 'inherit', 'inherit'], detached: process.platform !== 'win32', ...options })
  children.push(child)
  return child
}
function waitForExit(child) { return new Promise((resolveExit) => child.once('exit', resolveExit)) }
function stopAll() {
  if (shutdownPromise) return shutdownPromise
  shutdownPromise = (async () => {
    for (const child of children.reverse()) {
      if (child.exitCode !== null) continue
      try { process.kill(process.platform === 'win32' ? child.pid : -child.pid, 'SIGTERM') } catch {}
      await Promise.race([waitForExit(child), new Promise((done) => setTimeout(done, 5000))])
      if (child.exitCode === null) {
        try { process.kill(process.platform === 'win32' ? child.pid : -child.pid, 'SIGKILL') } catch {}
        await waitForExit(child)
      }
    }
    await makeWritableTree(runDir)
    await chmod(runDir, 0o700).catch(() => {})
    await rm(runDir, { recursive: true, force: true })
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
  const code = await new Promise((resolveExit) => child.once('exit', resolveExit))
  if (code !== 0) throw new Error(`${command} exited with ${code}`)
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
  }
  const retainedBrowserCache = resolve(root, '..', 'engineering-local/tools/playwright/browsers')
  if (!env.PLAYWRIGHT_BROWSERS_PATH && existsSync(retainedBrowserCache)) env.PLAYWRIGHT_BROWSERS_PATH = retainedBrowserCache
  await mkdir(env.COOKBOOK_MEDIA_DIR, { recursive: true, mode: 0o700 })
  if (process.env.E2E_SKIP_BUILD !== '1') await run('npm', ['run', 'build'], { cwd: frontend, env })
  await cp(join(frontend, 'dist'), env.PLAYWRIGHT_DIST, { recursive: true, errorOnExist: true })
  const apiBinary = join(runDir, 'cookbook-api')
  await run('go', ['build', '-mod=mod', '-o', apiBinary, '.'], {
    cwd: join(root, 'cookbook_back-end'),
    env: {
      ...env,
      GOCACHE: process.env.GOCACHE ?? join(runDir, 'go-cache'),
      GOMODCACHE: process.env.GOMODCACHE ?? join(runDir, 'go-mod-cache'),
      GOPATH: process.env.GOPATH ?? join(runDir, 'go-path'),
    },
  })
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
  await run(join(frontend, 'node_modules/.bin/playwright'), ['test', '--config=e2e/playwright.config.ts', ...process.argv.slice(2)], { cwd: frontend, env })
} finally {
  await stopAll()
}
