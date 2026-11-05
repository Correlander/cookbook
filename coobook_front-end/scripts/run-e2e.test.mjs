import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { chmod, copyFile, mkdtemp, mkdir, readFile, readFile as readFileAsync, readdir, rm, stat, writeFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { tmpdir } from 'node:os'
import { basename, join, relative, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const sourceRunner = fileURLToPath(new URL('./run-e2e.mjs', import.meta.url))
const sourceFrontend = fileURLToPath(new URL('..', import.meta.url))
const scratchPrefix = '.cookbook-e2e-build-'
const protectedDist = join(sourceFrontend, 'dist')
const protectedDistBefore = await hashTree(protectedDist)

async function hashTree(path, relativePath = '') {
  const entries = await readdir(path, { withFileTypes: true }).catch((error) => {
    if (error?.code === 'ENOENT') return null
    throw error
  })
  if (entries === null) return relativePath === '' ? null : ''
  const hash = createHash('sha256')
  for (const entry of entries.sort((left, right) => left.name.localeCompare(right.name))) {
    const childRelative = join(relativePath, entry.name)
    const childPath = join(path, entry.name)
    if (entry.isDirectory()) hash.update(childRelative).update(await hashTree(childPath, childRelative))
    else if (entry.isFile()) hash.update(childRelative).update(await readFile(childPath))
    else if (entry.isSymbolicLink()) hash.update(childRelative).update(await readFile(childPath).catch(() => Buffer.from('broken-link')))
  }
  return hash.digest('hex')
}

test.after(async () => {
  assert.equal(await hashTree(protectedDist), protectedDistBefore, 'runner tests must not modify the real frontend dist output')
})

async function makeFixture(t, { mode = 'fail', createGo = false, failGoBuild = false } = {}) {
  const testRoot = await mkdtemp(join(tmpdir(), 'cookbook-run-e2e-test-'))
  const project = join(testRoot, 'isolated project')
  const frontend = join(project, 'coobook_front-end')
  const scripts = join(frontend, 'scripts')
  const dist = join(frontend, 'dist')
  const runner = join(scripts, 'run-e2e.mjs')
  const runnerTemp = join(testRoot, 'runner-temp')
  const fakeBin = join(testRoot, 'fake-bin')
  const capture = join(testRoot, 'build-capture.json')
  const callerTmp = join(testRoot, 'caller-tmp')
  const callerTemp = join(testRoot, 'caller-temp')
  await Promise.all([mkdir(runnerTemp), mkdir(fakeBin), mkdir(callerTmp), mkdir(callerTemp), mkdir(scripts, { recursive: true }), mkdir(dist, { recursive: true }), mkdir(join(project, 'cookbook_back-end'), { recursive: true })])
  await Promise.all([
    copyFile(sourceRunner, runner),
    copyFile(join(sourceFrontend, 'scripts/static-host.mjs'), join(scripts, 'static-host.mjs')),
    copyFile(join(sourceFrontend, 'scripts/static-routes.mjs'), join(scripts, 'static-routes.mjs')),
    writeFile(join(dist, 'index.html'), '<!doctype html><html><body>owned fixture</body></html>'),
    readFile(join(sourceFrontend, '.gitignore'), 'utf8').then((content) => writeFile(join(frontend, '.gitignore'), content)),
    readFile(join(fileURLToPath(new URL('../..', import.meta.url)), '.dockerignore'), 'utf8').then((content) => writeFile(join(project, '.dockerignore'), content)),
  ])
  const fakeNpm = join(fakeBin, 'npm')
  const fakeGo = join(fakeBin, 'go')
  const goCapture = join(testRoot, 'go-capture.json')
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
  const fakeApiScript = (marker) => `#!${process.execPath}
import { writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
writeFileSync(${JSON.stringify(marker)}, JSON.stringify({ cwd: process.cwd() }))
createServer((_request, response) => { response.writeHead(200, { 'content-type': 'application/json' }); response.end('{"status":"ok"}') }).listen(Number(process.env.COOKBOOK_ADDR.split(':').at(-1)), '127.0.0.1')
`
  const fakeGoScript = `#!${process.execPath}
import { existsSync, readFileSync, writeFileSync, chmodSync } from 'node:fs'
const argv = process.argv.slice(2)
const capture = process.env.E2E_BUILD_TEST_GO_CAPTURE
const calls = existsSync(capture) ? JSON.parse(readFileSync(capture, 'utf8')) : []
calls.push({ cwd: process.cwd(), argv })
writeFileSync(capture, JSON.stringify(calls))
if (argv[0] === 'build') {
  if (process.env.E2E_BUILD_TEST_FAIL_GO_BUILD === '1') process.exit(91)
  const output = argv[argv.indexOf('-o') + 1]
  writeFileSync(output, ${JSON.stringify(fakeApiScript(join(testRoot, 'built-api-started.json')))})
  chmodSync(output, 0o700)
}
`
  await writeFile(fakeNpm, script, { mode: 0o700 })
  await chmod(fakeNpm, 0o700)
  if (createGo) {
    await writeFile(fakeGo, fakeGoScript, { mode: 0o700 })
    await chmod(fakeGo, 0o700)
  }
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
    E2E_BUILD_TEST_GO_CAPTURE: goCapture,
    E2E_BUILD_TEST_FAIL_GO_BUILD: failGoBuild ? '1' : '0',
    E2E_BUILD_TEST_RUNNER: runner,
    E2E_BUILD_TEST_FRONTEND: frontend,
    E2E_BUILD_TEST_MODE: mode,
    E2E_BUILD_TEST_SENTINEL: 'caller-env-is-preserved',
  }
  return { testRoot, project, frontend, runner, runnerTemp, fakeBin, capture, callerTmp, callerTemp, goCapture, fakeApiScript, env }
}

function startRunner(env) {
  const child = spawn(process.execPath, [env.E2E_BUILD_TEST_RUNNER], { env, stdio: ['ignore', 'ignore', 'pipe'] })
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

async function waitForGoCalls(path, count) {
  const deadline = Date.now() + 5000
  while (Date.now() < deadline) {
    try {
      const calls = await readCapture(path)
      if (calls.length >= count) return calls
    } catch {}
    await new Promise((done) => setTimeout(done, 20))
  }
  throw new Error(`fake Go command did not record ${count} calls`)
}

async function createBackendSource(fixture) {
  const sourceDir = join(fixture.testRoot, 'trusted backend source')
  await mkdir(sourceDir)
  for (const marker of ['go.mod', 'main.go', 'api.go', 'e2e_admin_fixture_test.go']) {
    await writeFile(join(sourceDir, marker), `// fixture marker: ${marker}\n`)
  }
  return sourceDir
}

async function assertBuildIsolation(fixture, captured) {
  assert.deepEqual(captured.argv, ['run', 'build'])
  assert.ok(captured.tmpdir.startsWith(join(fixture.frontend, scratchPrefix)))
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
  const frontendIgnore = await readFile(join(fixture.frontend, '.gitignore'), 'utf8')
  const dockerIgnore = await readFile(join(fixture.project, '.dockerignore'), 'utf8')
  assert.ok(frontendIgnore.split(/\r?\n/).includes(`${scratchPrefix}*/`))
  assert.ok(dockerIgnore.split(/\r?\n/).includes(`coobook_front-end/${scratchPrefix}*/`))
  const scratchRelative = relative(fixture.project, captured.tmpdir).split(sep).join('/')
  assert.ok(scratchRelative.startsWith(`coobook_front-end/${scratchPrefix}`))
}

test('cleans private and build scratch after the build command fails', async (t) => {
  const fixture = await makeFixture(t)
  const before = new Set((await readdir(fixture.frontend)).filter((name) => name.startsWith(scratchPrefix)))
  const runnerProcess = trackRunner(t, fixture.env)
  const result = await runnerProcess.closed
  assert.equal(result.signal, null)
  assert.equal(result.code, 1)
  assert.match(runnerProcess.stderr, /npm exited with 17/)
  const captured = await readCapture(fixture.capture)
  await assertBuildIsolation(fixture, captured)
  const after = new Set((await readdir(fixture.frontend)).filter((name) => name.startsWith(scratchPrefix)))
  assert.deepEqual(after, before, 'runner should remove only its own build scratch')
})

test('cleans build scratch after the build succeeds and a later step fails', async (t) => {
  const fixture = await makeFixture(t, { mode: 'success', createGo: true, failGoBuild: true })
  const runnerProcess = trackRunner(t, fixture.env)
  const result = await runnerProcess.closed
  assert.equal(result.signal, null)
  assert.equal(result.code, 1)
  assert.match(runnerProcess.stderr, /go exited with 91/)
  const calls = await readCapture(fixture.goCapture)
  assert.equal(calls.length, 1)
  assert.equal(calls[0].cwd, join(fixture.project, 'cookbook_back-end'), 'default build should use the runner project backend directory')
  assert.deepEqual(calls[0].argv.slice(0, 2), ['build', '-mod=mod'])
  assert.equal(calls[0].argv[2], '-o')
  assert.ok(calls[0].argv[3].startsWith(fixture.runnerTemp + sep))
  assert.equal(calls[0].argv[4], '.')
  await assertBuildIsolation(fixture, await readCapture(fixture.capture))
})

test('uses a supplied executable path and paired source for the Go fixture without building', async (t) => {
  const fixture = await makeFixture(t, { mode: 'success', createGo: true })
  const sourceDir = await createBackendSource(fixture)
  const apiDir = join(fixture.testRoot, 'trusted local artifact')
  await mkdir(apiDir)
  const selectedApi = join(apiDir, 'cookbook api')
  const selectedMarker = join(fixture.testRoot, 'selected-api.json')
  await writeFile(selectedApi, fixture.fakeApiScript(selectedMarker), { mode: 0o700 })
  await chmod(selectedApi, 0o700)
  fixture.env.E2E_API_BINARY = selectedApi
  fixture.env.E2E_BACKEND_SOURCE_DIR = sourceDir

  const runnerProcess = trackRunner(t, fixture.env)
  const calls = await waitForGoCalls(fixture.goCapture, 1)
  assert.equal(await stat(selectedMarker).then(() => true, () => false), true, 'runner should execute the exact supplied path')
  runnerProcess.child.kill('SIGTERM')
  const result = await runnerProcess.closed
  assert.equal(result.code, 143)
  assert.equal(result.signal, null)
  assert.equal(calls.length, 1)
  assert.equal(calls[0].cwd, sourceDir, 'admin fixture must compile from the supplied backend source')
  assert.equal(calls[0].argv[0], 'test')
  assert.deepEqual(await readdir(fixture.runnerTemp), [], 'private run directory should be removed')
  const captured = await readCapture(fixture.capture)
  await assertBuildIsolation(fixture, captured)
})

test('builds the API and admin fixture from a supplied source directory', async (t) => {
  const fixture = await makeFixture(t, { mode: 'success', createGo: true })
  const sourceDir = await createBackendSource(fixture)
  const apiMarker = join(fixture.testRoot, 'built-api-started.json')
  fixture.env.E2E_BACKEND_SOURCE_DIR = sourceDir

  const runnerProcess = trackRunner(t, fixture.env)
  const calls = await waitForGoCalls(fixture.goCapture, 2)
  assert.equal(await stat(apiMarker).then(() => true, () => false), true, 'API built by the source-directory Go command should start')
  runnerProcess.child.kill('SIGTERM')
  const result = await runnerProcess.closed
  assert.equal(result.code, 143)
  assert.equal(result.signal, null)
  assert.deepEqual(calls.map(({ cwd }) => cwd), [sourceDir, sourceDir], 'both Go commands must use the supplied source directory')
  assert.deepEqual(calls.map(({ argv }) => argv[0]), ['build', 'test'])
  assert.equal(calls[0].argv[1], '-mod=mod')
  assert.equal(calls[0].argv[2], '-o')
  assert.ok(calls[0].argv[3].startsWith(fixture.runnerTemp + sep), 'runner should own and clean the built binary')
  assert.match(calls[1].argv.join(' '), /TestPrepareE2EAdminActorFixture/)
  assert.deepEqual(await readdir(fixture.runnerTemp), [], 'private run directory and built API should be removed')
  await assertBuildIsolation(fixture, await readCapture(fixture.capture))
})

test('rejects an invalid supplied binary before creating private work', async (t) => {
  const relativeFixture = await makeFixture(t)
  relativeFixture.env.E2E_BACKEND_SOURCE_DIR = await createBackendSource(relativeFixture)
  relativeFixture.env.E2E_API_BINARY = 'relative/cookbook-api'
  const relativeRunner = trackRunner(t, relativeFixture.env)
  const relativeResult = await relativeRunner.closed
  assert.equal(relativeResult.code, 1)
  assert.match(relativeRunner.stderr, /E2E_API_BINARY must be an absolute path to an executable API binary/)
  assert.deepEqual(await readdir(relativeFixture.runnerTemp), [], 'relative path must fail before creating a run directory')
  assert.equal(await stat(relativeFixture.capture).then(() => true, () => false), false, 'invalid path must fail before building or starting workloads')

  const modeFixture = await makeFixture(t)
  modeFixture.env.E2E_BACKEND_SOURCE_DIR = await createBackendSource(modeFixture)
  const nonExecutable = join(modeFixture.testRoot, 'not-executable')
  await writeFile(nonExecutable, 'not executable')
  await chmod(nonExecutable, 0o600)
  modeFixture.env.E2E_API_BINARY = nonExecutable
  const modeRunner = trackRunner(t, modeFixture.env)
  const modeResult = await modeRunner.closed
  assert.equal(modeResult.code, 1)
  assert.match(modeRunner.stderr, /E2E_API_BINARY is not an executable file/)
  assert.deepEqual(await readdir(modeFixture.runnerTemp), [], 'non-executable file must fail before creating a run directory')
  assert.equal(await stat(modeFixture.capture).then(() => true, () => false), false, 'non-executable path must fail before building or starting workloads')
})

test('requires a source directory with a supplied API and validates source markers before private work', async (t) => {
  const missingPair = await makeFixture(t)
  missingPair.env.E2E_API_BINARY = '/absolute/trusted-api'
  const missingPairRunner = trackRunner(t, missingPair.env)
  const missingPairResult = await missingPairRunner.closed
  assert.equal(missingPairResult.code, 1)
  assert.match(missingPairRunner.stderr, /E2E_API_BINARY requires E2E_BACKEND_SOURCE_DIR/)
  assert.deepEqual(await readdir(missingPair.runnerTemp), [])
  assert.equal(await stat(missingPair.capture).then(() => true, () => false), false)

  const badSource = await makeFixture(t)
  badSource.env.E2E_BACKEND_SOURCE_DIR = join(badSource.testRoot, 'incomplete source')
  await mkdir(badSource.env.E2E_BACKEND_SOURCE_DIR)
  await writeFile(join(badSource.env.E2E_BACKEND_SOURCE_DIR, 'go.mod'), 'module incomplete\n')
  const badSourceRunner = trackRunner(t, badSource.env)
  const badSourceResult = await badSourceRunner.closed
  assert.equal(badSourceResult.code, 1)
  assert.match(badSourceRunner.stderr, /E2E_BACKEND_SOURCE_DIR is not a valid backend source directory/)
  assert.deepEqual(await readdir(badSource.runnerTemp), [])
  assert.equal(await stat(badSource.capture).then(() => true, () => false), false)
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
  const before = new Set((await readdir(fixture.frontend)).filter((name) => name.startsWith(scratchPrefix)))
  const runnerProcess = trackRunner(t, fixture.env)
  const result = await runnerProcess.closed
  assert.equal(result.signal, null)
  assert.equal(result.code, 1)
  assert.match(runnerProcess.stderr, /npm could not start/)
  assert.doesNotMatch(runnerProcess.stderr, /Unhandled 'error' event/)
  assert.deepEqual(await readdir(fixture.runnerTemp), [])
  const after = new Set((await readdir(fixture.frontend)).filter((name) => name.startsWith(scratchPrefix)))
  assert.deepEqual(after, before, 'runner should not touch unrelated build scratch')
})
