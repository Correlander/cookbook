import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdir, mkdtemp, readdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const runner = fileURLToPath(new URL('./run-e2e.mjs', import.meta.url))

test('cleans its temporary run directory when a command cannot be spawned', async () => {
  const testRoot = await mkdtemp(join(tmpdir(), 'cookbook-run-e2e-test-'))
  const runnerTemp = join(testRoot, 'runner-temp')
  const emptyPath = join(testRoot, 'empty-path')
  await Promise.all([mkdir(runnerTemp), mkdir(emptyPath)])
  let child
  let stderr = ''
  try {
    child = spawn(process.execPath, [runner], {
      env: { ...process.env, PATH: emptyPath, TMPDIR: runnerTemp },
      stdio: ['ignore', 'ignore', 'pipe'],
    })
    child.stderr.setEncoding('utf8').on('data', (chunk) => { stderr += chunk })
    const result = await new Promise((resolveExit, reject) => {
      const timeout = setTimeout(() => {
        child.kill('SIGKILL')
        reject(new Error('runner did not fail promptly after a spawn error'))
      }, 5000)
      child.once('error', (error) => { clearTimeout(timeout); reject(error) })
      child.once('close', (code, signal) => { clearTimeout(timeout); resolveExit({ code, signal }) })
    })
    assert.equal(result.signal, null)
    assert.equal(result.code, 1)
    assert.match(stderr, /npm could not start/)
    assert.doesNotMatch(stderr, /Unhandled 'error' event/)
    assert.deepEqual(await readdir(runnerTemp), [])
  } finally {
    if (child && child.exitCode === null && child.signalCode === null) child.kill('SIGKILL')
    await rm(testRoot, { recursive: true, force: true })
  }
})
