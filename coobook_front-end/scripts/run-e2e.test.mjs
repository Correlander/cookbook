import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { chmod, mkdtemp, mkdir, readFile, readFile as readFileAsync, readdir, rm, stat, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { basename, join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const runner = fileURLToPath(new URL('./run-e2e.mjs', import.meta.url))
const frontend = fileURLToPath(new URL('..', import.meta.url))
const repository = fileURLToPath(new URL('../..', import.meta.url))
const scratchPrefix = '.cookbook-e2e-build-'

async function makeFixture(t, { mode = 'fail' } = {}) {
  const testRoot = await mkdtemp(join(tmpdir(), 'cookbook-run-e2e-test-'))
  const runnerTemp = join(testRoot, 'runner-temp')
  const fakeBin = join(testRoot, 'fake-bin')
  const capture = join(testRoot, 'build-capture.json')
  const callerTmp = join(testRoot, 'caller-tmp')
  const callerTemp = join(testRoot, 'caller-temp')
  await Promise.all([mkdir(runnerTemp), mkdir(fakeBin), mkdir(callerTmp), mkdir(callerTemp)])
  const fakeNpm = join(fakeBin, 'npm')
  const script = `#!${process.execPath}
import { writeFileSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
const scratch = process.env.TMPDIR
const captured = {
  argv: process.argv.slice(2),
  tmpdir: scratch,
  tmp: process.env.TMP,
  temp: process.env.TEMP,
  sentinel: process.env.E2E_BUILD_TEST_SENTINEL,
  scratchMode: statSync(scratch).mode & 0o777,
  scratchDev: statSync(scratch).dev,
  frontendDev: statSync(process.env.E2E_BUILD_TEST_FRONTEND).dev,
  runDir: dirname(process.env.COOKBOOK_DATABASE),
  runDirMode: statSync(dirname(process.env.COOKBOOK_DATABASE)).mode & 0o777,
  database: process.env.COOKBOOK_DATABASE,
  media: process.env.COOKBOOK_MEDIA_DIR,
  emailCommand: process.env.COOKBOOK_EMAIL_COMMAND,
  emailCapture: process.env.E2E_EMAIL_CAPTURE,
  playwrightOutput: process.env.PLAYWRIGHT_OUTPUT_DIR,
  playwrightDist: process.env.PLAYWRIGHT_DIST,
}
writeFileSync(process.env.E2E_BUILD_TEST_CAPTURE, JSON.stringify(captured))
if (process.env.E2E_BUILD_TEST_MODE === 'signal') {
  process.on('SIGTERM', () => setTimeout(() => process.exit(0), 10))
  setInterval(() => {}, 1000)
} else if (process.env.E2E_BUILD_TEST_MODE === 'fail') {
  process.exit(17)
}
`
  await writeFile(fakeNpm, script, { mode: 0o700 })
  await chmod(fakeNpm, 0o700)
  t.after(async () => {
    const captureText = await readFile(capture, 'utf8').catch(() => '')
    if (captureText) {
      const { tmpdir: ownedScratch } = JSON.parse(captureText)
      if (ownedScratch.startsWith(join(frontend, scratchPrefix))) await rm(ownedScratch, { recursive: true, force: true })
    }
    await rm(testRoot, { recursive: true, force: true })
  })
  const env = {
    ...process.env,
    PATH: fakeBin,
    TMPDIR: runnerTemp,
    TMP: callerTmp,
    TEMP: callerTemp,
    E2E_SKIP_BUILD: '0',
    E2E_BUILD_TEST_CAPTURE: capture,
    E2E_BUILD_TEST_FRONTEND: frontend,
    E2E_BUILD_TEST_MODE: mode,
    E2E_BUILD_TEST_SENTINEL: 'caller-env-is-preserved',
  }
  return { testRoot, runnerTemp, fakeBin, capture, callerTmp, callerTemp, env }
}

function startRunner(env) {
  const child = spawn(process.execPath, [runner], { env, stdio: ['ignore', 'ignore', 'pipe'] })
  let stderr = ''
  child.stderr.setEncoding('utf8').on('data', (chunk) => { stderr += chunk })
  const closed = new Promise((resolveExit, reject) => {
    const timeout = setTimeout(() => {
      child.kill('SIGKILL')
      reject(new Error('runner did not exit within the test deadline'))
    }, 10000)
    child.once('error', (error) => { clearTimeout(timeout); reject(error) })
    child.once('close', (code, signal) => { clearTimeout(timeout); resolveExit({ code, signal }) })
  })
  return { child, closed, get stderr() { return stderr } }
}

function trackRunner(t, env) {
  const runnerProcess = startRunner(env)
  t.after(async () => {
    if (runnerProcess.child.exitCode !== null || runnerProcess.child.signalCode !== null) return
    runnerProcess.child.kill('SIGTERM')
    let timeout
    await Promise.race([
      runnerProcess.closed.catch(() => {}),
      new Promise((done) => { timeout = setTimeout(done, 7000) }),
    ])
    clearTimeout(timeout)
    if (runnerProcess.child.exitCode === null && runnerProcess.child.signalCode === null) runnerProcess.child.kill('SIGKILL')
  })
  return runnerProcess
}

async function readCapture(path) {
  return JSON.parse(await readFileAsync(path, 'utf8'))
}

async function waitForCapture(path) {
  const deadline = Date.now() + 5000
  while (Date.now() < deadline) {
    try { return await readCapture(path) } catch {}
    await new Promise((done) => setTimeout(done, 20))
  }
  throw new Error('fake npm did not capture its build environment')
}

async function assertBuildIsolation(fixture, captured) {
  assert.deepEqual(captured.argv, ['run', 'build'])
  assert.ok(captured.tmpdir.startsWith(join(frontend, scratchPrefix)))
  assert.ok(basename(captured.tmpdir).length > scratchPrefix.length, 'build scratch should have a unique suffix')
  assert.equal(captured.scratchMode, 0o700)
  assert.equal(captured.scratchDev, captured.frontendDev)
  assert.equal(captured.tmp, fixture.callerTmp)
  assert.equal(captured.temp, fixture.callerTemp)
  assert.equal(captured.sentinel, 'caller-env-is-preserved')
  assert.ok(captured.runDir.startsWith(fixture.runnerTemp + sep))
  assert.equal(captured.runDirMode, 0o700)
  for (const privatePath of [captured.database, captured.media, captured.emailCommand, captured.emailCapture, captured.playwrightOutput, captured.playwrightDist]) {
    assert.ok(privatePath.startsWith(fixture.runnerTemp + sep), `${privatePath} should remain under OS temp`)
  }
  assert.equal(await stat(captured.tmpdir).then(() => true, () => false), false, 'build scratch should be removed')
  assert.deepEqual(await readdir(fixture.runnerTemp), [], 'private run directory should be removed')
  const frontendIgnore = await readFile(join(frontend, '.gitignore'), 'utf8')
  const dockerIgnore = await readFile(join(repository, '.dockerignore'), 'utf8')
  assert.ok(frontendIgnore.split(/\r?\n/).includes(`${scratchPrefix}*/`))
  assert.ok(dockerIgnore.split(/\r?\n/).includes(`coobook_front-end/${scratchPrefix}*/`))
  const scratchRelative = relative(repository, captured.tmpdir).split(sep).join('/')
  assert.ok(scratchRelative.startsWith(`coobook_front-end/${scratchPrefix}`))
}

test('cleans private and build scratch after the build command fails', async (t) => {
  const fixture = await makeFixture(t)
  const before = new Set((await readdir(frontend)).filter((name) => name.startsWith(scratchPrefix)))
  const runnerProcess = trackRunner(t, fixture.env)
  const result = await runnerProcess.closed
  assert.equal(result.signal, null)
  assert.equal(result.code, 1)
  assert.match(runnerProcess.stderr, /npm exited with 17/)
  const captured = await readCapture(fixture.capture)
  await assertBuildIsolation(fixture, captured)
  const after = new Set((await readdir(frontend)).filter((name) => name.startsWith(scratchPrefix)))
  assert.deepEqual(after, before, 'runner should remove only its own build scratch')
})

test('cleans build scratch after the build succeeds and a later step fails', async (t) => {
  const fixture = await makeFixture(t, { mode: 'success' })
  const runnerProcess = trackRunner(t, fixture.env)
  const result = await runnerProcess.closed
  assert.equal(result.signal, null)
  assert.equal(result.code, 1)
  assert.match(runnerProcess.stderr, /go could not start|ENOENT/)
  await assertBuildIsolation(fixture, await readCapture(fixture.capture))
})

test('cleans build scratch when the runner receives SIGTERM during build', async (t) => {
  const fixture = await makeFixture(t, { mode: 'signal' })
  const runnerProcess = trackRunner(t, fixture.env)
  const captured = await waitForCapture(fixture.capture)
  assert.ok(await stat(captured.tmpdir).then(() => true, () => false), 'build scratch should exist while npm runs')
  runnerProcess.child.kill('SIGTERM')
  const result = await runnerProcess.closed
  assert.equal(result.code, 143)
  assert.equal(result.signal, null)
  await assertBuildIsolation(fixture, captured)
})

test('cleans its temporary run directory when a command cannot be spawned', async (t) => {
  const fixture = await makeFixture(t)
  const emptyPath = join(fixture.testRoot, 'empty-path')
  await mkdir(emptyPath)
  fixture.env.PATH = emptyPath
  const before = new Set((await readdir(frontend)).filter((name) => name.startsWith(scratchPrefix)))
  const runnerProcess = trackRunner(t, fixture.env)
  const result = await runnerProcess.closed
  assert.equal(result.signal, null)
  assert.equal(result.code, 1)
  assert.match(runnerProcess.stderr, /npm could not start/)
  assert.doesNotMatch(runnerProcess.stderr, /Unhandled 'error' event/)
  assert.deepEqual(await readdir(fixture.runnerTemp), [])
  const after = new Set((await readdir(frontend)).filter((name) => name.startsWith(scratchPrefix)))
  assert.deepEqual(after, before, 'runner should not touch unrelated build scratch')
})
